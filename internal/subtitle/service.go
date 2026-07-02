package subtitle

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"knox-media/internal/keystore"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
)

var sidecarExts = map[string]struct{}{
	".srt": {}, ".ass": {}, ".ssa": {}, ".vtt": {}, ".sub": {},
}

// ASRConfig selects optional speech-to-text backends when no subtitles exist.
type ASRConfig struct {
	Provider    string // none | whisper_cli | shell
	WhisperPath string
	// ExtraArgs are appended after input path (e.g. "--model small").
	ExtraArgs []string
	// Shell is invoked with /bin/sh -c; use {input} and {output_dir} placeholders.
	Shell string
}

// Service scans sidecar subtitles, probes embedded tracks, extracts WebVTT, and optionally runs ASR.
type Service struct {
	DB          *sql.DB
	Vault       *keystore.Vault
	Derived     *storage.DerivedAssetStore
	MediaRoot   string // directory containing config.yml; resolves tools/ paths in ASR shell
	FFmpegPath  string
	FFprobePath string
	SubtitleDir string
	ASR         ASRConfig
	OCR         OCRConfig
	// AIProofread enables LLM-based correction of ASR/OCR output when at least one
	// ai_provider_config is enabled. Mirrors config.SubtitleProcessingConfig.AIProofread.
	AIProofread bool

	cfgMu      sync.RWMutex
	mediaLocks sync.Map // mediaID -> *sync.Mutex
}

func (s *Service) lockMedia(mediaID int64) func() {
	v, _ := s.mediaLocks.LoadOrStore(mediaID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ApplyRecognition updates in-memory ASR/OCR settings (e.g. after config.yml save).
func (s *Service) ApplyRecognition(asr ASRConfig, ocr OCRConfig) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.ASR = asr
	s.OCR = ocr
	s.cfgMu.Unlock()
}

// ApplyAIProofread updates the in-memory AI proofread toggle (e.g. after config.yml save).
func (s *Service) ApplyAIProofread(enabled bool) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.AIProofread = enabled
	s.cfgMu.Unlock()
}

// AIProofreadEnabled reports whether LLM proofreading is enabled and at least one AI provider is configured.
func (s *Service) AIProofreadEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	enabled := s.AIProofread
	s.cfgMu.RUnlock()
	if !enabled {
		return false
	}
	return len(s.enabledAIProviders()) > 0
}

// EnabledAIProviders returns configured OpenAI-compatible LLM providers from ai_provider_config.
func (s *Service) EnabledAIProviders() []scraper.AIProviderConfig {
	return s.enabledAIProviders()
}

func (s *Service) enabledAIProviders() []scraper.AIProviderConfig {
	if s == nil || s.DB == nil {
		return nil
	}
	rows, err := s.DB.Query(
		`SELECT id, name, api_url, api_key, model FROM ai_provider_config WHERE enabled = 1 ORDER BY id`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]scraper.AIProviderConfig, 0)
	for rows.Next() {
		var p scraper.AIProviderConfig
		if rows.Scan(&p.ID, &p.Name, &p.APIURL, &p.APIKey, &p.Model) == nil {
			out = append(out, p)
		}
	}
	return out
}

func NewService(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, mediaRoot, ffmpegPath, ffprobePath, subtitleDir string, asr ASRConfig, ocr OCRConfig) *Service {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(ffprobePath) == "" {
		ffprobePath = "ffprobe"
	}
	return &Service{
		DB:           db,
		Vault:        vault,
		Derived:      derived,
		MediaRoot:    strings.TrimSpace(mediaRoot),
		FFmpegPath:   ffmpegPath,
		FFprobePath:  ffprobePath,
		SubtitleDir:  subtitleDir,
		ASR:          asr,
		OCR:          ocr,
		AIProofread:  true,
	}
}

