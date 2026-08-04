package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestServeAlbumArtworkServesCachedFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	art := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(art, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'music', 'music', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (2, 'admin', 'x', 'admin', 1, 'all')`)
	_, _ = db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, artwork_path) VALUES (1, 1, 'Album', 'album', ?)`, art)

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/album/1/artwork", nil)
	setUserCtx(c, 2, "admin", "admin")

	h.ServeAlbumArtwork(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Body.String() != "jpeg-bytes" {
		t.Fatalf("body=%q", w.Body.String())
	}
}

func TestAlbumArtworkCandidatePathsIncludesPlainDir(t *testing.T) {
	dir := t.TempDir()
	albumDir := filepath.Join(dir, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(albumDir, "cover.jpg")
	if err := os.WriteFile(cover, []byte("folder-cover"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(albumDir, "track.flac")
	enc := filepath.Join(dir, ".encrypted", "audio", "fid.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'music', 'music', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid', 'Track', ?, 'audio', 'active')`, enc)
	_, err = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (10, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)
	if err != nil {
		t.Fatalf("insert encrypted asset: %v", err)
	}

	paths := albumArtworkCandidatePaths(db, 10, 1, enc)
	found := false
	for _, p := range paths {
		if p == cover {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidates=%v want %q", paths, cover)
	}
}

func TestServeAlbumArtworkMissingStoredPathIsServeOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, writes := openPhotoGETWriteCountingDB(t)
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatal(err)
	}
	track := filepath.Join(albumDir, "track.flac")
	cover := filepath.Join(albumDir, "cover.jpg")
	if err := os.WriteFile(track, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cover, []byte("lazy-cover"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(2,'music','music',?); INSERT INTO music_album(id,library_id,title,title_norm) VALUES(40,2,'Album','album'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(50,2,'a','Track',?,'audio','active'); INSERT INTO music_track(album_id,media_id,title,sort_order) VALUES(40,50,'Track',1)`, root, track)
	if err != nil {
		t.Fatal(err)
	}
	before, err := snapshotFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	writes.Store(0)
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/album/40/artwork", nil)
	c.Params = gin.Params{{Key: "id", Value: "40"}}
	setUserCtx(c, 2, "admin", "admin")
	h.ServeAlbumArtwork(c)
	c.Writer.WriteHeaderNow()
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("writes=%d", got)
	}
	after, err := snapshotFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("files changed before=%v after=%v", before, after)
	}
}
