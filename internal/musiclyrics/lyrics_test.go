package musiclyrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSidecarLRC(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	lrc := filepath.Join(dir, "song.lrc")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "[00:12.00]Hello world\n[00:18.00]Second line"
	if err := os.WriteFile(lrc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, source, ok := Load(audio, "", "")
	if !ok {
		t.Fatal("expected lyrics")
	}
	if source != SourceSidecar {
		t.Fatalf("source=%q want file", source)
	}
	if got != content {
		t.Fatalf("content=%q", got)
	}
}

func TestEmbeddedFromMetaJSON(t *testing.T) {
	meta := `{"format":{"tags":{"lyrics":"[00:01.00]Line one\n[00:02.00]Line two"}}}`
	got, source, ok := Load("/missing.mp3", meta, "")
	if !ok {
		t.Fatal("expected embedded lyrics")
	}
	if source != SourceEmbedded {
		t.Fatalf("source=%q", source)
	}
	if !strings.Contains(got, "Line one") {
		t.Fatalf("got=%q", got)
	}
}