// RunBatch processes up to limit video items that have file_path set.
func (s *Service) RunBatch(ctx context.Context, libraryID int64, limit int) (processed int, errs int) {
	if limit <= 0 {
		limit = 50
	}
	// Prefer explicit subtitle_task rows; also cover legacy media without a task row.
	q := `
SELECT t.media_id FROM subtitle_task t
JOIN media m ON m.id = t.media_id
WHERE m.file_type = 'video' AND COALESCE(m.status, 'active') = 'active'
  AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
  AND t.status = 'pending'
UNION
SELECT m.id FROM media m
LEFT JOIN subtitle_task t ON t.media_id = m.id
WHERE m.file_type = 'video' AND COALESCE(m.status, 'active') = 'active'
  AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
  AND t.media_id IS NULL`
	args := []any{}
	if libraryID > 0 {
		q = `
SELECT t.media_id FROM subtitle_task t
JOIN media m ON m.id = t.media_id
WHERE m.library_id = ? AND m.file_type = 'video' AND COALESCE(m.status, 'active') = 'active'
  AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
  AND t.status = 'pending'
UNION
SELECT m.id FROM media m
LEFT JOIN subtitle_task t ON t.media_id = m.id
WHERE m.library_id = ? AND m.file_type = 'video' AND COALESCE(m.status, 'active') = 'active'
  AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
  AND t.media_id IS NULL`
		args = append(args, libraryID, libraryID)
	}
	q += ` ORDER BY media_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return 0, 1
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return processed, errs
		default:
		}
		if err := s.ProcessMedia(ctx, id); err != nil {
			log.Printf("subtitle process media=%d err=%v", id, err)
			errs++
		} else {
			processed++
		}
	}
	return processed, errs
}

// ProcessMedia scans sidecars, embedded tracks, and optional ASR for one media row.
func (s *Service) ProcessMedia(ctx context.Context, mediaID int64) (err error) {
	var status string
	if qerr := s.DB.QueryRow(`SELECT status FROM subtitle_task WHERE media_id = ?`, mediaID).Scan(&status); qerr == nil && status == "failed" {
		return nil
	}

	unlock := s.lockMedia(mediaID)
	defer unlock()

	s.upsertTaskRunning(mediaID)
	defer func() {
		if err != nil {
			s.upsertTaskFailed(mediaID, err.Error())
			return
		}
		s.upsertTaskDone(mediaID)
	}()

	var videoPath string
	if err = s.DB.QueryRow(`SELECT file_path FROM media WHERE id = ? AND file_type = 'video'`, mediaID).Scan(&videoPath); err != nil {
		return err
	}
	videoPath = strings.TrimSpace(videoPath)
	if videoPath == "" {
		return fmt.Errorf("empty path")
	}
	if fi, statErr := os.Stat(videoPath); statErr != nil || fi.IsDir() {
		return fmt.Errorf("video missing")
	}

	outDir := filepath.Join(s.SubtitleDir, strconv.FormatInt(mediaID, 10))
	if root := s.toolWorkDir(); root != "" && !filepath.IsAbs(outDir) {
		outDir = filepath.Clean(filepath.Join(root, outDir))
	}
	if err = os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	if err = s.syncEmbedded(ctx, mediaID, videoPath, outDir); err != nil {
		return err
	}
	if err = s.syncSidecars(ctx, mediaID, videoPath, outDir); err != nil {
		return err
	}

	hasAny, errSub := s.hasAnySubtitle(mediaID)
	if errSub != nil {
		return errSub
	}
	if !hasAny && s.shouldRunASR() {
		errASR := s.runASR(ctx, mediaID, videoPath, outDir)
		if errASR != nil {
			log.Printf("subtitle asr media=%d err=%v", mediaID, errASR)
		}
		hasAny, errSub = s.hasAnySubtitle(mediaID)
		if errSub != nil {
			return errSub
		}
		if !hasAny && errASR != nil {
			return fmt.Errorf("asr failed: %w", errASR)
		}
	}
	return nil
}

func (s *Service) hasAnySubtitle(mediaID int64) (bool, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM media_subtitle WHERE media_id = ? AND status = 'ready'`, mediaID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Service) shouldRunASR() bool {
	switch strings.ToLower(strings.TrimSpace(s.ASR.Provider)) {
	case "whisper_cli", "shell":
		return true
	default:
		return false
	}
}

