package scancoord

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"knox-media/internal/store"
)

const (
	defaultProgressFlushInterval   = 500 * time.Millisecond
	defaultProgressFileThreshold   = 100
	defaultProgressLogBatchSize    = 100
	defaultProgressMaxBufferedLogs = 1000
)

type ScanLog struct {
	FilePath string
	Action   string
	Message  string
}

type ProgressWriterOptions struct {
	FlushInterval   time.Duration
	FileThreshold   int
	LogBatchSize    int
	MaxBufferedLogs int
	Now             func() time.Time
}

type progressLogEntry struct {
	id uint64
	ScanLog
}

type ProgressWriter struct {
	db        *sql.DB
	metrics   *store.SQLiteMetrics
	taskID    int64
	libraryID int64
	opts      ProgressWriterOptions

	mu                sync.Mutex
	processed         int64
	failed            int64
	added             int64
	flushedProcessed  int64
	flushedFailed     int64
	flushedAdded      int64
	lastProgressFlush time.Time
	lastLogFlush      time.Time
	logs              []progressLogEntry
	nextLogID         uint64
	flushing          bool
	flushDone         chan struct{}
	writeLogs         func(context.Context, []progressLogEntry) error
}

func NewProgressWriter(db *sql.DB, metrics *store.SQLiteMetrics, taskID, libraryID int64, opts ProgressWriterOptions) *ProgressWriter {
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultProgressFlushInterval
	}
	if opts.FileThreshold <= 0 {
		opts.FileThreshold = defaultProgressFileThreshold
	}
	if opts.MaxBufferedLogs <= 0 || opts.MaxBufferedLogs > defaultProgressMaxBufferedLogs {
		opts.MaxBufferedLogs = defaultProgressMaxBufferedLogs
	}
	if opts.LogBatchSize <= 0 {
		opts.LogBatchSize = defaultProgressLogBatchSize
	}
	if opts.LogBatchSize > defaultProgressLogBatchSize {
		opts.LogBatchSize = defaultProgressLogBatchSize
	}
	if opts.LogBatchSize > opts.MaxBufferedLogs {
		opts.LogBatchSize = opts.MaxBufferedLogs
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	now := opts.Now()
	w := &ProgressWriter{db: db, metrics: metrics, taskID: taskID, libraryID: libraryID, opts: opts, lastProgressFlush: now, lastLogFlush: now}
	w.writeLogs = w.insertLogBatch
	return w
}

func (w *ProgressWriter) File(path string, err error) {
	if w == nil {
		return
	}
	action, message := "processed", ""
	w.mu.Lock()
	w.processed++
	if err != nil {
		w.failed++
		action, message = "error", err.Error()
	}
	progressDue := w.processed-w.flushedProcessed >= int64(w.opts.FileThreshold) || w.opts.Now().Sub(w.lastProgressFlush) >= w.opts.FlushInterval
	w.mu.Unlock()
	w.appendLog(ScanLog{FilePath: path, Action: action, Message: message})
	if progressDue {
		w.flushSynchronously()
	}
}

func (w *ProgressWriter) MediaAdded(mediaID int64, title, fileType string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.added++
	w.mu.Unlock()
	w.appendLog(ScanLog{FilePath: fmt.Sprintf("media:%d", mediaID), Action: "added", Message: fmt.Sprintf("%s (%s)", title, fileType)})
}

func (w *ProgressWriter) Log(entry ScanLog) {
	if w != nil {
		w.appendLog(entry)
	}
}

func (w *ProgressWriter) appendLog(entry ScanLog) {
	for {
		w.mu.Lock()
		if len(w.logs) < w.opts.MaxBufferedLogs {
			w.nextLogID++
			w.logs = append(w.logs, progressLogEntry{id: w.nextLogID, ScanLog: entry})
			due := !w.flushing && (len(w.logs) >= w.opts.LogBatchSize || w.opts.Now().Sub(w.lastLogFlush) >= w.opts.FlushInterval)
			w.mu.Unlock()
			if due {
				w.flushSynchronously()
			}
			return
		}
		w.mu.Unlock()
		w.flushSynchronously()
	}
}

