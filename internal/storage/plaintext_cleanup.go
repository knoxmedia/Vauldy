package storage

import (
	"database/sql"
	"strings"
	"time"
)

// mediaPlaintextBusy is set from main (JIT session manager) to detect live ffmpeg consumers.
var mediaPlaintextBusy func(mediaID int64) bool

// SetMediaPlaintextBusy registers a callback that reports active plaintext readers (e.g. JIT).
func SetMediaPlaintextBusy(fn func(mediaID int64) bool) {
	mediaPlaintextBusy = fn
}

// HasActivePlaintextConsumer reports whether preview/package/keyframe work or a
// registered JIT session still holds the plaintext source for mediaID.
// Retirement barrier uses this read-only predicate; deletion belongs to retirement.
func HasActivePlaintextConsumer(db *sql.DB, mediaID int64) bool {
	return plaintextConsumersBusy(db, mediaID)
}

// LibraryWantsPlaintextCleanup reports the library cleanup_plaintext policy.
func LibraryWantsPlaintextCleanup(db *sql.DB, mediaID int64) bool {
	return libraryWantsPlainCleanup(db, mediaID)
}

func plaintextConsumersBusy(db *sql.DB, mediaID int64) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	var previewStatus, packageStatus, keyframeStatus sql.NullString
	_ = db.QueryRow(`
		SELECT (SELECT status FROM preview_task WHERE media_id = ? LIMIT 1),
		       (SELECT status FROM package_task WHERE media_id = ? ORDER BY id DESC LIMIT 1),
		       (SELECT status FROM keyframe_task WHERE media_id = ? LIMIT 1)
	`, mediaID, mediaID, mediaID).Scan(&previewStatus, &packageStatus, &keyframeStatus)
	if previewStatus.Valid {
		switch strings.ToLower(previewStatus.String) {
		case "running", "processing":
			return true
		}
	}
	if packageStatus.Valid {
		switch strings.ToLower(packageStatus.String) {
		case "running":
			return true
		}
	}
	if keyframeStatus.Valid {
		switch strings.ToLower(keyframeStatus.String) {
		case "running":
			return true
		}
	}
	if mediaPlaintextBusy != nil && mediaPlaintextBusy(mediaID) {
		return true
	}
	return false
}

// WaitForPlaintextConsumers blocks until preview/package/keyframe tasks and live JIT sessions
// finish, or timeout elapses. Used before encryption starts; does not delete sources.
func WaitForPlaintextConsumers(db *sql.DB, mediaID int64, timeout time.Duration) {
	if db == nil || mediaID <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !plaintextConsumersBusy(db, mediaID) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func libraryWantsPlainCleanup(db *sql.DB, mediaID int64) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	var cleanup int
	err := db.QueryRow(`
		SELECT COALESCE(l.encrypted_assets_cleanup_plaintext, 0)
		FROM media m
		JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&cleanup)
	return err == nil && cleanup == 1
}
