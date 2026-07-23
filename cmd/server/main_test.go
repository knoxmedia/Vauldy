package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/buildinfo"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func TestPendingEncryptionUsesPostIngestQueue(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, "KickPendingMediaEncryption") || strings.Contains(src, "KickEncryptMedia(assetEnc") {
		t.Fatal("automatic encryption still uses legacy goroutine bypass")
	}
	if !strings.Contains(src, "EnqueuePendingMediaEncryption") || !strings.Contains(src, "TaskEncrypt") {
		t.Fatal("startup pending encryption does not enqueue unified encrypt tasks")
	}
}

func TestMainHasNoGlobalScannerMediaCallback(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, "sc.OnMediaAdded =") {
		t.Fatal("main still installs a shared Scanner.OnMediaAdded callback")
	}
	if strings.Contains(src, "func enqueueAutoTasksOnMediaAdded") {
		t.Fatal("legacy automatic post-ingest helper still exists")
	}
}
func TestMainGuardsPendingEncryptionWithGlobalSwitch(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	call := strings.Index(src, "postingest.EnqueuePendingMediaEncryption")
	guard := strings.LastIndex(src[:call], "cfg.EncryptedAssetsEnabled()")
	if call < 0 || guard < 0 || call-guard > 300 {
		t.Fatal("pending encryption is not guarded by global encrypted assets switch")
	}
}

func TestMainDoesNotStartEncryptedMP4PipeRepairs(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "KickEncryptedMP4PipeRepairs(") {
		t.Fatal("startup still launches encrypted MP4 repair bypass")
	}
}

func TestSharedResourceControlAssemblyMainOrder(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	markers := []string{
		"// (1) Database, metrics, and validated configuration.",
		"// (2) Vault, derived storage, and domain workers.",
		"// (3) Shared post-ingest queue, enqueuer, seven adapters, and dispatcher.",
		"// (4) Scanner dependencies and the process-wide scan coordinator.",
		"// (5) Admin overview uses the shared resource-control instances.",
		"// (6) Handler dependencies are injected into the API router.",
		"// (7) Monitor submits through the same coordinator and starts last.",
		"// (8) Root cancellation stops monitor, scans, and dispatcher.",
	}
	previous := -1
	for _, marker := range markers {
		at := strings.Index(src, marker)
		if at < 0 {
			t.Fatalf("missing assembly marker %q", marker)
		}
		if at <= previous {
			t.Fatalf("assembly marker out of order %q", marker)
		}
		previous = at
	}
	for _, required := range []string{"postingest.AdapterSet{", "postingest.NewThumbnailAdapter(", "postingest.NewPosterAdapter(", "postingest.NewPreviewAdapter(", "postingest.NewKeyframeAdapter(", "postingest.NewSubtitleAdapter(", "postingest.NewAtrackAdapter(", "postingest.NewEncryptAdapter(", "scancoord.New(", "monitor.NewService(db, coordinator", "api.NewEngine(cfg, application, deps)"} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing shared assembly %q", required)
		}
	}
}

func TestMainUsesThumbnailAdapterWithoutLegacyEnsure(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "postingest.NewThumbnailAdapter(") {
		t.Fatal("missing thumbnail adapter")
	}
	if strings.Contains(src, "generatePhotoVariantsOnScan") || strings.Contains(src, "imagethumb.Ensure") {
		t.Fatal("server retains legacy direct thumbnail publication")
	}
}

func TestGracefulShutdownAssemblyUsesSignalsAndHTTPServer(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, required := range []string{"signal.NotifyContext(", "os.Interrupt", "syscall.SIGTERM", "http.Server{", "httpServer.Shutdown(", "coordinator.ShutdownContext(", "dispatcherDone", "monitorDone"} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing graceful shutdown construct %q", required)
		}
	}
	if strings.Contains(src, "engine.Run(") {
		t.Fatal("main still uses uncontrollable engine.Run")
	}
}

func TestBuildDispatcherOptionsMapsPostIngestConfig(t *testing.T) {
	cfg := &config.Config{PostIngest: config.PostIngestConfig{MaxConcurrent: 3, PosterMaxConcurrent: 1, PreviewMaxConcurrent: 2}}
	got := buildDispatcherOptions(cfg, "owner")
	if got.OwnerID != "owner" || got.Global != 3 || got.Poster != 1 || got.Preview != 2 {
		t.Fatalf("options=%+v", got)
	}
}

func TestMainInjectsAndWaitsBackgroundGroup(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, required := range []string{"background := &handler.BackgroundGroup{}", "ServerContext: serverCtx", "Background: background", "background.Wait(shutdownCtx)", "coordinator.ShutdownContext(shutdownCtx)"} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing %q", required)
		}
	}
	if strings.Index(src, "background.Wait(shutdownCtx)") > strings.Index(src, "coordinator.ShutdownContext(shutdownCtx)") {
		t.Fatal("main waits background loops after coordinator")
	}
}

