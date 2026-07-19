package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBuildMediaQueryFastPathHasBoundLimit(t *testing.T) {
	q, err := buildMediaQuery(mediaListSpec{Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100, UserID: 7}, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "LIMIT ?") || strings.Contains(strings.ToUpper(q.SQL), "OFFSET") {
		t.Fatalf("unsafe pagination SQL: %s", q.SQL)
	}
	if q.NeedsGoFilter {
		t.Fatal("plain query unexpectedly needs Go filtering")
	}
	if got := q.Args[len(q.Args)-1]; got != 24 {
		t.Fatalf("limit arg=%v", got)
	}
}

func TestBuildMediaQueryRejectsUnknownSort(t *testing.T) {
	_, err := buildMediaQuery(mediaListSpec{Sort: mediaSort("id_desc; DROP TABLE media"), Limit: 1}, nil, 1)
	if err == nil {
		t.Fatal("unknown sort accepted")
	}
}

func TestBuildMediaQueryBindsLibraryIDsLimitAndCursor(t *testing.T) {
	raw := "x%' OR 1=1 --"
	spec := mediaListSpec{AllowedLibraryIDs: []int64{9, 3}, Search: raw, Sort: mediaSortCreatedDesc, Limit: 7, BatchSize: 100, UserID: 4}
	q, err := buildMediaQuery(spec, &mediaCursor{SortKey: ptrString("2026-01-01T00:00:00.000000Z"), ID: 22}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.SQL, raw) || strings.Contains(q.SQL, "2026-01-01") {
		t.Fatalf("values interpolated: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "m.created_at_sort < ?") || !strings.Contains(q.SQL, "m.created_at_sort = ? AND m.id < ?") {
		t.Fatalf("missing keyset: %s", q.SQL)
	}
	if strings.Contains(strings.ToUpper(q.SQL), "OFFSET") {
		t.Fatalf("OFFSET present: %s", q.SQL)
	}
	want := []any{int64(4), int64(9), int64(3)}
	for i := range want {
		if q.Args[i] != want[i] {
			t.Fatalf("arg[%d]=%v want %v; all=%#v", i, q.Args[i], want[i], q.Args)
		}
	}
	if q.Args[len(q.Args)-1] != 100 {
		t.Fatalf("limit not bound: %#v", q.Args)
	}
}

func TestParseMediaListSpecLimitsAndGoFilterClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		target       string
		limit, batch int
		goFilter     bool
	}{
		{"/?file_type=image&limit=99999&photo_place=p&photo_person=8", 5000, 500, false},
		{"/?limit=2", 2, 100, false},
		{"/?file_type=image&limit=30&photo_tag=custom%3Arare", 30, 100, true},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
		spec, err := parseMediaListSpec(c, userPermissionProfile{}, 5)
		if err != nil {
			t.Fatalf("%s: %v", tc.target, err)
		}
		if spec.Limit != tc.limit || spec.BatchSize != tc.batch {
			t.Fatalf("%s: limit/batch=%d/%d", tc.target, spec.Limit, spec.BatchSize)
		}
		q, err := buildMediaQuery(spec, nil, spec.Limit)
		if err != nil {
			t.Fatal(err)
		}
		if q.NeedsGoFilter != tc.goFilter {
			t.Fatalf("%s NeedsGoFilter=%v", tc.target, q.NeedsGoFilter)
		}
	}
}

