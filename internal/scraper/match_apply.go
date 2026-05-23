package scraper

import "strings"

// ApplyMatchCandidateFields fills missing poster/overview on a scrape result using
// values from the manual-match search list (e.g. when detail fetch omits artwork).
func ApplyMatchCandidateFields(res *ScrapeResult, poster, overview string) {
	if res == nil {
		return
	}
	poster = strings.TrimSpace(poster)
	overview = strings.TrimSpace(overview)
	if !HasScrapePoster(res) && poster != "" {
		res.Poster = poster
		if res.Extra == nil {
			res.Extra = map[string]any{}
		}
		if strings.TrimSpace(fmtString(res.Extra["poster"])) == "" {
			res.Extra["poster"] = poster
		}
	}
	if strings.TrimSpace(res.Overview) == "" && overview != "" {
		res.Overview = overview
	}
}

// SanitizeScrapeResult normalizes values that break JSON encoding or UI display.
func SanitizeScrapeResult(res *ScrapeResult) {
	if res == nil {
		return
	}
	if res.Rating < 0 {
		res.Rating = 0
	}
}
