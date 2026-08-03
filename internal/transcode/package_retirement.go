package transcode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

var (
	errPackageRetirementConflict      = errors.New("package retirement: conflicting active retirement")
	errPackageRetirementSchemaMissing = errors.New("package retirement: publication/retirement schema missing")
	errPackageRetirementGeneration    = errors.New("package retirement: generation changed during packaging")
	errPackageRetirementSource        = errors.New("package retirement: packaged source unreadable")
)

// packageRetirementRequest identifies a package-basis plaintext retirement intent.
type packageRetirementRequest struct {
	MediaID           int64
	PackageTaskID     int64
	RunID             int64
	Generation        int64
	SourcePath        string
	SourceFingerprint string
}

// upsertPackageRetirementIntentTx creates or refreshes the generation-scoped
// plaintext retirement row for a package basis.
//
// Updatable states (blocked/ready/retryable_failed) are rewritten with the new
// package basis identity. Active in-flight/terminal states succeed only when the
// existing row already matches the same package_task_id/basis/fingerprint
// (idempotent; state is not regressed). Any other conflict fails closed.
func upsertPackageRetirementIntentTx(ctx context.Context, tx store.SQLExecutor, req packageRetirementRequest) error {
	if tx == nil {
		return fmt.Errorf("package retirement: nil tx")
	}
	if req.MediaID <= 0 || req.PackageTaskID <= 0 || req.RunID <= 0 || req.Generation <= 0 {
		return fmt.Errorf("package retirement: invalid identity")
	}
	if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.SourceFingerprint) == "" {
		return fmt.Errorf("package retirement: missing source identity")
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement(
  media_id,run_id,generation,source_path,source_fingerprint,
  basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,
  state,quarantine_evidence_json,blocked_at,updated_at
) VALUES(?,?,?,?,?,'package',?,NULL,?,0,'blocked','{}',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(media_id,generation) DO UPDATE SET
  run_id=excluded.run_id,
  source_path=excluded.source_path,
  source_fingerprint=excluded.source_fingerprint,
  basis_kind='package',
  basis_id=excluded.basis_id,
  encryption_stage_id=NULL,
  package_task_id=excluded.package_task_id,
  retry_round=excluded.retry_round,
  updated_at=CURRENT_TIMESTAMP
WHERE media_plaintext_retirement.state IN ('blocked','ready','retryable_failed')`,
		req.MediaID, req.RunID, req.Generation, req.SourcePath, req.SourceFingerprint,
		req.PackageTaskID, req.PackageTaskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	var (
		existingKind, existingFP, existingState string
		existingBasis, existingPkg              int64
	)
	err = tx.QueryRowContext(ctx, `
SELECT basis_kind,basis_id,COALESCE(package_task_id,0),source_fingerprint,state
FROM media_plaintext_retirement WHERE media_id=? AND generation=?`,
		req.MediaID, req.Generation).Scan(&existingKind, &existingBasis, &existingPkg, &existingFP, &existingState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("package retirement: upsert affected no rows")
	}
	if err != nil {
		return err
	}
	switch existingState {
	case "quarantining", "quarantined", "deleting", "verified", "operator_required":
		if existingKind == "package" && existingBasis == req.PackageTaskID && existingPkg == req.PackageTaskID && existingFP == req.SourceFingerprint {
			_, _ = tx.ExecContext(ctx, `UPDATE media_plaintext_retirement SET updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND generation=? AND package_task_id=? AND state=?`,
				req.MediaID, req.Generation, req.PackageTaskID, existingState)
			return nil
		}
		return fmt.Errorf("%w: state=%s basis=%d", errPackageRetirementConflict, existingState, existingBasis)
	default:
		return fmt.Errorf("%w: unexpected state=%s", errPackageRetirementConflict, existingState)
	}
}

// readMediaIngestGeneration returns media.ingest_generation. Missing publication
// schema surfaces as errPackageRetirementSchemaMissing.
func readMediaIngestGeneration(ctx context.Context, q store.SQLExecutor, mediaID int64) (int64, error) {
	if q == nil || mediaID <= 0 {
		return 0, fmt.Errorf("package retirement: invalid media identity")
	}
	var gen int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, mediaID).Scan(&gen)
	if err != nil {
		if isMissingPublicationSchemaErr(err) {
			return 0, errPackageRetirementSchemaMissing
		}
		return 0, err
	}
	return gen, nil
}

// resolveAuthoritativePackageRetirement looks up the current non-superseded
// ingest generation for media and fingerprints the packaged source path.
//
// packagedGenerationOK distinguishes a successfully captured start generation
// (including 0) from an unknown/failed capture. When OK is true, any mid-flight
// change away from packagedGeneration fails closed — including 0→N.
//
// Outcomes when err == nil:
//   - ok=true: upsert this request
//   - ok=false: schema present but no authoritative current run (sql.ErrNoRows);
//     leave package done+pending without inventing a generation
//
// Fail-closed errors:
//   - missing publication/retirement schema
//   - packagedGenerationOK and media/run generation differs from packaged baseline
//   - packaged source missing/unreadable for fingerprint
func resolveAuthoritativePackageRetirement(ctx context.Context, q store.SQLExecutor, mediaID int64, sourcePath string, packagedGeneration int64, packagedGenerationOK bool) (packageRetirementRequest, bool, error) {
	var req packageRetirementRequest
	if q == nil || mediaID <= 0 {
		return req, false, fmt.Errorf("package retirement: invalid media identity")
	}

	// Probe retirement table so cleanup-pending cannot succeed on schema-less DBs.
	var probe int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM media_plaintext_retirement LIMIT 1`).Scan(&probe); err != nil && !errors.Is(err, sql.ErrNoRows) {
		if isMissingPublicationSchemaErr(err) {
			return req, false, errPackageRetirementSchemaMissing
		}
		return req, false, err
	}

	var runID, generation, mediaGen int64
	err := q.QueryRowContext(ctx, `
SELECT r.id, r.generation, m.ingest_generation
FROM media m
JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation
WHERE m.id=?
  AND m.ingest_generation>0
  AND (r.superseded_at IS NULL OR TRIM(COALESCE(r.superseded_at,''))='')
LIMIT 1`, mediaID).Scan(&runID, &generation, &mediaGen)
	if isMissingPublicationSchemaErr(err) {
		return req, false, errPackageRetirementSchemaMissing
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Schema present; no matching authoritative generation — intentional skip
		// when still at the packaged baseline (including captured 0).
		if packagedGenerationOK {
			cur, curErr := readMediaIngestGeneration(ctx, q, mediaID)
			if curErr != nil {
				return req, false, curErr
			}
			if cur != packagedGeneration {
				return req, false, fmt.Errorf("%w: packaged=%d current=%d", errPackageRetirementGeneration, packagedGeneration, cur)
			}
		}
		return req, false, nil
	}
	if err != nil {
		return req, false, err
	}
	if packagedGenerationOK && (generation != packagedGeneration || mediaGen != packagedGeneration) {
		return req, false, fmt.Errorf("%w: packaged=%d run=%d media=%d", errPackageRetirementGeneration, packagedGeneration, generation, mediaGen)
	}

	src := strings.TrimSpace(sourcePath)
	if src == "" {
		return req, false, fmt.Errorf("%w: empty source path", errPackageRetirementSource)
	}
	fp, err := storage.EncryptionSourceFingerprint(src)
	if err != nil {
		return req, false, fmt.Errorf("%w: %v", errPackageRetirementSource, err)
	}
	req = packageRetirementRequest{
		MediaID: mediaID, RunID: runID, Generation: generation,
		SourcePath: src, SourceFingerprint: fp,
	}
	return req, true, nil
}

func isMissingPublicationSchemaErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column")
}

// packageHandoffHook is an optional test seam invoked immediately before
// retirement resolve/upsert so mid-flight generation replace can be simulated.
// The callback must use the provided executor (open handoff tx), not a parallel
// *sql.DB connection, to avoid SQLite write-lock deadlocks.
var packageHandoffHook func(mediaID int64, tx store.SQLExecutor)
