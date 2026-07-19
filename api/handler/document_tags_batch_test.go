package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"knox-media/internal/app"
	"knox-media/internal/store"
)

func setupDocumentTagTestHandler(t *testing.T) *Handler {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "documents.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
        INSERT INTO library(id,name,type,path,enabled) VALUES
          (1,'docs','document','E:/docs',1),(2,'other','document','E:/other',1);
        INSERT INTO user(id,username,password,role,library_scope) VALUES
          (1,'limited','x','user','selected'),(2,'admin','x','admin','all'),(3,'folder','x','user','selected');
        INSERT INTO user_library_permission(user_id,library_id) VALUES(1,1),(3,1);
        INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(3,1,'E:/docs/allowed');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES
          (11,1,'d11','One','E:/docs/allowed/one.pdf','document','active'),
          (12,1,'d12','Two','E:/docs/denied/two.pdf','document','active'),
          (13,2,'d13','Three','E:/other/three.pdf','document','active'),
          (14,1,'v14','Video','E:/docs/allowed/video.mp4','video','active'),
          (15,1,'d15','Gone','E:/docs/allowed/gone.pdf','document','deleted');
        INSERT INTO document_tag(media_id,tag) VALUES(11,'Alpha'),(11,'beta'),(12,'Beta');`)
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func patchDocumentTags(t *testing.T, h *Handler, userID int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/documents/tags", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, userID, map[int64]string{1: "user", 2: "admin", 3: "user"}[userID], "test")
	h.BatchUpdateDocumentTags(c)
	return w
}

func tagsFor(t *testing.T, h *Handler, id int64) []string {
	t.Helper()
	rows, err := h.App.DB.Query(`SELECT tag FROM document_tag WHERE media_id=? ORDER BY tag COLLATE NOCASE, tag`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatal(err)
		}
		out = append(out, tag)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDocumentTagsBatchRouteIsRegistered(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `auth.PATCH("/documents/tags", h.BatchUpdateDocumentTags)`) {
		t.Fatal("authenticated PATCH /documents/tags route is not registered")
	}
}

func TestBatchUpdateDocumentTagsModesNormalizeAndReturnDeterministically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, body     string
		want11, want12 []string
	}{
		{"add", `{"media_ids":[12,11,11],"mode":"add","tags":[" beta ","Gamma","gamma"]}`, []string{"Alpha", "beta", "Gamma"}, []string{"Beta", "Gamma"}},
		{"remove", `{"media_ids":[12,11],"mode":"remove","tags":[" BETA "]}`, []string{"Alpha"}, nil},
		{"replace", `{"media_ids":[12,11],"mode":"replace","tags":[" zed ","Alpha","alpha"]}`, []string{"Alpha", "zed"}, []string{"Alpha", "zed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupDocumentTagTestHandler(t)
			w := patchDocumentTags(t, h, 2, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if got := tagsFor(t, h, 11); fmt.Sprint(got) != fmt.Sprint(tc.want11) {
				t.Fatalf("11 tags=%v want %v", got, tc.want11)
			}
			if got := tagsFor(t, h, 12); fmt.Sprint(got) != fmt.Sprint(tc.want12) {
				t.Fatalf("12 tags=%v want %v", got, tc.want12)
			}
			var payload struct {
				Updated int `json:"updated"`
				Items   []struct {
					MediaID int64    `json:"media_id"`
					Tags    []string `json:"tags"`
				} `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Updated != 2 || len(payload.Items) != 2 || payload.Items[0].MediaID != 12 || payload.Items[1].MediaID != 11 {
				t.Fatalf("unexpected response: %+v", payload)
			}
		})
	}
}

func TestBatchUpdateDocumentTagsRejectsValidationWithoutWrites(t *testing.T) {
	cases := []string{
		`{"media_ids":[],"mode":"add","tags":["x"]}`,
		`{"media_ids":[0],"mode":"add","tags":["x"]}`,
		`{"media_ids":[11],"mode":"bad","tags":["x"]}`,
		`{"media_ids":[11],"mode":"add","tags":[]}`,
		`{"media_ids":[11],"mode":"add","tags":["` + strings.Repeat("x", 65) + `"]}`,
		`{"media_ids":[11],"mode":"add","tags":["bad\u0001tag"]}`,
	}
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint(i + 1)
	}
	cases = append(cases, `{"media_ids":[`+strings.Join(tooMany, ",")+`],"mode":"add","tags":["x"]}`)
	for i, body := range cases {
		h := setupDocumentTagTestHandler(t)
		before := fmt.Sprint(tagsFor(t, h, 11))
		w := patchDocumentTags(t, h, 2, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("case %d status=%d body=%s", i, w.Code, w.Body.String())
		}
		if fmt.Sprint(tagsFor(t, h, 11)) != before {
			t.Fatalf("case %d wrote tags", i)
		}
	}
}

