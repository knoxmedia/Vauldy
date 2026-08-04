package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/postingest"
	"knox-media/internal/publication"
	"knox-media/internal/retirement"
	"knox-media/internal/scanner"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/internal/transcode"
	"knox-media/pkg/ffprobe"
)

// Phase 1 Task 15 acceptance cases: end-to-end safety boundaries using the
// existing publication e2e / handler harnesses (no new test framework).

type phase1ExecAdapter publication.StepType

func (a phase1ExecAdapter) TaskType() publication.StepType { return publication.StepType(a) }
func (phase1ExecAdapter) Execute(context.Context, int64) error {
	return nil
}

type phase1ExecRegistry map[publication.StepType]publication.ExecutableTaskAdapter

func (r phase1ExecRegistry) Adapter(step publication.StepType) (publication.ExecutableTaskAdapter, bool) {
	a, ok := r[step]
	return a, ok
}

func phase1RecognitionAdapters() phase1ExecRegistry {
	return phase1ExecRegistry{
		publication.StepSubtitleRecognize: phase1ExecAdapter(publication.StepSubtitleRecognize),
		publication.StepAIAnalysis:        phase1ExecAdapter(publication.StepAIAnalysis),
	}
}

type phase1StageRoot string

func (r phase1StageRoot) ResolveEncryptionStageRoot(context.Context, int64, string) (string, error) {
	return string(r), nil
}

func newPhase1ClosureE2E(t *testing.T) *publicationE2E {
	t.Helper()
	root, upload := t.TempDir(), t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-closure.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	res, err := db.Exec(`INSERT INTO library(name,type,path,subtitle_recognize,ai_analysis,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext)
VALUES('phase1','video',?,1,1,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'viewer','x','user',1,'all'),(2,'admin','x','admin',1,'all')`); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "Phase1.Closure.2026.mp4")
	if err = os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := &scanner.Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{Width: 1, Height: 1}, nil
	}}
	planner := publication.NewPlanner(publication.PlanOptions{
		EncryptGlobal:             true,
		ExecutableAdapters:        phase1RecognitionAdapters(),
		EncryptedSourceStrategies: publication.DefaultEncryptedSourceStrategies(),
	})
	added, err := sc.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, scanner.ScanCallbacks{OnMediaDiscoveredTx: func(ctx context.Context, tx *sql.Tx, discovery scanner.ScanDiscovery) error {
		_, e := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
			MediaID: discovery.MediaID, ScanTaskID: scanID, FileType: discovery.FileType,
		})
		return e
	}})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	var mediaID, runID int64
	if err = db.QueryRow(`SELECT id FROM media WHERE library_id=?`, libraryID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Data.Upload = upload
	h := &Handler{App: &app.App{DB: db, Config: cfg}, Queue: postingest.NewQueue(db, "phase1", nil), runningScans: map[int64]scanRuntime{}}
	return &publicationE2E{db: db, h: h, root: root, upload: upload, libraryID: libraryID, scanID: scanID, mediaID: mediaID, runID: runID}
}

