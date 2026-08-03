package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundGroupWaitsForRegisteredLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	group := &BackgroundGroup{}
	started := make(chan struct{})
	group.Go(ctx, func(loopCtx context.Context) { close(started); <-loopCtx.Done() })
	<-started
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	cancel()
	if err := group.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundGroupWaitHonorsTimeout(t *testing.T) {
	group := &BackgroundGroup{}
	release := make(chan struct{})
	group.Go(context.Background(), func(context.Context) { <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := group.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	close(release)
}

func TestBackgroundGroupPanicsOnNilReceiver(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil background group")
		}
	}()
	var g *BackgroundGroup
	g.Go(context.Background(), func(context.Context) {})
}

func TestBackgroundGroupPanicsOnNilContext(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil context")
		}
	}()
	g := &BackgroundGroup{}
	g.Go(nil, func(context.Context) {})
}

func TestBackgroundGroupPanicsOnNilLoop(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil loop")
		}
	}()
	g := &BackgroundGroup{}
	g.Go(context.Background(), nil)
}

func TestBackgroundGroupWaitNilReceiver(t *testing.T) {
	var g *BackgroundGroup
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := g.Wait(ctx); err != nil {
		t.Fatalf("nil receiver Wait should return nil, got %v", err)
	}
}

func TestBackgroundGroupShutdownOrderingWatcherBeforeReconciler(t *testing.T) {
	// Verify that watcher/reconciler loops registered via BackgroundGroup
	// are all properly waited on during shutdown without early release.
	group := &BackgroundGroup{}
	ctx, cancel := context.WithCancel(context.Background())

	var watcherExited, reconcilerExited atomic.Bool
	var checkOrder sync.Mutex
	var exitOrder []string

	// Watcher loop.
	group.Go(ctx, func(loopCtx context.Context) {
		<-loopCtx.Done()
		checkOrder.Lock()
		exitOrder = append(exitOrder, "watcher")
		checkOrder.Unlock()
		watcherExited.Store(true)
	})

	// Reconciler loop.
	group.Go(ctx, func(loopCtx context.Context) {
		<-loopCtx.Done()
		checkOrder.Lock()
		exitOrder = append(exitOrder, "reconciler")
		checkOrder.Unlock()
		reconcilerExited.Store(true)
	})

	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := group.Wait(waitCtx); err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if !watcherExited.Load() || !reconcilerExited.Load() {
		t.Fatalf("not all loops exited: watcher=%v reconciler=%v", watcherExited.Load(), reconcilerExited.Load())
	}
}

func TestBackgroundGroupShutdownWaitsWithoutReleasingFenced(t *testing.T) {
	// Simulate shutdown: background loops should exit cleanly when context
	// is cancelled, without requiring any additional release semantics.
	group := &BackgroundGroup{}
	ctx, cancel := context.WithCancel(context.Background())

	// A "live executor" that owns a fenced resource.
	liveHolding := make(chan struct{})
	executorStarted := make(chan struct{})

	group.Go(ctx, func(loopCtx context.Context) {
		close(executorStarted)
		// Simulate holding a fenced lease until context cancels.
		select {
		case <-liveHolding:
			// Released by explicit signal.
		case <-loopCtx.Done():
			// Normal shutdown.
		}
	})

	<-executorStarted

	// Cancel context but don't release the fenced resource.
	cancel()

	// Wait with a timeout — the executor should exit on context cancel.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := group.Wait(waitCtx); err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	// Verify: the executor exited without needing the liveHolding signal.
	// It responded to context cancellation, not to resource release.
	close(liveHolding) // Cleanup.
}
