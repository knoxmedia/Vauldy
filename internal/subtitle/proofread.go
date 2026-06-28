package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"knox-media/internal/scraper"
)

const proofreadBatchSize = 25

const subtitleProofreadSystemPrompt = `你是专业字幕/歌词校对编辑。用户给出 JSON：{"count":N,"lang":"...","lines":["..."]}。
这些字幕/歌词由语音识别(ASR)或图像识别(OCR)自动生成，可能存在错别字、同音字错误、漏字、多余语气词、标点缺失或 OCR 噪点。
请逐行校对并返回修正后的文本，输出 JSON：{"count":N,"lines":["..."]}。
硬性要求：
1. count 与输入相同，lines 长度必须等于 count，顺序一一对应
2. 不得合并、拆分或省略任一行；空字符串行原样保留
3. 保持原文语言不变（中文仍是中文，英文仍是英文），不得翻译
4. 只修正明显错误，不得改写语义、不得新增原文没有的内容、不得删除原文有效内容
5. 保留原文的换行（如有多行文本，对应位置仍用 \n 表示）
6. 不要给行加序号、引号或 Markdown；只输出 JSON`

type proofreadLinesPayload struct {
	Count int      `json:"count"`
	Lang  string   `json:"lang"`
	Lines []string `json:"lines"`
}

type proofreadLinesResponse struct {
	Count int      `json:"count"`
	Lines []string `json:"lines"`
}

// ProofreadContent corrects ASR/OCR errors in subtitle/lyric content using enabled OpenAI-compatible providers.
// Supported formats: WebVTT, SRT, ASS (subtitle cues) and LRC (lyrics). The output keeps the same
// format and timing as the input; only cue/lyric text is rewritten. Lang is a best-effort hint
// (e.g. "zh", "en", "und"); pass "und" or "" when unknown.
func ProofreadContent(ctx context.Context, content, lang string, providers []scraper.AIProviderConfig) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("empty subtitle content")
	}
	if len(providers) == 0 {
		return "", fmt.Errorf("no enabled AI provider")
	}
	format := DetectFormat(content, "")
	if format == FormatLRC {
		return proofreadLRCContent(ctx, content, lang, providers)
	}
	cues, format, err := ParseCues(content, format)
	if err != nil {
		return "", err
	}
	langLabel := normalizeProofreadLang(lang)

	for start := 0; start < len(cues); start += proofreadBatchSize {
		end := start + proofreadBatchSize
		if end > len(cues) {
			end = len(cues)
		}
		batch := cues[start:end]
		lines := make([]string, len(batch))
		for i, c := range batch {
			lines[i] = c.Text
		}
		fixed, err := proofreadLinesRobust(ctx, providers, langLabel, lines)
		if err != nil {
			return "", err
		}
		for i := range batch {
			cues[start+i].Text = fixed[i]
		}
	}
	_ = ctx
	return RenderCues(cues, format), nil
}

// proofreadLRCContent corrects LRC lyric text while preserving every [mm:ss.xx] timestamp
// tag and every non-lyric line (metadata tags like [ti:..], blank lines, plain text) verbatim.
// Only the lyric text following a timestamp tag is sent to the LLM.
func proofreadLRCContent(ctx context.Context, content, lang string, providers []scraper.AIProviderConfig) (string, error) {
	lines := splitLines(content)
	type lrcLine struct {
		prefix string
		text   string
		idx    int
	}
	var lyrics []lrcLine
	out := make([]string, len(lines))
	for i, raw := range lines {
		out[i] = raw
		prefix, text, ok := splitLRCLyricLine(raw)
		if !ok {
			continue
		}
		lyrics = append(lyrics, lrcLine{prefix, text, i})
	}
	if len(lyrics) == 0 {
		return content, nil
	}
	langLabel := normalizeProofreadLang(lang)
	texts := make([]string, len(lyrics))
	for j, l := range lyrics {
		texts[j] = l.text
	}
	fixed, err := proofreadLinesRobust(ctx, providers, langLabel, texts)
	if err != nil {
		return "", err
	}
	for j, l := range lyrics {
		out[l.idx] = l.prefix + fixed[j]
	}
	return strings.Join(out, "\n"), nil
}