func (w *ProgressWriter) BufferedLogs() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.logs)
}

func (w *ProgressWriter) flushSynchronously() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Flush(ctx, false); err != nil {
		log.Printf("scan progress flush task=%d: %v", w.taskID, err)
	}
}

func (w *ProgressWriter) Flush(ctx context.Context, force bool) error {
	if w == nil || w.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		w.mu.Lock()
		if w.flushing {
			done := w.flushDone
			w.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		w.flushing = true
		w.flushDone = make(chan struct{})
		targetLogID := w.nextLogID
		targetProcessed, targetFailed, targetAdded := w.processed, w.failed, w.added
		w.mu.Unlock()

		err := w.flushOwned(ctx, force, targetLogID, targetProcessed, targetFailed, targetAdded)
		w.mu.Lock()
		w.flushing = false
		close(w.flushDone)
		w.mu.Unlock()
		return err
	}
}

func (w *ProgressWriter) flushOwned(ctx context.Context, force bool, targetLogID uint64, targetProcessed, targetFailed, targetAdded int64) error {
	now := w.opts.Now()
	w.mu.Lock()
	progressDirty := targetProcessed > w.flushedProcessed || targetFailed > w.flushedFailed || targetAdded > w.flushedAdded
	progressDue := force || targetProcessed-w.flushedProcessed >= int64(w.opts.FileThreshold) || now.Sub(w.lastProgressFlush) >= w.opts.FlushInterval
	w.mu.Unlock()
	if progressDirty && progressDue {
		if err := store.WithBusyRetry(ctx, w.metrics, func() error {
			_, err := w.db.ExecContext(ctx, `UPDATE scan_task SET processed_count=?,failed_count=?,added_count=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, targetProcessed, targetFailed, targetAdded, w.taskID)
			return err
		}); err != nil {
			return err
		}
		if w.metrics != nil {
			w.metrics.ProgressBatches.Add(1)
		}
		w.mu.Lock()
		w.flushedProcessed, w.flushedFailed, w.flushedAdded = targetProcessed, targetFailed, targetAdded
		w.lastProgressFlush = now
		w.mu.Unlock()
	}

	for {
		w.mu.Lock()
		count := 0
		for count < len(w.logs) && count < w.opts.LogBatchSize && w.logs[count].id <= targetLogID {
			count++
		}
		logDue := force || len(w.logs) >= w.opts.LogBatchSize || now.Sub(w.lastLogFlush) >= w.opts.FlushInterval
		if count == 0 || !logDue {
			w.mu.Unlock()
			return nil
		}
		batch := append([]progressLogEntry(nil), w.logs[:count]...)
		w.logs = append(w.logs[:0], w.logs[count:]...)
		w.mu.Unlock()

		err := w.writeLogs(ctx, batch)
		if err != nil {
			if w.metrics != nil {
				w.metrics.LogBatchFailures.Add(1)
				w.metrics.DroppedLogs.Add(uint64(len(batch)))
			}
			log.Printf("scan log batch task=%d rows=%d: %v", w.taskID, len(batch), err)
		} else if w.metrics != nil {
			w.metrics.LogBatches.Add(1)
		}

		w.mu.Lock()
		w.lastLogFlush = now
		w.mu.Unlock()
	}
}

func (w *ProgressWriter) insertLogBatch(ctx context.Context, batch []progressLogEntry) error {
	return store.WithBusyRetry(ctx, w.metrics, func() error {
		tx, err := w.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO scan_log(scan_task_id,library_id,file_path,action,message) VALUES(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, entry := range batch {
			if _, err = stmt.ExecContext(ctx, w.taskID, w.libraryID, entry.FilePath, entry.Action, entry.Message); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}
