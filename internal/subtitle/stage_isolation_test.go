package subtitle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractIgnoresRecognizeStageFailure(t *testing.T) {
	db := newSubtitleTestDB(t)
	video := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE media(id INTEGER PRIMARY KEY,file_type TEXT,file_path TEXT)`)
	_, _ = db.Exec(`CREATE TABLE subtitle_task(
		media_id INTEGER UNIQUE,status TEXT,message TEXT,
		extract_status TEXT NOT NULL DEFAULT 'pending',recognize_status TEXT NOT NULL DEFAULT 'pending',
		extract_message TEXT,recognize_message TEXT,
		created_at TEXT,started_at TEXT,finished_at TEXT,updated_at TEXT)`)
	_, _ = db.Exec(`INSERT INTO media(id,file_type,file_path) VALUES(501,'video',?)`, video)
	_, _ = db.Exec(`INSERT INTO subtitle_task(media_id,status,message,extract_status,recognize_status,recognize_message)
		VALUES(501,'failed','recognize boom','done','failed','recognize boom')`)

	s := &Service{DB: db, SubtitleDir: t.TempDir(), FFprobePath: writeProbeNoStreams(t)}
	if err := s.ExtractMedia(context.Background(), 501); err != nil {
		t.Fatalf("extract blocked by recognize failure: %v", err)
	}
	var extractStatus, recognizeStatus, aggregate string
	if err := db.QueryRow(`SELECT extract_status,recognize_status,status FROM subtitle_task WHERE media_id=501`).Scan(&extractStatus, &recognizeStatus, &aggregate); err != nil {
		t.Fatal(err)
	}
	if extractStatus != "done" {
		t.Fatalf("extract_status=%s want done", extractStatus)
	}
	if recognizeStatus != "failed" {
		t.Fatalf("recognize_status=%s want failed (sibling preserved)", recognizeStatus)
	}
}

func TestRecognizeIgnoresExtractStageFailure(t *testing.T) {
	db := newSubtitleTestDB(t)
	video := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE media(id INTEGER PRIMARY KEY,file_type TEXT,file_path TEXT)`)
	_, _ = db.Exec(`CREATE TABLE subtitle_task(
		media_id INTEGER UNIQUE,status TEXT,message TEXT,
		extract_status TEXT NOT NULL DEFAULT 'pending',recognize_status TEXT NOT NULL DEFAULT 'pending',
		extract_message TEXT,recognize_message TEXT,
		created_at TEXT,started_at TEXT,finished_at TEXT,updated_at TEXT)`)
	_, _ = db.Exec(`INSERT INTO media(id,file_type,file_path) VALUES(502,'video',?)`, video)
	_, _ = db.Exec(`INSERT INTO subtitle_task(media_id,status,message,extract_status,recognize_status,extract_message)
		VALUES(502,'failed','extract boom','failed','pending','extract boom')`)

	s := &Service{
		DB: db, SubtitleDir: t.TempDir(), FFprobePath: writeProbeNoStreams(t),
		ASR: ASRConfig{Provider: "none"},
	}
	if err := s.RecognizeMedia(context.Background(), 502); err != nil {
		t.Fatalf("recognize blocked by extract failure: %v", err)
	}
	var extractStatus, recognizeStatus string
	if err := db.QueryRow(`SELECT extract_status,recognize_status FROM subtitle_task WHERE media_id=502`).Scan(&extractStatus, &recognizeStatus); err != nil {
		t.Fatal(err)
	}
	if extractStatus != "failed" {
		t.Fatalf("extract_status=%s want failed (sibling preserved)", extractStatus)
	}
	if recognizeStatus != "done" {
		t.Fatalf("recognize_status=%s want done", recognizeStatus)
	}
}

func TestSameStageFailureStillAllowsSplitRetry(t *testing.T) {
	db := newSubtitleTestDB(t)
	video := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE media(id INTEGER PRIMARY KEY,file_type TEXT,file_path TEXT)`)
	_, _ = db.Exec(`CREATE TABLE subtitle_task(
		media_id INTEGER UNIQUE,status TEXT,message TEXT,
		extract_status TEXT NOT NULL DEFAULT 'pending',recognize_status TEXT NOT NULL DEFAULT 'pending',
		extract_message TEXT,recognize_message TEXT,
		created_at TEXT,started_at TEXT,finished_at TEXT,updated_at TEXT)`)
	_, _ = db.Exec(`INSERT INTO media(id,file_type,file_path) VALUES(503,'video',?)`, video)
	_, _ = db.Exec(`INSERT INTO subtitle_task(media_id,status,message,extract_status,extract_message,recognize_status)
		VALUES(503,'failed','extract boom','failed','extract boom','pending')`)
	s := &Service{DB: db, SubtitleDir: t.TempDir(), FFprobePath: writeProbeNoStreams(t)}
	if err := s.ExtractMedia(context.Background(), 503); err != nil {
		t.Fatalf("split extract should allow stage retry: %v", err)
	}
	// Fused ProcessMedia still treats durable failure as terminal when both stages poisoned.
	_, _ = db.Exec(`UPDATE subtitle_task SET status='failed',extract_status='failed',recognize_status='failed',message='fused boom' WHERE media_id=503`)
	err := s.ProcessMedia(context.Background(), 503)
	if err == nil || !strings.Contains(err.Error(), "fused boom") {
		t.Fatalf("err=%v want fused failure", err)
	}
}

func TestCancelKeepsStagePendingNotFailed(t *testing.T) {
	db := newSubtitleTestDB(t)
	video := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE media(id INTEGER PRIMARY KEY,file_type TEXT,file_path TEXT)`)
	_, _ = db.Exec(`CREATE TABLE subtitle_task(
		media_id INTEGER UNIQUE,status TEXT,message TEXT,
		extract_status TEXT NOT NULL DEFAULT 'pending',recognize_status TEXT NOT NULL DEFAULT 'pending',
		extract_message TEXT,recognize_message TEXT,
		created_at TEXT,started_at TEXT,finished_at TEXT,updated_at TEXT)`)
	_, _ = db.Exec(`INSERT INTO media(id,file_type,file_path) VALUES(504,'video',?)`, video)
	_, _ = db.Exec(`INSERT INTO subtitle_task(media_id,status,extract_status,recognize_status) VALUES(504,'pending','pending','pending')`)

	s := &Service{DB: db, SubtitleDir: t.TempDir(), FFprobePath: writeProbeNoStreams(t)}
	s.processMediaHook = func(ctx context.Context, mediaID int64) error {
		return context.Canceled
	}
	err := s.ExtractMedia(context.Background(), 504)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var status, extractStatus string
	if err := db.QueryRow(`SELECT status,extract_status FROM subtitle_task WHERE media_id=504`).Scan(&status, &extractStatus); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || extractStatus != "pending" {
		t.Fatalf("cancel should leave pending, got status=%s extract=%s", status, extractStatus)
	}
}
