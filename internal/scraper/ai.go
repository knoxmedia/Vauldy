package scraper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var aiScrapeHTTP = &http.Client{Timeout: 90 * time.Second}

const aiMetadataSystemPrompt = `你是影视元数据专家。根据用户给出的搜索词（可能来自文件名），推断最匹配的电影/剧集/动漫作品。
只输出一个 JSON 对象，不要 Markdown，不要解释。字段：
title（string，中文作品用中文片名，否则用常见译名或原名）、
overview（string，2-4 句中文简介）、
release_date（string，YYYY-MM-DD，未知则空字符串）、
rating（number，0-10，未知可 0）、
genres（string 数组，中文类型标签）、
media_type（movie|tv|anime|other）、
year（integer，上映年份，未知为 0）。`

type aiChatRequest struct {
	Model          string        `json:"model"`
	Messages       []aiChatMsg   `json:"messages"`
	Temperature    float64       `json:"temperature,omitempty"`
	ResponseFormat *aiRespFormat `json:"response_format,omitempty"`
}

type aiChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiRespFormat struct {
	Type string `json:"type"`
}

type aiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type aiMetadataPayload struct {
	Title       string   `json:"title"`
	Overview    string   `json:"overview"`
	ReleaseDate string   `json:"release_date"`
	Rating      float64  `json:"rating"`
	Genres      []string `json:"genres"`
	MediaType   string   `json:"media_type"`
	Year        int      `json:"year"`
}

func scrapeAI(keyword, altKeyword string, year int, providers []AIProviderConfig, rawTitle string) (*ScrapeResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("ai: empty search keyword")
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("ai: no enabled AI provider")
	}
	user := buildAIMetadataUserPrompt(keyword, altKeyword, year, rawTitle)
	var lastErr error
	for _, p := range providers {
		if strings.TrimSpace(p.APIURL) == "" {
			lastErr = fmt.Errorf("ai: %s api url missing", p.ID)
			continue
		}
		if strings.TrimSpace(p.Model) == "" && !isLocalAIEndpoint(p.APIURL) {
			lastErr = fmt.Errorf("ai: %s model missing", p.ID)
			continue
		}
		if strings.TrimSpace(p.APIKey) == "" && !isLocalAIEndpoint(p.APIURL) {
			lastErr = fmt.Errorf("ai: %s api key missing", p.ID)
			continue
		}
		res, err := scrapeAIWithProvider(p, user)
		if err == nil && res != nil {
			return res, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ai: all providers failed")
	}
	return nil, lastErr
}

func buildAIMetadataUserPrompt(keyword, altKeyword string, year int, rawTitle string) string {
	var b strings.Builder
	b.WriteString("搜索关键词: ")
	b.WriteString(keyword)
	if alt := strings.TrimSpace(altKeyword); alt != "" && !strings.EqualFold(alt, keyword) {
		b.WriteString("\n备用关键词: ")
		b.WriteString(alt)
	}
	if year > 0 {
		b.WriteString(fmt.Sprintf("\n年份提示: %d", year))
	}
	if raw := strings.TrimSpace(rawTitle); raw != "" && raw != keyword {
		b.WriteString("\n原始文件名/标题: ")
		b.WriteString(raw)
	}
	return b.String()
}

func scrapeAIWithProvider(p AIProviderConfig, userPrompt string) (*ScrapeResult, error) {
	content, err := aiChatCompletion(p, aiMetadataSystemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}
	payload, err := parseAIMetadataJSON(content)
	if err != nil {
		return nil, fmt.Errorf("ai: %w", err)
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return nil, fmt.Errorf("ai: empty title in response")
	}
	overview := strings.TrimSpace(payload.Overview)
	if overview == "" && payload.Rating == 0 && len(payload.Genres) == 0 && strings.TrimSpace(payload.ReleaseDate) == "" {
		return nil, fmt.Errorf("ai: no useful metadata")
	}
	releaseDate := strings.TrimSpace(payload.ReleaseDate)
	if releaseDate == "" && payload.Year > 0 {
		releaseDate = fmt.Sprintf("%d-01-01", payload.Year)
	}
	return &ScrapeResult{
		Source:      "ai",
		Title:       title,
		Overview:    overview,
		ReleaseDate: releaseDate,
		Rating:      payload.Rating,
		Genres:      payload.Genres,
		Extra: map[string]any{
			"ai_provider":   p.ID,
			"ai_provider_name": p.Name,
			"media_type":    strings.TrimSpace(payload.MediaType),
			"ai_generated":  true,
		},
	}, nil
}

func aiChatCompletion(p AIProviderConfig, system, user string) (string, error) {
	return chatCompletion(p, system, user, true)
}

// ChatCompletion calls an OpenAI-compatible chat/completions endpoint.
// When jsonMode is false, response_format json_object is not requested (for plain-text tasks like translation).
func ChatCompletion(p AIProviderConfig, system, user string, jsonMode bool) (string, error) {
	return chatCompletion(p, system, user, jsonMode)
}

func chatCompletion(p AIProviderConfig, system, user string, jsonMode bool) (string, error) {
	model := strings.TrimSpace(p.Model)
	if model == "" && isLocalAIEndpoint(p.APIURL) {
		model = "llama3"
	}
	reqBody := aiChatRequest{
		Model: model,
		Messages: []aiChatMsg{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
	}
	if jsonMode {
		reqBody.ResponseFormat = &aiRespFormat{Type: "json_object"}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	endpoint := aiChatCompletionsURL(p.APIURL)
	resp, err := postAIJSON(endpoint, p.APIKey, body)
	if err != nil && jsonMode && strings.Contains(strings.ToLower(err.Error()), "response_format") {
		reqBody.ResponseFormat = nil
		body, _ = json.Marshal(reqBody)
		resp, err = postAIJSON(endpoint, p.APIKey, body)
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, msg)
	}
	var parsed aiChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return "", fmt.Errorf("%s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty model response")
	}
	return parsed.Choices[0].Message.Content, nil
}

func postAIJSON(endpoint, apiKey string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := strings.TrimSpace(apiKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return aiScrapeHTTP.Do(req)
}

func aiChatCompletionsURL(apiURL string) string {
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/v1") {
		return base + "/chat/completions"
	}
	if strings.Contains(lower, "11434") {
		return base + "/v1/chat/completions"
	}
	if strings.Contains(lower, "/v1/") || strings.HasSuffix(lower, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func isLocalAIEndpoint(apiURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(apiURL))
	return strings.Contains(lower, "localhost") || strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "0.0.0.0")
}

func parseAIMetadataJSON(content string) (*aiMetadataPayload, error) {
	content = extractJSONObject(content)
	var payload aiMetadataPayload
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	return &payload, nil
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
	return s
}
