package transcodeworker

import (
	"context"
	"testing"

	models "knox-media/internal/model"
)

func TestShouldDeferLookahead_TooFarAhead(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "session:s1", "current_segment", "1").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	task := &models.TranscodeTask{FileID: "f", SegmentID: 20, SessionID: "s1"}
	if !w.shouldDeferLookahead(task) {
		t.Fatalf("expected defer when segment >> current_segment")
	}
}

func TestShouldDeferLookahead_WithinWindow(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "session:s2", "current_segment", "5").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	task := &models.TranscodeTask{FileID: "f", SegmentID: 8, SessionID: "s2"}
	if w.shouldDeferLookahead(task) {
		t.Fatalf("did not expect defer when within window")
	}
}

func TestShouldDeferLookahead_SeekBoostBypassesThrottle(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	_ = rdb.HSet(ctx, "session:s3", "current_segment", "0").Err()
	_ = rdb.Set(ctx, "jit:session_seek_boost:s3", "1", 0).Err()
	task := &models.TranscodeTask{FileID: "f", SegmentID: 50, SessionID: "s3"}
	if w.shouldDeferLookahead(task) {
		t.Fatalf("seek boost should bypass throttle")
	}
}

func TestShouldDeferLookahead_PrefetchUsesAllSessions(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "session:viewer-a", "file_id", "f", "current_segment", "10").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	task := &models.TranscodeTask{FileID: "f", SegmentID: 30, SessionID: "prefetch"}
	if !w.shouldDeferLookahead(task) {
		t.Fatalf("expected prefetch to defer when all viewers behind by > lookahead")
	}
}

func TestCanVideoPassthrough_H264WithMatchingHeight(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "video:meta:f", "codec", "h264", "height", "1080").Err(); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	task := &models.TranscodeTask{FileID: "f", Bitrate: "4000k", Resolution: "1920x1080"}
	if !w.canVideoPassthrough(task) {
		t.Fatalf("expected passthrough when source is h264 and target matches resolution")
	}
}

func TestCanVideoPassthrough_RefusesHEVCSource(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "video:meta:fhevc", "codec", "hevc", "height", "1080").Err(); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	task := &models.TranscodeTask{FileID: "fhevc", Bitrate: "4000k", Resolution: "1920x1080"}
	if w.canVideoPassthrough(task) {
		t.Fatalf("expected re-encode for hevc source")
	}
}

func TestCanVideoPassthrough_RefusesWhenScalingDown(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	_ = rdb.HSet(ctx, "video:meta:fdown", "codec", "h264", "height", "1080").Err()
	task := &models.TranscodeTask{FileID: "fdown", Bitrate: "500k", Resolution: "640x360"}
	if w.canVideoPassthrough(task) {
		t.Fatalf("expected re-encode when ABR ladder downscales")
	}
}
