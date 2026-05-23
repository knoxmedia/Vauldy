package scraper

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MatchCandidate is one searchable metadata hit for manual Plex-style matching.
type MatchCandidate struct {
	Source      string `json:"source"`
	ExternalID  string `json:"external_id"`
	MediaType   string `json:"media_type,omitempty"`
	Title       string `json:"title"`
	Overview    string `json:"overview,omitempty"`
	Poster      string `json:"poster,omitempty"`
	Year        int    `json:"year,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

func defaultMatchLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func normalizeMatchLanguage(language string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		return "zh-CN"
	}
	return language
}

// SearchMatchCandidates searches a single metadata provider for manual match candidates.
func SearchMatchCandidates(query string, year int, source, language string, cfg Config, limit int) ([]MatchCandidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "tmdb"
	}
	language = normalizeMatchLanguage(language)
	limit = defaultMatchLimit(limit)
	keys := cfg.APIKeys
	switch source {
	case "tmdb":
		return searchTMDBCandidates(query, year, language, keys["tmdb"], limit)
	case "douban":
		return searchDoubanCandidates(query, year, limit)
	case "bangumi":
		return searchBangumiCandidates(query, limit)
	case "omdb":
		item, err := searchOMDbCandidate(query, year, keys["omdb"])
		if err != nil {
			return nil, err
		}
		return []MatchCandidate{item}, nil
	case "tvdb":
		return searchTVDBCandidates(query, year, keys["tvdb"], limit)
	default:
		return nil, fmt.Errorf("unsupported match source: %s", source)
	}
}

// FetchMatchByExternalID loads full scrape metadata for a user-selected candidate.
func FetchMatchByExternalID(source, externalID, mediaType, language string, cfg Config) (*ScrapeResult, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	externalID = strings.TrimSpace(externalID)
	if source == "" || externalID == "" {
		return nil, fmt.Errorf("source and external_id required")
	}
	language = normalizeMatchLanguage(language)
	keys := cfg.APIKeys
	switch source {
	case "tmdb":
		return fetchTMDBByID(externalID, mediaType, language, keys["tmdb"])
	case "douban":
		return fetchDoubanByID(externalID)
	case "bangumi":
		return fetchBangumiByID(externalID)
	case "omdb":
		return fetchOMDbByID(externalID, keys["omdb"])
	case "tvdb":
		return fetchTVDBByID(externalID, keys["tvdb"])
	default:
		return nil, fmt.Errorf("unsupported match source: %s", source)
	}
}

func searchTMDBCandidates(keyword string, year int, language, apiKey string, limit int) ([]MatchCandidate, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	u := "https://api.themoviedb.org/3/search/multi?api_key=" + url.QueryEscape(apiKey) +
		"&query=" + url.QueryEscape(keyword) + "&language=" + url.QueryEscape(language) +
		"&page=1&include_adult=false"
	if year > 0 {
		u += "&year=" + strconv.Itoa(year)
	}
	body, err := httpGetJSON(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			ID           int64   `json:"id"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	imgBase := "https://image.tmdb.org/t/p/w342"
	out := make([]MatchCandidate, 0, len(resp.Results))
	for _, x := range resp.Results {
		if x.MediaType != "movie" && x.MediaType != "tv" {
			continue
		}
		title := x.Title
		if title == "" {
			title = x.Name
		}
		release := x.ReleaseDate
		if release == "" {
			release = x.FirstAirDate
		}
		out = append(out, MatchCandidate{
			Source:      "tmdb",
			ExternalID:  strconv.FormatInt(x.ID, 10),
			MediaType:   x.MediaType,
			Title:       title,
			Overview:    x.Overview,
			Poster:      pickImage(imgBase, x.PosterPath),
			Year:        yearFromDate(release),
			ReleaseDate: release,
		})
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tmdb empty")
	}
	return out, nil
}

