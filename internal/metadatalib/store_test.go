package metadatalib

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/scraper"
)

func TestNormalizeImageURL(t *testing.T) {
	if got := normalizeImageURL("//image.tmdb.org/t/p/original/x.jpg"); got != "https://image.tmdb.org/t/p/original/x.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestPersistScrapeImagesDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	}))
	defer srv.Close()

	root := t.TempDir()
	upload := t.TempDir()
	res := &scraper.ScrapeResult{
		Poster: srv.URL + "/poster.jpg",
		Extra:  map[string]any{},
	}
	n, err := PersistScrapeImages(root, upload, 42, res)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if n != 1 {
		t.Fatalf("saved=%d want 1", n)
	}
	if !IsLocalMetadataURL(res.Poster) {
		t.Fatalf("poster=%q want local url", res.Poster)
	}
	dest := filepath.Join(MediaDir(root, 42), "poster.jpg")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestPersistScrapeImagesCopyUploads(t *testing.T) {
	upload := t.TempDir()
	_ = os.MkdirAll(filepath.Join(upload, "posters"), 0o755)
	src := filepath.Join(upload, "posters", "9.jpg")
	if err := os.WriteFile(src, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	res := &scraper.ScrapeResult{
		Extra: map[string]any{"poster": "/uploads/posters/9.jpg"},
	}
	n, err := PersistScrapeImages(root, upload, 9, res)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v poster=%q", n, err, res.Poster)
	}
}
