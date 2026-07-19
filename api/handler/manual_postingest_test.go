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