func fetchTMDBByID(externalID, mediaType, language, apiKey string) (*ScrapeResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	id := strings.TrimSpace(externalID)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType != "tv" {
		mediaType = "movie"
	}
	u := fmt.Sprintf("https://api.themoviedb.org/3/%s/%s?api_key=%s&language=%s",
		mediaType, url.PathEscape(id), url.QueryEscape(apiKey), url.QueryEscape(language))
	body, err := httpGetJSON(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Title        string  `json:"title"`
		Name         string  `json:"name"`
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		ReleaseDate  string  `json:"release_date"`
		FirstAirDate string  `json:"first_air_date"`
		VoteAverage  float64 `json:"vote_average"`
		ID           int64   `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	title := resp.Title
	if title == "" {
		title = resp.Name
	}
	release := resp.ReleaseDate
	if release == "" {
		release = resp.FirstAirDate
	}
	imgBase := "https://image.tmdb.org/t/p/original"
	return &ScrapeResult{
		Source:      "tmdb",
		Title:       title,
		Overview:    resp.Overview,
		Poster:      pickImage(imgBase, resp.PosterPath),
		Backdrop:    pickImage(imgBase, resp.BackdropPath),
		ReleaseDate: release,
		Rating:      resp.VoteAverage,
		Extra: map[string]any{
			"poster":    pickImage(imgBase, resp.PosterPath),
			"backdrop":  pickImage(imgBase, resp.BackdropPath),
			"tmdb_id":   resp.ID,
			"tmdb_type": mediaType,
		},
	}, nil
}

func searchDoubanCandidates(keyword string, year, limit int) ([]MatchCandidate, error) {
	params := url.Values{}
	params.Set("q", keyword)
	params.Set("count", strconv.Itoa(limit))
	u := "https://movie.douban.com/j/subject_suggest?" + params.Encode()
	b, err := httpGetJSON(u, doubanRequestHeaders())
	if err != nil {
		return nil, err
	}
	var items []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Year     string `json:"year"`
		Img      string `json:"img"`
		SubTitle string `json:"sub_title"`
		Type     string `json:"type"`
	}
	if json.Unmarshal(b, &items) != nil || len(items) == 0 {
		return nil, fmt.Errorf("douban empty")
	}
	out := make([]MatchCandidate, 0, len(items))
	for _, x := range items {
		if x.Type != "" && x.Type != "movie" && x.Type != "tv" {
			continue
		}
		yearInt, _ := strconv.Atoi(strings.TrimSpace(x.Year))
		if year > 0 && yearInt > 0 && absInt(yearInt-year) > 1 {
			continue
		}
		overview := strings.TrimSpace(x.SubTitle)
		out = append(out, MatchCandidate{
			Source:     "douban",
			ExternalID: x.ID,
			MediaType:  x.Type,
			Title:      x.Title,
			Overview:   overview,
			Poster:     x.Img,
			Year:       yearInt,
		})
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("douban empty")
	}
	return out, nil
}

func fetchDoubanByID(externalID string) (*ScrapeResult, error) {
	u := "https://movie.douban.com/j/subject_abstract?subject_id=" + url.QueryEscape(externalID)
	b, err := httpGetJSON(u, doubanRequestHeaders())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Subject struct {
			Title  string `json:"title"`
			Intro  string `json:"short_info"`
			PicURL string `json:"pic_url"`
			Rating struct {
				Value float64 `json:"value"`
			} `json:"rating"`
		} `json:"subject"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("douban parse")
	}
	title := resp.Subject.Title
	if title == "" {
		title = externalID
	}
	return &ScrapeResult{
		Source:   "douban",
		Title:    title,
		Overview: resp.Subject.Intro,
		Poster:   resp.Subject.PicURL,
		Rating:   resp.Subject.Rating.Value,
		Extra: map[string]any{
			"poster":    resp.Subject.PicURL,
			"douban_id": externalID,
		},
	}, nil
}

func searchBangumiCandidates(keyword string, limit int) ([]MatchCandidate, error) {
	u := "https://api.bgm.tv/search/subject/" + url.PathEscape(keyword) +
		"?type=2&responseGroup=small&max_results=" + strconv.Itoa(limit) + "&start=0"
	body, err := httpGetJSON(u, map[string]string{"User-Agent": "knox-media/1.0"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			NameCN  string `json:"name_cn"`
			Summary string `json:"summary"`
			AirDate string `json:"air_date"`
			Images  struct {
				Large  string `json:"large"`
				Common string `json:"common"`
			} `json:"images"`
		} `json:"list"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := make([]MatchCandidate, 0, len(resp.List))
	for _, x := range resp.List {
		title := x.NameCN
		if title == "" {
			title = x.Name
		}
		poster := x.Images.Large
		if poster == "" {
			poster = x.Images.Common
		}
		out = append(out, MatchCandidate{
			Source:      "bangumi",
			ExternalID:  strconv.FormatInt(x.ID, 10),
			MediaType:   "tv",
			Title:       title,
			Overview:    x.Summary,
			Poster:      poster,
			Year:        yearFromDate(x.AirDate),
			ReleaseDate: x.AirDate,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bangumi empty")
	}
	return out, nil
}

func fetchBangumiByID(externalID string) (*ScrapeResult, error) {
	u := "https://api.bgm.tv/subject/" + url.PathEscape(externalID)
	body, err := httpGetJSON(u, map[string]string{"User-Agent": "knox-media/1.0"})
	if err != nil {
		return nil, err
	}
	var x struct {
		Name    string `json:"name"`
		NameCN  string `json:"name_cn"`
		Summary string `json:"summary"`
		AirDate string `json:"air_date"`
		Images  struct {
			Large  string `json:"large"`
			Common string `json:"common"`
		} `json:"images"`
		Rating struct {
			Score float64 `json:"score"`
		} `json:"rating"`
	}
	if err := json.Unmarshal(body, &x); err != nil {
		return nil, err
	}
	title := x.NameCN
	if title == "" {
		title = x.Name
	}
	poster := x.Images.Large
	if poster == "" {
		poster = x.Images.Common
	}
	id, _ := strconv.ParseInt(externalID, 10, 64)
	return &ScrapeResult{
		Source:      "bangumi",
		Title:       title,
		Overview:    x.Summary,
		Poster:      poster,
		ReleaseDate: x.AirDate,
		Rating:      x.Rating.Score,
		Extra: map[string]any{
			"poster":     poster,
			"bangumi_id": id,
		},
	}, nil
}

func searchOMDbCandidate(keyword string, year int, apiKey string) (MatchCandidate, error) {
	if strings.TrimSpace(apiKey) == "" {
		return MatchCandidate{}, fmt.Errorf("omdb api key missing")
	}
	u := "https://www.omdbapi.com/?apikey=" + url.QueryEscape(apiKey) + "&t=" + url.QueryEscape(keyword) + "&plot=short"
	if year > 0 {
		u += "&y=" + strconv.Itoa(year)
	}
	body, err := httpGetJSON(u, nil)
	if err != nil {
		return MatchCandidate{}, err
	}
	var resp struct {
		Response string `json:"Response"`
		Title    string `json:"Title"`
		Plot     string `json:"Plot"`
		Poster   string `json:"Poster"`
		Released string `json:"Released"`
		imdbID   string `json:"imdbID"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return MatchCandidate{}, err
	}
	if !strings.EqualFold(resp.Response, "True") {
		return MatchCandidate{}, fmt.Errorf("omdb no result")
	}
	return MatchCandidate{
		Source:      "omdb",
		ExternalID:  resp.imdbID,
		Title:       resp.Title,
		Overview:    resp.Plot,
		Poster:      noneAsEmpty(resp.Poster),
		Year:        yearFromDate(resp.Released),
		ReleaseDate: resp.Released,
	}, nil
}

