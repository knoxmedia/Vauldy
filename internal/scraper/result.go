package scraper

import (
	"fmt"
	"strings"
)

// IsStubResult reports whether the result is the offline placeholder, not live provider data.
func IsStubResult(res *ScrapeResult) bool {
	if res == nil {
		return true
	}
	if res.Source == "aggregated-stub" {
		return true
	}
	if res.Extra != nil {
		if note, _ := res.Extra["note"].(string); strings.EqualFold(strings.TrimSpace(note), "stub") {
			return true
		}
	}
	return false
}

// HasScrapePoster reports whether metadata/image providers already supplied a poster URL.
func HasScrapePoster(res *ScrapeResult) bool {
	if res == nil {
		return false
	}
	if strings.TrimSpace(res.Poster) != "" {
		return true
	}
	if res.Extra != nil {
		if p, ok := res.Extra["poster"].(string); ok && strings.TrimSpace(p) != "" {
			return true
		}
	}
	return false
}

// HasMeaningfulScrapeData returns true when at least one useful metadata or image field was scraped.
func HasMeaningfulScrapeData(res *ScrapeResult) bool {
	if res == nil || IsStubResult(res) {
		return false
	}
	if res.Poster != "" || res.Backdrop != "" || res.Logo != "" {
		return true
	}
	if strings.TrimSpace(res.Overview) != "" {
		return true
	}
	if res.Rating > 0 {
		return true
	}
	if len(res.Genres) > 0 {
		return true
	}
	if strings.TrimSpace(res.ReleaseDate) != "" {
		return true
	}
	if strings.TrimSpace(res.Title) != "" && res.Source == "ai" {
		return true
	}
	if res.Extra != nil {
		for _, k := range []string{"tmdb_id", "imdb_id"} {
			v := strings.TrimSpace(fmt.Sprint(res.Extra[k]))
			if v != "" && v != "0" && v != "<nil>" {
				return true
			}
		}
	}
	return false
}

func providerErrorsFromResult(res *ScrapeResult) map[string]map[string]string {
	if res == nil || res.Extra == nil {
		return nil
	}
	if pe, ok := res.Extra["provider_errors"].(map[string]map[string]string); ok && len(pe) > 0 {
		return pe
	}
	return nil
}

// FormatScrapeErrorMessage turns scrape errors into a user-facing reason (Chinese).
func FormatScrapeErrorMessage(err error) string {
	if err == nil {
		return "刮削失败：未知原因"
	}
	msg := strings.TrimSpace(err.Error())
	switch {
	case msg == "":
		return "刮削失败：未知原因"
	case msg == "empty title":
		return "刮削失败：标题为空，无法搜索"
	case msg == "no scrape data":
		return "刮削失败：所有站点均未返回有效元数据"
	case strings.HasPrefix(msg, "all providers failed:"):
		summary := strings.TrimSpace(strings.TrimPrefix(msg, "all providers failed:"))
		if summary == "" {
			return "刮削失败：所有站点均未返回有效数据"
		}
		return "刮削失败：所有站点均未返回有效数据（" + formatProviderErrorSummary(summary) + "）"
	default:
		return "刮削失败：" + humanizeRawError(msg)
	}
}

func formatProviderErrorSummary(summary string) string {
	parts := strings.Split(summary, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		provider, cat, ok := strings.Cut(p, ":")
		if !ok {
			out = append(out, p)
			continue
		}
		out = append(out, strings.TrimSpace(provider)+":"+categoryLabel(strings.TrimSpace(cat)))
	}
	if len(out) == 0 {
		return summary
	}
	return strings.Join(out, "；")
}

func categoryLabel(cat string) string {
	switch strings.TrimSpace(cat) {
	case "key_missing":
		return "API 密钥未配置"
	case "auth_error":
		return "认证失败"
	case "quota_limited":
		return "请求频率受限"
	case "network_error":
		return "网络连接失败"
	case "no_result":
		return "未找到匹配结果"
	case "remote_error":
		return "远端服务错误"
	default:
		if cat == "" {
			return "远端服务错误"
		}
		return cat
	}
}

func humanizeRawError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "dial tcp") ||
		strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset"):
		return "网络连接失败（" + msg + "）"
	case strings.Contains(lower, "api key missing") || strings.Contains(lower, "key missing"):
		return "API 密钥未配置"
	case strings.Contains(lower, "no enabled ai provider"):
		return "未启用 AI 提供商，请在「AI 提供商」页面配置并启用"
	case strings.Contains(lower, "empty"):
		return "未找到匹配结果"
	default:
		return msg
	}
}

// NoDataFailureMessage builds a failure reason when providers ran but returned nothing useful.
func NoDataFailureMessage(res *ScrapeResult) string {
	if pe := providerErrorsFromResult(res); len(pe) > 0 {
		return FormatScrapeErrorMessage(fmt.Errorf("all providers failed: %s", summarizeProviderErrors(pe)))
	}
	return "刮削失败：未刮削到有效元数据或配图"
}
