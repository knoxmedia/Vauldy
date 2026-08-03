package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

// TestAdminOverviewPublicationPolicy_NotPresent verifies that the Console
// /admin/overview endpoint does NOT contain publication_policy diagnostics,
// as task control has been moved to the unified task control plane.
func TestAdminOverviewPublicationPolicy_NotPresent(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pub-not-in-overview.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":["duration"],"errors":[{"source":"ffprobe","message":"duration unavailable"}]},"optional_steps":["scrape"],"required_steps":["poster"],"steps":["poster","scrape"]}`
	_, err = db.Exec(`
INSERT INTO library(id,name,type,path) VALUES(1,'movies','video','E:/movies');
INSERT INTO media(id,library_id,title,file_path,file_type,status,ingest_generation) VALUES
 (10,1,'Failed','a.mkv','video','active',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,policy_version,terminal_reason,updated_at) VALUES
 (100,10,1,'scan','failed',0,?,'poster failed',2,'required_failed',datetime('2026-07-22 12:00:00'));
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (200,100,10,1,'poster',1,'failed',3,3,'poster failed'),
 (201,100,10,1,'scrape',0,'waiting',0,3,'');
`, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	b := NewAdminOverviewBuilder(db, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, data)
	encoded := string(raw)

	// publication_policy must NOT appear in the system-only overview
	if strings.Contains(encoded, `"publication_policy"`) {
		t.Fatalf("publication_policy must not appear in system-only overview: %s", encoded)
	}
}

// TestAdminOverviewPublicationPolicy_SecretsSafety verifies that no sensitive
// key material leaks through the overview response.
func TestAdminOverviewPublicationPolicy_SecretsSafety(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pub-secrets-safety.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapshot := `{"policy_version":2,"library_id":1,"file_type":"video","metadata":{"attempted":true,"fields":[],"errors":[]},"wrapped_dek":"SECRET_DEK","iv":"SECRET_IV","secret":"nope"}`
	_, err = db.Exec(`
INSERT INTO library(id,name,type,path) VALUES(1,'movies','video','E:/movies');
INSERT INTO media(id,library_id,title,file_path,file_type,status,ingest_generation) VALUES
 (20,1,'SecretTest','secret.mkv','video','active',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,policy_version,terminal_reason,updated_at) VALUES
 (120,20,1,'scan','published',0,?,'',2,'required_done',datetime('2026-07-22 10:00:00'));
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (220,120,20,1,'poster',1,'done',1,3,'');
`, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	b := NewAdminOverviewBuilder(db, nil)
	b.SampleSystem = func(context.Context, string) (SystemSample, error) { return SystemSample{}, nil }

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, data)
	encoded := string(raw)

	// No secrets should leak
	for _, secret := range []string{"WRAPPED_DEK_SECRET", "IV_SECRET", "SECRET_DEK", "SECRET_IV", "wrapped_dek", `"iv"`} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("key material leaked %q in %s", secret, encoded)
		}
	}
}

// TestAdminGetMediaIngestPublicationV2Enrichment verifies the per-media ingest
// endpoint enrichment (this endpoint is separate from the overview).
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
	if payload.Media["id"] == nil || len(payload.Steps) != 3 {
		t.Fatalf("backward compatible payload broken: media=%v steps=%d", payload.Media, len(payload.Steps))
	}
}