// splitLRCLyricLine separates leading [mm:ss.xx] timestamp tags from lyric text.
// Returns the concatenated prefix, the remaining text, and ok=true when the line
// is a lyric line (at least one leading timestamp tag). Metadata tags ([ti:..],
// [ar:..]) and plain text return ok=false so callers preserve them verbatim.
func splitLRCLyricLine(line string) (prefix, text string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	rest := line
	var b strings.Builder
	for strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			break
		}
		tag := rest[:end+1]
		if !lrcTimestamp.MatchString(tag) {
			break
		}
		b.WriteString(tag)
		rest = rest[end+1:]
	}
	if b.Len() == 0 {
		return "", "", false
	}
	return b.String(), strings.TrimSpace(rest), true
}

func proofreadLinesRobust(ctx context.Context, providers []scraper.AIProviderConfig, lang string, lines []string) ([]string, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty lines")
	}
	out, err := proofreadLinesWithProviders(ctx, providers, lang, lines)
	if err == nil && len(out) == len(lines) {
		return out, nil
	}
	// Batch mismatch or parse failure: fall back to cue-by-cue (reliable for short inputs).
	if len(lines) <= 80 {
		return proofreadLinesSequential(ctx, providers, lang, lines)
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("proofread line count mismatch (got %d, want %d)", len(out), len(lines))
}

func proofreadLinesSequential(ctx context.Context, providers []scraper.AIProviderConfig, lang string, lines []string) ([]string, error) {
	out := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		batch := []string{line}
		fixed, err := proofreadLinesWithProviders(ctx, providers, lang, batch)
		if err != nil || len(fixed) != 1 {
			// Keep original line so playback/lyrics are not blocked by a single bad call.
			out[i] = line
			continue
		}
		out[i] = fixed[0]
	}
	return out, nil
}

func proofreadLinesWithProviders(ctx context.Context, providers []scraper.AIProviderConfig, lang string, lines []string) ([]string, error) {
	_ = ctx
	payload := proofreadLinesPayload{
		Count: len(lines),
		Lang:  lang,
		Lines: lines,
	}
	userBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, p := range providers {
		out, err := scraper.ChatCompletion(p, subtitleProofreadSystemPrompt, string(userBytes), true)
		if err != nil {
			lastErr = err
			continue
		}
		parsed, err := parseProofreadLinesResponse(out, len(lines))
		if err != nil {
			lastErr = err
			continue
		}
		return parsed, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all AI providers failed")
	}
	return nil, lastErr
}

func parseProofreadLinesResponse(content string, expected int) ([]string, error) {
	content = extractJSONObject(content)
	var resp proofreadLinesResponse
	if err := json.Unmarshal([]byte(content), &resp); err == nil && len(resp.Lines) > 0 {
		return reconcileTranslatedLines(expected, resp.Lines), nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(content), &arr); err == nil && len(arr) > 0 {
		return reconcileTranslatedLines(expected, arr), nil
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &generic); err != nil {
		return nil, fmt.Errorf("invalid proofread json: %w", err)
	}
	for _, key := range []string{"lines", "corrected", "proofread", "result", "data"} {
		if raw, ok := generic[key]; ok {
			var lines []string
			if err := json.Unmarshal(raw, &lines); err == nil && len(lines) > 0 {
				return reconcileTranslatedLines(expected, lines), nil
			}
		}
	}
	if expected > 0 {
		indexed := make([]string, expected)
		found := 0
		for k, raw := range generic {
			idx, err := jsonInt(k)
			if err != nil || idx < 0 || idx >= expected {
				continue
			}
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				indexed[idx] = s
				found++
			}
		}
		if found > 0 {
			return reconcileTranslatedLines(expected, indexed), nil
		}
	}
	return nil, fmt.Errorf("empty proofread lines")
}

func jsonInt(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not int")
		}
		n = n*10 + int(r-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

func normalizeProofreadLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	lang = strings.ReplaceAll(lang, "-", "_")
	switch lang {
	case "", "und", "auto":
		return "auto"
	}
	return lang
}

// ProofreadFileInPlace reads a subtitle/lyric file (WebVTT, SRT, ASS, or LRC),
// runs LLM correction, and overwrites it in place. Best-effort: returns error so
// callers can log; the original file is preserved on failure. Source-kind-aware:
// only call this for ASR/OCR outputs (not for already-accurate text extraction).
func (s *Service) ProofreadFileInPlace(ctx context.Context, path, lang string) error {
	if s == nil || !s.AIProofreadEnabled() {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil
	}
	providers := s.enabledAIProviders()
	if len(providers) == 0 {
		return nil
	}
	corrected, err := ProofreadContent(ctx, string(b), lang, providers)
	if err != nil {
		return err
	}
	if strings.TrimSpace(corrected) == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(corrected), 0o644); err != nil {
		return err
	}
	return nil
}
