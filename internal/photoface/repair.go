package photoface

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"knox-media/internal/atomicfile"
	"knox-media/internal/storage"
)

const repairStateName = "singleton"

func (w *Worker) EnsureRepairSchema(ctx context.Context) error {
	if w == nil || w.DB == nil {
		return fmt.Errorf("worker unavailable")
	}
	_, err := w.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS photo_face_thumb_repair_state (
        name TEXT PRIMARY KEY, phase TEXT NOT NULL DEFAULT 'covers', last_person_id INTEGER NOT NULL DEFAULT 0, last_face_id INTEGER NOT NULL DEFAULT 0, completed_at TIMESTAMP, next_audit_at TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
        CREATE TABLE IF NOT EXISTS photo_face_thumb_repair_failure (face_id INTEGER PRIMARY KEY, person_id INTEGER, attempts INTEGER NOT NULL DEFAULT 1, next_retry_at TIMESTAMP NOT NULL, last_error TEXT, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(face_id) REFERENCES photo_face(id) ON DELETE CASCADE);
        CREATE INDEX IF NOT EXISTS idx_photo_face_thumb_repair_failure_due ON photo_face_thumb_repair_failure(next_retry_at,face_id)`)
	return err
}

// RepairMissingThumbnails incrementally repairs historical person cover thumbnails
// before scanning every face. The persistent keyset cursor keeps every call bounded.
func (w *Worker) RepairMissingThumbnails(ctx context.Context, limit int) (checked, repaired, failed int, err error) {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0, 0, 0, nil
	}
	if err = ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	w.repairMu.Lock()
	defer w.repairMu.Unlock()
	if err = ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	if err = w.EnsureRepairSchema(ctx); err != nil {
		return
	}

	var phase string
	var lastPerson, lastFace int64
	var nextAudit sql.NullTime
	scanErr := w.DB.QueryRowContext(ctx, `SELECT phase,last_person_id,last_face_id,next_audit_at FROM photo_face_thumb_repair_state WHERE name=?`, repairStateName).Scan(&phase, &lastPerson, &lastFace, &nextAudit)
	if errors.Is(scanErr, sql.ErrNoRows) {
		phase = "covers"
	} else if scanErr != nil {
		err = scanErr
		return
	}
	if phase == "complete" {
		if nextAudit.Valid && time.Now().Before(nextAudit.Time) {
			var did bool
			did, repaired, failed, err = w.retryDueFailure(ctx)
			if did {
				checked = repaired + failed
			}
			return
		}
		if err = w.saveRepairState(ctx, "covers", 0, 0); err != nil {
			return
		}
		phase, lastPerson, lastFace = "covers", 0, 0
	}

	if phase == "covers" {
		var ids []int64
		rows, qerr := w.DB.QueryContext(ctx, `SELECT id FROM photo_person WHERE id>? ORDER BY id LIMIT ?`, lastPerson, limit)
		if qerr != nil {
			err = qerr
			return
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		qerr = rows.Err()
		rows.Close()
		if qerr != nil {
			err = qerr
			return
		}
		if len(ids) == 0 {
			if err = w.saveRepairState(ctx, "all_faces", 0, lastFace); err != nil {
				return
			}
			phase = "all_faces"
		} else {
			for _, personID := range ids {
				if err = ctx.Err(); err != nil {
					return
				}
				var face int64
				faceErr := w.DB.QueryRowContext(ctx, `SELECT id FROM photo_face WHERE person_id=? ORDER BY quality DESC,id ASC LIMIT 1`, personID).Scan(&face)
				if faceErr != nil && !errors.Is(faceErr, sql.ErrNoRows) {
					err = faceErr
					return
				}
				if face > 0 {
					checked++
					didRepair, itemErr := w.repairFaceThumbnail(ctx, face, "", 0)
					if itemErr != nil {
						if ctx.Err() != nil {
							err = ctx.Err()
							return
						}
						failed++
						if err = w.recordRepairFailureAndAdvance(ctx, face, personID, itemErr, "covers", personID, lastFace); err != nil {
							return
						}
						continue
					}
					if didRepair {
						repaired++
					}
				}
				if _, err = w.repairPersonCover(ctx, personID, "covers", lastFace); err != nil {
					return
				}
			}
			return
		}
	}

	if phase == "all_faces" {
		type candidate struct{ id int64 }
		var candidates []candidate
		rows, qerr := w.DB.QueryContext(ctx, `SELECT id FROM photo_face WHERE id>? ORDER BY id LIMIT ?`, lastFace, limit)
		if qerr != nil {
			err = qerr
			return
		}
		for rows.Next() {
			var c candidate
			if rows.Scan(&c.id) == nil {
				candidates = append(candidates, c)
			}
		}
		qerr = rows.Err()
		rows.Close()
		if qerr != nil {
			err = qerr
			return
		}
		if len(candidates) == 0 {
			var did bool
			did, repaired, failed, err = w.retryDueFailure(ctx)
			if err != nil || did {
				checked = repaired + failed
				return
			}
			err = w.completeRepairState(ctx)
			return
		}
		for _, c := range candidates {
			if err = ctx.Err(); err != nil {
				return
			}
			checked++
			didRepair, itemErr := w.repairFaceThumbnail(ctx, c.id, "all_faces", lastPerson)
			if itemErr != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
					return
				}
				failed++
				if err = w.recordRepairFailureAndAdvance(ctx, c.id, 0, itemErr, "all_faces", lastPerson, c.id); err != nil {
					return
				}
			} else if didRepair {
				repaired++
			}

		}
	}
	return
}

func (w *Worker) repairPersonCover(ctx context.Context, personID int64, cursorPhase string, lastFace int64) (int64, error) {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var faceID int64
	scanErr := tx.QueryRowContext(ctx, `SELECT id FROM photo_face WHERE person_id=? ORDER BY quality DESC,id ASC LIMIT 1`, personID).Scan(&faceID)
	if errors.Is(scanErr, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `DELETE FROM photo_person WHERE id=?`, personID); err != nil {
			return 0, err
		}
	} else if scanErr != nil {
		return 0, scanErr
	} else {
		var faceCount, mediaCount int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT media_id) FROM photo_face WHERE person_id=?`, personID).Scan(&faceCount, &mediaCount); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE photo_person SET cover_face_id=?,face_count=?,media_count=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, faceID, faceCount, mediaCount, personID); err != nil {
			return 0, err
		}
	}
	if err = saveRepairStateTx(ctx, tx, cursorPhase, personID, lastFace); err != nil {
		return 0, err
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return faceID, nil
}

