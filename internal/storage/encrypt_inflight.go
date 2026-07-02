package storage

import "sync"

// encryptInFlight deduplicates concurrent EncryptMedia runs per media id.
var encryptInFlight sync.Map

func tryAcquireEncrypt(mediaID int64) bool {
	if mediaID <= 0 {
		return false
	}
	_, loaded := encryptInFlight.LoadOrStore(mediaID, struct{}{})
	return !loaded
}

func releaseEncrypt(mediaID int64) {
	if mediaID <= 0 {
		return
	}
	encryptInFlight.Delete(mediaID)
}
