package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	kcrypto "knox-media/internal/crypto"
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

// ShouldLinkEncryptedPlainPathScan reports whether a scanned file at diskPath should relink to
// an existing encrypted catalog row found via plain_path. After plaintext cleanup, a new file may
// reuse the same path; only identical content (MD5) should relink.
func ShouldLinkEncryptedPlainPathScan(db *sql.DB, mediaID int64, diskPath, diskMD5 string) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	diskPath = normalizeScanPath(diskPath)
	if diskPath == "" {
		return false
	}
	var catalogPath, storedMD5 sql.NullString
	var encStatus sql.NullString
	var plainPath sql.NullString
	err := db.QueryRow(`
		SELECT COALESCE(m.file_path,''), COALESCE(m.md5,''), COALESCE(e.status,''), COALESCE(e.plain_path,'')
		FROM media m
		LEFT JOIN media_encrypted_assets e ON e.media_id = m.id
		WHERE m.id = ?
	`, mediaID).Scan(&catalogPath, &storedMD5, &encStatus, &plainPath)
	if err != nil {
		return false
	}
	if normalizeScanPath(plainPath.String) != diskPath {
		return false
	}
	if encStatus.String != "encrypted" {
		return true
	}
	stored := strings.TrimSpace(storedMD5.String)
	diskMD5 = strings.TrimSpace(diskMD5)
	catalog := strings.TrimSpace(catalogPath.String)
	isEncCatalog := kcrypto.IsEncFile(catalog) || strings.EqualFold(filepath.Ext(catalog), ".enc")
	// After encrypt the catalog path is .enc while plaintext may still exist on disk (especially with SkipHash scans).
	if isEncCatalog {
		if stored != "" && diskMD5 != "" {
			return strings.EqualFold(stored, diskMD5)
		}
		return true
	}
	if stored == "" || diskMD5 == "" {
		return false
	}
	return strings.EqualFold(stored, diskMD5)
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
