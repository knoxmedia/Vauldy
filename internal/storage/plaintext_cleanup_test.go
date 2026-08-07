package storage

import (
	"os"
	"strings"
	"testing"
	"time"

	"knox-media/internal/store"
)

func TestPlaintextConsumersBusyKeyframeWaiting(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', '/x')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (7, 1, 'f', 't', '/x/a.mp4', 'video', 'active')`)
	if _, err := db.Exec(`INSERT INTO keyframe_task (media_id, status) VALUES (7, 'waiting')`); err != nil {
		t.Fatal(err)
	}
	if plaintextConsumersBusy(db, 7) {
		t.Fatal("waiting keyframe must not block encrypt")
	}
	_, _ = db.Exec(`UPDATE keyframe_task SET status='running' WHERE media_id=7`)
	if !plaintextConsumersBusy(db, 7) {
		t.Fatal("expected busy for running keyframe")
	}
	_, _ = db.Exec(`UPDATE keyframe_task SET status='done' WHERE media_id=7`)
	if plaintextConsumersBusy(db, 7) {
		t.Fatal("expected idle after keyframe done")
	}
}

func TestPlaintextConsumersBusyJITHook(t *testing.T) {
	SetMediaPlaintextBusy(func(mediaID int64) bool { return mediaID == 42 })
	defer SetMediaPlaintextBusy(nil)

	db, _ := store.OpenSQLite(":memory:")
	defer db.Close()
	if !plaintextConsumersBusy(db, 42) {
		t.Fatal("expected busy from JIT hook")
	}
}

func TestWaitForPlaintextConsumersReturnsWhenIdle(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	start := time.Now()
	WaitForPlaintextConsumers(db, 1, 5*time.Second)
	if time.Since(start) > 2*time.Second {
		t.Fatal("expected immediate return when no consumers")
	}
}

func TestPlaintextCleanupRetainsOnlyActiveConsumerPredicate(t *testing.T) {
	data, err := os.ReadFile("plaintext_cleanup.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, banned := range []string{
		"func KickPendingPlaintextCleanups",
		"func schedulePlaintextCleanup",
		"func cleanupPlaintextAfterEncrypt",
		"func removePlaintextFile",
	} {
		if strings.Contains(src, banned) {
			t.Fatalf("plaintext_cleanup still exports scheduling/deletion helper %q", banned)
		}
	}
	if !strings.Contains(src, "func HasActivePlaintextConsumer") {
		t.Fatal("Must retain HasActivePlaintextConsumer for retirement barrier")
	}
	if !strings.Contains(src, "func SetMediaPlaintextBusy") {
		t.Fatal("Must retain SetMediaPlaintextBusy for JIT callback")
	}
}

func TestHasActivePlaintextConsumerExported(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if HasActivePlaintextConsumer(db, 1) {
		t.Fatal("expected idle")
	}
}
