package transcodeworker

import (
	"context"
	"strings"
	"testing"

	models "knox-media/internal/model"
	"knox-media/internal/jit/hwenc"
)

func TestAudioOutputArgs_AACSourceCopy(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "video:meta:f-aac", "audio_codec", "aac").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := w.audioOutputArgs(&models.TranscodeTask{FileID: "f-aac"})
	if !equalStrings(got, []string{"-c:a", "copy", "-bsf:a", "aac_adtstoasc"}) {
		t.Fatalf("aac source args=%v, want copy + adtstoasc", got)
	}
}

func TestAudioOutputArgs_NonAACSourceTranscodes(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	_ = rdb.HSet(ctx, "video:meta:f-eac3", "audio_codec", "eac3").Err()
	got := w.audioOutputArgs(&models.TranscodeTask{FileID: "f-eac3"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-c:a aac") || !strings.Contains(joined, "-b:a 128k") {
		t.Fatalf("eac3 args=%v, want aac re-encode", got)
	}
}

func TestAudioOutputArgs_NoAudioCodecYieldsAN(t *testing.T) {
	w, _, _ := newTestTranscodeWorker(t)
	got := w.audioOutputArgs(&models.TranscodeTask{FileID: "f-noaudio"})
	if !equalStrings(got, []string{"-an"}) {
		t.Fatalf("no audio_codec args=%v, want -an", got)
	}
}

func TestBuildTranscodeArgs_PassthroughIncludesAudioMap(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	_ = rdb.HSet(ctx, "video:meta:f-pt", "codec", "h264", "height", "1080", "audio_codec", "aac").Err()

	task := &models.TranscodeTask{
		FileID:     "f-pt",
		Bitrate:    "4000k",
		Resolution: "1920x1080",
	}
	args := w.buildTranscodeArgs("/in.mp4", "/out.ts", task, 0, 0, hwenc.Libx264)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Fatalf("expected -c:v copy in args; got %s", joined)
	}
	if !strings.Contains(joined, "-map 0:a:0?") {
		t.Fatalf("expected audio map in args; got %s", joined)
	}
	if !strings.Contains(joined, "-c:a copy") {
		t.Fatalf("expected -c:a copy for AAC source; got %s", joined)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
