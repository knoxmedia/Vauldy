package postingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"knox-media/internal/storage"
)

type Executor interface {
	Execute(context.Context, Task) error
}
type CompletionDisposition uint8

const (
	CompleteThroughQueue CompletionDisposition = iota
	AlreadyCommittedAtomically
	FinalizationOutcomeUncertain
)

type ExecutionResult struct{ Completion CompletionDisposition }
type resultExecutor interface {
	ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
}

func executeTask(ctx context.Context, executor Executor, task Task) (ExecutionResult, error) {
	if atomic, ok := executor.(resultExecutor); ok {
		return atomic.ExecuteWithResult(ctx, task)
	}
	return ExecutionResult{Completion: CompleteThroughQueue}, executor.Execute(ctx, task)
}

type ClassifiedError struct {
	Kind FailureKind
	Err  error
}

func (e ClassifiedError) Error() string {
	if e.Err == nil {
		return "post-ingest classified error"
	}
	return e.Err.Error()
}
func (e ClassifiedError) Unwrap() error { return e.Err }

type BudgetSnapshot struct {
	GlobalLimit, GlobalUsed, PosterLimit, PosterUsed, PreviewLimit, PreviewUsed, SubtitleLimit, SubtitleUsed int
}
type DispatcherOptions struct {
	OwnerID                                                         string
	Global, Poster, Preview, Subtitle                               int
	SubtitleTimeoutRealtimeFactor                                   float64
	PollInterval, LeaseDuration, HeartbeatInterval, RecoverInterval time.Duration
	ExecutorStopGrace                                               time.Duration
	Timeouts                                                        map[TaskType]time.Duration
}

func DefaultDispatcherOptions() DispatcherOptions {
	global := runtime.NumCPU() / 2
	if global < 2 {
		global = 2
	}
	if global > 4 {
		global = 4
	}
	return DispatcherOptions{Global: global, Poster: min(2, global), Preview: 1, Subtitle: 1, SubtitleTimeoutRealtimeFactor: 2.0, PollInterval: 250 * time.Millisecond, LeaseDuration: leaseDuration, HeartbeatInterval: 30 * time.Second, RecoverInterval: 30 * time.Second, ExecutorStopGrace: 10 * time.Second, Timeouts: map[TaskType]time.Duration{
		TaskPoster: 2 * time.Minute, TaskPosterRepair: 2 * time.Minute, TaskThumbnail: 2 * time.Minute, TaskPreview: 30 * time.Minute, TaskKeyframe: 30 * time.Minute, TaskSubtitle: 60 * time.Minute, TaskSubtitleRecognize: 60 * time.Minute, TaskAIAnalysis: 15 * time.Minute, TaskAtrack: 30 * time.Minute, TaskEncrypt: 120 * time.Minute,
	}}
}

type workerState struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	kind   FailureKind
	cause  error
}

func (w *workerState) stop(kind FailureKind, cause error) {
	w.mu.Lock()
	if w.cause == nil {
		w.kind = kind
		w.cause = cause
	}
	w.mu.Unlock()
	w.cancel()
}
func (w *workerState) reason() (FailureKind, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.kind, w.cause
}

type Dispatcher struct {
	q                                                 *Queue
	executor                                          Executor
	opts                                              DispatcherOptions
	global, poster, preview, subtitle                 chan struct{}
	mu                                                sync.Mutex
	globalUsed, posterUsed, previewUsed, subtitleUsed int
	highPriorityUsed                                  int
	bandNext                                          []int
	running                                           map[int64]*workerState
	sourceLookupBudget                                time.Duration
	sourceSize                                        func(context.Context, Task) int64
	mediaDuration                                     func(context.Context, Task) int64
	sourceLookups                                     chan struct{}
	scans                                             map[int64]map[int64]*workerState
	wg                                                sync.WaitGroup
	startMu                                           sync.Mutex
	started                                           bool
	beforeRegister                                    func(Task)
	beforeRun                                         func(Task)
}

