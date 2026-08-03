package postingest

import (
	"context"
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
)

type EncryptionStageRootResolver interface {
	ResolveEncryptionStageRoot(context.Context, int64, string) (string, error)
}

type EncryptionRecoveryRoots struct {
	Quarantine string
	Resolver   EncryptionStageRootResolver
}

func quarantinePath(root string, mediaID, generation int64, stageID string) (string, error) {
	if strings.TrimSpace(root) == "" || mediaID <= 0 || generation <= 0 || strings.TrimSpace(stageID) == "" {
		return "", errors.New("encryption quarantine identity invalid")
	}
	resolved, err := resolvedQuarantineRoot(root, true)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID, "source"), nil
}

type encryptionFileOps struct {
	syncFile  func(*os.File) error
	syncDir   func(string) error
	remove    func(string) error
	rename    func(oldpath, newpath string) error
	afterMove func() error
}

func defaultEncryptionFileOps() encryptionFileOps {
	return encryptionFileOps{syncFile: func(f *os.File) error { return f.Sync() }, syncDir: syncEncryptionDir, remove: os.Remove, rename: os.Rename}
}

// encryptionFileOpsForCleanup is the post-commit plaintext cleanup seam. Tests
// replace it to inject cleanup failures without failing encryption completion.
var encryptionFileOpsForCleanup = defaultEncryptionFileOps

func encryptionRename(ops encryptionFileOps, oldpath, newpath string) error {
	if ops.rename != nil {
		return ops.rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func isCrossDeviceRename(err error) bool {
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
		// Windows ERROR_NOT_SAME_DEVICE == 17 (same numeric value as EXDEV).
		if runtime.GOOS == "windows" && errors.Is(linkErr.Err, syscall.Errno(17)) {
			return true
		}
	}
	if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(17)) {
		return true
	}
	return false
}
func syncEncryptionDir(path string) error {
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
func syncEncryptionParents(ops encryptionFileOps, paths ...string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		d := filepath.Dir(p)
		if !seen[d] {
			seen[d] = true
			if err := ops.syncDir(d); err != nil {
				return err
			}
		}
	}
	return nil
}
func quarantinePlaintext(source, root string, mediaID, generation int64, stageID string) (string, error) {
	return quarantinePlaintextWithOps(source, root, mediaID, generation, stageID, defaultEncryptionFileOps())
}
func quarantinePlaintextWithOps(source, root string, mediaID, generation int64, stageID string, ops encryptionFileOps) (string, error) {
	target, err := ensureQuarantineParent(root, mediaID, generation, stageID)
	if err != nil {
		return "", err
	}
	sourceInfo, err := encryptionLstat(source)
	if err != nil || sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return "", errors.New("unsafe encryption quarantine source")
	}
	_ = os.Chmod(filepath.Dir(target), 0700)
	if err = encryptionRename(ops, source, target); err == nil {
		_ = os.Chmod(target, 0600)
		f, e := os.OpenFile(target, os.O_RDWR, 0)
		if e == nil {
			e = ops.syncFile(f)
			e = errors.Join(e, f.Close())
		}
		if e != nil {
			return target, e
		}
		if e = syncEncryptionParents(ops, source, target); e != nil {
			return target, e
		}
		return target, nil
	}
	if isCrossDeviceRename(err) {
		return "", fmt.Errorf("encryption quarantine refuses cross-volume plaintext copy: %w", err)
	}
	tmp := target + ".tmp"
	src, e := os.Open(source)
	if e != nil {
		return "", e
	}
	defer src.Close()
	dst, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(dst, h), src)
	syncErr := ops.syncFile(dst)
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return "", errors.Join(copyErr, syncErr, closeErr)
	}
	before, err := fileSHA256(source)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if before != hex.EncodeToString(h.Sum(nil)) {
		_ = os.Remove(tmp)
		return "", errors.New("quarantine copy hash mismatch")
	}
	if err = os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err = syncEncryptionParents(ops, tmp, target); err != nil {
		return target, err
	}
	if err = os.Remove(source); err != nil {
		return target, err
	}
	if err = syncEncryptionParents(ops, source); err != nil {
		return target, err
	}
	return target, nil
}
func restoreQuarantinedPlaintext(quarantine, source, root string, mediaID, generation int64, stageID string) error {
	return restoreQuarantinedPlaintextWithOps(quarantine, source, root, mediaID, generation, stageID, defaultEncryptionFileOps())
}
func restoreQuarantinedPlaintextWithOps(quarantine, source, root string, mediaID, generation int64, stageID string, ops encryptionFileOps) error {
	qAbs, err := validateExistingQuarantinePath(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		return err
	}
	if _, err = os.Stat(source); err == nil {
		return errors.New("restore target already exists")
	}
	if err = os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		return err
	}
	if err = encryptionRename(ops, qAbs, source); err == nil {
		if ops.afterMove != nil {
			if err = ops.afterMove(); err != nil {
				return err
			}
		}
		return syncEncryptionParents(ops, qAbs, source)
	}
	src, e := os.Open(qAbs)
	if e != nil {
		return e
	}
	defer src.Close()
	tmp := source + ".restore"
	dst, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(dst, src)
	syncErr := ops.syncFile(dst)
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return errors.Join(copyErr, syncErr, closeErr)
	}
	if err = os.Rename(tmp, source); err != nil {
		return err
	}
	if ops.afterMove != nil {
		if err = ops.afterMove(); err != nil {
			return err
		}
	}
	if err = syncEncryptionParents(ops, tmp, source); err != nil {
		return err
	}
	if err = os.Remove(qAbs); err != nil {
		return err
	}
	return syncEncryptionParents(ops, qAbs)
}