func fetchOMDbByID(externalID, apiKey string) (*ScrapeResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("omdb api key missing")
	}
	u := "https://www.omdbapi.com/?apikey=" + url.QueryEscape(apiKey) + "&i=" + url.QueryEscape(externalID) + "&plot=full"
	body, err := httpGetJSON(u, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response   string `json:"Response"`
		Title      string `json:"Title"`
		Plot       string `json:"Plot"`
		Poster     string `json:"Poster"`
		Released   string `json:"Released"`
		IMDBRating string `json:"imdbRating"`
		Genre      string `json:"Genre"`
		imdbID     string `json:"imdbID"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if !strings.EqualFold(resp.Response, "True") {
		return nil, fmt.Errorf("omdb empty")
	}
	rating, _ := strconv.ParseFloat(resp.IMDBRating, 64)
	genres := []string{}
	for _, g := range strings.Split(resp.Genre, ",") {
		g = strings.TrimSpace(g)
		if g != "" {
			genres = append(genres, g)
		}
	}
	return &ScrapeResult{
		Source:      "omdb",
		Title:       resp.Title,
		Overview:    resp.Plot,
		Poster:      noneAsEmpty(resp.Poster),
		ReleaseDate: resp.Released,
		Rating:      rating,
		Genres:      genres,
		Extra: map[string]any{
			"poster":   noneAsEmpty(resp.Poster),
			"imdb_id":  resp.imdbID,
		},
	}, nil
}

func searchTVDBCandidates(keyword string, year int, keyRaw string, limit int) ([]MatchCandidate, error) {
	res, err := scrapeTVDB(keyword, year, keyRaw)
	if err != nil {
		return nil, err
	}
	return []MatchCandidate{{
		Source:      "tvdb",
		ExternalID:  keyword,
		MediaType:   "series",
		Title:       res.Title,
		Overview:    res.Overview,
		ReleaseDate: res.ReleaseDate,
		Year:        yearFromDate(res.ReleaseDate),
	}}, nil
}

func fetchTVDBByID(externalID, keyRaw string) (*ScrapeResult, error) {
	return scrapeTVDB(externalID, 0, keyRaw)
}

func yearFromDate(raw string) int {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 4 {
		if y, err := strconv.Atoi(raw[:4]); err == nil && y >= 1800 && y <= 2100 {
			return y
		}
	}
	return 0
}

// HasScrapedMetaJSON reports whether stored meta_json contains meaningful scrape data.
func HasScrapedMetaJSON(metaJSON string) bool {
	if strings.TrimSpace(metaJSON) == "" {
		return false
	}
	var raw map[string]any
	if json.Unmarshal([]byte(metaJSON), &raw) != nil {
		return false
	}
	sv, ok := raw["scrape"].(map[string]any)
	if !ok || len(sv) == 0 {
		return false
	}
	b, _ := json.Marshal(sv)
	var res ScrapeResult
	if json.Unmarshal(b, &res) != nil {
		return false
	}
	return HasMeaningfulScrapeData(&res)
}
