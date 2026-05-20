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
	keyword, altKeyword, year := ExtractSearchTerms(title)
	if keyword == "" {
		keyword = title
	}
	providers := cfg.Providers
	if s := strings.ToLower(strings.TrimSpace(scraperName)); s != "" && !isTaskSourceLabel(s) {
		providers = []string{s}
	} else {
		providers = orderProvidersForKeyword(cfg.Providers, keyword)
	}
	out := &ScrapeResult{
		Source:  "online-aggregate",
		Sources: providers,
		Title:   keyword,
		Genres:  []string{},
		Extra: map[string]any{
			"providers":           providers,
			"image_sources":       cfg.ImageSources,
			"search_keyword":      keyword,
			"search_keyword_alt":  altKeyword,
			"search_year":         year,
			"search_raw":          title,
		},
	}
	var got bool
	providerErrors := map[string]map[string]string{}
	for _, p := range providers {
		name := strings.ToLower(strings.TrimSpace(p))
		if !isMetadataProvider(name) {
			continue
		}
		r, err := scrapeProviderMetadata(name, keyword, altKeyword, year, cfg, out)
		if err == nil && r != nil {
			mergeMetadataResult(out, r)
			got = true
		} else if err != nil {
			providerErrors[name] = classifyProviderError(name, err)
		}
	}
	if len(providerErrors) > 0 {
		out.Extra["provider_errors"] = providerErrors
	}
	applyImageSources(out, keyword, year, cfg)
	if imgErrs, ok := out.Extra["image_provider_errors"].(map[string]map[string]string); ok && len(imgErrs) > 0 {
		if pe, ok := out.Extra["provider_errors"].(map[string]map[string]string); ok {
			for k, v := range imgErrs {
				pe[k] = v
			}
		} else {
			out.Extra["provider_errors"] = imgErrs
		}
	}
	if !got && !hasScrapedImages(out) {
		pe, _ := out.Extra["provider_errors"].(map[string]map[string]string)
		return out, fmt.Errorf("all providers failed: %s", summarizeProviderErrors(pe))
	}
	return out, nil
}

// isTaskSourceLabel reports scrape_task.source values that are not metadata provider names.
func isTaskSourceLabel(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "auto-scan", "manual":
		return true
	default:
		return false
	}
}

func hasScrapedImages(out *ScrapeResult) bool {
	if out == nil {
		return false
	}
	return out.Poster != "" || out.Backdrop != "" || out.Logo != ""
}

func isMetadataProvider(name string) bool {
	switch name {
	case "embedded", "screen_grabber":
		return false
	case "ai":
		return true
	default:
		return true
	}
}

func isOnlineImageProvider(name string) bool {
	switch name {
	case "tmdb", "omdb", "bangumi", "tvdb", "douban", "fanart":
		return true
	default:
		return false
	}
}

func scrapeProviderMetadata(name, keyword, altKeyword string, year int, cfg Config, out *ScrapeResult) (*ScrapeResult, error) {
	keys := cfg.APIKeys
	switch name {
	case "tmdb":
		return scrapeTMDBWithAlt(keyword, altKeyword, year, keys["tmdb"])
	case "omdb":
		return scrapeOMDbWithAlt(keyword, altKeyword, year, keys["omdb"])
	case "bangumi":
		return scrapeBangumi(keyword)
	case "tvdb":
		return scrapeTVDBWithAlt(keyword, altKeyword, year, keys["tvdb"])
	case "douban":
		return scrapeDouban(keyword, year)
	case "fanart":
		tmdbID := fmt.Sprint(out.Extra["tmdb_id"])
		return scrapeFanart(tmdbID, keys["fanart"])
	case "ai":
		rawTitle := ""
		if out != nil && out.Extra != nil {
			if v, ok := out.Extra["search_raw"].(string); ok {
				rawTitle = v
			}
		}
		return scrapeAI(keyword, altKeyword, year, cfg.AIProviders, rawTitle)
	default:
		return nil, fmt.Errorf("%s: unsupported metadata provider", name)
	}
}