var encryptionLstat = os.Lstat

var errUnsafeEncryptionQuarantinePath = errors.New("unsafe encryption quarantine path")

func unsafeEncryptionQuarantinePath() error { return errUnsafeEncryptionQuarantinePath }

func sameQuarantinePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func resolvedQuarantineRoot(root string, create bool) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", unsafeEncryptionQuarantinePath()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if create {
		if err = os.MkdirAll(abs, 0700); err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := encryptionLstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", unsafeEncryptionQuarantinePath()
	}
	return resolved, nil
}

func validateQuarantineIdentity(root, quarantine string, mediaID, generation int64, stageID string) (string, string, error) {
	if !safeEncryptionStageID(stageID) {
		return "", "", unsafeEncryptionQuarantinePath()
	}
	rootResolved, err := resolvedQuarantineRoot(root, false)
	if err != nil {
		return "", "", err
	}
	expected := filepath.Join(rootResolved, fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID, "source")
	qAbs, err := filepath.Abs(quarantine)
	if err != nil || !sameQuarantinePath(qAbs, expected) {
		return "", "", unsafeEncryptionQuarantinePath()
	}
	return rootResolved, expected, nil
}

type quarantineLeafState uint8

const (
	quarantineLeafExists quarantineLeafState = iota
	quarantineLeafMissing
)

// validateQuarantineParentLayout validates the exact journal identity and every
// parent component independently from whether the final plaintext leaf exists.
func validateQuarantineParentLayout(root, quarantine string, mediaID, generation int64, stageID string) (string, quarantineLeafState, error) {
	rootResolved, expected, err := validateQuarantineIdentity(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		return "", 0, err
	}
	current := rootResolved
	for _, component := range []string{fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID} {
		current = filepath.Join(current, component)
		info, statErr := encryptionLstat(current)
		if statErr != nil {
			return "", 0, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", 0, unsafeEncryptionQuarantinePath()
		}
	}
	info, statErr := encryptionLstat(expected)
	if os.IsNotExist(statErr) {
		return expected, quarantineLeafMissing, nil
	}
	if statErr != nil {
		return "", 0, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, unsafeEncryptionQuarantinePath()
	}
	resolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return "", 0, err
	}
	if !sameQuarantinePath(resolved, expected) || !pathWithinRoot(rootResolved, resolved) {
		return "", 0, unsafeEncryptionQuarantinePath()
	}
	return expected, quarantineLeafExists, nil
}

func validateExistingQuarantinePath(root, quarantine string, mediaID, generation int64, stageID string) (string, error) {
	expected, state, err := validateQuarantineParentLayout(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		return "", err
	}
	if state != quarantineLeafExists {
		return "", os.ErrNotExist
	}
	return expected, nil
}

func ensureQuarantineParent(root string, mediaID, generation int64, stageID string) (string, error) {
	if !safeEncryptionStageID(stageID) {
		return "", unsafeEncryptionQuarantinePath()
	}
	rootResolved, err := resolvedQuarantineRoot(root, true)
	if err != nil {
		return "", err
	}
	current := rootResolved
	for _, component := range []string{fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID} {
		current = filepath.Join(current, component)
		if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, statErr := encryptionLstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", unsafeEncryptionQuarantinePath()
		}
	}
	return filepath.Join(current, "source"), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
