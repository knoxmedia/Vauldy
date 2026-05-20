package scraper

import (
	"encoding/json"
	"fmt"
	"os"
)

// ScrapeResult is a minimal stub; production would call TMDB/豆瓣/Bangumi APIs.
type ScrapeResult struct {
	Source      string         `json:"source"`
	Sources     []string       `json:"sources"`
	Title       string         `json:"title"`
	Overview    string         `json:"overview"`
	Poster      string         `json:"poster"`
	Backdrop    string         `json:"backdrop"`
	Logo        string         `json:"logo"`
	ReleaseDate string         `json:"release_date"`
	Rating      float64        `json:"rating"`
	Genres      []string       `json:"genres"`
	Extra       map[string]any `json:"extra"`
}

// AIProviderConfig is an enabled OpenAI-compatible LLM used for metadata fallback scraping.
type AIProviderConfig struct {
	ID     string
	Name   string
	APIURL string
	APIKey string
	Model  string
}

type Config struct {
	Providers    []string
	ImageSources []string
	APIKeys      map[string]string
	AIProviders  []AIProviderConfig
}

func Scrape(title, scraperName string) (*ScrapeResult, error) {
	_ = scraperName
	if title == "" {
		return nil, fmt.Errorf("empty title")
	}
	keyword, year := ExtractSearch(title)
	if keyword == "" {
		keyword = title
	}
	allSources := []string{"tmdb", "omdb", "douban", "tvdb", "bangumi", "fanart", "ai"}
	return &ScrapeResult{
		Source:      "aggregated-stub",
		Sources:     allSources,
		Title:       keyword,
		Overview:    "Metadata aggregation stub — configure provider API keys for live scraping.",
		Poster:      "",
		Backdrop:    "",
		Logo:        "",
		ReleaseDate: "",
		Rating:      0,
		Genres:      []string{},
		Extra: map[string]any{
			"note":          "stub",
			"normalized":    NormalizeTitle(title),
			"search_keyword": keyword,
			"search_year":   year,
			"providers":     allSources,
			"image_sources": []string{"tmdb", "omdb", "screen_grabber", "embedded"},
		},
	}, nil
}

func ScrapeWithConfig(title, scraperName string, cfg Config) (*ScrapeResult, error) {
	if title == "" {
		return nil, fmt.Errorf("empty title")
	}
	res, err := ScrapeOnline(title, scraperName, cfg)
	if err != nil {
		// Return partial result so the handler can still run local image capture (screen_grabber).
		if res != nil {
			return res, err
		}
		return nil, err
	}
	if !HasMeaningfulScrapeData(res) {
		if pe := providerErrorsFromResult(res); len(pe) > 0 {
			return res, fmt.Errorf("all providers failed: %s", summarizeProviderErrors(pe))
		}
		return res, fmt.Errorf("no scrape data")
	}
	return res, nil
}

func MergeMetaJSON(existing string, patch map[string]any) (string, error) {
	var base map[string]any
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &base)
	}
	if base == nil {
		base = make(map[string]any)
	}
	for k, v := range patch {
		base[k] = v
	}
	b, err := json.Marshal(base)
	if err != nil {
		return existing, err
	}
	return string(b), nil
}

func ReadNFO(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
