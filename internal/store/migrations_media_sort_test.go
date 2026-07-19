package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openLegacyMediaSortDB(t *testing.T, rows int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE media (id INTEGER PRIMARY KEY, library_id INTEGER, file_type TEXT, created_at TEXT, meta_json TEXT); CREATE TABLE play_progress(id INTEGER PRIMARY KEY,user_id INTEGER,file_id TEXT,completed INTEGER,update_at TEXT); CREATE TABLE photo_face(id INTEGER PRIMARY KEY,media_id INTEGER,person_id INTEGER); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT);`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= rows; i++ {
		typ, meta := "video", "{}"
		if i%2 == 0 {
			typ, meta = "image", fmt.Sprintf(`{"photo":{"taken_at":"2026-07-18T01:02:%02dZ","place_id":"place-%d"}}`, i%60, i)
		}
		if _, err = db.Exec(`INSERT INTO media(id,library_id,file_type,created_at,meta_json) VALUES(?,1,?,'2026-07-18 01:02:03',?)`, i, typ, meta); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestNormalizeMediaTimeMixedValuesAndLexicalOrder(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"2026-07-18 01:02:03", "2026-07-18T01:02:03.000000Z"},
		{"2026-07-18T01:02:03.123Z", "2026-07-18T01:02:03.123000Z"},
		{"2026-07-18T09:02:03+08:00", "2026-07-18T01:02:03.000000Z"},
		{"1721264523", "2024-07-18T01:02:03.000000Z"},
	}
	for _, tt := range tests {
		got, fallback := NormalizeMediaTime(tt.raw, 9)
		if fallback || got != tt.want {
			t.Errorf("NormalizeMediaTime(%q)=(%q,%v), want (%q,false)", tt.raw, got, fallback, tt.want)
		}
		if len(got) != len("2006-01-02T15:04:05.000000Z") {
			t.Errorf("non-fixed width %q", got)
		}
	}
	early, _ := NormalizeMediaTime("2026-07-18T00:00:00-01:00", 1)
	late, _ := NormalizeMediaTime("2026-07-18 02:00:00", 2)
	if !(early < late) {
		t.Fatalf("normalized lexical order unsafe: %q !< %q", early, late)
	}
}

func TestNormalizeMediaTimeInvalidFallbackStableAndIDOrdered(t *testing.T) {
	a1, fallback := NormalizeMediaTime("invalid", 10)
	a2, _ := NormalizeMediaTime("invalid", 10)
	b, _ := NormalizeMediaTime("invalid", 11)
	if !fallback || a1 != a2 {
		t.Fatalf("fallback unstable: %q %q", a1, a2)
	}
	if !(a1 < b) {
		t.Fatalf("fallback must preserve ascending media id order: id10=%q id11=%q", a1, b)
	}
}

func TestPhotoTimelineTimeAndPlaceFromJSON(t *testing.T) {
	got, failed := PhotoTimelineTime(`{"photo":{"taken_at":"2026-07-18T09:02:03+08:00","place_id":"  abc  "}}`, "2020-01-01T00:00:00.000000Z", 1)
	if failed || got != "2026-07-18T01:02:03.000000Z" {
		t.Fatalf("got (%q,%v)", got, failed)
	}
	fallback, failed := PhotoTimelineTime(`{"photo":{"taken_at":"bad"}}`, "2020-01-01T00:00:00.000000Z", 1)
	if !failed || fallback != "2020-01-01T00:00:00.000000Z" {
		t.Fatalf("fallback got (%q,%v)", fallback, failed)
	}
	if got := PhotoPlaceID(`{"photo":{"place_id":"  abc  "}}`); got != "abc" {
		t.Fatalf("place=%q", got)
	}
}

func TestMigrateMediaSortColumnsMixedPhotoAndIndexes(t *testing.T) {
	db := openLegacyMediaSortDB(t, 5)
	_, _ = db.Exec(`UPDATE media SET created_at='2026-07-18T01:02:03.123Z' WHERE id=2; UPDATE media SET created_at='2026-07-18T09:02:03+08:00' WHERE id=3; UPDATE media SET created_at='1721264523' WHERE id=4; UPDATE media SET created_at='invalid' WHERE id=5; UPDATE media SET meta_json='{"photo":{"taken_at":"invalid","place_id":"x"}}' WHERE id=2`)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"created_at_sort", "photo_taken_at", "photo_place_id"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media') WHERE name=?`, col).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column %s count=%d err=%v", col, n, err)
		}
	}
	var nulls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE created_at_sort IS NULL OR (file_type='image' AND photo_taken_at IS NULL)`).Scan(&nulls); err != nil || nulls != 0 {
		t.Fatalf("nulls=%d err=%v", nulls, err)
	}
	var created, taken, place string
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at,photo_place_id FROM media WHERE id=2`).Scan(&created, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if taken != created || place != "x" {
		t.Fatalf("photo fallback=(%q,%q,%q)", created, taken, place)
	}
	for _, idx := range mediaSortIndexNames {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s count=%d err=%v", idx, n, err)
		}
	}
}

