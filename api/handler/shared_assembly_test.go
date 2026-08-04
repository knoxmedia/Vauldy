package handler

import (
	"context"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/postingest"
	"knox-media/internal/scancoord"
)

type assemblySubmitter struct{}

func (*assemblySubmitter) Submit(context.Context, scancoord.ScanRequest) (scancoord.SubmitResult, error) {
	return scancoord.SubmitResult{}, nil
}
func (*assemblySubmitter) Cancel(context.Context, int64) (scancoord.CancelResult, error) {
	return scancoord.CancelResult{}, nil
}

func TestSharedResourceControlAssemblyHandlerKeepsInjectedInstances(t *testing.T) {
	coordinator := &assemblySubmitter{}
	queue := &postingest.Queue{}
	enqueuer := &postingest.Enqueuer{}
	dispatcher := &postingest.Dispatcher{}
	h := New(&app.App{}, Dependencies{Coordinator: coordinator, Queue: queue, PostIngest: enqueuer, Dispatcher: dispatcher})
	if h.ScanCoordinator != coordinator || h.Queue != queue || h.PostIngestEnqueuer != enqueuer || h.Dispatcher != dispatcher {
		t.Fatalf("handler did not preserve injected resource-control instances: %+v", h)
	}
}
