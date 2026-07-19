package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func BenchmarkListMediaCreatedDesc(b *testing.B) {
	benchmarkListMedia(b, 5000, "created_desc", 24)
}

func BenchmarkListMediaFolderLowHit(b *testing.B) {
	benchmarkListMediaFolderScope(b, 5000, 24)
}

func benchmarkListMedia(b *testing.B, mediaCount int, sort string, limit int) {
	h := benchmarkMediaHandler(b, mediaCount)
	benchmarkListMediaHTTP(b, h, fmt.Sprintf("/api/v1/media?sort=%s&limit=%d", sort, limit), 2)
}

func benchmarkListMediaFolderScope(b *testing.B, mediaCount, limit int) {
	h := benchmarkMediaHandler(b, mediaCount)
	benchmarkListMediaHTTP(b, h, fmt.Sprintf("/api/v1/media?sort=id_desc&limit=%d", limit), 1)
}

func benchmarkMediaHandler(b *testing.B, mediaCount int) *Handler {
	b.Helper()
	db, err := store.OpenSQLite(filepath.Join(b.TempDir(), "list-media-benchmark.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES
        (1,'allowed','movie','E:/bench/allowed',1),(2,'denied','movie','E:/bench/denied',1)`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES
        (1,'folder-user','x','user',1,'selected'),(2,'admin','x','admin',1,'all')`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_library_permission(user_id,library_id) VALUES(1,1)`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,'E:/bench/allowed/hit')`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,duration,meta_json,created_at) VALUES(?,?,?,?,?,'image',120,'{}',?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= mediaCount; i++ {
		libID := int64(2)
		path := fmt.Sprintf("E:/bench/denied/%06d.jpg", i)
		if i%2 == 0 {
			libID = 1
			path = fmt.Sprintf("E:/bench/allowed/miss/%06d.jpg", i)
		}
		if i%997 == 0 {
			libID = 1
			path = fmt.Sprintf("E:/bench/allowed/hit/%06d.jpg", i)
		}
		if _, err := stmt.Exec(i, libID, fmt.Sprintf("file-%06d", i), fmt.Sprintf("Media %06d", i), path, fmt.Sprintf("2026-01-%02d 12:%02d:00", 1+(i%28), i%60)); err != nil {
			b.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func benchmarkListMediaHTTP(b *testing.B, h *Handler, target string, userID int64) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	b.ReportAllocs()
	b.ResetTimer()
	totalRows := 0
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req.Clone(req.Context())
		if userID == 1 {
			setUserCtx(c, 1, "user", "folder-user")
		} else {
			setUserCtx(c, 2, "admin", "admin")
		}
		h.ListMedia(c)
		if w.Code != http.StatusOK {
			b.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			b.Fatal(err)
		}
		totalRows += len(body.Items)
	}
	b.StopTimer()
	b.ReportMetric(float64(totalRows)/float64(b.N), "rows/op")
}

func benchmarkMediaRows(b *testing.B, h *Handler, spec mediaListSpec) {
	b.Helper()
	b.ReportAllocs()
	var totals mediaListStats
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, stats, err := h.listMediaRows(context.Background(), spec)
		if err != nil {
			b.Fatal(err)
		}
		totals.Batches += stats.Batches
		totals.Candidates += stats.Candidates
		totals.Rejected += stats.Rejected
		totals.Returned += stats.Returned
	}
	b.StopTimer()
	b.ReportMetric(float64(totals.Batches)/float64(b.N), "list_statements/op")
	b.ReportMetric(float64(totals.Batches)/float64(b.N), "batches/op")
	b.ReportMetric(float64(totals.Candidates)/float64(b.N), "candidate_rows/op")
	b.ReportMetric(float64(totals.Rejected)/float64(b.N), "rejected_rows/op")
	b.ReportMetric(float64(totals.Returned)/float64(b.N), "rows/op")
}

func BenchmarkListMediaCreatedDescStats(b *testing.B) {
	h := benchmarkMediaHandler(b, 5000)
	benchmarkMediaRows(b, h, mediaListSpec{Sort: mediaSortCreatedDesc, Limit: 24, BatchSize: 100})
}

func BenchmarkListMediaFolderLowHitStats(b *testing.B) {
	h := benchmarkMediaHandler(b, 5000)
	benchmarkMediaRows(b, h, mediaListSpec{AllowedLibraryIDs: []int64{1}, RestrictLibraries: true, FolderScope: map[int64][]string{1: {"E:/bench/allowed/hit"}}, Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100})
}

func benchmarkMetricNames() string {
	return "rows/op list_statements/op batches/op candidate_rows/op rejected_rows/op B/op allocs/op"
}

func TestBenchmarkReportsRuntimeMediaListStats(t *testing.T) {
	for _, metric := range []string{"list_statements/op", "batches/op", "candidate_rows/op", "rejected_rows/op"} {
		if !strings.Contains(benchmarkMetricNames(), metric) {
			t.Fatalf("missing runtime metric %s: %s", metric, benchmarkMetricNames())
		}
	}
}