func TestMigrateMediaSortColumnsResumeAndIdempotent(t *testing.T) {
	db := openLegacyMediaSortDB(t, 251)
	ctx, cancel := context.WithCancel(context.Background())
	migrateMediaSortAfterBatch = cancel
	err := MigrateMediaSortColumns(ctx, db)
	migrateMediaSortAfterBatch = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first migration err=%v", err)
	}
	var completed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE created_at_sort IS NOT NULL`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != mediaSortBatchSize {
		t.Fatalf("completed=%d want=%d", completed, mediaSortBatchSize)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT group_concat(id||':'||created_at_sort||':'||COALESCE(photo_taken_at,'')||':'||COALESCE(photo_place_id,''),'|') FROM media`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.QueryRow(`SELECT group_concat(id||':'||created_at_sort||':'||COALESCE(photo_taken_at,'')||':'||COALESCE(photo_place_id,''),'|') FROM media`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("second migration changed normalized rows")
	}
}

func TestUpdateMediaMetaAndPhotoTimeUsesTransactionExecutor(t *testing.T) {
	db := openLegacyMediaSortDB(t, 1)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"photo":{"taken_at":"2026-07-18T01:02:03Z","place_id":"p"}}`
	if err := UpdateMediaMetaAndPhotoTime(context.Background(), tx, 1, meta); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == meta {
		t.Fatal("helper escaped caller transaction")
	}
}

func TestMigrateMediaSortColumnsHonorsPreCancelledContext(t *testing.T) {
	db := openLegacyMediaSortDB(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(MigrateMediaSortColumns(ctx, db), context.Canceled) {
		t.Fatal("expected context cancellation")
	}
}

func TestNormalizeMediaTimeRejectsOutOfRangeUnix(t *testing.T) {
	got1, f1 := NormalizeMediaTime("999999999999999999999", 7)
	got2, f2 := NormalizeMediaTime("bad", 7)
	if !f1 || !f2 || got1 != got2 {
		t.Fatalf("out of range fallback mismatch %q %q", got1, got2)
	}
}

var _ = time.RFC3339

func TestMigrateMediaSortColumnsReconcilesPreexistingDriftOnce(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	if err := ensureMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`UPDATE media SET created_at_sort='wrong',photo_taken_at='wrong',photo_place_id='wrong' WHERE id=2`)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var created, taken, place string
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at,photo_place_id FROM media WHERE id=2`).Scan(&created, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if created != "2026-07-18T01:02:03.000000Z" || taken != "2026-07-18T01:02:02.000000Z" || place != "place-2" {
		t.Fatalf("drift not reconciled: %q %q %q", created, taken, place)
	}
	_, _ = db.Exec(`UPDATE media SET photo_place_id='writer-value' WHERE id=2`)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT photo_place_id FROM media WHERE id=2`).Scan(&place); err != nil {
		t.Fatal(err)
	}
	if place != "writer-value" {
		t.Fatalf("completed migration rescanned rows: %q", place)
	}
}

func TestMigrateMediaSortColumnsPersistsBatchProgress(t *testing.T) {
	db := openLegacyMediaSortDB(t, 501)
	if err := ensureMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE media SET created_at_sort='stale',photo_taken_at='stale',photo_place_id='stale'`)
	ctx, cancel := context.WithCancel(context.Background())
	migrateMediaSortAfterBatch = cancel
	err := MigrateMediaSortColumns(ctx, db)
	migrateMediaSortAfterBatch = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var lastID int64
	if err := db.QueryRow(`SELECT last_id FROM media_sort_migration_state WHERE version=1`).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	if lastID != mediaSortBatchSize {
		t.Fatalf("last_id=%d", lastID)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var complete int
	if err := db.QueryRow(`SELECT completed FROM media_sort_migration_state WHERE version=1`).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete != 1 {
		t.Fatalf("completed=%d", complete)
	}
}

