package handler

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/metadatalib"
	"knox-media/internal/store"
)

func TestResolvePosterFilePath(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	meta := filepath.Join(root, "metadata")
	if err := os.MkdirAll(filepath.Join(upload, "posters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(metadatalib.MediaDir(meta, 42), 0o755); err != nil {
		t.Fatal(err)
	}
	localPoster := filepath.Join(upload, "posters", "42.jpg")
	if err := writeTestJPEG(localPoster, color.RGBA{255, 0, 0, 255}); err != nil {
		t.Fatal(err)
	}
	metaPoster := filepath.Join(metadatalib.MediaDir(meta, 42), "poster.jpg")
	if err := writeTestJPEG(metaPoster, color.RGBA{0, 255, 0, 255}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{Config: &config.Config{Data: config.DataConfig{
		Upload:           upload,
		MetadataLibrary: meta,
	}}}}

	if got := h.resolvePosterFilePath(42, "/uploads/posters/42.jpg"); got != localPoster {
		t.Fatalf("upload poster: got %q want %q", got, localPoster)
	}
	if got := h.resolvePosterFilePath(42, metadatalib.PublicURL(42, "poster.jpg")); got != metaPoster {
		t.Fatalf("metadata poster: got %q want %q", got, metaPoster)
	}
	if got := h.resolvePosterFilePath(42, ""); got != localPoster {
		t.Fatalf("fallback poster: got %q want %q", got, localPoster)
	}
}

func TestComposeLibraryPreviewImage(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	if err := os.MkdirAll(filepath.Join(upload, "posters"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		id int64
		c  color.RGBA
	}{
		{11, color.RGBA{255, 0, 0, 255}},
		{12, color.RGBA{0, 255, 0, 255}},
		{13, color.RGBA{0, 0, 255, 255}},
		{14, color.RGBA{255, 255, 0, 255}},
	} {
		if err := writeTestJPEG(filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", spec.id)), spec.c); err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{App: &app.App{Config: &config.Config{Data: config.DataConfig{Upload: upload}}}}
	sources := []libraryPreviewSource{
		{mediaID: 11, posterURL: "/uploads/posters/11.jpg"},
		{mediaID: 12, posterURL: "/uploads/posters/12.jpg"},
		{mediaID: 13, posterURL: "/uploads/posters/13.jpg"},
		{mediaID: 14, posterURL: "/uploads/posters/14.jpg"},
	}
	out := filepath.Join(root, "preview.jpg")
	if err := composeLibraryPreviewImage(h, sources, out); err != nil {
		t.Fatalf("compose: %v", err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("output missing or empty: %v", err)
	}
}

func TestLatestLibraryPreviewSources(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('Movies', 'movie', '/movies')`); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"d", "c", "b", "a", "z"} {
		_, err := db.Exec(
			`INSERT INTO media (library_id, file_id, title, file_path, file_type, created_at, meta_json)
			 VALUES (1, ?, ?, ?, 'video', datetime('now', ?), ?)`,
			"f"+title, title, "/v/"+title, fmt.Sprintf("-%d seconds", i), `{"scrape":{"poster":"/uploads/posters/`+title+`.jpg"}}`,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, created_at)
	 VALUES (1, 'audio1', 'song', '/a.mp3', 'audio', datetime('now'))`)

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: t.TempDir()}}}}
	got, err := h.latestLibraryPreviewSources(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len=%d want 4", len(got))
	}
	if got[0].posterURL != "/uploads/posters/d.jpg" || got[3].posterURL != "/uploads/posters/a.jpg" {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func writeTestJPEG(path string, c color.Color) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, 120, 180))
	if c == nil {
		c = color.RGBA{128, 128, 128, 255}
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 80})
}
