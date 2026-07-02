package storage

import (
	"database/sql"
	"log"
	"os"
	"strings"
	"time"
)

// mediaPlaintextBusy is set from main (JIT session manager) to detect live ffmpeg consumers.
var mediaPlaintextBusy func(mediaID int64) bool

// SetMediaPlaintextBusy registers a callback that reports active plaintext readers (e.g. JIT).
func SetMediaPlaintextBusy(fn func(mediaID int64) bool) {
	mediaPlaintextBusy = fn
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
// finish, or timeout elapses.
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

func removePlaintextFile(path string) error {
	const attempts = 8
	for i := 0; i < attempts; i++ {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if i == attempts-1 {
			return err
		}
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	return nil
}

func cleanupPlaintextAfterEncrypt(db *sql.DB, mediaID int64, plainPath string) {
	plainPath = strings.TrimSpace(plainPath)
	if plainPath == "" {
		return
	}
	WaitForPlaintextConsumers(db, mediaID, 10*time.Minute)
	if err := removePlaintextFile(plainPath); err != nil {
		log.Printf("asset encrypt: cleanup plain media=%d path=%s err=%v", mediaID, plainPath, err)
		schedulePlaintextCleanup(db, mediaID, plainPath)
		return
	}
	log.Printf("asset encrypt: removed plaintext media=%d path=%s", mediaID, plainPath)
}

func schedulePlaintextCleanup(db *sql.DB, mediaID int64, plainPath string) {
	go func() {
		const maxAttempts = 40 // ~20 minutes at 30s
		for attempt := 0; attempt < maxAttempts; attempt++ {
			time.Sleep(30 * time.Second)
			if !libraryWantsPlainCleanup(db, mediaID) {
				return
			}
			if _, err := os.Stat(plainPath); os.IsNotExist(err) {
				return
			}
			WaitForPlaintextConsumers(db, mediaID, 2*time.Minute)
			if err := removePlaintextFile(plainPath); err == nil {
				log.Printf("asset encrypt: removed plaintext media=%d path=%s (deferred)", mediaID, plainPath)
				return
			}
		}
		log.Printf("asset encrypt: cleanup plain gave up media=%d path=%s", mediaID, plainPath)
	}()
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

// KickPendingPlaintextCleanups retries plaintext deletion for encrypted media whose library
// still has cleanup enabled but the source file was left on disk (e.g. Windows file lock).
func KickPendingPlaintextCleanups(db *sql.DB) {
	if db == nil {
		return
	}
	rows, err := db.Query(`
		SELECT e.media_id, e.plain_path
		FROM media_encrypted_assets e
		JOIN media m ON m.id = e.media_id
		JOIN library l ON l.id = m.library_id
		WHERE e.status = 'encrypted'
		  AND COALESCE(l.encrypted_assets_cleanup_plaintext, 0) = 1
		  AND TRIM(COALESCE(e.plain_path, '')) != ''
	`)
	if err != nil {
		log.Printf("asset encrypt: pending plaintext cleanup query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var plainPath string
		if err := rows.Scan(&mediaID, &plainPath); err != nil {
			continue
		}
		plainPath = strings.TrimSpace(plainPath)
		if plainPath == "" {
			continue
		}
		if _, err := os.Stat(plainPath); os.IsNotExist(err) {
			continue
		}
		schedulePlaintextCleanup(db, mediaID, plainPath)
	}
}