func TestUpdateMediaMetaAndPhotoTimeClearsRemovedDerivedFields(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMediaMetaAndPhotoTime(context.Background(), db, 2, `{"document":{"title":"generic"}}`); err != nil {
		t.Fatal(err)
	}
	var created, taken string
	var place sql.NullString
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at,photo_place_id FROM media WHERE id=2`).Scan(&created, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if taken != created || place.Valid {
		t.Fatalf("generic writer drift: created=%q taken=%q place=%v", created, taken, place)
	}
}

func TestMigrateMediaSortColumnsCompletedMarkerRepairsNullInvariant(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE media SET created_at_sort=NULL,photo_taken_at=NULL WHERE id=2`)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE created_at_sort IS NULL OR (file_type='image' AND photo_taken_at IS NULL)`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("nulls=%d", nulls)
	}
}

func TestMigrateMediaSortColumnsFinalizationSeesConcurrentInsert(t *testing.T) {
	db := openLegacyMediaSortDB(t, 1)
	migrateMediaSortBeforeFinalize = func() {
		_, err := db.Exec(`INSERT INTO media(id,library_id,file_type,created_at,meta_json) VALUES(2,1,'image','2026-07-18 01:02:04','{}')`)
		if err != nil {
			t.Fatal(err)
		}
		migrateMediaSortBeforeFinalize = nil
	}
	defer func() { migrateMediaSortBeforeFinalize = nil }()
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var created, taken sql.NullString
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at FROM media WHERE id=2`).Scan(&created, &taken); err != nil {
		t.Fatal(err)
	}
	if !created.Valid || !taken.Valid {
		t.Fatalf("insert not reconciled: %v %v", created, taken)
	}
}

func TestMediaSortInsertValuesUsesPhotoMetadata(t *testing.T) {
	now := time.Date(2026, 7, 18, 2, 3, 4, 123456789, time.FixedZone("x", 8*3600))
	values := MediaSortInsertValues(now, `{"photo":{"taken_at":"2026-07-17T23:00:00-02:00","place_id":" home "}}`, true)
	if values.CreatedAt != "2026-07-17T18:03:04.123456Z" || values.PhotoTakenAt != "2026-07-18T01:00:00.000000Z" || values.PhotoPlaceID != "home" {
		t.Fatalf("%+v", values)
	}
}

func TestMediaSortInsertValuesFallsBackToCreatedForPhoto(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)
	values := MediaSortInsertValues(now, `{"photo":{"taken_at":"invalid"}}`, true)
	if values.PhotoTakenAt != values.CreatedAt {
		t.Fatalf("%+v", values)
	}
}

