package doccover

import (
	"errors"
	"testing"
)

func TestCoverRetryBlocked(t *testing.T) {
	dir := t.TempDir()
	preview := dir
	id := int64(3361)
	if CoverRetryBlocked(preview, id) {
		t.Fatal("expected not blocked initially")
	}
	MarkCoverFailed(preview, id, errors.New("libreoffice jpg: no output"))
	if !CoverRetryBlocked(preview, id) {
		t.Fatal("expected blocked after failure")
	}
}

func TestNeedsCoverWorkRespectsSkip(t *testing.T) {
	dir := t.TempDir()
	preview := dir
	id := int64(99)
	MarkCoverFailed(preview, id, errors.New("failed"))
	if NeedsCoverWork(nil, preview, dir, id, 0) {
		t.Fatal("expected no work when retry blocked")
	}
}
