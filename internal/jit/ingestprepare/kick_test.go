package ingestprepare

import (
	"path/filepath"
	"testing"

	models "knox-media/internal/model"
	"knox-media/internal/store"
)

type schedulerSpy struct {
	prepareCalls int
	triggerCalls int

	fileID     string
	filePath   string
	format     string
	videoCodec string
	audioCodec string
	sessionID  string
}

func (s *schedulerSpy) PrepareVideoMeta(fileID, filePath, format, videoCodec, audioCodec string) error {
	s.prepareCalls++
	s.fileID = fileID
	s.filePath = filePath
	s.format = format
	s.videoCodec = videoCodec
	s.audioCodec = audioCodec
	return nil
}

func (s *schedulerSpy) TriggerSlicing(fileID, sessionID string) error {
	s.triggerCalls++
	s.fileID = fileID
	s.sessionID = sessionID
	return nil
}

func (s *schedulerSpy) SetAudioPlaylists(fileID string, playlists []models.AudioPlaylistInfo) {
	// no-op for test
}

func TestKick_IngestPrepareAnalyzesCodecAndTriggersSlicing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kick.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, drm_enabled, jit_prepare_on_ingest) VALUES (1, 'lib', 'movie', '/data', 0, 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}

	metaJSON := `{
		"format": {"format_name":"matroska,webm"},
		"streams": [
			{"codec_type":"video","codec_name":"hevc"},
			{"codec_type":"audio","codec_name":"eac3"}
		]
	}`
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, format, meta_json) VALUES (101, 1, 'f-101', '/mnt/media/a.mkv', '', ?)`, metaJSON); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	spy := &schedulerSpy{}
	Kick(db, spy, 101)

	if spy.prepareCalls != 1 {
		t.Fatalf("prepare calls=%d, want 1", spy.prepareCalls)
	}
	if spy.triggerCalls != 1 {
		t.Fatalf("trigger calls=%d, want 1", spy.triggerCalls)
	}
	if spy.fileID != "f-101" {
		t.Fatalf("file_id=%q, want f-101", spy.fileID)
	}
	if spy.filePath != filepath.Clean("/mnt/media/a.mkv") && spy.filePath != "/mnt/media/a.mkv" {
		t.Fatalf("file_path=%q, want /mnt/media/a.mkv", spy.filePath)
	}
	if spy.format != "matroska,webm" {
		t.Fatalf("format=%q, want matroska,webm", spy.format)
	}
	if spy.videoCodec != "hevc" || spy.audioCodec != "eac3" {
		t.Fatalf("codecs video=%q audio=%q, want hevc/eac3", spy.videoCodec, spy.audioCodec)
	}
	if spy.sessionID != "ingest:101" {
		t.Fatalf("session_id=%q, want ingest:101", spy.sessionID)
	}
}

func TestKick_SkipsWhenOnlyDRMEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kick-drm.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, drm_enabled, jit_prepare_on_ingest) VALUES (1, 'lib', 'movie', '/data', 1, 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, format) VALUES (103, 1, 'f-103', '/mnt/media/c.mkv', 'matroska')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	spy := &schedulerSpy{}
	Kick(db, spy, 103)

	if spy.prepareCalls != 0 || spy.triggerCalls != 0 {
		t.Fatalf("expected no ingest prepare when only drm_enabled, got prepare=%d trigger=%d", spy.prepareCalls, spy.triggerCalls)
	}
}

func TestKick_SkipsWhenIngestPrepareDisabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kick-disabled.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, drm_enabled, jit_prepare_on_ingest) VALUES (1, 'lib', 'movie', '/data', 0, 0)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, format) VALUES (102, 1, 'f-102', '/mnt/media/b.mkv', 'matroska')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	spy := &schedulerSpy{}
	Kick(db, spy, 102)

	if spy.prepareCalls != 0 || spy.triggerCalls != 0 {
		t.Fatalf("expected no ingest prepare calls, got prepare=%d trigger=%d", spy.prepareCalls, spy.triggerCalls)
	}
}
