package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"knox-media/internal/scraper"
)

const translateBatchSize = 25

const subtitleTranslateSystemPrompt = `你是专业字幕翻译。用户给出 JSON：{"count":N,"source_lang":"...","target_lang":"...","lines":["..."]}。
将每条 lines[i] 从 source_lang 译为 target_lang，输出 JSON：{"count":N,"lines":["..."]}。
硬性要求：
1. count 与输入相同，lines 长度必须等于 count
2. 不得合并、拆分或省略任一行；空字符串行原样保留
3. 只输出 JSON，不要 Markdown`

type translateLinesPayload struct {
	Count      int      `json:"count"`
	SourceLang string   `json:"source_lang"`
	TargetLang string   `json:"target_lang"`
	Lines      []string `json:"lines"`
}

type translateLinesResponse struct {
	Count int      `json:"count"`
	Lines []string `json:"lines"`
}

// TranslateContent translates subtitle file content using enabled OpenAI-compatible providers.
func TranslateContent(ctx context.Context, content, srcLang, targetLang string, providers []scraper.AIProviderConfig) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("empty subtitle content")
	}
	if len(providers) == 0 {
		return "", fmt.Errorf("no enabled AI provider")
	}
	format := DetectFormat(content, "")
	cues, format, err := ParseCues(content, format)
	if err != nil {
		return "", err
	}
	srcLabel := languageLabel(srcLang)
	targetLabel := languageLabel(targetLang)
	if targetLabel == "" {
		return "", fmt.Errorf("unsupported target language")
	}

	for start := 0; start < len(cues); start += translateBatchSize {
		end := start + translateBatchSize
		if end > len(cues) {
			end = len(cues)
		}
		batch := cues[start:end]
		lines := make([]string, len(batch))
		for i, c := range batch {
			lines[i] = c.Text
		}
		translated, err := translateLinesRobust(ctx, providers, srcLabel, targetLabel, lines)
		if err != nil {
			return "", err
		}
		for i := range batch {
			cues[start+i].Text = translated[i]
		}
	}
	_ = ctx
	return RenderCues(cues, format), nil
}

func translateLinesRobust(ctx context.Context, providers []scraper.AIProviderConfig, srcLang, targetLang string, lines []string) ([]string, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty lines")
	}
	out, err := translateLinesWithProviders(ctx, providers, srcLang, targetLang, lines)
	if err == nil && len(out) == len(lines) {
		return out, nil
	}
	// Batch mismatch or parse failure: translate cue-by-cue (reliable for short/single-track subs).
	if len(lines) <= 80 {
		return translateLinesSequential(ctx, providers, srcLang, targetLang, lines)
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("translation line count mismatch (got %d, want %d)", len(out), len(lines))
}

func translateLinesSequential(ctx context.Context, providers []scraper.AIProviderConfig, srcLang, targetLang string, lines []string) ([]string, error) {
	out := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		batch := []string{line}
		translated, err := translateLinesWithProviders(ctx, providers, srcLang, targetLang, batch)
		if err != nil || len(translated) != 1 {
			// Last resort: keep original line so playback is not blocked.
			out[i] = line
			continue
		}
		out[i] = translated[0]
	}
	return out, nil
}

func translateLinesWithProviders(ctx context.Context, providers []scraper.AIProviderConfig, srcLang, targetLang string, lines []string) ([]string, error) {
	payload := translateLinesPayload{
		Count:      len(lines),
		SourceLang: srcLang,
		TargetLang: targetLang,
		Lines:      lines,
	}
	userBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, p := range providers {
		out, err := scraper.ChatCompletion(p, subtitleTranslateSystemPrompt, string(userBytes), true)
		if err != nil {
			lastErr = err
			continue
		}
		parsed, err := parseTranslateLinesResponse(out, len(lines))
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

func parseTranslateLinesResponse(content string, expected int) ([]string, error) {
	content = extractJSONObject(content)
	var resp translateLinesResponse
	if err := json.Unmarshal([]byte(content), &resp); err == nil && len(resp.Lines) > 0 {
		return reconcileTranslatedLines(expected, resp.Lines), nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(content), &arr); err == nil && len(arr) > 0 {
		return reconcileTranslatedLines(expected, arr), nil
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &generic); err != nil {
		return nil, fmt.Errorf("invalid translation json: %w", err)
	}
	for _, key := range []string{"lines", "translated", "translated_lines", "result", "data"} {
		if raw, ok := generic[key]; ok {
			var lines []string
			if err := json.Unmarshal(raw, &lines); err == nil && len(lines) > 0 {
				return reconcileTranslatedLines(expected, lines), nil
			}
		}
	}
	// Indexed map {"0":"...", "1":"..."}
	if expected > 0 {
		indexed := make([]string, expected)
		found := 0
		for k, raw := range generic {
			idx, err := strconv.Atoi(k)
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
	return nil, fmt.Errorf("empty translation lines")
}

func reconcileTranslatedLines(expected int, lines []string) []string {
	if expected <= 0 {
		return lines
	}
	out := make([]string, expected)
	for i := 0; i < expected; i++ {
		if i < len(lines) {
			out[i] = lines[i]
		}
	}
	return out
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			return s[i : j+1]
		}
	}
	return s
}

func languageLabel(code string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	code = strings.ReplaceAll(code, "-", "_")
	switch code {
	case "zh_cn", "zh", "chi", "cmn":
		return "简体中文"
	case "zh_tw", "zh_hk", "zh_hant":
		return "繁体中文"
	case "en_us", "en", "eng":
		return "English"
	case "ja_jp", "ja", "jpn":
		return "日本語"
	case "ko_kr", "ko", "kor":
		return "한국어"
	case "fr", "fra":
		return "Français"
	case "de", "deu":
		return "Deutsch"
	case "es", "spa":
		return "Español"
	case "ru", "rus":
		return "Русский"
	case "pt", "por":
		return "Português"
	case "it", "ita":
		return "Italiano"
	case "und", "":
		return "auto"
	default:
		if len(code) >= 2 {
			return code
		}
		return ""
	}
}
