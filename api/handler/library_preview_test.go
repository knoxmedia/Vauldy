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
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('Movies', 'movie', '/movies')`); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"d", "c", "b", "a", "z"} {
		poster := filepath.Join(upload, "posters", title+".jpg")
		if err := writeTestJPEG(poster, color.RGBA{byte(i), 32, 64, 255}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(
			`INSERT INTO media (library_id, file_id, title, file_path, file_type, status, created_at, meta_json)
			 VALUES (1, ?, ?, ?, 'video', 'active', datetime('now', ?), ?)`,
			"f"+title, title, "/v/"+title, fmt.Sprintf("-%d seconds", i), `{"scrape":{"poster":"/uploads/posters/`+title+`.jpg"}}`,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, created_at)
	 VALUES (1, 'audio1', 'song', '/a.mp3', 'audio', datetime('now'))`)

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: upload}}}}
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

func TestLatestLibraryPreviewSourcesPhoto(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "preview", "photos")
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (2, 'Photos', 'photo', '/photos')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{21, 22, 23} {
		thumb := filepath.Join(upload, fmt.Sprintf("%d", id), "thumb.jpg")
		if err := writeTestJPEG(thumb, color.RGBA{byte(id), 64, 128, 255}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(
			`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, created_at)
			 VALUES (?, 2, ?, 'p', ?, 'image', 'active', datetime('now'))`,
			id, fmt.Sprintf("f-%d", id), fmt.Sprintf("/photos/%d.jpg", id),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: filepath.Join(root, "preview")}}}}
	got, err := h.latestLibraryPreviewSources(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	if got[0].kind != libraryPreviewKindPhotoThumb || got[0].mediaID != 23 {
		t.Fatalf("unexpected first source: %+v", got[0])
	}
}

func TestLatestLibraryPreviewSourcesMusic(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (3, 'Music', 'music', '/music')`); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(root, "art1.jpg")
	if err := writeTestJPEG(art, color.RGBA{255, 128, 0, 255}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, artwork_path) VALUES (1, 3, 'A', 'a', ?)`, art); err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: root}}}}
	got, err := h.latestLibraryPreviewSources(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	if got[0].kind != libraryPreviewKindMusicArtwork || got[0].albumID != 1 {
		t.Fatalf("unexpected source: %+v", got[0])
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
