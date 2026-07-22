package postingest

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"knox-media/internal/publication"
)

type callbackEnqueuerFunc func(context.Context, int64, *int64, string) ([]TaskType, error)

func (f callbackEnqueuerFunc) EnqueueMedia(ctx context.Context, mediaID int64, scanTaskID *int64, fileType string) ([]TaskType, error) {
	return f(ctx, mediaID, scanTaskID, fileType)
}

func TestNewScanMediaAddedEnqueueCallbackOnlyEnqueues(t *testing.T) {
	want := errors.New("enqueue failed")
	calls := 0
	callback := NewScanMediaAddedEnqueueCallback(callbackEnqueuerFunc(func(_ context.Context, mediaID int64, scanTaskID *int64, fileType string) ([]TaskType, error) {
		calls++
		if mediaID != 42 || scanTaskID == nil || *scanTaskID != 7 || fileType != "video" {
			t.Fatalf("media=%d scan=%v type=%q", mediaID, scanTaskID, fileType)
		}
		return nil, want
	}))
	if err := callback(context.Background(), 7, 42, "title", "video"); !errors.Is(err, want) {
		t.Fatalf("callback error=%v want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("enqueue calls=%d want 1", calls)
	}
}

func TestNewScanMediaAddedCallbackUsesProvidedCallback(t *testing.T) {
	called := 0
	callback := NewScanMediaAddedEnqueueCallback(callbackEnqueuerFunc(func(context.Context, int64, *int64, string) ([]TaskType, error) { called++; return nil, nil }))
	if err := callback(context.Background(), 7, 42, "movie", "video"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("calls=%d want 1", called)
	}
}

type callbackPlannerFunc func(context.Context, *sql.Tx, publication.NewMedia) (publication.Run, error)

func (f callbackPlannerFunc) PlanNewMediaTx(ctx context.Context, tx *sql.Tx, media publication.NewMedia) (publication.Run, error) {
	return f(ctx, tx, media)
}

func TestNewScanMediaDiscoveredTxCallbackPlansInCallerTransaction(t *testing.T) {
	want := errors.New("plan failed")
	calls := 0
	callback := NewScanMediaDiscoveredTxCallback(callbackPlannerFunc(func(_ context.Context, tx *sql.Tx, media publication.NewMedia) (publication.Run, error) {
		calls++
		if media.MediaID != 42 || media.ScanTaskID != 7 || media.FileType != "video" {
			t.Fatalf("tx=%v media=%+v", tx, media)
		}
		return publication.Run{}, want
	}))
	if err := callback(context.Background(), nil, 7, 42, "title", "video"); !errors.Is(err, want) {
		t.Fatalf("callback error=%v want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("planner calls=%d want 1", calls)
	}
}
