package pretranscode

import (
	"path/filepath"
	"testing"
)

func TestComputeTaskOutputRootSource(t *testing.T) {
	got := ComputeTaskOutputRoot(TaskOutputRootInput{
		Mode:       OutputDirModeSource,
		FileID:     "fid-1",
		PresetID:   2,
		SourcePath: `F:\videos\movie.mp4`,
	})
	want := filepath.Join(`F:\videos`, "movie.pretranscode", "preset2")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestComputeTaskOutputRootCustom(t *testing.T) {
	got := ComputeTaskOutputRoot(TaskOutputRootInput{
		Mode:      OutputDirModeCustom,
		CustomDir: `D:\transcodes`,
		FileID:    "fid-1",
		PresetID:  3,
	})
	want := filepath.Join(`D:\transcodes`, "fid-1", "preset3")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestComputeTaskOutputRootFallbackData(t *testing.T) {
	got := ComputeTaskOutputRoot(TaskOutputRootInput{
		Mode:         OutputDirModeSource,
		TranscodeDir: `/data/transcode`,
		FileID:       "fid-1",
		PresetID:     1,
	})
	want := filepath.Join(`/data/transcode`, "fid-1", "preset1")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenditionDeletePathHLS(t *testing.T) {
	playlist := filepath.Join(`F:\out`, "480p", "480p.m3u8")
	got := RenditionDeletePath(playlist, "hls")
	want := filepath.Join(`F:\out`, "480p")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenditionDeletePathMP4(t *testing.T) {
	file := filepath.Join(`F:\out`, "720p.mp4")
	got := RenditionDeletePath(file, "mp4")
	if got != file {
		t.Fatalf("got %q want %q", got, file)
	}
}
