package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/app"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestAdminOverviewPublicationV2Diagnostics(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pub-diag.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":["duration"],"errors":[{"source":"ffprobe","message":"duration unavailable"},{"source":"probe","message":"partial"}]},"optional_steps":["scrape","prepare"],"required_steps":["poster"],"steps":["poster","scrape","prepare"]}`
	secretSnapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":[],"errors":[]},"wrapped_dek":"SECRET_DEK","iv":"SECRET_IV","secret":"nope"}`

	_, err = db.Exec(`
INSERT INTO library(id,name,type,path) VALUES(1,'movies','video','E:/movies');
INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,publication_error,ingest_generation) VALUES
 (10,1,'a','Failed','a.mkv','video','active','failed','poster failed',1),
 (11,1,'b','Processing','b.mkv','video','active','processing','',1),
 (12,1,'c','PublishedClean','c.mkv','video','active','published','',1),
 (13,1,'d','Superseded','d.mkv','video','active','failed','old',2),
 (14,1,'e','PublishedOptional','e.mkv','video','active','published','',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,policy_version,terminal_reason,superseded_by_generation,superseded_at,updated_at) VALUES
 (100,10,1,'scan','failed',0,?,'poster failed',2,'required_failed',NULL,NULL,datetime('2026-07-22 12:00:00')),
 (101,11,1,'scan','processing',0,?,'',2,'',NULL,NULL,datetime('2026-07-22 11:00:00')),
 (102,12,1,'scan','published',0,'{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":["title"],"errors":[]},"steps":["poster"]}','',2,'required_done',NULL,NULL,datetime('2026-07-22 10:00:00')),
 (103,13,1,'scan','failed',0,'{}','old',2,'required_failed',2,datetime('2026-07-22 09:00:00'),datetime('2026-07-22 09:00:00')),
 (104,13,2,'manual_retry','processing',0,?,'',2,'',NULL,NULL,datetime('2026-07-22 12:30:00')),
 (105,14,1,'scan','published',0,?,'',2,'required_done',NULL,NULL,datetime('2026-07-22 08:00:00'));
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (200,100,10,1,'poster',1,'failed',3,3,'poster failed'),
 (201,100,10,1,'scrape',0,'waiting',0,3,''),
 (211,100,10,1,'media_visible',0,'done',0,3,''),
 (202,101,11,1,'poster',1,'waiting',0,3,''),
 (203,101,11,1,'scrape',0,'waiting',0,3,''),
 (204,101,11,1,'prepare',0,'waiting',0,3,''),
 (212,101,11,1,'media_visible',0,'done',0,3,''),
 (205,102,12,1,'poster',1,'done',1,3,''),
 (206,103,13,1,'poster',1,'failed',3,3,'old'),
 (207,104,13,2,'poster',1,'running',1,3,''),
 (208,105,14,1,'poster',1,'done',1,3,''),
 (209,105,14,1,'scrape',0,'waiting',0,3,''),
 (210,105,14,1,'prepare',0,'failed',3,3,'prepare boom'),
 (213,105,14,1,'media_visible',0,'done',0,3,'');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES
 (201,211,'success'),(203,212,'success'),(204,212,'success'),(209,213,'success'),(210,213,'success');
INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,recovery_error,updated_at) VALUES
 ('stage-10',10,100,200,1,'owner','fp','poster','quarantined','/tmp/stage','{}','asset recovery failed',datetime('2026-07-22 12:01:00'));
`, snapshot, snapshot, secretSnapshot, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(11,101,202,1,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	_, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,updated_at)
 VALUES('enc-11',?,1,11,101,202,1,'owner','/src','fp','/enc','WRAPPED_DEK_SECRET','IV_SECRET','abc',10,'staged','encrypt recovery pending',datetime('2026-07-22 11:05:00'));
INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json,recovery_error,updated_at)
 VALUES('repair-14',?,14,105,1,'owner',1,'fp','failed_closed','/staged','{}','older poster repair',datetime('2026-07-22 07:00:00'))`, taskID, taskID)
	if err != nil {
		t.Fatal(err)
	}

	caps := publication.NewCapabilityMatrix([]string{"poster", "thumbnail", "encrypt", "preview", "subtitle"})
	b := NewAdminOverviewBuilder(db, nil, nil)
	b.Capabilities = caps
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, data)
	encoded := string(raw)
	if !strings.Contains(encoded, `"publication_policy"`) {
		t.Fatalf("missing publication_policy in %s", encoded)
	}
	for _, secret := range []string{"WRAPPED_DEK_SECRET", "IV_SECRET", "SECRET_DEK", "SECRET_IV", "wrapped_dek", `"iv"`} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("key material leaked %q in %s", secret, encoded)
		}
	}

	var decoded struct {
		PublicationPolicy []PublicationPolicyDiagnostic `json:"publication_policy"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.PublicationPolicy) == 0 {
		t.Fatal("expected actionable publication_policy rows")
	}
	byMedia := map[int64]PublicationPolicyDiagnostic{}
	for _, row := range decoded.PublicationPolicy {
		byMedia[row.MediaID] = row
	}
	if _, ok := byMedia[12]; ok {
		t.Fatalf("clean published media should be excluded: %+v", byMedia[12])
	}
	if _, ok := byMedia[13]; !ok {
		t.Fatalf("current generation superseded media missing: %+v", decoded.PublicationPolicy)
	}
	if byMedia[13].Generation != 2 || byMedia[13].Status != "processing" {
		t.Fatalf("superseded media row=%+v", byMedia[13])
	}
	failed := byMedia[10]
	if failed.PolicyVersion != 2 || failed.Status != "failed" || failed.TerminalReason != "required_failed" ||
		failed.RequiredFailed != 1 || failed.OptionalWaiting != 1 || failed.RecoveryError != "asset recovery failed" {
		t.Fatalf("failed row=%+v", failed)
	}
	if len(failed.MetadataErrors) < 1 || !strings.Contains(strings.Join(failed.MetadataErrors, "|"), "duration unavailable") {
		t.Fatalf("metadata errors=%v", failed.MetadataErrors)
	}
	proc := byMedia[11]
	if proc.RequiredWaiting != 1 || proc.OptionalWaiting != 2 || proc.RecoveryError != "encrypt recovery pending" {
		t.Fatalf("processing row=%+v", proc)
	}
	wantAdapters := map[string]bool{"scrape": true, "prepare": true}
	for _, name := range proc.AdapterUnavailable {
		delete(wantAdapters, name)
	}
	if len(wantAdapters) != 0 {
		t.Fatalf("adapter_unavailable=%v missing=%v", proc.AdapterUnavailable, wantAdapters)
	}
	pubOpt := byMedia[14]
	if pubOpt.OptionalFailed != 1 || pubOpt.OptionalWaiting != 1 {
		t.Fatalf("published optional issues=%+v", pubOpt)
	}
	if len(decoded.PublicationPolicy) > 100 {
		t.Fatalf("bounded to 100 got %d", len(decoded.PublicationPolicy))
	}
	// severity: failed before processing
	if decoded.PublicationPolicy[0].Status != "failed" {
		t.Fatalf("expected failed first by severity, got %+v", decoded.PublicationPolicy[0])
	}

	gin.SetMode(gin.TestMode)
	w := callAdminOverview(t, b)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"publication_policy"`) {
		t.Fatalf("HTTP overview status=%d body=%s", w.Code, w.Body.String())
	}

	r := gin.New()
	admin := r.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) { setUserCtx(c, 1, "user", "viewer") })
	admin.Use(middleware.RequireAdmin())
	admin.GET("/overview", (&Handler{AdminOverviewBuilder: b}).AdminOverview)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminOverviewPublicationIgnoresCommittedRecoveryMarkers(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pub-committed-recovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":["title"],"errors":[]},"optional_steps":[],"required_steps":["poster"],"steps":["poster"]}`
	_, err = db.Exec(`
INSERT INTO library(id,name,type,path) VALUES(1,'movies','video','E:/movies');
INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,publication_error,ingest_generation) VALUES
 (20,1,'enc-ok','PublishedEncMarker','enc.mkv','video','active','published','',1),
 (21,1,'asset-ok','PublishedAssetMarker','asset.mkv','video','active','published','',1),
 (22,1,'poster-ok','PublishedPosterMarker','poster.mkv','video','active','published','',1),
 (23,1,'unresolved','PublishedUnresolved','bad.mkv','video','active','published','',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,policy_version,terminal_reason,updated_at) VALUES
 (120,20,1,'scan','published',0,?,'',2,'required_done',datetime('2026-07-22 10:00:00')),
 (121,21,1,'scan','published',0,?,'',2,'required_done',datetime('2026-07-22 09:00:00')),
 (122,22,1,'scan','published',0,?,'',2,'required_done',datetime('2026-07-22 08:00:00')),
 (123,23,1,'scan','published',0,?,'',2,'required_done',datetime('2026-07-22 07:00:00'));
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (220,120,20,1,'poster',1,'done',1,3,''),
 (221,121,21,1,'poster',1,'done',1,3,''),
 (222,122,22,1,'poster',1,'done',1,3,''),
 (223,123,23,1,'poster',1,'done',1,3,'');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES
 (320,20,120,220,1,'encrypt','done'),
 (321,22,122,222,1,'poster_repair','done'),
 (322,23,123,223,1,'encrypt','failed');
INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,updated_at)
 VALUES('enc-20',320,1,20,120,220,1,'owner','/src','fp','/enc','dek','iv','abc',10,'committed','verified_committed',datetime('2026-07-22 10:05:00'));
INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,recovery_error,updated_at)
 VALUES('asset-21',21,121,221,1,'owner','fp','poster','committed','/tmp/stage','{}','verified_committed',datetime('2026-07-22 09:05:00'));
INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json,recovery_error,updated_at)
 VALUES('repair-22',321,22,122,1,'owner',1,'fp','committed','/staged','{}','cleaned_unreferenced',datetime('2026-07-22 08:05:00'));
INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,updated_at)
 VALUES('enc-23',322,1,23,123,223,1,'owner','/src','fp','/enc','dek','iv','abc',10,'failed_closed','encrypt still broken',datetime('2026-07-22 07:05:00'));
`, snapshot, snapshot, snapshot, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	b := NewAdminOverviewBuilder(db, nil, nil)
	b.Capabilities = publication.NewCapabilityMatrix([]string{"poster", "thumbnail", "encrypt"})
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		PublicationPolicy []PublicationPolicyDiagnostic `json:"publication_policy"`
	}
	if err := json.Unmarshal(mustJSON(t, data), &decoded); err != nil {
		t.Fatal(err)
	}
	byMedia := map[int64]PublicationPolicyDiagnostic{}
	for _, row := range decoded.PublicationPolicy {
		byMedia[row.MediaID] = row
	}
	for _, id := range []int64{20, 21, 22} {
		if row, ok := byMedia[id]; ok {
			t.Fatalf("published media %d with committed recovery markers must be excluded, got %+v", id, row)
		}
	}
	unresolved, ok := byMedia[23]
	if !ok {
		t.Fatalf("published media with unresolved recovery_error missing: %+v", decoded.PublicationPolicy)
	}
	if unresolved.RecoveryError != "encrypt still broken" {
		t.Fatalf("unresolved recovery_error=%q want encrypt still broken", unresolved.RecoveryError)
	}
	if len(decoded.PublicationPolicy) > 100 {
		t.Fatalf("bounded to 100 got %d", len(decoded.PublicationPolicy))
	}
}

