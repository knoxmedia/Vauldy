package postingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Executor interface {
	Execute(context.Context, Task) error
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

type BudgetSnapshot struct{ GlobalLimit, GlobalUsed, PosterLimit, PosterUsed, PreviewLimit, PreviewUsed int }
type DispatcherOptions struct {
	OwnerID                                        string
	Global, Poster, Preview                        int
	PollInterval, LeaseDuration, HeartbeatInterval time.Duration
	ExecutorStopGrace                              time.Duration
	Timeouts                                       map[TaskType]time.Duration
}

func DefaultDispatcherOptions() DispatcherOptions {
	global := runtime.NumCPU() / 2
	if global < 2 {
		global = 2
	}
	if global > 4 {
		global = 4
	}
	return DispatcherOptions{Global: global, Poster: min(2, global), Preview: 1, PollInterval: 250 * time.Millisecond, LeaseDuration: leaseDuration, HeartbeatInterval: 30 * time.Second, ExecutorStopGrace: 10 * time.Second, Timeouts: map[TaskType]time.Duration{
		TaskPoster: 2 * time.Minute, TaskThumbnail: 2 * time.Minute, TaskPreview: 30 * time.Minute, TaskKeyframe: 30 * time.Minute, TaskSubtitle: 60 * time.Minute, TaskAtrack: 30 * time.Minute, TaskEncrypt: 120 * time.Minute,
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
	q                                   *Queue
	executor                            Executor
	opts                                DispatcherOptions
	global, poster, preview             chan struct{}
	mu                                  sync.Mutex
	globalUsed, posterUsed, previewUsed int
	running                             map[int64]*workerState
	scans                               map[int64]map[int64]*workerState
	wg                                  sync.WaitGroup
	startMu                             sync.Mutex
	started                             bool
	beforeRegister                      func(Task)
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
	for _, typ := range taskTypes {
		if timeout, ok := opts.Timeouts[typ]; !ok || timeout <= 0 {
			return nil, fmt.Errorf("Dispatcher.Timeouts[%s] must be positive", typ)
		}
	}
	return &Dispatcher{q: q, executor: executor, opts: opts, global: make(chan struct{}, opts.Global), poster: make(chan struct{}, opts.Poster), preview: make(chan struct{}, opts.Preview), running: map[int64]*workerState{}, scans: map[int64]map[int64]*workerState{}}, nil
}

var taskTypes = []TaskType{TaskPoster, TaskThumbnail, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt}

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
	next := 0
	for {
		if ctx.Err() != nil {
			d.cancelAll(FailureShutdown, ctx.Err())
			d.wg.Wait()
			return nil
		}
		for {
			claimed := false
			for offset := 0; offset < len(taskTypes); offset++ {
				index := (next + offset) % len(taskTypes)
				typ := taskTypes[index]
				if !d.tryAcquire(typ) {
					continue
				}
				task, err := d.q.Claim(ctx, typ)
				if err != nil {
					d.release(typ)
					if ctx.Err() == nil {
						log.Printf("postingest dispatcher claim %s: %v", typ, err)
					}
					continue
				}
				if task == nil {
					d.release(typ)
					continue
				}
				next = (index + 1) % len(taskTypes)
				claimed = true
				d.launch(ctx, *task)
				break
			}
			if !claimed {
				break
			}
		}
		select {
		case <-ctx.Done():
			d.cancelAll(FailureShutdown, ctx.Err())
			d.wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) tryAcquire(typ TaskType) bool {
	select {
	case d.global <- struct{}{}:
	default:
		return false
	}
	var sub chan struct{}
	if typ == TaskPoster {
		sub = d.poster
	} else if typ == TaskPreview {
		sub = d.preview
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
	if typ == TaskPoster {
		d.posterUsed++
	}
	if typ == TaskPreview {
		d.previewUsed++
	}
	d.mu.Unlock()
	return true
}
func (d *Dispatcher) release(typ TaskType) {
	if typ == TaskPoster {
		<-d.poster
	} else if typ == TaskPreview {
		<-d.preview
	}
	<-d.global
	d.mu.Lock()
	d.globalUsed--
	if typ == TaskPoster {
		d.posterUsed--
	}
	if typ == TaskPreview {
		d.previewUsed--
	}
	d.mu.Unlock()
}

func (d *Dispatcher) launch(parent context.Context, task Task) {
	if d.beforeRegister != nil {
		d.beforeRegister(task)
	}
	taskCtx, cancel := context.WithTimeout(parent, d.opts.Timeouts[task.Type])
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
	go d.runTask(parent, taskCtx, task, state)
}

func (d *Dispatcher) runTask(parent, taskCtx context.Context, task Task, state *workerState) {
	defer d.wg.Done()
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
		cancelled, err := d.q.IsScanCancelled(taskCtx, *task.ScanTaskID)
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
	result := make(chan error, 1)
	go func() { result <- d.executor.Execute(taskCtx, task) }()
	heartbeat := time.NewTicker(d.opts.HeartbeatInterval)
	defer heartbeat.Stop()
	var execErr error
	executorUnresponsive := false
	for {
		select {
		case execErr = <-result:
			goto finish
		case <-heartbeat.C:
			if task.ScanTaskID != nil {
				cancelled, err := d.q.IsScanCancelled(taskCtx, *task.ScanTaskID)
				if err != nil {
					state.stop(FailureRetryable, fmt.Errorf("check scan cancellation: %w", err))
					continue
				}
				if cancelled {
					state.stop(FailureCancelled, errors.New("scan cancelled"))
					continue
				}
			}
			ok, err := d.q.Renew(taskCtx, task)
			if err != nil {
				state.stop(FailureRetryable, fmt.Errorf("renew lease: %w", err))
				continue
			}
			if !ok {
				state.stop(FailureRetryable, errors.New("post-ingest lease lost"))
				continue
			}
		case <-taskCtx.Done():
			timer := time.NewTimer(d.opts.ExecutorStopGrace)
			select {
			case execErr = <-result:
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
		if err := d.q.Complete(writeCtx, task); err != nil {
			log.Printf("postingest dispatcher complete task %d: %v", task.ID, err)
		}
		return
	}
	kind := failureKind(execErr)
	if err := d.q.Fail(writeCtx, &task, kind, execErr); err != nil {
		log.Printf("postingest dispatcher fail task %d: %v", task.ID, err)
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
func (d *Dispatcher) Snapshot() BudgetSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return BudgetSnapshot{d.opts.Global, d.globalUsed, d.opts.Poster, d.posterUsed, d.opts.Preview, d.previewUsed}
}