func TestPhase1LibraryClosureDrivenPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPhase1ClosureE2E(t)

	var snapshotJSON string
	if err := e.db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, e.runID).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot publication.ConfigSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.ProcessingExplicit.SubtitleRecognize || !snapshot.ProcessingExplicit.AIAnalysis {
		t.Fatalf("explicit=%+v", snapshot.ProcessingExplicit)
	}
	if !snapshot.ProcessingEffective.SubtitleExtract || !snapshot.ProcessingEffective.ATrackExtract ||
		!snapshot.ProcessingEffective.SubtitleRecognize || !snapshot.ProcessingEffective.AIAnalysis {
		t.Fatalf("effective=%+v", snapshot.ProcessingEffective)
	}
	sort.Strings(snapshot.ProcessingProvenance.DependencyAdded)
	wantDeps := []string{"atrack_extract", "subtitle_extract"}
	if fmt.Sprint(snapshot.ProcessingProvenance.DependencyAdded) != fmt.Sprint(wantDeps) {
		t.Fatalf("dependency_added=%v want %v", snapshot.ProcessingProvenance.DependencyAdded, wantDeps)
	}

	w := e2eRequest(t, e, true, http.MethodGet, fmt.Sprintf("/api/v1/admin/media/%d/ingest", e.mediaID), e.h.AdminGetMediaIngest)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest status=%d body=%s", w.Code, w.Body.String())
	}
	var ingest struct {
		Steps []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ingest); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range ingest.Steps {
		got[s.Type] = true
	}
	for _, want := range []string{"subtitle_extract", "atrack_extract", "subtitle_recognize", "ai_analysis", "encrypt", "poster"} {
		if !got[want] {
			t.Fatalf("missing closed step %s in %v", want, got)
		}
	}
	rows, err := e.db.Query(`SELECT child.step_type, parent.step_type, d.dependency_kind
FROM media_ingest_step_dependency d
JOIN media_ingest_step child ON child.id=d.step_id
JOIN media_ingest_step parent ON parent.id=d.depends_on_step_id
WHERE child.run_id=? AND child.step_type IN ('subtitle_recognize','ai_analysis')`, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	edges := map[string]bool{}
	for rows.Next() {
		var child, parent, kind string
		if err := rows.Scan(&child, &parent, &kind); err != nil {
			t.Fatal(err)
		}
		edges[kind+":"+child+"<-"+parent] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !edges["success:subtitle_recognize<-subtitle_extract"] || !edges["success:ai_analysis<-subtitle_recognize"] {
		t.Fatalf("closure edges=%v", edges)
	}
}

func TestPhase1RecognitionPermanentFailSkipsAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPhase1ClosureE2E(t)

	completePostIngest(t, e, postingest.TaskPoster)
	completePostIngest(t, e, postingest.TaskEncrypt)
	// Queue-less media_visible must latch to done so optional dependents become claimable.
	if _, err := e.db.Exec(`UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type='media_visible' AND status='waiting'`, e.runID); err != nil {
		t.Fatal(err)
	}
	completePostIngest(t, e, postingest.TaskSubtitle)
	completePostIngest(t, e, postingest.TaskAtrack)

	task, err := e.h.Queue.Claim(context.Background(), postingest.TaskSubtitleRecognize)
	if err != nil || task == nil {
		t.Fatalf("claim recognize task=%v err=%v", task, err)
	}
	if err = e.h.Queue.Fail(context.Background(), task, postingest.FailurePermanent, errors.New("recognize permanent")); err != nil {
		t.Fatal(err)
	}

	var recogStatus, aiStatus, aiQueue, pub string
	if err = e.db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='subtitle_recognize'`, e.runID).Scan(&recogStatus); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='ai_analysis'`, e.runID).Scan(&aiStatus); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_run_id=? AND task_type='ai_analysis'`, e.runID).Scan(&aiQueue); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, e.mediaID).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if recogStatus != "failed" || aiStatus != "skipped" || aiQueue != "skipped" {
		t.Fatalf("recognize=%s ai_step=%s ai_queue=%s", recogStatus, aiStatus, aiQueue)
	}
	if pub != "published" {
		t.Fatalf("publication=%s want published", pub)
	}
	if w := e2eRequest(t, e, false, http.MethodGet, fmt.Sprintf("/api/v1/media/%d", e.mediaID), e.h.GetMedia); w.Code != http.StatusOK {
		t.Fatalf("published media hidden after optional recognition fail: %d %s", w.Code, w.Body.String())
	}
}

func TestPhase1PublicationVisibleWhileOptionalRemainAssertsPlanCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, true)
	completePostIngest(t, e, postingest.TaskPoster)
	completePostIngest(t, e, postingest.TaskEncrypt)

	var pub string
	var allTerminal, waiting int
	if err := e.db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, e.mediaID).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=?`, e.runID).Scan(&allTerminal, &waiting); err != nil {
		t.Fatal(err)
	}
	if pub != "published" || allTerminal != 0 || waiting < 1 {
		t.Fatalf("pub=%s all_terminal=%d waiting=%d", pub, allTerminal, waiting)
	}
	if w := e2eRequest(t, e, false, http.MethodGet, "/api/v1/media", e.h.ListMedia); w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, e.mediaID)) {
		t.Fatalf("ordinary list must show published media: %s", w.Body.String())
	}
}

func TestPhase1AllTerminalReleasesRetirement(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-retire.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	source := filepath.Join(root, "movie.mp4")
	encPath := filepath.Join(root, "movie.mp4.enc")
	plain := []byte("plaintext-body-for-retirement")
	encBody := []byte("ciphertext-body")
	if err = os.WriteFile(source, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(encPath, encBody, 0o600); err != nil {
		t.Fatal(err)
	}
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encBody)
	encSHA := hex.EncodeToString(sum[:])
	stageID := "00000000-0000-4000-8000-000000000015"

	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatalf("%s: %v", q, e)
		}
	}
	exec(`INSERT INTO library(id,name,type,path,encrypted_assets_cleanup_plaintext) VALUES(1,'lib','video',?,1)`, root)
	exec(`INSERT INTO media(id,library_id,file_id,file_type,file_path,ingest_generation,publication_state,published_at) VALUES(1,1,'f1','video',?,1,'published',CURRENT_TIMESTAMP)`, encPath)
	exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','published',?,3)`,
		`{"policy_version":3,"steps":["poster","encrypt","preview"],"encrypted_source_strategies":{"poster":{"strategy":"derivative","validated":true},"encrypt":{"strategy":"derivative","validated":true},"preview":{"strategy":"stream_decrypt","validated":true}}}`)
	exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),
 (12,10,1,1,'preview',0,'waiting',0,3),
 (13,10,1,1,'encrypt',1,'done',1,3)`)
	exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES
 (112,1,10,12,1,'preview','waiting',0,3,0),
 (113,1,10,13,1,'encrypt','done',1,3,0)`)
	exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count)
