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
