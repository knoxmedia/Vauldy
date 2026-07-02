package transcode

import "testing"

func TestSettingsFromOptionsJSON(t *testing.T) {
	raw := `{"transcoder":{"quality":"high","background_x264_preset":"slow","max_cpu_concurrent":"2","max_background_concurrent":"3"}}`
	s := SettingsFromOptionsJSON(raw)
	if s.Quality != "high" || s.BackgroundX264Preset != "slow" {
		t.Fatalf("unexpected settings: %+v", s)
	}
	if s.MaxCPUConcurrent != 2 || s.MaxBackgroundConcurrent != 3 {
		t.Fatalf("unexpected limits: %+v", s)
	}
	if s.InstantX264Preset() != "faster" || s.InstantCRF() != 20 {
		t.Fatalf("instant profile: preset=%q crf=%d", s.InstantX264Preset(), s.InstantCRF())
	}
	if s.EffectiveBackgroundPreset() != "slow" {
		t.Fatalf("background preset = %q", s.EffectiveBackgroundPreset())
	}
}

func TestBackgroundSlots(t *testing.T) {
	s := Settings{MaxBackgroundConcurrent: 3}
	if got := BackgroundSlots(s, 3, 5); got != 0 {
		t.Fatalf("background cap should block when running=3 max_bg=3, got %d", got)
	}
	if got := BackgroundSlots(s, 1, 5); got != 2 {
		t.Fatalf("expected bg slots=2, got %d", got)
	}
	unlimited := Settings{MaxBackgroundConcurrent: 0}
	if got := BackgroundSlots(unlimited, 1, 4); got != 4 {
		t.Fatalf("expected unlimited bg slots=4, got %d", got)
	}
}

func TestInstantSlots(t *testing.T) {
	s := Settings{MaxCPUConcurrent: 2}
	if InstantSlots(s, 2) {
		t.Fatal("should deny new instant session when active=2 max=2")
	}
	if !InstantSlots(s, 1) {
		t.Fatal("should allow instant session when active=1 max=2")
	}
	if !InstantSlots(Settings{MaxCPUConcurrent: 0}, 99) {
		t.Fatal("unlimited cpu should always allow")
	}
}
