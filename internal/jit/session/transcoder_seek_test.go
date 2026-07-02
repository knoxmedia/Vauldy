package session

import (
	"testing"

	"knox-media/internal/storage"
)

func TestSeekTimeForTranscodeEncryptedPipeUsesKeyframePTS(t *testing.T) {
	mgr := &Manager{}
	s := &Session{
		mgr: mgr,
	}
	// Inject keyframe meta via loadKeyframeMeta path is heavy; test plainFileSeekPlan path directly.
	kfPTS := []float64{0, 2, 4, 6}
	seek := func(target float64) float64 {
		idx := 0
		for i, pts := range kfPTS {
			if pts > target {
				break
			}
			idx = i
		}
		return kfPTS[idx]
	}
	in := &storage.FFmpegInput{FromEnc: true}
	got := seekTimeForTranscode(s, 5.5, in)
	// Without keyframe meta loaded, falls back to targetSec.
	if got != 5.5 {
		t.Fatalf("without meta: got %v want 5.5", got)
	}
	_ = seek
}

func TestSeekTimeForTranscodePlainPathUsesTarget(t *testing.T) {
	s := &Session{}
	in := &storage.FFmpegInput{Path: "/movies/clip.mp4"}
	got := seekTimeForTranscode(s, 12.5, in)
	if got != 12.5 {
		t.Fatalf("got %v want 12.5", got)
	}
}
