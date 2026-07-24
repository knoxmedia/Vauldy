package publication

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

// RepairLegacyMedia creates bounded, visibility-preserving ingest generations
// for active legacy videos and images that lack evidence required by the current plan.
func RepairLegacyMedia(ctx context.Context, db *sql.DB, planner *Planner, batchSize int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if db == nil {
		return 0, errors.New("publication repair: nil database")
	}
	if planner == nil {
		return 0, errors.New("publication repair: nil planner")
	}
	if batchSize <= 0 {
		return 0, nil
	}

	repaired, after := 0, int64(0)
	for {
		rows, err := db.QueryContext(ctx, `SELECT id FROM media WHERE id>? AND status='active' AND file_type IN ('video','image') AND publication_state IN ('published','degraded') ORDER BY id LIMIT ?`, after, batchSize)
		if err != nil {
			return repaired, fmt.Errorf("publication repair: list candidates: %w", err)
		}
		ids := make([]int64, 0, batchSize)
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return repaired, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return repaired, err
		}
		if len(ids) == 0 {
			return repaired, nil
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return repaired, err
			}
			created, err := repairLegacyMediaOne(ctx, db, planner, id)
			if err != nil {
				return repaired, fmt.Errorf("publication repair media %d: %w", id, err)
			}
			if created {
				repaired++
			}
		}
		after = ids[len(ids)-1]
	}
}

var repairRetryPolicy = store.RetryPolicy{
	Operation:  "publication_legacy_repair_media",
	MaxElapsed: 2 * time.Second, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
}

