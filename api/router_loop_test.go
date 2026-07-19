package api

import (
	"os"
	"strings"
	"testing"
)

func TestNewEngineDoesNotStartLegacyPreviewOrKeyframeLoops(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, call := range []string{"go h.StartPreviewTaskLoop(", "go h.StartKeyframeTaskLoop("} {
		if strings.Contains(source, call) {
			t.Fatalf("NewEngine still starts legacy loop %q", call)
		}
	}
}