func TestListMediaStableCreatedAndTakenTie(t *testing.T) {
	h := setupAccessTestDB(t)
	for _, id := range []int64{31, 32, 33} {
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at,created_at_sort,photo_taken_at,meta_json) VALUES(?,1,?,?,'image','2026-01-01','2026-01-01T00:00:00.000000Z','2026-02-01T00:00:00.000000Z','{}')`, id, fmt.Sprintf("f-%d", id), fmt.Sprintf("E:/lib1/%d.jpg", id))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, sort := range []string{"created_desc", "taken_desc"} {
		c, w := listMediaTestContext("/api/v1/media?file_type=image&sort="+sort+"&limit=3", 2)
		h.ListMedia(c)
		ids := responseMediaIDs(t, w)
		if fmt.Sprint(ids) != "[33 32 31]" {
			t.Fatalf("%s ids=%v", sort, ids)
		}
	}
}

func TestListMediaFolderAndTagLowHitScansUntilExhausted(t *testing.T) {
	h := setupAccessTestDB(t)
	_, _ = h.App.DB.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,'E:/lib1/hit')`)
	for i := 1; i <= 230; i++ {
		path, meta := fmt.Sprintf("E:/lib1/miss/%03d.jpg", i), `{"photo":{"tags":["other"]}}`
		if i == 1 || i == 121 || i == 229 {
			path = fmt.Sprintf("E:/lib1/hit/%03d.jpg", i)
			meta = `{"photo":{"tags":["rare"]}}`
		}
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(?,1,?,?,'image',?,?,?)`, 1000+i, fmt.Sprintf("low-%d", i), path, fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60), meta)
		if err != nil {
			t.Fatal(err)
		}
	}
	c, w := listMediaTestContext("/api/v1/media?file_type=image&photo_tag=custom%3Arare&sort=id_desc&limit=3", 1)
	h.ListMedia(c)
	if ids := responseMediaIDs(t, w); len(ids) != 3 {
		t.Fatalf("low-hit result=%v body=%s", ids, w.Body.String())
	}
}

func TestListMediaCancellationReturnsNoPartialSuccess(t *testing.T) {
	h := setupAccessTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := h.listMediaRows(ctx, mediaListSpec{Sort: mediaSortIDDesc, Limit: 10, BatchSize: 100})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func listMediaTestContext(target string, uid int64) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if uid == 1 {
		setUserCtx(c, 1, "user", "normal")
	} else {
		setUserCtx(c, 2, "admin", "admin")
	}
	return c, w
}

func responseMediaIDs(t *testing.T, w *httptest.ResponseRecorder) []int64 {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(body.Items))
	for i := range body.Items {
		ids[i] = body.Items[i].ID
	}
	return ids
}

func TestListMediaSelectedScopeWithNoLibrariesReturnsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`DELETE FROM user_library_permission WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	c, w := listMediaTestContext("/api/v1/media", 1)
	h.ListMedia(c)
	if ids := responseMediaIDs(t, w); len(ids) != 0 {
		t.Fatalf("items=%v", ids)
	}
}

