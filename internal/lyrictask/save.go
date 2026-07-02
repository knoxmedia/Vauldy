package lyrictask

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SaveLyrics persists edited/imported LRC content for an audio track. It prefers
// writing a sidecar .lrc next to the audio (portable, takes priority on load);
// when the source directory is not writable it falls back to the encrypted
// derived lyric_lrc artifact and records the path on lyric_task.
func (w *Worker) SaveLyrics(ctx context.Context, mediaID int64, lrcContent string) error {
	if w == nil || w.DB == nil {
		return os.ErrInvalid
	}
	if mediaID <= 0 {
		return fmt.Errorf("invalid media id")
	}
	content := strings.TrimSpace(lrcContent)
	if content == "" {
		return fmt.Errorf("empty lyrics")
	}
	var filePath sql.NullString
	if err := w.DB.QueryRow(`SELECT file_path FROM media WHERE id = ? LIMIT 1`, mediaID).Scan(&filePath); err != nil {
		return fmt.Errorf("media not found: %w", err)
	}
	audioPath := strings.TrimSpace(filePath.String)
	if audioPath == "" {
		return fmt.Errorf("empty media path")
	}

	if sidecar := sidecarLRCPath(audioPath); writeSidecar(sidecar, content) {
		w.ensureLyricTaskRow(mediaID)
		w.markDone(mediaID, "", sidecar, "校对/导入保存")
		return nil
	}

	// Fallback: encrypted/plaintext derived asset.
	if w.Derived != nil {
		finalLRC, err := w.Derived.FinalizeBytes(ctx, mediaID, "lyric_lrc", "asr.lrc", []byte(content+"\n"))
		if err != nil {
			return err
		}
		w.ensureLyricTaskRow(mediaID)
		w.markDone(mediaID, "", finalLRC, "校对/导入保存")
		return nil
	}
	return fmt.Errorf("could not write lyrics (source dir not writable and no derived store)")
}

// ensureLyricTaskRow inserts a placeholder lyric_task row if none exists so that
// markDone has a row to update after an edit/import.
func (w *Worker) ensureLyricTaskRow(mediaID int64) {
	if w == nil || w.DB == nil {
		return
	}
	_, _ = w.DB.Exec(`
		INSERT OR IGNORE INTO lyric_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, mediaID)
}

func writeSidecar(path, content string) bool {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		return false
	}
	return true
}
