package documenttask

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ArtifactManager handles atomic staged PDF validation and commit.
type ArtifactManager struct {
	WorkDir string
}

// NewArtifactManager creates a new ArtifactManager.
func NewArtifactManager(workDir string) *ArtifactManager {
	return &ArtifactManager{WorkDir: workDir}
}

// StagePath returns the staging directory for a media item.
func (m *ArtifactManager) StagePath(mediaID int64) string {
	return filepath.Join(m.WorkDir, "staging", fmt.Sprintf("%d", mediaID))
}

// CommittedPath returns the committed output path.
func (m *ArtifactManager) CommittedPath(mediaID int64) string {
	return filepath.Join(m.WorkDir, "committed", fmt.Sprintf("%d", mediaID), "preview.pdf")
}

// ValidatePDF reads the staged PDF, validates it starts with %PDF-, and probes size/hash.
func (m *ArtifactManager) ValidatePDF(stagedPath string) (ConvertOutput, error) {
	f, err := os.Open(stagedPath)
	if err != nil {
		return ConvertOutput{}, fmt.Errorf("artifact validate open: %w", err)
	}
	defer f.Close()

	header := make([]byte, 5)
	n, err := io.ReadFull(f, header)
	if err != nil || n < 5 {
		return ConvertOutput{}, fmt.Errorf("artifact validate: not a valid PDF (too short)")
	}
	if !bytes.Equal(header, []byte("%PDF-")) {
		return ConvertOutput{}, fmt.Errorf("artifact validate: missing PDF header")
	}

	info, err := f.Stat()
	if err != nil {
		return ConvertOutput{}, fmt.Errorf("artifact validate stat: %w", err)
	}

	h := sha256.New()
	if _, err := f.Seek(0, 0); err != nil {
		return ConvertOutput{}, fmt.Errorf("artifact validate seek: %w", err)
	}
	if _, err := io.Copy(h, f); err != nil {
		return ConvertOutput{}, fmt.Errorf("artifact validate hash: %w", err)
	}

	output := ConvertOutput{
		PDFPath: stagedPath,
		PDFSize: info.Size(),
		PDFHash: fmt.Sprintf("%x", h.Sum(nil)),
	}

	pageCount, err := probePageCount(stagedPath)
	if err != nil {
		return output, nil
	}
	output.PageCount = pageCount

	return output, nil
}

// Commit atomically renames the staged file to the committed path.
func (m *ArtifactManager) Commit(ctx context.Context, mediaID int64, stagedPath string) (string, error) {
	dest := m.CommittedPath(mediaID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("artifact commit mkdir: %w", err)
	}
	if err := os.Rename(stagedPath, dest); err != nil {
		return "", fmt.Errorf("artifact commit rename: %w", err)
	}
	return dest, nil
}

// HasCommitted checks if a committed artifact exists.
func (m *ArtifactManager) HasCommitted(mediaID int64) bool {
	info, err := os.Stat(m.CommittedPath(mediaID))
	return err == nil && !info.IsDir() && info.Size() > 0
}

// probePageCount is a minimal page-count probe for PDF files.
func probePageCount(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Contains(line, []byte("/Type")) && bytes.Contains(line, []byte("/Page")) {
			count++
		}
	}
	if count == 0 {
		return 1, nil
	}
	return count, nil
}
