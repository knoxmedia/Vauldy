package keyframes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseKeyframePackets_FiltersNonKeyframes(t *testing.T) {
	csv := "0.000000,K_\n0.040000,__\n0.080000,__\n2.000000,K_\n,K_\n3.000000,K\n"
	got := parseKeyframePackets(csv)
	want := []float64{0.000000, 2.000000, 3.000000}
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("[%d]=%v want %v", i, got[i], want[i])
		}
	}
}

func TestCache_SaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(dir, "/usr/bin/ffprobe")
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	src := filepath.Join(dir, "src.mp4")
	if err := os.WriteFile(src, []byte("dummy-source"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat src: %v", err)
	}

	m := &Meta{
		FileID:   "abc-123",
		FilePath: src,
		SrcSize:  st.Size(),
		SrcMTime: st.ModTime().Unix(),
		Duration: 10,
		PTS:      []float64{0, 2, 4, 6, 8, 10},
	}
	if err := c.Save(m); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := c.Load("abc-123", src)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.PTS) != len(m.PTS) {
		t.Fatalf("loaded meta mismatch: %+v", got)
	}
	for i := range m.PTS {
		if got.PTS[i] != m.PTS[i] {
			t.Fatalf("pts[%d]=%v want %v", i, got.PTS[i], m.PTS[i])
		}
	}
}

func TestCache_LoadInvalidatesOnSizeChange(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(dir, "/usr/bin/ffprobe")
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	src := filepath.Join(dir, "src.mp4")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	st, _ := os.Stat(src)
	m := &Meta{
		FileID: "f", FilePath: src,
		SrcSize: st.Size(), SrcMTime: st.ModTime().Unix(),
		Duration: 1, PTS: []float64{0},
	}
	if err := c.Save(m); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Replace contents -> size changes
	if err := os.WriteFile(src, []byte("v1-much-longer"), 0o644); err != nil {
		t.Fatalf("rewrite src: %v", err)
	}
	got, err := c.Load("f", src)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil cached entry after size change, got %+v", got)
	}
}

func TestSanitizeFileID_ReplacesUnsafeChars(t *testing.T) {
	got := sanitizeFileID("a/b\\c d:e")
	want := "a_b_c_d_e"
	if got != want {
		t.Fatalf("sanitize=%q, want %q", got, want)
	}
}
