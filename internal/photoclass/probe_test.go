package photoclass

import (
	"context"
	"path/filepath"
	"testing"

	"knox-media/internal/config"
)

func TestCheckPhotoClassifyConfigHeuristic(t *testing.T) {
	mediaRoot := filepath.Join("..", "..")
	cfg := config.PhotoClassifyConfig{
		Engine:     "heuristic",
		ScriptPath: "tools/photo_classify/classify.py",
	}
	r := CheckPhotoClassifyConfig(context.Background(), mediaRoot, cfg)
	if !r.OK {
		t.Fatalf("got %q", r.Message)
	}
}

func TestCheckPhotoClassifyConfigBadEngine(t *testing.T) {
	r := CheckPhotoClassifyConfig(context.Background(), "", config.PhotoClassifyConfig{Engine: "bad"})
	if r.OK {
		t.Fatal("expected failure")
	}
}