func TestHandleScanCancelledFailsInitialPublicationBeforeLocalCancellation(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('cancel','video','/cancel')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'cancelled','test')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,publication_state,ingest_generation) VALUES(?,'cancel-me','processing',1)`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,scan_task_id,generation,reason,status,config_snapshot_json) VALUES(?,?,1,'scan','processing','{}')`, mediaID, scanID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'waiting')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(?,?,?,?,1,'poster','waiting')`, mediaID, scanID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	queue := postingest.NewQueue(db, "server-cancel", nil)
	localCalls := 0
	ctx := context.WithValue(context.Background(), struct{}{}, "provided")
	if err := handleScanCancelled(ctx, scanID, queue.CancelScan, func(id int64) {
		localCalls++
		if id != scanID {
			t.Fatalf("local id=%d", id)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if localCalls != 1 {
		t.Fatalf("local calls=%d", localCalls)
	}
	var q, s, r, m string
	if err := db.QueryRow(`SELECT q.status,s.status,r.status,m.publication_state FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id JOIN media_ingest_run r ON r.id=q.ingest_run_id JOIN media m ON m.id=q.media_id WHERE q.scan_task_id=?`, scanID).Scan(&q, &s, &r, &m); err != nil {
		t.Fatal(err)
	}
	if q != "cancelled" || s != "cancelled" || r != "cancelled" || m != "cancelled" {
		t.Fatalf("states=%s/%s/%s/%s", q, s, r, m)
	}
}

func TestHandleScanCancelledCancelsLocalWorkerAndPropagatesPersistenceError(t *testing.T) {
	want := errors.New("persist failed")
	localCalls := 0
	err := handleScanCancelled(context.Background(), 77, func(context.Context, int64) (int64, error) { return 0, want }, func(id int64) {
		localCalls++
		if id != 77 {
			t.Fatalf("id=%d", id)
		}
	})
	if !errors.Is(err, want) || localCalls != 1 {
		t.Fatalf("err=%v local=%d", err, localCalls)
	}
}

func TestMainManagesLegacyRepairWithBackgroundGroup(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, required := range []string{
		"background.Go(serverCtx, func(repairCtx context.Context)",
		"publication.RepairLegacyMedia(repairCtx, db, repairPlanner, 64)",
		"background.Wait(shutdownCtx)",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing managed repair lifecycle %q", required)
		}
	}
	if strings.Contains(src, "go func() {\n\t\trepaired, repairErr := publication.RepairLegacyMedia(serverCtx") {
		t.Fatal("legacy repair still uses unmanaged goroutine")
	}
	if strings.Index(src, "publication.RepairLegacyMedia(repairCtx") > strings.Index(src, "background.Wait(shutdownCtx)") {
		t.Fatal("repair starts after background shutdown wait")
	}
}

func TestMainInjectsRegisteredIngestPrepareCapability(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "PreparePlanner: preparePlanner, Capabilities: prepareCapabilities") {
		t.Fatal("publication planner does not receive registered prepare capability")
	}
	if strings.Contains(src, "PrepareAvailable: false") {
		t.Fatal("startup still hard-codes prepare unavailable")
	}
}

func TestMainStartsBoundedFinalizeRecoveryLoop(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, required := range []string{"scancoord.RecoverPendingFinalizations(serverCtx, db, 16)", "finalizeRecoveryDone", "case <-serverCtx.Done()"} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing finalize recovery wiring %q", required)
		}
	}
}

func TestServerStartupBuildLogFields(t *testing.T) {
	got := startupBuildLog(buildinfo.Parse("v1.2.3", "abc123", "2026-07-22T01:02:03Z", "false", buildinfo.VCSInfo{Revision: "abc123", Time: "2026-07-22T01:00:00Z", ModifiedKnown: true}), store.SQLiteDBIdentity{Path: "data/knox.db"})
	for _, want := range []string{"version=v1.2.3", "commit=abc123", "build_time=2026-07-22T01:02:03Z", "dirty=false", "vcs_revision=abc123", "vcs_time=2026-07-22T01:00:00Z", "vcs_modified=false", "db_path=data/knox.db"} {
		if !strings.Contains(got, want) {
			t.Fatalf("startup log %q missing %q", got, want)
		}
	}
}

func TestStartupBuildLogUsesOpenedSQLiteIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.sqlite")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identity, ok := store.SQLiteIdentity(db)
	if !ok {
		t.Fatal("missing SQLite identity")
	}
	got := startupBuildLog(buildinfo.Parse("v1", "abc", "2026-07-22T01:02:03Z", "false", buildinfo.VCSInfo{}), identity)
	absolute, _ := filepath.Abs(path)
	for _, want := range []string{"db_path=" + absolute, "schema_version=", "user_version="} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q missing %q", got, want)
		}
	}
	data, _ := os.ReadFile("main.go")
	src := string(data)
	if strings.Index(src, "store.OpenSQLiteContext") > strings.Index(src, "startupBuildLog") {
		t.Fatal("build/database identity log occurs before database open")
	}
}
