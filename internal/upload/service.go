package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Service struct {
	UploadDir string
	ChunksDir string
}

func (s *Service) SaveChunk(uploadID string, index int, r io.Reader) (string, error) {
	dir := filepath.Join(s.ChunksDir, uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Join(dir, fmt.Sprintf("%08d.part", index))
	f, err := os.Create(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Service) Merge(uploadID string, totalParts int, destName string) (dest string, sha256hex string, err error) {
	dir := filepath.Join(s.ChunksDir, uploadID)
	dest = strings.TrimSpace(destName)
	if dest == "" || dest == "." {
		dest = "merged.bin"
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(s.UploadDir, dest)
	}
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", "", err
	}
	out, err := os.Create(dest)
	if err != nil {
		return "", "", err
	}
	defer out.Close()
	h := sha256.New()
	for i := 0; i < totalParts; i++ {
		part := filepath.Join(dir, fmt.Sprintf("%08d.part", i))
		pf, err := os.Open(part)
		if err != nil {
			return "", "", fmt.Errorf("open chunk %d: %w", i, err)
		}
		if _, err := io.Copy(io.MultiWriter(out, h), pf); err != nil {
			_ = pf.Close()
			return "", "", err
		}
		_ = pf.Close()
	}
	_ = os.RemoveAll(dir)
	return dest, hex.EncodeToString(h.Sum(nil)), nil
}
