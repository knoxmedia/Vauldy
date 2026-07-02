package scheduler

import (
	"strings"
	"testing"

	models "knox-media/internal/model"
)

func TestAdaptiveLadder_FiltersBySourceHeight(t *testing.T) {
	got := adaptiveLadder(720, 0)
	if len(got) == 0 {
		t.Fatalf("expected non-empty ladder")
	}
	for _, v := range got {
		if v.Height > 720 {
			t.Fatalf("variant %s height=%d exceeds source 720", v.Name, v.Height)
		}
	}
	if got[0].Height != 720 {
		t.Fatalf("expected top variant=720, got %d", got[0].Height)
	}
}

func TestAdaptiveLadder_FiltersByClientMaxHeight(t *testing.T) {
	got := adaptiveLadder(2160, 720)
	if len(got) == 0 {
		t.Fatalf("expected non-empty ladder")
	}
	for _, v := range got {
		if v.Height > 720 {
			t.Fatalf("client max=720 but got %d", v.Height)
		}
	}
}

func TestAdaptiveLadder_UnknownDefaultsToFull(t *testing.T) {
	got := adaptiveLadder(0, 0)
	if len(got) != len(builtinLadder) {
		t.Fatalf("expected full ladder when source unknown, got %d", len(got))
	}
}

func TestTargetDuration_RoundsUpFromMaxSegment(t *testing.T) {
	segs := []models.VideoSegmentInfo{
		{Duration: 5.4},
		{Duration: 6.7},
		{Duration: 4.0},
	}
	if got := targetDuration(segs); got != 7 {
		t.Fatalf("targetDuration=%d, want 7", got)
	}
}

func TestTargetDurationAudio_RoundsUpFromMaxSegment(t *testing.T) {
	segs := []models.AudioSegmentInfo{
		{Duration: 5.4},
		{Duration: 6.1},
	}
	if got := targetDurationAudio(segs); got != 7 {
		t.Fatalf("targetDurationAudio=%d, want 7", got)
	}
}

func TestAudioLanguages_DedupesAndDefaultsToUnd(t *testing.T) {
	segs := []models.AudioSegmentInfo{
		{Language: "eng"},
		{Language: ""},
		{Language: "eng"},
		{Language: "chi"},
	}
	got := audioLanguages(segs)
	want := []string{"eng", "und", "chi"}
	if len(got) != len(want) {
		t.Fatalf("languages=%v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("languages[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderMediaPlaylist_HasRequiredHeaders(t *testing.T) {
	out := renderMediaPlaylist(7, []segmentEntry{
		{Duration: 5.4, URL: "/seg/0"},
		{Duration: 6.0, URL: "/seg/1"},
	})
	for _, want := range []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:7", "#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "/seg/0", "/seg/1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("playlist missing %q\n%s", want, out)
		}
	}
}

func TestVideoCodecsAttr_PassthroughHEVC(t *testing.T) {
	got := videoCodecsAttr(true, "hevc")
	if !strings.Contains(got, "hvc1") {
		t.Fatalf("hevc passthrough codecs=%q, expected hvc1.* prefix", got)
	}
}

func TestVideoCodecsAttr_DefaultIsAVC(t *testing.T) {
	got := videoCodecsAttr(false, "h264")
	if !strings.HasPrefix(got, "avc1.") {
		t.Fatalf("default codecs=%q, expected avc1.*", got)
	}
}

func TestFormatBitrate_ParsesKAndM(t *testing.T) {
	cases := map[string]int{
		"500k":  500000,
		"2M":    2000000,
		"trash": 0,
	}
	for in, want := range cases {
		if got := formatBitrate(in); got != want {
			t.Fatalf("formatBitrate(%q)=%d, want %d", in, got, want)
		}
	}
}