func scrapeProviderImages(name, keyword string, year int, keys map[string]string, out *ScrapeResult) (*ScrapeResult, error) {
	switch name {
	case "tmdb":
		return scrapeTMDB(keyword, year, keys["tmdb"])
	case "omdb":
		return scrapeOMDb(keyword, year, keys["omdb"])
	case "bangumi":
		return scrapeBangumi(keyword)
	case "tvdb":
		return scrapeTVDB(keyword, year, keys["tvdb"])
	case "douban":
		return scrapeDouban(keyword, year)
	case "fanart":
		tmdbID := fmt.Sprint(out.Extra["tmdb_id"])
		return scrapeFanart(tmdbID, keys["fanart"])
	default:
		return nil, fmt.Errorf("%s: unsupported image provider", name)
	}
}

func applyImageSources(out *ScrapeResult, keyword string, year int, cfg Config) {
	sources := cfg.ImageSources
	if len(sources) == 0 {
		sources = []string{"tmdb", "omdb", "screen_grabber", "embedded"}
	}
	if out.Extra == nil {
		out.Extra = map[string]any{}
	}
	out.Extra["image_sources"] = sources
	imageErrors := map[string]map[string]string{}
	for _, p := range sources {
		name := strings.ToLower(strings.TrimSpace(p))
		if !isOnlineImageProvider(name) {
			continue
		}
		if out.Poster != "" && out.Backdrop != "" && out.Logo != "" {
			break
		}
		r, err := scrapeProviderImages(name, keyword, year, cfg.APIKeys, out)
		if err == nil && r != nil {
			mergeImageResult(out, r)
		} else if err != nil {
			imageErrors[name] = classifyProviderError(name, err)
		}
	}
	if len(imageErrors) > 0 {
		out.Extra["image_provider_errors"] = imageErrors
	}
}

func scrapeTMDBWithAlt(keyword, altKeyword string, year int, apiKey string) (*ScrapeResult, error) {
	res, err := scrapeTMDB(keyword, year, apiKey)
	if err == nil {
		return res, nil
	}
	alt := strings.TrimSpace(altKeyword)
	if alt == "" || strings.EqualFold(alt, keyword) {
		return nil, err
	}
	if res, altErr := scrapeTMDB(alt, year, apiKey); altErr == nil {
		return res, nil
	}
	return nil, err
}

func scrapeOMDbWithAlt(keyword, altKeyword string, year int, apiKey string) (*ScrapeResult, error) {
	res, err := scrapeOMDb(keyword, year, apiKey)
	if err == nil {
		return res, nil
	}
	alt := strings.TrimSpace(altKeyword)
	if alt == "" || strings.EqualFold(alt, keyword) {
		return nil, err
	}
	if res, altErr := scrapeOMDb(alt, year, apiKey); altErr == nil {
		return res, nil
	}
	return nil, err
}

