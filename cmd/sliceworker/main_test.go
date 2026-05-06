package sliceworker

import (
	"strings"
	"testing"
)

func TestGenerateSegmentIndex_SeparatesAudioSegments(t *testing.T) {
	w := &SliceWorker{}
	info := &VideoInfo{
		Duration:   18.3,
		VideoCodec: "h264",
		AudioCodec: "aac",
		Keyframes:  []float64{6.0, 12.0, 18.0},
	}

	idx, err := w.generateSegmentIndex("file-a", info)
	if err != nil {
		t.Fatalf("generateSegmentIndex failed: %v", err)
	}
	if idx.Status != "slicing" {
		t.Fatalf("index status=%q, want slicing before save", idx.Status)
	}
	if len(idx.VideoSegments) == 0 {
		t.Fatalf("expected video segments")
	}
	if len(idx.AudioSegments) == 0 {
		t.Fatalf("expected audio segments when source has audio codec")
	}
	for i, seg := range idx.VideoSegments {
		if seg.Status != "indexed" {
			t.Fatalf("video segment[%d] status=%q, want indexed", i, seg.Status)
		}
		if strings.TrimSpace(seg.SlicePath) != "" {
			t.Fatalf("video segment[%d] should be virtual indexed slice, got path=%q", i, seg.SlicePath)
		}
		if seg.Duration <= 0 {
			t.Fatalf("video segment[%d] duration must be positive, got %.3f", i, seg.Duration)
		}
	}
	if len(idx.AudioSegments) != len(idx.VideoSegments) {
		t.Fatalf("audio segments=%d, want one-per-video=%d (combined transcode)",
			len(idx.AudioSegments), len(idx.VideoSegments))
	}
	for i, seg := range idx.AudioSegments {
		if seg.Status != "indexed" {
			t.Fatalf("audio segment[%d] status=%q, want indexed (no physical pre-slicing)", i, seg.Status)
		}
		if strings.TrimSpace(seg.SlicePath) != "" {
			t.Fatalf("audio segment[%d] should be virtual, got path=%q", i, seg.SlicePath)
		}
		if i < len(idx.VideoSegments) {
			if seg.StartTime != idx.VideoSegments[i].StartTime {
				t.Fatalf("audio segment[%d] start=%v, want match video=%v",
					i, seg.StartTime, idx.VideoSegments[i].StartTime)
			}
		}
	}
}

func TestGenerateSegmentIndex_DenseKeyframesAvoidTinySegments(t *testing.T) {
	w := &SliceWorker{}
	// Simulate a dense-GOP video with keyframes every ~1 second.
	kfs := []float64{0.033}
	for i := 1.0; i < 30.0; i += 1.0 {
		kfs = append(kfs, i)
	}
	info := &VideoInfo{
		Duration:   30.0,
		VideoCodec: "h264",
		AudioCodec: "aac",
		Keyframes:  kfs,
	}
	idx, err := w.generateSegmentIndex("dense-kf", info)
	if err != nil {
		t.Fatalf("generateSegmentIndex failed: %v", err)
	}
	for i, seg := range idx.VideoSegments {
		if seg.Duration < 2.0 {
			t.Fatalf("segment[%d] duration=%.3f, want >= 2.0s (dense GOP should not produce tiny segments)", i, seg.Duration)
		}
		if seg.Duration > 8.0 {
			t.Fatalf("segment[%d] duration=%.3f, want <= 8.0s (should stay near target 6s)", i, seg.Duration)
		}
	}
	// With duration=30s and target segment=6s, expect ~5 segments.
	if len(idx.VideoSegments) < 3 || len(idx.VideoSegments) > 8 {
		t.Fatalf("got %d segments, want 3-8 for 30s source", len(idx.VideoSegments))
	}
}

func TestGenerateSegmentIndex_NoAudioCodecDoesNotCreateAudioSegments(t *testing.T) {
	w := &SliceWorker{}
	info := &VideoInfo{
		Duration:   12.0,
		VideoCodec: "h264",
		AudioCodec: "",
		Keyframes:  []float64{6.0, 12.0},
	}

	idx, err := w.generateSegmentIndex("file-no-audio", info)
	if err != nil {
		t.Fatalf("generateSegmentIndex failed: %v", err)
	}
	if len(idx.VideoSegments) == 0 {
		t.Fatalf("expected video segments")
	}
	if len(idx.AudioSegments) != 0 {
		t.Fatalf("audio segments=%d, want 0 for source without audio track", len(idx.AudioSegments))
	}
}
