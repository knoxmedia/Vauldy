package doctrans

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"knox-media/internal/config"
)

// Converter converts Office documents to PDF using configured engine priority.
type Converter struct {
	MediaRoot string
	Config    config.DocTransConfig
	Preview   string
	mu        sync.Mutex
	locks     map[int64]*sync.Mutex
}

func NewConverter(mediaRoot, previewRoot string, cfg config.DocTransConfig) *Converter {
	return &Converter{
		MediaRoot: mediaRoot,
		Config:    cfg,
		Preview:   previewRoot,
		locks:     map[int64]*sync.Mutex{},
	}
}

func (c *Converter) lock(mediaID int64) func() {
	c.mu.Lock()
	if c.locks == nil {
		c.locks = map[int64]*sync.Mutex{}
	}
	lk, ok := c.locks[mediaID]
	if !ok {
		lk = &sync.Mutex{}
		c.locks[mediaID] = lk
	}
	c.mu.Unlock()
	lk.Lock()
	return lk.Unlock
}

func (c *Converter) PreviewPDFPath(mediaID int64) string {
	return filepath.Join(c.cacheRoot(), fmt.Sprintf("%d", mediaID), "preview.pdf")
}

func (c *Converter) cacheRoot() string {
	if c != nil && strings.TrimSpace(c.Config.CacheDir) != "" {
		return ResolvePath(c.MediaRoot, c.Config.CacheDir)
	}
	if c != nil && strings.TrimSpace(c.Preview) != "" {
		return filepath.Join(c.Preview, "documents", "convert")
	}
	return filepath.Join("data", "preview", "documents", "convert")
}

// EnsurePreviewPDF converts source to PDF if needed and returns the PDF path.
func (c *Converter) EnsurePreviewPDF(ctx context.Context, mediaID int64, sourcePath string, sourceMtime int64) (string, error) {
	if c == nil || !docTransEnabled(c.Config) {
		return "", fmt.Errorf("document conversion disabled")
	}
	if !IsOfficeFormat(sourcePath) {
		return "", fmt.Errorf("not an office document")
	}
	unlock := c.lock(mediaID)
	defer unlock()

	outPDF := c.PreviewPDFPath(mediaID)
	metaPath := outPDF + ".meta"
	if st, err := os.Stat(outPDF); err == nil && !st.IsDir() && st.Size() > 0 {
		ttl := c.Config.CacheTTLDays
		if ttl <= 0 {
			ttl = 30
		}
		if cacheValid(metaPath, sourceMtime, st.ModTime(), ttl) {
			return outPDF, nil
		}
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return "", fmt.Errorf("source missing: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPDF), 0o755); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(outPDF), "doctrans-out-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	timeoutSec := c.Config.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	order := engineOrderFromConfig(c.Config)
	var lastErr error
	for _, kind := range order {
		st := engineStatusFor(c.MediaRoot, c.Config, kind)
		if !st.Available {
			lastErr = fmt.Errorf("%s: %s", kind, st.Message)
			continue
		}
		converted, err := convertWithEngine(cctx, c.MediaRoot, c.Config, kind, sourcePath, tmpDir)
		if err != nil {
			lastErr = err
			continue
		}
		if st, err := os.Stat(converted); err != nil || st.IsDir() || st.Size() == 0 {
			lastErr = fmt.Errorf("%s: empty output", kind)
			continue
		}
		if err := copyFile(converted, outPDF); err != nil {
			return "", err
		}
		writeCacheMeta(metaPath, sourceMtime, kind)
		return outPDF, nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("all engines failed: %w", lastErr)
	}
	return "", fmt.Errorf("no conversion engine available")
}

func engineStatusFor(mediaRoot string, cfg config.DocTransConfig, kind EngineKind) EngineStatus {
	switch kind {
	case EngineOffice:
		return detectOffice(mediaRoot, cfg)
	case EngineWPS:
		return detectWPS(mediaRoot, cfg)
	case EngineLibreOffice:
		return detectLibreOffice(mediaRoot, cfg)
	default:
		return EngineStatus{Kind: kind, Message: "unknown engine"}
	}
}
