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
	"knox-media/internal/store"
)

// PreCaptureConfig holds dependencies for scan poster capture after the scanner transaction commits.
type PreCaptureConfig struct {
	DB        *sql.DB
	Runner    *LocalPosterRunner
	Derived   *storage.DerivedAssetStore
	UploadDir string
	Timeout   time.Duration
}

// CapturedPoster identifies both the publication plan and the captured artifact/source.
type CapturedPoster struct {
	MediaID, RunID, StepID, TaskID, Generation int64
	StageID, StageDir                          string
	ArtifactPath, ArtifactURL                  string
	ArtifactHash                               string
	ArtifactSize                               int64
	SourcePath, SourceFingerprint, Source      string
	Derived                                    *storage.StagedDerivedAsset
}

func quickSourceFingerprint(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("quick fingerprint: abs path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("quick fingerprint: stat: %w", err)
	}
	return fmt.Sprintf("%s|%d|%d|sha256:%s", abs, info.Size(), info.ModTime().UnixNano(), strings.Repeat("0", 64)), nil
}

// CapturePoster performs all filesystem, ffprobe, ffmpeg, sealing, and derived staging work without a writer transaction.
func CapturePoster(ctx context.Context, mediaID int64, run publication.Run, cfg PreCaptureConfig) (captured CapturedPoster, retErr error) {
	if cfg.DB == nil || cfg.Runner == nil || mediaID <= 0 || run.ID <= 0 || run.Generation <= 0 {
		return captured, fmt.Errorf("precapture: invalid configuration or identity")
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	var libraryID, generation, stepID, taskID, duration int64
	var fileType string
	if err := cfg.DB.QueryRowContext(ctx, `SELECT m.library_id,COALESCE(m.file_type,''),m.ingest_generation,COALESCE(m.duration,0),s.id,p.id FROM media m JOIN media_ingest_run r ON r.id=? AND r.media_id=m.id AND r.generation=m.ingest_generation JOIN media_ingest_step s ON s.run_id=r.id AND s.media_id=m.id AND s.generation=r.generation AND s.step_type='poster' AND s.required=1 AND s.status='waiting' JOIN post_ingest_task p ON p.ingest_step_id=s.id AND p.ingest_run_id=r.id AND p.media_id=m.id AND p.generation=r.generation AND p.task_type='poster' AND p.status='waiting' WHERE m.id=? AND m.ingest_generation=? AND m.publication_state='processing' AND m.published_at IS NULL`, run.ID, mediaID, run.Generation).Scan(&libraryID, &fileType, &generation, &duration, &stepID, &taskID); err != nil {
		return captured, fmt.Errorf("precapture: load current plan: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return captured, fmt.Errorf("precapture: media is not video")
	}
	selection, err := loadPreferredPosterSource(ctx, cfg.DB, mediaID)
	if err != nil || selection.path == "" {
		if err == nil {
			err = errors.New("media file is unavailable")
		}
		return captured, fmt.Errorf("precapture: source: %w", err)
	}
	fp, err := quickSourceFingerprint(selection.path)
	if err != nil {
		return captured, fmt.Errorf("precapture: source fingerprint: %w", err)
	}
	scraperCfg, err := precaptureConfigForLibrary(ctx, cfg.DB, libraryID)
	if err != nil {
		return captured, fmt.Errorf("precapture: load library config: %w", err)
	}
	stageID := uuid.NewString()
	dir := filepath.Join(strings.TrimSpace(cfg.UploadDir), "posters", fmt.Sprintf("precapture-%d", mediaID), stageID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return captured, fmt.Errorf("precapture: mkdir: %w", err)
	}
	plain := filepath.Join(dir, posterLogicalName)
	captured = CapturedPoster{MediaID: mediaID, RunID: run.ID, StepID: stepID, TaskID: taskID, Generation: generation, StageID: stageID, StageDir: dir, SourcePath: selection.path, SourceFingerprint: fp}
	defer func() {
		if retErr != nil {
			_ = os.Remove(plain)
			if captured.Derived != nil && cfg.Derived != nil {
				cfg.Derived.AbortStaged(captured.Derived)
			}
			_ = os.Remove(dir)
		}
	}()
	enabled := func(name string) bool {
		for _, value := range scraperCfg.ImageSources {
			if strings.EqualFold(strings.TrimSpace(value), name) {
				return true
			}
		}
		return false
	}
	if enabled("embedded") && strings.TrimSpace(cfg.Runner.FFprobePath) != "" {
		if index, ok, probeErr := cfg.Runner.attachedPicture(ctx, mediaID, selection.path); probeErr == nil && ok {
			if _, captureErr := cfg.Runner.ffmpeg(ctx, mediaID, selection.path, nil, []string{"-map", fmt.Sprintf("0:%d", index), "-frames:v", "1", plain}); captureErr == nil && nonEmptyFile(plain) {
				captured.Source = "embedded"
			}
		}
	}
	if captured.Source == "" && enabled("screen_grabber") {
		if _, err = cfg.Runner.ffmpeg(ctx, mediaID, selection.path, storage.PosterSeekPreInput(posterSnapSecond(duration), selection.path), []string{"-frames:v", "1", "-q:v", "3", plain}); err != nil {
			return captured, err
		}
		if nonEmptyFile(plain) {
			captured.Source = "screen_grabber"
		}
	}
	if captured.Source == "" {
		return captured, errors.New("precapture: poster capture produced no file")
	}
	if cfg.Derived != nil && storage.NeedsDerivedEncryption(cfg.DB, mediaID) {
		captured.Derived, err = cfg.Derived.StagePath(ctx, mediaID, posterKind, posterLogicalName, plain)
		if err != nil {
			return captured, fmt.Errorf("precapture: stage derived: %w", err)
		}
		_ = os.Remove(plain)
		captured.ArtifactPath, captured.ArtifactURL = captured.Derived.EncPath(), storage.DerivedPosterAPIPath(mediaID)
	} else {
		size, hash, hashErr := hashPath(plain)
		if hashErr != nil {
			return captured, hashErr
		}
		sealed, _, sealErr := sealPlainPosterObject(ctx, cfg.UploadDir, StagedPoster{Path: plain, Hash: hash, Size: size})
		if sealErr != nil {
			return captured, fmt.Errorf("precapture: seal artifact: %w", sealErr)
		}
		captured.ArtifactPath, captured.ArtifactURL = sealed.Path, sealed.URL
	}
	captured.ArtifactSize, captured.ArtifactHash, err = hashPath(captured.ArtifactPath)
	if err != nil {
		return captured, fmt.Errorf("precapture: hash artifact: %w", err)
	}
	return captured, nil
}

// FinalizeCapturedPoster atomically fences the capture, records evidence, and publishes the run in a short writer transaction.
func FinalizeCapturedPoster(ctx context.Context, db *sql.DB, captured CapturedPoster) error {
	if db == nil || captured.MediaID <= 0 || captured.RunID <= 0 || captured.StepID <= 0 || captured.TaskID <= 0 || captured.Generation <= 0 {
		return errors.New("precapture finalize: invalid identity")
	}
	currentFP, err := quickSourceFingerprint(captured.SourcePath)
	if err != nil || currentFP != captured.SourceFingerprint {
		return errors.New("precapture finalize: source stat changed")
	}
	size, hash, err := hashPath(captured.ArtifactPath)
	if err != nil || size != captured.ArtifactSize || hash != captured.ArtifactHash {
		return errors.New("precapture finalize: artifact changed")
	}
	_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		var currentGeneration int64
		var catalog, libraryRoot, encryptedPath, plainPath, encryptedStatus string
		if e := tx.QueryRowContext(ctx, `SELECT m.ingest_generation,COALESCE(m.file_path,''),COALESCE(l.path,''),COALESCE(a.enc_path,''),COALESCE(a.plain_path,''),COALESCE(a.status,'') FROM media m JOIN library l ON l.id=m.library_id LEFT JOIN media_encrypted_assets a ON a.media_id=m.id WHERE m.id=? AND m.ingest_generation=? AND m.publication_state='processing' AND m.published_at IS NULL`, captured.MediaID, captured.Generation).Scan(&currentGeneration, &catalog, &libraryRoot, &encryptedPath, &plainPath, &encryptedStatus); e != nil {
			return fmt.Errorf("precapture finalize: stale media: %w", e)
		}
		selection := posterSourceSelection{mediaID: captured.MediaID, catalog: catalog, libraryRoot: libraryRoot, encryptedPath: encryptedPath, plainPath: plainPath, encryptedStatus: encryptedStatus}
		if currentGeneration != captured.Generation || !selectedPosterSourceMatches(selection, captured.SourcePath) {
			return errors.New("precapture finalize: source selection changed")
		}
		if statFP, e := quickSourceFingerprint(captured.SourcePath); e != nil || statFP != captured.SourceFingerprint {
			return errors.New("precapture finalize: source stat changed")
		}
		if captured.Derived != nil {
			if _, e := (&storage.DerivedAssetStore{}).CommitStagedTx(ctx, tx, captured.Derived); e != nil {
				return e
			}
		}
		if _, e := persistPosterMetaTx(ctx, tx, captured.MediaID, captured.ArtifactURL, captured.Source, true); e != nil {
			return e
		}
		refs, _ := json.Marshal(map[string]any{"path": captured.ArtifactPath, "url": captured.ArtifactURL, "source": captured.Source, "size": captured.ArtifactSize, "sha256": captured.ArtifactHash, "generation": captured.Generation, "stage_id": captured.StageID})
		if _, e := tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'poster',?,?,'precapture',CURRENT_TIMESTAMP,?)`, captured.RunID, captured.StepID, captured.MediaID, captured.Generation, captured.SourceFingerprint, string(refs), captured.StageID); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND media_id=? AND generation=? AND step_type='poster' AND status='waiting'`, captured.StepID, captured.RunID, captured.MediaID, captured.Generation)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("precapture finalize: stale poster step")
		}
		result, e = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='done',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND ingest_run_id=? AND ingest_step_id=? AND media_id=? AND generation=? AND task_type='poster' AND status='waiting'`, captured.TaskID, captured.RunID, captured.StepID, captured.MediaID, captured.Generation)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("precapture finalize: stale poster task")
		}
		return publication.AggregateTx(ctx, tx, captured.RunID)
	})
	if err == nil {
		_ = os.Remove(captured.StageDir)
	}
	return err
}

// RejectCapturedPoster removes an uncommitted capture and its matching unpublished processing media.
func RejectCapturedPoster(ctx context.Context, db *sql.DB, captured CapturedPoster) error {
	if db == nil {
		return errors.New("precapture reject: nil database")
	}
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `DELETE FROM media WHERE id=? AND ingest_generation=? AND published_at IS NULL AND publication_state='processing'`, captured.MediaID, captured.Generation)
		return e
	})
	if err != nil {
		return err
	}
	if captured.Derived != nil {
		_ = os.Remove(captured.ArtifactPath)
	} else if captured.ArtifactPath != "" {
		if cleanupErr := cleanupPosterPaths(ctx, db, []string{captured.ArtifactPath}, captured.ArtifactPath); cleanupErr != nil {
			return cleanupErr
		}
	}
	_ = os.Remove(filepath.Join(captured.StageDir, posterLogicalName))
	_ = os.Remove(captured.StageDir)
	return nil
}

func precaptureConfigForLibrary(ctx context.Context, db *sql.DB, libraryID int64) (scraper.Config, error) {
	var providers string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(image_providers,'') FROM library WHERE id=?`, libraryID).Scan(&providers); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scraper.Config{}, fmt.Errorf("library %d not found", libraryID)
		}
		return scraper.Config{}, err
	}
	cfg := scraper.Config{APIKeys: map[string]string{}}
	for _, provider := range strings.Split(providers, ",") {
		if provider = strings.TrimSpace(provider); provider != "" {
			cfg.ImageSources = append(cfg.ImageSources, provider)
		}
	}
	return cfg, nil
}