func NewDispatcher(q *Queue, executor Executor, opts DispatcherOptions) (*Dispatcher, error) {
	if q == nil {
		return nil, errors.New("Dispatcher.Queue is required")
	}
	if executor == nil {
		return nil, errors.New("Dispatcher.Executor is required")
	}
	if strings.TrimSpace(opts.OwnerID) == "" || opts.OwnerID != strings.TrimSpace(opts.OwnerID) || strings.Contains(opts.OwnerID, "/") {
		return nil, fmt.Errorf("Dispatcher.OwnerID is invalid")
	}
	if q.owner != opts.OwnerID {
		return nil, fmt.Errorf("Dispatcher.OwnerID must match Queue owner")
	}
	if opts.Global < 1 || opts.Global > 32 {
		return nil, fmt.Errorf("Dispatcher.Global must be in [1,32]")
	}
	if opts.Poster < 1 || opts.Poster > 2 || opts.Poster > opts.Global {
		return nil, fmt.Errorf("Dispatcher.Poster must be in [1,2] and <= Global")
	}
	if opts.Preview < 1 || opts.Preview > 2 || opts.Preview > opts.Global {
		return nil, fmt.Errorf("Dispatcher.Preview must be in [1,2] and <= Global")
	}
	if opts.Subtitle < 1 || opts.Subtitle > opts.Global {
		return nil, fmt.Errorf("Dispatcher.Subtitle must be in [1,Global]")
	}
	if opts.SubtitleTimeoutRealtimeFactor <= 0 || opts.SubtitleTimeoutRealtimeFactor > 10 {
		return nil, fmt.Errorf("Dispatcher.SubtitleTimeoutRealtimeFactor must be in (0,10]")
	}
	if opts.PollInterval <= 0 {
		return nil, fmt.Errorf("Dispatcher.PollInterval must be positive")
	}
	if opts.LeaseDuration != leaseDuration {
		return nil, fmt.Errorf("Dispatcher.LeaseDuration must equal Queue lease duration %v", leaseDuration)
	}
	if opts.ExecutorStopGrace <= 0 {
		return nil, fmt.Errorf("Dispatcher.ExecutorStopGrace must be positive")
	}
	if opts.HeartbeatInterval <= 0 || opts.HeartbeatInterval >= opts.LeaseDuration {
		return nil, fmt.Errorf("Dispatcher.HeartbeatInterval must be positive and less than LeaseDuration")
	}
	if opts.RecoverInterval <= 0 {
		return nil, fmt.Errorf("Dispatcher.RecoverInterval must be positive")
	}
	for _, typ := range taskTypes {
		if timeout, ok := opts.Timeouts[typ]; !ok || timeout <= 0 {
			return nil, fmt.Errorf("Dispatcher.Timeouts[%s] must be positive", typ)
		}
	}
	globalCap := opts.Global + priorityBurstSlots
	d := &Dispatcher{q: q, executor: executor, opts: opts, global: make(chan struct{}, globalCap), poster: make(chan struct{}, opts.Poster), preview: make(chan struct{}, opts.Preview), subtitle: make(chan struct{}, opts.Subtitle), bandNext: make([]int, len(priorityBands)), running: map[int64]*workerState{}, sourceLookupBudget: 2 * time.Second, sourceLookups: make(chan struct{}, globalCap), scans: map[int64]map[int64]*workerState{}}
	d.sourceSize = d.posterSourceSize
	d.mediaDuration = d.mediaDurationSec
	return d, nil
}

var taskTypes = []TaskType{TaskPoster, TaskPosterRepair, TaskThumbnail, TaskPreview, TaskKeyframe, TaskSubtitle, TaskSubtitleRecognize, TaskAIAnalysis, TaskAtrack, TaskEncrypt}

// priorityBands is highest-priority first. Types in the same band are equal and rotate.
var priorityBands = [][]TaskType{
	{TaskPoster, TaskPosterRepair},
	{TaskEncrypt},
	{TaskPreview, TaskThumbnail, TaskKeyframe, TaskAtrack},
	{TaskSubtitle, TaskSubtitleRecognize, TaskAIAnalysis},
}

const priorityBurstSlots = 1

func isHighPriorityTask(typ TaskType) bool {
	return typ == TaskPoster || typ == TaskPosterRepair || typ == TaskEncrypt
}

const (
	posterTimeoutPerGiB  = 2 * time.Minute
	posterTimeoutMax     = 30 * time.Minute
	encryptTimeoutPerGiB = 10 * time.Minute
	encryptTimeoutMax    = 8 * time.Hour
	subtitleTimeoutMax   = 8 * time.Hour
	sourceTimeoutGiB     = int64(1 << 30)
)

