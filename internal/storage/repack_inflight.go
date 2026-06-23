package storage

import "sync"

var encRepackInFlight sync.Map

func tryAcquireRepack(mediaID int64) bool {
	if mediaID <= 0 {
		return false
	}
	_, loaded := encRepackInFlight.LoadOrStore(mediaID, struct{}{})
	return !loaded
}

func releaseRepack(mediaID int64) {
	if mediaID <= 0 {
		return
	}
	encRepackInFlight.Delete(mediaID)
}

func repackInFlight(mediaID int64) bool {
	if mediaID <= 0 {
		return false
	}
	_, ok := encRepackInFlight.Load(mediaID)
	return ok
}
