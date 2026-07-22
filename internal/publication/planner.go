package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Planner creates immutable ingest plans using fixed process capabilities and
// library settings read through the caller-owned transaction.
type Planner struct {
	options PlanOptions
}

func NewPlanner(options PlanOptions) *Planner {
	return &Planner{options: options}
}

func (p *Planner) PlanNewMediaTx(ctx context.Context, tx *sql.Tx, media NewMedia) (Run, error) {
	return p.planMediaTx(ctx, tx, media, "scan", false)
}

// RepairMediaTx creates a visibility-preserving generation for legacy media.
// It intentionally has no scan task: repair discovery is independent of scans.
func (p *Planner) RepairMediaTx(ctx context.Context, tx *sql.Tx, mediaID int64) (Run, error) {
	return p.planMediaTx(ctx, tx, NewMedia{MediaID: mediaID, FileType: "video"}, "repair", true)
}

func (p *Planner) planMediaTx(ctx context.Context, tx *sql.Tx, media NewMedia, reason string, preserve bool) (Run, error) {
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	if p == nil {
		return Run{}, errors.New("publication planner: nil planner")
	}
	if tx == nil {
		return Run{}, errors.New("publication planner: nil transaction")
	}
	if media.MediaID <= 0 {
		return Run{}, errors.New("publication planner: invalid media id")
	}
	if reason == "scan" && media.ScanTaskID <= 0 {
		return Run{}, errors.New("publication planner: invalid scan task id")
	}

	var libraryID, currentGeneration int64
	var fileType string
	var previewExtract, libraryEncrypt int
	err := tx.QueryRowContext(ctx, `
SELECT m.library_id,COALESCE(m.file_type,''),COALESCE(l.preview_extract,0),
       COALESCE(l.encrypted_assets_enabled,0),m.ingest_generation
FROM media m
JOIN library l ON l.id=m.library_id
WHERE m.id=?`, media.MediaID).Scan(&libraryID, &fileType, &previewExtract, &libraryEncrypt, &currentGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, errors.New("publication planner: media or library not found")
	}
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: load media: %w", err)
	}
	fileType = strings.TrimSpace(fileType)
	hint := strings.TrimSpace(media.FileType)
	if hint == "" {
		return Run{}, errors.New("publication planner: empty file type hint")
	}
	if hint != fileType {
		return Run{}, fmt.Errorf("publication planner: file type hint %q does not match database file type %q", hint, fileType)
	}

	if reason == "scan" {
		var scanLibraryID int64
		err = tx.QueryRowContext(ctx, `SELECT library_id FROM scan_task WHERE id=?`, media.ScanTaskID).Scan(&scanLibraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, errors.New("publication planner: scan task not found")
		}
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: load scan task: %w", err)
		}
		if scanLibraryID != libraryID {
			return Run{}, errors.New("publication planner: scan task does not belong to media library")
		}
	}

	if fileType != "video" {
		return Run{}, nil
	}

	steps := []StepType{StepPoster, StepScrape}
	if previewExtract == 1 {
		steps = append(steps, StepPreview)
	}
	steps = append(steps, StepKeyframe)
	if p.options.SubtitleAuto {
		steps = append(steps, StepSubtitle)
	}
	if p.options.ATrackAuto {
		steps = append(steps, StepAtrack)
	}
	encrypt := p.options.EncryptGlobal && libraryEncrypt == 1
	if encrypt {
		steps = append(steps, StepEncrypt)
	}

	snapshot := ConfigSnapshot{
		LibraryID:      libraryID,
		FileType:       fileType,
		PreviewExtract: previewExtract == 1,
		SubtitleAuto:   p.options.SubtitleAuto,
		ATrackAuto:     p.options.ATrackAuto,
		Encrypt:        encrypt,
		Steps:          append([]StepType(nil), steps...),
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: encode snapshot: %w", err)
	}

	var generation int64
	err = tx.QueryRowContext(ctx, `
UPDATE media
SET ingest_generation=ingest_generation+1,
    publication_state=CASE WHEN ? THEN publication_state ELSE 'processing' END,
    publication_error=CASE WHEN ? THEN publication_error ELSE '' END
WHERE id=? AND ingest_generation=?
RETURNING ingest_generation`, boolDB(preserve), boolDB(preserve), media.MediaID, currentGeneration).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) && reason == "repair" {
		return Run{}, nil
	}
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: advance generation: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json)
VALUES(?,?,?,?, 'processing',?,?)`, media.MediaID, generation, nullScanTask(media.ScanTaskID), reason, boolDB(preserve), string(snapshotJSON))
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: insert run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return Run{}, fmt.Errorf("publication planner: read run id: %w", err)
	}

	for _, step := range steps {
		result, err = tx.ExecContext(ctx, `
INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status)
VALUES(?,?,?,?,1,'waiting')`, runID, media.MediaID, generation, step)
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: insert %s step: %w", step, err)
		}
		stepID, err := result.LastInsertId()
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: read %s step id: %w", step, err)
		}
		if step == StepScrape {
			_, err = tx.ExecContext(ctx, `
INSERT INTO scrape_task(media_id,source,status,progress,ingest_run_id,ingest_step_id,generation)
VALUES(?,'auto-scan','waiting',0,?,?,?)
ON CONFLICT(ingest_run_id,ingest_step_id,generation) DO NOTHING`, media.MediaID, runID, stepID, generation)
			if err != nil {
				return Run{}, fmt.Errorf("publication planner: enqueue scrape: %w", err)
			}
			continue
		}
		if !queueBacked(step) {
			continue
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status)
VALUES(?,?,?,?,?,?,'waiting')`, media.MediaID, nullScanTask(media.ScanTaskID), runID, stepID, generation, step)
		if err != nil {
			return Run{}, fmt.Errorf("publication planner: enqueue %s step: %w", step, err)
		}
	}

	return Run{
		ID: runID, MediaID: media.MediaID, ScanTaskID: media.ScanTaskID,
		LibraryID: libraryID, Generation: generation, State: StateProcessing,
		Steps: append([]StepType(nil), steps...),
	}, nil
}

func boolDB(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullScanTask(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func queueBacked(step StepType) bool {
	switch step {
	case StepPoster, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt:
		return true
	default:
		return false
	}
}
