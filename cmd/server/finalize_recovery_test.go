package main

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
	"testing"
	"time"
)

func TestStartFinalizeRecoveryLoopStopsWithContext(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := startFinalizeRecoveryLoop(ctx, db, time.Millisecond, func(error) {})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop")
	}
}

var _ *sql.DB
