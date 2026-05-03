package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var onlineHTTP = &http.Client{Timeout: 12 * time.Second}

func ScrapeOnline(title, scraperName string, cfg Config) (*ScrapeResult, error) {
	keyword, year := ExtractSearch(title)
	if keyword == "" {
		keyword = title
	}
	providers := cfg.Providers
	if len(providers) == 0 {
		providers = []string{"tmdb", "omdb", "bangumi", "tvdb", "douban", "fanart"}
	}
	out := &ScrapeResult{
		Source:  "online-aggregate",
		Sources: providers,
		Title:   keyword,
		Genres:  []string{},
		Extra: map[string]any{
			"providers":      providers,
			"search_keyword": keyword,
			"search_year":    year,
		},
	}
	_ = scraperName

	var got bool
	providerErrors := map[string]map[string]string{}
	for _, p := range providers {
		name := strings.ToLower(strings.TrimSpace(p))
		switch name {
		case "tmdb":
			if r, err := scrapeTMDB(keyword, year, cfg.APIKeys["tmdb"]); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		case "omdb":
			if r, err := scrapeOMDb(keyword, year, cfg.APIKeys["omdb"]); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		case "bangumi":
			if r, err := scrapeBangumi(keyword); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		case "tvdb":
			if r, err := scrapeTVDB(keyword, year, cfg.APIKeys["tvdb"]); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		case "douban":
			if r, err := scrapeDouban(keyword); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		case "fanart":
			tmdbID := fmt.Sprint(out.Extra["tmdb_id"])
			if r, err := scrapeFanart(tmdbID, cfg.APIKeys["fanart"]); err == nil && r != nil {
				mergeResult(out, r)
				got = true
			} else if err != nil {
				providerErrors[name] = classifyProviderError(name, err)
			}
		}
	}
	if len(providerErrors) > 0 {
		out.Extra["provider_errors"] = providerErrors
	}
	if !got {
		return nil, fmt.Errorf("all providers failed: %s", summarizeProviderErrors(providerErrors))
	}
	return out, nil
}

func scrapeTMDB(keyword string, year int, apiKey string) (*ScrapeResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	u := "https://api.themoviedb.org/3/search/multi?api_key=" + url.QueryEscape(apiKey) +
		"&query=" + url.QueryEscape(keyword) + "&language=zh-CN&page=1&include_adult=false"
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
			BackdropPath string  `json:"backdrop_path"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			VoteAverage  float64 `json:"vote_average"`
			ID           int64   `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("tmdb empty")
	}
	x := resp.Results[0]
	title := x.Title
	if title == "" {
		title = x.Name
	}
	release := x.ReleaseDate
	if release == "" {
		release = x.FirstAirDate
	}
	imgBase := "https://image.tmdb.org/t/p/original"
	return &ScrapeResult{
		Source:      "tmdb",
		Title:       title,
		Overview:    x.Overview,
		Poster:      pickImage(imgBase, x.PosterPath),
		Backdrop:    pickImage(imgBase, x.BackdropPath),
		ReleaseDate: release,
		Rating:      x.VoteAverage,
		Extra: map[string]any{
			"poster":    pickImage(imgBase, x.PosterPath),
			"backdrop":  pickImage(imgBase, x.BackdropPath),
			"tmdb_id":   x.ID,
			"tmdb_type": x.MediaType,
		},
	}, nil
}

func scrapeOMDb(keyword string, year int, apiKey string) (*ScrapeResult, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("omdb api key missing")
	}
	u := "https://www.omdbapi.com/?apikey=" + url.QueryEscape(apiKey) + "&t=" + url.QueryEscape(keyword) + "&plot=full"
	if year > 0 {
		u += "&y=" + strconv.Itoa(year)
	}
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
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if !strings.EqualFold(resp.Response, "True") {
		return nil, fmt.Errorf("omdb no result")
	}
	rating, _ := strconv.ParseFloat(resp.IMDBRating, 64)
	genres := splitComma(resp.Genre)
	return &ScrapeResult{
		Source:      "omdb",
		Title:       resp.Title,
		Overview:    resp.Plot,
		Poster:      noneAsEmpty(resp.Poster),
		ReleaseDate: resp.Released,
		Rating:      rating,
		Genres:      genres,
		Extra: map[string]any{
			"poster": noneAsEmpty(resp.Poster),
		},
	}, nil
}

