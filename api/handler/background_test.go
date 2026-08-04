package handler

import (
	"context"
	"errors"
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
