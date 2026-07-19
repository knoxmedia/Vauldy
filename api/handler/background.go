package handler

import (
	"context"
	"sync"
)

// BackgroundGroup owns server-scoped background loops registered during router construction.
type BackgroundGroup struct {
	wg sync.WaitGroup
}

func (g *BackgroundGroup) Go(ctx context.Context, loop func(context.Context)) {
	if g == nil || ctx == nil || loop == nil {
		panic("handler: valid background group, context, and loop are required")
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		loop(ctx)
	}()
}

func (g *BackgroundGroup) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