func scrapeBangumi(keyword string) (*ScrapeResult, error) {
	u := "https://api.bgm.tv/search/subject/" + url.PathEscape(keyword) + "?type=2&responseGroup=small&max_results=1&start=0"
	body, err := httpGetJSON(u, map[string]string{"User-Agent": "knox-media/1.0"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []struct {
			Name      string `json:"name"`
			NameCN    string `json:"name_cn"`
			Summary   string `json:"summary"`
			AirDate   string `json:"air_date"`
			Images    struct {
				Large string `json:"large"`
				Common string `json:"common"`
			} `json:"images"`
			Rating struct {
				Score float64 `json:"score"`
			} `json:"rating"`
		} `json:"list"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.List) == 0 {
		return nil, fmt.Errorf("bangumi empty")
	}
	x := resp.List[0]
	title := x.NameCN
	if title == "" {
		title = x.Name
	}
	poster := x.Images.Large
	if poster == "" {
		poster = x.Images.Common
	}
	return &ScrapeResult{
		Source:      "bangumi",
		Title:       title,
		Overview:    x.Summary,
		Poster:      poster,
		ReleaseDate: x.AirDate,
		Rating:      x.Rating.Score,
		Extra: map[string]any{
			"poster": poster,
		},
	}, nil
}

func scrapeTVDB(keyword string, year int, keyRaw string) (*ScrapeResult, error) {
	keyRaw = strings.TrimSpace(keyRaw)
	if keyRaw == "" {
		return nil, fmt.Errorf("tvdb key missing")
	}
	apiKey := keyRaw
	pin := ""
	if strings.Contains(keyRaw, ":") {
		parts := strings.SplitN(keyRaw, ":", 2)
		apiKey = strings.TrimSpace(parts[0])
		pin = strings.TrimSpace(parts[1])
	}
	bodyReq := map[string]string{"apikey": apiKey}
	if pin != "" {
		bodyReq["pin"] = pin
	}
	js, _ := json.Marshal(bodyReq)
	req, _ := http.NewRequest(http.MethodPost, "https://api4.thetvdb.com/v4/login", strings.NewReader(string(js)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := onlineHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tvdb login %d", resp.StatusCode)
	}
	loginBody, _ := io.ReadAll(resp.Body)
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if json.Unmarshal(loginBody, &login) != nil || login.Data.Token == "" {
		return nil, fmt.Errorf("tvdb token missing")
	}
	u := "https://api4.thetvdb.com/v4/search?query=" + url.QueryEscape(keyword) + "&type=series"
	if year > 0 {
		u += "&year=" + strconv.Itoa(year)
	}
	b, err := httpGetJSON(u, map[string]string{
		"Authorization": "Bearer " + login.Data.Token,
		"Accept":        "application/json",
	})
	if err != nil {
		return nil, err
	}
	var s struct {
		Data []struct {
			Name     string `json:"name"`
			Overview string `json:"overview"`
			FirstAired string `json:"firstAired"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &s) != nil || len(s.Data) == 0 {
		return nil, fmt.Errorf("tvdb empty")
	}
	x := s.Data[0]
	return &ScrapeResult{
		Source:      "tvdb",
		Title:       x.Name,
		Overview:    x.Overview,
		ReleaseDate: x.FirstAired,
	}, nil
}

func scrapeDouban(keyword string) (*ScrapeResult, error) {
	u := "https://movie.douban.com/j/subject_suggest?q=" + url.QueryEscape(keyword)
	b, err := httpGetJSON(u, map[string]string{
		"User-Agent": "Mozilla/5.0",
		"Referer":    "https://movie.douban.com/",
	})
	if err != nil {
		return nil, err
	}
	var items []struct {
		Title   string `json:"title"`
		Year    string `json:"year"`
		Img     string `json:"img"`
		SubTitle string `json:"sub_title"`
	}
	if json.Unmarshal(b, &items) != nil || len(items) == 0 {
		return nil, fmt.Errorf("douban empty")
	}
	x := items[0]
	return &ScrapeResult{
		Source:      "douban",
		Title:       x.Title,
		Overview:    x.SubTitle,
		Poster:      x.Img,
		ReleaseDate: x.Year,
		Extra: map[string]any{
			"poster": x.Img,
		},
	}, nil
}

func scrapeFanart(tmdbID, apiKey string) (*ScrapeResult, error) {
	if strings.TrimSpace(tmdbID) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("fanart missing args")
	}
	u := "https://webservice.fanart.tv/v3/movies/" + url.PathEscape(tmdbID)
	b, err := httpGetJSON(u, map[string]string{"api-key": apiKey})
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("fanart parse")
	}
	poster := firstURL(resp["movieposter"])
	backdrop := firstURL(resp["moviebackground"])
	logo := firstURL(resp["hdmovielogo"])
	if poster == "" && backdrop == "" && logo == "" {
		return nil, fmt.Errorf("fanart empty")
	}
	return &ScrapeResult{
		Source:   "fanart",
		Poster:   poster,
		Backdrop: backdrop,
		Logo:     logo,
		Extra: map[string]any{
			"poster":   poster,
			"backdrop": backdrop,
			"logo":     logo,
		},
	}, nil
}

