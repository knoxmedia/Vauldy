package scraper

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TVScrapeContext holds parsed TV episode context from scan metadata.
type TVScrapeContext struct {
	SeriesTitle string
	Year        int
	Season      int
	Episode     int
	TMDBID      string
	TVDBID      string
}

// ParseTVScrapeContext reads TV episode fields from media meta_json.
func ParseTVScrapeContext(metaJSON string) TVScrapeContext {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return TVScrapeContext{}
	}
	var root map[string]any
	if json.Unmarshal([]byte(metaJSON), &root) != nil || root == nil {
		return TVScrapeContext{}
	}
	tv, _ := root["tv"].(map[string]any)
	if tv == nil {
		return TVScrapeContext{}
	}
	ctx := TVScrapeContext{
		SeriesTitle: stringField(tv["series_title"]),
		TMDBID:      stringField(tv["tmdb_id"]),
		TVDBID:      stringField(tv["tvdb_id"]),
	}
	ctx.Year = intField(tv["year"])
	ctx.Season = intField(tv["season"])
	ctx.Episode = intField(tv["episode"])
	return ctx
}

func (c TVScrapeContext) ValidEpisode() bool {
	return c.Season >= 0 && c.Episode > 0 && strings.TrimSpace(c.SeriesTitle) != ""
}

