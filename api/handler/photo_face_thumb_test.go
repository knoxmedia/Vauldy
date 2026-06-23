package handler

import (
	"bytes"
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
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", ct)
	}
	if w.Body.Len() < 32 {
		t.Fatalf("expected non-empty jpeg body, got %d bytes", w.Body.Len())
	}
}