VALUES(10,1,1,0,3,2,1,0,2,0,0,0)`)
	exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(1,?, 'aabb','ccdd',?,'encrypted')`, encPath, source)
	exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state)
VALUES(?,113,0,1,1,10,13,1,'worker',?,?,?,'aabb','ccdd',?,?,1,'committed')`, stageID, source, fp, encPath, encSHA, int64(len(encBody)))
	exec(`INSERT INTO media_ingest_evidence(media_id,run_id,step_id,generation,kind,stage_id,source_fingerprint,artifact_refs_json,verified_at)
VALUES(1,10,13,1,'encrypt',?,?,'{"path":"enc"}',CURRENT_TIMESTAMP)`, stageID, fp)
	exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'encryption',113,?,NULL,0,'blocked','{}',CURRENT_TIMESTAMP)`, source, fp, stageID)

	var retirementID int64
	if err = db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE media_id=1`).Scan(&retirementID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = retirement.RecomputeRetirementBarrierTx(context.Background(), tx, 10); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, blocker string
	if err = db.QueryRow(`SELECT state,blocker_code FROM media_plaintext_retirement WHERE id=?`, retirementID).Scan(&state, &blocker); err != nil {
		t.Fatal(err)
	}
	if state != string(retirement.StateBlocked) || blocker != string(retirement.BlockerPlanNotTerminal) {
		t.Fatalf("optional waiting: state=%s blocker=%s", state, blocker)
	}

	// Complete the final optional node through the real post-ingest queue lifecycle.
	// Queue.Complete mirrors the linked step and invokes the plan projection plus
	// retirement-barrier hook in the same transaction.
	barrierCalls := 0
	publication.SetRetirementBarrierProbeForTest(func(runID int64) {
		if runID == 10 {
			barrierCalls++
		}
	})
	t.Cleanup(publication.ClearRetirementBarrierProbeForTest)
	q := postingest.NewQueue(db, "phase1-retirement", nil)
	task, err := q.Claim(context.Background(), postingest.TaskPreview)
	if err != nil || task == nil || task.ID != 112 {
		t.Fatalf("claim final optional task=(%+v,%v)", task, err)
	}
	if err = q.Complete(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
	if barrierCalls != 1 {
		t.Fatalf("retirement barrier hook calls=%d want 1", barrierCalls)
	}
	var allTerminal, terminalCount, waitingCount int
	if err = db.QueryRow(`SELECT all_terminal,terminal_count,waiting_count FROM media_plan_completion WHERE run_id=10`).Scan(&allTerminal, &terminalCount, &waitingCount); err != nil {
		t.Fatal(err)
	}
	if allTerminal != 1 || terminalCount != 3 || waitingCount != 0 {
		t.Fatalf("projection all_terminal=%d terminal=%d waiting=%d", allTerminal, terminalCount, waitingCount)
	}
	if err = db.QueryRow(`SELECT state,blocker_code FROM media_plaintext_retirement WHERE id=?`, retirementID).Scan(&state, &blocker); err != nil {
		t.Fatal(err)
	}
	if state != string(retirement.StateReady) || blocker != "" {
		t.Fatalf("all-terminal release: state=%s blocker=%s", state, blocker)
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatalf("source must remain until retirement executes: %v", err)
	}
}

func TestPhase1GenerationReplacementDuringRetirement(t *testing.T) {
	e := newPublicationE2E(t, false)
	completePostIngest(t, e, postingest.TaskPoster)

	source := filepath.Join(e.root, "Task15.Movie.2026.mp4")
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	pkgRes, err := e.db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status,progress,source_cleanup_status) VALUES(?,'cmaf_drm','done',100,'pending')`, e.mediaID)
	if err != nil {
		t.Fatal(err)
	}
	pkgID, _ := pkgRes.LastInsertId()
	res, err := e.db.Exec(`INSERT INTO media_plaintext_retirement(
media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(?,?,1,?,?,'package',?,NULL,?,0,'blocked','{}',CURRENT_TIMESTAMP)`, e.mediaID, e.runID, source, fp, pkgID, pkgID)
	if err != nil {
		t.Fatal(err)
	}
	retirementID, _ := res.LastInsertId()

	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := publication.NewPlanner(publication.PlanOptions{}).RepairMediaTx(context.Background(), tx, e.mediaID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if next.Generation != 2 {
		t.Fatalf("generation=%d", next.Generation)
	}

	tx, err = e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = retirement.RecomputeRetirementBarrierTx(context.Background(), tx, e.runID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, blocker string
	if err = e.db.QueryRow(`SELECT state,blocker_code FROM media_plaintext_retirement WHERE id=?`, retirementID).Scan(&state, &blocker); err != nil {
		t.Fatal(err)
	}
	if state != string(retirement.StateBlocked) || (blocker != string(retirement.BlockerGenerationFence) && blocker != string(retirement.BlockerSuperseded)) {
		t.Fatalf("state=%s blocker=%s want blocked/(generation fence|superseded)", state, blocker)
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatalf("source must remain during fenced retirement: %v", err)
	}
}

func TestPhase1EncryptedSourceRetryAfterSourceRemoval(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-enc-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := keystore.NewVault("phase1-acceptance-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := []byte("ftypmp42" + strings.Repeat("x", 2048))
	plainPath := filepath.Join(dir, "movie.mp4")
	if err = os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "movie.enc")
	out, err := os.Create(encPath)
	if err != nil {
		_ = in.Close()
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(in, out, kek)
	_ = in.Close()
	_ = out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(plainPath); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'lib','video',?)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,format,status) VALUES(1,1,'f','t',?,'video','mp4','active')`, encPath); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(1,?,?,?,?, 'encrypted')`,
		encPath, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV), plainPath); err != nil {
		t.Fatal(err)
	}

	got := storage.PreferredFFmpegPath(db, 1, 1, encPath)
	ff, err := storage.OpenFFmpegInput(db, vault, 1, got, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ff.Cleanup != nil {
			ff.Cleanup()
		}
	})
	if ff.Stdin == nil || !ff.FromEnc || ff.Path != "" {
		t.Fatalf("encrypted-source retry adapter=%+v want decrypt pipe", ff)
	}
	// This is the production retry boundary used by preview/keyframe/prepare workers:
	// OpenFFmpegInput supplies pipe:0 bytes to the external ffmpeg process. Consume the
	// stream exactly here so the acceptance test does not depend on an installed ffmpeg.
	args, stdin := storage.ApplyFFmpegInput(nil, ff)
	if len(args) != 2 || args[0] != "-i" || args[1] != "pipe:0" || stdin == nil {
		t.Fatalf("ffmpeg adapter args=%v stdin=%v", args, stdin != nil)
	}
	decrypted, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("decrypted retry payload mismatch: got %d bytes want %d", len(decrypted), len(plain))
	}
}

