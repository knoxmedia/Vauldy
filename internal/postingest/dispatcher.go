package postingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"knox-media/internal/progressctx"
	"knox-media/internal/scheduler"
	"knox-media/internal/storage"
	"knox-media/internal/store"
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

// WithProgressReporter attaches a progress callback to ctx. Long-running
// executors call ReportProgress while their underlying work advances so the
// dispatcher can distinguish a healthy task from a stalled one.
func WithProgressReporter(ctx context.Context, report func()) context.Context {
	return progressctx.WithReporter(ctx, report)
}

// ReportProgress signals that the current task made forward progress. It is a
// no-op when the context carries no reporter (e.g. tests or non-dispatcher
// execution).
func ReportProgress(ctx context.Context) {
	progressctx.Report(ctx)
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

type DispatcherOptions struct {
	OwnerID                                                         string
	SubtitleTimeoutRealtimeFactor                                   float64
	PollInterval, LeaseDuration, HeartbeatInterval, RecoverInterval time.Duration
	ExecutorStopGrace                                               time.Duration
	// ProgressIdleTimeout bounds how long a task may run without reporting any
	// progress. Long-running workers (encryption, preview, keyframe) report
	// progress while they advance; a task that stops reporting for this long is
	// considered stalled and is force-cancelled instead of waiting for the
	// fixed wall-clock Timeouts entry.
	ProgressIdleTimeout time.Duration
	// MaxRuntime is the absolute upper bound for progress-driven tasks. It
	// replaces the fixed Timeouts entry as the task-context deadline once
	// progress reporting is active, so a healthy task never times out purely on
	// wall clock. Zero keeps the per-task Timeouts behavior.
	MaxRuntime time.Duration
	Timeouts   map[TaskType]time.Duration
}

func DefaultDispatcherOptions() DispatcherOptions {
	return DispatcherOptions{SubtitleTimeoutRealtimeFactor: 2.0, PollInterval: 250 * time.Millisecond, LeaseDuration: leaseDuration, HeartbeatInterval: 30 * time.Second, RecoverInterval: 30 * time.Second, ExecutorStopGrace: 10 * time.Second, ProgressIdleTimeout: 15 * time.Minute, MaxRuntime: 12 * time.Hour, Timeouts: map[TaskType]time.Duration{
		TaskPoster: 2 * time.Minute, TaskPosterRepair: 2 * time.Minute, TaskThumbnail: 2 * time.Minute, TaskPreview: 30 * time.Minute, TaskKeyframe: 30 * time.Minute, TaskSubtitle: 60 * time.Minute, TaskSubtitleRecognize: 60 * time.Minute, TaskAIAnalysis: 15 * time.Minute, TaskAtrack: 30 * time.Minute, TaskEncrypt: 120 * time.Minute,
	}}
}

type workerState struct {
	cancel       context.CancelFunc
	executionID  string
	mu           sync.Mutex
	kind         FailureKind
	cause        error
	progressSeen bool
	lastProgress time.Time
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

// reportProgress records the task as making forward progress. Executors call it
// through ReportProgress whenever their underlying work advances, so the
// dispatcher can distinguish a healthy long-running task from a stalled one.
func (w *workerState) reportProgress() {
	w.mu.Lock()
	w.progressSeen = true
	w.lastProgress = time.Now()
	w.mu.Unlock()
}

// progressStale reports whether a progress-reporting task has been silent for
// longer than idle. Tasks that never report progress (short captures, subtitle
// inference) are never considered stale: their fixed Timeouts still apply.
func (w *workerState) progressStale(idle time.Duration) bool {
	if idle <= 0 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.progressSeen {
		return false
	}
	return time.Since(w.lastProgress) > idle
}

type Dispatcher struct {
	q                  *Queue
	executor           Executor
	opts               DispatcherOptions
	svc                *scheduler.Service
	mu                 sync.Mutex
	running            map[int64]*workerState
	sourceLookupBudget time.Duration
	sourceSize         func(context.Context, Task) int64
	mediaDuration      func(context.Context, Task) int64
	sourceLookups      chan struct{}
	scans              map[int64]map[int64]*workerState
	wg                 sync.WaitGroup
	startMu            sync.Mutex
	started            bool
	beforeRegister     func(Task)
	beforeRun          func(Task)
}

func NewDispatcher(q *Queue, executor Executor, opts DispatcherOptions, svc *scheduler.Service) (*Dispatcher, error) {
	if q == nil {
		return nil, errors.New("Dispatcher.Queue is required")
	}
	if executor == nil {
		return nil, errors.New("Dispatcher.Executor is required")
	}
	if svc == nil {
		return nil, errors.New("Dispatcher.SchedulerService is required")
	}
	if strings.TrimSpace(opts.OwnerID) == "" || opts.OwnerID != strings.TrimSpace(opts.OwnerID) || strings.Contains(opts.OwnerID, "/") {
		return nil, fmt.Errorf("Dispatcher.OwnerID is invalid")
	}
	if q.owner != opts.OwnerID {
		return nil, fmt.Errorf("Dispatcher.OwnerID must match Queue owner")
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
	if opts.ProgressIdleTimeout < 0 {
		return nil, fmt.Errorf("Dispatcher.ProgressIdleTimeout must not be negative")
	}
	if opts.MaxRuntime < 0 {
		return nil, fmt.Errorf("Dispatcher.MaxRuntime must not be negative")
	}
	for _, typ := range taskTypes {
		if timeout, ok := opts.Timeouts[typ]; !ok || timeout <= 0 {
			return nil, fmt.Errorf("Dispatcher.Timeouts[%s] must be positive", typ)
		}
	}
	d := &Dispatcher{q: q, executor: executor, opts: opts, svc: svc, running: map[int64]*workerState{}, sourceLookupBudget: 2 * time.Second, sourceLookups: make(chan struct{}, 16), scans: map[int64]map[int64]*workerState{}}
	d.sourceSize = d.posterSourceSize
	d.mediaDuration = d.mediaDurationSec
	svc.SetClaimer(d.schedulerClaim)
	return d, nil
}

var taskTypes = []TaskType{TaskPoster, TaskPosterRepair, TaskThumbnail, TaskPreview, TaskKeyframe, TaskSubtitle, TaskSubtitleRecognize, TaskAIAnalysis, TaskAtrack, TaskEncrypt}

func taskTypeNames() []string {
	out := make([]string, 0, len(taskTypes))
	for _, typ := range taskTypes {
		out = append(out, string(typ))
	}
	return out
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

// progressDrivenTask reports whether the task type is driven by forward-progress
// reporting instead of a fixed wall-clock deadline: a healthy task keeps
// reporting progress (encryption checkpoints, ffmpeg output, OCR/ASR/LLM batch
// completion, ffprobe packet streams) and therefore must not be cancelled on
// time alone; a stalled task is force-cancelled by the heartbeat loop after
// ProgressIdleTimeout. Each listed type must report progress from its executor,
// otherwise its stall detection never activates and MaxRuntime becomes the only
// ceiling.
func progressDrivenTask(typ TaskType) bool {
	switch typ {
	case TaskEncrypt, TaskPreview, TaskSubtitle, TaskSubtitleRecognize,
		TaskAIAnalysis, TaskKeyframe, TaskAtrack:
		return true
	default:
		return false
	}
}

func deferredTaskTimeout(typ TaskType) bool {
	return sizedTaskTimeout(typ) || progressDrivenTask(typ) || typ == TaskSubtitle || typ == TaskSubtitleRecognize
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
	if d.opts.MaxRuntime > 0 && progressDrivenTask(task.Type) {
		// Progress-driven tasks (encryption, preview) report forward progress
		// while they advance, and the heartbeat loop force-cancels them when
		// progress stops for ProgressIdleTimeout. MaxRuntime is only the
		// absolute wall-clock ceiling.
		return d.opts.MaxRuntime, ctx.Err() == nil
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
		// A lease renewal that fails on SQLite lock contention (SQLITE_BUSY) must
		// not fail a task whose executor is still running normally. Mirrors
		// scancoord's busy-aware heartbeat: retry within the remaining lease
		// budget first; if the database stays locked past the lease budget, defer
		// the failure while the executor is alive and making progress — the next
		// heartbeat renews again once contention clears.
		if store.IsSQLiteBusy(err) {
			if renewed, renewErr := d.renewLeaseWithBusyRetry(task); renewErr == nil && renewed {
				return true
			} else if renewErr != nil {
				err = renewErr
			}
			if store.IsSQLiteBusy(err) && !state.progressStale(d.opts.ProgressIdleTimeout) {
				log.Printf("postingest dispatcher task %d (%s media %d) lease renew blocked by SQLite lock contention past lease budget; executor still running, deferring failure", task.ID, task.Type, task.MediaID)
				return true
			}
		}
		state.stop(FailureRetryable, fmt.Errorf("renew lease: %w", err))
		return false
	}
	if !ok {
		state.stop(FailureRetryable, errors.New("post-ingest lease lost"))
		return false
	}
	return true
}

// renewLeaseWithBusyRetry renews the task lease while retrying SQLITE_BUSY
// errors for a bounded window (mirroring scancoord.heartbeat). A momentarily
// locked database (e.g. a concurrent batch of ingest writes) must not kill a
// long-running task like preview; the renewal is retried within the budget, and
// if the database stays locked the caller defers the failure while the executor
// is still running. The window is bounded by the heartbeat interval so a single
// heartbeat never monopolizes the run loop for the full lease.
func (d *Dispatcher) renewLeaseWithBusyRetry(task Task) (bool, error) {
	remaining := time.Until(task.LeaseUntil)
	if remaining <= 0 {
		// Fall back to the configured lease duration if the in-memory copy is stale.
		remaining = d.opts.LeaseDuration
	}
	if remaining > d.opts.HeartbeatInterval {
		remaining = d.opts.HeartbeatInterval
	}
	safety := d.opts.HeartbeatInterval / 4
	if safety > 250*time.Millisecond {
		safety = 250 * time.Millisecond
	}
	policy := store.HeartbeatLeaseRetryPolicy("postingest_lease_renew", remaining, safety)
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	defer cancel()
	var renewed bool
	err := store.WithBusyRetryPolicyContext(ctx, d.q.metrics, policy, func(attemptCtx context.Context) error {
		ok, renewErr := d.q.Renew(attemptCtx, task)
		if renewErr != nil {
			return renewErr
		}
		if !ok {
			return errors.New("post-ingest lease lost")
		}
		renewed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return renewed, nil
}

func (d *Dispatcher) Start(ctx context.Context) error {
	d.startMu.Lock()
	if d.started {
		d.startMu.Unlock()
		return errors.New("Dispatcher.Start may only be called once")
	}
	d.started = true
	d.startMu.Unlock()
	// Startup recovery reclaims leases orphaned by a previous process. No
	// in-memory executors exist yet, so nothing needs to be skipped.
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
			claim, err := d.svc.Claim(ctx, d.opts.OwnerID, taskTypeNames())
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("postingest dispatcher claim: %v", err)
				}
				break
			}
			if claim == nil {
				break
			}
			task, ok := claim.Payload.(Task)
			if !ok {
				log.Printf("postingest dispatcher: scheduler returned unsupported claim payload %T", claim.Payload)
				break
			}
			d.launch(ctx, task)
		}
		select {
		case <-ctx.Done():
			d.cancelAll(FailureShutdown, ctx.Err())
			d.wg.Wait()
			return nil
		case <-recoverTicker.C:
			// Skip tasks this process is still executing. A task whose lease
			// lapsed under SQLite lock contention is still running normally
			// in-memory; recovering it here would mark it failed even though its
			// executor is healthy (see heartbeatTask's busy-aware renewal).
			skipTasks, skipExecutions := d.runningSnapshot()
			if n, err := d.q.RecoverExpiredSkipping(ctx, skipTasks, skipExecutions); err != nil {
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

// runningSnapshot returns the task IDs and scheduler execution IDs currently
// executing in this process. The periodic recovery loop skips these so a task
// whose lease expired only because of transient database lock contention is not
// marked failed while its executor is still alive.
func (d *Dispatcher) runningSnapshot() (map[int64]struct{}, map[string]struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tasks := make(map[int64]struct{}, len(d.running))
	executions := make(map[string]struct{}, len(d.running))
	for id, state := range d.running {
		tasks[id] = struct{}{}
		if state.executionID != "" {
			executions[state.executionID] = struct{}{}
		}
	}
	return tasks, executions
}

// schedulerClaim is the scheduler service claimer. It asks the queue for the
// next eligible post-ingest unit; the queue performs scheduler admission and
// only returns tasks with a durable reservation, which become the sole units
// the dispatcher launches.
func (d *Dispatcher) schedulerClaim(ctx context.Context, owner string, taskTypeNames []string) (*scheduler.ClaimResult, error) {
	types := make([]TaskType, 0, len(taskTypeNames))
	for _, name := range taskTypeNames {
		types = append(types, TaskType(name))
	}
	task, err := d.q.ClaimAny(ctx, types)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	return &scheduler.ClaimResult{
		ExecutionID: task.ExecutionID,
		TaskType:    string(task.Type),
		Owner:       owner,
		QueueID:     task.ID,
		MediaID:     task.MediaID,
		LeaseUntil:  task.LeaseUntil,
		Payload:     *task,
	}, nil
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
	state := &workerState{cancel: cancel, executionID: task.ExecutionID, lastProgress: time.Now()}
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
	cleanup := func() { cleanupOnce.Do(func() { d.unregister(task, state); d.releaseReservation(task) }) }
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
	// Executors report forward progress through the task context; the heartbeat
	// loop below force-cancels tasks that stop reporting for ProgressIdleTimeout.
	taskCtx = WithProgressReporter(taskCtx, state.reportProgress)
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
			if d.opts.ProgressIdleTimeout > 0 && state.progressStale(d.opts.ProgressIdleTimeout) {
				log.Printf("postingest dispatcher task %d (%s media %d) stalled: no progress for %v; force-cancelling", task.ID, task.Type, task.MediaID, d.opts.ProgressIdleTimeout)
				state.stop(FailureRetryable, errors.New("task stalled: no progress reported"))
			}
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

// releaseReservation frees the task's scheduler reservation when its execution
// unit finally ends. Normal complete/fail paths already released it (the
// compare-and-set makes a duplicate release a no-op); the shutdown-unresponsive
// path retains budget while the executor is unresponsive and releases here.
func (d *Dispatcher) releaseReservation(task Task) {
	if task.ExecutionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := releaseReservationDirect(ctx, d.q.db, task, "execution_end"); err != nil {
		log.Printf("postingest dispatcher release reservation %s: %v", task.ExecutionID, err)
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

// Snapshot reports scheduler usage and limits for the compatibility overview.
// Usage is derived from durable reservations; limits come from the effective
// scheduler policy.
func (d *Dispatcher) Snapshot(ctx context.Context) (scheduler.BudgetSnapshot, error) {
	if d == nil || d.svc == nil {
		return scheduler.BudgetSnapshot{}, nil
	}
	return d.svc.Snapshot(ctx)
}
