package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestReadOnlyDSN(t *testing.T) {
	got := readOnlyDSN(`E:\data dir\media.db`)
	for _, want := range []string{"file:///E:/data%20dir/media.db", "mode=ro", "query_only%28ON%29", "busy_timeout%285000%29"} {
		if !strings.Contains(got, want) {
			t.Fatalf("readOnlyDSN() = %q, want substring %q", got, want)
		}
	}
}

func TestScenarioSQLNeverMutates(t *testing.T) {
	forbidden := map[string]struct{}{"INSERT": {}, "UPDATE": {}, "DELETE": {}, "ALTER": {}, "CREATE": {}}
	for _, s := range scenarios("before") {
		cleaned := stripSQLCommentsAndLiterals(s.SQL)
		for _, token := range sqlTokens(cleaned) {
			if _, bad := forbidden[strings.ToUpper(token)]; bad {
				t.Fatalf("scenario %q contains mutating SQL token %q after removing comments/literals: %s", s.Name, token, cleaned)
			}
		}
	}
}

func TestMutationDetectorIgnoresExplainTextCommentsAndLiterals(t *testing.T) {
	sql := `-- EXPLAIN says UPDATE here
SELECT 'DELETE FROM media', "CREATE index", value /* ALTER TABLE x */ FROM pragma_compile_options`
	cleaned := stripSQLCommentsAndLiterals(sql)
	for _, token := range sqlTokens(cleaned) {
		switch strings.ToUpper(token) {
		case "INSERT", "UPDATE", "DELETE", "ALTER", "CREATE":
			t.Fatalf("false positive token %q in %q", token, cleaned)
		}
	}
}

func TestOutputPathRejectsDatabaseAliasesWithoutChangingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "media.db")
	if err := os.WriteFile(dbPath, []byte("immutable database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _, err := fileState(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	symlink := filepath.Join(dir, "report-symlink.json")
	if err := os.Symlink(dbPath, symlink); err != nil {
		t.Logf("symlink unavailable on this Windows configuration: %v", err)
		symlink = ""
	}
	hardlink := filepath.Join(dir, "report-hardlink.json")
	if err := os.Link(dbPath, hardlink); err != nil {
		t.Fatalf("create hardlink: %v", err)
	}
	cases := []struct{ name, out string }{{"same absolute", dbPath}, {"same relative", "media.db"}, {"hardlink", hardlink}}
	if symlink != "" {
		cases = append(cases, struct{ name, out string }{"symlink", symlink})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveOutputPath(dbPath, tc.out); err == nil {
				t.Fatalf("resolveOutputPath(%q) unexpectedly succeeded", tc.out)
			}
		})
	}
	after, _, err := fileState(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("database hash changed: before=%s after=%s", before, after)
	}
}

func TestAtomicWriteFilePreservesExistingTargetOnFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := atomicWriteFile(target, func(w io.Writer) error {
		if _, err := io.WriteString(w, "partial"); err != nil {
			return err
		}
		return errors.New("render failed")
	})
	if err == nil {
		t.Fatal("atomicWriteFile unexpectedly succeeded")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target = %q, want old content", got)
	}
}

func TestScenariosCoverTaskOneReadPaths(t *testing.T) {
	names := map[string]bool{}
	for _, s := range scenarios("before") {
		names[s.Name] = true
	}
	for _, want := range []string{"photo_builtin_tag_candidate_stage", "photo_custom_tag_candidate_stage", "browse_movie_media", "browse_photo_media", "browse_document_media", "music_album_detail", "tv_series_detail", "list_media_folder_candidate_stage"} {
		if !names[want] {
			t.Errorf("missing scenario %q", want)
		}
	}
}

func openScenarioSchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:scenario-schema-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE library(id INTEGER PRIMARY KEY, name TEXT, type TEXT, enabled INTEGER, encrypted_assets_cleanup_plaintext INTEGER);
CREATE TABLE media(id INTEGER PRIMARY KEY, library_id INTEGER, file_id TEXT, file_path TEXT, file_type TEXT, title TEXT, duration INTEGER, meta_json TEXT, created_at TEXT, created_at_sort TEXT, photo_taken_at TEXT, photo_place_id TEXT);
CREATE INDEX idx_media_library ON media(library_id);
CREATE INDEX idx_media_library_created_id ON media(library_id,created_at_sort DESC,id DESC);
CREATE INDEX idx_media_library_type_created_id ON media(library_id,file_type,created_at_sort DESC,id DESC);
CREATE INDEX idx_media_library_type_photo_taken_id ON media(library_id,file_type,photo_taken_at DESC,id DESC);
CREATE INDEX idx_media_library_type_photo_timeline_id ON media(library_id,file_type,COALESCE(photo_taken_at,created_at_sort) DESC,id DESC);
CREATE TABLE scan_task(id INTEGER PRIMARY KEY, library_id INTEGER, status TEXT, processed_count INTEGER, total_count INTEGER, added_count INTEGER, started_at TEXT);
CREATE INDEX idx_scan_task_library ON scan_task(library_id, id);
CREATE TABLE play_progress(id INTEGER PRIMARY KEY, user_id INTEGER, file_id TEXT, position INTEGER, completed INTEGER, update_at TEXT);
CREATE INDEX idx_play_progress_update ON play_progress(update_at);
CREATE INDEX idx_progress_file_update ON play_progress(file_id,update_at DESC);
CREATE INDEX idx_progress_user_file_update_completed ON play_progress(user_id,file_id,update_at DESC,completed ASC);
CREATE TABLE user(id INTEGER PRIMARY KEY, library_scope TEXT);
CREATE TABLE user_library_permission(user_id INTEGER, library_id INTEGER);
CREATE INDEX idx_user_library_permission ON user_library_permission(user_id, library_id);
CREATE TABLE photo_face(id INTEGER PRIMARY KEY, media_id INTEGER, person_id INTEGER);
CREATE INDEX idx_photo_face_media ON photo_face(media_id, person_id);
CREATE TABLE photo_classify_task(id INTEGER PRIMARY KEY, status TEXT);
CREATE TABLE photo_location_task(id INTEGER PRIMARY KEY, status TEXT);
CREATE TABLE photo_face_task(id INTEGER PRIMARY KEY, status TEXT);
CREATE TABLE music_album(id INTEGER PRIMARY KEY, library_id INTEGER, title TEXT, year INTEGER, genre TEXT, artwork_path TEXT);
CREATE TABLE music_track(id INTEGER PRIMARY KEY, media_id INTEGER, album_id INTEGER, artist_display TEXT);
CREATE INDEX idx_music_track_album ON music_track(album_id);
CREATE INDEX idx_music_track_media ON music_track(media_id);
CREATE TABLE media_encrypted_assets(media_id INTEGER PRIMARY KEY,status TEXT,plain_path TEXT);
CREATE TABLE series(id INTEGER PRIMARY KEY, library_id INTEGER, title TEXT, year INTEGER, folder_paths TEXT);
CREATE TABLE season(id INTEGER PRIMARY KEY, tv_id INTEGER, season_num INTEGER);
CREATE INDEX idx_season_tv ON season(tv_id);
CREATE TABLE episode(id INTEGER PRIMARY KEY, season_id INTEGER, episode_num INTEGER);
CREATE TABLE episode_media(id INTEGER PRIMARY KEY, episode_id INTEGER, media_id INTEGER);
CREATE INDEX idx_episode_media_episode ON episode_media(episode_id);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create current schema fixture: %v", err)
	}
	return db
}

func TestEveryScenarioExplainsAgainstCurrentSchema(t *testing.T) {
	db := openScenarioSchema(t)
	for _, phase := range []string{"before", "after"} {
		for _, s := range scenarios(phase) {
			t.Run(phase+"/"+s.Name, func(t *testing.T) {
				if _, err := explain(context.Background(), db, s); err != nil {
					t.Fatalf("EXPLAIN QUERY PLAN failed: %v\nSQL: %s", err, s.SQL)
				}
			})
		}
	}
}

