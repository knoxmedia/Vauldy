package postingest

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/scanner"
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
		if !media.MetadataAttempt.Attempted || len(media.MetadataAttempt.Fields) != 1 || media.MetadataAttempt.Fields[0] != "duration" || len(media.MetadataAttempt.Errors) != 1 || media.MetadataAttempt.Errors[0].Source != "ffprobe" {
			t.Fatalf("metadata=%+v", media.MetadataAttempt)
		}
		return publication.Run{}, want
	}))
	if err := callback(context.Background(), nil, 7, scanner.ScanDiscovery{MediaID: 42, Title: "title", FileType: "video", MetadataAttempt: scanner.MetadataAttempt{Attempted: true, Fields: []string{"duration"}, Errors: []scanner.MetadataDiagnostic{{Source: "ffprobe", Message: "partial"}}}}); !errors.Is(err, want) {
		t.Fatalf("callback error=%v want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("planner calls=%d want 1", calls)
	}
}

func TestNewScanMediaDiscoveredFinalizerNonVideoNoOp(t *testing.T) {
	callback := NewScanMediaDiscoveredFinalizer(PreCaptureConfig{})
	if err := callback(context.Background(), 7, scanner.ScanDiscovery{MediaID: 42, Title: "title", FileType: "image"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewScanMediaDiscoveredFinalizerRejectsCaptureFailure(t *testing.T) {
	db, _, _, run := seedPreCapturePlan(t)
	callback := NewScanMediaDiscoveredFinalizer(PreCaptureConfig{DB: db})
	if err := callback(context.Background(), run.ScanTaskID, scanner.ScanDiscovery{MediaID: run.MediaID, Title: "title", FileType: "video"}); err == nil {
		t.Fatal("capture failure was not returned")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE id=?`, run.MediaID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed media count=%d want 0", count)
	}
}
