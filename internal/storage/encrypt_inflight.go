package storage

import (
	"context"
	"sync"
)

type encryptFlight struct {
	done chan struct{}
	err  error
}

// encryptInFlight deduplicates concurrent EncryptMedia runs per media id.
var encryptInFlight sync.Map

func acquireEncryptFlight(mediaID int64) (bool, *encryptFlight) {
	if mediaID <= 0 {
		return false, nil
	}
	flight := &encryptFlight{done: make(chan struct{})}
	actual, loaded := encryptInFlight.LoadOrStore(mediaID, flight)
	if loaded {
		return false, actual.(*encryptFlight)
	}
	return true, flight
}

func waitEncryptFlight(ctx context.Context, flight *encryptFlight) error {
	if flight == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-flight.done:
		return flight.err
	}
}

func finishEncryptFlight(mediaID int64, flight *encryptFlight, err error) {
	if flight == nil {
		return
	}
	flight.err = err
	close(flight.done)
	encryptInFlight.CompareAndDelete(mediaID, flight)
}
