package doccover

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/doctrans"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

const (
	CoverMaxEdge         = 480
	docCoverKind         = "doc_cover"
	docCoverLogicalName  = "cover.jpg"
)

// Options configures document cover generation.
type Options struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	Derived    *storage.DerivedAssetStore
	FFmpegPath string
	PreviewDir string
	MediaRoot  string
	DocTrans   config.DocTransConfig
}

func (o Options) derivedBaseDir() string {
	if o.Derived != nil && strings.TrimSpace(o.Derived.BaseDir) != "" {
		return o.Derived.BaseDir
	}
	return ""
}

// Path returns the cached cover image path for one media item.
func Path(previewDir string, mediaID int64) string {
	return filepath.Join(previewDir, "documents", fmt.Sprintf("%d", mediaID), "cover.jpg")
}

// Exists reports whether a non-empty plaintext cover cache file is present.
func Exists(previewDir string, mediaID int64) bool {
	st, err := os.Stat(Path(previewDir, mediaID))
	return err == nil && !st.IsDir() && st.Size() > 0
}

// CachedCover reports whether a usable cover exists (plaintext or encrypted derived asset).
// When sourceMtime <= 0, any non-empty cover file is accepted.
func CachedCover(db *sql.DB, previewDir, derivedBaseDir string, mediaID int64, sourceMtime int64) bool {
	if mediaID <= 0 {
		return false
	}
	if coverFresh(Path(previewDir, mediaID), sourceMtime) {
		return true
	}
	if db == nil {
		return false
	}
	if enc, ok := storage.ResolveDerivedEncPath(db, derivedBaseDir, mediaID, docCoverKind, docCoverLogicalName); ok {
		return coverFresh(enc, sourceMtime)
	}
	return false
}

type coverStrategy int

const (
	strategyNone coverStrategy = iota
	strategyEPUB
	strategyPDF
	strategyOffice
	strategyImage
)

func coverStrategyFor(sourcePath string) coverStrategy {
	ext := strings.ToLower(filepath.Ext(sourcePath))
	switch ext {
	case ".epub":
		return strategyEPUB
	case ".pdf":
		return strategyPDF
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".tif", ".tiff":
		return strategyImage
	default:
		if doctrans.IsOfficeFormat(sourcePath) {
			return strategyOffice
		}
		return strategyNone
	}
}

// Ensure generates a cover.jpg preview when supported for the source document.
func Ensure(ctx context.Context, opts Options, mediaID int64, sourcePath string, fileMtime int64) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if mediaID <= 0 || sourcePath == "" {
		return fmt.Errorf("invalid cover input")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return fmt.Errorf("source missing: %w", err)
	}
	workPath, cleanup, err := storage.MaterializePlaintextTemp(opts.DB, opts.Vault, mediaID, sourcePath)
	if err != nil {
		return err
	}
	defer cleanup()

	outPath := Path(opts.PreviewDir, mediaID)
	if CachedCover(opts.DB, opts.PreviewDir, opts.derivedBaseDir(), mediaID, fileMtime) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	strategy := coverStrategyFor(workPath)
	var genErr error
	switch strategy {
	case strategyEPUB:
		if ExtractEPUBCover(workPath, outPath) == "" {
			genErr = fmt.Errorf("epub cover not found")
		}
	case strategyPDF:
		genErr = renderPageCover(ctx, opts, mediaID, workPath, outPath)
	case strategyImage:
		genErr = renderJPEG(ctx, opts, mediaID, workPath, outPath)
	case strategyOffice:
		if !docTransEnabled(opts.DocTrans) {
			genErr = fmt.Errorf("office cover requires document conversion")
		} else if err := doctrans.ExportOfficeCoverJPEG(ctx, opts.MediaRoot, opts.DocTrans, workPath, outPath); err != nil {
			conv := doctrans.NewConverter(opts.MediaRoot, opts.PreviewDir, opts.DocTrans)
			pdfPath, convErr := conv.EnsurePreviewPDF(ctx, mediaID, workPath, fileMtime)
			if convErr != nil {
				genErr = convErr
			} else {
				genErr = renderPageCover(ctx, opts, mediaID, pdfPath, outPath)
			}
		}
	default:
		return nil
	}
	if genErr != nil {
		return genErr
	}
	TouchCoverAfterWrite(outPath, fileMtime)
	if opts.Derived != nil {
		if _, err := opts.Derived.FinalizePath(ctx, mediaID, "doc_cover", "cover.jpg", outPath); err != nil {
			return err
		}
	}
	return nil
}

func coverFresh(coverPath string, sourceMtime int64) bool {
	st, err := os.Stat(coverPath)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return false
	}
	if sourceMtime <= 0 {
		return true
	}
	return st.ModTime().UTC().Unix() >= sourceMtime
}

func docTransEnabled(cfg config.DocTransConfig) bool {
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}

// TouchCoverAfterWrite aligns cover mtime with source mtime for stable cache checks.
func TouchCoverAfterWrite(coverPath string, sourceMtime int64) {
	if sourceMtime <= 0 {
		return
	}
	_ = os.Chtimes(coverPath, time.Unix(sourceMtime, 0).UTC(), time.Unix(sourceMtime, 0).UTC())
}
