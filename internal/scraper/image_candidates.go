package scraper

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ImageCandidate is a single selectable image URL returned by a configured image source.
type ImageCandidate struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// FetchImageCandidates queries the library-configured image sources (cfg.ImageSources)
// for the requested kind and returns a deduplicated, order-preserving candidate list.
// Only configured online image providers are contacted — unconfigured sources are
// never queried, so unreachable providers can simply be omitted from the library config.
// kind is "poster" | "backdrop" | "logo".
//
// tmdbID (optional, e.g. from existing scrape metadata) lets fanart resolve artwork
// without re-running a TMDb search; when TMDb itself is configured it is queried first
// and the resolved id is reused for fanart.
func FetchImageCandidates(cfg Config, keyword string, year int, kind string, tmdbID string) ([]ImageCandidate, map[string]string, bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "poster"
	}
	sources := cfg.ImageSources
	// No fallback here: the caller (readLibraryScrapeConfig) already supplies global
	// defaults when the library column is empty. An explicitly empty list means
	// "no online image sources for this library" — contact nothing.

	candidates := make([]ImageCandidate, 0, 16)
	seen := map[string]struct{}{}
	errors := map[string]string{}
	scraped := false
	add := func(u, source string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		candidates = append(candidates, ImageCandidate{URL: u, Source: source})
	}

	currentTmdbID := strings.TrimSpace(tmdbID)
	keys := cfg.APIKeys

	for _, raw := range sources {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !isOnlineImageProvider(name) {
			continue
		}
		scraped = true
		switch name {
		case "tmdb":
			urls, id, err := fetchTmdbImageList(keys["tmdb"], keyword, year, kind)
			if err != nil {
				errors["tmdb"] = err.Error()
				continue
			}
			if id != 0 {
				currentTmdbID = strconv.FormatInt(id, 10)
			}
			for _, u := range urls {
				add(u, "tmdb")
			}
		case "fanart":
			if currentTmdbID == "" {
				errors["fanart"] = "fanart missing tmdb_id"
				continue
			}
			urls, err := fetchFanartImageList(keys["fanart"], currentTmdbID, kind)
			if err != nil {
				errors["fanart"] = err.Error()
				continue
			}
			for _, u := range urls {
				add(u, "fanart")
			}
		case "douban", "bangumi", "tvdb", "omdb":
			res, err := scrapeProviderImages(name, keyword, year, keys, &ScrapeResult{
				Extra: map[string]any{"tmdb_id": currentTmdbID},
			})
			if err != nil {
				errors[name] = err.Error()
				continue
			}
			add(imageFieldByKind(res, kind), name)
		}
	}

	return candidates, errors, scraped
}

func imageFieldByKind(res *ScrapeResult, kind string) string {
	if res == nil {
		return ""
	}
	switch kind {
	case "backdrop":
		return res.Backdrop
	case "logo":
		return res.Logo
	default:
		return res.Poster
	}
}

// fetchTmdbImageList searches TMDb by keyword/year, then fetches the full image set for
// the matched movie or TV item. Returns all URLs for the requested kind plus the TMDb id.
func fetchTmdbImageList(apiKey, keyword string, year int, kind string) ([]string, int64, error) {
	apiKey = strings.TrimSpace(apiKey)
	keyword = strings.TrimSpace(keyword)
	if apiKey == "" {
		return nil, 0, fmt.Errorf("tmdb api key missing")
	}
	if keyword == "" {
		return nil, 0, fmt.Errorf("tmdb query empty")
	}
	u := "https://api.themoviedb.org/3/search/multi?api_key=" + url.QueryEscape(apiKey) +
		"&query=" + url.QueryEscape(keyword) + "&language=zh-CN&page=1&include_adult=false"
	if year > 0 {
		u += "&year=" + strconv.Itoa(year)
	}
	body, err := httpGetJSON(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, 0, err
	}
	var search struct {
		Results []struct {
			ID        int64  `json:"id"`
			MediaType string `json:"media_type"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &search); err != nil {
		return nil, 0, fmt.Errorf("tmdb parse: %w", err)
	}
	if len(search.Results) == 0 {
		return nil, 0, nil
	}
	x := search.Results[0]
	imgPath := "movie"
	if strings.EqualFold(x.MediaType, "tv") {
		imgPath = "tv"
	}
	imgURL := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d/images?api_key=%s", imgPath, x.ID, url.QueryEscape(apiKey))
	imgBody, err := httpGetJSON(imgURL, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, 0, err
	}
	var imgs struct {
		Posters []struct {
			FilePath string `json:"file_path"`
		} `json:"posters"`
		Backdrops []struct {
			FilePath string `json:"file_path"`
		} `json:"backdrops"`
		Logos []struct {
			FilePath string `json:"file_path"`
		} `json:"logos"`
	}
	_ = json.Unmarshal(imgBody, &imgs)
	base := "https://image.tmdb.org/t/p/original"
	var out []string
	switch kind {
	case "backdrop":
		for _, p := range imgs.Backdrops {
			if p.FilePath != "" {
				out = append(out, pickImage(base, p.FilePath))
			}
		}
	case "logo":
		for _, p := range imgs.Logos {
			if p.FilePath != "" {
				out = append(out, pickImage(base, p.FilePath))
			}
		}
	default:
		for _, p := range imgs.Posters {
			if p.FilePath != "" {
				out = append(out, pickImage(base, p.FilePath))
			}
		}
	}
	return out, x.ID, nil
}

// fetchFanartImageList fetches the fanart.tv movie artwork set and returns all URLs for
// the requested kind (movieposter / moviebackground / hdmovielogo).
func fetchFanartImageList(apiKey, tmdbID, kind string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	tmdbID = strings.TrimSpace(tmdbID)
	if apiKey == "" || tmdbID == "" {
		return nil, fmt.Errorf("fanart missing args")
	}
	u := "https://webservice.fanart.tv/v3/movies/" + url.PathEscape(tmdbID)
	b, err := httpGetJSON(u, map[string]string{"api-key": apiKey})
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("fanart parse: %w", err)
	}
	var key string
	switch kind {
	case "backdrop":
		key = "moviebackground"
	case "logo":
		key = "hdmovielogo"
	default:
		key = "movieposter"
	}
	return allURLs(resp[key]), nil
}

// allURLs extracts every "url" string from an array-of-maps value (fanart artwork lists).
func allURLs(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if u, _ := m["url"].(string); strings.TrimSpace(u) != "" {
			out = append(out, strings.TrimSpace(u))
		}
	}
	return out
}
