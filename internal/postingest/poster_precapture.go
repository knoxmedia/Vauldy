package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
)

// PreCaptureConfig holds dependencies for synchronous poster pre-capture during scan.
type PreCaptureConfig struct {
	DB        *sql.DB
	Runner    *LocalPosterRunner
	Derived   *storage.DerivedAssetStore
	UploadDir string
	Timeout   time.Duration
}

// quickSourceFingerprint builds a source fingerprint using only file stat
// (size + mtime) without hashing the file content. The sha256 portion is
// set to all zeros as a sentinel to avoid the expensive full-file hash.
func quickSourceFingerprint(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("quick fingerprint: abs path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("quick fingerprint: stat: %w", err)
	}
	zeroHash := strings.Repeat("0", 64)
	return fmt.Sprintf("%s|%d|%d|sha256:%s", abs, info.Size(), info.ModTime().UnixNano(), zeroHash), nil
}

// PreCapturePoster runs synchronous poster capture inside the scanner's database
// transaction. It queries the media row (already INSERTed by the scanner within
// the same tx), captures a poster frame via ffmpeg, writes the poster artifact,
// marks the poster step/task done, inserts evidence, and runs AggregateTx to
// transition the run to 'published'.
//
// On any error, the caller MUST roll back the transaction.
func PreCapturePoster(ctx context.Context, tx *sql.Tx, mediaID int64, run publication.Run, cfg PreCaptureConfig) error {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	// Query the media row from the same transaction.
	var libraryID int64
	var fileType, filePath string
	var duration int64
	if err := tx.QueryRowContext(ctx,
		`SELECT library_id, COALESCE(file_type,''), COALESCE(file_path,''), COALESCE(duration,0) FROM media WHERE id=?`,
		mediaID).Scan(&libraryID, &fileType, &filePath, &duration); err != nil {
		return fmt.Errorf("precapture: load media: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return nil // only video needs poster
	}

	// Look up the poster step and task IDs created by the planner.
	var stepID, taskID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT s.id, p.id FROM media_ingest_step s JOIN post_ingest_task p ON p.ingest_step_id=s.id AND p.ingest_run_id=s.run_id AND p.generation=s.generation WHERE s.run_id=? AND s.step_type='poster' AND s.required=1 AND s.generation=?`,
		run.ID, run.Generation).Scan(&stepID, &taskID); err != nil {
		return fmt.Errorf("precapture: find poster step: %w", err)
	}

	// Resolve the source file path.
	input := preferredFFmpegPathTx(tx, cfg.DB, mediaID, libraryID, filePath)
	if input == "" {
		return fmt.Errorf("precapture: media file is unavailable")
	}

	// Load scraper config for image sources.
	scraperCfg, err := precaptureConfigForLibrary(ctx, tx, libraryID)
	if err != nil {
		return fmt.Errorf("precapture: load library config: %w", err)
	}

	// Quick fingerprint (stat only, no full-file hash).
	fp, err := quickSourceFingerprint(input)
	if err != nil {
		return fmt.Errorf("precapture: source fingerprint: %w", err)
	}

	// Create staging directory.
	stageID := uuid.NewString()
	dir := filepath.Join(strings.TrimSpace(cfg.UploadDir), "posters", fmt.Sprintf("precapture-%d", mediaID), stageID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("precapture: mkdir: %w", err)
	}
	plain := filepath.Join(dir, posterLogicalName)
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(plain)
			_ = os.Remove(dir)
		}
	}()

	// Run ffmpeg capture (same logic as LocalPosterRunner.StagePoster).
	enabled := func(name string) bool {
		for _, v := range scraperCfg.ImageSources {
			if strings.EqualFold(strings.TrimSpace(v), name) {
				return true
			}
		}
		return false
	}

	source := ""
	if enabled("embedded") && cfg.Runner != nil && strings.TrimSpace(cfg.Runner.FFprobePath) != "" {
		if index, ok, e := cfg.Runner.AttachedPicture(ctx, mediaID, input); e == nil && ok {
			if _, e = cfg.Runner.FFmpeg(ctx, mediaID, input, nil, []string{"-map", fmt.Sprintf("0:%d", index), "-frames:v", "1", plain}); e == nil && nonEmptyFile(plain) {
				source = "embedded"
			}
		}
	}
	if source == "" && enabled("screen_grabber") && cfg.Runner != nil {
		snap := posterSnapSecond(duration)
		if _, e := cfg.Runner.FFmpeg(ctx, mediaID, input, storage.PosterSeekPreInput(snap, input), []string{"-frames:v", "1", "-q:v", "3", plain}); e == nil && nonEmptyFile(plain) {
			source = "screen_grabber"
		}
	}
	if source == "" {
		return fmt.Errorf("precapture: poster capture produced no file")
	}

	// Hash the captured poster file.
	artifactSize, artifactHash, err := hashPath(plain)
	if err != nil {
		return fmt.Errorf("precapture: hash artifact: %w", err)
	}

	// Seal to content-addressed storage.
	staged := StagedPoster{
		Path:   plain,
		Source: source,
		Hash:   artifactHash,
		Size:   artifactSize,
	}
	staged, _, sealErr := sealPlainPosterObject(ctx, cfg.UploadDir, staged)
	if sealErr != nil {
		return fmt.Errorf("precapture: seal artifact: %w", sealErr)
	}

	// Handle derived encryption if needed.
	posterURL := staged.URL
	if cfg.Derived != nil && storage.NeedsDerivedEncryption(cfg.DB, mediaID) {
		derived, err := cfg.Derived.StagePath(ctx, mediaID, posterKind, posterLogicalName, staged.Path)
		if err != nil {
			return fmt.Errorf("precapture: stage derived: %w", err)
		}
		_ = os.Remove(staged.Path)
		staged.Path = derived.EncPath()
		posterURL = storage.DerivedPosterAPIPath(mediaID)
	}

	// Build artifact refs JSON.
	refsJSON, _ := json.Marshal(map[string]any{
		"path": staged.Path, "url": posterURL, "source": source,
		"size": artifactSize, "sha256": artifactHash,
	})

	// Update media.meta_json with poster URL.
	if _, err := persistPosterMetaTx(ctx, tx, mediaID, posterURL, source, true); err != nil {
		return fmt.Errorf("precapture: persist meta: %w", err)
	}

	// Insert evidence row.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id)
		VALUES(?,?,?,?,'poster',?,?,'precapture',CURRENT_TIMESTAMP,?)`,
		run.ID, stepID, mediaID, run.Generation, fp, string(refsJSON), stageID)
	if err != nil {
		return fmt.Errorf("precapture: insert evidence: %w", err)
	}

	// Mark step done.
	_, err = tx.ExecContext(ctx,
		`UPDATE media_ingest_step SET status='done', finished_at=CURRENT_TIMESTAMP WHERE id=? AND status='waiting'`,
		stepID)
	if err != nil {
		return fmt.Errorf("precapture: mark step done: %w", err)
	}

	// Mark task done.
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status='done', finished_at=CURRENT_TIMESTAMP WHERE id=? AND status='waiting'`,
		taskID)
	if err != nil {
		return fmt.Errorf("precapture: mark task done: %w", err)
	}

	// Transition run and media visibility.
	if err := publication.AggregateTx(ctx, tx, run.ID); err != nil {
		return fmt.Errorf("precapture: aggregate: %w", err)
	}

	cleanup = false
	return nil
}

// preferredFFmpegPathTx resolves the best ffmpeg input path using the scan transaction.
// This is a tx-based variant of storage.PreferredFFmpegPath.
func preferredFFmpegPathTx(tx *sql.Tx, db *sql.DB, mediaID, libraryID int64, catalogPath string) string {
	catalogPath = strings.TrimSpace(catalogPath)
	if catalogPath == "" {
		return ""
	}
	// Resolve library root from the transaction.
	var libPath string
	_ = tx.QueryRow(`SELECT COALESCE(path,'') FROM library WHERE id=?`, libraryID).Scan(&libPath)
	var abs string
	if filepath.IsAbs(catalogPath) {
		abs = filepath.Clean(catalogPath)
	} else if strings.TrimSpace(libPath) != "" {
		abs = filepath.Clean(filepath.Join(libPath, filepath.FromSlash(catalogPath)))
	} else {
		abs = filepath.Clean(catalogPath)
	}
	if abs == "" {
		return ""
	}
	// Check encryption status.
	isEnc := strings.HasSuffix(strings.ToLower(filepath.Clean(abs)), ".enc")
	if isEnc && mediaID > 0 {
		var n int
		_ = tx.QueryRow(`SELECT COUNT(1) FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'`, mediaID).Scan(&n)
		if n == 0 {
			isEnc = false
		}
	}
	if !isEnc {
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
		return ""
	}
	if mediaID > 0 {
		var plainPath sql.NullString
		_ = tx.QueryRow(`SELECT plain_path FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'`, mediaID).Scan(&plainPath)
		if p := strings.TrimSpace(plainPath.String); p != "" {
			if _, err := os.Stat(p); err == nil {
				return filepath.Clean(p)
			}
		}
	}
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	return ""
}

// precaptureConfigForLibrary loads the scraper image sources config for a library.
func precaptureConfigForLibrary(ctx context.Context, tx *sql.Tx, libraryID int64) (scraper.Config, error) {
	var providers string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(image_providers,'') FROM library WHERE id=?`, libraryID).Scan(&providers); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scraper.Config{}, fmt.Errorf("library %d not found", libraryID)
		}
		return scraper.Config{}, err
	}
	cfg := scraper.Config{APIKeys: map[string]string{}}
	for _, p := range strings.Split(providers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.ImageSources = append(cfg.ImageSources, p)
		}
	}
	return cfg, nil
}
