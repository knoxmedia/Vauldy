package musiclyrics

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	SourceSidecar  = "file"
	SourceEmbedded = "embedded"
)

// Load resolves LRC lyrics for an audio file: sidecar .lrc first, then embedded tags.
func Load(absPath, metaJSON, ffprobePath string) (content string, source string, ok bool) {
	absPath = strings.TrimSpace(absPath)
	if absPath == "" {
		return "", "", false
	}
	if lrc, ok := readSidecarLRC(absPath); ok {
		return lrc, SourceSidecar, true
	}
	if lrc := embeddedFromMetaJSON(metaJSON); lrc != "" {
		return lrc, SourceEmbedded, true
	}
	if ffprobePath != "" {
		if lrc := embeddedFromFFprobe(ffprobePath, absPath); lrc != "" {
			return lrc, SourceEmbedded, true
		}
	}
	return "", "", false
}

func readSidecarLRC(absPath string) (string, bool) {
	dir := filepath.Dir(absPath)
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	if base == "" {
		return "", false
	}
	for _, name := range []string{base + ".lrc", base + ".LRC"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		text := decodeText(data)
		if strings.TrimSpace(text) != "" {
			return normalizeLRC(text), true
		}
	}
	return "", false
}

func decodeText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return string(data)
	}
	return string(data)
}

func embeddedFromMetaJSON(metaJSON string) string {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return ""
	}
	var root struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(metaJSON), &root); err != nil {
		return ""
	}
	return pickLyricsTag(root.Format.Tags)
}

func embeddedFromFFprobe(ffprobePath, absPath string) string {
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_entries", "format_tags",
		absPath,
	}
	cmd := exec.Command(ffprobePath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	var root struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out.Bytes(), &root); err != nil {
		return ""
	}
	return pickLyricsTag(root.Format.Tags)
}

func pickLyricsTag(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	lower := make(map[string]string, len(tags))
	for k, v := range tags {
		lower[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, key := range []string{"lyrics", "unsynced lyrics", "unsyncedlyrics", "uslt", "lyr", "©lyr", "syncedlyrics", "sylt"} {
		if v := lower[key]; v != "" && looksLikeLRC(v) {
			return normalizeLRC(v)
		}
	}
	for k, v := range lower {
		if v == "" {
			continue
		}
		if strings.Contains(k, "lyric") || strings.HasPrefix(k, "uslt") {
			if looksLikeLRC(v) {
				return normalizeLRC(v)
			}
		}
	}
	return ""
}

func looksLikeLRC(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.Contains(raw, "[") && strings.Contains(raw, "]") {
		return true
	}
	return len(strings.Split(raw, "\n")) > 1
}

func normalizeLRC(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	return strings.TrimSpace(raw)
}
