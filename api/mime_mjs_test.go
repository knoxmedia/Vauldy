package api

import (
	"mime"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestMimeMJSExtension(t *testing.T) {
	ct := mime.TypeByExtension(".mjs")
	if ct != "application/javascript" {
		t.Fatalf("mime.TypeByExtension(.mjs) = %q, want application/javascript", ct)
	}
}

func TestMountWebFrontendMJSContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fsys := fstest.MapFS{
		"index.html":                         {Data: []byte("<html/>")},
		"assets/pdf.worker.min-Dr1KORA9.mjs": {Data: []byte("export {};")},
	}
	r := gin.New()
	mountWebFrontend(r, webBundle{embedFS: fsys})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/assets/pdf.worker.min-Dr1KORA9.mjs", nil)
	r.ServeHTTP(w, req)
	t.Logf("status=%d content-type=%q body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	if w.Code != 200 {
		t.Fatalf("expected 200")
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("unexpected content-type for .mjs: %q", ct)
	}
}
