package handler

import (
	"os"
	"strings"
	"testing"
)

func TestPreviewAndKeyframeAPIsDoNotDirectlyFanOutWorkers(t *testing.T) {
	for _, name := range []string{"preview_task.go", "keyframe.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{".RunBatch(", "go func()", ".Run(context.Background()"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s retains direct worker fan-out %q", name, forbidden)
			}
		}
	}
}

func TestManualSubtitleHandlersEnqueuePostIngestOnly(t *testing.T) {
	rawSub, err := os.ReadFile("subtitle.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawSub), ".RunBatch(") || strings.Contains(string(rawSub), ".ProcessMedia(") {
		t.Fatal("subtitle.go must not sync-process subtitles")
	}
	if !strings.Contains(string(rawSub), "enqueueExplicitPostIngest") {
		t.Fatal("ProcessMediaSubtitles must enqueue post_ingest")
	}
	raw, err := os.ReadFile("subtitle_task.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, fn := range []string{"func (h *Handler) ResetSubtitleTask", "func (h *Handler) RetrySubtitleTask", "func (h *Handler) EnqueueSubtitleProcessing"} {
		idx := strings.Index(src, fn)
		if idx < 0 {
			t.Fatalf("missing %s", fn)
		}
		chunk := src[idx:]
		if end := strings.Index(chunk[len(fn):], "\nfunc (h *Handler)"); end >= 0 {
			chunk = chunk[:len(fn)+end]
		}
		if !strings.Contains(chunk, "enqueueExplicitPostIngest") {
			t.Fatalf("%s must call enqueueExplicitPostIngest", fn)
		}
		if strings.Contains(chunk, "RunBatch(") || strings.Contains(chunk, "ProcessMedia(") {
			t.Fatalf("%s must not sync-process subtitles", fn)
		}
	}
}
