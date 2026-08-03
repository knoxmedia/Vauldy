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

	"knox-media/internal/store"
)

type overviewBuilderFunc func(context.Context) (AdminOverviewData, error)

func (f overviewBuilderFunc) Build(ctx context.Context) (AdminOverviewData, error) { return f(ctx) }

func callAdminOverview(t *testing.T, builder OverviewBuilder) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{AdminOverviewBuilder: builder}
	e := setupRouter()
	e.GET("/admin/overview", h.AdminOverview)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/overview", nil))
	return w
}

func TestAdminOverview_Timeout(t *testing.T) {
	started := time.Now()
	w := callAdminOverview(t, overviewBuilderFunc(func(ctx context.Context) (AdminOverviewData, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"admin_overview_timeout"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	lo := adminOverviewTimeout - 200*time.Millisecond
	hi := adminOverviewTimeout + time.Second
	if elapsed := time.Since(started); elapsed < lo || elapsed > hi {
		t.Fatalf("elapsed=%s want about %s", elapsed, adminOverviewTimeout)
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

func TestAdminOverview_ReturnsSystemMonitorOnly(t *testing.T) {
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
	b := NewAdminOverviewBuilder(db, metrics)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, data))
	// System monitor fields must be present
	for _, want := range []string{`"monitor"`, `"cpu_percent"`, `"memory_percent"`, `"disk_percent"`, `"system"`, `"cpu_count"`, `"memory_total"`, `"os"`, `"database"`, `"software_version"`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("missing system key %s in %s", want, encoded)
		}
	}
	// SQLite metrics must be present
	for _, want := range []string{`"sqlite_metrics"`, `"busy_retries":2`, `"progress_batches":3`, `"dropped_logs":4`, `"scope":"process_since_start"`, `"persistent":false`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("missing SQLite metric %s in %s", want, encoded)
		}
	}
	// Task control fields must NOT be present
	for _, forbidden := range []string{`"post_ingest_queue"`, `"task_alignment"`, `"running_post_ingest_tasks"`, `"scan_leases"`, `"resource_budget"`, `"publication_policy"`, `"transcode_task_count"`, `"media_total"`} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("task control field %q must not appear in system-only overview: %s", forbidden, encoded)
		}
	}
}

func TestAdminOverview_SystemOnlyExcludesTaskFields(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "overview-system-only.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := NewAdminOverviewBuilder(db, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, data))

	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}

	// Task-related keys must NOT exist in the response
	taskKeys := []string{"post_ingest_queue", "task_alignment", "running_post_ingest_tasks", "scan_leases", "resource_budget", "publication_policy"}
	for _, key := range taskKeys {
		if _, ok := decoded[key]; ok {
			t.Errorf("task key %q must not exist in system-only overview, but was found", key)
		}
	}

	// System keys that MUST exist
	systemKeys := []string{"monitor", "system", "activities", "sqlite_metrics"}
	for _, key := range systemKeys {
		if _, ok := decoded[key]; !ok {
			t.Errorf("system key %q must exist in system-only overview, but was missing", key)
		}
	}

	// Monitor must only contain cpu, memory, disk
	monitor := decoded["monitor"].(map[string]any)
	for _, key := range []string{"cpu_percent", "memory_percent", "disk_percent"} {
		if _, ok := monitor[key]; !ok {
			t.Errorf("monitor key %q missing", key)
		}
	}
	if _, ok := monitor["transcode_task_count"]; ok {
		t.Errorf("monitor.transcode_task_count must not exist in system-only overview")
	}
	if _, ok := monitor["media_total"]; ok {
		t.Errorf("monitor.media_total must not exist in system-only overview")
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
	b := NewAdminOverviewBuilder(db, nil)
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

func TestAdminOverview_NilDB(t *testing.T) {
	b := NewAdminOverviewBuilder(nil, nil)
	_, err := b.Build(context.Background())
	if err == nil {
		t.Fatal("expected error for nil DB")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAdminOverview_NilBuilderIsNoop(t *testing.T) {
	if _, err := NewAdminOverviewBuilder(nil, nil).Build(context.Background()); err == nil {
		t.Fatal("expected error for nil builder")
	}
}

// TestAdminOverview_ConsoleSystemOnly_NoTaskFields verifies that the
// Console /admin/overview endpoint response excludes task control fields.
func TestAdminOverview_ConsoleSystemOnly_NoTaskFields(t *testing.T) {
	taskKeys := []string{
		"post_ingest_queue",
		"task_alignment",
		"running_post_ingest_tasks",
		"scan_leases",
		"resource_budget",
		"publication_policy",
	}

	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "console-only.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := NewAdminOverviewBuilder(db, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range taskKeys {
		if _, ok := data[key]; ok {
			t.Errorf("task control key %q must not appear in console overview", key)
		}
	}
}

func TestAdminOverview_ActivitiesPresent(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "activities.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Insert an activity to verify it appears
	if _, err := db.Exec(`INSERT INTO activity_log(username,action,media_id,message) VALUES('admin','test',0,'hello')`); err != nil {
		t.Fatal(err)
	}
	b := NewAdminOverviewBuilder(db, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }
	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	activities := data["activities"].([]map[string]any)
	if len(activities) == 0 {
		t.Fatal("expected at least one activity")
	}
	if activities[0]["action"] != "test" {
		t.Fatalf("unexpected activity: %v", activities[0])
	}
}

// setupRouter helper - minimal for tests
func setupRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}
