package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FindMediaIDByEncryptedPlainPath returns media id when path is the plaintext side of an encrypted asset.
func FindMediaIDByEncryptedPlainPath(db *sql.DB, libraryID int64, plainPath string) int64 {
	if db == nil || libraryID <= 0 {
		return 0
	}
	plainPath = normalizeScanPath(plainPath)
	if plainPath == "" {
		return 0
	}
	var id int64
	err := db.QueryRow(`
		SELECT m.id
		FROM media m
		INNER JOIN media_encrypted_assets e ON e.media_id = m.id AND e.status = 'encrypted'
		WHERE m.library_id = ? AND lower(e.plain_path) = lower(?)
		LIMIT 1
	`, libraryID, plainPath).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// FindMediaIDByEncryptedMD5 returns media id when md5 matches an encrypted catalog row in the library.
func FindMediaIDByEncryptedMD5(db *sql.DB, libraryID int64, md5 string) int64 {
	md5 = strings.TrimSpace(md5)
	if db == nil || libraryID <= 0 || md5 == "" {
		return 0
	}
	var id int64
	err := db.QueryRow(`
		SELECT m.id
		FROM media m
		INNER JOIN media_encrypted_assets e ON e.media_id = m.id AND e.status = 'encrypted'
		WHERE m.library_id = ? AND m.md5 = ?
		LIMIT 1
	`, libraryID, md5).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// MediaFileStillPresentAfterEncrypt reports whether an encrypted media row should be kept during library scan sync.
func MediaFileStillPresentAfterEncrypt(db *sql.DB, mediaID int64, dbFilePath string, seenMedia map[string]struct{}) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM media_encrypted_assets WHERE media_id = ?`, mediaID).Scan(&status); err != nil || status != "encrypted" {
		return false
	}
	var plainPath string
	_ = db.QueryRow(`
		SELECT COALESCE(plain_path,'') FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&plainPath)
	for _, p := range []string{plainPath, dbFilePath} {
		p = normalizeScanPath(p)
		if p == "" {
			continue
		}
		if seenMedia != nil {
			if _, ok := seenMedia[p]; ok {
				return true
			}
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func normalizeScanPath(p string) string {
	cleaned := filepath.Clean(strings.TrimSpace(p))
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}
