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
	taskscheduler "knox-media/internal/scheduler"
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
	// Assembly markers in main.go are Chinese; keep English meaning aligned with order.
	markers := []string{
		"// (1) 打开 SQLite、记录构建/库身份信息，并写入默认用户。",
		"// (2) 密钥库/派生资产存储，以及转码、预览、字幕等域内 Worker。",
		"// (3) 入库后处理：能力矩阵、队列、入队器、七类适配器、分发器；企业模块与 Publication V2 启动编排。",
		"// (4) 媒体库扫描器与进程级扫描协调器（租约、心跳、发现回调）。",
		"// (5) 后台阶段对账协程，并组装 Handler 依赖注入包。",
		"// (6) 注入依赖并创建 HTTP API 路由引擎。",
		"// (7) 目录监控：通过同一扫描协调器提交增量扫描（放在最后启动）。",
		"// (8) 启动 HTTP 服务；根 context 取消后按序关停各子系统。",
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

func TestMainDoesNotStartKickPendingPlaintextCleanups(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "KickPendingPlaintextCleanups") {
		t.Fatal("main still starts legacy KickPendingPlaintextCleanups goroutine")
	}
}

func TestMainWiresRetirementWorkerBeforeClaimers(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	startupSrc, err := os.ReadFile("startup_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	main := string(mainSrc)
	startup := string(startupSrc)
	if !strings.Contains(startup, "retirement.ReconcileStartup(") {
		t.Fatal("startup recovery missing retirement.ReconcileStartup")
	}
	workerAt := strings.Index(main, "retirement.RunWorkerLoop(")
	if workerAt < 0 {
		workerAt = strings.Index(main, "RunWorkerLoop(")
	}
	reconcilerAt := strings.Index(main, "retirement.RunReconciler(")
	if reconcilerAt < 0 {
		reconcilerAt = strings.Index(main, "RunReconciler(")
	}
	claimerAt := strings.Index(main, "dispatcher.Start(serverCtx)")
	bgAt := strings.Index(main, "background := &handler.BackgroundGroup{}")
	if workerAt < 0 || reconcilerAt < 0 || claimerAt < 0 || bgAt < 0 {
		t.Fatalf("missing retirement wiring markers worker=%d reconciler=%d claimer=%d bg=%d", workerAt, reconcilerAt, claimerAt, bgAt)
	}
	if bgAt > claimerAt {
		t.Fatal("background group must be created before claimers so retirement can register first")
	}
	if workerAt > claimerAt || reconcilerAt > claimerAt {
		t.Fatal("retirement worker/reconciler must start before dispatcher claimers")
	}
}

func TestMainWiresRetirementActiveConsumerCallback(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "retirement.SetDefaultActiveConsumer(") {
		t.Fatal("main must register default ActiveConsumer for barrier recompute")
	}
	defaultAt := strings.Index(src, "retirement.SetDefaultActiveConsumer(")
	workerAt := strings.Index(src, "retirementWorker := &retirement.Worker{")
	if defaultAt < 0 || workerAt < 0 || defaultAt > workerAt {
		t.Fatal("SetDefaultActiveConsumer must run before retirement worker construction")
	}
	seamsAt := strings.Index(src[workerAt:], "Seams: retirement.CrashSeams{")
	if seamsAt < 0 {
		t.Fatal("retirement worker missing CrashSeams")
	}
	seamsBlock := src[workerAt+seamsAt:]
	end := strings.Index(seamsBlock, "},")
	if end < 0 {
		end = len(seamsBlock)
	}
	seamsBlock = seamsBlock[:end]
	if !strings.Contains(seamsBlock, "ActiveConsumer:") || !strings.Contains(seamsBlock, "storage.HasActivePlaintextConsumer(") {
		t.Fatal("main must wire HasActivePlaintextConsumer into CrashSeams.ActiveConsumer")
	}
	if !strings.Contains(src[defaultAt:workerAt], "storage.HasActivePlaintextConsumer(") {
		t.Fatal("SetDefaultActiveConsumer must use HasActivePlaintextConsumer")
	}
}

