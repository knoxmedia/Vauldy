package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"knox-media/internal/discovery"
)

func TestIdentityContracts(t *testing.T) {
	if SourceFilesystemEvent != "filesystem_event" || SourceUpload != "upload" || SourceScan != "scan" {
		t.Fatalf("unexpected sources: %q %q %q", SourceFilesystemEvent, SourceUpload, SourceScan)
	}
	fp := Fingerprint{SHA256: "abc", Size: 12, ModTimeNS: 34}
	c := Candidate{Source: SourceScan, LibraryID: 7, Path: "p", PathKey: "k", UploadID: "u", ExpectedFingerprint: &fp}
	if c.LibraryID != 7 || c.ExpectedFingerprint != &fp {
		t.Fatalf("candidate contract: %+v", c)
	}
	r := PlanResult{ItemID: 1, MediaID: 2, RunID: 3, Generation: 4, Duplicate: true}
	if r.Generation != 4 || !r.Duplicate {
		t.Fatalf("plan result contract: %+v", r)
	}
}

func TestIdentityNormalizesRawNonexistentFilesystemCandidateLexically(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "gone", "..", "renamed.mkv")
	c, err := NormalizeFilesystemCandidate(42, raw)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(raw)
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Clean(want)
	if c.Source != SourceFilesystemEvent || c.LibraryID != 42 || c.Path != want || c.PathKey == "" {
		t.Fatalf("candidate = %+v, want path %q", c, want)
	}
	if _, err := os.Stat(raw); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test requires nonexistent path: %v", err)
	}
}

func TestIdentityResolvesLongestContainingRootAndRejectsSiblingPrefix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Media")
	nested := filepath.Join(root, "Movies")
	file := filepath.Join(nested, "film.mkv")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("film"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveFilesystemPath(file, []string{root, nested})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Root != nested {
		t.Fatalf("root = %q, want %q", resolved.Root, nested)
	}
	sibling := filepath.Join(base, "Media2", "film.mkv")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveFilesystemPath(sibling, []string{root}); !errors.Is(err, ErrOutsideLibraryRoots) {
		t.Fatalf("sibling error = %v", err)
	}
}

func TestIdentityRejectsExistingSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "secret.mkv")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escaped.mkv")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS != "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		junction := filepath.Join(root, "escaped")
		if mklinkErr := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).Run(); mklinkErr != nil {
			t.Skipf("symlink and junction unavailable: symlink=%v junction=%v", err, mklinkErr)
		}
		link = filepath.Join(junction, filepath.Base(target))
	}
	if _, err := ResolveFilesystemPath(link, []string{root}); !errors.Is(err, ErrOutsideLibraryRoots) {
		t.Fatalf("escape error = %v", err)
	}
}

func TestIdentityFingerprintIsDeterministicContentEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := FingerprintFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fp.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || fp.Size != 3 || fp.ModTimeNS == 0 {
		t.Fatalf("fingerprint = %+v", fp)
	}
	other := filepath.Join(t.TempDir(), "copy.bin")
	if err := os.WriteFile(other, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherFP, err := FingerprintFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if fp.SHA256 != otherFP.SHA256 {
		t.Fatalf("same bytes differ: %+v %+v", fp, otherFP)
	}
	c1, _ := NormalizeFilesystemCandidate(1, path)
	c2, _ := NormalizeFilesystemCandidate(1, other)
	if c1.PathKey == c2.PathKey {
		t.Fatal("distinct paths unexpectedly share path key")
	}
}

func TestIdentityFingerprintReportsRetryableUnstableSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changing.bin")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := false
	hooks := identityHooks{read: func(f *os.File, p []byte) (int, error) {
		n, err := f.Read(p)
		if !changed {
			changed = true
			if writeErr := os.WriteFile(path, []byte("after-change"), 0o644); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return n, err
	}}
	_, err := fingerprintFileContextWithHooks(context.Background(), path, hooks)
	var retryable interface{ Retryable() bool }
	if !errors.As(err, &retryable) || !retryable.Retryable() || !errors.Is(err, ErrUnstableSource) {
		t.Fatalf("error = %v, want retryable unstable", err)
	}
}

func TestIdentityFingerprintContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FingerprintFileContext(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestIdentityRejectsDirectoryAsPermanent(t *testing.T) {
	_, err := FingerprintFile(t.TempDir())
	if !errors.Is(err, ErrNonRegularSource) {
		t.Fatalf("error = %v, want non-regular source", err)
	}
	if ClassifyIdentityError(err) != ErrorPermanent {
		t.Fatalf("class = %v, want permanent", ClassifyIdentityError(err))
	}
}

func TestIdentityClassifiesTransientAndPermanentErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "temporarily-missing.mkv")
	_, err := FingerprintFile(missing)
	if ClassifyIdentityError(err) != ErrorRetryable || !IsRetryable(err) {
		t.Fatalf("missing class = %v, err = %v", ClassifyIdentityError(err), err)
	}
	if ClassifyIdentityError(ErrOutsideLibraryRoots) != ErrorPermanent || IsRetryable(ErrOutsideLibraryRoots) {
		t.Fatal("outside-root must be permanent")
	}
	if ClassifyIdentityError(filepath.ErrBadPattern) != ErrorPermanent {
		t.Fatal("malformed path must be permanent")
	}
}

func TestIdentityRejectsSameSizeMTimePathReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.bin")
	replacement := filepath.Join(dir, "replacement.bin")
	if err := os.WriteFile(path, []byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("BBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	hooks := identityHooks{beforeFinalValidation: func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Rename(replacement, path)
	}}
	_, err = fingerprintFileContextWithHooks(context.Background(), path, hooks)
	if !errors.Is(err, ErrUnstableSource) || !IsRetryable(err) {
		t.Fatalf("error = %v, want retryable unstable source", err)
	}
}

func TestIdentityResolveRevalidatesOpenedHandleAfterPathReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "media.bin")
	replacement := filepath.Join(root, "replacement.bin")
	if err := os.WriteFile(path, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("swapped"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooks := identityHooks{beforeFinalValidation: func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Rename(replacement, path)
	}}
	_, err := resolveFilesystemPathContextWithHooks(context.Background(), path, []string{root}, hooks)
	if !errors.Is(err, ErrUnstableSource) || !IsRetryable(err) {
		t.Fatalf("error = %v, want retryable unstable source", err)
	}
}

func TestIdentityResolveContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveFilesystemPathContext(ctx, filepath.Join(t.TempDir(), "media.bin"), []string{t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestIdentityWindowsTransientLockClassification(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows error codes")
	}
	for _, err := range []error{syscall.Errno(32), syscall.Errno(33), syscall.Errno(2), syscall.Errno(3)} {
		if ClassifyIdentityError(err) != ErrorRetryable || !IsRetryable(err) {
			t.Fatalf("error %v classified permanent", err)
		}
	}
	if err := syscall.Errno(5); ClassifyIdentityError(err) != ErrorPermanent || IsRetryable(err) {
		t.Fatalf("access denied classified retryable: %v", err)
	}
}

func TestIdentityRejectsJunctionRetargetDuringHash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junction regression")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	insideA := filepath.Join(root, "inside-a")
	insideB := filepath.Join(root, "inside-b")
	junction := filepath.Join(root, "current")
	for _, dir := range []string{insideA, insideB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ dir, bytes string }{{insideA, "AAAA"}, {insideB, "BBBB"}} {
		if err := os.WriteFile(filepath.Join(item.dir, "media.bin"), []byte(item.bytes), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("cmd", "/c", "mklink", "/J", junction, insideA).Run(); err != nil {
		t.Skipf("junction unavailable; cannot exercise reparse retarget fallback: %v", err)
	}
	path := filepath.Join(junction, "media.bin")
	hooks := identityHooks{beforeFinalValidation: func() error {
		if err := os.Remove(junction); err != nil {
			return err
		}
		return exec.Command("cmd", "/c", "mklink", "/J", junction, insideB).Run()
	}}
	_, err := resolveFilesystemPathContextWithHooks(context.Background(), path, []string{root}, hooks)
	if !errors.Is(err, ErrUnstableSource) || !IsRetryable(err) {
		t.Fatalf("error = %v, want retryable unstable junction retarget", err)
	}
}

func TestIdentityFingerprintContextCancelsActiveHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hooks := identityHooks{read: func(f *os.File, p []byte) (int, error) {
		n, err := f.Read(p)
		cancel()
		return n, err
	}}
	_, err := fingerprintFileContextWithHooks(ctx, path, hooks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want active-hash cancellation", err)
	}
}

func TestIdentityMalformedPathIsPermanent(t *testing.T) {
	_, err := NormalizeFilesystemCandidate(1, "bad\x00path")
	if err == nil || ClassifyIdentityError(err) != ErrorPermanent || IsRetryable(err) {
		t.Fatalf("error = %v, want permanent malformed path", err)
	}
}

func TestIdentityRejectsPostHashSameHandleMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(path, []byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	hooks := identityHooks{beforeFinalValidation: func() error {
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if openErr != nil {
			return openErr
		}
		if _, writeErr := f.Write([]byte("BBBB")); writeErr != nil {
			f.Close()
			return writeErr
		}
		if closeErr := f.Close(); closeErr != nil {
			return closeErr
		}
		return os.Chtimes(path, before.ModTime().Add(time.Second), before.ModTime().Add(time.Second))
	}}
	_, err = fingerprintFileContextWithHooks(context.Background(), path, hooks)
	if !errors.Is(err, ErrUnstableSource) || !IsRetryable(err) {
		t.Fatalf("error = %v, want retryable post-hash mutation", err)
	}
}

func TestIdentityHandleResolutionUnsupportedIsPermanent(t *testing.T) {
	err := fmt.Errorf("fd path unavailable: %w", discovery.ErrHandleResolutionUnsupported)
	if ClassifyIdentityError(err) != ErrorPermanent || IsRetryable(err) {
		t.Fatalf("unsupported handle resolution classified retryable: %v", err)
	}
	if !errors.Is(err, discovery.ErrHandleResolutionUnsupported) {
		t.Fatalf("errors.Is lost sentinel: %v", err)
	}
}