func TestBatchUpdateDocumentTagsRejectsMissingNonDocumentInactiveAndUnauthorizedAtomically(t *testing.T) {
	cases := []struct {
		name string
		uid  int64
		ids  string
	}{{"missing", 2, "11,999"}, {"non-document", 2, "11,14"}, {"inactive", 2, "11,15"}, {"library", 1, "11,13"}, {"folder", 3, "11,12"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupDocumentTagTestHandler(t)
			w := patchDocumentTags(t, h, tc.uid, `{"media_ids":[`+tc.ids+`],"mode":"replace","tags":["changed"]}`)
			if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if got := tagsFor(t, h, 11); fmt.Sprint(got) != "[Alpha beta]" {
				t.Fatalf("partial write: %v", got)
			}
		})
	}
}

func TestBatchUpdateDocumentTagsEnforcesFinalLimitAndRollsBackDatabaseFailure(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	values := make([]string, 49)
	for i := range values {
		values[i] = fmt.Sprintf("t%02d", i)
	}
	for _, tag := range values {
		if _, err := h.App.DB.Exec(`INSERT INTO document_tag(media_id,tag) VALUES(11,?)`, tag); err != nil {
			t.Fatal(err)
		}
	}
	w := patchDocumentTags(t, h, 2, `{"media_ids":[11],"mode":"add","tags":["overflow"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("limit status=%d body=%s", w.Code, w.Body.String())
	}
	h = setupDocumentTagTestHandler(t)
	if _, err := h.App.DB.Exec(`CREATE TRIGGER fail_doc_tag BEFORE INSERT ON document_tag WHEN NEW.media_id=12 BEGIN SELECT RAISE(ABORT,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	w = patchDocumentTags(t, h, 2, `{"media_ids":[11,12],"mode":"replace","tags":["changed"]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("db status=%d body=%s", w.Code, w.Body.String())
	}
	if got := tagsFor(t, h, 11); fmt.Sprint(got) != "[Alpha beta]" {
		t.Fatalf("transaction not rolled back: %v", got)
	}
}

func TestBatchUpdateDocumentTagsRejectsTooManyNormalizedTagsBeforeTransaction(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	tags := make([]string, 51)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag-%02d", i)
	}
	payload, err := json.Marshal(batchUpdateDocumentTagsBody{MediaIDs: []int64{11}, Mode: "replace", Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	w := patchDocumentTags(t, h, 2, string(payload))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := tagsFor(t, h, 11); fmt.Sprint(got) != "[Alpha beta]" {
		t.Fatalf("validation wrote tags: %v", got)
	}
}

func TestBatchUpdateDocumentTagsCapsRequestBodyBeforeBinding(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	body := `{"media_ids":[11],"mode":"replace","tags":["x"],"padding":"` + strings.Repeat("x", 70<<10) + `"}`
	w := patchDocumentTags(t, h, 2, body)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := tagsFor(t, h, 11); fmt.Sprint(got) != "[Alpha beta]" {
		t.Fatalf("oversized body wrote tags: %v", got)
	}
}

func openCountingDocumentTagBatchDB(t *testing.T, documents int) (*sql.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "document-tags-count.sqlite")
	bootstrap, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrap.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'docs','document','E:/docs',1); INSERT INTO user(id,username,password,role,library_scope) VALUES(2,'admin','x','admin','all')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= documents; i++ {
		if _, err = bootstrap.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(?,1,?,?,?,'document','active')`, i, fmt.Sprintf("d%d", i), fmt.Sprintf("Doc %d", i), fmt.Sprintf("E:/docs/%d.pdf", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	return openCountingSQLitePath(t, path)
}

func TestBatchUpdateDocumentTagsUsesConstantDatabaseOperations(t *testing.T) {
	db, counter := openCountingDocumentTagBatchDB(t, 200)
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	counts := make([]int64, 0, 2)
	for _, count := range []int{1, 200} {
		ids := make([]int64, count)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		payload, err := json.Marshal(batchUpdateDocumentTagsBody{MediaIDs: ids, Mode: "replace", Tags: []string{"one", "two"}})
		if err != nil {
			t.Fatal(err)
		}
		counter.Store(0)
		w := patchDocumentTags(t, h, 2, string(payload))
		if w.Code != http.StatusOK {
			t.Fatalf("count=%d status=%d body=%s", count, w.Code, w.Body.String())
		}
		counts = append(counts, counter.Load())
	}
	if counts[0] != 11 || counts[1] != 11 {
		t.Fatalf("database operations for 1/200 documents=%v, want [11 11]", counts)
	}
}

func TestBatchUpdateDocumentTagsMapsCanceledContextWithoutWrites(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/documents/tags", bytes.NewBufferString(`{"media_ids":[11],"mode":"replace","tags":["changed"]}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, 2, "admin", "admin")
	h.BatchUpdateDocumentTags(c)
	if w.Code != http.StatusRequestTimeout || !strings.Contains(w.Body.String(), `"code":"document_tags_canceled"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := tagsFor(t, h, 11); fmt.Sprint(got) != "[Alpha beta]" {
		t.Fatalf("canceled request wrote tags: %v", got)
	}
}

func TestBatchUpdateDocumentTagsMapsLockedDatabaseDeadlineWithoutPartialWrites(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	lockTx, err := h.App.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockTx.Exec(`UPDATE document_tag SET tag=tag WHERE media_id=11`); err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	started := time.Now()
	w := patchDocumentTags(t, h, 2, `{"media_ids":[11],"mode":"replace","tags":["changed"]}`)
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":"document_tags_timeout"`) {
		t.Fatalf("elapsed=%s status=%d body=%s", time.Since(started), w.Code, w.Body.String())
	}
}

func TestBatchUpdateDocumentTagsReturnsAuthoritativeFacetDeltasForAllTargets(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(16,1,'d16','Hidden','E:/docs/allowed/hidden.pdf','document','active'); INSERT INTO document_tag(media_id,tag) VALUES(16,'HIDDEN'),(16,'Shared'),(11,'shared')`); err != nil {
		t.Fatal(err)
	}
	w := patchDocumentTags(t, h, 2, `{"media_ids":[11,16],"mode":"replace","tags":["shared","New"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		FacetDeltas []struct {
			Tag   string `json:"tag"`
			Delta int    `json:"delta"`
		} `json:"facet_deltas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, delta := range payload.FacetDeltas {
		got[delta.Tag] = delta.Delta
	}
	want := map[string]int{"alpha": -1, "beta": -1, "hidden": -1, "new": 2}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("facet deltas=%v want %v body=%s", got, want, w.Body.String())
	}
	if _, exists := got["shared"]; exists {
		t.Fatalf("case-only/equivalent shared tag must have zero omitted delta: %v", got)
	}
}

func TestDocumentTagFilterAndFacetsAreCaseInsensitive(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	items, err := h.queryDocumentsContext(context.Background(), 1, documentListQuery{Tag: "ALPHA"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0]["id"] != int64(11) {
		t.Fatalf("filtered items=%v", items)
	}
	if _, err = h.App.DB.Exec(`INSERT INTO document_tag(media_id,tag) VALUES(12,'alpha')`); err != nil {
		t.Fatal(err)
	}
	facets, err := h.queryDocumentFacets(1, "tag")
	if err != nil {
		t.Fatal(err)
	}
	var alphaCount int64
	for _, facet := range facets {
		if strings.EqualFold(facet["name"].(string), "alpha") {
			alphaCount += facet["count"].(int64)
		}
	}
	if alphaCount != 2 {
		t.Fatalf("alpha facet count=%d facets=%v", alphaCount, facets)
	}
}

func TestBatchUpdateDocumentTagsRejectsUnauthorizedBeforeWaitingForWriteLock(t *testing.T) {
	h := setupDocumentTagTestHandler(t)
	lockTx, err := h.App.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockTx.Exec(`UPDATE document_tag SET tag=tag WHERE media_id=11`); err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	started := time.Now()
	w := patchDocumentTags(t, h, 1, `{"media_ids":[13],"mode":"replace","tags":["changed"]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("elapsed=%s status=%d body=%s", time.Since(started), w.Code, w.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("unauthorized preflight waited for write lock: %s", elapsed)
	}
}