func TestPhase1TombstonedJournalRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-tombstone.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	quarantine := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	if err = os.WriteFile(source, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'l','video',?,1)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,ingest_generation) VALUES(10,1,'f','t',?,'video','active','processing',1)`, source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'encrypt',1,'failed')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round)
VALUES(40,10,20,30,1,'encrypt','failed',1,3,0)`); err != nil {
		t.Fatal(err)
	}
	enabled := true
	q := postingest.NewQueue(db, "phase1-tombstone", nil)
	h := &Handler{
		App:   &app.App{DB: db, Config: &config.Config{EncryptedAssets: config.EncryptedAssetsConfig{Enabled: &enabled}}},
		Queue: q,
	}
	wDel := httptest.NewRecorder()
	cDel, _ := gin.CreateTestContext(wDel)
	cDel.Params = gin.Params{{Key: "id", Value: "40"}}
	cDel.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/encrypt/task/40", nil)
	setUserCtx(cDel, 2, "admin", "admin")
	h.DeleteEncryptTask(cDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", wDel.Code, wDel.Body.String())
	}
	var removedAt sql.NullString
	if err = db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=40`).Scan(&removedAt); err != nil || !removedAt.Valid {
		t.Fatalf("tombstone missing: %v %v", removedAt, err)
	}

	stage := "10000000-0000-0000-0000-000000000015"
	enc := filepath.Join(root, stage+".enc")
	if err = os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,updated_at)
VALUES(?,40,0,1,10,20,30,1,'owner',?,'fp',?,'wrapped','iv','hash',3,'staged','2099-01-01 00:00:00')`, stage, source, enc); err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := postingest.ReconcileEncryptionStages(context.Background(), db, postingest.EncryptionRecoveryRoots{
		Quarantine: quarantine,
		Resolver:   phase1StageRoot(root),
	}, 10)
	if err != nil || checked < 1 || cleaned < 1 {
		t.Fatalf("tombstone recovery checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "staged" {
		t.Fatalf("expected recovery through tombstone, state=%s", state)
	}
	var stillThere int
	if err = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE id=40 AND removed_at IS NOT NULL`).Scan(&stillThere); err != nil || stillThere != 1 {
		t.Fatalf("tombstone row must remain count=%d err=%v", stillThere, err)
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatalf("authoritative source must not be deleted by journal recovery: %v", err)
	}
}

func phase1MockFFmpeg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "ffmpeg-run.bat")
		content := "@echo off\r\nsetlocal EnableDelayedExpansion\r\nset \"out=\"\r\n:next\r\nif \"%~1\"==\"\" goto done\r\nset \"out=%~1\"\r\nshift\r\ngoto next\r\n:done\r\nif \"%out%\"==\"\" exit /b 2\r\necho #EXTM3U> \"%out%\"\r\nexit /b 0\r\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "ffmpeg-run.sh")
	content := "#!/bin/sh\nout=\"\"\nfor arg in \"$@\"; do out=\"$arg\"; done\nif [ -z \"$out\" ]; then exit 2; fi\necho \"#EXTM3U\" > \"$out\"\nexit 0\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPhase1PackageCleanupDelegatesToRetirement(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-package.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	uploadDir := t.TempDir()
	src := filepath.Join(uploadDir, "movie.mp4")
	if err = os.WriteFile(src, []byte("authoritative-package-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := storage.EncryptionSourceFingerprint(src)
	if err != nil {
		t.Fatal(err)
	}
	libRes, err := db.Exec(`INSERT INTO library(name,type,path,drm_enabled,cleanup_local_source_after_package) VALUES('pkg','video',?,1,1)`, uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation,height) VALUES(?,'f1',?,'video','active','published',1,1080)`, libID, src)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','published','{}',3)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runRes.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'media_visible',1,'done')`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at)
