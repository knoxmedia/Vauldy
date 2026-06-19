package subtitle

import (
	"strings"
	"testing"
)

func TestParseAndRenderVTT(t *testing.T) {
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello\n\n00:00:05.000 --> 00:00:07.000\nWorld\n"
	cues, format, err := ParseCues(raw, FormatVTT)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 || format != FormatVTT {
		t.Fatalf("unexpected cues: %+v format=%v", cues, format)
	}
	out := RenderCues(cues, FormatVTT)
	if !strings.Contains(out, "1\n00:00:01.000 --> 00:00:04.000\nHello") {
		t.Fatalf("render vtt: %q", out)
	}
}

func TestNormalizeForPowerPlayer(t *testing.T) {
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello\n"
	out, err := NormalizeForPowerPlayer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "WEBVTT") || !strings.Contains(out, "1\n00:00:01.000 --> 00:00:04.000\nHello") {
		t.Fatalf("normalize: %q", out)
	}
}

func TestParseMediaSubtitleVTTURL(t *testing.T) {
	mid, sid, ok := ParseMediaSubtitleVTTURL("/api/v1/media/12/subtitles/34/vtt?access_token=x")
	if !ok || mid != 12 || sid != 34 {
		t.Fatalf("parse failed: ok=%v mid=%d sid=%d", ok, mid, sid)
	}
}
