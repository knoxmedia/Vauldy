package handler

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTranscoderHardwareAccelUserConfigured(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "", want: false},
		{raw: "{}", want: false},
		{raw: `{"general":{"display_language":"zh-CN"}}`, want: false},
		{raw: `{"transcoder":{"quality":"auto"}}`, want: false},
		{raw: `{"transcoder":{"hardware_acceleration":"none"}}`, want: true},
		{raw: `{"transcoder":{"enable_hardware_encoding":false}}`, want: true},
	}
	for _, tc := range cases {
		if got := transcoderHardwareAccelUserConfigured(tc.raw); got != tc.want {
			t.Fatalf("raw=%q got %v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestEnsureHardwareAccelDefaults(t *testing.T) {
	dbPath := t.TempDir() + "/opts.sqlite"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_options (id INTEGER PRIMARY KEY, options_json TEXT NOT NULL, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO system_options (id, options_json) VALUES (1, '{}')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := EnsureHardwareAccelDefaults(db, "ffmpeg", []string{"nvenc", "amf"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("scan: %v", err)
	}
	opts := decodeSystemOptions(raw)
	if opts.Transcoder.HardwareAcceleration != "nvenc" {
		t.Fatalf("hardware_acceleration=%q want nvenc", opts.Transcoder.HardwareAcceleration)
	}
	if !opts.Transcoder.EnableHardwareEncoding {
		t.Fatal("expected enable_hardware_encoding=true")
	}

	if err := EnsureHardwareAccelDefaults(db, "ffmpeg", []string{"nvenc", "amf"}); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	var raw2 string
	_ = db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw2)
	if raw2 != raw {
		t.Fatal("expected second ensure to leave user/bootstrap config unchanged")
	}
}

func TestEnsureHardwareAccelDefaultsRespectsUserNone(t *testing.T) {
	dbPath := t.TempDir() + "/opts-user.sqlite"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_options (id INTEGER PRIMARY KEY, options_json TEXT NOT NULL, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	userJSON := `{"transcoder":{"hardware_acceleration":"none","enable_hardware_encoding":false}}`
	if _, err := db.Exec(`INSERT INTO system_options (id, options_json) VALUES (1, ?)`, userJSON); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := EnsureHardwareAccelDefaults(db, "ffmpeg", []string{"nvenc"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var raw string
	_ = db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw)
	opts := decodeSystemOptions(raw)
	if opts.Transcoder.HardwareAcceleration != "none" || opts.Transcoder.EnableHardwareEncoding {
		t.Fatalf("user config overwritten: %#v", opts.Transcoder)
	}
}
