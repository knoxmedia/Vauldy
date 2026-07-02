package subtitle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

// SubtitleCues loads a ready subtitle, parses it, and returns the format name plus
// the cue list (start/end timing + text) for inline editing in the UI.
func (s *Service) SubtitleCues(mediaID, subtitleID int64, vault *keystore.Vault) (string, []Cue, error) {
	if s == nil {
		return "", nil, os.ErrInvalid
	}
	content, err := s.ReadVTTContent(mediaID, subtitleID, vault)
	if err != nil {
		return "", nil, err
	}
	format := DetectFormat(content, "")
	cues, _, err := ParseCues(content, format)
	if err != nil {
		return "", nil, err
	}
	return formatName(format), cues, nil
}

// SaveSubtitleCues re-renders the edited cues as WebVTT and writes them back to the
// subtitle artifact (encrypted Knox derived asset or plain file), preserving the path.
func (s *Service) SaveSubtitleCues(ctx context.Context, mediaID, subtitleID int64, cues []Cue) error {
	if s == nil {
		return os.ErrInvalid
	}
	if len(cues) == 0 {
		return fmt.Errorf("no cues to save")
	}
	path, err := s.VTTPath(mediaID, subtitleID)
	if err != nil {
		return err
	}
	content := RenderCues(cues, FormatVTT)
	return s.writeSubtitleArtifact(ctx, mediaID, path, content)
}

// writeSubtitleArtifact overwrites a subtitle file in place. Encrypted Knox .enc
// artifacts are rewritten through the DerivedAssetStore (re-keying the asset);
// plain files are overwritten directly.
func (s *Service) writeSubtitleArtifact(ctx context.Context, mediaID int64, path, content string) error {
	path = strings.TrimSpace(filepath.Clean(path))
	if path == "" {
		return fmt.Errorf("empty subtitle path")
	}
	if kcrypto.IsEncFile(path) {
		logicalName := strings.TrimSuffix(filepath.Base(path), ".enc")
		if s.Derived == nil {
			return fmt.Errorf("derived asset store unavailable for %s", path)
		}
		_, err := s.Derived.Write(ctx, mediaID, "subtitle", logicalName, strings.NewReader(content))
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ImportSubtitleFile imports an uploaded subtitle file (VTT/SRT/ASS) as a new
// media_subtitle entry with source_kind="imported", converts it to WebVTT for
// playback, stores it, and returns the new row.
func (s *Service) ImportSubtitleFile(ctx context.Context, mediaID int64, filename string, data []byte) (map[string]any, error) {
	if s == nil || s.DB == nil {
		return nil, os.ErrInvalid
	}
	if mediaID <= 0 || len(data) == 0 {
		return nil, fmt.Errorf("invalid import args")
	}
	content := string(data)
	format := DetectFormat(content, filename)
	if format == FormatUnknown || format == FormatLRC {
		return nil, fmt.Errorf("unsupported subtitle format (use .vtt/.srt/.ass)")
	}
	cues, _, err := ParseCues(content, format)
	if err != nil || len(cues) == 0 {
		return nil, fmt.Errorf("could not parse subtitle file: %w", err)
	}
	vtt := RenderCues(cues, FormatVTT)

	lang, langSrc := "und", "default"
	if c, ok := DetectLanguageFromFilename(filename); ok {
		lang = c
		langSrc = "filename"
	}
	token := safeFileToken(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	if token == "" {
		token = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	dedupe := "import:" + strings.ToLower(filepath.Base(filename))
	dedupe = uniqueDedupeKey(s.DB, mediaID, dedupe)

	outDir := filepath.Join(s.SubtitleDir, strconv.FormatInt(mediaID, 10))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	outName := fmt.Sprintf("imported-%s.vtt", token)
	outPath := filepath.Join(outDir, outName)
	if err := os.WriteFile(outPath, []byte(vtt), 0o644); err != nil {
		return nil, err
	}
	if err := s.upsertPlaceholder(mediaID, dedupe, "imported", -1, "vtt", lang, langSrc, "", filename, outPath); err != nil {
		return nil, err
	}
	if err := s.markSubtitleReady(ctx, mediaID, dedupe, outName, outPath); err != nil {
		return nil, err
	}
	return s.importedRow(mediaID, dedupe)
}

func (s *Service) importedRow(mediaID int64, dedupe string) (map[string]any, error) {
	row, err := s.DB.Query(`
		SELECT id, source_kind, stream_index, codec_name, lang, lang_source, label, source_path, vtt_path, status, error_message, updated_at
		FROM media_subtitle WHERE media_id = ? AND dedupe_key = ?`, mediaID, dedupe)
	if err != nil {
		return nil, err
	}
	defer row.Close()
	if !row.Next() {
		return nil, sql.ErrNoRows
	}
	var id int64
	var si sql.NullInt64
	var kind, codec, lang, lsrc, label, src, vtt, status, errMsg, updated sql.NullString
	if err := row.Scan(&id, &kind, &si, &codec, &lang, &lsrc, &label, &src, &vtt, &status, &errMsg, &updated); err != nil {
		return nil, err
	}
	m := map[string]any{
		"id":            id,
		"source_kind":   kind.String,
		"codec_name":    codec.String,
		"lang":          lang.String,
		"lang_source":   lsrc.String,
		"label":         label.String,
		"source_path":   src.String,
		"vtt_path":      vtt.String,
		"status":        status.String,
		"error_message": errMsg.String,
		"updated_at":    updated.String,
	}
	if si.Valid {
		m["stream_index"] = si.Int64
	}
	return m, nil
}

func uniqueDedupeKey(db *sql.DB, mediaID int64, base string) string {
	key := base
	for i := 1; ; i++ {
		var exists int
		err := db.QueryRow(`SELECT 1 FROM media_subtitle WHERE media_id = ? AND dedupe_key = ? LIMIT 1`, mediaID, key).Scan(&exists)
		if err != nil {
			return key
		}
		key = fmt.Sprintf("%s-%d", base, i)
		if i > 999 {
			return key + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	}
}

func formatName(f Format) string {
	switch f {
	case FormatVTT:
		return "vtt"
	case FormatSRT:
		return "srt"
	case FormatASS:
		return "ass"
	case FormatLRC:
		return "lrc"
	default:
		return "vtt"
	}
}