func httpGetJSON(u string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := onlineHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func mergeResult(dst *ScrapeResult, src *ScrapeResult) {
	if src == nil || dst == nil {
		return
	}
	if dst.Title == "" && src.Title != "" {
		dst.Title = src.Title
	}
	if src.Overview != "" && (dst.Overview == "" || len(src.Overview) > len(dst.Overview)) {
		dst.Overview = src.Overview
	}
	if dst.Poster == "" && src.Poster != "" {
		dst.Poster = src.Poster
	}
	if dst.Backdrop == "" && src.Backdrop != "" {
		dst.Backdrop = src.Backdrop
	}
	if dst.Logo == "" && src.Logo != "" {
		dst.Logo = src.Logo
	}
	if dst.ReleaseDate == "" && src.ReleaseDate != "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	if dst.Rating == 0 && src.Rating > 0 {
		dst.Rating = src.Rating
	}
	if len(dst.Genres) == 0 && len(src.Genres) > 0 {
		dst.Genres = src.Genres
	}
	if dst.Extra == nil {
		dst.Extra = map[string]any{}
	}
	for k, v := range src.Extra {
		if _, ok := dst.Extra[k]; !ok {
			dst.Extra[k] = v
		}
	}
}

func splitComma(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		n := strings.TrimSpace(p)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func pickImage(base, p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(p, "/")
}

func noneAsEmpty(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "N/A") {
		return ""
	}
	return v
}

func firstURL(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	item, ok := arr[0].(map[string]any)
	if !ok {
		return ""
	}
	u, _ := item["url"].(string)
	return strings.TrimSpace(u)
}

func classifyProviderError(provider string, err error) map[string]string {
	msg := strings.TrimSpace(err.Error())
	lower := strings.ToLower(msg)
	category := "remote_error"
	switch {
	case strings.Contains(lower, "key missing") || strings.Contains(lower, "api key missing") || strings.Contains(lower, "missing args"):
		category = "key_missing"
	case strings.Contains(lower, "token missing") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "http 401") || strings.Contains(lower, "http 403"):
		category = "auth_error"
	case strings.Contains(lower, "http 429") || strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit"):
		category = "quota_limited"
	case strings.Contains(lower, "http 5"):
		category = "remote_error"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused"):
		category = "network_error"
	case strings.Contains(lower, "empty") || strings.Contains(lower, "no result"):
		category = "no_result"
	}
	return map[string]string{
		"provider": provider,
		"category": category,
		"message":  msg,
	}
}

func summarizeProviderErrors(errs map[string]map[string]string) string {
	if len(errs) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, len(errs))
	for provider, detail := range errs {
		cat := strings.TrimSpace(detail["category"])
		if cat == "" {
			cat = "remote_error"
		}
		parts = append(parts, provider+":"+cat)
	}
	return strings.Join(parts, "; ")
}

