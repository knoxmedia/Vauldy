package postingest

import (
	"context"
	"errors"
	"testing"
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
