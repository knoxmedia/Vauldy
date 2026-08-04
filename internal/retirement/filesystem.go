package retirement

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"knox-media/internal/storage"
)

// FileOps abstracts rename/remove/fsync for crash-seam tests.
type FileOps struct {
	SyncFile  func(*os.File) error
	SyncDir   func(string) error
	Remove    func(string) error
	Rename    func(oldpath, newpath string) error
	AfterMove func() error
}

func defaultFileOps() FileOps {
	return FileOps{
		SyncFile: func(f *os.File) error { return f.Sync() },
		SyncDir:  syncDir,
		Remove:   os.Remove,
		Rename:   os.Rename,
	}
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	err = f.Sync()
	if runtime.GOOS == "windows" && (errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.Errno(5))) {
		return nil
	}
	return err
}

func syncParents(ops FileOps, paths ...string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		d := filepath.Dir(p)
		if seen[d] {
			continue
		}
		seen[d] = true
		fn := ops.SyncDir
		if fn == nil {
			fn = syncDir
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return nil
}

// QuarantinePath builds the durable quarantine identity path.
// Layout: {root}/{mediaID}/{generation}/r{retirementID}/rr{retry}/a{attempt}/source
func QuarantinePath(root string, id Identity) (string, error) {
	if strings.TrimSpace(root) == "" || id.MediaID <= 0 || id.Generation <= 0 || id.RetirementID <= 0 || id.Attempt <= 0 {
		return "", ErrInvalidIdentity
	}
	resolved, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved,
		fmt.Sprintf("%d", id.MediaID),
		fmt.Sprintf("%d", id.Generation),
		fmt.Sprintf("r%d", id.RetirementID),
		fmt.Sprintf("rr%d", id.RetryRound),
		fmt.Sprintf("a%d", id.Attempt),
		"source"), nil
}

// ValidateQuarantineLayout ensures path matches the reserved identity under root
// without requiring the file to still exist (post-delete verify resume).
func ValidateQuarantineLayout(root, path string, id Identity) error {
	want, err := QuarantinePath(root, id)
	if err != nil {
		return err
	}
	got, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !samePath(got, want) {
		return fmt.Errorf("%w: got=%s want=%s", ErrUnsafeQuarantinePath, got, want)
	}
	return nil
}

// ValidateQuarantinePath ensures path matches the reserved identity layout under root.
func ValidateQuarantinePath(root, path string, id Identity) error {
	if err := ValidateQuarantineLayout(root, path, id); err != nil {
		return err
	}
	got, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(got)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrUnsafeQuarantinePath)
	}
	return nil
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func isCrossDevice(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EXDEV) {
		return true
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		if errors.Is(linkErr.Err, syscall.EXDEV) {
			return true
		}
		if runtime.GOOS == "windows" && errors.Is(linkErr.Err, syscall.Errno(17)) {
			return true
		}
	}
	if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(17)) {
		return true
	}
	return false
}

// ResolveQuarantineRoot picks a same-volume quarantine root for the source.
func ResolveQuarantineRoot(sourcePath, preferredRoot string) string {
	local := filepath.Join(filepath.Dir(sourcePath), ".quarantine", "retirement")
	if strings.TrimSpace(preferredRoot) == "" {
		return local
	}
	return storage.QuarantineRootForSource(sourcePath, preferredRoot)
}

// MoveToQuarantine renames (or same-volume copy-fallback refused for EXDEV) source into the reserved path.
func MoveToQuarantine(source, root string, id Identity, ops FileOps) (quarantinePath, quarantineFingerprint string, err error) {
	if ops.Rename == nil {
		ops = defaultFileOps()
	}
	target, err := QuarantinePath(root, id)
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", errors.New("unsafe retirement quarantine source")
	}
	rename := ops.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err = rename(source, target); err != nil {
		if isCrossDevice(err) {
			return "", "", fmt.Errorf("retirement quarantine refuses cross-volume plaintext copy: %w", err)
		}
		return "", "", err
	}
	_ = os.Chmod(target, 0600)
	syncFile := ops.SyncFile
	if syncFile == nil {
		syncFile = func(f *os.File) error { return f.Sync() }
	}
	f, e := os.OpenFile(target, os.O_RDWR, 0)
	if e == nil {
		e = syncFile(f)
		e = errors.Join(e, f.Close())
	}
	if e != nil {
		return target, "", e
	}
	if e = syncParents(ops, source, target); e != nil {
		return target, "", e
	}
	if ops.AfterMove != nil {
		if e = ops.AfterMove(); e != nil {
			return target, "", e
		}
	}
	fp, e := fingerprintFile(target)
	if e != nil {
		return target, "", e
	}
	return target, fp, nil
}

func fingerprintFile(path string) (string, error) {
	return storage.EncryptionSourceFingerprint(path)
}

// DeleteQuarantine removes the quarantined file after path/fingerprint validation.
// Empty expectedFingerprint never authorizes delete (fail closed).
func DeleteQuarantine(root, path, expectedFingerprint string, id Identity, ops FileOps) error {
	if ops.Remove == nil {
		ops = defaultFileOps()
	}
	if strings.TrimSpace(expectedFingerprint) == "" {
		return fmt.Errorf("%w: missing expected fingerprint", ErrUnsafeQuarantinePath)
	}
	if err := ValidateQuarantinePath(root, path, id); err != nil {
		return err
	}
	got, err := fingerprintFile(path)
	if err != nil {
		return err
	}
	if got != expectedFingerprint {
		return ErrFingerprintMismatch
	}
	remove := ops.Remove
	if remove == nil {
		remove = os.Remove
	}
	if err := remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncParents(ops, path)
}

// VerifyAbsent confirms source and quarantine paths are gone.
func VerifyAbsent(sourcePath, quarantinePath string) error {
	for _, p := range []string{sourcePath, quarantinePath} {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%w: still present %s", ErrVerificationFailed, p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RestoreQuarantine moves quarantined bytes back to source (crash before state commit).
func RestoreQuarantine(quarantinePath, sourcePath, root string, id Identity, ops FileOps) error {
	if ops.Rename == nil {
		ops = defaultFileOps()
	}
	if err := ValidateQuarantinePath(root, quarantinePath, id); err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err == nil {
		return errors.New("restore target already exists")
	}
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0755); err != nil {
		return err
	}
	rename := ops.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(quarantinePath, sourcePath); err != nil {
		return err
	}
	if ops.AfterMove != nil {
		if err := ops.AfterMove(); err != nil {
			return err
		}
	}
	return syncParents(ops, quarantinePath, sourcePath)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
