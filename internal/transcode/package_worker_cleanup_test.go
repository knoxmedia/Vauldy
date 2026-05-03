package transcode

import (
	"path/filepath"
	"testing"
)

func TestCleanupRunsOnlyForUploadLocalPath(t *testing.T) {
	t.Parallel()
	base := filepath.Clean(`E:\uploads`)

	inUpload := filepath.Join(base, "movies", "a.mp4")
	if !shouldCleanup(base, inUpload) {
		t.Fatalf("expected cleanup allowed for upload path")
	}

	outside := filepath.Clean(`E:\external\movies\a.mp4`)
	if shouldCleanup(base, outside) {
		t.Fatalf("expected cleanup denied for external path")
	}
}
