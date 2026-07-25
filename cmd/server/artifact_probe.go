package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type artifactProbeFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}
type artifactProbeOps interface {
	MkdirAll(string, os.FileMode) error
	OpenFile(string, int, os.FileMode) (artifactProbeFile, error)
	Remove(string) error
	SyncDir(string) error
}
type osArtifactProbeOps struct{}

func (osArtifactProbeOps) MkdirAll(p string, m os.FileMode) error { return os.MkdirAll(p, m) }
func (osArtifactProbeOps) OpenFile(p string, f int, m os.FileMode) (artifactProbeFile, error) {
	return os.OpenFile(p, f, m)
}
func (osArtifactProbeOps) Remove(p string) error { return os.Remove(p) }
func (osArtifactProbeOps) SyncDir(p string) error {
	d, err := os.Open(p)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil && (runtime.GOOS == "windows" || isUnsupportedDirSync(err)) {
		return nil
	}
	return err
}
func isUnsupportedDirSync(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "incorrect function") || strings.Contains(text, "invalid argument") || errors.Is(err, os.ErrInvalid)
}

func probeArtifactRoot(root string, ops artifactProbeOps) (retErr error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("artifact root unavailable")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err = ops.MkdirAll(abs, 0700); err != nil {
		return err
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	name := filepath.Join(abs, ".publication-v2-probe-"+hex.EncodeToString(token))
	f, err := ops.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			retErr = errors.Join(retErr, f.Close())
		}
		if removeErr := ops.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove probe: %w", removeErr))
		}
	}()
	if _, err = f.Write([]byte("publication-v2-root-probe")); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	closed = true
	if err = ops.Remove(name); err != nil {
		return err
	}
	if err = ops.SyncDir(abs); err != nil {
		return err
	}
	return nil
}
