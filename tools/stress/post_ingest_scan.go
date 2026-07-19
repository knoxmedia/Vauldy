package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/storage"
	"knox-media/internal/store"

	_ "modernc.org/sqlite"
)

type Options struct {
	Media         int
	HoldWriteLock time.Duration
	Timeout       time.Duration
	ExecutorDelay time.Duration
}

type Result struct {
	DirectFFmpeg                                     int
	GlobalLimit, PosterLimit, PreviewLimit           int
	PeakGlobal, PeakPoster, PeakPreview              int
	Duplicates                                       int
	BusyRetries, BusyExhausted                       uint64
	Statuses                                         map[string]int
	GoroutineBaseline, GoroutinePeak, GoroutineFinal int
}

type recordingExecutor struct {
	delay                               time.Duration
	mu                                  sync.Mutex
	running, poster, preview            int
	peakGlobal, peakPoster, peakPreview int
	seen                                map[string]int
	duplicates                          int
	executions                          int
}

func (e *recordingExecutor) Execute(ctx context.Context, task postingest.Task) error {
	key := fmt.Sprintf("%d/%s", task.MediaID, task.Type)
	e.mu.Lock()
	e.seen[key]++
	e.executions++
	if e.seen[key] > 1 {
		e.duplicates++
	}
	e.running++
	if task.Type == postingest.TaskPoster {
		e.poster++
	}
	if task.Type == postingest.TaskPreview {
		e.preview++
	}
	e.peakGlobal = max(e.peakGlobal, e.running)
	e.peakPoster = max(e.peakPoster, e.poster)
	e.peakPreview = max(e.peakPreview, e.preview)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running--
		if task.Type == postingest.TaskPoster {
			e.poster--
		}
		if task.Type == postingest.TaskPreview {
			e.preview--
		}
		e.mu.Unlock()
	}()
	timer := time.NewTimer(e.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *recordingExecutor) executionStarts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executions
}

func (e *recordingExecutor) snapshot() (int, int, int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.peakGlobal, e.peakPoster, e.peakPreview, e.duplicates
}

