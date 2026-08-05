package metadatalib

import (
	"encoding/json"
	"strings"

	"knox-media/internal/scraper"
)

// ScrapeResultFromMetaJSON extracts scrape result from media meta_json.
func ScrapeResultFromMetaJSON(metaJSON string) (*scraper.ScrapeResult, bool) {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return nil, false
	}
	var raw map[string]any
	if json.Unmarshal([]byte(metaJSON), &raw) != nil {
		return nil, false
	}
	sv, ok := raw["scrape"].(map[string]any)
	if !ok || len(sv) == 0 {
		return nil, false
	}
	b, err := json.Marshal(sv)
	if err != nil {
		return nil, false
	}
	var res scraper.ScrapeResult
	if json.Unmarshal(b, &res) != nil {
		return nil, false
	}
	return &res, true
}

// MetaHasRemoteScrapeImages reports whether meta_json contains remote scrape image URLs.
func MetaHasRemoteScrapeImages(metaJSON string) bool {
	res, ok := ScrapeResultFromMetaJSON(metaJSON)
	if !ok || res == nil {
		return false
	}
	for _, u := range collectRemoteImages(res) {
		if u != "" {
			return true
		}
	}
	return false
}

// ResultHasRemoteScrapeImages reports whether a scrape result still references
// artwork that must be copied into the metadata library.
func ResultHasRemoteScrapeImages(res *scraper.ScrapeResult) bool {
	return len(collectRemoteImages(res)) > 0
}

// ResultHasRemoteScrapePoster reports whether the selected poster is volatile.
func ResultHasRemoteScrapePoster(res *scraper.ScrapeResult) bool {
	if res == nil {
		return false
	}
	for _, raw := range []string{res.Poster, scrapeExtraImage(res, "poster")} {
		raw = normalizeImageURL(strings.TrimSpace(raw))
		if isRemoteHTTPURL(raw) || isLocalUploadsURL(raw) || strings.HasPrefix(raw, "/static/") {
			return true
		}
	}
	return false
}

// ResultHasLocalScrapePoster reports whether both selected poster pointers were
// normalized to the durable metadata-library URL when present.
func ResultHasLocalScrapePoster(res *scraper.ScrapeResult) bool {
	if res == nil {
		return false
	}
	poster := strings.TrimSpace(res.Poster)
	extra := strings.TrimSpace(scrapeExtraImage(res, "poster"))
	if poster == "" && extra == "" {
		return false
	}
	return (poster == "" || IsLocalMetadataURL(poster)) && (extra == "" || IsLocalMetadataURL(extra))
}

func scrapeExtraImage(res *scraper.ScrapeResult, kind string) string {
	if res == nil || res.Extra == nil {
		return ""
	}
	value, _ := res.Extra[kind].(string)
	return value
}
