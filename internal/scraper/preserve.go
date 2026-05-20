package scraper

import (
	"encoding/json"
	"strings"
)

// HasScrapePosterFromMeta reports whether meta_json already contains a scrape poster URL.
func HasScrapePosterFromMeta(metaJSON string) bool {
	poster, _, _ := scrapeImagesFromMetaJSON(metaJSON)
	return strings.TrimSpace(poster) != ""
}

// PreserveScrapeImagesFromExisting copies poster/backdrop/logo from existing meta_json when the
// new scrape result did not obtain replacements (avoids wiping a good local poster on partial failure).
func PreserveScrapeImagesFromExisting(res *ScrapeResult, existingMeta string) {
	if res == nil {
		return
	}
	poster, backdrop, logo := scrapeImagesFromMetaJSON(existingMeta)
	if strings.TrimSpace(res.Poster) == "" && poster != "" {
		res.Poster = poster
	}
	if strings.TrimSpace(res.Backdrop) == "" && backdrop != "" {
		res.Backdrop = backdrop
	}
	if strings.TrimSpace(res.Logo) == "" && logo != "" {
		res.Logo = logo
	}
	if res.Extra == nil {
		res.Extra = map[string]any{}
	}
	if strings.TrimSpace(fmtString(res.Extra["poster"])) == "" && poster != "" {
		res.Extra["poster"] = poster
	}
}

func scrapeImagesFromMetaJSON(metaJSON string) (poster, backdrop, logo string) {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return "", "", ""
	}
	var root map[string]any
	if json.Unmarshal([]byte(metaJSON), &root) != nil {
		return "", "", ""
	}
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil {
		return "", "", ""
	}
	poster = strings.TrimSpace(fmtString(scrape["poster"]))
	backdrop = strings.TrimSpace(fmtString(scrape["backdrop"]))
	logo = strings.TrimSpace(fmtString(scrape["logo"]))
	if extra, ok := scrape["extra"].(map[string]any); ok {
		if poster == "" {
			poster = strings.TrimSpace(fmtString(extra["poster"]))
		}
		if backdrop == "" {
			backdrop = strings.TrimSpace(fmtString(extra["backdrop"]))
		}
		if logo == "" {
			logo = strings.TrimSpace(fmtString(extra["logo"]))
		}
	}
	return poster, backdrop, logo
}

func fmtString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
