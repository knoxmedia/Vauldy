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
	for i, seg := range idx.AudioSegments {
		if seg.Status != "pending" {
			t.Fatalf("audio segment[%d] status=%q, want pending before physical slicing", i, seg.Status)
		}
		if seg.Language != "eng" {
			t.Fatalf("audio segment[%d] language=%q, want eng", i, seg.Language)
		}
		if !strings.HasPrefix(seg.SlicePath, "raw/audio/file-a/segment_") {
			t.Fatalf("audio segment[%d] unexpected slice path=%q", i, seg.SlicePath)
		}
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