VALUES(?,?,1,1,1,1,0,0,1,0,0,0,CURRENT_TIMESTAMP)`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	taskRes, err := db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status,progress) VALUES(?,'cmaf_drm','waiting',0)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskRes.LastInsertId()

	w := &transcode.PackageWorker{
		DB:           db,
		FFmpegPath:   phase1MockFFmpeg(t),
		TranscodeDir: t.TempDir(),
		UploadDir:    uploadDir,
	}
	if err = w.RunTask(context.Background(), taskID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	var status, cleanup string
	if err = db.QueryRow(`SELECT status,source_cleanup_status FROM package_task WHERE id=?`, taskID).Scan(&status, &cleanup); err != nil {
		t.Fatal(err)
	}
	if status != "done" || cleanup != "pending" {
		t.Fatalf("status=%s cleanup=%s want done/pending", status, cleanup)
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatalf("package must not delete authoritative source: %v", err)
	}
	var n int
	var basisKind, state, sourcePath, sourceFP string
	var basisID, pkgTaskID int64
	if err = db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(basis_kind),''),COALESCE(MAX(basis_id),0),COALESCE(MAX(package_task_id),0),COALESCE(MAX(source_path),''),COALESCE(MAX(source_fingerprint),''),COALESCE(MAX(state),'')
FROM media_plaintext_retirement WHERE media_id=?`, mediaID).
		Scan(&n, &basisKind, &basisID, &pkgTaskID, &sourcePath, &sourceFP, &state); err != nil {
		t.Fatal(err)
	}
	if n != 1 || basisKind != "package" || basisID != taskID || pkgTaskID != taskID || state == "" {
		t.Fatalf("retirement n=%d kind=%s basis=%d pkg=%d state=%s", n, basisKind, basisID, pkgTaskID, state)
	}
	if sourcePath != src || sourceFP != fp {
		t.Fatalf("identity path=%s fp=%s", sourcePath, sourceFP)
	}

	gin.SetMode(gin.TestMode)
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/transcode/task?limit=10", nil)
	h.ListTranscodeTasks(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	idStr := strconv.FormatInt(taskID, 10)
	if !strings.Contains(rec.Body.String(), idStr) || !strings.Contains(rec.Body.String(), `"source_cleanup_status":"pending"`) {
		t.Fatalf("list missing pending cleanup: %s", rec.Body.String())
	}
}
