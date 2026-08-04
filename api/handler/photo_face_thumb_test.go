package handler

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/imagethumb"
	"knox-media/internal/keystore"
	"knox-media/internal/photoface"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func TestServePhotoFaceThumbCropsFromPhotoThumb(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "face-thumb.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	previewDir := t.TempDir()
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'photos', 'photo', 'E:/photos', 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, file_type) VALUES (10, 1, 'f-10', 'E:/photos/sample.jpg', 'image')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (1, 'user', 'x', 'user', 1, 'all')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO photo_face (id, media_id, library_id, bbox_x, bbox_y, bbox_w, bbox_h) VALUES (7, 10, 1, 0.25, 0.25, 0.5, 0.5)`); err != nil {
		t.Fatalf("insert photo_face: %v", err)
	}

	thumbPath := imagethumb.ExpectedPaths(filepath.Join(previewDir, "photos"), 10).Thumb
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		t.Fatalf("mkdir thumb dir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	if err := os.WriteFile(thumbPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}
	facePath := photoface.ExpectedFaceThumbnailPath(filepath.Join(previewDir, "photos"), 7)
	if err := os.MkdirAll(filepath.Dir(facePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(facePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		App: &app.App{
			DB: db,
			Config: &config.Config{
				Data: config.DataConfig{Preview: previewDir},
			},
		},
		runningScans: map[int64]scanRuntime{},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/7/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	setUserCtx(c, 1, "user", "user")
	h.ServePhotoFaceThumb(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", ct)
	}
	if w.Body.Len() < 32 {
		t.Fatalf("expected non-empty jpeg body, got %d bytes", w.Body.Len())
	}
}

func TestServePhotoFaceThumbIsServeOnlyOnCacheMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "face-miss.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'photos','photo','E:/photos',1);
		INSERT INTO media(id,library_id,file_id,file_path,file_type) VALUES(10,1,'f','E:/photos/missing.jpg','image');
		INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'u','x','user',1,'all');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.2,.2,.4,.4)`)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := snapshotFiles(preview)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: preview}}}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/7/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	setUserCtx(c, 1, "u", "user")
	h.ServePhotoFaceThumb(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Thumbnail-Pending") != "1" || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("pending headers=%v", w.Header())
	}
	after, _ := snapshotFiles(preview)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("GET changed files")
	}
}

func TestServePhotoFaceThumbDecryptsDerivedArtifactWithoutPlaintext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-derived.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("face-handler-key", "")
	if err != nil {
		t.Fatal(err)
	}
	derived := &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(t.TempDir(), "derived")}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path,enabled,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'photos','photo','x',1,1,1);
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','x.jpg','image','active');
		INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'u','x','user',1,'all');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.5,.5)`)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("jpeg-body"), 20)
	if _, err = derived.Write(context.Background(), 10, "photo_face_thumb", "face:7", bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, DerivedStore: derived, KeyVault: vault, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/7/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	setUserCtx(c, 1, "u", "user")
	h.ServePhotoFaceThumb(c)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.Bytes())
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	if w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("content type=%q", w.Header().Get("Content-Type"))
	}
}

func TestServeEncryptedPhotoFaceThumbRejectsUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-derived-auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path,enabled,encrypted_assets_enabled) VALUES(1,'photos','photo','x',1,1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','x.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.5,.5)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/7/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	c.Set("role", "user")
	h.ServePhotoFaceThumb(c)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestServePhotoFaceThumbRejectsCorruptPlainCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-corrupt.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'p','photo','x',1); INSERT INTO media(id,library_id,file_id,file_path,file_type) VALUES(10,1,'f','x.jpg','image'); INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'u','x','user',1,'all'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,0,0,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	path := photoface.ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: preview}}}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/7/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	setUserCtx(c, 1, "u", "user")
	h.ServePhotoFaceThumb(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.Bytes())
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "corrupt" {
		t.Fatalf("GET mutated cache got=%q err=%v", got, err)
	}
}

func TestServePhotoFaceThumbMissingRelationIsNotRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-face.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/999/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "999"}}
	h.ServePhotoFaceThumb(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
	if got := w.Header().Get("X-Thumbnail-Pending"); got != "" {
		t.Fatalf("pending=%q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Fatalf("retry-after=%q", got)
	}
}
