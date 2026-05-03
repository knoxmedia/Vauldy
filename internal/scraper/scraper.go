package scraper

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
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

type Config struct {
	Providers    []string
	ImageSources []string
	APIKeys      map[string]string
}

var noisyTags = regexp.MustCompile(`(?i)\b(bluray|bdrip|webrip|web-dl|x264|x265|h264|h265|hevc|aac|dts|hdr|dv|remux|1080p|2160p|720p|10bit)\b`)
var yearPattern = regexp.MustCompile(`\b(19|20)\d{2}\b`)
var splitNoise = regexp.MustCompile(`[._-]+`)

func NormalizeTitle(raw string) string {
	v := strings.TrimSpace(raw)
	v = splitNoise.ReplaceAllString(v, " ")
	v = noisyTags.ReplaceAllString(v, " ")
	v = strings.Join(strings.Fields(v), " ")
	return strings.TrimSpace(v)
}

func ExtractSearch(raw string) (keyword string, year int) {
	clean := NormalizeTitle(raw)
	m := yearPattern.FindString(clean)
	if m != "" {
		if y, err := strconv.Atoi(m); err == nil {
			year = y
		}
		clean = strings.Replace(clean, m, "", 1)
	}
	clean = strings.Join(strings.Fields(clean), " ")
	return strings.TrimSpace(clean), year
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
	if err == nil && res != nil {
		return res, nil
	}
	return Scrape(title, scraperName)
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