type thumbnailRetryError struct{ err error }

func (e *thumbnailRetryError) Error() string { return e.err.Error() }
func (e *thumbnailRetryError) Unwrap() error { return e.err }

func (w *Worker) advanceFaceCursor(ctx context.Context, phase string, lastPerson, faceID int64) error {
	if phase == "" {
		return nil
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveRepairStateTx(ctx, tx, phase, lastPerson, faceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) repairFaceThumbnail(ctx context.Context, faceID int64, cursorPhase string, lastPerson int64) (bool, error) {
	var mediaID, libraryID int64
	var x, y, width, height float64
	var src string
	if err := w.DB.QueryRowContext(ctx, `SELECT f.media_id,f.library_id,f.bbox_x,f.bbox_y,f.bbox_w,f.bbox_h,m.file_path FROM photo_face f JOIN media m ON m.id=f.media_id WHERE f.id=?`, faceID).Scan(&mediaID, &libraryID, &x, &y, &width, &height, &src); err != nil {
		return false, err
	}
	target := ExpectedFaceThumbnailPath(w.photoCacheDir(), faceID)
	plainUsable := ValidateFaceJPEG(target) == nil
	if storage.NeedsDerivedEncryption(w.DB, mediaID) {
		if _, ok := storage.LookupEncPath(w.DB, mediaID, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(faceID)); ok {
			if e := w.validateEncryptedFaceArtifact(ctx, mediaID, faceID); e == nil {
				if plainUsable {
					if e = os.Remove(target); e != nil && !os.IsNotExist(e) {
						return false, &thumbnailRetryError{e}
					}
				}
				if e := w.advanceFaceCursor(ctx, cursorPhase, lastPerson, faceID); e != nil {
					return false, e
				}
				return false, nil
			}
		}
		if plainUsable {
			data, e := os.ReadFile(target)
			if e != nil {
				return false, &thumbnailRetryError{e}
			}
			tx, e := w.DB.BeginTx(ctx, nil)
			if e != nil {
				return false, &thumbnailRetryError{e}
			}
			defer tx.Rollback()
			finalize, e := w.commitFaceThumbnail(ctx, tx, mediaID, faceID, data)
			if e != nil {
				return false, &thumbnailRetryError{e}
			}
			committed := false
			defer func() { finalize(committed) }()
			if e = ctx.Err(); e != nil {
				return false, e
			}
			if e = tx.Commit(); e != nil {
				return false, &thumbnailRetryError{e}
			}
			committed = true
			if e = os.Remove(target); e != nil && !os.IsNotExist(e) {
				return false, &thumbnailRetryError{e}
			}
			if e = w.advanceFaceCursor(ctx, cursorPhase, lastPerson, faceID); e != nil {
				return false, e
			}
			return true, nil
		}
	} else if plainUsable {
		if err := w.advanceFaceCursor(ctx, cursorPhase, lastPerson, faceID); err != nil {
			return false, err
		}
		return false, nil
	}
	detectPath, cleanup, err := w.ensureDetectImage(ctx, mediaID, libraryID, src)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return false, err
	}
	data, err := CropFaceJPEG(detectPath, x, y, width, height, 88)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	finalize, err := w.commitFaceThumbnail(ctx, tx, mediaID, faceID, data)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() { finalize(committed) }()
	if cursorPhase != "" {
		if err = saveRepairStateTx(ctx, tx, cursorPhase, lastPerson, faceID); err != nil {
			return false, err
		}
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

func (w *Worker) recordRepairFailureAndAdvance(ctx context.Context, faceID, personID int64, cause error, phase string, lastPerson, lastFace int64) error {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO photo_face_thumb_repair_failure(face_id,person_id,attempts,next_retry_at,last_error,updated_at) VALUES(?,?,1,datetime('now','+1 minute'),?,CURRENT_TIMESTAMP) ON CONFLICT(face_id) DO UPDATE SET person_id=COALESCE(excluded.person_id,person_id),attempts=attempts+1,next_retry_at=datetime('now','+'||MIN(1440,(1 << MIN(10,attempts)))||' minutes'),last_error=excluded.last_error,updated_at=CURRENT_TIMESTAMP`, faceID, nilIfZero(personID), cause.Error())
	if err != nil {
		return err
	}
	if err = saveRepairStateTx(ctx, tx, phase, lastPerson, lastFace); err != nil {
		return err
	}
	return tx.Commit()
}
func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func (w *Worker) retryDueFailure(ctx context.Context) (did bool, repaired, failed int, err error) {
	var faceID int64
	var person sql.NullInt64
	err = w.DB.QueryRowContext(ctx, `SELECT face_id,person_id FROM photo_face_thumb_repair_failure WHERE next_retry_at<=CURRENT_TIMESTAMP ORDER BY next_retry_at,face_id LIMIT 1`).Scan(&faceID, &person)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, err
	}
	did = true
	ok, itemErr := w.repairFaceThumbnail(ctx, faceID, "", 0)
	if itemErr != nil {
		if ctx.Err() != nil {
			return true, 0, 0, ctx.Err()
		}
		_, err = w.DB.ExecContext(ctx, `UPDATE photo_face_thumb_repair_failure SET attempts=attempts+1,next_retry_at=datetime('now','+'||MIN(1440,(1 << MIN(10,attempts)))||' minutes'),last_error=?,updated_at=CURRENT_TIMESTAMP WHERE face_id=?`, itemErr.Error(), faceID)
		return true, 0, 1, err
	}
	if person.Valid {
		if _, err = w.repairPersonCover(ctx, person.Int64, "", 0); err != nil {
			return true, 0, 1, err
		}
	}
	_, err = w.DB.ExecContext(ctx, `DELETE FROM photo_face_thumb_repair_failure WHERE face_id=?`, faceID)
	if err != nil {
		return true, 0, 0, err
	}
	if ok {
		return true, 1, 0, nil
	}
	return true, 0, 0, nil
}

func (w *Worker) completeRepairState(ctx context.Context) error {
	hours := 24
	if w.Cfg != nil && w.Cfg().ThumbnailRepairAuditHours > 0 {
		hours = w.Cfg().ThumbnailRepairAuditHours
	}
	_, err := w.DB.ExecContext(ctx, `INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id,completed_at,next_audit_at,updated_at) VALUES(?,'complete',0,0,CURRENT_TIMESTAMP,datetime('now', printf('+%d hours', ?)),CURRENT_TIMESTAMP) ON CONFLICT(name) DO UPDATE SET phase='complete',last_person_id=0,last_face_id=0,completed_at=CURRENT_TIMESTAMP,next_audit_at=datetime('now', printf('+%d hours', ?)),updated_at=CURRENT_TIMESTAMP`, repairStateName, hours, hours)
	return err
}

func saveRepairStateTx(ctx context.Context, tx *sql.Tx, phase string, lastPerson, lastFace int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id,updated_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(name) DO UPDATE SET phase=excluded.phase,last_person_id=excluded.last_person_id,last_face_id=excluded.last_face_id,updated_at=CURRENT_TIMESTAMP`, repairStateName, phase, lastPerson, lastFace)
	return err
}

func (w *Worker) saveRepairState(ctx context.Context, phase string, lastPerson, lastFace int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := w.DB.ExecContext(ctx, `INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id,updated_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP)
        ON CONFLICT(name) DO UPDATE SET phase=excluded.phase,last_person_id=excluded.last_person_id,last_face_id=excluded.last_face_id,updated_at=CURRENT_TIMESTAMP`, repairStateName, phase, lastPerson, lastFace)
	return err
}

const FaceThumbnailArtifactKind = "photo_face_thumb"

func FaceThumbnailLogicalName(faceID int64) string { return fmt.Sprintf("face:%d", faceID) }

func (w *Worker) commitFaceThumbnail(ctx context.Context, tx *sql.Tx, mediaID, faceID int64, data []byte) (func(bool), error) {
	if storage.NeedsDerivedEncryption(w.DB, mediaID) {
		if w.Derived == nil || tx == nil {
			return nil, fmt.Errorf("encrypted face thumbnail store unavailable")
		}
		staged, err := w.Derived.StageBytes(ctx, mediaID, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(faceID), data)
		if err != nil {
			return nil, err
		}
		old, err := w.Derived.CommitStagedTx(ctx, tx, staged)
		if err != nil {
			w.Derived.AbortStaged(staged)
			return nil, err
		}
		if w.afterThumbnailCommit != nil {
			w.afterThumbnailCommit()
		}
		if err := ctx.Err(); err != nil {
			w.Derived.AbortStaged(staged)
			return nil, err
		}
		return func(committed bool) {
			if committed {
				w.Derived.CleanupReplaced(old)
			} else {
				w.Derived.AbortStaged(staged)
			}
		}, nil
	}
	staged, err := atomicfile.Stage(ctx, ExpectedFaceThumbnailPath(w.photoCacheDir(), faceID), data, 0o644)
	if err != nil {
		return nil, err
	}
	if err = staged.Publish(ctx); err != nil {
		staged.Rollback()
		return nil, err
	}
	if w.afterThumbnailCommit != nil {
		w.afterThumbnailCommit()
	}
	if err = ctx.Err(); err != nil {
		staged.Rollback()
		return nil, err
	}
	return func(committed bool) {
		if committed {
			staged.Commit()
		} else {
			staged.Rollback()
		}
	}, nil
}
