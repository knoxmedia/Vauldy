package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

const (
	encryptionStageBatchMax       = 100
	encryptionRecoveryMaxAttempts = 5
)

var reconcileRestoreQuarantinedPlaintext = restoreQuarantinedPlaintextWithOps
var reconcileRestoreAfterMove = func() error { return nil }
var reconcileRestoreAfterSync = func() error { return nil }
var resolveEncryptionQuarantineRoot = encryptionQuarantineRoot

func encryptionQuarantineRoot(source, preferred string) string {
	return storage.QuarantineRootForSource(source, preferred)
}

func ReconcileEncryptionStages(ctx context.Context, db *sql.DB, roots EncryptionRecoveryRoots, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("encryption stage reconcile: database required")
	}
	if roots.Resolver == nil {
		return 0, 0, errors.New("encryption stage reconcile: stage root resolver required")
	}
	if limit <= 0 || limit > encryptionStageBatchMax {
		limit = encryptionStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT j.stage_id,j.task_id,j.retry_round,j.attempt,j.media_id,j.run_id,j.step_id,j.generation,j.owner_token,j.source_path,j.quarantine_path,j.source_fingerprint,j.enc_path,j.wrapped_dek,j.iv,j.enc_sha256,j.enc_size,j.cleanup_plaintext,j.state FROM media_encryption_stage_journal j WHERE (j.state IN ('staged','quarantining','quarantined') OR (j.state='failed_closed' AND j.quarantine_path<>'' AND j.recovery_error LIKE 'restore_pending:%' AND j.recovery_attempts<? AND j.next_retry_at<=CURRENT_TIMESTAMP) OR (j.state='committed' AND j.recovery_attempts<? AND (j.recovery_error='' OR ((j.recovery_error LIKE 'plaintext_cleanup_pending:%' OR j.recovery_error LIKE 'retirement_handoff_pending:%') AND j.next_retry_at<=CURRENT_TIMESTAMP)))) AND (j.state='committed' OR j.state='quarantining' OR NOT EXISTS(SELECT 1 FROM post_ingest_task p WHERE p.id=j.task_id AND p.retry_round=j.retry_round AND p.attempts=j.attempt AND p.status='running' AND p.lease_owner=j.owner_token)) ORDER BY CASE WHEN j.state='failed_closed' THEN j.next_retry_at ELSE j.updated_at END,j.stage_id LIMIT ?`, encryptionRecoveryMaxAttempts, encryptionRecoveryMaxAttempts, limit)
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		stage                                                               string
		task, retryRound, attempt, media, run, step, generation             int64
		owner, source, quarantine, fp, enc, wrapped, iv, hash, state        string
		size                                                                int64
		cleanup                                                             int
	}
	var batch []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.stage, &r.task, &r.retryRound, &r.attempt, &r.media, &r.run, &r.step, &r.generation, &r.owner, &r.source, &r.quarantine, &r.fp, &r.enc, &r.wrapped, &r.iv, &r.hash, &r.size, &r.cleanup, &r.state); err != nil {
			rows.Close()
			return checked, cleaned, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	for _, r := range batch {
		checked++
		safeSource, identityErr := authoritativeEncryptionRestoreSource(ctx, db, r.media, r.source)
		if identityErr != nil || !safeSource {
			if _, err = db.ExecContext(ctx, `UPDATE media SET publication_state='failed',publication_error='unsafe encryption recovery identity' WHERE id=?`, r.media); err != nil {
				return checked, cleaned, err
			}
			if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='unsafe_identity',next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, r.stage); err != nil {
				return checked, cleaned, err
			}
			retErr = errors.Join(retErr, errors.New("unsafe encryption recovery identity"))
			continue
		}
		stageRoot, resolveErr := roots.Resolver.ResolveEncryptionStageRoot(ctx, r.media, r.source)
		if resolveErr != nil {
			return checked, cleaned, resolveErr
		}
		quarantineRoot := resolveEncryptionQuarantineRoot(r.source, roots.Quarantine)
		if !managedEncryptionPath(stageRoot, r.enc) || r.quarantine != "" && !managedEncryptionPath(quarantineRoot, r.quarantine) {
			if _, err = db.ExecContext(ctx, `UPDATE media SET publication_state='failed',publication_error='unsafe encryption recovery path' WHERE id=?`, r.media); err != nil {
				return checked, cleaned, err
			}
			if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='unsafe_path',updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, r.stage); err != nil {
				return checked, cleaned, err
			}
			retErr = errors.Join(retErr, errors.New("unsafe encryption recovery path"))
			continue
		}
		var selected string
		if err = db.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, r.media).Scan(&selected); err != nil {
			return checked, cleaned, err
		}
		if samePathForEvidence(selected, r.enc) {
			var n int
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.id=? AND p.attempts=? AND p.status='done' JOIN media_ingest_step s ON s.id=? AND s.status='done' JOIN media_ingest_run run ON run.id=? AND run.status IN ('published','degraded') WHERE e.stage_id=? AND e.kind='encrypt' AND e.media_id=? AND e.generation=?`, r.task, r.attempt, r.step, r.run, r.stage, r.media, r.generation).Scan(&n)
			if err != nil {
				return checked, cleaned, err
			}
			if n != 1 {
				if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='committed_verification_mismatch',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, r.stage); err != nil {
					return checked, cleaned, err
				}
				retErr = errors.Join(retErr, errors.New("committed encryption recovery mismatch"))
				continue
			}
			// Committed encryption leaves plaintext in place; the community build
			// has no retirement worker, so no cleanup intent is recorded. Mark the
			// committed state as verified so recovery does not retry the stage.
			if err = markCommittedEncryptionVerified(ctx, db, r.stage); err != nil {
				return checked, cleaned, err
			}
			continue
		}
		var active int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND retry_round=(SELECT retry_round FROM media_encryption_stage_journal WHERE stage_id=?) AND attempts=? AND status='running' AND lease_owner=?`, r.task, r.stage, r.attempt, r.owner).Scan(&active); err != nil {
			return checked, cleaned, err
		}
		if active == 1 && r.state != "quarantining" {
			continue
		}
		if r.state == "quarantining" {
			sourceInfo, sourceErr := os.Stat(r.source)
			quarantineInfo, quarantineErr := os.Stat(r.quarantine)
			switch {
			case sourceErr == nil && os.IsNotExist(quarantineErr):
				actual, moveErr := quarantinePlaintext(r.source, quarantineRoot, r.media, r.generation, r.stage)
				if moveErr != nil {
					return checked, cleaned, moveErr
				}
				if !samePathForEvidence(actual, r.quarantine) {
					return checked, cleaned, errors.New("recovery quarantine reservation mismatch")
				}
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			case os.IsNotExist(sourceErr) && quarantineErr == nil:
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			case sourceErr == nil && quarantineErr == nil:
				sourceHash, _ := fileSHA256(r.source)
				quarantineHash, _ := fileSHA256(r.quarantine)
				if sourceHash != quarantineHash {
					return checked, cleaned, errors.New("quarantine duplicate hash mismatch")
				}
				_ = sourceInfo
				_ = quarantineInfo
				_ = os.Remove(r.source)
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			default:
				return checked, cleaned, errors.New("unknown quarantining filesystem state")
			}
		}
		if r.quarantine != "" {
			outcome, restoreErr := reconcilePlaintextRestore(r.quarantine, r.source, quarantineRoot, r.fp, r.media, r.generation, r.stage, defaultEncryptionFileOps())
			if restoreErr != nil {
				if outcome == plaintextRestoreConflict || outcome == plaintextRestoreMissing || errors.Is(restoreErr, errUnsafeEncryptionQuarantinePath) {
					marker := "restore_conflict"
					if outcome == plaintextRestoreMissing {
						marker = "restore_missing"
					}
					if errors.Is(restoreErr, errUnsafeEncryptionQuarantinePath) {
						marker = "unsafe_path"
					}
					if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, marker, r.stage); updateErr != nil {
						return checked, cleaned, updateErr
					}
					retErr = errors.Join(retErr, restoreErr)
					continue
				}
				marker := boundedRecoveryError("restore_pending: ", restoreErr)
				if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=datetime(CURRENT_TIMESTAMP,'+' || min(3600,30*(1 << min(recovery_attempts,7))) || ' seconds'),updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, marker, r.stage); updateErr != nil {
					return checked, cleaned, updateErr
				}
				continue
			}
		}
		_ = os.Remove(r.enc)
		if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='restored',quarantine_path='',recovery_error='stale_restored',updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, r.stage); err != nil {
			return checked, cleaned, err
		}
		cleaned++
	}
	return checked, cleaned, retErr
}

type plaintextRestoreOutcome uint8

const (
	plaintextRestoreComplete plaintextRestoreOutcome = iota
	plaintextRestoreRetry
	plaintextRestoreConflict
	plaintextRestoreMissing
)

type plaintextIdentity struct {
	size int64
	hash string
	algo string
}

func plaintextIdentityFromFingerprint(fingerprint string) (plaintextIdentity, error) {
	algo, digest, ok := publication.FingerprintHash(fingerprint)
	if !ok {
		return plaintextIdentity{}, errors.New("invalid encryption source fingerprint")
	}
	hashAt := strings.LastIndex(fingerprint, "|"+algo+":")
	if hashAt < 0 || hashAt+len(algo)+2 >= len(fingerprint) {
		return plaintextIdentity{}, errors.New("invalid encryption source fingerprint")
	}
	prefix := fingerprint[:hashAt]
	mtimeAt := strings.LastIndex(prefix, "|")
	if mtimeAt < 0 {
		return plaintextIdentity{}, errors.New("invalid encryption source fingerprint")
	}
	sizePrefix := prefix[:mtimeAt]
	sizeAt := strings.LastIndex(sizePrefix, "|")
	if sizeAt < 0 {
		return plaintextIdentity{}, errors.New("invalid encryption source fingerprint")
	}
	size, err := strconv.ParseInt(sizePrefix[sizeAt+1:], 10, 64)
	if err != nil || size < 0 {
		return plaintextIdentity{}, errors.New("invalid encryption source fingerprint")
	}
	return plaintextIdentity{size: size, hash: digest, algo: algo}, nil
}

func regularPlaintextIdentity(path, algo string) (plaintextIdentity, bool, error) {
	info, err := encryptionLstat(path)
	if os.IsNotExist(err) {
		return plaintextIdentity{}, false, nil
	}
	if err != nil {
		return plaintextIdentity{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return plaintextIdentity{}, true, errUnsafeEncryptionQuarantinePath
	}
	var hash string
	switch algo {
	case "imohash":
		hash, err = fileImoHash(path)
	case "sha256":
		hash, err = fileSHA256(path)
	default:
		return plaintextIdentity{}, true, fmt.Errorf("unsupported encryption fingerprint algorithm %q", algo)
	}
	if err != nil {
		return plaintextIdentity{}, true, err
	}
	return plaintextIdentity{size: info.Size(), hash: hash, algo: algo}, true, nil
}

func syncRestoredPlaintext(source, quarantine string, ops encryptionFileOps) error {
	f, err := os.OpenFile(source, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = errors.Join(ops.syncFile(f), f.Close())
	if err != nil {
		return err
	}
	return syncEncryptionParents(ops, source, quarantine)
}

func reconcilePlaintextRestore(quarantine, source, root, fingerprint string, mediaID, generation int64, stageID string, ops encryptionFileOps) (plaintextRestoreOutcome, error) {
	expected, err := plaintextIdentityFromFingerprint(fingerprint)
	if err != nil {
		return plaintextRestoreConflict, err
	}
	qPath, qState, err := validateQuarantineParentLayout(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		return plaintextRestoreRetry, err
	}
	sourceID, sourceExists, err := regularPlaintextIdentity(source, expected.algo)
	if err != nil {
		return plaintextRestoreConflict, err
	}
	var quarantineID plaintextIdentity
	quarantineExists := qState == quarantineLeafExists
	if quarantineExists {
		quarantineID, _, err = regularPlaintextIdentity(qPath, expected.algo)
		if err != nil {
			return plaintextRestoreConflict, err
		}
	}
	sourceExpected := sourceExists && sourceID == expected
	quarantineExpected := quarantineExists && quarantineID == expected
	switch {
	case sourceExists && !quarantineExists:
		if !sourceExpected {
			return plaintextRestoreConflict, errors.New("restored plaintext conflicts with journal identity")
		}
		if err = syncRestoredPlaintext(source, quarantine, ops); err != nil {
			return plaintextRestoreRetry, err
		}
	case sourceExists && quarantineExists:
		if !sourceExpected || !quarantineExpected {
			return plaintextRestoreConflict, errors.New("plaintext restore copies conflict with journal identity")
		}
		if err = syncRestoredPlaintext(source, quarantine, ops); err != nil {
			return plaintextRestoreRetry, err
		}
		if err = ops.remove(qPath); err != nil && !os.IsNotExist(err) {
			return plaintextRestoreRetry, err
		}
		if err = ops.syncDir(filepath.Dir(qPath)); err != nil {
			return plaintextRestoreRetry, err
		}
	case !sourceExists && quarantineExists:
		if !quarantineExpected {
			return plaintextRestoreConflict, errors.New("quarantined plaintext conflicts with journal identity")
		}
		ops.afterMove = reconcileRestoreAfterMove
		if err = reconcileRestoreQuarantinedPlaintext(qPath, source, root, mediaID, generation, stageID, ops); err != nil {
			return plaintextRestoreRetry, err
		}
		if err = syncRestoredPlaintext(source, quarantine, ops); err != nil {
			return plaintextRestoreRetry, err
		}
	default:
		return plaintextRestoreMissing, errors.New("plaintext restore source and quarantine missing")
	}
	if err = reconcileRestoreAfterSync(); err != nil {
		return plaintextRestoreRetry, err
	}
	return plaintextRestoreComplete, nil
}

type committedCleanupOutcome uint8

const (
	committedCleanupVerified committedCleanupOutcome = iota
	committedCleanupUnsafe
	committedCleanupRetry
)

func recordCommittedCleanupOutcome(ctx context.Context, db *sql.DB, stageID string, outcome committedCleanupOutcome, cleanupErr error) error {
	var err error
	switch outcome {
	case committedCleanupVerified:
		_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error='verified_committed',next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, stageID)
	case committedCleanupUnsafe:
		_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='unsafe_identity',next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, stageID)
	case committedCleanupRetry:
		marker := boundedRecoveryError("plaintext_cleanup_pending: ", cleanupErr)
		_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=datetime(CURRENT_TIMESTAMP,'+' || min(3600,30*(1 << min(recovery_attempts,7))) || ' seconds'),updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, marker, stageID)
	default:
		return errors.New("unknown committed cleanup outcome")
	}
	return err
}

func markCommittedEncryptionVerified(ctx context.Context, db *sql.DB, stageID string) error {
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error='verified_committed',next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, stageID)
		return e
	})
	if err != nil {
		pending := boundedRecoveryError("committed_marker_pending: ", err)
		_, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=datetime(CURRENT_TIMESTAMP,'+' || min(3600,30*(1 << min(recovery_attempts,7))) || ' seconds'),updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, pending, stageID)
		return errors.Join(err, updateErr)
	}
	return nil
}

func cleanupCommittedEncryptionPlaintext(root string, mediaID, generation int64, stageID, quarantine string, ops encryptionFileOps) (committedCleanupOutcome, error) {
	if quarantine == "" {
		return committedCleanupVerified, nil
	}
	expected, leaf, err := validateQuarantineParentLayout(root, quarantine, mediaID, generation, stageID)
	if err != nil {
		if errors.Is(err, errUnsafeEncryptionQuarantinePath) {
			return committedCleanupUnsafe, err
		}
		return committedCleanupRetry, err
	}
	if leaf == quarantineLeafExists {
		if err = ops.remove(expected); err != nil && !os.IsNotExist(err) {
			return committedCleanupRetry, err
		}
	}
	// Sync even when the leaf is already absent: this is the crash-after-remove
	// recovery case and makes the directory entry deletion durable/idempotent.
	if err = ops.syncDir(filepath.Dir(expected)); err != nil {
		return committedCleanupRetry, err
	}
	return committedCleanupVerified, nil
}

func authoritativeEncryptionRestoreSource(ctx context.Context, db *sql.DB, mediaID int64, source string) (bool, error) {
	var selected, libraryRoot string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(m.file_path,''),COALESCE(l.path,'') FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&selected, &libraryRoot); err != nil {
		return false, err
	}
	var plain string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(plain_path,'') FROM media_encrypted_assets WHERE media_id=?`, mediaID).Scan(&plain)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(plain) != "" && !samePathForEvidence(source, plain) {
		return false, nil
	}
	if strings.TrimSpace(plain) == "" && !strings.HasSuffix(strings.ToLower(strings.TrimSpace(selected)), ".enc") && !samePathForEvidence(source, selected) {
		return false, nil
	}
	roots := []string{libraryRoot}
	rows, err := db.QueryContext(ctx, `SELECT path FROM library_folder WHERE library_id=(SELECT library_id FROM media WHERE id=?)`, mediaID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var root string
		if err = rows.Scan(&root); err != nil {
			rows.Close()
			return false, err
		}
		roots = append(roots, root)
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	for _, root := range roots {
		if safeEncryptionPlainPath(root, source) {
			return true, nil
		}
	}
	return false, nil
}

func safeEncryptionPlainPath(root, path string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil || !pathWithinRoot(rootAbs, pathAbs) {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "." {
		return false
	}
	reserved := map[string]bool{".encrypted": true, ".quarantine": true, "quarantine": true, "derived": true, "metadata": true, "upload": true, "uploads": true}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' }) {
		if reserved[strings.ToLower(part)] {
			return false
		}
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false
	}
	parent := filepath.Dir(pathAbs)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			return pathWithinRoot(rootResolved, resolved)
		}
		next := filepath.Dir(parent)
		if next == parent || !pathWithinRoot(rootAbs, next) {
			return false
		}
		parent = next
	}
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func managedEncryptionPath(root, path string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	ra, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	pa, e := filepath.Abs(path)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(ra, pa)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func RunEncryptionStageReconciler(ctx context.Context, db *sql.DB, roots EncryptionRecoveryRoots, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, _, err := ReconcileEncryptionStages(ctx, db, roots, limit)
		if err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
