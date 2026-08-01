package fileutil

import "testing"

func TestConfigureReplacesVideoKeepsOtherDefaults(t *testing.T) {
	t.Cleanup(ResetForTest)
	video := []string{"mp4", ".TS"}
	if err := Configure(ExtensionConfig{Video: &video}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("a.mp4") != "video" || GuessFileType("a.ts") != "video" {
		t.Fatal("expected customized video")
	}
	if GuessFileType("a.mkv") != "other" {
		t.Fatalf("mkv should be other after replace, got %q", GuessFileType("a.mkv"))
	}
	if GuessFileType("a.mp3") != "audio" {
		t.Fatal("audio defaults must remain")
	}
}

func TestConfigureEmptyAudioDisablesAudio(t *testing.T) {
	t.Cleanup(ResetForTest)
	empty := []string{}
	if err := Configure(ExtensionConfig{Audio: &empty}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("a.mp3") != "other" {
		t.Fatal("empty audio list must match nothing")
	}
}

func TestConfigureRejectsBlankEntry(t *testing.T) {
	t.Cleanup(ResetForTest)
	bad := []string{"mp4", "  "}
	if err := Configure(ExtensionConfig{Video: &bad}); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigureDuplicatePrefersVideo(t *testing.T) {
	t.Cleanup(ResetForTest)
	video := []string{".dat"}
	audio := []string{".dat"}
	if err := Configure(ExtensionConfig{Video: &video, Audio: &audio}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("x.dat") != "video" {
		t.Fatalf("got %q", GuessFileType("x.dat"))
	}
}
