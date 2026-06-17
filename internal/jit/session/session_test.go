package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTimeSinceLastRequestUsesCreatedAtWhenNeverRequested(t *testing.T) {
	s := &Session{CreatedAt: time.Now().Add(-10 * time.Second)}
	idle := s.TimeSinceLastRequest()
	if idle < 9*time.Second || idle > 15*time.Second {
		t.Fatalf("expected idle ~10s from CreatedAt, got %v", idle)
	}
	if idle >= time.Hour {
		t.Fatalf("must not return time.Hour sentinel, got %v", idle)
	}
}

func TestCleanupEncoderOutputFromSegRemovesPlaylistAndSegmentsFromStart(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"0.ts", "10.ts", "11.ts", "master.m3u8", "enc.key", "enc.keyinfo"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := cleanupEncoderOutputFromSeg(dir, 10); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, name := range []string{"0.ts", "enc.key", "enc.keyinfo"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
	for _, name := range []string{"10.ts", "11.ts", "master.m3u8"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat err=%v", name, err)
		}
	}
}

func TestResetForSeekCleansEncoderOutputFromTargetSeg(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"5.ts", "6.ts", "master.m3u8"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	s := &Session{
		TempDir:   dir,
		CreatedAt: time.Now(),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	close(s.done)

	s.ResetForSeek(6)
	if s.StartSeg != 6 {
		t.Fatalf("StartSeg=%d want 6", s.StartSeg)
	}
	for _, name := range []string{"6.ts", "master.m3u8"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed after seek reset", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "5.ts")); err != nil {
		t.Fatalf("expected 5.ts kept before seek point: %v", err)
	}
}
