package main

import (
	"os"
	"strings"
	"testing"

	"knox-media/internal/config"
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
		"// (3) Shared post-ingest queue, enqueuer, six adapters, and dispatcher.",
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
	for _, required := range []string{"postingest.AdapterSet{", "postingest.NewPosterAdapter(", "postingest.NewPreviewAdapter(", "postingest.NewKeyframeAdapter(", "postingest.NewSubtitleAdapter(", "postingest.NewAtrackAdapter(", "postingest.NewEncryptAdapter(", "scancoord.New(", "monitor.NewService(db, coordinator", "api.NewEngine(cfg, application, deps)"} {
		if !strings.Contains(src, required) {
			t.Fatalf("main missing shared assembly %q", required)
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
