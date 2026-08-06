package retirement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/storage"
)

func TestFilesystemQuarantineIdentityAndFingerprint(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "a.bin")
	writeFile(t, src, []byte("payload-bytes"))
	fp, err := storage.EncryptionSourceFingerprint(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = fp
	qRoot := filepath.Join(root, "q")
	id := Identity{MediaID: 7, Generation: 2, RetirementID: 99, RetryRound: 1, Attempt: 3}
	path, err := QuarantinePath(qRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, string(filepath.Separator)+"r99"+string(filepath.Separator)) {
		t.Fatalf("path=%s", path)
	}
	writeFile(t, path, []byte("payload-bytes"))
	if err = ValidateQuarantinePath(qRoot, path, id); err != nil {
		t.Fatal(err)
	}
	if err = ValidateQuarantinePath(qRoot, filepath.Join(qRoot, "evil"), id); err == nil {
		t.Fatal("expected unsafe path")
	}
	_ = os.Remove(path)

	qPath, qFP, err := MoveToQuarantine(context.Background(), src, qRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}
	if _, err = os.Stat(qPath); err != nil {
		t.Fatal(err)
	}
	if qFP == "" {
		t.Fatal("missing quarantine fingerprint")
	}
}

func TestFilesystemRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.bin")
	writeFile(t, real, []byte("x"))
	link := filepath.Join(root, "link.bin")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlink not supported:", err)
	}
	id := Identity{MediaID: 1, Generation: 1, RetirementID: 1, Attempt: 1}
	_, _, err := MoveToQuarantine(context.Background(), link, filepath.Join(root, "q"), id, FileOps{})
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestFilesystemCrashSeamsAroundMoveDeleteVerify(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "movie.mp4")
	writeFile(t, src, []byte("crash-seam-body"))
	qRoot := filepath.Join(root, "q")
	id := Identity{MediaID: 1, Generation: 1, RetirementID: 1, Attempt: 1}
	boom := errors.New("injected crash")

	ops := defaultFileOps()
	ops.AfterMove = func() error { return boom }
	qPath, qFP, err := MoveToQuarantine(context.Background(), src, qRoot, id, ops)
	if !errors.Is(err, boom) {
		t.Fatalf("after move: %v", err)
	}
	if _, err = os.Stat(qPath); err != nil {
		t.Fatal(err)
	}

	err = DeleteQuarantine(context.Background(), qRoot, qPath, qFP, id, FileOps{})
	if err != nil {
		// AfterMove crash returns empty fingerprint; compute from file for cleanup assertion.
		if qFP == "" && pathExists(qPath) {
			qFP, err = fingerprintFile(qPath)
			if err != nil {
				t.Fatal(err)
			}
			err = DeleteQuarantine(context.Background(), qRoot, qPath, qFP, id, FileOps{})
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyAbsent(src, qPath); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemFingerprintMismatchBlocksDelete(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.bin")
	writeFile(t, src, []byte("one"))
	qRoot := filepath.Join(root, "q")
	id := Identity{MediaID: 1, Generation: 1, RetirementID: 1, Attempt: 1}
	qPath, _, err := MoveToQuarantine(context.Background(), src, qRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if err = DeleteQuarantine(context.Background(), qRoot, qPath, "wrong-fp", id, FileOps{}); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestFilesystemEmptyExpectedFingerprintBlocksDelete(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.bin")
	writeFile(t, src, []byte("one"))
	qRoot := filepath.Join(root, "q")
	id := Identity{MediaID: 1, Generation: 1, RetirementID: 1, Attempt: 1}
	qPath, _, err := MoveToQuarantine(context.Background(), src, qRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if err = DeleteQuarantine(context.Background(), qRoot, qPath, "", id, FileOps{}); !errors.Is(err, ErrUnsafeQuarantinePath) {
		t.Fatalf("empty fingerprint must not authorize delete: %v", err)
	}
	if _, err = os.Stat(qPath); err != nil {
		t.Fatal("quarantine must remain intact")
	}
}
