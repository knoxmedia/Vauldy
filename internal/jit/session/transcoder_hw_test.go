package session

import (
	"testing"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/storage"
)

func TestEncryptedPipeUsesHWEncodeOnlyWithoutInputAccel(t *testing.T) {
	in := &storage.FFmpegInput{FromEnc: true}
	localFile := in.Path != "" && !in.FromEnc
	pipeline := hwenc.PipelineModeForInput(true, localFile)
	if pipeline != hwenc.PipelineHWEncodeOnly {
		t.Fatalf("pipeline=%v want HWEncodeOnly", pipeline)
	}
	if pipeline == hwenc.PipelineHWFull {
		t.Fatal("encrypted pipe must not use full HW decode pipeline")
	}
}
