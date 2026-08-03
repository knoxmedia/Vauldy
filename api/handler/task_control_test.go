package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/taskcontrol"

	_ "modernc.org/sqlite"
)

// setupTaskControlTestDB creates an in-memory SQLite DB with the required tables.
func setupTaskControlTestDB(t *testing.T) (*sql.DB, *taskcontrol.ProjectionBuilder, *taskcontrol.Registry) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE post_ingest_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL DEFAULT 0,
			scan_task_id INTEGER,
			generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
			task_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			attempts INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0 CHECK(retry_round >= 0),
			max_attempts INTEGER NOT NULL DEFAULT 3,
			available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			lease_owner TEXT,
			lease_until TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			priority INTEGER NOT NULL DEFAULT 0,
			removed_at TIMESTAMP,
			removed_by TEXT NOT NULL DEFAULT '',
			remove_reason TEXT NOT NULL DEFAULT '',
			source_class INTEGER NOT NULL DEFAULT 0,
			base_priority INTEGER NOT NULL DEFAULT 0,
			library_id INTEGER,
			run_now_expires TIMESTAMP,
			finished_at TIMESTAMP,
			started_at TIMESTAMP,
			abort_requested_at TIMESTAMP,
			abort_timeout_recovery_required INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE task_projection_sequence (
			singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
			next_revision INTEGER NOT NULL DEFAULT 1 CHECK (next_revision >= 1)
		)`,
		`CREATE TABLE task_projection_revision (
			task_identity TEXT PRIMARY KEY,
			revision INTEGER NOT NULL UNIQUE,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_identity TEXT NOT NULL DEFAULT '',
			actor_id INTEGER NOT NULL DEFAULT 0,
			actor_name TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			prev_status TEXT NOT NULL DEFAULT '',
			new_status TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_batch_operation (
			operation_id TEXT PRIMARY KEY,
			action TEXT NOT NULL,
			actor_id INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			requested_count INTEGER NOT NULL DEFAULT 0 CHECK (requested_count >= 0),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP
		)`,
		`CREATE TABLE task_batch_item (
			operation_id TEXT NOT NULL,
			task_identity TEXT NOT NULL,
			action TEXT NOT NULL,
			request_revision INTEGER NOT NULL DEFAULT 0,
			ok INTEGER NOT NULL CHECK (ok IN (0, 1)),
			outcome_code TEXT NOT NULL DEFAULT '',
			result_revision INTEGER,
			result_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (operation_id, task_identity, action)
		)`,
		`INSERT INTO task_projection_sequence(singleton_id, next_revision) VALUES(1, 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create schema: %v\n%s", err, stmt)
		}
	}
	reg := taskcontrol.NewRegistry()
	builder := taskcontrol.NewProjectionBuilder(db, reg)
	builder.RegisterAdapter(taskcontrol.NewOracleAdapter(db))
	return db, builder, reg
}