func TestAdminGetMediaIngestPublicationV2Enrichment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "media-ingest-enrich.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":["duration"],"errors":[{"source":"ffprobe","message":"duration unavailable"}]},"optional_steps":["scrape"],"required_steps":["poster"],"steps":["poster","scrape"],"dependencies":[{"step":"scrape","kind":"media_visible"}]}`
	_, err = db.Exec(`
INSERT INTO library(id,name,type,path,enabled) VALUES(1,'movies','movie','E:/movies',1);
INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all');
INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,publication_error,ingest_generation) VALUES
 (101,1,'processing','Processing','E:/movies/processing.mkv','video','active','processing','',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,policy_version,terminal_reason) VALUES
 (201,101,1,'scan','processing',0,?,'',2,'');
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (301,201,101,1,'poster',1,'running',1,3,''),(302,201,101,1,'scrape',0,'waiting',0,3,''),(303,201,101,1,'media_visible',0,'done',0,3,'');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(302,303,'success');
INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES
 (201,301,101,1,'poster','fp','{"path":"/poster.jpg","wrapped_dek":"SECRET"}',CURRENT_TIMESTAMP,'ev-1');
INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,recovery_error) VALUES
 ('stage-101',101,201,301,1,'owner','fp','poster','quarantined','/tmp/s','{}','journal recover me');
`, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	caps := publication.NewCapabilityMatrix([]string{"poster", "thumbnail", "encrypt"})
	h := New(&app.App{DB: db}, Dependencies{PublicationPlanner: publication.NewPlanner(publication.PlanOptions{}), PublicationCapabilities: caps})
	c, w := adminIngestContext(http.MethodGet, "/api/v1/admin/media/101/ingest", "101")
	h.AdminGetMediaIngest(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"SECRET", "wrapped_dek"} {
		if strings.Contains(body, secret) {
			t.Fatalf("key material leaked %q in %s", secret, body)
		}
	}
	var payload struct {
		Media map[string]any `json:"media"`
		Run   struct {
			ID             int64  `json:"id"`
			PolicyVersion  int    `json:"policy_version"`
			TerminalReason string `json:"terminal_reason"`
			Status         string `json:"status"`
			Generation     int64  `json:"generation"`
		} `json:"run"`
		Steps                []map[string]any `json:"steps"`
		MetadataDiagnostics  []map[string]any `json:"metadata_diagnostics"`
		Dependencies         []map[string]any `json:"dependencies"`
		Evidence             []map[string]any `json:"evidence"`
		AdapterUnavailable   []string         `json:"adapter_unavailable"`
		UnresolvedRecovery   []string         `json:"unresolved_recovery_errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Run.PolicyVersion != 2 || payload.Run.ID != 201 || payload.Run.Status != "processing" {
		t.Fatalf("run=%+v", payload.Run)
	}
	if len(payload.MetadataDiagnostics) != 1 || payload.MetadataDiagnostics[0]["source"] != "ffprobe" {
		t.Fatalf("metadata_diagnostics=%v", payload.MetadataDiagnostics)
	}
	if len(payload.Dependencies) != 1 {
		t.Fatalf("dependencies=%v", payload.Dependencies)
	}
	if len(payload.Evidence) != 1 {
		t.Fatalf("evidence missing: %s", body)
	}
	ev := payload.Evidence[0]
	if ev["kind"] != "poster" || ev["stage_id"] != "ev-1" {
		t.Fatalf("evidence=%v", payload.Evidence)
	}
	if _, ok := ev["wrapped_dek"]; ok {
		t.Fatalf("evidence leaked secrets: %v", ev)
	}
	foundScrape := false
	for _, name := range payload.AdapterUnavailable {
		if name == "scrape" {
			foundScrape = true
		}
	}
	if !foundScrape {
		t.Fatalf("adapter_unavailable=%v", payload.AdapterUnavailable)
	}
	if len(payload.UnresolvedRecovery) == 0 || !strings.Contains(strings.Join(payload.UnresolvedRecovery, "|"), "journal recover me") {
		t.Fatalf("unresolved_recovery_errors=%v", payload.UnresolvedRecovery)
	}
	// backward compatible keys
	if payload.Media["id"] == nil || len(payload.Steps) != 3 {
		t.Fatalf("backward compatible payload broken: media=%v steps=%d", payload.Media, len(payload.Steps))
	}
}