func TestMainWiresThumbnailStageStartupAndPeriodicReconciliation(t *testing.T) {
	startup, err := os.ReadFile("startup_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(startup)
	reconcileAt, resetAt := strings.Index(src, "postingest.ReconcileThumbnailStages("), strings.Index(src, "store.ResetInterruptedTasks(")
	if reconcileAt < 0 || resetAt < 0 || reconcileAt > resetAt {
		t.Fatal("thumbnail stages must reconcile before task recovery")
	}
	for _, want := range []string{"background.Go(serverCtx", "postingest.RunThumbnailStageReconciler("} {
		if !strings.Contains(string(main), want) {
			t.Fatalf("main missing %q", want)
		}
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

func TestMainInlinesDispatcherOptionsWiring(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	// After migration, dispatcher options are inlined rather than calling buildDispatcherOptions.
	if strings.Contains(src, "buildDispatcherOptions") {
		t.Fatal("buildDispatcherOptions must be removed after scheduler migration")
	}
	for _, required := range []string{
		"postingest.DefaultDispatcherOptions()",
		"OwnerID =",
		"SubtitleTimeoutRealtimeFactor",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing inline dispatcher wiring %q", required)
		}
	}
}

func TestBuildSchedulerPolicyMergesConfig(t *testing.T) {
	cfg := &config.Config{Scheduler: config.SchedulerConfig{
		Concurrency: map[string]int{"poster": 4, "preview": 3},
		Resources:   map[string]int{"cpu": 8, "gpu": 2},
	}}
	p, err := buildSchedulerPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.TypeConcurrency["poster"] != 4 {
		t.Fatalf("poster=%d want 4", p.TypeConcurrency["poster"])
	}
	if p.TypeConcurrency["preview"] != 3 {
		t.Fatalf("preview=%d want 3", p.TypeConcurrency["preview"])
	}
	if p.ResourceCapacity["cpu"] != 8 {
		t.Fatalf("cpu=%d want 8", p.ResourceCapacity["cpu"])
	}
	if _, ok := p.ResourceCapacity["gpu"]; !ok || p.ResourceCapacity["gpu"] != 2 {
		t.Fatalf("gpu=%v want 2", p.ResourceCapacity["gpu"])
	}
	if p.AgingIntervalSec <= 0 || p.AgingStep <= 0 || p.RunNowAmount <= 0 || p.RunNowTTLSec <= 0 {
		t.Fatalf("default priority tuning not applied: %+v", p)
	}
}

func TestActivateSchedulerPolicyCreatesActiveRevision(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "sched.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{Scheduler: config.SchedulerConfig{
		Concurrency: map[string]int{"poster": 2},
		Resources:   map[string]int{"cpu": 4},
	}}
	p, err := buildSchedulerPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := activateSchedulerPolicy(context.Background(), db, p); err != nil {
		t.Fatal(err)
	}
	st := taskscheduler.NewStore(db)
	rev, err := st.GetActivePolicyRevision(context.Background())
	if err != nil {
		t.Fatalf("get active revision: %v", err)
	}
	if rev == nil {
		t.Fatal("no active revision after activation")
	}
	// Re-activation preserves the existing active revision.
	if err := activateSchedulerPolicy(context.Background(), db, p); err != nil {
		t.Fatal(err)
	}
	rev2, err := st.GetActivePolicyRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rev2 == nil || rev2.ID != rev.ID {
		t.Fatalf("re-activation changed revision: %d -> %v", rev.ID, rev2)
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
	if !strings.Contains(src, "PreparePlanner: preparePlanner, Capabilities: publicationCapabilities") {
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

func TestMainWiresOneSharedPublicationCapabilityRegistry(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	constructor := strings.Index(src, "publicationCapabilities := publication.NewCapabilityMatrix(publicationSteps)")
	if constructor < 0 {
		t.Fatal("missing process publication capability registry")
	}
	if !hasOneUnconditionalPosterRepair(src, constructor) {
		t.Fatal(`publicationSteps composite literal must contain exact capability "poster_repair" once, adjacent to "poster"`)
	}
	for _, required := range []string{"postingest.NewQueue(db, queueOwner, sqliteMetrics, publicationCapabilities)", "PublicationCapabilities: publicationCapabilities", "Capabilities: publicationCapabilities"} {
		if !strings.Contains(src, required) {
			t.Fatalf("missing shared registry wiring %q", required)
		}
	}
}

func hasOneUnconditionalPosterRepair(src string, constructor int) bool {
	const declaration = "publicationSteps := []string{"
	steps := strings.Index(src, declaration)
	if steps < 0 || steps >= constructor {
		return false
	}
	literalStart := steps + len(declaration)
	literalEnd := strings.Index(src[literalStart:constructor], "}")
	if literalEnd < 0 {
		return false
	}
	literal := src[literalStart : literalStart+literalEnd]
	const token = `"poster_repair"`
	if strings.Count(literal, token) != 1 || !strings.Contains(literal, `"poster", "poster_repair"`) {
		return false
	}
	return !strings.Contains(src[literalStart+literalEnd+1:constructor], token)
}

func TestPublicationStepsPosterRepairMustBeUniqueAndUnconditional(t *testing.T) {
	const constructor = "publicationCapabilities := publication.NewCapabilityMatrix(publicationSteps)"
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{name: "valid", src: `publicationSteps := []string{"poster", "poster_repair", "thumbnail"}
	if coreiface.IngestPreparePlannerHandle() != nil {
		publicationSteps = append(publicationSteps, "prepare")
	}
	` + constructor, want: true},
		{name: "conditional append", src: `publicationSteps := []string{"poster", "thumbnail"}
	if coreiface.IngestPreparePlannerHandle() != nil {
		publicationSteps = append(publicationSteps, "poster_repair", "prepare")
	}
	` + constructor, want: false},
		{name: "duplicate literal", src: `publicationSteps := []string{"poster", "poster_repair", "poster_repair", "thumbnail"}
	if coreiface.IngestPreparePlannerHandle() != nil {
		publicationSteps = append(publicationSteps, "prepare")
	}
	` + constructor, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hasOneUnconditionalPosterRepair(tc.src, strings.Index(tc.src, constructor))
			if got != tc.want {
				t.Fatalf("hasOneUnconditionalPosterRepair()=%v want %v", got, tc.want)
			}
		})
	}
}
