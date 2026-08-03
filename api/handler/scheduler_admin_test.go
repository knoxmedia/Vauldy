package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/app"
	"knox-media/internal/scheduler"
	"knox-media/internal/store"
)

// seedSchedulerAdminPolicy persists base as the active policy revision and
// returns its revision id (the expected_revision for the next write).
func seedSchedulerAdminPolicy(t *testing.T, db *sql.DB, base scheduler.Policy) int64 {
	t.Helper()
	ctx := context.Background()
	st := scheduler.NewStore(db)
	raw, err := scheduler.EncodePolicyJSON(base)
	if err != nil {
		t.Fatalf("encode policy: %v", err)
	}
	rev, err := st.CreatePolicyRevision(ctx, 1, nil, raw, "system", "startup defaults", "startup")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := st.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

// newSchedulerAdminTestHandler wires a Handler with a scheduler service backed
// by a real sqlite database. The base policy is seeded as the active revision.
func newSchedulerAdminTestHandler(t *testing.T, base scheduler.Policy) (*Handler, *scheduler.Service, *sql.DB, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scheduler-admin.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	revID := seedSchedulerAdminPolicy(t, db, base)
	svc := scheduler.NewService(db)
	svc.SetBasePolicy(base)
	svc.SetPolicy(base)
	h := &Handler{App: &app.App{DB: db}, SchedulerAdmin: svc}
	return h, svc, db, revID
}

// doSchedulerAdmin executes a handler method on a gin context with the given
// user context and body.
func doSchedulerAdmin(t *testing.T, h *Handler, method, path string, body any, userID int64, role, username string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(mustJSON(t, body))
	} else {
		reader = bytes.NewReader(nil)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, userID, role, username)
	switch method {
	case http.MethodGet:
		h.SchedulerAdminGetPolicy(c)
	case http.MethodPut:
		h.SchedulerAdminPutPolicy(c)
	case http.MethodPatch:
		h.SchedulerAdminPatchPolicy(c)
	case http.MethodPost:
		h.SchedulerAdminControl(c)
	}
	return w
}

func TestSchedulerAdminRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, _, _ := newSchedulerAdminTestHandler(t, scheduler.PolicyDefaults())

	r := gin.New()
	admin := r.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) { setUserCtx(c, 1, "user", "viewer") })
	admin.Use(middleware.RequireAdmin())
	admin.GET("/scheduler/policy", h.SchedulerAdminGetPolicy)
	admin.PUT("/scheduler/policy", h.SchedulerAdminPutPolicy)
	admin.PATCH("/scheduler/policy", h.SchedulerAdminPatchPolicy)
	admin.POST("/scheduler/control", h.SchedulerAdminControl)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/scheduler/policy"},
		{http.MethodPut, "/api/v1/admin/scheduler/policy"},
		{http.MethodPatch, "/api/v1/admin/scheduler/policy"},
		{http.MethodPost, "/api/v1/admin/scheduler/control"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s want 403", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// Admin is allowed through the same route group.
	r2 := gin.New()
	admin2 := r2.Group("/api/v1/admin")
	admin2.Use(func(c *gin.Context) { setUserCtx(c, 2, "admin", "admin") })
	admin2.Use(middleware.RequireAdmin())
	admin2.GET("/scheduler/policy", h.SchedulerAdminGetPolicy)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/scheduler/policy", nil)
	r2.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s want 200", w.Code, w.Body.String())
	}
}