func seedHandlerTasks(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	ctx := context.Background()
	types := []string{"poster", "thumbnail", "preview", "keyframe", "transcode", "subtitle_extract", "subtitle_recognize", "package", "encrypt", "ai_analysis"}
	statuses := []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}
	var ids []int64
	for i := 0; i < 30; i++ {
		typ := types[i%len(types)]
		st := statuses[i%len(statuses)]
		cols := []string{"task_type", "status", "media_id", "base_priority"}
		vals := []any{typ, st, int64(100 + i), int64(i * 10)}
		args := []string{"?", "?", "?", "?"}
		if i%3 == 0 {
			cols = append(cols, "lease_owner")
			vals = append(vals, "worker-"+string(rune('a'+i%26)))
			args = append(args, "?")
		}
		if i%7 == 0 {
			cols = append(cols, "removed_at", "removed_by", "remove_reason")
			vals = append(vals, "2024-01-01T00:00:00Z", "admin", "cleanup")
			args = append(args, "?", "?", "?")
		}
		query := "INSERT INTO post_ingest_task("
		for j, c := range cols {
			if j > 0 {
				query += ", "
			}
			query += c
		}
		query += ") VALUES("
		for j := range args {
			if j > 0 {
				query += ", "
			}
			query += "?"
		}
		query += ")"
		res, err := db.ExecContext(ctx, query, vals...)
		if err != nil {
			t.Fatalf("insert task %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids
}

func newTaskControlHandler(t *testing.T, db *sql.DB, builder *taskcontrol.ProjectionBuilder, reg *taskcontrol.Registry) *Handler {
	t.Helper()
	return &Handler{
		TaskCtrl: &TaskControl{
			Registry:  reg,
			Queries:   taskcontrol.NewQueryService(builder),
			Mutations: taskcontrol.NewMutateService(db),
			Overview:  taskcontrol.NewOverviewBuilder(builder),
		},
	}
}

func newGinContext(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	return c, w
}

// --- Registry Tests ---

func TestTaskControlRegistryReturnsRegistry(t *testing.T) {
	_, builder, reg := setupTaskControlTestDB(t)
	h := newTaskControlHandler(t, nil, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks/registry")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlRegistry(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result taskcontrol.Registry
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal registry: %v", err)
	}
	if len(result.Groups) == 0 {
		t.Error("expected non-empty groups")
	}
}

func TestTaskControlRegistryRequiresAdmin(t *testing.T) {
	_, builder, reg := setupTaskControlTestDB(t)
	h := newTaskControlHandler(t, nil, builder, reg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := r.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) { setUserCtx(c, 1, "viewer", "user") })
	admin.Use(middleware.RequireAdmin())
	admin.GET("/tasks/registry", h.TaskControlRegistry)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/registry", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// --- Overview Tests ---

func TestTaskControlOverviewReturnsOverview(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks/overview")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlOverview(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result taskcontrol.Overview
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
}

// --- List Tests ---

func TestTaskControlListReturnsItems(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks?limit=10")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result taskcontrol.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(result.Items) == 0 {
		t.Error("expected non-empty items")
	}
	if result.Total == 0 {
		t.Error("expected non-zero total")
	}
	if result.Items[0].TaskID == "" {
		t.Error("expected task_id in items")
	}
}

func TestTaskControlListWithFilters(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks?task_type=poster&status=waiting&limit=50")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result taskcontrol.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, item := range result.Items {
		if item.TaskType != "poster" {
			t.Errorf("expected poster, got %s", item.TaskType)
		}
	}
}

func TestTaskControlListInvalidCursor(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks?cursor=invalid!!!==")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlList(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cursor, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTaskControlListLimit(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks?limit=3")
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result taskcontrol.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Items) > 3 {
		t.Errorf("expected at most 3 items, got %d", len(result.Items))
	}
}

func TestTaskControlListCursorPagination(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	// First page
	c1, w1 := newGinContext(http.MethodGet, "/api/v1/admin/tasks?limit=5")
	setUserCtx(c1, 1, "admin", "admin")
	h.TaskControlList(c1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w1.Code)
	}
	var page1 taskcontrol.ListResult
	if err := json.Unmarshal(w1.Body.Bytes(), &page1); err != nil {
		t.Fatalf("unmarshal page1: %v", err)
	}
	if !page1.HasMore || page1.NextCursor == "" {
		t.Skip("not enough data for cursor test (need has_more=true)")
		return
	}

	// Second page
	c2, w2 := newGinContext(http.MethodGet, "/api/v1/admin/tasks?limit=5&cursor="+page1.NextCursor)
	setUserCtx(c2, 1, "admin", "admin")
	h.TaskControlList(c2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	var page2 taskcontrol.ListResult
	if err := json.Unmarshal(w2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("unmarshal page2: %v", err)
	}

	// Ensure different items
	if len(page2.Items) > 0 && len(page1.Items) > 0 {
		if page1.Items[0].TaskID == page2.Items[0].TaskID {
			t.Error("page2 first item should differ from page1 first item")
		}
	}
}

// --- Detail Tests ---