func sizedTaskTimeout(typ TaskType) bool {
	return typ == TaskPoster || typ == TaskPosterRepair || typ == TaskEncrypt
}

func deferredTaskTimeout(typ TaskType) bool {
	return sizedTaskTimeout(typ) || typ == TaskSubtitle || typ == TaskSubtitleRecognize
}

func usesSubtitleSlot(typ TaskType) bool {
	return typ == TaskSubtitle || typ == TaskSubtitleRecognize
}

// subtitleTaskTimeout returns min(8h, max(base, durationSec*factor seconds)).
// durationSec <= 0 uses base. Invalid factor falls back to 2.0.
func subtitleTaskTimeout(base time.Duration, durationSec int64, factor float64) time.Duration {
	if factor <= 0 {
		factor = 2.0
	}
	timeout := base
	if durationSec > 0 {
		scaled := time.Duration(float64(durationSec)*factor) * time.Second
		if scaled > timeout {
			timeout = scaled
		}
	}
	if timeout > subtitleTimeoutMax {
		return subtitleTimeoutMax
	}
	if timeout < base {
		return base
	}
	return timeout
}

func taskTimeoutForSource(typ TaskType, base time.Duration, size int64) time.Duration {
	if !sizedTaskTimeout(typ) || size <= 0 {
		return base
	}
	perGiB := posterTimeoutPerGiB
	maxTimeout := posterTimeoutMax
	if typ == TaskEncrypt {
		perGiB = encryptTimeoutPerGiB
		maxTimeout = encryptTimeoutMax
	}
	if base >= maxTimeout {
		return maxTimeout
	}
	units := size / sourceTimeoutGiB
	if size%sourceTimeoutGiB != 0 {
		units++
	}
	if units > math.MaxInt64/int64(perGiB) {
		return maxTimeout
	}
	extra := time.Duration(units) * perGiB
	if base > maxTimeout-extra {
		return maxTimeout
	}
	return base + extra
}

func (d *Dispatcher) posterSourceSize(ctx context.Context, task Task) int64 {
	var libraryID int64
	var catalog string
	if err := d.q.db.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_path,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &catalog); err != nil {
		return 0
	}
	sourcePath := storage.PreferredFFmpegPath(d.q.db, task.MediaID, libraryID, catalog)
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

func (d *Dispatcher) mediaDurationSec(ctx context.Context, task Task) int64 {
	var duration int64
	if err := d.q.db.QueryRowContext(ctx, `SELECT COALESCE(duration,0) FROM media WHERE id=?`, task.MediaID).Scan(&duration); err != nil {
		return 0
	}
	return duration
}

func (d *Dispatcher) timeoutForTask(ctx context.Context, task Task, heartbeat *time.Ticker, state *workerState) (time.Duration, bool) {
	base := d.opts.Timeouts[task.Type]
	if !deferredTaskTimeout(task.Type) {
		return base, ctx.Err() == nil
	}
	select {
	case d.sourceLookups <- struct{}{}:
	default:
		return base, ctx.Err() == nil
	}
	result := make(chan int64, 1)
	// os.Stat cannot be canceled on every platform. The buffered result and
	// dispatcher-wide slot bound abandoned lookup goroutines to Global; a stuck
	// call retains its slot, so later tasks fall back instead of amplifying leaks.
	go func() {
		defer func() { <-d.sourceLookups }()
		if task.Type == TaskSubtitle || task.Type == TaskSubtitleRecognize {
			lookup := d.mediaDuration
			if lookup == nil {
				lookup = d.mediaDurationSec
			}
			result <- lookup(ctx, task)
			return
		}
		result <- d.sourceSize(ctx, task)
	}()
	timer := time.NewTimer(d.sourceLookupBudget)
	defer timer.Stop()
	for {
		select {
		case value := <-result:
			if task.Type == TaskSubtitle || task.Type == TaskSubtitleRecognize {
				return subtitleTaskTimeout(base, value, d.opts.SubtitleTimeoutRealtimeFactor), ctx.Err() == nil
			}
			return taskTimeoutForSource(task.Type, base, value), ctx.Err() == nil
		case <-timer.C:
			return base, ctx.Err() == nil
		case <-heartbeat.C:
			if !d.heartbeatTask(ctx, task, state) {
				return base, false
			}
		case <-ctx.Done():
			return base, false
		}
	}
}

