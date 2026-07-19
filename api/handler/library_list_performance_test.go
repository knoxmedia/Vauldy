package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

type listLibrariesResponse struct {
	Items []struct {
		ID                 int64  `json:"id"`
		MediaCount         int64  `json:"media_count"`
		ScanTaskID         int64  `json:"scan_task_id"`
		ScanStatus         string `json:"scan_status"`
		ScanProcessedCount int64  `json:"scan_processed_count"`
		ScanTotalCount     int64  `json:"scan_total_count"`
		ScanAddedCount     int64  `json:"scan_added_count"`
		ScanStartedAt      string `json:"scan_started_at"`
	} `json:"items"`
}

func callListLibraries(t *testing.T, h *Handler, requestContext context.Context, uid int64, role, username string) (*httptest.ResponseRecorder, listLibrariesResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library", nil).WithContext(requestContext)
	setUserCtx(c, uid, role, username)
	h.ListLibraries(c)
	var body listLibrariesResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v body=%s", err, w.Body.String())
		}
	}
	return w, body
}

func TestListLibrariesUsesLatestScanPerLibrary(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO scan_task(id,library_id,status,processed_count,total_count,added_count,started_at) VALUES
        (40,1,'done',10,10,2,'2026-07-18 10:00:00'),
        (41,1,'running',7,12,3,'2026-07-18 11:00:00'),
        (50,2,'failed',1,8,0,'2026-07-18 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	w, body := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := body.Items[0]
	if got.ScanTaskID != 41 || got.ScanStatus != "running" || got.ScanProcessedCount != 7 || got.ScanTotalCount != 12 || got.ScanAddedCount != 3 || got.ScanStartedAt != "2026-07-18 11:00:00" {
		t.Fatalf("latest scan mismatch: %+v", got)
	}
}

func TestListLibrariesEmptyLibraryDefaultsRemainZero(t *testing.T) {
	h := setupAccessTestDB(t)
	w, body := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := body.Items[2]
	if got.MediaCount != 0 || got.ScanTaskID != 0 || got.ScanStatus != "" || got.ScanProcessedCount != 0 || got.ScanTotalCount != 0 || got.ScanAddedCount != 0 || got.ScanStartedAt != "" {
		t.Fatalf("empty defaults changed: %+v", got)
	}
}

func TestListLibrariesScanErrorReturnsNon200(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE library SET auto_scan='not-an-integer' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	w, _ := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
	if w.Code == http.StatusOK {
		t.Fatalf("scan error returned 200: %s", w.Body.String())
	}
}

func TestListLibrariesHonorsRequestCancellation(t *testing.T) {
	h := setupAccessTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w, _ := callListLibraries(t, h, ctx, 2, "admin", "admin")
	if w.Code == http.StatusOK {
		t.Fatalf("cancelled request returned 200: %s", w.Body.String())
	}
}

func TestListLibrariesQueryCountDoesNotGrowWithLibraries(t *testing.T) {
	counts := make([]int64, 0, 2)
	for _, libraryCount := range []int{1, 20} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("libraries-%d.sqlite", libraryCount))
		bootstrap, err := store.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= libraryCount; i++ {
			if _, err = bootstrap.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(?,?,'movie',?,1)`, i, fmt.Sprintf("lib-%d", i), fmt.Sprintf("E:/lib/%d", i)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = bootstrap.Exec(`INSERT INTO user(id,username,password,role,library_scope) VALUES(2,'admin','x','admin','all')`); err != nil {
			t.Fatal(err)
		}
		if err = bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		db, counter := openCountingSQLitePath(t, path)
		h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
		counter.Store(0)
		w, body := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
		if w.Code != http.StatusOK || len(body.Items) != libraryCount {
			t.Fatalf("libraries=%d status=%d rows=%d body=%s", libraryCount, w.Code, len(body.Items), w.Body.String())
		}
		counts = append(counts, counter.Load())
	}
	if fmt.Sprint(counts) != "[3 3]" {
		t.Fatalf("actual statement counts for 1/20 libraries=%v, want [3 3]", counts)
	}
}

func TestListLibrariesOrphanMalformedFolderRowIsIgnored(t *testing.T) {
	h := setupAccessTestDB(t)
	h.App.DB.SetMaxOpenConns(1)
	if _, err := h.App.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO library_folder(library_id,path,sort_order) VALUES('malformed-library-id','E:/bad',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	w, body := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
	if w.Code != http.StatusOK || len(body.Items) != 3 {
		t.Fatalf("orphan folder affected visible libraries: status=%d items=%d body=%s", w.Code, len(body.Items), w.Body.String())
	}
}
func TestListLibrariesEmptyFolderPathReturnsNon200WithoutPartialItems(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO library_folder(library_id,path,sort_order) VALUES(1,'   ',0)`); err != nil {
		t.Fatal(err)
	}
	w, _ := callListLibraries(t, h, context.Background(), 2, "admin", "admin")
	if w.Code == http.StatusOK {
		t.Fatalf("empty folder path returned 200: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("empty folder response exposed partial items: %s", w.Body.String())
	}
}

func TestListLibrariesFolderScopeHidesFoldersAndCountsOnlyAllowedMedia(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO library_folder(library_id,path,sort_order) VALUES(1,'E:/lib1/hidden',0),(1,'E:/lib1/visible-root',1);
		INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,' E:/lib1/allowed ');
		INSERT INTO media(id,library_id,file_id,file_path) VALUES(31,1,'allowed-exact','E:/lib1/allowed'),(32,1,'allowed-child','E:/lib1/allowed/child/a.mp4'),(33,1,'boundary-miss','E:/lib1/allowed-other/b.mp4'),(34,1,'hidden','E:/lib1/hidden/c.mp4')`); err != nil {
		t.Fatal(err)
	}
	w, body := callListLibraries(t, h, context.Background(), 1, "user", "normal")
	if w.Code != http.StatusOK || len(body.Items) != 1 {
		t.Fatalf("status=%d items=%d body=%s", w.Code, len(body.Items), w.Body.String())
	}
	var raw struct {
		Items []struct {
			Folders    []string `json:"folders"`
			MediaCount int64    `json:"media_count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(raw.Items[0].Folders) != "[E:/lib1/allowed]" {
		t.Fatalf("folders leaked: %v", raw.Items[0].Folders)
	}
	if raw.Items[0].MediaCount != 2 {
		t.Fatalf("folder-scoped media_count=%d want 2", raw.Items[0].MediaCount)
	}
}