func TestReadOnlyDSNEscapesWindowsURIPaths(t *testing.T) {
	tests := []struct{ name, path, wantPrefix string }{
		{"drive", `C:\a #?%\db.sqlite`, "file:///C:/a%20%23%3F%25/db.sqlite?"},
		{"unc", `\\server\share\db.sqlite`, "file:////server/share/db.sqlite?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readOnlyDSN(tt.path)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("readOnlyDSN(%q)=%q, want prefix %q", tt.path, got, tt.wantPrefix)
			}
			if !strings.Contains(got, "mode=ro") || !strings.Contains(got, "_pragma=query_only%28ON%29") || !strings.Contains(got, "_pragma=busy_timeout%285000%29") {
				t.Fatalf("missing intact query parameters: %q", got)
			}
		})
	}
}

func TestReadOnlyDSNOpensEscapedLocalPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "a #% db.sqlite")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ro, err := sql.Open("sqlite", readOnlyDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var n int
	if err := ro.QueryRow(`PRAGMA query_only`).Scan(&n); err != nil {
		t.Fatalf("open escaped read-only URI: %v; dsn=%s", err, readOnlyDSN(dbPath))
	}
}
func TestAtomicWriteFileReplacesExistingTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(target, func(w io.Writer) error { _, err := io.WriteString(w, "new"); return err }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("target=%q, want new", got)
	}
}

func TestRepresentativeScenariosDocumentKeyBoundaries(t *testing.T) {
	var history, libraries scenario
	for _, s := range scenarios("before") {
		if s.Name == "home_continue_watching" {
			history = s
		}
		if s.Name == "home_libraries_latest_scan" {
			libraries = s
		}
	}
	if len(history.Args) < 2 || history.Args[0] != int64(1) {
		t.Fatalf("history args=%v, want bound user id then limit", history.Args)
	}
	for _, want := range []string{"JOIN media", "JOIN library", "l.type", "p.user_id = ?"} {
		if !strings.Contains(history.SQL, want) {
			t.Errorf("history SQL missing %q", want)
		}
	}
	for _, want := range []string{"scan_task_id", "scan_processed_count", "scan_total_count", "scan_added_count", "scan_started_at"} {
		if !strings.Contains(libraries.SQL, want) {
			t.Errorf("libraries SQL missing %q", want)
		}
	}
}