func (d *Dispatcher) heartbeatTask(ctx context.Context, task Task, state *workerState) bool {
	// Task budget already gone: do not renew or attribute FailureRetryable to lease I/O.
	// The run loop's taskCtx.Done() path owns timeout/cancel reporting.
	if err := ctx.Err(); err != nil {
		return false
	}
	if task.ScanTaskID != nil {
		cancelled, err := d.q.IsScanCancelled(ctx, *task.ScanTaskID)
		if err != nil {
			state.stop(FailureRetryable, fmt.Errorf("check scan cancellation: %w", err))
			return false
		}
		if cancelled {
			state.stop(FailureCancelled, errors.New("scan cancelled"))
			return false
		}
	}
	// Renew on a short independent budget so a nearly-expired task deadline (or USB
	// SQLite busy retries) is not misreported as "renew lease: context deadline exceeded".
	renewCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ok, err := d.q.Renew(renewCtx, task)
	if err != nil {
		state.stop(FailureRetryable, fmt.Errorf("renew lease: %w", err))
		return false
	}
	if !ok {
		state.stop(FailureRetryable, errors.New("post-ingest lease lost"))
		return false
	}
	return true
}

func (d *Dispatcher) Start(ctx context.Context) error {
	d.startMu.Lock()
	if d.started {
		d.startMu.Unlock()
		return errors.New("Dispatcher.Start may only be called once")
	}
	d.started = true
	d.startMu.Unlock()
	if _, err := d.q.RecoverExpired(ctx); err != nil {
		return fmt.Errorf("recover expired post-ingest tasks: %w", err)
	}
	ticker := time.NewTicker(d.opts.PollInterval)
	defer ticker.Stop()
	recoverTicker := time.NewTicker(d.opts.RecoverInterval)
	defer recoverTicker.Stop()
	for {
		if ctx.Err() != nil {
			d.cancelAll(FailureShutdown, ctx.Err())
			d.wg.Wait()
			return nil
		}
		for {
			allowed := d.allowedTaskTypes()
			if len(allowed) == 0 {
				break
			}
			task, err := d.q.ClaimAny(ctx, allowed)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("postingest dispatcher claim sweep: %v", err)
				}
				break
			}
			if task == nil {
				break
			}
			d.noteClaimed(task.Type)
			if !d.tryAcquire(task.Type) {
				return fmt.Errorf("postingest dispatcher claimed unavailable task type %s", task.Type)
			}
			d.launch(ctx, *task)
		}
		select {
		case <-ctx.Done():
			d.cancelAll(FailureShutdown, ctx.Err())
			d.wg.Wait()
			return nil
		case <-recoverTicker.C:
			if n, err := d.q.RecoverExpired(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("postingest dispatcher recover expired: %v", err)
				}
			} else if n > 0 {
				log.Printf("postingest dispatcher recovered %d expired lease(s)", n)
			}
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) allowedTaskTypes() []TaskType {
	d.mu.Lock()
	globalUsed := d.globalUsed
	highUsed := d.highPriorityUsed
	posterAvailable := d.posterUsed < d.opts.Poster
	previewAvailable := d.previewUsed < d.opts.Preview
	subtitleAvailable := d.subtitleUsed < d.opts.Subtitle
	bandNext := append([]int(nil), d.bandNext...)
	d.mu.Unlock()

	// Soft-cap oversubscribe (Global+1): prefer high-priority burst; otherwise allow one
	// subtitle so ASR is not starved behind a full poster/preview mid-band backlog.
	burstHigh := false
	burstSubtitleOnly := false
	switch {
	case globalUsed < d.opts.Global:
	case d.opts.Global == 1:
		// Preserve strict priority for a single configured slot. The burst slot is a
		// starvation escape hatch for concurrent pools, not a second worker lane.
		return nil
	case globalUsed < d.opts.Global+priorityBurstSlots && highUsed == 0:
		burstHigh = true
	case globalUsed < d.opts.Global+priorityBurstSlots && subtitleAvailable:
		burstSubtitleOnly = true
	default:
		return nil
	}

	allowed := make([]TaskType, 0, len(taskTypes))
	for band, types := range priorityBands {
		if burstSubtitleOnly {
			hasSubtitle := false
			for _, typ := range types {
				if usesSubtitleSlot(typ) {
					hasSubtitle = true
					break
				}
			}
			if !hasSubtitle {
				continue
			}
		} else if burstHigh && band > 1 {
			hasSubtitle := false
			for _, typ := range types {
				if usesSubtitleSlot(typ) {
					hasSubtitle = true
					break
				}
			}
			if !hasSubtitle {
				// High-priority bands only, plus subtitle family so ClaimAny can fall through when
				// no high-priority work is eligible.
				continue
			}
		}
		n := len(types)
		start := 0
		if band < len(bandNext) && n > 0 {
			start = bandNext[band] % n
		}
		for i := 0; i < n; i++ {
			typ := types[(start+i)%n]
			if (typ == TaskPoster || typ == TaskPosterRepair) && !posterAvailable {
				continue
			}
			if typ == TaskPreview && !previewAvailable {
				continue
			}
			if usesSubtitleSlot(typ) && !subtitleAvailable {
				continue
			}
			if burstSubtitleOnly && !usesSubtitleSlot(typ) {
				continue
			}
			allowed = append(allowed, typ)
		}
	}
	return allowed
}

