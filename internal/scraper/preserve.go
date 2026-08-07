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

// MergeSeriesFieldsPreservingEpisode merges a series-level patch into an
// episode's existing scrape metadata without dropping the episode's own artwork
// or episode-specific fields. MergeMetaJSON replaces the whole "scrape" object,
// which would discard a committed poster pointer on sibling episodes every time
// another episode in the same show is scraped, forcing repair runs to regenerate
// posters in a loop. This merge only adds/updates the supplied keys and merges
// the nested "extra" map.
func MergeSeriesFieldsPreservingEpisode(existing string, patch map[string]any) (string, error) {
	var base map[string]any
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &base)
	}
	if base == nil {
		base = make(map[string]any)
	}
	scrape, _ := base["scrape"].(map[string]any)
	if scrape == nil {
		scrape = make(map[string]any)
	}
	patchScrape, _ := patch["scrape"].(map[string]any)
	for k, v := range patchScrape {
		if k == "extra" {
			continue
		}
		scrape[k] = v
	}
	if patchExtra, ok := patchScrape["extra"].(map[string]any); ok {
		extra, _ := scrape["extra"].(map[string]any)
		if extra == nil {
			extra = make(map[string]any)
		}
		for k, v := range patchExtra {
			extra[k] = v
		}
		scrape["extra"] = extra
	}
	base["scrape"] = scrape
	raw, err := json.Marshal(base)
	if err != nil {
		return existing, err
	}
	return string(raw), nil
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
