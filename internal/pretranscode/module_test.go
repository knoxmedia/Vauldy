package pretranscode

import (
	"context"
	"testing"

	"knox-media/internal/coreiface"
)

func TestModuleShutdownClearsOnlyOwnedGlobalHandles(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := coreiface.ModuleDeps{DB: db, TranscodeDir: t.TempDir(), FFmpegPath: "ffmpeg"}
	m1 := NewModule()
	if err := m1.Init(ctx, deps); err != nil {
		t.Fatal(err)
	}
	m2 := NewModule()
	if err := m2.Init(ctx, deps); err != nil {
		t.Fatal(err)
	}
	if ActiveModule() != m2 || coreiface.PretranscodeModuleHandle() != m2.Playback || CurrentWebhookDispatcher() != m2.dispatcher {
		t.Fatal("second module does not own handles")
	}
	if err := m1.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ActiveModule() != m2 || coreiface.PretranscodeModuleHandle() != m2.Playback || CurrentWebhookDispatcher() != m2.dispatcher {
		t.Fatal("old module cleared new owner handles")
	}
	if err := m2.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ActiveModule() != nil || coreiface.PretranscodeModuleHandle() != nil || CurrentWebhookDispatcher() != nil {
		t.Fatal("owning module did not clear handles")
	}
}
