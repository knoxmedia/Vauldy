package photoclass

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestColorTagsWarm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warm.png")
	if err := writeSolidPNG(path, 200, 200, color.RGBA{R: 220, G: 120, B: 60, A: 255}); err != nil {
		t.Fatal(err)
	}
	stats, ok := analyzeColor(path)
	if !ok {
		t.Fatal("analyze failed")
	}
	tags := colorTagsFromStats(stats)
	found := false
	for _, tag := range tags {
		if tag == "暖色系" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tags=%v", tags)
	}
}

func TestClassifyHeuristicNight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "night.png")
	if err := writeSolidPNG(path, 100, 100, color.RGBA{R: 10, G: 12, B: 20, A: 255}); err != nil {
		t.Fatal(err)
	}
	stats, ok := analyzeColor(path)
	if !ok {
		t.Fatal("analyze failed")
	}
	res := classifyHeuristic(Input{FilePath: path, Title: "night-shot"}, stats, true)
	found := false
	for _, tag := range res.Tags {
		if tag == "夜景" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tags=%v", res.Tags)
	}
}

func TestMergeTagsIntoMetaJSON(t *testing.T) {
	out := MergeTagsIntoMetaJSON("", []string{"风景", "暖色系"}, "heuristic", false, nil)
	if !containsAll(out, "风景", "暖色系") {
		t.Fatalf("out=%s", out)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func writeSolidPNG(path string, w, h int, c color.Color) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