func (d *Dispatcher) noteClaimed(typ TaskType) {
	for band, types := range priorityBands {
		for i, candidate := range types {
			if candidate != typ {
				continue
			}
			d.mu.Lock()
			if band < len(d.bandNext) && len(types) > 0 {
				d.bandNext[band] = (i + 1) % len(types)
			}
			d.mu.Unlock()
			return
		}
	}
}

func (d *Dispatcher) tryAcquire(typ TaskType) bool {
	d.mu.Lock()
	atSoftCap := d.globalUsed >= d.opts.Global
	atHardCap := d.globalUsed >= d.opts.Global+priorityBurstSlots
	highUsed := d.highPriorityUsed
	d.mu.Unlock()
	if atHardCap {
		return false
	}
	if atSoftCap {
		highBurstOK := isHighPriorityTask(typ) && highUsed == 0
		subtitleBurstOK := usesSubtitleSlot(typ)
		if !highBurstOK && !subtitleBurstOK {
			return false
		}
	}
	select {
	case d.global <- struct{}{}:
	default:
		return false
	}
	var sub chan struct{}
	if typ == TaskPoster || typ == TaskPosterRepair {
		sub = d.poster
	} else if typ == TaskPreview {
		sub = d.preview
	} else if usesSubtitleSlot(typ) {
		sub = d.subtitle
	}
	if sub != nil {
		select {
		case sub <- struct{}{}:
		default:
			<-d.global
			return false
		}
	}
	d.mu.Lock()
	d.globalUsed++
	if isHighPriorityTask(typ) {
		d.highPriorityUsed++
	}
	if typ == TaskPoster || typ == TaskPosterRepair {
		d.posterUsed++
	}
	if typ == TaskPreview {
		d.previewUsed++
	}
	if usesSubtitleSlot(typ) {
		d.subtitleUsed++
	}
	d.mu.Unlock()
	return true
}
func (d *Dispatcher) release(typ TaskType) {
	if typ == TaskPoster || typ == TaskPosterRepair {
		<-d.poster
	} else if typ == TaskPreview {
		<-d.preview
	} else if usesSubtitleSlot(typ) {
		<-d.subtitle
	}
	<-d.global
	d.mu.Lock()
	d.globalUsed--
	if isHighPriorityTask(typ) {
		d.highPriorityUsed--
	}
	if typ == TaskPoster || typ == TaskPosterRepair {
		d.posterUsed--
	}
	if typ == TaskPreview {
		d.previewUsed--
	}
	if usesSubtitleSlot(typ) {
		d.subtitleUsed--
	}
	d.mu.Unlock()
}

