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
	syncFile func(*os.File) error
	syncDir  func(string) error
	remove   func(string) error
}

func defaultEncryptionFileOps() encryptionFileOps {
	return encryptionFileOps{syncFile: func(f *os.File) error { return f.Sync() }, syncDir: syncEncryptionDir, remove: os.Remove}
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
	if err = os.Rename(source, target); err == nil {
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
	if err = os.Rename(qAbs, source); err == nil {
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
	if err = syncEncryptionParents(ops, tmp, source); err != nil {
		return err
	}
	if err = os.Remove(qAbs); err != nil {
		return err
	}
	return syncEncryptionParents(ops, qAbs)
}

var encryptionLstat = os.Lstat

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
		return "", errors.New("unsafe encryption quarantine path")
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
		return "", errors.New("unsafe encryption quarantine path")
	}
	info, err := encryptionLstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("unsafe encryption quarantine path")
	}
	return resolved, nil
}

func validateQuarantineIdentity(root, quarantine string, mediaID, generation int64, stageID string) (string, string, error) {
	if !safeEncryptionStageID(stageID) {
		return "", "", errors.New("unsafe encryption quarantine path")
	}
	rootResolved, err := resolvedQuarantineRoot(root, false)
	if err != nil {
		return "", "", err
	}
	expected := filepath.Join(rootResolved, fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID, "source")
	qAbs, err := filepath.Abs(quarantine)
	if err != nil || !sameQuarantinePath(qAbs, expected) {
		return "", "", errors.New("unsafe encryption quarantine path")
	}
	return rootResolved, expected, nil
}

func validateExistingQuarantinePath(root, quarantine string, mediaID, generation int64, stageID string) (string, error) {
	rootResolved, expected, err := validateQuarantineIdentity(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		return "", err
	}
	current := rootResolved
	for _, component := range []string{fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID, "source"} {
		current = filepath.Join(current, component)
		info, statErr := encryptionLstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe encryption quarantine path")
		}
		if current != expected && !info.IsDir() {
			return "", errors.New("unsafe encryption quarantine path")
		}
		if current == expected && !info.Mode().IsRegular() {
			return "", errors.New("unsafe encryption quarantine path")
		}
	}
	resolved, err := filepath.EvalSymlinks(expected)
	if err != nil || !sameQuarantinePath(resolved, expected) || !pathWithinRoot(rootResolved, resolved) {
		return "", errors.New("unsafe encryption quarantine path")
	}
	return expected, nil
}

func ensureQuarantineParent(root string, mediaID, generation int64, stageID string) (string, error) {
	if !safeEncryptionStageID(stageID) {
		return "", errors.New("unsafe encryption quarantine path")
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
			return "", errors.New("unsafe encryption quarantine path")
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