func TestListLibrariesRepeatedPollingDoesNotDirtyRunningPreview(t *testing.T) {
	h := setupAccessTestDB(t)
	h.App.Config = &config.Config{}
	ctx, cancel := context.WithCancel(context.Background())
	background := &BackgroundGroup{}
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int64
	h.Background, h.ServerContext = background, ctx
	h.libraryPreviewRefresh = func(context.Context, int64) error {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	h.libraryPreviewScheduler = newLibraryPreviewScheduler(ctx, background, 1, 16, h.runLibraryPreviewRefresh)
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := background.Wait(waitCtx); err != nil {
			t.Fatal(err)
		}
	})
	w, _ := callListLibraries(t, h, context.Background(), 1, "user", "normal")
	if w.Code != http.StatusOK {
		t.Fatalf("first status=%d", w.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preview did not start")
	}
	for i := 0; i < 10; i++ {
		w, _ = callListLibraries(t, h, context.Background(), 1, "user", "normal")
		if w.Code != http.StatusOK {
			t.Fatalf("poll %d status=%d", i, w.Code)
		}
	}
	close(release)
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("repeated ListLibraries caused %d runs want 1", got)
	}
}

func TestListLibrariesMissingPreviewFailureUsesCooldown(t *testing.T) {
	h := setupAccessTestDB(t)
	h.App.Config = &config.Config{}
	ctx, cancel := context.WithCancel(context.Background())
	background := &BackgroundGroup{}
	var runs atomic.Int64
	finished := make(chan struct{}, 1)
	h.Background, h.ServerContext = background, ctx
	h.libraryPreviewRefresh = func(context.Context, int64) error {
		runs.Add(1)
		finished <- struct{}{}
		return ErrLibraryPreviewUnavailable
	}
	h.libraryPreviewScheduler = newLibraryPreviewSchedulerWithOptions(ctx, background, 1, 16, libraryPreviewSchedulerOptions{InitialRetry: time.Minute, MaxRetry: 30 * time.Minute, MaxFailures: 16}, h.runLibraryPreviewRefresh)
	t.Cleanup(func() {
		cancel()
		wc, wcancel := context.WithTimeout(context.Background(), time.Second)
		defer wcancel()
		if err := background.Wait(wc); err != nil {
			t.Fatal(err)
		}
	})
	for i := 0; i < 2; i++ {
		w, _ := callListLibraries(t, h, context.Background(), 1, "user", "normal")
		if w.Code != http.StatusOK {
			t.Fatalf("poll %d status=%d", i, w.Code)
		}
		if i == 0 {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("preview run timeout")
			}
		}
	}
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs=%d want 1 during cooldown", got)
	}
}