func (d *Dispatcher) launch(parent context.Context, task Task) {
	if d.beforeRegister != nil {
		d.beforeRegister(task)
	}
	var lifecycleCtx context.Context
	var cancel context.CancelFunc
	if deferredTaskTimeout(task.Type) {
		lifecycleCtx, cancel = context.WithCancel(parent)
	} else {
		lifecycleCtx, cancel = context.WithTimeout(parent, d.opts.Timeouts[task.Type])
	}
	state := &workerState{cancel: cancel}
	d.mu.Lock()
	d.running[task.ID] = state
	if task.ScanTaskID != nil {
		m := d.scans[*task.ScanTaskID]
		if m == nil {
			m = map[int64]*workerState{}
			d.scans[*task.ScanTaskID] = m
		}
		m[task.ID] = state
	}
	d.mu.Unlock()
	d.wg.Add(1)
	go d.runTask(parent, lifecycleCtx, task, state)
}

func (d *Dispatcher) runTask(parent, lifecycleCtx context.Context, task Task, state *workerState) {
	defer d.wg.Done()
	if d.beforeRun != nil {
		d.beforeRun(task)
	}
	defer state.cancel()
	var cleanupOnce sync.Once
	cleanup := func() { cleanupOnce.Do(func() { d.unregister(task, state); d.release(task.Type) }) }
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()
	if task.ScanTaskID != nil {
		cancelled, err := d.q.IsScanCancelled(lifecycleCtx, *task.ScanTaskID)
		if err != nil {
			kind := FailureRetryable
			cause := error(fmt.Errorf("check scan cancellation before execute: %w", err))
			if stoppedKind, stoppedCause := state.reason(); stoppedCause != nil {
				kind, cause = stoppedKind, stoppedCause
			}
			writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if failErr := d.q.Fail(writeCtx, &task, kind, cause); failErr != nil {
				log.Printf("postingest dispatcher preflight task %d: %v", task.ID, failErr)
			}
			return
		}
		if cancelled {
			writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if failErr := d.q.Fail(writeCtx, &task, FailureCancelled, errors.New("scan cancelled")); failErr != nil {
				log.Printf("postingest dispatcher preflight cancel task %d: %v", task.ID, failErr)
			}
			return
		}
	}
	if deferredTaskTimeout(task.Type) && !d.heartbeatTask(lifecycleCtx, task, state) {
		d.failBeforeExecute(parent, task, state)
		return
	}
	heartbeat := time.NewTicker(d.opts.HeartbeatInterval)
	defer heartbeat.Stop()
	taskCtx := lifecycleCtx
	taskCancel := func() {}
	if deferredTaskTimeout(task.Type) {
		timeout, proceed := d.timeoutForTask(lifecycleCtx, task, heartbeat, state)
		if !proceed {
			d.failBeforeExecute(parent, task, state)
			return
		}
		taskCtx, taskCancel = context.WithTimeout(lifecycleCtx, timeout)
	}
	defer taskCancel()
	if taskCtx.Err() != nil {
		d.failBeforeExecute(parent, task, state)
		return
	}
	type executionOutcome struct {
		result ExecutionResult
		err    error
	}
	result := make(chan executionOutcome, 1)
	go func() { r, e := executeTask(taskCtx, d.executor, task); result <- executionOutcome{r, e} }()
	var execErr error
	var execResult ExecutionResult
	executorUnresponsive := false
	for {
		select {
		case outcome := <-result:
			execResult, execErr = outcome.result, outcome.err
			goto finish
		case <-heartbeat.C:
			d.heartbeatTask(taskCtx, task, state)
		case <-taskCtx.Done():
			timer := time.NewTimer(d.opts.ExecutorStopGrace)
			select {
			case outcome := <-result:
				execResult, execErr = outcome.result, outcome.err
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				executorUnresponsive = true
				execErr = errors.New("executor did not stop after context cancellation")
			}
			goto finish
		}
	}
finish:
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if parent.Err() != nil {
		cause := error(parent.Err())
		if executorUnresponsive {
			cause = execErr
		}
		if err := d.q.Fail(writeCtx, &task, FailureShutdown, cause); err != nil {
			log.Printf("postingest dispatcher shutdown task %d: %v", task.ID, err)
		}
		if executorUnresponsive {
			handedOff = true
			go func() { <-result; cleanup() }()
		}
		return
	}
	if kind, cause := state.reason(); cause != nil {
		if executorUnresponsive && kind != FailureCancelled {
			kind, cause = FailureShutdown, execErr
		}
		if err := d.q.Fail(writeCtx, &task, kind, cause); err != nil {
			log.Printf("postingest dispatcher fail task %d: %v", task.ID, err)
		}
		if executorUnresponsive {
			handedOff = true
			go func() { <-result; cleanup() }()
		}
		return
	}
	if executorUnresponsive {
		if err := d.q.Fail(writeCtx, &task, FailureShutdown, execErr); err != nil {
			log.Printf("postingest dispatcher isolate task %d: %v", task.ID, err)
		}
		handedOff = true
		go func() { <-result; cleanup() }()
		return
	}
	if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
		execErr = context.DeadlineExceeded
	}
	if execErr == nil {
		if execResult.Completion == AlreadyCommittedAtomically {
			return
		}
		if err := d.q.Complete(writeCtx, task); err != nil {
			log.Printf("postingest dispatcher complete task %d: %v", task.ID, err)
		}
		return
	}
	if execResult.Completion == FinalizationOutcomeUncertain {
		return
	}
	kind := failureKind(execErr)
	if err := d.q.Fail(writeCtx, &task, kind, execErr); err != nil {
		log.Printf("postingest dispatcher fail task %d: %v", task.ID, err)
	}
}

