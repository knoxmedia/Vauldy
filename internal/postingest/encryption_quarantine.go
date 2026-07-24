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
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, fmt.Sprintf("%d", mediaID), fmt.Sprintf("%d", generation), stageID, "source"), nil
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
	target, err := quarantinePath(root, mediaID, generation, stageID)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
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
func restoreQuarantinedPlaintext(quarantine, source, root string) error {
	return restoreQuarantinedPlaintextWithOps(quarantine, source, root, defaultEncryptionFileOps())
}
func restoreQuarantinedPlaintextWithOps(quarantine, source, root string, ops encryptionFileOps) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	qAbs, err := filepath.Abs(quarantine)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, qAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("unsafe encryption quarantine path")
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
