package publication

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCancelRunTxReleasesPostIngestPlaintextTemp(t *testing.T) {
	db := completionTestDB(t)
	runID, _, _, _, _ := seedLifecycleGraph(t, db)

	root := filepath.Join(t.TempDir(), ".task-plaintext")
	previewDir := filepath.Join(root, "1", "1", "112")
	aiDir := filepath.Join(root, "1", "1", "113")
	for _, dir := range []string{previewDir, aiDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	SetPostIngestTempRelease(func(mediaID, generation, taskID int64) {
		_ = os.RemoveAll(filepath.Join(root,
			strconv.FormatInt(mediaID, 10),
			strconv.FormatInt(generation, 10),
			strconv.FormatInt(taskID, 10)))
	})
	t.Cleanup(func() { SetPostIngestTempRelease(nil) })

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ok, err := CancelRunTx(context.Background(), tx, runID, "operator_cancel")
	if err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{previewDir, aiDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected orphan temp removed at %s: %v", dir, err)
		}
	}
}
