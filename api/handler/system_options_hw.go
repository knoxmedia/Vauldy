package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"

	"knox-media/internal/jit/hwenc"
)

// transcoderHardwareAccelUserConfigured reports whether hardware acceleration
// fields were persisted (user saved system options, or a prior bootstrap wrote them).
func transcoderHardwareAccelUserConfigured(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return false
	}
	var doc struct {
		Transcoder json.RawMessage `json:"transcoder"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || len(doc.Transcoder) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc.Transcoder, &fields); err != nil {
		return false
	}
	_, hasAccel := fields["hardware_acceleration"]
	_, hasEnable := fields["enable_hardware_encoding"]
	return hasAccel || hasEnable
}

// EnsureHardwareAccelDefaults applies detected hardware acceleration on first init.
// When the user has already saved transcoder hardware settings, stored values are kept.
func EnsureHardwareAccelDefaults(db *sql.DB, ffmpegPath string, available []string) error {
	if db == nil {
		return nil
	}
	var raw sql.NullString
	if err := db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		return err
	}
	rawStr := strings.TrimSpace(raw.String)
	if transcoderHardwareAccelUserConfigured(rawStr) {
		return nil
	}

	best := hwenc.DetectHWAccel(ffmpegPath)
	if best == string(hwenc.HWAccelNone) || len(available) == 0 {
		return nil
	}

	opts := decodeSystemOptions(rawStr)
	opts.Transcoder.HardwareAcceleration = best
	opts.Transcoder.EnableHardwareEncoding = true
	clampTranscoderHardware(&opts.Transcoder, available)
	if opts.Transcoder.HardwareAcceleration == "none" || !opts.Transcoder.EnableHardwareEncoding {
		return nil
	}

	out, err := json.Marshal(normalizeSystemOptions(opts))
	if err != nil {
		return err
	}
	if _, err := db.Exec(
		`UPDATE system_options SET options_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
		string(out),
	); err != nil {
		return err
	}
	log.Printf("system options: initialized hardware acceleration=%s enable_hardware_encoding=true",
		opts.Transcoder.HardwareAcceleration)
	return nil
}