func TestMigrateMediaSortBatchLocksBeforeSelect(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	var dbPath string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	blocked := make(chan error, 1)
	migrateMediaSortAfterSelect = func() {
		migrateMediaSortAfterSelect = nil
		go func() { _, err := writer.Exec(`UPDATE media SET meta_json='third-party' WHERE id=2`); blocked <- err }()
		if err := <-blocked; err == nil {
			t.Fatal("writer was not blocked after migration batch SELECT")
		}
	}
	defer func() { migrateMediaSortAfterSelect = nil }()
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMediaMetaAndPhotoTime(context.Background(), db, 2, `{"photo":{"taken_at":"2026-07-19T01:00:00Z","place_id":"writer"}}`); err != nil {
		t.Fatal(err)
	}
	var place, taken string
	if err := db.QueryRow(`SELECT photo_place_id,photo_taken_at FROM media WHERE id=2`).Scan(&place, &taken); err != nil {
		t.Fatal(err)
	}
	if place != "writer" || taken != "2026-07-19T01:00:00.000000Z" {
		t.Fatalf("place=%q taken=%q", place, taken)
	}
}

func TestMigrateMediaSortDropsRedundantEncryptedPrimaryKeyIndex(t *testing.T) {
	db := openLegacyMediaSortDB(t, 1)
	if _, err := db.Exec(`CREATE INDEX idx_media_encrypted_media_status ON media_encrypted_assets(media_id,status)`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_media_encrypted_media_status'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("redundant index remains")
	}
}