func TestReadTransactionKeepsStableWALSnapshotAndDetectsExternalWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE item(id INTEGER PRIMARY KEY); INSERT INTO item VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	before, err := sidecarStates(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var first, second int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM item`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(`INSERT INTO item VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM item`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	after, err := sidecarStates(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("snapshot counts=%d/%d", first, second)
	}
	if !externalWritesDetected(true, before, after) {
		t.Fatalf("WAL write not detected: before=%+v after=%+v", before, after)
	}
}

func TestOutputPathRejectsDatabaseSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "media.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := resolveOutputPath(dbPath, dbPath+suffix); err == nil {
			t.Errorf("sidecar %s accepted", suffix)
		}
	}
}

func TestExternalWriteEvidenceIgnoresSHMOnlyChanges(t *testing.T) {
	before := map[string]sidecarStateJSON{"-wal": {Exists: true, Size: 10, Hash: "same"}, "-shm": {Exists: true, Size: 20, Hash: "old"}}
	after := map[string]sidecarStateJSON{"-wal": {Exists: true, Size: 10, Hash: "same"}, "-shm": {Exists: true, Size: 30, Hash: "new"}}
	if externalWritesDetected(true, before, after) {
		t.Fatal("SHM coordination-only change treated as external write")
	}
	if !coordinationChanged(before, after) {
		t.Fatal("SHM coordination change not reported")
	}
	if !externalWritesDetected(false, before, after) {
		t.Fatal("main database change not detected")
	}
}

func TestWriteReportThenReturnsExternalWritesSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	r := report{ExternalWritesDetected: true, CoordinationChanged: true}
	err := writeReportAndValidate(path, r)
	if !errors.Is(err, ErrExternalWrites) {
		t.Fatalf("err=%v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), `"external_writes_detected": true`) {
		t.Fatalf("report=%s", data)
	}
}

func scenarioByName(t *testing.T, phase, name string) scenario {
	t.Helper()
	for _, s := range scenarios(phase) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("phase %s missing scenario %s", phase, name)
	return scenario{}
}

func TestListMediaScenariosArePhaseAware(t *testing.T) {
	before := scenarioByName(t, "before", "list_media_task4_before")
	after := scenarioByName(t, "after", "list_media_task4_after")
	if before.SQL == after.SQL {
		t.Fatal("before and after ListMedia scenarios are identical")
	}
	if !strings.Contains(strings.ToUpper(before.SQL), "SELECT MAX(PP.UPDATE_AT)") || !strings.Contains(before.SQL, "music_track mt WHERE mt.media_id=m.id") {
		t.Fatalf("before SQL no longer represents legacy correlated shape: %s", before.SQL)
	}
	for _, want := range []string{"AS MATERIALIZED", "pu_latest_time AS", "MIN(COALESCE(pp.completed,0))", "JOIN pu_latest_time", "LEFT JOIN pu", "media_encrypted_assets", "encrypted_assets_cleanup_plaintext"} {
		if !strings.Contains(after.SQL, want) {
			t.Errorf("after SQL missing %q: %s", want, after.SQL)
		}
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(after.SQL)), " ")
	if strings.Contains(normalized, "select pp.completed from play_progress pp where") || strings.Contains(normalized, "pp.file_id=m.file_id") {
		t.Fatalf("after SQL retains completed correlation: %s", after.SQL)
	}
}

func TestAfterListMediaScenarioBoundsCompletedPreaggregation(t *testing.T) {
	db := openScenarioSchema(t)
	s := scenarioByName(t, "after", "list_media_task4_after")
	plan, err := explain(context.Background(), db, s)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "idx_progress_user_file_update_completed") {
		t.Fatalf("plan=%s", joined)
	}
	if !strings.Contains(strings.ToUpper(joined), "MATERIALIZE CANDIDATES") {
		t.Fatalf("plan=%s", joined)
	}
}

func TestExpectedPlansAvoidTempSortOnIndexedFastPaths(t *testing.T) {
	db := openScenarioSchema(t)
	for _, tc := range []struct{ name, index string }{
		{"list_media_created_desc", "idx_media_library_created_id"},
		{"photo_taken_desc", "idx_media_library_type_photo_timeline_id"},
	} {
		s := scenarioByName(t, "after", tc.name)
		plan, err := explain(context.Background(), db, s)
		if err != nil {
			t.Fatal(err)
		}
		assessment := validateScenarioPolicy(s, plan)
		if !assessment.Accepted {
			t.Fatalf("%s plan rejected: notes=%v plan=%v", tc.name, assessment.Notes, plan)
		}
		if !assessment.UsesExpectedCompositeIndex {
			t.Fatalf("%s did not use expected composite index %q: %v", tc.name, tc.index, plan)
		}
	}
}

func TestExpectedPlansUseBoundedSlowPath(t *testing.T) {
	for _, name := range []string{"list_media_folder_candidate_stage", "photo_builtin_tag_candidate_stage", "photo_custom_tag_candidate_stage"} {
		s := scenarioByName(t, "after", name)
		a := validateScenarioPolicy(s, nil)
		if !a.Accepted {
			t.Fatalf("%s policy rejected: %v; SQL=%s", name, a.Notes, s.SQL)
		}
		upper := strings.ToUpper(stripSQLCommentsAndLiterals(s.SQL))
		if !strings.Contains(upper, "LIMIT") || strings.Contains(upper, "OFFSET") {
			t.Fatalf("%s must use SQL LIMIT without OFFSET: %s", name, s.SQL)
		}
	}
}

func TestProductionFastPathScenariosMatchSortColumnsAndIndexes(t *testing.T) {
	created := scenarioByName(t, "after", "list_media_created_desc")
	if !strings.Contains(created.SQL, "m.library_id=") || !strings.Contains(created.SQL, "m.created_at_sort DESC, m.id DESC") {
		t.Fatalf("created scenario is not production library fast-path shape: %s", created.SQL)
	}
	taken := scenarioByName(t, "after", "photo_taken_desc")
	if !strings.Contains(taken.SQL, "m.library_id=") || !strings.Contains(taken.SQL, "m.file_type='image'") || !strings.Contains(taken.SQL, "COALESCE(m.photo_taken_at,m.created_at_sort) DESC, m.id DESC") {
		t.Fatalf("taken scenario is not completed-migration production shape: %s", taken.SQL)
	}
}

func TestCompareReportsRejectsUnsafeOrNonComparableEvidence(t *testing.T) {
	before, after := validComparisonReport("before"), validComparisonReport("after")
	if got, err := compareReports(before, after); err != nil || !got.Accepted {
		t.Fatalf("valid comparison=%+v err=%v", got, err)
	}
	cases := []struct {
		name   string
		mutate func(*report)
	}{
		{"fingerprint", func(r *report) { r.DatabaseFingerprint = "other" }},
		{"schema", func(r *report) { r.SchemaVersion++ }},
		{"user-version", func(r *report) { r.UserVersion++ }},
		{"runs", func(r *report) { r.Runs = 29 }},
		{"unchanged", func(r *report) { r.Unchanged = false }},
		{"external-write", func(r *report) { r.ExternalWritesDetected = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := after
			tc.mutate(&bad)
			if _, err := compareReports(before, bad); err == nil {
				t.Fatal("invalid evidence compared")
			}
		})
	}
}

func TestCompareReportsMarksPlanFailureAndInsufficientRunsUnaccepted(t *testing.T) {
	base := validComparisonReport("before")
	base.Cache = "cold"
	base.Runs = 4
	base.Scenarios[0].SampleCount = 4
	base.Scenarios[0].Runs = base.Scenarios[0].Runs[:4]
	base.Scenarios[0].DurationsNS = base.Scenarios[0].DurationsNS[:4]
	after := base
	after.Phase = "after"
	after.Scenarios = append([]scenarioResult(nil), base.Scenarios...)
	after.Scenarios[0].PlanAccepted = false
	got, err := compareReports(base, after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted || len(got.Notes) == 0 || got.Comparisons[0].Accepted {
		t.Fatalf("comparison should be diagnostic-only: %+v", got)
	}
}

func TestSQLScenarioObservationsAreExplicitAndHonest(t *testing.T) {
	db := openScenarioSchema(t)
	s := scenarioByName(t, "after", "list_media_id_desc")
	samples, err := timeScenario(context.Background(), db, s, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatal(len(samples))
	}
	for _, sample := range samples {
		if sample.MeasurementKind != "sql_scenario" || sample.ScenarioExecutions != 1 || sample.SQLStatements != 1 || sample.Batches != 1 || sample.Candidates != sample.Rows || sample.Rejects != 0 || sample.PayloadBytes < 0 {
			t.Fatalf("dishonest observation: %+v", sample)
		}
	}
}

func TestAtomicWriteFileNeverExposesMissingOrPartialTarget(t *testing.T) {
	type publishedReport struct {
		Generation int    `json:"generation"`
		Payload    string `json:"payload"`
	}
	const replacements = 100
	payload := strings.Repeat("x", 256)
	encode := func(generation int) []byte {
		b, err := json.Marshal(publishedReport{Generation: generation, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	target := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(target, encode(0), 0600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	startWriter := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, 1)
	var reads atomic.Int64
	go func() {
		defer close(done)
		signaled := false
		for {
			select {
			case <-stop:
				return
			default:
				b, err := readAtomicTarget(target)
				if err != nil {
					select {
					case errs <- fmt.Errorf("read target: %w", err):
					default:
					}
					return
				}
				var got publishedReport
				if err := json.Unmarshal(b, &got); err != nil || got.Generation < 0 || got.Generation > replacements || got.Payload != payload {
					select {
					case errs <- fmt.Errorf("invalid published report generation=%d err=%v", got.Generation, err):
					default:
					}
					return
				}
				reads.Add(1)
				if !signaled {
					close(ready)
					signaled = true
				}
			}
		}
	}()
	<-ready
	readyReads := reads.Load()
	close(startWriter)
	<-startWriter
	for i := 1; i <= replacements; i++ {
		value := encode(i)
		if err := atomicWriteFile(target, func(w io.Writer) error { runtime.Gosched(); _, err := w.Write(value); return err }); err != nil {
			close(stop)
			<-done
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	close(stop)
	<-done
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if readyReads < 1 || reads.Load() <= readyReads {
		t.Fatalf("reader did not overlap replacements: ready=%d final=%d", readyReads, reads.Load())
	}
}

func TestPhotoTakenScenarioUsesProductionTimelineExpression(t *testing.T) {
	s := scenarioByName(t, "after", "photo_taken_desc")
	want := "COALESCE(m.photo_taken_at,m.created_at_sort) DESC, m.id DESC"
	if !strings.Contains(s.SQL, want) {
		t.Fatalf("photo scenario missing production order %q: %s", want, s.SQL)
	}
	p := policyFor(s.Name)
	if !containsString(p.ExpectedIndexes, "idx_media_library_type_photo_timeline_id") {
		t.Fatalf("policy indexes=%v", p.ExpectedIndexes)
	}
}

func TestDatabaseFingerprintIncludesStartingWALIdentity(t *testing.T) {
	main := strings.Repeat("a", 64)
	a := databaseIdentity{MainSHA256: main, WALExists: true, WALSHA256: strings.Repeat("b", 64), WALSize: 10}
	b := a
	b.WALSHA256 = strings.Repeat("c", 64)
	if databaseFingerprint(a) == databaseFingerprint(b) {
		t.Fatal("different WAL contents produced same fingerprint")
	}
	before := validComparisonReport("before")
	after := validComparisonReport("after")
	before.DatabaseIdentity = a
	after.DatabaseIdentity = b
	before.DatabaseFingerprint = databaseFingerprint(a)
	after.DatabaseFingerprint = databaseFingerprint(b)
	if _, err := compareReports(before, after); err == nil {
		t.Fatal("reports with different starting WAL compared")
	}
}

func TestCompareReportsStrictlyValidatesScenarioEvidence(t *testing.T) {
	before, after := validComparisonReport("before"), validComparisonReport("after")
	if _, err := compareReports(before, after); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*report)
	}{
		{"kind", func(r *report) { r.MeasurementKind = "" }},
		{"scenario-samples", func(r *report) { r.Scenarios[0].SampleCount = 0 }},
		{"run-json-count", func(r *report) { r.Scenarios[0].Runs = nil }},
		{"negative-p50", func(r *report) { r.Scenarios[0].P50MS = -1 }},
		{"unordered", func(r *report) { r.Scenarios[0].P50MS = 3; r.Scenarios[0].P95MS = 2 }},
		{"executions", func(r *report) { r.Scenarios[0].ScenarioExecutions = 0 }},
		{"statements", func(r *report) { r.Scenarios[0].SQLStatements = 0 }},
		{"batches", func(r *report) { r.Scenarios[0].Batches = 0 }},
		{"candidates-less-rows", func(r *report) { r.Scenarios[0].Candidates = 0; r.Scenarios[0].Runs[0].Rows = 1 }},
		{"rejects", func(r *report) { r.Scenarios[0].Rejects = -1 }},
		{"payload", func(r *report) { r.Scenarios[0].PayloadBytes = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := before
			bad.Scenarios = append([]scenarioResult(nil), before.Scenarios...)
			bad.Scenarios[0].Runs = append([]runJSON(nil), before.Scenarios[0].Runs...)
			tc.mutate(&bad)
			if _, err := compareReports(bad, after); err == nil {
				t.Fatal("invalid report compared")
			}
		})
	}
}

func validComparisonReport(phase string) report {
	id := databaseIdentity{MainSHA256: strings.Repeat("a", 64), WALExists: false}
	runs := make([]runJSON, 30)
	durations := make([]int64, 30)
	for i := range runs {
		runs[i] = runJSON{DurationMS: 1, Rows: 1}
		durations[i] = int64(time.Millisecond)
	}
	return report{Phase: phase, Cache: "warm", Environment: "windows/amd64", Runs: 30, DatabaseIdentity: id, DatabaseFingerprint: databaseFingerprint(id), SchemaVersion: 1, UserVersion: 1, MeasurementKind: "sql_scenario", Unchanged: true, Scenarios: []scenarioResult{{Name: "x", ComparisonKey: "x", ComparisonType: "same-scenario", ProductionEquivalent: true, PlanAccepted: true, ScenarioExecutions: 1, SQLStatements: 1, Batches: 1, Candidates: 1, Rejects: 0, PayloadBytes: 1, SampleCount: 30, DurationsNS: durations, Runs: runs, P50MS: 1, P95MS: 1, MaxMS: 1}}}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestCompareReportsRejectsTamperedDistributionAndDuplicateKeys(t *testing.T) {
	before, after := validComparisonReport("before"), validComparisonReport("after")
	before.Scenarios[0].DurationsNS = make([]int64, before.Runs)
	after.Scenarios[0].DurationsNS = make([]int64, after.Runs)
	for i := range before.Scenarios[0].DurationsNS {
		before.Scenarios[0].DurationsNS[i] = int64(time.Millisecond)
		after.Scenarios[0].DurationsNS[i] = int64(time.Millisecond)
	}
	if _, err := compareReports(before, after); err != nil {
		t.Fatal(err)
	}
	tampered := before
	tampered.Scenarios = append([]scenarioResult(nil), before.Scenarios...)
	tampered.Scenarios[0].P95MS = 2
	if _, err := compareReports(tampered, after); err == nil {
		t.Fatal("tampered p95 compared")
	}
	for _, side := range []string{"before", "after"} {
		t.Run(side, func(t *testing.T) {
			b, a := before, after
			if side == "before" {
				b.Scenarios = append(b.Scenarios, b.Scenarios[0])
			} else {
				a.Scenarios = append(a.Scenarios, a.Scenarios[0])
			}
			if _, err := compareReports(b, a); err == nil {
				t.Fatal("duplicate comparison key accepted")
			}
		})
	}
}

func TestCompareReportsDoesNotPropagateEarlierScenarioFailure(t *testing.T) {
	before, after := validComparisonReport("before"), validComparisonReport("after")
	failedBefore, failedAfter := before.Scenarios[0], after.Scenarios[0]
	failedBefore.Name = "failed"
	failedAfter.Name = "failed"
	failedBefore.ComparisonKey = "failed"
	failedAfter.ComparisonKey = "failed"
	failedAfter.PlanAccepted = false
	passingBefore, passingAfter := before.Scenarios[0], after.Scenarios[0]
	passingBefore.Name = "passing"
	passingAfter.Name = "passing"
	passingBefore.ComparisonKey = "passing"
	passingAfter.ComparisonKey = "passing"
	before.Scenarios = []scenarioResult{failedBefore, passingBefore}
	after.Scenarios = []scenarioResult{failedAfter, passingAfter}
	got, err := compareReports(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted || got.Comparisons[0].Accepted || !got.Comparisons[1].Accepted || len(got.Comparisons[1].Notes) != 0 {
		t.Fatalf("failure propagated: %+v", got)
	}
}

func TestImplementationProxyTimingIsInconclusiveNotRegressionGate(t *testing.T) {
	before, after := validComparisonReport("before"), validComparisonReport("after")
	before.Scenarios[0].ComparisonType = "implementation-proxy"
	after.Scenarios[0].ComparisonType = "implementation-proxy"
	before.Scenarios[0].ProductionEquivalent = false
	after.Scenarios[0].ProductionEquivalent = false
	for i := range after.Scenarios[0].DurationsNS {
		after.Scenarios[0].DurationsNS[i] = 2 * int64(time.Millisecond)
		after.Scenarios[0].Runs[i].DurationMS = 2
	}
	after.Scenarios[0].P50MS = 2
	after.Scenarios[0].P95MS = 2
	after.Scenarios[0].MaxMS = 2
	got, err := compareReports(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted || !got.TimingInconclusive || !got.Comparisons[0].Accepted || len(got.Notes) == 0 {
		t.Fatalf("proxy timing treated as gate: %+v", got)
	}
}

func TestCollectDBStatsReportsSchemaInventory(t *testing.T) {
	db := openScenarioSchema(t)
	stats, err := collectDBStats(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DatabaseBytes < 0 || stats.TableCounts["media"] != 0 || !containsString(stats.Indexes, "idx_media_library_created_id") {
		t.Fatalf("stats=%+v", stats)
	}
}