func (d *Dispatcher) failBeforeExecute(parent context.Context, task Task, state *workerState) {
	kind, cause := state.reason()
	if parent.Err() != nil {
		kind, cause = FailureShutdown, parent.Err()
	} else if cause == nil {
		kind, cause = FailureRetryable, errors.New("task lifecycle canceled before execute")
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.q.Fail(writeCtx, &task, kind, cause); err != nil {
		log.Printf("postingest dispatcher pre-execute task %d: %v", task.ID, err)
	}
}

func failureKind(err error) FailureKind {
	var ptr *ClassifiedError
	if errors.As(err, &ptr) && ptr != nil {
		return ptr.Kind
	}
	var val ClassifiedError
	if errors.As(err, &val) {
		return val.Kind
	}
	return FailureRetryable
}
func (d *Dispatcher) unregister(task Task, state *workerState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[task.ID] == state {
		delete(d.running, task.ID)
	}
	if task.ScanTaskID != nil {
		if m := d.scans[*task.ScanTaskID]; m != nil && m[task.ID] == state {
			delete(m, task.ID)
			if len(m) == 0 {
				delete(d.scans, *task.ScanTaskID)
			}
		}
	}
}
func (d *Dispatcher) cancelAll(kind FailureKind, cause error) {
	d.mu.Lock()
	states := make([]*workerState, 0, len(d.running))
	for _, s := range d.running {
		states = append(states, s)
	}
	d.mu.Unlock()
	for _, s := range states {
		s.stop(kind, cause)
	}
}
func (d *Dispatcher) CancelScan(scanTaskID int64) {
	d.mu.Lock()
	states := make([]*workerState, 0)
	for _, s := range d.scans[scanTaskID] {
		states = append(states, s)
	}
	d.mu.Unlock()
	for _, s := range states {
		s.stop(FailureCancelled, errors.New("scan cancelled"))
	}
}

// CancelTask cooperatively cancels a running task, then force-cancels in DB after grace
// if the executor has not released the slot (admin abort soft→hard).
func (d *Dispatcher) CancelTask(taskID int64) {
	if taskID <= 0 {
		return
	}
	d.mu.Lock()
	state := d.running[taskID]
	d.mu.Unlock()
	if state != nil {
		state.stop(FailureCancelled, errors.New("cancelled by admin"))
	}
	grace := d.opts.ExecutorStopGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		<-timer.C
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.q.AdminCancelTask(ctx, taskID)
		d.mu.Lock()
		still := d.running[taskID]
		d.mu.Unlock()
		if still != nil {
			// Executor still holding the slot; stop again and wait briefly for cleanup.
			still.stop(FailureCancelled, errors.New("cancelled by admin (forced)"))
		}
	}()
}

func (d *Dispatcher) Snapshot() BudgetSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return BudgetSnapshot{d.opts.Global, d.globalUsed, d.opts.Poster, d.posterUsed, d.opts.Preview, d.previewUsed, d.opts.Subtitle, d.subtitleUsed}
}
