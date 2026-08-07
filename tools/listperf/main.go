package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"knox-media/internal/atomicfile"

	_ "modernc.org/sqlite"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type databaseIdentity struct {
	MainSHA256 string `json:"main_sha256"`
	WALExists  bool   `json:"wal_exists"`
	WALSHA256  string `json:"wal_sha256,omitempty"`
	WALSize    int64  `json:"wal_size"`
}

func databaseFingerprint(identity databaseIdentity) string {
	b, _ := json.Marshal(identity)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func identityFromState(mainSHA string, sidecars map[string]sidecarStateJSON) databaseIdentity {
	wal := sidecars["-wal"]
	return databaseIdentity{MainSHA256: mainSHA, WALExists: wal.Exists, WALSHA256: wal.Hash, WALSize: wal.Size}
}

type sidecarStateJSON struct {
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
	MTime  string `json:"mtime"`
	Hash   string `json:"sha256"`
}

func sidecarStates(dbPath string) (map[string]sidecarStateJSON, error) {
	out := make(map[string]sidecarStateJSON, 2)
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		hash, mtime, err := fileState(path)
		if errors.Is(err, os.ErrNotExist) {
			out[suffix] = sidecarStateJSON{}
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		out[suffix] = sidecarStateJSON{Exists: true, Size: info.Size(), MTime: mtime.Format(time.RFC3339Nano), Hash: hash}
	}
	return out, nil
}

func externalWritesDetected(mainUnchanged bool, before, after map[string]sidecarStateJSON) bool {
	return !mainUnchanged || before["-wal"] != after["-wal"]
}

func coordinationChanged(before, after map[string]sidecarStateJSON) bool {
	return before["-shm"] != after["-shm"]
}

type planPolicy struct {
	ExpectedIndexes []string `json:"expected_indexes,omitempty"`
	AvoidTempOrder  bool     `json:"avoid_temp_order"`
	RequireLimit    bool     `json:"require_limit"`
	ForbidOffset    bool     `json:"forbid_offset"`
}

type scenario struct {
	Name string
	SQL  string
	Args []any
}

type sample struct {
	Duration           time.Duration
	Rows               int
	MeasurementKind    string
	ScenarioExecutions int
	SQLStatements      int
	Batches            int
	Candidates         int
	Rejects            int
	PayloadBytes       int64
}

type scenarioResult struct {
	Name                 string     `json:"name"`
	SQL                  string     `json:"sql"`
	Plan                 []string   `json:"plan"`
	ComparisonKey        string     `json:"comparison_key"`
	ComparisonType       string     `json:"comparison_type"`
	ProductionEquivalent bool       `json:"production_equivalent"`
	PlanPolicy           planPolicy `json:"plan_policy"`
	PlanAccepted         bool       `json:"plan_accepted"`
	PlanNotes            []string   `json:"plan_notes,omitempty"`
	ScenarioExecutions   int        `json:"scenario_executions"`
	SQLStatements        int        `json:"sql_statements"`
	Batches              int        `json:"batches"`
	Candidates           int        `json:"candidates"`
	Rejects              int        `json:"rejects"`
	PayloadBytes         int64      `json:"payload_bytes"`
	SampleCount          int        `json:"samples"`
	DurationsNS          []int64    `json:"durations_ns"`
	Samples              []sample   `json:"-"`
	Runs                 []runJSON  `json:"runs"`
	P50MS                float64    `json:"p50_ms"`
	P95MS                float64    `json:"p95_ms"`
	MaxMS                float64    `json:"max_ms"`
}

type runJSON struct {
	DurationMS float64 `json:"duration_ms"`
	Rows       int     `json:"rows"`
}

type dbStats struct {
	DatabaseBytes   int64             `json:"database_bytes"`
	TableCounts     map[string]int    `json:"table_counts"`
	Indexes         []string          `json:"indexes"`
	MigrationStates map[string]string `json:"migration_states"`
}

type report struct {
	GeneratedAt            string                      `json:"generated_at"`
	Environment            string                      `json:"environment"`
	Database               string                      `json:"database"`
	DatabaseIdentity       databaseIdentity            `json:"database_identity"`
	DatabaseFingerprint    string                      `json:"database_fingerprint"`
	SchemaVersion          int                         `json:"schema_version"`
	UserVersion            int                         `json:"user_version"`
	MeasurementKind        string                      `json:"measurement_kind"`
	Phase                  string                      `json:"phase"`
	Cache                  string                      `json:"cache"`
	CacheNote              string                      `json:"cache_note"`
	Runs                   int                         `json:"runs"`
	HashBefore             string                      `json:"hash_before"`
	HashAfter              string                      `json:"hash_after"`
	MTimeBefore            string                      `json:"mtime_before"`
	MTimeAfter             string                      `json:"mtime_after"`
	Unchanged              bool                        `json:"database_unchanged"`
	SidecarsBefore         map[string]sidecarStateJSON `json:"sidecars_before"`
	SidecarsAfter          map[string]sidecarStateJSON `json:"sidecars_after"`
	ExternalWritesDetected bool                        `json:"external_writes_detected"`
	CoordinationChanged    bool                        `json:"coordination_changed"`
	DBStats                dbStats                     `json:"db_stats"`
	Scenarios              []scenarioResult            `json:"scenarios"`
}

func readOnlyDSN(path string) string {
	slash := filepath.ToSlash(path)
	if len(slash) >= 3 && slash[1] == ':' && slash[2] == '/' {
		slash = "/" + slash
	}
	u := &url.URL{Scheme: "file", Path: slash}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	return u.String()
}

type planAssessment struct {
	Accepted                   bool     `json:"accepted"`
	UsesExpectedCompositeIndex bool     `json:"uses_expected_composite_index"`
	Notes                      []string `json:"notes,omitempty"`
}

func assessPlan(policy planPolicy, plan []string) planAssessment {
	a := planAssessment{Accepted: true, UsesExpectedCompositeIndex: len(policy.ExpectedIndexes) == 0}
	upperSQLPlan := strings.ToUpper(strings.Join(plan, "\n"))
	for _, index := range policy.ExpectedIndexes {
		if strings.Contains(upperSQLPlan, strings.ToUpper(index)) {
			a.UsesExpectedCompositeIndex = true
			break
		}
	}
	if !a.UsesExpectedCompositeIndex {
		a.Accepted = false
		a.Notes = append(a.Notes, "expected composite index not observed")
	}
	if policy.AvoidTempOrder && strings.Contains(upperSQLPlan, "USE TEMP B-TREE FOR ORDER BY") {
		a.Accepted = false
		a.Notes = append(a.Notes, "avoidable temporary ORDER BY sort observed")
	}
	return a
}

func policyFor(name string) planPolicy {
	switch name {
	case "list_media_created_desc":
		return planPolicy{ExpectedIndexes: []string{"idx_media_library_created_id", "idx_media_library_type_created_id"}, AvoidTempOrder: true, RequireLimit: true}
	case "photo_taken_desc":
		return planPolicy{ExpectedIndexes: []string{"idx_media_library_type_photo_timeline_id"}, AvoidTempOrder: true, RequireLimit: true}
	case "list_media_folder_candidate_stage", "photo_builtin_tag_candidate_stage", "photo_custom_tag_candidate_stage":
		return planPolicy{RequireLimit: true, ForbidOffset: true}
	default:
		return planPolicy{}
	}
}

func validateScenarioPolicy(s scenario, plan []string) planAssessment {
	policy := policyFor(s.Name)
	a := assessPlan(policy, plan)
	upper := strings.ToUpper(stripSQLCommentsAndLiterals(s.SQL))
	if policy.RequireLimit && !strings.Contains(upper, "LIMIT") {
		a.Accepted = false
		a.Notes = append(a.Notes, "SQL LIMIT required")
	}
	if policy.ForbidOffset && strings.Contains(upper, "OFFSET") {
		a.Accepted = false
		a.Notes = append(a.Notes, "OFFSET forbidden")
	}
	return a
}

func scenarios(phase string) []scenario {
	items := []scenario{
		{"home_libraries_latest_scan", `SELECT l.id, l.name, l.type, l.enabled,
            (SELECT COUNT(*) FROM media m WHERE m.library_id=l.id) AS media_count,
            (SELECT id FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_task_id,
            (SELECT COALESCE(status,'') FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_status,
            (SELECT COALESCE(processed_count,0) FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_processed_count,
            (SELECT COALESCE(total_count,0) FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_total_count,
            (SELECT COALESCE(added_count,0) FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_added_count,
            (SELECT COALESCE(started_at,'') FROM scan_task st WHERE st.library_id=l.id ORDER BY st.id DESC LIMIT 1) AS scan_started_at
            FROM library l ORDER BY l.id`, nil},
		{"home_continue_watching", `SELECT p.user_id, p.file_id, p.position, m.duration, p.completed, p.update_at, l.type
            FROM play_progress p INNER JOIN media m ON m.file_id=p.file_id
            LEFT JOIN library l ON l.id=m.library_id
            WHERE p.user_id = ? AND p.completed=0 ORDER BY p.update_at DESC LIMIT ?`, []any{int64(1), 24}},
		{"list_media_id_desc", `SELECT m.id, m.library_id, m.file_id, m.file_path, m.file_type, m.created_at
            FROM media m ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"list_media_created_desc", `SELECT m.id, m.library_id, m.file_id, m.file_path, m.file_type, m.created_at_sort
            FROM media m WHERE m.library_id=(SELECT id FROM library ORDER BY id LIMIT 1)
            ORDER BY m.created_at_sort DESC, m.id DESC LIMIT ?`, []any{24}},
		{"list_media_library_filter", `SELECT m.id, m.file_id, m.file_type, m.created_at FROM media m
            WHERE m.library_id=(SELECT id FROM library ORDER BY id LIMIT 1) ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"list_media_selected_library_permission", `SELECT m.id, m.library_id, m.file_id FROM media m
            WHERE m.library_id IN (SELECT library_id FROM user_library_permission WHERE user_id=(SELECT id FROM user WHERE library_scope='selected' ORDER BY id LIMIT 1))
            ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"list_media_folder_candidate_stage", `SELECT m.id, m.library_id, m.file_path FROM media m
            WHERE m.library_id IN (SELECT library_id FROM user_library_permission WHERE user_id=(SELECT id FROM user WHERE library_scope='selected' ORDER BY id LIMIT 1))
            ORDER BY m.id DESC LIMIT ?`, []any{500}},
		{"list_media_search", `SELECT m.id, m.library_id, m.title FROM media m
            WHERE lower(COALESCE(m.title,'')) LIKE ? ORDER BY m.id DESC LIMIT ?`, []any{"%a%", 24}},
		{"photo_taken_desc", `SELECT m.id, m.library_id, m.photo_taken_at
            FROM media m WHERE m.library_id=(SELECT id FROM library WHERE type='photo' ORDER BY id LIMIT 1) AND m.file_type='image'
            ORDER BY COALESCE(m.photo_taken_at,m.created_at_sort) DESC, m.id DESC LIMIT ?`, []any{24}},
		{"photo_builtin_tag_candidate_stage", `SELECT m.id, m.library_id, m.meta_json FROM media m
            WHERE m.file_type='image' ORDER BY m.id DESC LIMIT ?`, []any{500}},
		{"photo_custom_tag_candidate_stage", `SELECT m.id, m.library_id, m.meta_json FROM media m
            WHERE m.file_type='image' ORDER BY m.id DESC LIMIT ?`, []any{500}},
		{"browse_movie_media", `SELECT m.id, m.library_id, m.file_type FROM media m
            WHERE m.library_id=(SELECT id FROM library WHERE type IN ('movie','video','anime') ORDER BY id LIMIT 1)
            ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"browse_photo_media", `SELECT m.id, m.library_id, m.file_type FROM media m
            WHERE m.library_id=(SELECT id FROM library WHERE type='photo' ORDER BY id LIMIT 1) AND m.file_type='image'
            ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"browse_document_media", `SELECT m.id, m.library_id, m.file_type FROM media m
            WHERE m.library_id=(SELECT id FROM library WHERE type='document' ORDER BY id LIMIT 1)
            ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"photo_place_filter", `SELECT m.id, json_extract(m.meta_json,'$.photo.place_id') FROM media m
            WHERE m.file_type='image' AND json_extract(m.meta_json,'$.photo.place_id') IS NOT NULL ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"photo_person_filter", `SELECT m.id FROM media m WHERE m.file_type='image'
            AND EXISTS (SELECT 1 FROM photo_face pf WHERE pf.media_id=m.id) ORDER BY m.id DESC LIMIT ?`, []any{24}},
		{"photo_metadata_progress", `SELECT
            (SELECT COUNT(*) FROM photo_classify_task WHERE status IN ('pending','failed','running')),
            (SELECT COUNT(*) FROM photo_location_task WHERE status IN ('pending','failed','running')),
            (SELECT COUNT(*) FROM photo_face_task WHERE status IN ('pending','failed','running'))`, nil},
		{"music_albums", `SELECT a.id, a.library_id, a.title, a.year,
            (SELECT COUNT(*) FROM music_track mt WHERE mt.album_id=a.id) AS track_count
            FROM music_album a ORDER BY a.id DESC LIMIT ?`, []any{24}},
		{"music_album_detail", `SELECT a.id, a.library_id, a.title, a.year, a.genre, a.artwork_path,
            (SELECT COUNT(*) FROM music_track mt WHERE mt.album_id=a.id) AS track_count
            FROM music_album a WHERE a.id=(SELECT id FROM music_album ORDER BY id LIMIT 1)`, nil},
		{"music_tracks", `SELECT mt.id, mt.media_id, mt.album_id, mt.artist_display
            FROM music_track mt ORDER BY mt.id DESC LIMIT ?`, []any{24}},
		{"tv_series", `SELECT s.id, s.library_id, s.title, s.year,
            (SELECT COUNT(*) FROM season se WHERE se.tv_id=s.id) AS season_count
            FROM series s ORDER BY s.id DESC LIMIT ?`, []any{24}},
		{"tv_series_detail", `SELECT s.id, s.library_id, s.title, s.year, s.folder_paths,
            (SELECT COUNT(*) FROM season se WHERE se.tv_id=s.id) AS season_count
            FROM series s WHERE s.id=(SELECT id FROM series ORDER BY id LIMIT 1)`, nil},
		{"tv_episodes", `SELECT em.media_id, e.episode_num, se.season_num, se.tv_id
            FROM episode_media em JOIN episode e ON e.id=em.episode_id JOIN season se ON se.id=e.season_id
            ORDER BY em.id DESC LIMIT ?`, []any{24}},
		{"latest_scan_per_library", `SELECT st.library_id, st.id, st.status, st.processed_count, st.total_count, st.added_count, st.started_at
            FROM scan_task st JOIN (SELECT library_id, MAX(id) max_id FROM scan_task GROUP BY library_id) latest ON latest.max_id=st.id
            ORDER BY st.library_id`, nil},
	}
	if phase == "before" {
		items = append(items, scenario{"list_media_task4_before", `SELECT m.id,m.file_id,
 (SELECT MAX(pp.update_at) FROM play_progress pp WHERE pp.file_id=m.file_id) AS last_play_at,
 COALESCE((SELECT pp.completed FROM play_progress pp WHERE pp.user_id=? AND pp.file_id=m.file_id ORDER BY pp.update_at DESC LIMIT 1),0) AS completed,
 (SELECT mt.album_id FROM music_track mt WHERE mt.media_id=m.id LIMIT 1) AS album_id,
 EXISTS(SELECT 1 FROM media_encrypted_assets mea WHERE mea.media_id=m.id AND mea.status='encrypted') AS encrypted_asset
FROM media m ORDER BY m.id DESC LIMIT ?`, []any{int64(1), 24}})
	} else if phase == "after" {
		items = append(items, scenario{"list_media_task4_after", `WITH params AS (SELECT ? AS user_id),
candidates AS MATERIALIZED (SELECT m.* FROM media m ORDER BY m.id DESC LIMIT ?),
pmax AS (SELECT pp.file_id,MAX(pp.update_at) AS last_play_at FROM play_progress pp JOIN candidates c ON c.file_id=pp.file_id GROUP BY pp.file_id),
pu_latest_time AS (SELECT pp.file_id,MAX(pp.update_at) AS max_update FROM play_progress pp JOIN candidates c ON c.file_id=pp.file_id WHERE pp.user_id=(SELECT user_id FROM params) GROUP BY pp.file_id),
pu AS (SELECT pp.file_id,MIN(COALESCE(pp.completed,0)) AS completed FROM play_progress pp JOIN pu_latest_time latest ON latest.file_id=pp.file_id AND latest.max_update=pp.update_at WHERE pp.user_id=(SELECT user_id FROM params) GROUP BY pp.file_id),
mt_pick AS (SELECT mt.media_id,MIN(mt.id) AS track_id FROM music_track mt JOIN candidates c ON c.id=mt.media_id GROUP BY mt.media_id)
SELECT m.id,m.file_id,pmax.last_play_at,COALESCE(pu.completed,0) AS completed,
 mt.album_id,CASE WHEN mea.status='encrypted' OR lower(m.file_path) LIKE '%.enc' THEN 1 ELSE 0 END AS encrypted_asset,
 CASE WHEN lower(COALESCE(m.file_type,''))!='video' OR NULLIF(TRIM(m.file_path),'') IS NULL THEN 0
  WHEN lower(TRIM(m.file_path)) NOT LIKE '%.enc' THEN 1
  WHEN COALESCE(l.encrypted_assets_cleanup_plaintext,0)=0 AND mea.status='encrypted' AND NULLIF(TRIM(mea.plain_path),'') IS NOT NULL THEN 1 ELSE 0 END AS optimization_asset_recorded
FROM candidates m
LEFT JOIN pmax ON pmax.file_id=m.file_id
LEFT JOIN pu ON pu.file_id=m.file_id
LEFT JOIN mt_pick ON mt_pick.media_id=m.id
LEFT JOIN music_track mt ON mt.id=mt_pick.track_id
LEFT JOIN media_encrypted_assets mea ON mea.media_id=m.id
LEFT JOIN library l ON l.id=m.library_id
ORDER BY m.id DESC`, []any{int64(1), 24}})
	}
	return items
}

func explain(ctx context.Context, db queryer, s scenario) ([]string, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+s.SQL, s.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%d|%d|%s", id, parent, detail))
	}
	return out, rows.Err()
}

func timeScenario(ctx context.Context, db queryer, s scenario, runs int) ([]sample, error) {
	if runs < 1 {
		return nil, errors.New("runs must be at least 1")
	}
	out := make([]sample, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		rows, err := db.QueryContext(ctx, s.SQL, s.Args...)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		values := make([]any, len(columns))
		refs := make([]any, len(columns))
		for j := range values {
			refs[j] = &values[j]
		}
		count := 0
		var payloadBytes int64
		for rows.Next() {
			if err := rows.Scan(refs...); err != nil {
				rows.Close()
				return nil, err
			}
			count++
			for _, value := range values {
				payloadBytes += observedValueBytes(value)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		out = append(out, sample{Duration: time.Since(start), Rows: count, MeasurementKind: "sql_scenario", ScenarioExecutions: 1, SQLStatements: 1, Batches: 1, Candidates: count, Rejects: 0, PayloadBytes: payloadBytes})
	}
	return out, nil
}

func observedValueBytes(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case []byte:
		return int64(len(v))
	case string:
		return int64(len(v))
	default:
		return int64(len(fmt.Sprint(v)))
	}
}

func stripSQLCommentsAndLiterals(in string) string {
	var out strings.Builder
	for i := 0; i < len(in); {
		if i+1 < len(in) && in[i:i+2] == "--" {
			i += 2
			for i < len(in) && in[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
			continue
		}
		if i+1 < len(in) && in[i:i+2] == "/*" {
			i += 2
			for i+1 < len(in) && in[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(in) {
				i += 2
			}
			out.WriteByte(' ')
			continue
		}
		if in[i] == '\'' || in[i] == '"' {
			quote := in[i]
			i++
			for i < len(in) {
				if in[i] == quote {
					if i+1 < len(in) && in[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out.WriteByte(' ')
			continue
		}
		out.WriteByte(in[i])
		i++
	}
	return out.String()
}

func sqlTokens(in string) []string {
	return strings.FieldsFunc(in, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') })
}

func resolveOutputPath(dbPath, outputPath string) (string, error) {
	if strings.TrimSpace(outputPath) == "" {
		return "", nil
	}
	dbAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	outAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	dbAbs = filepath.Clean(dbAbs)
	outAbs = filepath.Clean(outAbs)
	if strings.EqualFold(dbAbs, outAbs) || strings.EqualFold(dbAbs+"-wal", outAbs) || strings.EqualFold(dbAbs+"-shm", outAbs) {
		return "", errors.New("-out must not refer to the database or its WAL/SHM sidecars")
	}
	dbInfo, err := os.Stat(dbAbs)
	if err != nil {
		return "", fmt.Errorf("stat database: %w", err)
	}
	outInfo, err := os.Stat(outAbs)
	if err == nil {
		if os.SameFile(dbInfo, outInfo) {
			return "", errors.New("-out must not refer to the database")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat output: %w", err)
	}
	return outAbs, nil
}

func atomicWriteFile(path string, write func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".listperf-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err = write(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return atomicfile.ReplaceFile(tmpName, path)
}

func fileState(path string) (string, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", time.Time{}, err
	}
	st, err := f.Stat()
	if err != nil {
		return "", time.Time{}, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.ModTime(), nil
}

func percentileNS(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	idx := int(float64(len(copyValues)-1)*p + 0.5)
	return copyValues[idx]
}

func percentile(samples []sample, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	ds := make([]time.Duration, len(samples))
	for i, s := range samples {
		ds[i] = s.Duration
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	idx := int(float64(len(ds)-1)*p + 0.5)
	return float64(ds[idx]) / float64(time.Millisecond)
}

type distribution struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
}

type comparison struct {
	Scenario          string       `json:"scenario"`
	Type              string       `json:"type"`
	Before            distribution `json:"before"`
	After             distribution `json:"after"`
	SQLStatementDelta int          `json:"sql_statement_delta"`
	CandidateDelta    int          `json:"candidate_delta"`
	PayloadBytesDelta int64        `json:"payload_bytes_delta"`
	P95DeltaPct       float64      `json:"p95_delta_percent"`
	Accepted          bool         `json:"accepted"`
	Notes             []string     `json:"notes,omitempty"`
}

type comparisonReport struct {
	GeneratedAt        string       `json:"generated_at"`
	Cache              string       `json:"cache"`
	Environment        string       `json:"environment"`
	Accepted           bool         `json:"accepted"`
	TimingInconclusive bool         `json:"timing_inconclusive"`
	Notes              []string     `json:"notes,omitempty"`
	Comparisons        []comparison `json:"comparisons"`
}

func validateReport(r report) error {
	if r.MeasurementKind != "sql_scenario" {
		return errors.New("measurement_kind must be sql_scenario")
	}
	if r.Runs <= 0 {
		return errors.New("report runs must be positive")
	}
	if r.DatabaseIdentity.MainSHA256 == "" || r.DatabaseFingerprint == "" || databaseFingerprint(r.DatabaseIdentity) != r.DatabaseFingerprint {
		return errors.New("database identity is missing or inconsistent")
	}
	if len(r.Scenarios) == 0 {
		return errors.New("report has no scenarios")
	}
	seen := make(map[string]struct{}, len(r.Scenarios))
	for _, s := range r.Scenarios {
		if s.ComparisonKey == "" {
			return fmt.Errorf("scenario %q has no comparison key", s.Name)
		}
		if _, exists := seen[s.ComparisonKey]; exists {
			return fmt.Errorf("duplicate comparison key %q", s.ComparisonKey)
		}
		seen[s.ComparisonKey] = struct{}{}
		if s.SampleCount != r.Runs || len(s.Runs) != r.Runs || len(s.DurationsNS) != r.Runs {
			return fmt.Errorf("scenario %q sample count does not match report runs", s.Name)
		}
		if s.P50MS < 0 || s.P95MS < 0 || s.MaxMS < 0 || s.P50MS > s.P95MS || s.P95MS > s.MaxMS {
			return fmt.Errorf("scenario %q has invalid timing distribution", s.Name)
		}
		for _, ns := range s.DurationsNS {
			if ns < 0 {
				return fmt.Errorf("scenario %q has negative raw duration", s.Name)
			}
		}
		if s.P50MS != float64(percentileNS(s.DurationsNS, .50))/float64(time.Millisecond) || s.P95MS != float64(percentileNS(s.DurationsNS, .95))/float64(time.Millisecond) || s.MaxMS != float64(percentileNS(s.DurationsNS, 1))/float64(time.Millisecond) {
			return fmt.Errorf("scenario %q distribution does not match raw durations", s.Name)
		}
		if s.ScenarioExecutions < 1 || s.SQLStatements < 1 || s.Batches < 1 || s.Rejects < 0 || s.PayloadBytes < 0 {
			return fmt.Errorf("scenario %q has invalid observations", s.Name)
		}
		maxRows := 0
		for _, run := range s.Runs {
			if run.DurationMS < 0 || run.Rows < 0 {
				return fmt.Errorf("scenario %q has invalid run", s.Name)
			}
			if run.Rows > maxRows {
				maxRows = run.Rows
			}
		}
		if s.Candidates < maxRows {
			return fmt.Errorf("scenario %q candidates are less than rows", s.Name)
		}
	}
	return nil
}

func compareReports(before, after report) (comparisonReport, error) {
	if err := validateReport(before); err != nil {
		return comparisonReport{}, fmt.Errorf("before report: %w", err)
	}
	if err := validateReport(after); err != nil {
		return comparisonReport{}, fmt.Errorf("after report: %w", err)
	}
	if before.Phase != "before" || after.Phase != "after" {
		return comparisonReport{}, errors.New("reports must be before then after")
	}
	if before.Cache != after.Cache {
		return comparisonReport{}, errors.New("cache labels differ")
	}
	if before.Environment == "" || before.Environment != after.Environment {
		return comparisonReport{}, errors.New("environments differ or are missing")
	}
	if before.DatabaseIdentity != after.DatabaseIdentity || before.DatabaseFingerprint != after.DatabaseFingerprint {
		return comparisonReport{}, errors.New("database fingerprints differ or are missing")
	}
	if before.SchemaVersion != after.SchemaVersion || before.UserVersion != after.UserVersion {
		return comparisonReport{}, errors.New("database schema versions differ")
	}
	if before.Runs <= 0 || before.Runs != after.Runs {
		return comparisonReport{}, errors.New("run counts differ or are invalid")
	}
	if !before.Unchanged || !after.Unchanged || before.ExternalWritesDetected || after.ExternalWritesDetected {
		return comparisonReport{}, errors.New("database safety evidence is invalid")
	}
	index := make(map[string]scenarioResult, len(after.Scenarios))
	for _, s := range after.Scenarios {
		index[s.ComparisonKey] = s
	}
	out := comparisonReport{GeneratedAt: time.Now().Format(time.RFC3339), Cache: before.Cache, Environment: before.Environment, Accepted: true}
	minimum := 30
	if before.Cache == "cold" {
		minimum = 5
	}
	if before.Runs < minimum {
		out.Accepted = false
		out.Notes = append(out.Notes, fmt.Sprintf("%s comparison has %d runs; acceptance requires at least %d", before.Cache, before.Runs, minimum))
	}
	for _, b := range before.Scenarios {
		if b.ComparisonKey == "" {
			return comparisonReport{}, fmt.Errorf("scenario %q has no comparison key", b.Name)
		}
		a, ok := index[b.ComparisonKey]
		if !ok || b.ComparisonType != a.ComparisonType {
			return comparisonReport{}, fmt.Errorf("scenario mismatch for %q", b.ComparisonKey)
		}
		c := comparison{Scenario: b.ComparisonKey, Type: b.ComparisonType, Before: distribution{b.SampleCount, b.P50MS, b.P95MS, b.MaxMS}, After: distribution{a.SampleCount, a.P50MS, a.P95MS, a.MaxMS}, SQLStatementDelta: a.SQLStatements - b.SQLStatements, CandidateDelta: a.Candidates - b.Candidates, PayloadBytesDelta: a.PayloadBytes - b.PayloadBytes, Accepted: true}
		if b.P95MS > 0 {
			c.P95DeltaPct = (a.P95MS - b.P95MS) / b.P95MS * 100
		}
		if !b.PlanAccepted || !a.PlanAccepted {
			c.Accepted = false
			c.Notes = append(c.Notes, "plan policy not accepted")
		}
		if b.ComparisonType == "implementation" && b.ProductionEquivalent && a.ProductionEquivalent && c.P95DeltaPct > 10 {
			c.Accepted = false
			c.Notes = append(c.Notes, "p95 regression exceeds 10% acceptance threshold")
		}
		if b.ComparisonType == "same-scenario" && (c.P95DeltaPct > 10 || c.P95DeltaPct < -10) {
			out.TimingInconclusive = true
			out.Notes = append(out.Notes, fmt.Sprintf("control drift in %s: %.3f%%", b.ComparisonKey, c.P95DeltaPct))
		}
		if !b.ProductionEquivalent || !a.ProductionEquivalent {
			out.TimingInconclusive = true
			out.Notes = append(out.Notes, fmt.Sprintf("production timing unavailable for %s", b.ComparisonKey))
		}
		if !c.Accepted {
			out.Accepted = false
		}
		out.Comparisons = append(out.Comparisons, c)
		delete(index, b.ComparisonKey)
	}
	if out.TimingInconclusive {
		out.Accepted = false
	}
	if len(index) != 0 {
		return comparisonReport{}, errors.New("after report has unmatched scenarios")
	}
	return out, nil
}

func writeComparisonAtomic(path string, c comparisonReport) error {
	return atomicWriteFile(path, func(w io.Writer) error { enc := json.NewEncoder(w); enc.SetIndent("", "  "); return enc.Encode(c) })
}

func loadReport(path string) (report, error) {
	f, err := os.Open(path)
	if err != nil {
		return report{}, err
	}
	defer f.Close()
	var r report
	err = json.NewDecoder(f).Decode(&r)
	return r, err
}

func collectDBStats(ctx context.Context, db queryer) (dbStats, error) {
	stats := dbStats{TableCounts: map[string]int{}, MigrationStates: map[string]string{}}
	for _, table := range []string{"library", "media", "play_progress", "scan_task"} {
		rows, err := db.QueryContext(ctx, "SELECT COUNT(*) FROM "+table)
		if err != nil {
			continue
		}
		if rows.Next() {
			var n int
			if err := rows.Scan(&n); err == nil {
				stats.TableCounts[table] = n
			}
		}
		rows.Close()
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='index' ORDER BY name")
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return stats, err
		}
		stats.Indexes = append(stats.Indexes, name)
	}
	stateRows, err := db.QueryContext(ctx, "SELECT version,last_id,completed FROM media_sort_migration_state ORDER BY version")
	if err == nil {
		defer stateRows.Close()
		for stateRows.Next() {
			var version, last, completed int
			if err := stateRows.Scan(&version, &last, &completed); err != nil {
				return stats, err
			}
			stats.MigrationStates[fmt.Sprint(version)] = fmt.Sprintf("last_id=%d completed=%d", last, completed)
		}
	}
	return stats, rows.Err()
}

func writeReport(w io.Writer, r report, markdown bool) error {
	if !markdown {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	fmt.Fprintf(bw, "# List performance %s baseline\n\n- Generated: `%s`\n- Database: `%s`\n- Cache label: `%s` - %s\n- Runs: `%d`\n- Database unchanged: `%t`\n- External writes detected: `%t`\n- Coordination changed: `%t`\n- SHA-256 before/after: `%s` / `%s`\n- mtime before/after: `%s` / `%s`\n\n", r.Phase, r.GeneratedAt, r.Database, r.Cache, r.CacheNote, r.Runs, r.Unchanged, r.ExternalWritesDetected, r.CoordinationChanged, r.HashBefore, r.HashAfter, r.MTimeBefore, r.MTimeAfter)
	for _, s := range r.Scenarios {
		fmt.Fprintf(bw, "## %s\n\n- rows: `%d`\n- p50/p95/max: `%.3f / %.3f / %.3f ms`\n- plan:\n", s.Name, s.Runs[len(s.Runs)-1].Rows, s.P50MS, s.P95MS, s.MaxMS)
		for _, p := range s.Plan {
			fmt.Fprintf(bw, "  - `%s`\n", strings.ReplaceAll(p, "`", "'"))
		}
		fmt.Fprintln(bw)
	}
	return nil
}

var ErrExternalWrites = errors.New("external database writes detected during benchmark")

func writeReportAndValidate(path string, r report) error {
	var err error
	if path == "" {
		err = writeReport(os.Stdout, r, false)
	} else {
		err = atomicWriteFile(path, func(w io.Writer) error { return writeReport(w, r, strings.EqualFold(filepath.Ext(path), ".md")) })
	}
	if err != nil {
		return err
	}
	if r.ExternalWritesDetected {
		return ErrExternalWrites
	}
	return nil
}

func run() error {
	dbPath := flag.String("db", "", "SQLite database path (required; opened read-only)")
	phase := flag.String("phase", "before", "measurement phase: before or after")
	cache := flag.String("cache", "warm", "cache label: cold or warm")
	runs := flag.Int("runs", 30, "timed runs per scenario")
	out := flag.String("out", "", "output path (.json or .md); stdout when empty")
	beforeReport := flag.String("before-report", "", "before report JSON to compare")
	afterReport := flag.String("after-report", "", "after report JSON to compare")
	compareOut := flag.String("compare", "", "atomic comparison JSON output path")
	flag.Parse()
	if *beforeReport != "" || *afterReport != "" || *compareOut != "" {
		if *beforeReport == "" || *afterReport == "" || *compareOut == "" {
			return errors.New("-before-report, -after-report, and -compare must be used together")
		}
		b, err := loadReport(*beforeReport)
		if err != nil {
			return fmt.Errorf("read before report: %w", err)
		}
		a, err := loadReport(*afterReport)
		if err != nil {
			return fmt.Errorf("read after report: %w", err)
		}
		c, err := compareReports(b, a)
		if err != nil {
			return err
		}
		return writeComparisonAtomic(*compareOut, c)
	}
	if strings.TrimSpace(*dbPath) == "" {
		return errors.New("-db is required")
	}
	if *phase != "before" && *phase != "after" {
		return errors.New("-phase must be before or after")
	}
	if *cache != "cold" && *cache != "warm" {
		return errors.New("-cache must be cold or warm")
	}
	if *runs < 1 {
		return errors.New("-runs must be at least 1")
	}
	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		return err
	}
	resolvedOut, err := resolveOutputPath(abs, *out)
	if err != nil {
		return err
	}
	hashBefore, mtimeBefore, err := fileState(abs)
	if err != nil {
		return fmt.Errorf("database before state: %w", err)
	}
	sidecarsBefore, err := sidecarStates(abs)
	if err != nil {
		return fmt.Errorf("database sidecars before state: %w", err)
	}
	db, err := sql.Open("sqlite", readOnlyDSN(abs))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("open read-only database: %w", err)
	}
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		db.Close()
		return fmt.Errorf("query_only verification failed: value=%d err=%v", queryOnly, err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.Close()
		return fmt.Errorf("begin stable read snapshot: %w", err)
	}
	defer tx.Rollback()
	// Establish the WAL snapshot before any scenario runs.
	var snapshotVersion, userVersion int
	if err := tx.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&snapshotVersion); err != nil {
		db.Close()
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		db.Close()
		return err
	}
	stats, err := collectDBStats(ctx, tx)
	if err != nil {
		db.Close()
		return fmt.Errorf("collect database diagnostics: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr == nil {
		stats.DatabaseBytes = info.Size()
	}
	r := report{DBStats: stats, Environment: runtime.GOOS + "/" + runtime.GOARCH, GeneratedAt: time.Now().Format(time.RFC3339), Database: abs, DatabaseIdentity: identityFromState(hashBefore, sidecarsBefore), DatabaseFingerprint: databaseFingerprint(identityFromState(hashBefore, sidecarsBefore)), SchemaVersion: snapshotVersion, UserVersion: userVersion, MeasurementKind: "sql_scenario", Phase: *phase, Cache: *cache, CacheNote: "cold means a separately launched process only; no OS cache eviction is claimed", Runs: *runs, HashBefore: hashBefore, MTimeBefore: mtimeBefore.Format(time.RFC3339Nano), SidecarsBefore: sidecarsBefore}
	for _, s := range scenarios(*phase) {
		plan, err := explain(ctx, tx, s)
		if err != nil {
			db.Close()
			return fmt.Errorf("explain %s: %w", s.Name, err)
		}
		samples, err := timeScenario(ctx, tx, s, *runs)
		if err != nil {
			db.Close()
			return fmt.Errorf("time %s: %w", s.Name, err)
		}
		key, comparisonType, productionEquivalent := s.Name, "same-scenario", true
		if strings.HasPrefix(s.Name, "list_media_task4_") {
			key, comparisonType, productionEquivalent = "list_media_task4", "implementation-proxy", false
		}
		assessment := validateScenarioPolicy(s, plan)
		sr := scenarioResult{SampleCount: len(samples), Name: s.Name, SQL: s.SQL, Plan: plan, ComparisonKey: key, ComparisonType: comparisonType, ProductionEquivalent: productionEquivalent, PlanPolicy: policyFor(s.Name), PlanAccepted: assessment.Accepted, PlanNotes: assessment.Notes, Samples: samples, P50MS: percentile(samples, .50), P95MS: percentile(samples, .95), MaxMS: percentile(samples, 1)}
		if len(samples) > 0 {
			sr.ScenarioExecutions = samples[0].ScenarioExecutions
			sr.SQLStatements = samples[0].SQLStatements
			sr.Batches = samples[0].Batches
			sr.Candidates = samples[0].Candidates
			sr.Rejects = samples[0].Rejects
			sr.PayloadBytes = samples[0].PayloadBytes
		}
		for _, x := range samples {
			sr.DurationsNS = append(sr.DurationsNS, int64(x.Duration))
			sr.Runs = append(sr.Runs, runJSON{float64(x.Duration) / float64(time.Millisecond), x.Rows})
		}
		r.Scenarios = append(r.Scenarios, sr)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	hashAfter, mtimeAfter, err := fileState(abs)
	if err != nil {
		return fmt.Errorf("database after state: %w", err)
	}
	r.HashAfter = hashAfter
	r.MTimeAfter = mtimeAfter.Format(time.RFC3339Nano)
	r.Unchanged = hashBefore == hashAfter && mtimeBefore.Equal(mtimeAfter)
	sidecarsAfter, err := sidecarStates(abs)
	if err != nil {
		return fmt.Errorf("database sidecars after state: %w", err)
	}
	r.SidecarsAfter = sidecarsAfter
	r.ExternalWritesDetected = externalWritesDetected(r.Unchanged, sidecarsBefore, sidecarsAfter)
	r.CoordinationChanged = coordinationChanged(sidecarsBefore, sidecarsAfter)
	return writeReportAndValidate(resolvedOut, r)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "listperf:", err)
		os.Exit(1)
	}
}