func TestSchedulerAdminGetPolicyReturnsProvenance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	base.MergeYAML(scheduler.SchedulerYAMLConfig{TypeConcurrency: map[string]int{"poster": 5}})
	h, _, _, _ := newSchedulerAdminTestHandler(t, base)

	w := doSchedulerAdmin(t, h, http.MethodGet, "/api/v1/admin/scheduler/policy", nil, 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s want 200", w.Code, w.Body.String())
	}
	var body struct {
		Revision int64  `json:"revision"`
		Active   bool   `json:"active"`
		Actor    string `json:"actor"`
		Reason   string `json:"reason"`
		Policy   struct {
			TypeConcurrency map[string]int  `json:"type_concurrency"`
			Provenance      map[string]string `json:"provenance"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.Revision <= 0 || !body.Active {
		t.Fatalf("revision=%d active=%v", body.Revision, body.Active)
	}
	if body.Actor != "system" || body.Reason != "startup defaults" {
		t.Fatalf("actor=%q reason=%q", body.Actor, body.Reason)
	}
	if body.Policy.TypeConcurrency["poster"] != 5 {
		t.Fatalf("poster=%d want 5", body.Policy.TypeConcurrency["poster"])
	}
	if body.Policy.Provenance["concurrency.poster"] != "yaml" {
		t.Fatalf("provenance=%q want yaml", body.Policy.Provenance["concurrency.poster"])
	}
	if body.Policy.Provenance["concurrency.scan"] != "default" {
		t.Fatalf("provenance=%q want default", body.Policy.Provenance["concurrency.scan"])
	}
}

func TestSchedulerAdminPutPolicyRevisionConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	h, svc, _, revID := newSchedulerAdminTestHandler(t, base)
	ctx := context.Background()

	// Advance to revision 2 first.
	res, err := svc.ApplyRuntimeOverride(ctx, scheduler.RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 2},
		Author:           "admin",
		Reason:           "first",
	})
	if err != nil {
		t.Fatal(err)
	}

	// PUT with a stale expected_revision returns 409 and the current revision.
	w := doSchedulerAdmin(t, h, http.MethodPut, "/api/v1/admin/scheduler/policy",
		map[string]any{"expected_revision": revID, "reason": "stale", "concurrency": map[string]int{"poster": 1}},
		2, "admin", "admin")
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s want 409", w.Code, w.Body.String())
	}
	var resp struct {
		Error            string `json:"error"`
		ExpectedRevision int64  `json:"expected_revision"`
		CurrentRevision  int64  `json:"current_revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "revision_conflict" {
		t.Fatalf("error=%q", resp.Error)
	}
	if resp.CurrentRevision != res.RevisionID {
		t.Fatalf("current_revision=%d want %d", resp.CurrentRevision, res.RevisionID)
	}
}

func TestSchedulerAdminPutPolicyInvalid422NoActivation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	h, _, db, revID := newSchedulerAdminTestHandler(t, base)
	st := scheduler.NewStore(db)
	ctx := context.Background()
	before, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	w := doSchedulerAdmin(t, h, http.MethodPut, "/api/v1/admin/scheduler/policy",
		map[string]any{"expected_revision": revID, "reason": "bad", "concurrency": map[string]int{"poster": -1}},
		2, "admin", "admin")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s want 422", w.Code, w.Body.String())
	}
	var resp struct {
		Error            string   `json:"error"`
		ValidationErrors []string `json:"validation_errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "validation_failed" || len(resp.ValidationErrors) == 0 {
		t.Fatalf("resp=%+v", resp)
	}
	// No activation and no audit on invalid update.
	after, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatalf("active revision changed from %d to %d", before.ID, after.ID)
	}
	entries, _ := st.ListAudit(ctx, 10)
	if len(entries) != 0 {
		t.Fatalf("audit entries=%d want 0", len(entries))
	}
}

func TestSchedulerAdminPatchPolicyValidActivation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	h, _, db, revID := newSchedulerAdminTestHandler(t, base)
	st := scheduler.NewStore(db)
	ctx := context.Background()

	w := doSchedulerAdmin(t, h, http.MethodPatch, "/api/v1/admin/scheduler/policy",
		map[string]any{"expected_revision": revID, "reason": "raise ingest", "concurrency": map[string]int{"ingest": 6}},
		2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Revision int64  `json:"revision"`
		Active   bool   `json:"active"`
		Actor    string `json:"actor"`
		Reason   string `json:"reason"`
		Policy   struct {
			TypeConcurrency map[string]int    `json:"type_concurrency"`
			Provenance      map[string]string `json:"provenance"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Revision <= revID || !resp.Active {
		t.Fatalf("revision=%d active=%v want new active revision", resp.Revision, resp.Active)
	}
	if resp.Actor != "admin" || resp.Reason != "raise ingest" {
		t.Fatalf("actor=%q reason=%q", resp.Actor, resp.Reason)
	}
	if resp.Policy.TypeConcurrency["ingest"] != 6 {
		t.Fatalf("ingest=%d want 6", resp.Policy.TypeConcurrency["ingest"])
	}
	if resp.Policy.Provenance["concurrency.ingest"] != "override" {
		t.Fatalf("provenance=%q want override", resp.Policy.Provenance["concurrency.ingest"])
	}
	// Durable activation: active revision is the returned one and audit exists.
	active, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != resp.Revision {
		t.Fatalf("active revision=%d want %d", active.ID, resp.Revision)
	}
	entries, _ := st.ListAudit(ctx, 10)
	if len(entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(entries))
	}
	if entries[0].Actor != "admin" {
		t.Fatalf("audit actor=%q want admin", entries[0].Actor)
	}
}