func TestMigrateMediaSortCompletedInvariantHoldsWriteLock(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var dbPath string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	migrateMediaSortCompletedAfterLock = func() {
		migrateMediaSortCompletedAfterLock = nil
		result := make(chan error, 1)
		go func() {
			_, err := writer.Exec(`INSERT INTO media(id,library_id,file_type,created_at,meta_json) VALUES(3,1,'image','2026-07-18 02:00:00','{}')`)
			result <- err
		}()
		if err := <-result; err == nil {
			t.Fatal("legacy insert was not blocked during completed invariant")
		}
	}
	defer func() { migrateMediaSortCompletedAfterLock = nil }()
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func markMediaSortV1Completed(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := ensureMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS media_sort_migration_state (version INTEGER PRIMARY KEY, last_id INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0, completed_at TEXT);
		INSERT INTO media_sort_migration_state(version,last_id,completed,completed_at) VALUES(1,999999,1,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateMediaSortColumnsRunsTagNormalizationAfterCompletedV1(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	markMediaSortV1Completed(t, db)
	if _, err := db.Exec(`UPDATE media SET created_at_sort='2026-07-18T01:02:03.000000Z',photo_taken_at='2026-07-18T01:02:02.000000Z',meta_json='{"photo":{"tags":[" \u4fdd\u5b58 ","\u4e0b\u8f7d\u4fdd\u5b58"," custom ","custom"],"ai_tags":[" \u4fdd\u5b58 ","\u4fdd\u5b58"],"manual_tags":[" custom ","custom"]}}' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var root struct {
		Photo struct {
			Tags       []string `json:"tags"`
			AITags     []string `json:"ai_tags"`
			ManualTags []string `json:"manual_tags"`
		} `json:"photo"`
	}
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(root.Photo.Tags); got != "[\u4e0b\u8f7d\u4fdd\u5b58 custom]" {
		t.Fatalf("tags=%s", got)
	}
	if got := fmt.Sprint(root.Photo.AITags); got != "[\u4e0b\u8f7d\u4fdd\u5b58]" {
		t.Fatalf("ai_tags=%s", got)
	}
	if got := fmt.Sprint(root.Photo.ManualTags); got != "[custom]" {
		t.Fatalf("manual_tags=%s", got)
	}
	var v2Completed int
	if err := db.QueryRow(`SELECT completed FROM media_sort_migration_state WHERE version=2`).Scan(&v2Completed); err != nil || v2Completed != 1 {
		t.Fatalf("v2 completed=%d err=%v", v2Completed, err)
	}
}

func TestMigrateMediaSortTagNormalizationResumesWithoutRescanningCompletedRows(t *testing.T) {
	db := openLegacyMediaSortDB(t, mediaSortBatchSize+1)
	if err := ensureMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET meta_json='{"photo":{"tags":[" \\u4fdd\\u5b58 ","\\u4e0b\\u8f7d\\u4fdd\\u5b58"]}}' WHERE file_type='image'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	migrateMediaTagsAfterBatch = cancel
	err := MigrateMediaSortColumns(ctx, db)
	migrateMediaTagsAfterBatch = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first migration err=%v", err)
	}
	var lastID int64
	if err := db.QueryRow(`SELECT last_id FROM media_sort_migration_state WHERE version=2`).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	if lastID != mediaSortBatchSize {
		t.Fatalf("v2 last_id=%d", lastID)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET meta_json='{"photo":{"tags":["writer-value"]}}' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "writer-value") {
		t.Fatalf("completed v2 rescanned row: %s", raw)
	}
}

func TestTagMigrationPreservesMixedNonStringArraysExactly(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	markMediaSortV1Completed(t, db)
	const raw = `{"photo":{"tags":[" custom ",7,{"keep":true},null],"ai_tags":"not-array"},"keep":1}`
	if _, err := db.Exec(`UPDATE media SET created_at_sort='2026-01-01T00:00:00.000000Z',photo_taken_at='2026-01-01T00:00:00.000000Z',meta_json=? WHERE id=2`, raw); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=2`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != raw {
		t.Fatalf("mixed metadata changed: got=%s want=%s", got, raw)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var again string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=2`).Scan(&again)
	if again != got {
		t.Fatalf("not idempotent")
	}
}

func TestCompletedTagMigrationNormalizesNewHigherIDsOnly(t *testing.T) {
	db := openLegacyMediaSortDB(t, 2)
	markMediaSortV1Completed(t, db)
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_type,created_at,created_at_sort,photo_taken_at,meta_json) VALUES(3,1,'image','2026-01-01','2026-01-01T00:00:00.000000Z','2026-01-01T00:00:00.000000Z','{"photo":{"tags":[" custom ","custom"]}}')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMediaSortColumns(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var got string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=3`).Scan(&got)
	if !strings.Contains(got, `"tags":["custom"]`) {
		t.Fatalf("new row not normalized: %s", got)
	}
	var last int64
	_ = db.QueryRow(`SELECT last_id FROM media_sort_migration_state WHERE version=2`).Scan(&last)
	if last != 3 {
		t.Fatalf("last_id=%d", last)
	}
}

func TestMigrateMediaSortColumnsReconcilesIndexesAfterCompletedFastPath(t *testing.T) {
	db:=openLegacyMediaSortDB(t,4)
	if err:=MigrateMediaSortColumns(context.Background(),db);err!=nil{t.Fatal(err)}
	if _,err:=db.Exec(`DROP INDEX idx_media_library_type_photo_timeline_id`);err!=nil{t.Fatal(err)}
	var beforeRows,beforeLast,beforeCompleted int
	if err:=db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&beforeRows);err!=nil{t.Fatal(err)}
	if err:=db.QueryRow(`SELECT last_id,completed FROM media_sort_migration_state WHERE version=1`).Scan(&beforeLast,&beforeCompleted);err!=nil{t.Fatal(err)}
	if beforeCompleted!=1{t.Fatal("fixture migration not completed")}
	if err:=MigrateMediaSortColumns(context.Background(),db);err!=nil{t.Fatal(err)}
	for _,idx:=range mediaSortIndexNames{var n int;if err:=db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`,idx).Scan(&n);err!=nil||n!=1{t.Fatalf("index %s count=%d err=%v",idx,n,err)}}
	var afterRows,afterLast,afterCompleted int
	if err:=db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&afterRows);err!=nil{t.Fatal(err)}
	if err:=db.QueryRow(`SELECT last_id,completed FROM media_sort_migration_state WHERE version=1`).Scan(&afterLast,&afterCompleted);err!=nil{t.Fatal(err)}
	if afterRows!=beforeRows||afterLast!=beforeLast||afterCompleted!=1{t.Fatalf("completed state/data changed before=%d/%d/%d after=%d/%d/%d",beforeRows,beforeLast,beforeCompleted,afterRows,afterLast,afterCompleted)}
}