func run(parent context.Context, options Options) (result Result, returnErr error) {
	if options.Media <= 0 {
		return result, errors.New("media must be positive")
	}
	if options.Timeout <= 0 {
		return result, errors.New("timeout must be positive")
	}
	if options.HoldWriteLock < 0 {
		return result, errors.New("hold-write-lock must not be negative")
	}
	if options.ExecutorDelay <= 0 {
		options.ExecutorDelay = 15 * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	result.Statuses = map[string]int{"waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0}
	result.GoroutineBaseline = runtime.NumGoroutine()
	result.GoroutinePeak = result.GoroutineBaseline
	defer func() {
		for i := 0; i < 20; i++ {
			runtime.GC()
			result.GoroutineFinal = runtime.NumGoroutine()
			if result.GoroutineFinal <= result.GoroutineBaseline+10 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	tempDir, err := os.MkdirTemp("", "knox-post-ingest-stress-")
	if err != nil {
		return result, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	dbPath := tempDir + string(os.PathSeparator) + "stress.db"
	setupDB, err := store.OpenSQLite(dbPath)
	if err != nil {
		return result, fmt.Errorf("open schema database: %w", err)
	}
	if err := setupDB.Close(); err != nil {
		return result, fmt.Errorf("close schema database: %w", err)
	}

	dsn := "file:" + dbPath + "?_pragma=busy_timeout(1)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return result, fmt.Errorf("open runtime database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return result, fmt.Errorf("ping runtime database: %w", err)
	}
	lockDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return result, fmt.Errorf("open lock database: %w", err)
	}
	lockDB.SetMaxOpenConns(1)
	defer lockDB.Close()

	libraryResult, err := db.ExecContext(ctx, `INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled) VALUES('stress','video',?,1,1)`, tempDir)
	if err != nil {
		return result, fmt.Errorf("insert library: %w", err)
	}
	libraryID, err := libraryResult.LastInsertId()
	if err != nil {
		return result, fmt.Errorf("library id: %w", err)
	}
	scanResult, err := db.ExecContext(ctx, `INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		return result, fmt.Errorf("insert scan task: %w", err)
	}
	scanTaskID, err := scanResult.LastInsertId()
	if err != nil {
		return result, fmt.Errorf("scan task id: %w", err)
	}
	mediaIDs := make([]int64, 0, options.Media)
	for i := 0; i < options.Media; i++ {
		row, insertErr := db.ExecContext(ctx, `INSERT INTO media(library_id,file_id,file_path,title,file_type,duration) VALUES(?,?,?,?, 'video',120)`, libraryID, fmt.Sprintf("stress-%06d", i), fmt.Sprintf("stress-%06d.mp4", i), fmt.Sprintf("Stress %06d", i))
		if insertErr != nil {
			return result, fmt.Errorf("insert media %d: %w", i, insertErr)
		}
		id, idErr := row.LastInsertId()
		if idErr != nil {
			return result, fmt.Errorf("media %d id: %w", i, idErr)
		}
		mediaIDs = append(mediaIDs, id)
	}

	metrics := &store.SQLiteMetrics{}
	cfg := &config.Config{}
	enqueuer := postingest.NewEnqueuer(db, cfg, metrics)
	owner := "stress-dispatcher"
	queue := postingest.NewQueue(db, owner, metrics)
	executor := &recordingExecutor{delay: options.ExecutorDelay, seen: make(map[string]int)}
	dispatcherOptions := postingest.DefaultDispatcherOptions()
	dispatcherOptions.OwnerID = owner
	dispatcherOptions.PollInterval = 5 * time.Millisecond
	dispatcherOptions.HeartbeatInterval = time.Second
	dispatcherOptions.ExecutorStopGrace = time.Second
	dispatcher, err := postingest.NewDispatcher(queue, executor, dispatcherOptions)
	if err != nil {
		return result, fmt.Errorf("new dispatcher: %w", err)
	}
	result.GlobalLimit = dispatcherOptions.Global
	result.PosterLimit = dispatcherOptions.Poster
	result.PreviewLimit = dispatcherOptions.Preview

	dispatchCtx, stopDispatcher := context.WithCancel(ctx)
	dispatchDone := make(chan error, 1)
	defer func() {
		stopDispatcher()
		select {
		case dispatchErr := <-dispatchDone:
			if returnErr == nil && dispatchErr != nil {
				returnErr = fmt.Errorf("dispatcher shutdown: %w", dispatchErr)
			}
		case <-time.After(15 * time.Second):
			if returnErr == nil {
				returnErr = errors.New("dispatcher did not stop")
			}
		}
	}()

	lockerCtx, stopLocker := context.WithCancel(ctx)
	lockerDone := make(chan struct{})
	if options.HoldWriteLock > 0 {
		go func() {
			defer close(lockerDone)
			for lockerCtx.Err() == nil {
				conn, connErr := lockDB.Conn(lockerCtx)
				if connErr != nil {
					return
				}
				if _, beginErr := conn.ExecContext(lockerCtx, `BEGIN IMMEDIATE`); beginErr == nil {
					timer := time.NewTimer(options.HoldWriteLock)
					select {
					case <-timer.C:
					case <-lockerCtx.Done():
					}
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					_, _ = conn.ExecContext(context.Background(), `COMMIT`)
				}
				conn.Close()
				idle := 4 * options.HoldWriteLock
				if idle < 50*time.Millisecond {
					idle = 50 * time.Millisecond
				}
				select {
				case <-time.After(idle):
				case <-lockerCtx.Done():
				}
			}
		}()
	} else {
		close(lockerDone)
	}
	defer func() { stopLocker(); <-lockerDone }()

	var goroutinePeak atomic.Int64
	goroutinePeak.Store(int64(result.GoroutineBaseline))
	sampleStop := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := int64(runtime.NumGoroutine())
				for old := goroutinePeak.Load(); current > old && !goroutinePeak.CompareAndSwap(old, current); old = goroutinePeak.Load() {
				}
			case <-sampleStop:
				return
			}
		}
	}()
	stopSampler := sync.OnceFunc(func() {
		close(sampleStop)
		<-sampleDone
		result.GoroutinePeak = int(goroutinePeak.Load())
	})
	defer stopSampler()

	scanCallback := postingest.NewScanMediaAddedEnqueueCallback(enqueuer)
	ffmpegBefore := storage.FFmpegLaunchCount()
	for _, mediaID := range mediaIDs {
		for {
			enqueueErr := scanCallback(ctx, scanTaskID, mediaID, "", "video")
			if enqueueErr == nil {
				break
			}
			if ctx.Err() != nil {
				return result, fmt.Errorf("enqueue media %d: %w", mediaID, ctx.Err())
			}
			if !store.IsSQLiteBusy(enqueueErr) {
				return result, fmt.Errorf("enqueue media %d: %w", mediaID, enqueueErr)
			}
		}
		err := scanCallback(ctx, scanTaskID, mediaID, "", "video")
		if err != nil && !store.IsSQLiteBusy(err) {
			return result, fmt.Errorf("idempotent enqueue media %d: %w", mediaID, err)
		}
	}
	result.DirectFFmpeg = int(storage.FFmpegLaunchCount() - ffmpegBefore)

	go func() { dispatchDone <- dispatcher.Start(dispatchCtx) }()

	expected := options.Media * 6
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		statuses, statusErr := loadStatuses(ctx, db)
		if statusErr == nil {
			result.Statuses = statuses
			if statuses["done"]+statuses["failed"]+statuses["cancelled"] == expected && statuses["waiting"] == 0 && statuses["running"] == 0 {
				break
			}
		} else if !store.IsSQLiteBusy(statusErr) {
			return result, fmt.Errorf("load statuses: %w", statusErr)
		}
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("workload did not converge before timeout: statuses=%v: %w", result.Statuses, ctx.Err())
		case <-poll.C:
		}
	}

	stopLocker()
	<-lockerDone
	beforeStable := metrics.BusyExhausted.Load()
	select {
	case <-time.After(250 * time.Millisecond):
	case <-ctx.Done():
		return result, ctx.Err()
	}
	afterStable := metrics.BusyExhausted.Load()
	result.BusyRetries = metrics.BusyRetries.Load()
	result.BusyExhausted = afterStable
	result.PeakGlobal, result.PeakPoster, result.PeakPreview, _ = executor.snapshot()
	result.Duplicates, _, err = loadDuplicateTasks(ctx, db)
	if err != nil {
		return result, fmt.Errorf("load duplicate tasks: %w", err)
	}
	currentGoroutines := int64(runtime.NumGoroutine())
	for old := goroutinePeak.Load(); currentGoroutines > old && !goroutinePeak.CompareAndSwap(old, currentGoroutines); old = goroutinePeak.Load() {
	}
	stopSampler()

	if err := validateResult(result, expected, beforeStable, afterStable); err != nil {
		return result, err
	}
	return result, nil
}

func validateResult(result Result, expected int, busyBeforeStable, busyAfterStable uint64) error {
	if result.DirectFFmpeg != 0 {
		return fmt.Errorf("direct ffmpeg starts=%d", result.DirectFFmpeg)
	}
	if result.PeakGlobal > result.GlobalLimit || result.PeakPoster > result.PosterLimit || result.PeakPreview > result.PreviewLimit {
		return fmt.Errorf("budget exceeded: global=%d/%d poster=%d/%d preview=%d/%d", result.PeakGlobal, result.GlobalLimit, result.PeakPoster, result.PosterLimit, result.PeakPreview, result.PreviewLimit)
	}
	if result.Duplicates != 0 {
		return fmt.Errorf("duplicate database tasks=%d", result.Duplicates)
	}
	if result.Statuses["done"] != expected || result.Statuses["waiting"] != 0 || result.Statuses["running"] != 0 || result.Statuses["failed"] != 0 || result.Statuses["cancelled"] != 0 {
		return fmt.Errorf("non-converged statuses=%v, want done=%d", result.Statuses, expected)
	}
	if busyAfterStable != busyBeforeStable {
		return fmt.Errorf("busy exhausted continued after lock stopped: before=%d after=%d", busyBeforeStable, busyAfterStable)
	}
	if result.GoroutineFinal > result.GoroutineBaseline+10 {
		return fmt.Errorf("goroutines did not converge: baseline=%d final=%d peak=%d", result.GoroutineBaseline, result.GoroutineFinal, result.GoroutinePeak)
	}
	return nil
}

func loadDuplicateTasks(ctx context.Context, db *sql.DB) (duplicates, total int, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) - COUNT(DISTINCT printf('%d:%s',media_id,task_type)), COUNT(*)
		FROM post_ingest_task`).Scan(&duplicates, &total)
	return duplicates, total, err
}

func loadStatuses(ctx context.Context, db *sql.DB) (map[string]int, error) {
	statuses := map[string]int{"waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0}
	rows, err := db.QueryContext(ctx, `SELECT status,COUNT(*) FROM post_ingest_task GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		statuses[status] = count
	}
	return statuses, rows.Err()
}

func main() {
	media := flag.Int("media", 100, "number of media rows")
	holdWriteLock := flag.Duration("hold-write-lock", 0, "periodic SQLite writer-lock hold duration")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall workload timeout")
	flag.Parse()
	result, err := run(context.Background(), Options{Media: *media, HoldWriteLock: *holdWriteLock, Timeout: *timeout})
	fmt.Printf("direct_ffmpeg=%d peak_global=%d/%d peak_poster=%d/%d peak_preview=%d/%d duplicates=%d busy_retries=%d busy_exhausted=%d statuses=%v goroutines=%d/%d/%d\n", result.DirectFFmpeg, result.PeakGlobal, result.GlobalLimit, result.PeakPoster, result.PosterLimit, result.PeakPreview, result.PreviewLimit, result.Duplicates, result.BusyRetries, result.BusyExhausted, result.Statuses, result.GoroutineBaseline, result.GoroutinePeak, result.GoroutineFinal)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}