func repairLegacyMediaOne(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (bool, error) {
	preflight, err := loadRepairPreflight(ctx, db, planner, mediaID)
	if err != nil || preflight == nil {
		return false, err
	}
	created := false
	err = store.WithBusyRetryPolicyContext(ctx, nil, repairRetryPolicy, func(attemptCtx context.Context) error {
		var attemptCreated bool
		attemptCreated, err := repairLegacyMediaOneAttempt(attemptCtx, db, planner, mediaID, preflight)
		if err == nil {
			created = attemptCreated
		}
		return err
	})
	if err != nil && store.IsSQLiteConstraint(err) {
		// Only normalize a concurrent unique/CAS loser after independently
		// observing the winning current repair. Unrelated constraints remain errors.
		current, checkErr := currentRepairCoversRequiredKindsDB(ctx, db, planner, mediaID)
		if checkErr != nil {
			return false, checkErr
		}
		if current {
			return false, nil
		}
	}
	return created, err
}

func currentRepairCoversRequiredKindsDB(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	required, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	covered, err := currentRepairCoversRequiredKindsTx(ctx, tx, mediaID, required)
	if err != nil {
		return false, err
	}
	return covered, tx.Commit()
}

func repairLegacyMediaOneAttempt(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64, preflight *repairPreflight) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT publication_state FROM media WHERE id=? AND status='active' AND file_type IN ('video','image') AND publication_state IN ('published','degraded')`, mediaID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	required, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	covered, err := currentRepairCoversRequiredKindsTx(ctx, tx, mediaID, required)
	if err != nil {
		return false, err
	}
	if covered {
		return false, nil
	}
	complete, err := hasCompleteRepairEvidenceTx(ctx, tx, mediaID, required, preflight)
	if err != nil {
		return false, err
	}
	if complete {
		return false, nil
	}
	result, err := planner.PlanReplacementTx(ctx, tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, PreserveVisibility: preflight.preserveVisibility, ExpectedGeneration: preflight.generation})
	run := result.Run
	if err != nil {
		return false, err
	}
	if run.ID == 0 {
		return false, nil
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func currentRepairCoversRequiredKindsTx(ctx context.Context, tx *sql.Tx, mediaID int64, required []StepType) (bool, error) {
	var runID int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.status FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.media_id=? AND r.reason='repair'`, mediaID).Scan(&runID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status == "published" {
		return false, nil
	}
	for _, step := range required {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_ingest_step WHERE run_id=? AND step_type=? AND required=1)`, runID, step).Scan(&present); err != nil {
			return false, err
		}
		if present == 0 {
			return false, nil
		}
	}
	return true, nil
}

func hasCompleteEvidenceTx(ctx context.Context, tx *sql.Tx, planner *Planner, mediaID int64) (bool, error) {
	steps, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	return hasCompleteEvidenceForStepsTx(ctx, tx, mediaID, steps)
}

func hasCompleteEvidenceForStepsTx(ctx context.Context, tx *sql.Tx, mediaID int64, steps []StepType) (bool, error) {
	for _, step := range steps {
		ok, err := stepEvidenceTx(ctx, tx, mediaID, step)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (p *Planner) requiredStepsTx(ctx context.Context, tx *sql.Tx, mediaID int64) ([]StepType, error) {
	var fileType string
	var encrypted int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(m.file_type,''),COALESCE(l.encrypted_assets_enabled,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&fileType, &encrypted); err != nil {
		return nil, err
	}
	var steps []StepType
	switch strings.TrimSpace(fileType) {
	case "video":
		steps = []StepType{StepPoster}
	case "image":
		steps = []StepType{StepThumbnail}
	default:
		return nil, nil
	}
	if p.options.EncryptGlobal && encrypted == 1 {
		steps = append(steps, StepEncrypt)
	}
	return steps, nil
}

func stepEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64, step StepType) (bool, error) {
	// A terminal successful run is durable intent evidence even if workers later
	// clean task history or an operator removes the physical artifact.
	var done int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id WHERE s.media_id=? AND s.step_type=? AND s.status IN ('done','skipped') AND r.status IN ('published','degraded'))`, mediaID, step).Scan(&done); err != nil {
		return false, err
	}
	if done == 1 {
		return true, nil
	}

	var query string
	switch step {
	case StepPoster:
		query = `SELECT EXISTS(SELECT 1 FROM media_derived_assets WHERE media_id=? AND artifact_kind='poster' AND TRIM(COALESCE(enc_path,''))<>'') OR EXISTS(SELECT 1 FROM media WHERE id=? AND (json_valid(meta_json) AND (TRIM(COALESCE(json_extract(meta_json,'$.scrape.poster'),''))<>'' OR TRIM(COALESCE(json_extract(meta_json,'$.scrape.extra.poster'),''))<>''))) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='poster' AND status='done')`
	case StepThumbnail:
		return thumbnailEvidenceTx(ctx, tx, mediaID)
	case StepScrape:
		var taskDone int
		var metaJSON string
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM scrape_task WHERE media_id=? AND status='done'),COALESCE((SELECT meta_json FROM media WHERE id=?),'')`, mediaID, mediaID).Scan(&taskDone, &metaJSON); err != nil {
			return false, err
		}
		return taskDone == 1 || scraper.HasScrapedMetaJSON(metaJSON), nil
	case StepPreview:
		query = `SELECT EXISTS(SELECT 1 FROM preview_task WHERE media_id=? AND status='done' AND (TRIM(COALESCE(sprite_path,''))<>'' OR TRIM(COALESCE(vtt_path,''))<>'')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='preview' AND status='done')`
	case StepKeyframe:
		query = `SELECT EXISTS(SELECT 1 FROM keyframe_task WHERE media_id=? AND status='done' AND (COALESCE(keyframe_count,0)>0 OR TRIM(COALESCE(output_dir,''))<>'')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='keyframe' AND status='done')`
	case StepSubtitle:
		query = `SELECT EXISTS(SELECT 1 FROM media_subtitle WHERE media_id=? AND status='ready') OR EXISTS(SELECT 1 FROM subtitle_task WHERE media_id=? AND status='done') OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='subtitle' AND status='done')`
	case StepAtrack:
		query = `SELECT EXISTS(SELECT 1 FROM atrack_task WHERE media_id=? AND status='done') OR EXISTS(SELECT 1 FROM media_derived_assets WHERE media_id=? AND artifact_kind IN ('atrack_playlist','atrack_segment')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='atrack' AND status='done')`
	case StepEncrypt:
		return encryptionEvidenceTx(ctx, tx, mediaID)
	case StepPrepare:
		query = `SELECT EXISTS(SELECT 1 FROM transcode_task WHERE file_id=(SELECT file_id FROM media WHERE id=?) AND task_type='pretranscode' AND status='done')`
	default:
		return false, fmt.Errorf("unknown step %q", step)
	}
	args := strings.Count(query, "?")
	values := make([]any, args)
	for i := range values {
		values[i] = mediaID
	}
	if err := tx.QueryRowContext(ctx, query, values...).Scan(&done); err != nil {
		return false, err
	}
	return done == 1, nil
}

func thumbnailEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64) (bool, error) {
	var metaRaw string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'{}') FROM media WHERE id=?`, mediaID).Scan(&metaRaw); err != nil {
		return false, err
	}
	var meta struct {
		Photo struct {
			ThumbPath  string `json:"thumb_path"`
			MediumPath string `json:"medium_path"`
		} `json:"photo"`
	}
	if json.Unmarshal([]byte(metaRaw), &meta) != nil || !regularFile(meta.Photo.ThumbPath) || !regularFile(meta.Photo.MediumPath) {
		return false, nil
	}
	if strings.EqualFold(filepath.Ext(meta.Photo.ThumbPath), ".enc") || strings.EqualFold(filepath.Ext(meta.Photo.MediumPath), ".enc") {
		return encryptedPhotoVariantsTx(ctx, tx, mediaID, meta.Photo.ThumbPath, meta.Photo.MediumPath)
	}
	return true, nil
}

func encryptionEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64) (bool, error) {
	var fileType, encPath, wrapped, iv string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(m.file_type,''),COALESCE(e.enc_path,''),COALESCE(e.wrapped_dek,''),COALESCE(e.iv,'') FROM media m LEFT JOIN media_encrypted_assets e ON e.media_id=m.id AND e.status='encrypted' WHERE m.id=?`, mediaID).Scan(&fileType, &encPath, &wrapped, &iv)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(fileType) != "image" {
		return strings.TrimSpace(encPath) != "", nil
	}
	if !regularFile(encPath) || strings.TrimSpace(wrapped) == "" || strings.TrimSpace(iv) == "" {
		return false, nil
	}
	var metaRaw string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'{}') FROM media WHERE id=?`, mediaID).Scan(&metaRaw); err != nil {
		return false, err
	}
	var meta struct {
		Photo struct {
			ThumbPath  string `json:"thumb_path"`
			MediumPath string `json:"medium_path"`
		} `json:"photo"`
	}
	if json.Unmarshal([]byte(metaRaw), &meta) != nil {
		return false, nil
	}
	return encryptedPhotoVariantsTx(ctx, tx, mediaID, meta.Photo.ThumbPath, meta.Photo.MediumPath)
}

func encryptedPhotoVariantsTx(ctx context.Context, tx *sql.Tx, mediaID int64, thumb, medium string) (bool, error) {
	if !regularFile(thumb) || !regularFile(medium) {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT artifact_kind,COALESCE(enc_path,''),COALESCE(wrapped_dek,''),COALESCE(iv,'') FROM media_derived_assets WHERE media_id=? AND artifact_kind IN ('photo_thumb','photo_medium')`, mediaID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	want := map[string]string{"photo_thumb": filepath.Clean(thumb), "photo_medium": filepath.Clean(medium)}
	seen := map[string]bool{}
	for rows.Next() {
		var kind, path, wrapped, iv string
		if err = rows.Scan(&kind, &path, &wrapped, &iv); err != nil {
			return false, err
		}
		if filepath.Clean(path) == want[kind] && regularFile(path) && strings.TrimSpace(wrapped) != "" && strings.TrimSpace(iv) != "" {
			seen[kind] = true
		}
	}
	return seen["photo_thumb"] && seen["photo_medium"], rows.Err()
}

func regularFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// repairPreflight is computed before opening the replacement transaction so
// source hashing never holds a SQLite writer transaction.
type repairPreflight struct {
	fileType, sourceFingerprint string
	generation                  int64
	preserveVisibility          bool
}

func loadRepairPreflight(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (*repairPreflight, error) {
	var fileType, selected, encPath, plainPath string
	var generation int64
	var libraryEncrypt int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(m.file_type,''),COALESCE(m.file_path,''),m.ingest_generation,COALESCE(l.encrypted_assets_enabled,0),COALESCE(e.enc_path,''),COALESCE(e.plain_path,'') FROM media m JOIN library l ON l.id=m.library_id LEFT JOIN media_encrypted_assets e ON e.media_id=m.id AND e.status='encrypted' WHERE m.id=?`, mediaID).Scan(&fileType, &selected, &generation, &libraryEncrypt, &encPath, &plainPath)
	if err != nil {
		return nil, err
	}
	preflight := &repairPreflight{fileType: strings.TrimSpace(fileType), generation: generation, preserveVisibility: true}
	if preflight.fileType == "image" {
		fingerprintSource := selected
		if regularFile(plainPath) {
			fingerprintSource = plainPath
		}
		preflight.sourceFingerprint, err = SourceFingerprint(fingerprintSource)
		if err != nil {
			return nil, err
		}
		if planner.options.EncryptGlobal && libraryEncrypt == 1 && !samePath(selected, encPath) {
			preflight.preserveVisibility = false
		}
	}
	return preflight, nil
}

