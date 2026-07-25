package handler

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

var ErrLibraryPreviewUnavailable = errors.New("library preview unavailable")

type libraryPreviewJobState struct {
	running, dirty, deferred bool
	missing                  bool
}
type libraryPreviewFailure struct {
	attempts            int
	nextRetry, timeSeen time.Time
}
type libraryPreviewSchedulerOptions struct {
	Now                    func() time.Time
	InitialRetry, MaxRetry time.Duration
	MaxFailures            int
}
type libraryPreviewScheduler struct {
	mu         sync.Mutex
	pending    map[int64]struct{}
	states     map[int64]*libraryPreviewJobState
	failures   map[int64]libraryPreviewFailure
	wake       chan struct{}
	run        func(context.Context, int64) error
	maxPending int
	options    libraryPreviewSchedulerOptions
}

func defaultLibraryPreviewSchedulerOptions() libraryPreviewSchedulerOptions {
	return libraryPreviewSchedulerOptions{Now: time.Now, InitialRetry: time.Minute, MaxRetry: 30 * time.Minute, MaxFailures: 1024}
}
func newLibraryPreviewScheduler(ctx context.Context, b *BackgroundGroup, workers, maxPending int, run func(context.Context, int64) error) *libraryPreviewScheduler {
	return newLibraryPreviewSchedulerWithOptions(ctx, b, workers, maxPending, defaultLibraryPreviewSchedulerOptions(), run)
}
func newLibraryPreviewSchedulerWithOptions(ctx context.Context, b *BackgroundGroup, workers, maxPending int, o libraryPreviewSchedulerOptions, run func(context.Context, int64) error) *libraryPreviewScheduler {
	if ctx == nil || b == nil || workers <= 0 || maxPending <= 0 || run == nil {
		return nil
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.InitialRetry <= 0 {
		o.InitialRetry = time.Minute
	}
	if o.MaxRetry < o.InitialRetry {
		o.MaxRetry = 30 * time.Minute
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = 1024
	}
	s := &libraryPreviewScheduler{pending: map[int64]struct{}{}, states: map[int64]*libraryPreviewJobState{}, failures: map[int64]libraryPreviewFailure{}, wake: make(chan struct{}, 1), run: run, maxPending: maxPending, options: o}
	for i := 0; i < workers; i++ {
		b.Go(ctx, s.worker)
	}
	return s
}
func (s *libraryPreviewScheduler) enqueueIfMissing(id int64) bool { return s.enqueue(id, false) }
func (s *libraryPreviewScheduler) enqueueDirty(id int64) bool     { return s.enqueue(id, true) }
func (s *libraryPreviewScheduler) enqueue(id int64, dirty bool) bool {
	if s == nil || id <= 0 {
		return false
	}
	s.mu.Lock()
	now := s.options.Now()
	s.pruneFailuresLocked(now)
	if dirty {
		delete(s.failures, id)
	} else if f, ok := s.failures[id]; ok && now.Before(f.nextRetry) {
		s.mu.Unlock()
		return true
	}
	if st, ok := s.states[id]; ok {
		if dirty {
			st.missing = false
			if st.running {
				st.dirty = true
			}
		}
		s.mu.Unlock()
		return true
	}
	if len(s.pending) >= s.maxPending {
		s.mu.Unlock()
		return false
	}
	s.states[id] = &libraryPreviewJobState{missing: !dirty}
	s.pending[id] = struct{}{}
	s.mu.Unlock()
	s.signal()
	return true
}
func (s *libraryPreviewScheduler) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}
func (s *libraryPreviewScheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *libraryPreviewScheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		for {
			id, more, ok := s.take()
			if !ok {
				break
			}
			if more {
				s.signal()
			}
			err := s.run(ctx, id)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("library preview refresh library=%d: %v", id, err)
			}
			s.finish(ctx, id, err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}
func (s *libraryPreviewScheduler) take() (int64, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.pending {
		delete(s.pending, id)
		s.states[id].running = true
		s.promoteDeferredLocked()
		return id, len(s.pending) > 0, true
	}
	return 0, false, false
}
func (s *libraryPreviewScheduler) finish(ctx context.Context, id int64, runErr error) {
	s.mu.Lock()
	st := s.states[id]
	if st != nil && st.missing && runErr != nil && ctx.Err() == nil {
		s.recordFailureLocked(id)
	} else if runErr == nil {
		delete(s.failures, id)
	}
	if st != nil && st.dirty {
		st.running, st.dirty, st.missing = false, false, false
		if len(s.pending) < s.maxPending {
			s.pending[id] = struct{}{}
		} else {
			st.deferred = true
		}
	} else {
		delete(s.states, id)
	}
	s.promoteDeferredLocked()
	more := len(s.pending) > 0
	s.mu.Unlock()
	if more {
		s.signal()
	}
}
func (s *libraryPreviewScheduler) recordFailureLocked(id int64) {
	now := s.options.Now()
	f := s.failures[id]
	f.attempts++
	delay := s.options.InitialRetry
	for i := 1; i < f.attempts && delay < s.options.MaxRetry; i++ {
		delay *= 2
		if delay > s.options.MaxRetry {
			delay = s.options.MaxRetry
		}
	}
	f.nextRetry = now.Add(delay)
	f.timeSeen = now
	s.failures[id] = f
	s.pruneFailuresLocked(now)
}
func (s *libraryPreviewScheduler) pruneFailuresLocked(now time.Time) {
	for id, f := range s.failures {
		if now.After(f.nextRetry.Add(s.options.MaxRetry)) {
			delete(s.failures, id)
		}
	}
	for len(s.failures) > s.options.MaxFailures {
		var oldestID int64
		var oldest time.Time
		for id, f := range s.failures {
			if oldest.IsZero() || f.timeSeen.Before(oldest) {
				oldestID, oldest = id, f.timeSeen
			}
		}
		delete(s.failures, oldestID)
	}
}
func (s *libraryPreviewScheduler) promoteDeferredLocked() {
	for len(s.pending) < s.maxPending {
		promoted := false
		for id, st := range s.states {
			if st.deferred {
				st.deferred = false
				s.pending[id] = struct{}{}
				promoted = true
				break
			}
		}
		if !promoted {
			return
		}
	}
}
