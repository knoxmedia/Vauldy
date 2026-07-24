package postingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type EncryptionRecoveryRoots struct{ Quarantine, Staged string }

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
func quarantinePlaintext(source, root string, mediaID, generation int64, stageID string) (string, error) {
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
	syncErr := dst.Sync()
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
	if err = os.Remove(source); err != nil {
		return "", err
	}
	return target, nil
}
func restoreQuarantinedPlaintext(quarantine, source, root string) error {
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
		return nil
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
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return errors.Join(copyErr, syncErr, closeErr)
	}
	if err = os.Rename(tmp, source); err != nil {
		return err
	}
	return os.Remove(qAbs)
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