func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

// SourceFingerprint binds publication evidence to exact source bytes and identity.
func SourceFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	canonical, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(canonical), info.Size(), info.ModTime().UnixNano(), hex.EncodeToString(h.Sum(nil))), nil
}

func hasCompleteRepairEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64, steps []StepType, preflight *repairPreflight) (bool, error) {
	if preflight.fileType != "image" {
		return hasCompleteEvidenceForStepsTx(ctx, tx, mediaID, steps)
	}
	for _, step := range steps {
		if step == StepThumbnail || step == StepEncrypt {
			ok, err := exactImageEvidenceTx(ctx, tx, mediaID, step, preflight.sourceFingerprint)
			if err != nil || !ok {
				return ok, err
			}
			continue
		}
		ok, err := stepEvidenceTx(ctx, tx, mediaID, step)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func exactImageEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64, step StepType, fingerprint string) (bool, error) {
	var refs string
	err := tx.QueryRowContext(ctx, `SELECT e.artifact_refs_json FROM media_ingest_evidence e JOIN media m ON m.id=e.media_id JOIN media_ingest_run r ON r.id=e.run_id JOIN media_ingest_step s ON s.id=e.step_id WHERE e.media_id=? AND e.generation=m.ingest_generation AND e.kind=? AND e.source_fingerprint=? AND r.status IN ('published','degraded') AND s.status IN ('done','skipped') ORDER BY e.id DESC LIMIT 1`, mediaID, step, fingerprint).Scan(&refs)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return validateImageEvidenceRefsTx(ctx, tx, mediaID, step, refs)
}

func validateImageEvidenceRefsTx(ctx context.Context, tx *sql.Tx, mediaID int64, step StepType, raw string) (bool, error) {
	if step == StepThumbnail {
		var evidence struct {
			Variants []struct {
				Kind, LogicalName, Path, SHA256 string
				Size                            int64
			} `json:"variants"`
		}
		if json.Unmarshal([]byte(raw), &evidence) != nil || len(evidence.Variants) != 2 {
			return false, nil
		}
		for _, kind := range []string{"photo_thumb", "photo_medium"} {
			found := false
			for _, variant := range evidence.Variants {
				if variant.Kind != kind || !validFileHash(variant.Path, variant.Size, variant.SHA256) {
					continue
				}
				var selected string
				var encrypted int
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(l.encrypted_assets_enabled,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&encrypted); err != nil {
					return false, err
				}
				if encrypted == 0 {
					found = true
					continue
				}
				if err := tx.QueryRowContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, mediaID, variant.Kind, variant.LogicalName).Scan(&selected); err == nil && samePath(selected, variant.Path) {
					found = true
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	}
	var evidence struct {
		Path, SHA256, WrappedDEK, IV string
		Size                         int64
	}
	if json.Unmarshal([]byte(raw), &evidence) != nil || evidence.WrappedDEK == "" || evidence.IV == "" || !validFileHash(evidence.Path, evidence.Size, evidence.SHA256) {
		return false, nil
	}
	var selected, wrapped, iv string
	err := tx.QueryRowContext(ctx, `SELECT m.file_path,a.wrapped_dek,a.iv FROM media m JOIN media_encrypted_assets a ON a.media_id=m.id AND a.status='encrypted' AND a.enc_path=m.file_path WHERE m.id=?`, mediaID).Scan(&selected, &wrapped, &iv)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && samePath(selected, evidence.Path) && wrapped == evidence.WrappedDEK && iv == evidence.IV, err
}

func validFileHash(path string, size int64, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return err == nil && n == size && strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want)
}
