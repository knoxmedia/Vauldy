package pretranscode

import (
	"strings"
	"testing"
)

func TestAdaptRenditionForSourcePortraitSwaps720p(t *testing.T) {
	t.Parallel()

	r := Rendition{Name: "720p", Height: 720}
	got := AdaptRenditionForSource(r, 720, 1280)
	if got.Width != 720 || got.Height != 1280 {
		t.Fatalf("got %dx%d want 720x1280", got.Width, got.Height)
	}
}

func TestAdaptRenditionForSourcePortraitCustom1280x720(t *testing.T) {
	t.Parallel()

	r := Rendition{Name: "custom", Height: 720, Width: 1280}
	got := AdaptRenditionForSource(r, 1080, 1920)
	if got.Width != 720 || got.Height != 1280 {
		t.Fatalf("got %dx%d want 720x1280", got.Width, got.Height)
	}
}

func TestAdaptRenditionForSourceLandscapeUnchanged(t *testing.T) {
	t.Parallel()

	r := Rendition{Name: "720p", Height: 720}
	got := AdaptRenditionForSource(r, 1920, 1080)
	if got.Width != 0 || got.Height != 720 {
		t.Fatalf("expected unchanged rendition, got %+v", got)
	}
}

func TestShouldSkipRenditionAboveSourcePortrait1080pOn960(t *testing.T) {
	t.Parallel()

	r := Rendition{Name: "1080p", Height: 1080}
	if !ShouldSkipRenditionAboveSource(r, 540, 960) {
		t.Fatal("expected 1080p to be skipped for 540x960 portrait source")
	}
}

func TestShouldSkipRenditionAboveSourcePortrait720pOn1280(t *testing.T) {
	t.Parallel()

	r := Rendition{Name: "720p", Height: 720}
	if ShouldSkipRenditionAboveSource(r, 720, 1280) {
		t.Fatal("expected adapted 720x1280 to fit 720x1280 source")
	}
}

func TestBuildHLSArgsUsesAdaptedPortraitHeight(t *testing.T) {
	t.Parallel()

	p := &Preset{VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23, AudioCodec: "aac"}
	r := AdaptRenditionForSource(Rendition{Name: "720p", Height: 720}, 720, 1280)
	got := BuildHLSArgs(t.TempDir(), p, &r, nil, "")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	if !strings.Contains(joined, "scale=-2:1280") {
		t.Fatalf("expected portrait scale height 1280, got: %s", joined)
	}
}