// ScrapeTVEpisode resolves series metadata and fetches episode-level details.
func ScrapeTVEpisode(cfg Config, ctx TVScrapeContext, libraryType string) (*ScrapeResult, error) {
	if !ctx.ValidEpisode() {
		return nil, fmt.Errorf("invalid tv scrape context")
	}
	providers := tvScrapeProviderOrder(cfg.Providers, libraryType)
	var lastErr error
	for _, p := range providers {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "tmdb":
			if res, err := scrapeTMDBEpisode(cfg, ctx); err == nil && res != nil {
				return res, nil
			} else if err != nil {
				lastErr = err
			}
		case "tvdb":
			if res, err := scrapeTVDBEpisode(cfg, ctx); err == nil && res != nil {
				return res, nil
			} else if err != nil {
				lastErr = err
			}
		case "bangumi":
			if strings.EqualFold(strings.TrimSpace(libraryType), "anime") {
				if res, err := scrapeBangumiEpisode(cfg, ctx); err == nil && res != nil {
					return res, nil
				} else if err != nil {
					lastErr = err
				}
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("tv episode scrape: no provider matched")
}

func tvScrapeProviderOrder(providers []string, libraryType string) []string {
	base := append([]string(nil), providers...)
	if len(base) == 0 {
		if strings.EqualFold(strings.TrimSpace(libraryType), "anime") {
			return []string{"bangumi", "tmdb", "tvdb"}
		}
		return []string{"tmdb", "tvdb", "bangumi"}
	}
	priority := []string{"tmdb", "tvdb", "bangumi"}
	if strings.EqualFold(strings.TrimSpace(libraryType), "anime") {
		priority = []string{"bangumi", "tmdb", "tvdb"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(base))
	for _, p := range priority {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		for _, x := range base {
			if strings.EqualFold(strings.TrimSpace(x), p) {
				out = append(out, p)
				seen[p] = true
				break
			}
		}
	}
	for _, x := range base {
		p := strings.ToLower(strings.TrimSpace(x))
		if p != "" && !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}

func scrapeTMDBEpisode(cfg Config, ctx TVScrapeContext) (*ScrapeResult, error) {
	key := cfg.APIKeys["tmdb"]
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	language := "zh-CN"
	seriesID := strings.TrimSpace(ctx.TMDBID)
	if seriesID == "" {
		candidates, err := searchTMDBCandidates(ctx.SeriesTitle, ctx.Year, language, key, 5)
		if err != nil {
			return nil, err
		}
		for _, c := range candidates {
			if c.MediaType == "tv" && strings.TrimSpace(c.ExternalID) != "" {
				seriesID = c.ExternalID
				break
			}
		}
		if seriesID == "" && len(candidates) > 0 {
			seriesID = candidates[0].ExternalID
		}
	}
	if seriesID == "" {
		return nil, fmt.Errorf("tmdb series not found")
	}
	seriesRes, err := fetchTMDBByID(seriesID, "tv", language, key)
	if err != nil {
		return nil, err
	}
	seasonNum := ctx.Season
	if seasonNum == 0 {
		seasonNum = 0 // specials
	}
	u := fmt.Sprintf("https://api.themoviedb.org/3/tv/%s/season/%d/episode/%d?api_key=%s&language=%s",
		url.PathEscape(seriesID), seasonNum, ctx.Episode, url.QueryEscape(key), url.QueryEscape(language))
	body, err := httpGetJSON(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		// Fall back to series-level metadata when episode endpoint misses (some specials).
		return buildTVResultFromSeries(seriesRes, ctx, "tmdb"), nil
	}
	var ep struct {
		Name        string  `json:"name"`
		Overview    string  `json:"overview"`
		StillPath   string  `json:"still_path"`
		AirDate     string  `json:"air_date"`
		EpisodeNum  int     `json:"episode_number"`
		SeasonNum   int     `json:"season_number"`
		VoteAverage float64 `json:"vote_average"`
	}
	if json.Unmarshal(body, &ep) != nil {
		return buildTVResultFromSeries(seriesRes, ctx, "tmdb"), nil
	}
	imgBase := "https://image.tmdb.org/t/p/original"
	still := pickImage(imgBase, ep.StillPath)
	seriesTitle := seriesRes.Title
	if seriesTitle == "" {
		seriesTitle = ctx.SeriesTitle
	}
	displayTitle := formatEpisodeDisplayTitle(seriesTitle, ctx.Season, ctx.Episode, ep.Name)
	overview := strings.TrimSpace(ep.Overview)
	if overview == "" {
		overview = seriesRes.Overview
	}
	poster := still
	if poster == "" {
		poster = seriesRes.Poster
	}
	extra := cloneExtraMap(seriesRes.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["tmdb_id"] = seriesID
	extra["tmdb_type"] = "tv"
	extra["season"] = ctx.Season
	extra["episode"] = ctx.Episode
	extra["episode_name"] = ep.Name
	extra["series_title"] = seriesTitle
	extra["series_overview"] = seriesRes.Overview
	extra["series_poster"] = seriesRes.Poster
	extra["series_backdrop"] = seriesRes.Backdrop
	if still != "" {
		extra["episode_still"] = still
		extra["poster"] = still
	}
	return &ScrapeResult{
		Source:      "tmdb",
		Title:       displayTitle,
		Overview:    overview,
		Poster:      poster,
		Backdrop:    seriesRes.Backdrop,
		ReleaseDate: ep.AirDate,
		Rating:      ep.VoteAverage,
		Genres:      seriesRes.Genres,
		Extra:       extra,
	}, nil
}

func scrapeTVDBEpisode(cfg Config, ctx TVScrapeContext) (*ScrapeResult, error) {
	seriesID := strings.TrimSpace(ctx.TVDBID)
	if seriesID == "" {
		res, err := scrapeTVDB(ctx.SeriesTitle, ctx.Year, cfg.APIKeys["tvdb"])
		if err != nil {
			return nil, err
		}
		if res.Extra != nil {
			if v, ok := res.Extra["tvdb_id"]; ok {
				seriesID = stringField(v)
			}
		}
		if seriesID == "" {
			return buildTVResultFromGeneric(res, ctx, "tvdb"), nil
		}
	}
	res, err := scrapeTVDB(ctx.SeriesTitle, ctx.Year, cfg.APIKeys["tvdb"])
	if err != nil {
		return nil, err
	}
	return buildTVResultFromGeneric(res, ctx, "tvdb"), nil
}

func scrapeBangumiEpisode(cfg Config, ctx TVScrapeContext) (*ScrapeResult, error) {
	_ = cfg
	res, err := scrapeBangumi(ctx.SeriesTitle)
	if err != nil {
		return nil, err
	}
	return buildTVResultFromGeneric(res, ctx, "bangumi"), nil
}

func buildTVResultFromSeries(series *ScrapeResult, ctx TVScrapeContext, source string) *ScrapeResult {
	if series == nil {
		return nil
	}
	seriesTitle := series.Title
	if seriesTitle == "" {
		seriesTitle = ctx.SeriesTitle
	}
	displayTitle := formatEpisodeDisplayTitle(seriesTitle, ctx.Season, ctx.Episode, "")
	extra := cloneExtraMap(series.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["season"] = ctx.Season
	extra["episode"] = ctx.Episode
	extra["series_title"] = seriesTitle
	extra["series_overview"] = series.Overview
	extra["series_poster"] = series.Poster
	extra["series_backdrop"] = series.Backdrop
	extra["tmdb_type"] = "tv"
	return &ScrapeResult{
		Source:      source,
		Title:       displayTitle,
		Overview:    series.Overview,
		Poster:      series.Poster,
		Backdrop:    series.Backdrop,
		ReleaseDate: series.ReleaseDate,
		Rating:      series.Rating,
		Genres:      series.Genres,
		Extra:       extra,
	}
}

func buildTVResultFromGeneric(base *ScrapeResult, ctx TVScrapeContext, source string) *ScrapeResult {
	if base == nil {
		return nil
	}
	seriesTitle := base.Title
	if seriesTitle == "" {
		seriesTitle = ctx.SeriesTitle
	}
	displayTitle := formatEpisodeDisplayTitle(seriesTitle, ctx.Season, ctx.Episode, "")
	extra := cloneExtraMap(base.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["season"] = ctx.Season
	extra["episode"] = ctx.Episode
	extra["series_title"] = seriesTitle
	extra["series_overview"] = base.Overview
	extra["series_poster"] = base.Poster
	extra["series_backdrop"] = base.Backdrop
	extra["tmdb_type"] = "tv"
	return &ScrapeResult{
		Source:      source,
		Title:       displayTitle,
		Overview:    base.Overview,
		Poster:      base.Poster,
		Backdrop:    base.Backdrop,
		ReleaseDate: base.ReleaseDate,
		Rating:      base.Rating,
		Genres:      base.Genres,
		Extra:       extra,
	}
}

func formatEpisodeDisplayTitle(seriesTitle string, season, episode int, episodeName string) string {
	label := fmt.Sprintf("%s - S%02dE%02d", strings.TrimSpace(seriesTitle), season, episode)
	if strings.TrimSpace(episodeName) != "" {
		return label + " - " + strings.TrimSpace(episodeName)
	}
	return label
}

func cloneExtraMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func stringField(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case json.Number:
		return x.String()
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func intField(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}