func TestSchedulerAdminPutPolicyFullReplaceClearsOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	base.MergeYAML(scheduler.SchedulerYAMLConfig{TypeConcurrency: map[string]int{"poster": 5, "ingest": 7}})
	h, svc, _, revID := newSchedulerAdminTestHandler(t, base)
	ctx := context.Background()

	// PATCH first: override both poster and ingest.
	res, err := svc.ApplyRuntimeOverride(ctx, scheduler.RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 2, "ingest": 4},
		Author:           "admin",
		Reason:           "override both",
	})
	if err != nil {
		t.Fatal(err)
	}

	// PUT full replace: only poster override survives; ingest falls back to YAML.
	w := doSchedulerAdmin(t, h, http.MethodPut, "/api/v1/admin/scheduler/policy",
		map[string]any{"expected_revision": res.RevisionID, "reason": "replace", "concurrency": map[string]int{"poster": 1}},
		2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s want 200", w.Code, w.Body.String())
	}
	var resp struct {
		Policy struct {
			TypeConcurrency map[string]int    `json:"type_concurrency"`
			Provenance      map[string]string `json:"provenance"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Policy.TypeConcurrency["poster"] != 1 {
		t.Fatalf("poster=%d want 1", resp.Policy.TypeConcurrency["poster"])
	}
	if resp.Policy.TypeConcurrency["ingest"] != 7 {
		t.Fatalf("ingest=%d want 7 (cleared by full replace, falls back to YAML)", resp.Policy.TypeConcurrency["ingest"])
	}
	if resp.Policy.Provenance["concurrency.ingest"] != "yaml" {
		t.Fatalf("ingest provenance=%q want yaml", resp.Policy.Provenance["concurrency.ingest"])
	}
}

func TestSchedulerAdminControlPauseResumeDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	h, _, db, _ := newSchedulerAdminTestHandler(t, base)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until)
		 VALUES('owner/drain-1','poster',1,(SELECT id FROM scheduler_policy_revision WHERE is_active=1),'active',?)`,
		time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Pause.
	w := doSchedulerAdmin(t, h, http.MethodPost, "/api/v1/admin/scheduler/control",
		map[string]any{"task_type": "poster", "command": "pause", "reason": "maintenance"}, 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", w.Code, w.Body.String())
	}
	var pause struct {
		TaskType         string `json:"task_type"`
		State            string `json:"state"`
		Revision         int64  `json:"revision"`
		LiveReservations int    `json:"live_reservations"`
		Drained          bool   `json:"drained"`
		Actor            string `json:"actor"`
		Reason           string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pause); err != nil {
		t.Fatal(err)
	}
	if pause.TaskType != "poster" || pause.State != "paused" || pause.Actor != "admin" || pause.Reason != "maintenance" {
		t.Fatalf("pause resp=%+v", pause)
	}

	// Resume.
	w = doSchedulerAdmin(t, h, http.MethodPost, "/api/v1/admin/scheduler/control",
		map[string]any{"task_type": "poster", "command": "resume", "expected_revision": pause.Revision, "reason": "back"}, 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", w.Code, w.Body.String())
	}
	var resume struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resume)
	if resume.State != "running" {
		t.Fatalf("resume state=%q want running", resume.State)
	}

	// Drain with a live reservation.
	w = doSchedulerAdmin(t, h, http.MethodPost, "/api/v1/admin/scheduler/control",
		map[string]any{"task_type": "poster", "command": "drain", "reason": "drain it"}, 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("drain status=%d body=%s", w.Code, w.Body.String())
	}
	var drain struct {
		State            string `json:"state"`
		LiveReservations int    `json:"live_reservations"`
		Drained          bool   `json:"drained"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &drain); err != nil {
		t.Fatal(err)
	}
	if drain.State != "draining" || drain.LiveReservations != 1 || drain.Drained {
		t.Fatalf("drain resp=%+v", drain)
	}
}

func TestSchedulerAdminControlRevisionConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := scheduler.PolicyDefaults()
	h, _, _, _ := newSchedulerAdminTestHandler(t, base)

	w := doSchedulerAdmin(t, h, http.MethodPost, "/api/v1/admin/scheduler/control",
		map[string]any{"task_type": "poster", "command": "pause", "reason": "first"}, 2, "admin", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", w.Code, w.Body.String())
	}

	// Stale expected_revision for control → 409.
	w = doSchedulerAdmin(t, h, http.MethodPost, "/api/v1/admin/scheduler/control",
		map[string]any{"task_type": "poster", "command": "resume", "expected_revision": 999, "reason": "stale"}, 2, "admin", "admin")
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "revision_conflict") {
		t.Fatalf("body=%s want revision_conflict", w.Body.String())
	}
}