func scrapeTVDBWithAlt(keyword, altKeyword string, year int, keyRaw string) (*ScrapeResult, error) {
	res, err := scrapeTVDB(keyword, year, keyRaw)
	if err == nil {
		return res, nil
	}
	alt := strings.TrimSpace(altKeyword)
	if alt == "" || strings.EqualFold(alt, keyword) {
		return nil, err
	}
	if res, altErr := scrapeTVDB(alt, year, keyRaw); altErr == nil {
		return res, nil
	}
	return nil, err
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
			Name       string `json:"name"`
			Overview   string `json:"overview"`
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

func scrapeDouban(keyword string, year int) (*ScrapeResult, error) {
	item, err := searchDoubanBest(keyword, year)
	if err != nil {
		return nil, err
	}
	overview := item.SubTitle
	rating := item.Rating
	if item.ID != "" {
		if detail, dErr := fetchDoubanAbstract(item.ID); dErr == nil {
			if detail.Overview != "" {
				overview = detail.Overview
			}
			if detail.Rating > 0 {
				rating = detail.Rating
			}
			if detail.Poster != "" {
				item.Img = detail.Poster
			}
		}
	}
	return &ScrapeResult{
		Source:      "douban",
		Title:       item.Title,
		Overview:    overview,
		Poster:      item.Img,
		ReleaseDate: strconv.Itoa(item.Year),
		Rating:      rating,
		Extra: map[string]any{
			"poster":    item.Img,
			"douban_id": item.ID,
		},
	}, nil
}

type doubanSuggestItem struct {
	ID       string
	Title    string
	Year     int
	Img      string
	SubTitle string
	Rating   float64
}

func searchDoubanBest(keyword string, year int) (*doubanSuggestItem, error) {
	params := url.Values{}
	params.Set("q", keyword)
	params.Set("count", "5")
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
		if year > 0 {
			return searchDoubanBest(keyword, 0)
		}
		return nil, fmt.Errorf("douban empty")
	}
	for _, x := range items {
		if x.Type != "" && x.Type != "movie" && x.Type != "tv" {
			continue
		}
		yearInt, _ := strconv.Atoi(strings.TrimSpace(x.Year))
		if year > 0 && yearInt > 0 && absInt(yearInt-year) > 1 {
			continue
		}
		return &doubanSuggestItem{
			ID: x.ID, Title: x.Title, Year: yearInt, Img: x.Img, SubTitle: x.SubTitle,
		}, nil
	}
	if year > 0 {
		return searchDoubanBest(keyword, 0)
	}
	return nil, fmt.Errorf("douban empty")
}

type doubanAbstract struct {
	Overview string
	Rating   float64
	Poster   string
}

func fetchDoubanAbstract(subjectID string) (doubanAbstract, error) {
	u := "https://movie.douban.com/j/subject_abstract?subject_id=" + url.QueryEscape(subjectID)
	b, err := httpGetJSON(u, doubanRequestHeaders())
	if err != nil {
		return doubanAbstract{}, err
	}
	var resp struct {
		Subject struct {
			Title   string   `json:"title"`
			Intro   string   `json:"short_info"`
			Genres  []string `json:"genres"`
			PicURL  string   `json:"pic_url"`
			Rating  struct {
				Value float64 `json:"value"`
			} `json:"rating"`
		} `json:"subject"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return doubanAbstract{}, fmt.Errorf("douban parse")
	}
	return doubanAbstract{
		Overview: resp.Subject.Intro,
		Rating:   resp.Subject.Rating.Value,
		Poster:   resp.Subject.PicURL,
	}, nil
}

func doubanRequestHeaders() map[string]string {
	return map[string]string{
		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Referer":         "https://movie.douban.com/",
		"Accept":          "application/json, text/javascript, */*; q=0.01",
		"Accept-Language": "zh-CN,zh;q=0.9",
		"X-Requested-With": "XMLHttpRequest",
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
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

func mergeMetadataResult(dst, src *ScrapeResult) {
	if src == nil || dst == nil {
		return
	}
	if dst.Title == "" && src.Title != "" {
		dst.Title = src.Title
	}
	if src.Overview != "" && (dst.Overview == "" || len(src.Overview) > len(dst.Overview)) {
		dst.Overview = src.Overview
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

func mergeImageResult(dst, src *ScrapeResult) {
	if src == nil || dst == nil {
		return
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
	if dst.Extra == nil {
		dst.Extra = map[string]any{}
	}
	for _, key := range []string{"poster", "backdrop", "logo"} {
		if _, ok := dst.Extra[key]; ok {
			continue
		}
		if v, ok := src.Extra[key]; ok {
			dst.Extra[key] = v
		}
	}
}

func mergeResult(dst, src *ScrapeResult) {
	if src == nil || dst == nil {
		return
	}
	mergeMetadataResult(dst, src)
	mergeImageResult(dst, src)
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
