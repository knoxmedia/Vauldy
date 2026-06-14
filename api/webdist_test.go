package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBundledPowerPlayerDir(t *testing.T) {
	dir := t.TempDir()
	pp := filepath.Join(dir, "static", "powerplayer6")
	if err := os.MkdirAll(pp, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := bundledPowerPlayerDir(dir); got != pp {
		t.Fatalf("got %q want %q", got, pp)
	}
	if got := bundledPowerPlayerDir(""); got != "" {
		t.Fatalf("empty webDist should return empty, got %q", got)
	}
}

func TestMountStaticRoutesBundledPowerPlayer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	dataStatic := filepath.Join(root, "data-static")
	if err := os.MkdirAll(bundled, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataStatic, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundled, "powerplayer.min.js"), []byte("bundled-js"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataStatic, "powerplayer.min.js"), []byte("data-js"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	mountStaticRoutes(r, dataStatic, bundled)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/static/powerplayer6/powerplayer.min.js", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "bundled-js" {
		t.Fatalf("bundled response: code=%d body=%q", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/static/other.txt", nil)
	os.WriteFile(filepath.Join(dataStatic, "other.txt"), []byte("ok"), 0o644)
	r.ServeHTTP(w2, req2)
	if w2.Code != 200 || w2.Body.String() != "ok" {
		t.Fatalf("data static response: code=%d body=%q", w2.Code, w2.Body.String())
	}
}

func TestIsPathUnderRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "escape.txt")
	if isPathUnderRoot(root, inside) != true {
		t.Fatal("expected inside path")
	}
	if isPathUnderRoot(root, outside) != false {
		t.Fatal("expected outside path rejected")
	}
}
