package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type overviewBuilderFunc func(context.Context) (AdminOverviewData, error)

func (f overviewBuilderFunc) Build(ctx context.Context) (AdminOverviewData, error) { return f(ctx) }

func callAdminOverview(t *testing.T, b OverviewBuilder) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	(&Handler{AdminOverviewBuilder: b}).AdminOverview(c)
	return w
}

func TestAdminOverview_TimesOut(t *testing.T) {
	started := time.Now()
	w := callAdminOverview(t, overviewBuilderFunc(func(ctx context.Context) (AdminOverviewData, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"admin_overview_timeout"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(started); elapsed < 2900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("elapsed=%s want about 3s", elapsed)
	}
}

func TestAdminOverview_InternalError(t *testing.T) {
	w := callAdminOverview(t, overviewBuilderFunc(func(context.Context) (AdminOverviewData, error) {
		return nil, errors.New("boom")
	}))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"admin_overview_internal"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type snapshotterStub struct{ snapshot postingest.BudgetSnapshot }

func (s snapshotterStub) Snapshot() postingest.BudgetSnapshot { return s.snapshot }

func TestAdminOverview_ReturnsResourceControlMetrics(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "overview.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, _ := db.Exec(`INSERT INTO library(name,type,path) VALUES('lib','video','x')`)
	libraryID, _ := lib.LastInsertId()
	media, _ := db.Exec(`INSERT INTO media(library_id,title,file_path,file_type) VALUES(?,'m','x','video')`, libraryID)
	mediaID, _ := media.LastInsertId()
	scan, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	scanID, _ := scan.LastInsertId()
	_, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,lease_owner,lease_until,started_at,available_at) VALUES(?,?,'poster','running',2,'worker',datetime(CURRENT_TIMESTAMP,'-1 second'),CURRENT_TIMESTAMP,datetime(CURRENT_TIMESTAMP,'-10 second'))`, mediaID, scanID)
	_, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,status,available_at) VALUES(?,'preview','waiting',datetime(CURRENT_TIMESTAMP,'-10 second'))`, mediaID)
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?, 'scan-worker',datetime(CURRENT_TIMESTAMP,'+30 second'))`, libraryID, scanID)
	metrics := &store.SQLiteMetrics{}
	metrics.BusyRetries.Add(2)
	metrics.ProgressBatches.Add(3)
	metrics.DroppedLogs.Add(4)
	b := NewAdminOverviewBuilder(db, snapshotterStub{postingest.BudgetSnapshot{GlobalLimit: 6, GlobalUsed: 2, PosterLimit: 2, PosterUsed: 1, PreviewLimit: 1, PreviewUsed: 1}}, metrics)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, data))
	for _, want := range []string{`"post_ingest_queue"`, `"by_status"`, `"by_type"`, `"oldest_waiting_seconds"`, `"expired_lease_count":1`, `"running_post_ingest_tasks"`, `"scan_leases"`, `"resource_budget"`, `"global_limit":6`, `"sqlite_metrics"`, `"busy_retries":2`, `"progress_batches":3`, `"dropped_logs":4`, `"scope":"process_since_start"`, `"persistent":false`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("missing %s in %s", want, encoded)
		}
	}
}

func TestAdminOverview_ResourceControlMatrixAndExactFields(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "overview-matrix.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('matrix','video','x')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	scan, err := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := scan.LastInsertId()
	insertTask := func(title, typ, status string) int64 {
		t.Helper()
		media, err := db.Exec(`INSERT INTO media(library_id,title,file_path,file_type) VALUES(?,?,?,'video')`, libraryID, title, title)
		if err != nil {
			t.Fatal(err)
		}
		mediaID, _ := media.LastInsertId()
		_, err = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,max_attempts,lease_owner,lease_until,started_at) VALUES(?,?,?,?,2,5,'owner',datetime(CURRENT_TIMESTAMP,'-1 second'),datetime(CURRENT_TIMESTAMP,'-5 second'))`, mediaID, scanID, typ, status)
		if err != nil {
			t.Fatal(err)
		}
		return mediaID
	}
	insertTask("poster-running", "poster", "running")
	insertTask("poster-failed", "poster", "failed")
	insertTask("preview-waiting", "preview", "waiting")
	_, err = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?, 'expired-owner',datetime(CURRENT_TIMESTAMP,'-1 second'))`, libraryID, scanID)
	if err != nil {
		t.Fatal(err)
	}
	b := NewAdminOverviewBuilder(db, nil, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Queue struct {
			ByStatus map[string]int64            `json:"by_status"`
			ByType   map[string]map[string]int64 `json:"by_type"`
		} `json:"post_ingest_queue"`
		Running []map[string]any `json:"running_post_ingest_tasks"`
		Leases  []struct {
			Expired bool `json:"expired"`
		} `json:"scan_leases"`
	}
	if err := json.Unmarshal(mustJSON(t, data), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Queue.ByStatus["running"] != 1 || decoded.Queue.ByStatus["failed"] != 1 || decoded.Queue.ByStatus["waiting"] != 1 {
		t.Fatalf("by_status=%v", decoded.Queue.ByStatus)
	}
	if decoded.Queue.ByType["poster"]["running"] != 1 || decoded.Queue.ByType["poster"]["failed"] != 1 || decoded.Queue.ByType["preview"]["waiting"] != 1 {
		t.Fatalf("by_type=%v", decoded.Queue.ByType)
	}
	if len(decoded.Running) != 1 {
		t.Fatalf("running=%v", decoded.Running)
	}
	for _, field := range []string{"task_type", "attempts", "max_attempts", "run_seconds", "lease_until"} {
		if _, ok := decoded.Running[0][field]; !ok {
			t.Errorf("running field %q missing: %v", field, decoded.Running[0])
		}
	}
	if decoded.Running[0]["run_seconds"].(float64) < 0 {
		t.Fatalf("run_seconds=%v", decoded.Running[0]["run_seconds"])
	}
	if len(decoded.Leases) != 1 || !decoded.Leases[0].Expired {
		t.Fatalf("leases=%v", decoded.Leases)
	}
}

func TestAdminOverview_WrappedDeadlineMapsToTimeout(t *testing.T) {
	w := callAdminOverview(t, overviewBuilderFunc(func(context.Context) (AdminOverviewData, error) {
		return nil, fmt.Errorf("build query: %w", context.DeadlineExceeded)
	}))
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"admin_overview_timeout"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminOverviewExposesBuildMetadata(t *testing.T) {
	data, err := os.ReadFile("admin_overview.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{"buildinfo.Current()", "software_version", "software_commit", "software_build_time", "software_dirty"} {
		if !strings.Contains(src, want) {
			t.Errorf("admin overview missing build metadata wiring %q", want)
		}
	}
}

func TestAdminOverviewReturnsFullBuildMetadata(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "build-overview.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := NewAdminOverviewBuilder(db, nil, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	system := data["system"].(map[string]any)
	for _, key := range []string{"software_version", "software_commit", "software_build_time", "software_dirty", "software_dirty_known", "software_vcs_revision", "software_vcs_time", "software_vcs_modified", "software_vcs_modified_known"} {
		if _, ok := system[key]; !ok {
			t.Errorf("missing %s in %#v", key, system)
		}
	}
}