func (s *Service) syncEmbedded(ctx context.Context, mediaID int64, videoPath, outDir string) error {
	streams, err := s.subtitleStreams(ctx, mediaID, videoPath)
	if err != nil {
		return err
	}
	for _, st := range streams {
		codec := strings.TrimSpace(st.CodecName)
		if IsBitmapSubtitleCodec(codec) {
			dedupe := fmt.Sprintf("embedded-ocr:%d", st.Index)
			lang := NormalizeFFprobeLang(st.Language)
			langSrc := "ffprobe"
			if lang == "" {
				lang = "und"
				langSrc = "default"
			}
			label := strings.TrimSpace(st.Title)
			outPath := filepath.Join(outDir, fmt.Sprintf("embedded-ocr-%d.vtt", st.Index))
			if err := s.upsertPlaceholder(mediaID, dedupe, "embedded_ocr", st.Index, codec, lang, langSrc, label, "", outPath); err != nil {
				return err
			}
			if !s.OCR.Enabled || strings.TrimSpace(s.OCR.ScriptPath) == "" {
				msg := "graphic/bitmap subtitle (PGS/VobSub); enable subtitle.graphical_ocr and set script_path"
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`,
					msg, mediaID, dedupe)
				continue
			}
			if err := s.RunBitmapSubtitleOCR(ctx, mediaID, videoPath, st.Index, outPath); err != nil {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`,
					trimErr(err), mediaID, dedupe)
				continue
			}
			if perr := s.ProofreadFileInPlace(ctx, outPath, lang); perr != nil {
				log.Printf("subtitle ai-proofread media=%d dedupe=%s err=%v", mediaID, dedupe, perr)
			}
			_ = s.markSubtitleReady(ctx, mediaID, dedupe, filepath.Base(outPath), outPath)
			continue
		}

		dedupe := fmt.Sprintf("embedded:%d", st.Index)
		lang := NormalizeFFprobeLang(st.Language)
		langSrc := "ffprobe"
		if lang == "" {
			lang = "und"
			langSrc = "default"
		}
		label := strings.TrimSpace(st.Title)
		outPath := filepath.Join(outDir, fmt.Sprintf("embedded-%d.vtt", st.Index))
		if err := s.upsertPlaceholder(mediaID, dedupe, "embedded", st.Index, codec, lang, langSrc, label, "", outPath); err != nil {
			return err
		}
		if err := s.extractEmbedded(ctx, mediaID, videoPath, st.Index, outPath); err != nil {
			_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`,
				trimErr(err), mediaID, dedupe)
			continue
		}
		_ = s.markSubtitleReady(ctx, mediaID, dedupe, filepath.Base(outPath), outPath)
	}
	return nil
}

func (s *Service) upsertPlaceholder(mediaID int64, dedupe, kind string, streamIdx int, codec, lang, langSrc, label, srcPath, vttPath string) error {
	var si any
	if streamIdx >= 0 {
		si = streamIdx
	}
	_, err := s.DB.Exec(`
		INSERT INTO media_subtitle (media_id, dedupe_key, source_kind, stream_index, codec_name, lang, lang_source, label, source_path, vtt_path, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', CURRENT_TIMESTAMP)
		ON CONFLICT(media_id, dedupe_key) DO UPDATE SET
			stream_index=excluded.stream_index,
			codec_name=excluded.codec_name,
			lang=excluded.lang,
			lang_source=excluded.lang_source,
			label=excluded.label,
			source_path=excluded.source_path,
			vtt_path=excluded.vtt_path,
			status='running',
			error_message=NULL,
			updated_at=CURRENT_TIMESTAMP
	`, mediaID, dedupe, kind, si, nullStr(codec), nullStr(lang), nullStr(langSrc), nullStr(label), nullStr(srcPath), vttPath)
	return err
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (s *Service) extractEmbedded(ctx context.Context, mediaID int64, videoPath string, streamIndex int, outPath string) error {
	mapArg := fmt.Sprintf("0:%d", streamIndex)
	post := []string{"-map", mapArg, "-c:s", "webvtt", outPath}
	if _, err := storage.RunFFmpeg(ctx, s.DB, s.Vault, s.FFmpegPath, mediaID, videoPath, 0, 0, nil, post, ""); err != nil {
		return err
	}
	return nil
}

func (s *Service) syncSidecars(ctx context.Context, mediaID int64, videoPath, outDir string) error {
	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		isVobsubIdx := ext == ".idx"
		if _, ok := sidecarExts[ext]; !ok && !isVobsubIdx {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(stem)) {
			continue
		}
		full := filepath.Join(dir, name)
		if isVobsubIdx {
			subCompanion := filepath.Join(dir, strings.TrimSuffix(name, ext)+".sub")
			if fi, err := os.Stat(subCompanion); err != nil || fi.IsDir() {
				continue
			}
			if !s.OCR.Enabled || strings.TrimSpace(s.OCR.ScriptPath) == "" {
				continue
			}
			dedupe := "ext-ocr:" + strings.ToLower(full)
			lang, langSrc := "und", "default"
			if c, ok := DetectLanguageFromFilename(name); ok {
				lang = c
				langSrc = "filename"
			}
			outName := fmt.Sprintf("sidecar-ocr-%s.vtt", safeFileToken(strings.TrimSuffix(name, ext)))
			outPath := filepath.Join(outDir, outName)
			if err := s.upsertPlaceholder(mediaID, dedupe, "external_ocr", -1, "vobsub", lang, langSrc, "", full, outPath); err != nil {
				return err
			}
			if err := s.RunVobSubIdxOCR(ctx, full, outPath); err != nil {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimErr(err), mediaID, dedupe)
				continue
			}
			if perr := s.ProofreadFileInPlace(ctx, outPath, lang); perr != nil {
				log.Printf("subtitle ai-proofread media=%d dedupe=%s err=%v", mediaID, dedupe, perr)
			}
			_ = s.markSubtitleReady(ctx, mediaID, dedupe, filepath.Base(outPath), outPath)
			continue
		}
		if ext == ".sub" {
			idxCompanion := filepath.Join(dir, strings.TrimSuffix(name, ext)+".idx")
			if _, err := os.Stat(idxCompanion); err == nil && s.OCR.Enabled && strings.TrimSpace(s.OCR.ScriptPath) != "" {
				continue
			}
		}
		dedupe := "ext:" + strings.ToLower(full)
		lang, langSrc := "und", "default"
		if c, ok := DetectLanguageFromFilename(name); ok {
			lang = c
			langSrc = "filename"
		}
		outName := fmt.Sprintf("sidecar-%s.vtt", safeFileToken(strings.TrimSuffix(name, ext)))
		outPath := filepath.Join(outDir, outName)
		if err := s.upsertPlaceholder(mediaID, dedupe, "external", -1, strings.TrimPrefix(ext, "."), lang, langSrc, "", full, outPath); err != nil {
			return err
		}
		if strings.EqualFold(ext, ".vtt") {
			if err := copyOrWriteVTT(ctx, s.FFmpegPath, full, outPath); err != nil {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, err.Error(), mediaID, dedupe)
				continue
			}
		} else {
			cmd := exec.CommandContext(ctx, s.FFmpegPath, "-y", "-i", full, "-c:s", "webvtt", outPath)
			out, err := cmd.CombinedOutput()
			if err != nil {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimBytes(out), mediaID, dedupe)
				continue
			}
		}
		_ = s.markSubtitleReady(ctx, mediaID, dedupe, filepath.Base(outPath), outPath)
	}
	return nil
}

func (s *Service) markSubtitleReady(ctx context.Context, mediaID int64, dedupe, logicalName, plainPath string) error {
	stored := plainPath
	if s.Derived != nil {
		var err error
		stored, err = s.Derived.FinalizePath(ctx, mediaID, "subtitle", logicalName, plainPath)
		if err != nil {
			_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimErr(err), mediaID, dedupe)
			return err
		}
	}
	_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='ready', vtt_path=?, error_message=NULL, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, stored, mediaID, dedupe)
	return nil
}

func safeFileToken(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, s)
	if s == "" {
		return "sub"
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func copyOrWriteVTT(ctx context.Context, ffmpegPath, src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "WEBVTT") {
		return os.WriteFile(dst, b, 0o644)
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-i", src, "-c:s", "webvtt", dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimBytes(out))
	}
	return nil
}

func (s *Service) runASR(ctx context.Context, mediaID int64, videoPath, outDir string) error {
	dedupe := "asr:auto"
	outPath := filepath.Join(outDir, "asr.vtt")
	_ = s.upsertPlaceholder(mediaID, dedupe, "asr", -1, "", "und", "asr", "", "", outPath)

	asrInput, asrCleanup, err := s.asrInputPath(ctx, mediaID, videoPath, outDir)
	if err != nil {
		_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimErr(err), mediaID, dedupe)
		return err
	}
	defer asrCleanup()

	switch strings.ToLower(strings.TrimSpace(s.ASR.Provider)) {
	case "whisper_cli":
		wp := s.resolveMediaPath(strings.TrimSpace(s.ASR.WhisperPath))
		if wp == "" {
			wp = "whisper"
		}
		args := []string{asrInput, "--output_format", "vtt", "--output_dir", outDir}
		args = append(args, s.ASR.ExtraArgs...)
		cmd := exec.CommandContext(ctx, wp, args...)
		s.applyToolEnv(cmd)
		if root := s.toolWorkDir(); root != "" {
			cmd.Dir = root
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimBytes(out), mediaID, dedupe)
			return err
		}
		// whisper writes <basename>.vtt next to input by default in output_dir
		base := strings.TrimSuffix(filepath.Base(asrInput), filepath.Ext(asrInput))
		gen := filepath.Join(outDir, base+".vtt")
		if err := os.Rename(gen, outPath); err != nil {
			// if rename fails, try copying from cwd
			if b, e := os.ReadFile(gen); e == nil {
				_ = os.WriteFile(outPath, b, 0o644)
			} else {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, err.Error(), mediaID, dedupe)
				return err
			}
		}
	case "shell":
		sh := strings.TrimSpace(s.ASR.Shell)
		if sh == "" {
			return fmt.Errorf("asr.shell empty")
		}
		shellInput := asrInput
		shellCleanup := func() {}
		if storage.InputNeedsPipe(s.DB, mediaID, videoPath) {
			// Custom shell may expect a video path; provide decrypted temp when input was pipe-derived WAV only.
			var matErr error
			shellInput, shellCleanup, matErr = storage.MaterializePlaintextTemp(s.DB, s.Vault, mediaID, videoPath)
			if matErr != nil {
				_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimErr(matErr), mediaID, dedupe)
				return matErr
			}
		}
		defer shellCleanup()
		sh = strings.ReplaceAll(sh, "{input}", shellInput)
		sh = strings.ReplaceAll(sh, "{output_dir}", outDir)
		sh = strings.ReplaceAll(sh, "{output_vtt}", outPath)
		sh = resolveShellMediaPaths(sh, s.MediaRoot)
		out, err := s.runShellCommand(ctx, sh)
		if err != nil {
			_, _ = s.DB.Exec(`UPDATE media_subtitle SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND dedupe_key=?`, trimErr(err), mediaID, dedupe)
			return err
		}
		_ = out
	default:
		return nil
	}
	if perr := s.ProofreadFileInPlace(ctx, outPath, "und"); perr != nil {
		log.Printf("subtitle ai-proofread media=%d dedupe=%s err=%v", mediaID, dedupe, perr)
	}
	return s.markSubtitleReady(ctx, mediaID, dedupe, filepath.Base(outPath), outPath)
}

func trimBytes(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 1500 {
		return s[:1500]
	}
	return s
}

func trimErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 1500 {
		return s[:1500]
	}
	return s
}

// List returns subtitle rows for API responses.
func (s *Service) List(mediaID int64) ([]map[string]any, error) {
	rows, err := s.DB.Query(`
		SELECT id, source_kind, stream_index, codec_name, lang, lang_source, label, source_path, vtt_path, status, error_message, updated_at
		FROM media_subtitle WHERE media_id = ? ORDER BY id`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var si sql.NullInt64
		var kind, codec, lang, lsrc, label, src, vtt, status, errMsg, updated sql.NullString
		if err := rows.Scan(&id, &kind, &si, &codec, &lang, &lsrc, &label, &src, &vtt, &status, &errMsg, &updated); err != nil {
			continue
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
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) VTTPath(mediaID, subtitleID int64) (string, error) {
	var p sql.NullString
	err := s.DB.QueryRow(`SELECT vtt_path FROM media_subtitle WHERE id = ? AND media_id = ? AND status = 'ready'`, subtitleID, mediaID).Scan(&p)
	if err != nil {
		return "", err
	}
	if !p.Valid || strings.TrimSpace(p.String) == "" {
		return "", sql.ErrNoRows
	}
	return p.String, nil
}
