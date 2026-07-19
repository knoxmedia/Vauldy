package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"knox-media/api/handler"
	"knox-media/internal/app"
	"knox-media/internal/config"
)

func TestNewEngineRequiresExplicitServerLifecycle(t *testing.T) {
	cfg := &config.Config{}
	application := &app.App{Config: cfg}
	defer func() {
		if recover() == nil {
			t.Fatal("NewEngine accepted missing ServerContext/Background")
		}
	}()
	NewEngine(cfg, application, handler.Dependencies{})
}

func TestRouterBackgroundLoopsUseInjectedLifecycle(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if strings.Contains(src, "context.Background()") {
		t.Fatal("router contains detached background context")
	}
	for _, forbidden := range []string{"go h.StartScheduleLoop", "go h.StartScrapeTaskLoop", "go h.StartTranscodeTaskLoop", "go h.StartLyricTaskLoop"} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("router directly starts loop %q", forbidden)
		}
	}
	if !strings.Contains(src, "deps.Background.Go(deps.ServerContext") {
		t.Fatal("router does not register loops with shared lifecycle")
	}
	_ = context.Background()
}
