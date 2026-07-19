package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

func TestListScanLogsTimesOutWhileWaitingForConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-logs-timeout.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/scan-logs", nil)
	started := time.Now()
	h.ListScanLogs(c)
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"scan_logs_timeout"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(started); elapsed < 2900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("elapsed=%s", elapsed)
	}
}
