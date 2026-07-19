package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"

	"knox-media/internal/scancoord"
	"knox-media/internal/store"

	"knox-media/internal/postingest"
)

type postIngestEnqueuerSpy struct {
	calls      int
	scanTaskID *int64
	err        error
}

func (s *postIngestEnqueuerSpy) EnqueueMedia(_ context.Context, _ int64, scanTaskID *int64, _ string) ([]postingest.TaskType, error) {
	s.calls++
	s.scanTaskID = scanTaskID
	return nil, s.err
}

func TestEnqueuePostIngestForNewMediaSynchronouslyUsesUnifiedQueue(t *testing.T) {
	spy := &postIngestEnqueuerSpy{}
	h := &Handler{PostIngestEnqueuer: spy}
	if err := h.EnqueuePostIngestForNewMedia(42, "video"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if spy.calls != 1 || spy.scanTaskID != nil {
		t.Fatalf("calls=%d scanTaskID=%v want 1,nil", spy.calls, spy.scanTaskID)
	}
}

func TestEnqueuePostIngestForNewMediaReturnsAndReportsError(t *testing.T) {
	want := errors.New("queue unavailable")
	spy := &postIngestEnqueuerSpy{err: want}
	var reported error
	h := &Handler{PostIngestEnqueuer: spy, OnPostIngestError: func(err error) { reported = err }}
	if err := h.EnqueuePostIngestForNewMedia(42, "video"); !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
	if !errors.Is(reported, want) {
		t.Fatalf("reported=%v want %v", reported, want)
	}
}

type scanSubmitterSpy struct {
	requests []scancoord.ScanRequest
	result   scancoord.SubmitResult
	err      error
}

func (s *scanSubmitterSpy) Submit(_ context.Context, req scancoord.ScanRequest) (scancoord.SubmitResult, error) {
	s.requests = append(s.requests, req)
	return s.result, s.err
}

func (s *scanSubmitterSpy) Cancel(context.Context, int64) (scancoord.CancelResult, error) {
	return scancoord.CancelResult{}, nil
}

func TestScanSourcesManualAndScheduledUseExactValues(t *testing.T) {
	spy := &scanSubmitterSpy{result: scancoord.SubmitResult{TaskID: 81, Started: true}}
	h := &Handler{ScanCoordinator: spy}
	manual, err := h.submitLibraryScan(context.Background(), 7, []string{"manual-root"}, scancoord.SourceManual)
	if err != nil || manual.TaskID != 81 {
		t.Fatalf("manual result=%+v err=%v", manual, err)
	}
	scheduled, err := h.submitLibraryScan(context.Background(), 8, []string{"scheduled-root"}, scancoord.SourceScheduled)
	if err != nil || scheduled.TaskID != 81 {
		t.Fatalf("scheduled result=%+v err=%v", scheduled, err)
	}
	if len(spy.requests) != 2 || spy.requests[0].Source != scancoord.SourceManual || spy.requests[1].Source != scancoord.SourceScheduled {
		t.Fatalf("requests=%+v", spy.requests)
	}
}

func TestHandlerPreservesExistingScanSemantics(t *testing.T) {
	spy := &scanSubmitterSpy{result: scancoord.SubmitResult{TaskID: 82, ExistingTaskID: 44, Started: false}}
	h := &Handler{ScanCoordinator: spy}
	got, err := h.submitLibraryScan(context.Background(), 7, []string{"root"}, scancoord.SourceManual)
	if err != nil || got.ExistingTaskID != 44 || got.Started {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func TestScanLibraryHandlerSubmitsManualRequestAndPreservesConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "handler-scan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	res, err := db.Exec(`INSERT INTO library (name,type,path) VALUES ('manual','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	spy := &scanSubmitterSpy{result: scancoord.SubmitResult{TaskID: 90, ExistingTaskID: 41}}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, ScanCoordinator: spy}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/libraries/scan", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(libraryID, 10)}}
	h.ScanLibrary(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(spy.requests) != 1 || spy.requests[0].Source != scancoord.SourceManual || len(spy.requests[0].Roots) != 1 || spy.requests[0].Roots[0] != root {
		t.Fatalf("request=%+v", spy.requests)
	}
}

type scanCoordinatorSpy struct {
	scanSubmitterSpy
	cancelIDs    []int64
	cancelResult scancoord.CancelResult
	cancelErr    error
	ctxErr       error
}

func (s *scanCoordinatorSpy) Cancel(ctx context.Context, id int64) (scancoord.CancelResult, error) {
	s.cancelIDs = append(s.cancelIDs, id)
	s.ctxErr = ctx.Err()
	return s.cancelResult, s.cancelErr
}

func callCancelScanTask(t *testing.T, h *Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/scan-tasks/"+id+"/cancel", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.CancelScanTask(c)
	return w
}

func TestCancelScanTaskDelegatesToSharedCoordinatorWithoutRunningScanMap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &scanCoordinatorSpy{}
	h := &Handler{ScanCoordinator: spy}
	w := callCancelScanTask(t, h, "73")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(spy.cancelIDs) != 1 || spy.cancelIDs[0] != 73 {
		t.Fatalf("cancel IDs=%v", spy.cancelIDs)
	}
}

func TestCancelScanTaskMapsCoordinatorResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		id   string
		err  error
		want int
	}{
		{name: "invalid", id: "bad", want: http.StatusBadRequest},
		{name: "missing", id: "99", err: sql.ErrNoRows, want: http.StatusNotFound},
		{name: "internal", id: "99", err: errors.New("cancel failed"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &scanCoordinatorSpy{cancelErr: tc.err}
			w := callCancelScanTask(t, &Handler{ScanCoordinator: spy}, tc.id)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestCancelScanTaskReturnsActualCoordinatorResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		result   scancoord.CancelResult
		wantBody string
	}{
		{name: "transition", result: scancoord.CancelResult{Cancelled: true, Status: "cancelling"}, wantBody: `"cancelled":true`},
		{name: "terminal no-op", result: scancoord.CancelResult{Cancelled: false, Status: "done"}, wantBody: `"cancelled":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := callCancelScanTask(t, &Handler{ScanCoordinator: &scanCoordinatorSpy{cancelResult: tc.result}}, "73")
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.wantBody) || !strings.Contains(w.Body.String(), `"status":"`+tc.result.Status+`"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListScanTasksIncludesFailedCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-list.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('scan-list','video',?)`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,processed_count,failed_count,added_count) VALUES(?,'failed','manual',9,3,4)`, libraryID); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/scan-tasks", nil)
	h.ListScanTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"failed_count":3`) {
		t.Fatalf("body missing failed_count: %s", w.Body.String())
	}
}

func TestListScanTasksTimesOutWhileWaitingForConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-timeout.sqlite"))
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
	c.Request = httptest.NewRequest(http.MethodGet, "/scan/task", nil)
	started := time.Now()
	h.ListScanTasks(c)
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"scan_tasks_timeout"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(started); elapsed < 2900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("elapsed=%s", elapsed)
	}
}