func TestListMediaSQLFiltersPlaceAndPerson(t *testing.T) {
	h := setupAccessTestDB(t)
	_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,photo_place_id,meta_json) VALUES(41,1,'p41','E:/lib1/41.jpg','image','2026-01-01T00:00:00.000000Z','2026-01-01T00:00:00.000000Z','home','{}'),(42,1,'p42','E:/lib1/42.jpg','image','2026-01-02T00:00:00.000000Z','2026-01-02T00:00:00.000000Z','away','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.App.DB.Exec(`INSERT INTO photo_person(id,library_id,label) VALUES(77,1,'person')`); err != nil {
		t.Fatal(err)
	}
	if _, err = h.App.DB.Exec(`INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(410,41,1,77,.1,.1,.2,.2)`); err != nil {
		t.Fatal(err)
	}
	c, w := listMediaTestContext("/api/v1/media?file_type=image&photo_place=home&photo_person=77", 2)
	h.ListMedia(c)
	if ids := responseMediaIDs(t, w); fmt.Sprint(ids) != "[41]" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestMediaQueryCreatedAndTakenPlansAvoidTempSort(t *testing.T) {
	h := setupAccessTestDB(t)
	for _, sortName := range []mediaSort{mediaSortCreatedDesc, mediaSortTakenDesc} {
		spec := mediaListSpec{LibraryID: ptrInt64(1), FileType: "image", Sort: sortName, Limit: 10, BatchSize: 100}
		q, err := buildMediaQuery(spec, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := h.App.DB.Query("EXPLAIN QUERY PLAN "+q.SQL, q.Args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		joined := strings.Join(details, "\n")
		// The final sort is over at most the materialized candidate batch; the
		// candidate CTE itself must still use the timeline index before LIMIT.
		if sortName == mediaSortTakenDesc && !strings.Contains(joined, "idx_media_library_type_photo_timeline_id") {
			t.Fatalf("taken_desc plan=%v, want timeline expression index", details)
		}
	}
}
func ptrInt64(v int64) *int64    { return &v }
func ptrString(v string) *string { return &v }

func TestListMediaUnknownSortReturnsBadRequest(t *testing.T) {
	h := setupAccessTestDB(t)
	c, w := listMediaTestContext("/api/v1/media?sort=bogus", 2)
	h.ListMedia(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestParseMediaListSpecCapturesUserPermissionsAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?library_id=7&file_type=image&q=needle&photo_tag=builtin&photo_place=home&photo_person=9&sort=taken_desc&limit=12", nil)
	profile := userPermissionProfile{LibraryScope: "selected", AllowedLibraryIDs: map[int64]struct{}{7: {}, 3: {}}, AllowedLibraryFolders: map[int64][]string{7: {"E:/allowed"}}}
	spec, err := parseMediaListSpec(c, profile, 44)
	if err != nil {
		t.Fatal(err)
	}
	if spec.LibraryID == nil || *spec.LibraryID != 7 || spec.FileType != "image" || spec.Search != "needle" || spec.PhotoTag != "builtin" || spec.PhotoPlace != "home" || spec.PhotoPerson != "9" || spec.Sort != mediaSortTakenDesc || spec.Limit != 12 || spec.BatchSize != 100 || spec.UserID != 44 || !spec.RestrictLibraries {
		t.Fatalf("spec=%+v", spec)
	}
	if fmt.Sprint(spec.AllowedLibraryIDs) != "[3 7]" {
		t.Fatalf("ids=%v", spec.AllowedLibraryIDs)
	}
}

func TestListMediaTakenSortFallsBackForNullPhotoTime(t *testing.T) {
	h := setupAccessTestDB(t)
	_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(61,1,'null-taken','E:/lib1/null.jpg','image','2026-03-01T00:00:00.000000Z',NULL,'{}'),(62,1,'real-taken','E:/lib1/real.jpg','image','2026-02-01T00:00:00.000000Z','2026-02-01T00:00:00.000000Z','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	c, w := listMediaTestContext("/api/v1/media?file_type=image&sort=taken_desc&limit=2", 2)
	h.ListMedia(c)
	if ids := responseMediaIDs(t, w); fmt.Sprint(ids) != "[61 62]" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestListMediaCancellationAfterFirstBatchReturnsNoRows(t *testing.T) {
	h := setupAccessTestDB(t)
	for i := 1; i <= 150; i++ {
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(?,1,?,?,'image',?,?,'{}')`, 2000+i, fmt.Sprintf("cancel-%d", i), fmt.Sprintf("E:/lib1/miss/%d.jpg", i), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60))
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	spec := mediaListSpec{LibraryID: ptrInt64(1), FolderScope: map[int64][]string{1: {"E:/lib1/hit"}}, Sort: mediaSortIDDesc, Limit: 2, BatchSize: 100}
	items, stats, err := h.listMediaRowsObserved(ctx, spec, func(got mediaListStats) {
		if got.Batches == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || items != nil || stats.Batches != 1 {
		t.Fatalf("items=%v stats=%+v err=%v", items, stats, err)
	}
}

func TestListMediaStatsCountActualBatchesCandidatesAndRejected(t *testing.T) {
	h := setupAccessTestDB(t)
	for i := 1; i <= 105; i++ {
		path := fmt.Sprintf("E:/lib1/miss/%d.jpg", i)
		if i == 1 {
			path = "E:/lib1/hit/one.jpg"
		}
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,created_at_sort) VALUES(?,1,?,?,?)`, 3000+i, fmt.Sprintf("stat-%d", i), path, fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60))
		if err != nil {
			t.Fatal(err)
		}
	}
	spec := mediaListSpec{LibraryID: ptrInt64(1), FolderScope: map[int64][]string{1: {"E:/lib1/hit"}}, Sort: mediaSortIDDesc, Limit: 2, BatchSize: 100}
	items, stats, err := h.listMediaRows(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || stats.Batches < 2 || stats.Candidates < 105 || stats.Rejected != stats.Candidates-stats.Returned {
		t.Fatalf("items=%d stats=%+v", len(items), stats)
	}
}

func TestListMediaCancellationAfterFirstBatchReturnsNonSuccessHTTP(t *testing.T) {
	h := setupAccessTestDB(t)
	for i := 1; i <= 120; i++ {
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,created_at_sort) VALUES(?,1,?,?,?)`, 4000+i, fmt.Sprintf("http-cancel-%d", i), fmt.Sprintf("E:/lib1/miss/%d.jpg", i), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.App.DB.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,'E:/lib1/hit')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?limit=2", nil).WithContext(ctx)
	setUserCtx(c, 1, "user", "normal")
	h.listMediaObserved(c, func(stats mediaListStats) {
		if stats.Batches == 1 {
			cancel()
		}
	})
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("partial success status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildMediaQueryTakenCursorUsesFallbackExpressionAndBoundArgs(t *testing.T) {
	spec := mediaListSpec{LibraryID: ptrInt64(7), FileType: "image", Sort: mediaSortTakenDesc, Limit: 10, BatchSize: 100, UserID: 3}
	cursor := &mediaCursor{SortKey: ptrString("2026-01-02T03:04:05.000000Z"), ID: 44}
	q, err := buildMediaQuery(spec, cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	key := "COALESCE(m.photo_taken_at,m.created_at_sort)"
	if !strings.Contains(q.SQL, key+" < ?") || !strings.Contains(q.SQL, key+" = ? AND m.id < ?") || strings.Contains(q.SQL, *cursor.SortKey) {
		t.Fatalf("SQL=%s", q.SQL)
	}
	got := q.Args[len(q.Args)-4:]
	want := []any{*cursor.SortKey, *cursor.SortKey, int64(44), 10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cursor args=%#v want %#v", got, want)
	}
}

func TestListMediaCustomTagOnlyLowHitScansToExhaustion(t *testing.T) {
	h := setupAccessTestDB(t)
	const firstID = 5000
	want := []int64{5230, 5115, 5001}
	for i := 1; i <= 230; i++ {
		tags := `["other"]`
		id := int64(firstID + i)
		if id == want[0] || id == want[1] || id == want[2] {
			tags = `["rare"]`
		}
		meta := fmt.Sprintf(`{"photo":{"tags":%s}}`, tags)
		_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(?,1,?,?,'image',?,?,?)`, id, fmt.Sprintf("tag-only-%d", i), fmt.Sprintf("E:/lib1/photo/%d.jpg", i), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60), meta)
		if err != nil {
			t.Fatal(err)
		}
	}
	spec := mediaListSpec{LibraryID: ptrInt64(1), FileType: "image", PhotoTag: "custom:rare", Sort: mediaSortIDDesc, Limit: 4, BatchSize: 100}
	items, stats, err := h.listMediaRows(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids=%v want=%v stats=%+v", ids, want, stats)
	}
	if stats.Batches <= 1 || stats.Candidates != 230 || stats.Returned != 3 || stats.Rejected != 227 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestBuildMediaQueryNullableCursorPartitions(t *testing.T) {
	keyValue := "2026-01-01T00:00:00.000000Z"
	for _, sortName := range []mediaSort{mediaSortCreatedDesc, mediaSortTakenDesc} {
		spec := mediaListSpec{Sort: sortName, Limit: 10, BatchSize: 100}
		nonnull, err := buildMediaQuery(spec, &mediaCursor{SortKey: &keyValue, ID: 9}, 10)
		if err != nil {
			t.Fatal(err)
		}
		key, _, _ := sortSQL(sortName)
		if !strings.Contains(nonnull.SQL, key+" < ?") || !strings.Contains(nonnull.SQL, key+" = ? AND m.id < ?") || !strings.Contains(nonnull.SQL, key+" IS NULL") {
			t.Fatalf("%s nonnull SQL=%s", sortName, nonnull.SQL)
		}
		want := []any{keyValue, keyValue, int64(9), 10}
		if fmt.Sprint(nonnull.Args[len(nonnull.Args)-4:]) != fmt.Sprint(want) {
			t.Fatalf("%s args=%#v", sortName, nonnull.Args)
		}
		nullCursor, err := buildMediaQuery(spec, &mediaCursor{SortKey: nil, ID: 8}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(nullCursor.SQL, key+" IS NULL AND m.id < ?") {
			t.Fatalf("%s null SQL=%s", sortName, nullCursor.SQL)
		}
		if got := nullCursor.Args[len(nullCursor.Args)-2:]; fmt.Sprint(got) != fmt.Sprint([]any{int64(8), 10}) {
			t.Fatalf("%s null args=%#v", sortName, got)
		}
	}
}

func TestListMediaNullableSortKeysCrossBatchesWithTagFilter(t *testing.T) {
	for _, sortName := range []mediaSort{mediaSortCreatedDesc, mediaSortTakenDesc} {
		t.Run(string(sortName), func(t *testing.T) {
			h := setupAccessTestDB(t)
			want := []int64{7105, 7001}
			for i := 1; i <= 130; i++ {
				id := int64(7000 + i)
				created, taken := any(nil), any(nil)
				if i > 30 {
					created = fmt.Sprintf("2026-01-01T00:%02d:%02d.000000Z", (i/60)%60, i%60)
					if sortName == mediaSortTakenDesc {
						taken = created
					}
				}
				tags := `["other"]`
				if id == want[0] || id == want[1] {
					tags = `["rare"]`
				}
				meta := fmt.Sprintf(`{"photo":{"tags":%s}}`, tags)
				_, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(?,1,?,?,'image',?,?,?)`, id, fmt.Sprintf("nullable-%s-%d", sortName, i), fmt.Sprintf("E:/lib1/%s/%d.jpg", sortName, i), created, taken, meta)
				if err != nil {
					t.Fatal(err)
				}
			}
			spec := mediaListSpec{LibraryID: ptrInt64(1), FileType: "image", PhotoTag: "custom:rare", Sort: sortName, Limit: 3, BatchSize: 100}
			items, stats, err := h.listMediaRows(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, len(items))
			for i := range items {
				ids[i] = items[i].ID
			}
			if fmt.Sprint(ids) != fmt.Sprint(want) {
				t.Fatalf("ids=%v want=%v stats=%+v", ids, want, stats)
			}
			if stats.Batches <= 1 || stats.Candidates != 130 || stats.Returned != 2 || stats.Rejected != 128 {
				t.Fatalf("stats=%+v", stats)
			}
		})
	}
}

func TestMediaQueryNullableKeysetPlansKeepSortIndexes(t *testing.T) {
	h := setupAccessTestDB(t)
	key := "2026-01-01T00:00:00.000000Z"
	cases := []struct {
		name   string
		sort   mediaSort
		cursor *mediaCursor
		index  string
	}{
		{"created-nonnull", mediaSortCreatedDesc, &mediaCursor{SortKey: &key, ID: 10}, "idx_media_library_type_created_id"},
		{"created-null", mediaSortCreatedDesc, &mediaCursor{SortKey: nil, ID: 10}, "idx_media_library_type_created_id"},
		{"taken-nonnull", mediaSortTakenDesc, &mediaCursor{SortKey: &key, ID: 10}, "idx_media_library_type_photo_timeline_id"},
		{"taken-null", mediaSortTakenDesc, &mediaCursor{SortKey: nil, ID: 10}, "idx_media_library_type_photo_timeline_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := buildMediaQuery(mediaListSpec{LibraryID: ptrInt64(1), FileType: "image", Sort: tc.sort, Limit: 10, BatchSize: 100}, tc.cursor, 10)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := h.App.DB.Query("EXPLAIN QUERY PLAN "+q.SQL, q.Args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(details, "\n")
			if !strings.Contains(joined, tc.index) || !strings.Contains(strings.ToUpper(joined), "MATERIALIZE CANDIDATES") {
				t.Fatalf("plan=%v want bounded candidates via %s", details, tc.index)
			}
		})
	}
}

func TestListMediaJoinsPreserveProgressAndMusicFields(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET file_type='audio', title='Track' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(71,1,'Album Artist','album artist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(72,1,'Album','album',71)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO music_track(id,album_id,media_id,title,artist_display) VALUES(73,72,10,'Track','Track Artist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO play_progress(user_id,file_id,completed,update_at) VALUES(1,'f-10',0,'2026-07-18 01:00:00'),(2,'f-10',1,'2026-07-18 02:00:00')`); err != nil {
		t.Fatal(err)
	}

	c, w := listMediaTestContext("/api/v1/media?library_id=1&limit=1", 1)
	h.ListMedia(c)
	var body struct {
		Items []struct {
			ID         int64  `json:"id"`
			LastPlayAt string `json:"last_play_at"`
			Completed  int64  `json:"completed"`
			AlbumID    int64  `json:"music_album_id"`
			AlbumTitle string `json:"music_album_title"`
			Artist     string `json:"music_artist"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(body.Items) != 1 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := body.Items[0]
	if got.ID != 10 || !strings.Contains(got.LastPlayAt, "2026-07-18 02:00:00") || got.Completed != 0 || got.AlbumID != 72 || got.AlbumTitle != "Album" || got.Artist != "Track Artist" {
		t.Fatalf("item=%+v body=%s", got, w.Body.String())
	}
}

func TestListMediaDirtyRelationsDoNotDuplicateMedia(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO play_progress(user_id,file_id,completed,update_at) VALUES(1,'f-10',0,'2026-07-18 01:00:00'),(1,'f-10',1,'2026-07-18 02:00:00'),(2,'f-10',0,'2026-07-18 03:00:00')`); err != nil {
		t.Fatal(err)
	}
	items, stats, err := h.listMediaRows(context.Background(), mediaListSpec{LibraryID: ptrInt64(1), Sort: mediaSortIDDesc, Limit: 10, BatchSize: 100, UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || stats.Batches != 1 || stats.Candidates != 1 || items[0].PlayCompleted.Int64 != 1 {
		t.Fatalf("items=%+v stats=%+v", items, stats)
	}
}

func TestListMediaDoesNotCallFilesystemAvailability(t *testing.T) {
	for _, path := range []string{"media.go", "media_query.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if path == "media.go" && fn.Name.Name != "ListMedia" && fn.Name.Name != "listMediaObserved" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if name := callName(call.Fun); name == "PlaintextSourceAvailable" || name == "Stat" {
					t.Errorf("%s.%s contains forbidden ListMedia filesystem availability call %s", path, fn.Name.Name, name)
				}
				return true
			})
		}
	}
}

func TestListMediaAPIClientHasZeroCompletedProgress(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO play_progress(user_id,file_id,completed,update_at) VALUES(1,'f-10',1,'2026-07-18 01:00:00')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?library_id=1&limit=1", nil)
	setUserCtx(c, 0, "api_client", "machine")
	h.ListMedia(c)
	var body struct {
		Items []struct {
			Completed int64 `json:"completed"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(body.Items) != 1 || body.Items[0].Completed != 0 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildMediaQueryUsesBoundedPreaggregatedJoins(t *testing.T) {
	q, err := buildMediaQuery(mediaListSpec{Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100, UserID: 7}, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GROUP BY pp.file_id", "LEFT JOIN media_encrypted_assets mea", "LEFT JOIN music_album", "LEFT JOIN music_artist"} {
		if !strings.Contains(q.SQL, want) {
			t.Errorf("missing %q in %s", want, q.SQL)
		}
	}
	for _, forbidden := range []string{"(SELECT MAX(pp.update_at)", "(SELECT pp.completed", "(SELECT mt.album_id", "(SELECT COALESCE(NULLIF(TRIM(a.title)"} {
		if strings.Contains(q.SQL, forbidden) {
			t.Errorf("correlated row query %q remains in %s", forbidden, q.SQL)
		}
	}
}

func TestListMediaOptimizationAssetRecordedMatrix(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`DELETE FROM media WHERE id IN (10,20); UPDATE library SET encrypted_assets_cleanup_plaintext=0 WHERE id=1; UPDATE library SET encrypted_assets_cleanup_plaintext=1 WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type) VALUES
		(101,1,'plain-video','E:/media/plain.mp4','video'),
		(102,1,'enc-plain','E:/vault/with-plain.enc','video'),
		(103,1,'enc-only','E:/vault/only.enc','video'),
		(104,1,'audio','E:/media/audio.mp3','audio'),
		(105,2,'cleanup-enc','E:/vault/cleanup.enc','video')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES
		(102,'E:/vault/with-plain.enc','aa','bb','E:/media/plain-source.mp4','encrypted'),
		(103,'E:/vault/only.enc','aa','bb','','encrypted'),
		(105,'E:/vault/cleanup.enc','aa','bb','E:/media/cleanup-source.mp4','encrypted')`); err != nil {
		t.Fatal(err)
	}

	c, w := listMediaTestContext("/api/v1/media?limit=10", 2)
	h.ListMedia(c)
	var body struct {
		Items []struct {
			ID       int64 `json:"id"`
			Recorded bool  `json:"optimization_asset_recorded"`
			Alias    bool  `json:"optimization_available"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(body.Items) != 5 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	want := map[int64]bool{101: true, 102: true, 103: false, 104: false, 105: false}
	for _, item := range body.Items {
		if item.Recorded != want[item.ID] || item.Alias != want[item.ID] {
			t.Errorf("id=%d recorded/alias=%v/%v want %v", item.ID, item.Recorded, item.Alias, want[item.ID])
		}
	}
}

func TestGetMediaReturnsRecordedAliasAndRuntimeAvailability(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET file_type='video',file_path='E:/missing/ordinary.mp4' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 2, "admin", "admin")
	h.GetMedia(c)
	var body struct {
		Recorded        bool `json:"optimization_asset_recorded"`
		Alias           bool `json:"optimization_available"`
		SourceAvailable bool `json:"optimization_source_available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !body.Recorded || !body.Alias || body.SourceAvailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListMediaCurrentUserCompletedIsMonotonicAcrossDirtyRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows string
	}{
		{
			name: "completed first mixed timestamp formats",
			rows: `(1,'f-10',1,'2026-07-18T01:00:00Z'),(1,'f-10',0,'2026-07-18 03:00:00'),(2,'f-10',1,'2026-07-18 04:00:00')`,
		},
		{
			name: "completed last equal timestamp",
			rows: `(1,'f-10',0,'2026-07-18 03:00:00'),(2,'f-10',1,'2026-07-18 04:00:00'),(1,'f-10',1,'2026-07-18 03:00:00')`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setupAccessTestDB(t)
			if _, err := h.App.DB.Exec(`INSERT INTO play_progress(user_id,file_id,completed,update_at) VALUES ` + tc.rows); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?library_id=1&limit=1", nil)
			setUserCtx(c, 1, "user", "normal")
			h.ListMedia(c)
			var payload struct {
				Items []struct {
					ID        int64 `json:"id"`
					Completed int64 `json:"completed"`
				} `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || len(payload.Items) != 1 || payload.Items[0].ID != 10 || payload.Items[0].Completed != 1 {
				t.Fatalf("current-user MAX completion must dominate and other user must not leak: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListMediaOtherUserCompletionDoesNotLeak(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO play_progress(user_id,file_id,completed,update_at) VALUES
		(2,'f-10',1,'2026-07-18 04:00:00'),(1,'f-10',0,'2026-07-18 03:00:00')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?library_id=1&limit=1", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListMedia(c)
	var payload struct {
		Items []struct {
			Completed int64 `json:"completed"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(payload.Items) != 1 || payload.Items[0].Completed != 0 {
		t.Fatalf("other-user completion leaked into ListMedia: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildMediaQueryLimitsCandidatesBeforeRelations(t *testing.T) {
	q, err := buildMediaQuery(mediaListSpec{LibraryID: ptrInt64(1), Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100, UserID: 7}, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(q.SQL)
	candidate := strings.Index(upper, "CANDIDATES AS")
	limit := strings.Index(upper, "LIMIT ?")
	progress := strings.Index(upper, "PMAX AS")
	if candidate < 0 || limit < 0 || progress < 0 || !(candidate < limit && limit < progress) {
		t.Fatalf("relations are not bounded after candidate LIMIT: %s", q.SQL)
	}
	for _, want := range []string{"JOIN candidates", "MAX(COALESCE(pp.completed,0))", "LEFT JOIN pu"} {
		if !strings.Contains(q.SQL, want) {
			t.Errorf("missing %q: %s", want, q.SQL)
		}
	}
}

func TestMediaQueryPlanMaterializesCandidatesAndBoundsProgress(t *testing.T) {
	h := setupAccessTestDB(t)
	q, err := buildMediaQuery(mediaListSpec{LibraryID: ptrInt64(1), Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100, UserID: 1}, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.App.DB.Query("EXPLAIN QUERY PLAN "+q.SQL, q.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(strings.ToUpper(joined), "MATERIALIZE CANDIDATES") || !strings.Contains(joined, "idx_progress_file_update") {
		t.Fatalf("plan=%s", joined)
	}
}

func TestMediaQueryCompletedUsesCandidateBoundedPreaggregation(t *testing.T) {
	h := setupAccessTestDB(t)
	q, err := buildMediaQuery(mediaListSpec{LibraryID: ptrInt64(1), Sort: mediaSortIDDesc, Limit: 24, BatchSize: 100, UserID: 1}, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(q.SQL)), " ")
	for _, want := range []string{
		"pu as ( select pp.file_id,max(coalesce(pp.completed,0)) as completed",
		"join candidates c on c.file_id=pp.file_id",
		"where pp.user_id=(select user_id from params) group by pp.file_id",
		"left join pu on pu.file_id=m.file_id",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("missing %q: %s", want, q.SQL)
		}
	}
	if strings.Contains(normalized, "select pp.completed from play_progress pp where") || strings.Contains(normalized, "pp.file_id=m.file_id") {
		t.Fatalf("outer-row completed correlation remains: %s", q.SQL)
	}
	rows, err := h.App.DB.Query("EXPLAIN QUERY PLAN "+q.SQL, q.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(strings.ToUpper(plan), "MATERIALIZE CANDIDATES") || !strings.Contains(plan, "idx_progress_user_file_completed") {
		t.Fatalf("plan=%s", plan)
	}
}