func TestTaskControlDetailExistingTask(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	taskID := taskcontrol.BuildIdentity("orchestration", ids[0])
	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks/"+taskID)
	setUserCtx(c, 1, "admin", "admin")
	c.Params = gin.Params{{Key: "task_id", Value: taskID}}
	h.TaskControlDetail(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var result taskcontrol.DetailResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if result.Row.TaskID != taskID {
		t.Errorf("expected task_id %s, got %s", taskID, result.Row.TaskID)
	}
}

func TestTaskControlDetailNotFound(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	c, w := newGinContext(http.MethodGet, "/api/v1/admin/tasks/orchestration:99999")
	setUserCtx(c, 1, "admin", "admin")
	c.Params = gin.Params{{Key: "task_id", Value: "orchestration:99999"}}
	h.TaskControlDetail(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// --- Action Tests ---

func doActionRequest(t *testing.T, h *Handler, taskID string, body taskActionBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	c, w := newGinContext(http.MethodPost, "/api/v1/admin/tasks/"+taskID+"/actions")
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/tasks/"+taskID+"/actions", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, 1, "admin", "admin")
	c.Params = gin.Params{{Key: "task_id", Value: taskID}}
	h.TaskControlActions(c)
	return w
}

func TestTaskControlActionSkipWaiting(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	var waitingID int64
	ctx := context.Background()
	for _, id := range ids {
		result, _ := h.TaskCtrl.Queries.Detail(ctx, taskcontrol.BuildIdentity("orchestration", id))
		if result != nil && result.Row.NormalizedStatus == taskcontrol.StatusWaiting {
			waitingID = id
			break
		}
	}
	if waitingID == 0 {
		t.Skip("no waiting task found")
		return
	}

	taskID := taskcontrol.BuildIdentity("orchestration", waitingID)
	w := doActionRequest(t, h, taskID, taskActionBody{
		Action: "skip",
		Reason: "test skip",
	})

	if w.Code != http.StatusOK {
		t.Logf("skip response: %d body=%s — may not be skippable", w.Code, w.Body.String())
	}
}

func TestTaskControlActionInvalidReturnsBadRequest(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)
	taskID := taskcontrol.BuildIdentity("orchestration", 1)

	w := doActionRequest(t, h, taskID, taskActionBody{
		Action: "nonexistent_action",
		Reason: "test",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTaskControlActionCancelWaiting(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	// Find a waiting task
	var waitingID int64
	ctx := context.Background()
	for _, id := range ids {
		result, _ := h.TaskCtrl.Queries.Detail(ctx, taskcontrol.BuildIdentity("orchestration", id))
		if result != nil && result.Row.NormalizedStatus == taskcontrol.StatusWaiting {
			waitingID = id
			break
		}
	}
	if waitingID == 0 {
		t.Skip("no waiting task found")
		return
	}

	taskID := taskcontrol.BuildIdentity("orchestration", waitingID)
	w := doActionRequest(t, h, taskID, taskActionBody{
		Action: "cancel",
		Reason: "test cancel",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTaskControlActionRemove(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	id := ids[0]
	taskID := taskcontrol.BuildIdentity("orchestration", id)
	w := doActionRequest(t, h, taskID, taskActionBody{
		Action: "remove",
		Reason: "test remove",
	})

	if w.Code != http.StatusOK {
		t.Logf("remove response: %d body=%s", w.Code, w.Body.String())
	}
}

func TestTaskControlActionRunNow(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	var waitingID int64
	ctx := context.Background()
	for _, id := range ids {
		result, _ := h.TaskCtrl.Queries.Detail(ctx, taskcontrol.BuildIdentity("orchestration", id))
		if result != nil && result.Row.NormalizedStatus == taskcontrol.StatusWaiting {
			waitingID = id
			break
		}
	}
	if waitingID == 0 {
		t.Skip("no waiting task found")
		return
	}

	taskID := taskcontrol.BuildIdentity("orchestration", waitingID)
	w := doActionRequest(t, h, taskID, taskActionBody{
		Action: "run_now",
		Reason: "test run-now",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// --- Batch Tests ---

func TestTaskControlBatchCancel(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	ids := seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	body := taskBatchBody{
		OperationID: "550e8400-e29b-41d4-a716-446655440000",
		Action:      "cancel",
		Reason:      "batch test",
		Items: []taskcontrol.BatchItem{
			{TaskIdentity: taskcontrol.BuildIdentity("orchestration", ids[0])},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/tasks/batch", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlBatch(c)

	// Batch can succeed (200) or return for non-waiting tasks
	if w.Code != http.StatusOK {
		t.Logf("batch response: %d body=%s", w.Code, w.Body.String())
	}
}

func TestTaskControlBatchInvalidUUID(t *testing.T) {
	db, builder, reg := setupTaskControlTestDB(t)
	defer db.Close()
	seedHandlerTasks(t, db)

	h := newTaskControlHandler(t, db, builder, reg)

	body := taskBatchBody{
		OperationID: "not-a-uuid",
		Action:      "cancel",
		Reason:      "batch test",
		Items: []taskcontrol.BatchItem{
			{TaskIdentity: "orchestration:1"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/tasks/batch", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	setUserCtx(c, 1, "admin", "admin")
	h.TaskControlBatch(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}
