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
	info := &VideoInfo{
		Duration:   30.0,
		VideoCodec: "h264",
		AudioCodec: "aac",
	}
	idx, err := w.generateSegmentIndex("dense-kf", info)
	if err != nil {
		t.Fatalf("generateSegmentIndex failed: %v", err)
	}
	for i, seg := range idx.VideoSegments {
		if seg.Duration < 5.0 {
			t.Fatalf("segment[%d] duration=%.3f, want >= 5.0s (fixed 6s grid)", i, seg.Duration)
		}
		if seg.Duration > 7.0 {
			t.Fatalf("segment[%d] duration=%.3f, want <= 7.0s", i, seg.Duration)
		}
	}
	// With duration=30s and target segment=6s, expect exactly 5 segments.
	if len(idx.VideoSegments) != 5 {
		t.Fatalf("got %d segments, want 5 for 30s source", len(idx.VideoSegments))
	}
}

func TestGenerateSegmentIndex_NoAudioCodecDoesNotCreateAudioSegments(t *testing.T) {
	w := &SliceWorker{}
	info := &VideoInfo{
		Duration:   12.0,
		VideoCodec: "h264",
		AudioCodec: "",
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
