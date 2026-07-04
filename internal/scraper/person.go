package scraper

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// tmdbAPIBase is the configurable TMDB API base URL.
// Defaults to official endpoint; can be overridden via SetTMDBAPIBase for proxy support.
var tmdbAPIBase = "https://api.themoviedb.org"

// tmdbImageBase is the configurable TMDB image base URL.
// Defaults to official endpoint; can be overridden via SetTMDBImageBase for proxy support.
var tmdbImageBase = "https://image.tmdb.org"

// SetTMDBAPIBase overrides the TMDB API base URL (for proxy support).
// Pass empty string to reset to official endpoint.
func SetTMDBAPIBase(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		tmdbAPIBase = "https://api.themoviedb.org"
	} else {
		tmdbAPIBase = strings.TrimRight(base, "/")
	}
}

// SetTMDBImageBase overrides the TMDB image base URL (for proxy support).
// Pass empty string to reset to official endpoint.
func SetTMDBImageBase(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		tmdbImageBase = "https://image.tmdb.org"
	} else {
		tmdbImageBase = strings.TrimRight(base, "/")
	}
}

// GetTMDBAPIBase returns the current TMDB API base URL.
func GetTMDBAPIBase() string {
	return tmdbAPIBase
}

// GetTMDBImageBase returns the current TMDB image base URL.
func GetTMDBImageBase() string {
	return tmdbImageBase
}

// tmdbAPIURL constructs a full TMDB API URL with the configured base.
func tmdbAPIURL(path string) string {
	return tmdbAPIBase + path
}

// tmdbImageURL constructs a full TMDB image URL with the configured base.
func tmdbImageURL(path string) string {
	return tmdbImageBase + path
}

// httpGetJSONWithRetry performs an HTTP GET with retry logic for transient failures.
// Retries on timeout, network errors, and HTTP 429 (rate limit).
// Non-retryable errors (401, 404) fail immediately.
// Max 2 attempts with 2-4 second randomized backoff between retries.
func httpGetJSONWithRetry(u string, headers map[string]string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			// Randomized backoff: 2-4 seconds
			time.Sleep(time.Duration(2000+rand.Intn(2000)) * time.Millisecond)
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := onlineHTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			buf := make([]byte, 0, 64*1024)
			tmp := make([]byte, 32*1024)
			for {
				n, readErr := resp.Body.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[:n]...)
				}
				if readErr != nil {
					break
				}
			}
			return buf, nil
		}
		resp.Body.Close()
		lastErr = fmt.Errorf("http %d", resp.StatusCode)
		// Non-retryable errors: fail immediately
		if resp.StatusCode == 401 || resp.StatusCode == 404 {
			return nil, lastErr
		}
		// HTTP 429 (rate limit) or 5xx: retry
	}
	return nil, fmt.Errorf("tmdb request failed (retried): %w", lastErr)
}

// PersonCandidate is a searchable person hit for manual matching.
type PersonCandidate struct {
	Source      string `json:"source"`
	ExternalID  string `json:"external_id"`
	Name        string `json:"name"`
	EnglishName string `json:"english_name,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Birthday    string `json:"birthday,omitempty"`
	KnownFor    string `json:"known_for,omitempty"`
	Gender      int    `json:"gender,omitempty"`
}

// PersonProfile is full person metadata from a provider.
type PersonProfile struct {
	Name        string   `json:"name"`
	EnglishName string   `json:"english_name"`
	Gender      int      `json:"gender"`
	BirthDate   string   `json:"birth_date"`
	BirthPlace  string   `json:"birth_place"`
	Biography   string   `json:"biography"`
	AvatarURL   string   `json:"avatar_url"`
	Aliases     string   `json:"aliases"`
	Occupations []string `json:"occupations"`
	TMDBID      string   `json:"tmdb_id"`
	IMDBID      string   `json:"imdb_id"`
}

// CreditMember is one cast or crew entry from TMDB credits.
type CreditMember struct {
	TMDBPersonID  string
	Name          string
	ProfilePath   string
	Occupation    string
	CharacterName string
	RoleType      string
	SortOrder     int
}

// SearchPersonCandidates searches TMDB for persons.
func SearchPersonCandidates(query, language, apiKey string, limit int) ([]PersonCandidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	language = normalizeMatchLanguage(language)
	u := tmdbAPIURL("/3/search/person?api_key=") + url.QueryEscape(apiKey) +
		"&query=" + url.QueryEscape(query) + "&language=" + url.QueryEscape(language) +
		"&page=1&include_adult=false"
	body, err := httpGetJSONWithRetry(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			ProfilePath  string `json:"profile_path"`
			KnownForDept string `json:"known_for_department"`
			Gender       int    `json:"gender"`
			Birthday     string `json:"birthday"`
			KnownFor     []struct {
				Title string `json:"title"`
				Name  string `json:"name"`
			} `json:"known_for"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	imgBase := tmdbImageBase + "/t/p/w185"
	out := make([]PersonCandidate, 0, len(resp.Results))
	for i, x := range resp.Results {
		if i >= limit {
			break
		}
		known := ""
		if len(x.KnownFor) > 0 {
			known = x.KnownFor[0].Title
			if known == "" {
				known = x.KnownFor[0].Name
			}
		}
		out = append(out, PersonCandidate{
			Source:     "tmdb",
			ExternalID: strconv.FormatInt(x.ID, 10),
			Name:       x.Name,
			Profile:    pickImage(imgBase, x.ProfilePath),
			Birthday:   x.Birthday,
			KnownFor:   known,
			Gender:     x.Gender,
		})
	}
	return out, nil
}

// FetchPersonByTMDBID loads person details from TMDB.
func FetchPersonByTMDBID(externalID, language, apiKey string) (*PersonProfile, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return nil, fmt.Errorf("external_id required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	language = normalizeMatchLanguage(language)
	u := tmdbAPIURL("/3/person/") + url.PathEscape(externalID) +
		"?api_key=" + url.QueryEscape(apiKey) + "&language=" + url.QueryEscape(language)
	body, err := httpGetJSONWithRetry(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID                 int64    `json:"id"`
		Name               string   `json:"name"`
		OriginalName       string   `json:"original_name"`
		Gender             int      `json:"gender"`
		Birthday           string   `json:"birthday"`
		PlaceOfBirth       string   `json:"place_of_birth"`
		Biography          string   `json:"biography"`
		ProfilePath        string   `json:"profile_path"`
		AlsoKnownAs        []string `json:"also_known_as"`
		KnownForDepartment string   `json:"known_for_department"`
		IMDBID             string   `json:"imdb_id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	occ := mapTMDBDepartment(resp.KnownForDepartment)
	aliases := strings.Join(resp.AlsoKnownAs, ", ")
	avatar := pickImage(tmdbImageBase+"/t/p/original", resp.ProfilePath)
	return &PersonProfile{
		Name:        resp.Name,
		EnglishName: resp.OriginalName,
		Gender:      resp.Gender,
		BirthDate:   resp.Birthday,
		BirthPlace:  resp.PlaceOfBirth,
		Biography:   resp.Biography,
		AvatarURL:   avatar,
		Aliases:     aliases,
		Occupations: occ,
		TMDBID:      strconv.FormatInt(resp.ID, 10),
		IMDBID:      resp.IMDBID,
	}, nil
}

func mapTMDBDepartment(dept string) []string {
	switch strings.TrimSpace(dept) {
	case "Acting":
		return []string{"actor"}
	case "Directing":
		return []string{"director"}
	case "Writing":
		return []string{"writer"}
	case "Production":
		return []string{"producer"}
	case "Camera":
		return []string{"cinematographer"}
	case "Editing":
		return []string{"editor"}
	case "Art":
		return []string{"art_director"}
	case "Sound":
		return []string{"composer"}
	case "Costume & Make-Up":
		return []string{"costume"}
	default:
		return []string{"other"}
	}
}

// FetchTMDBCredits loads cast and crew for a TMDB movie or TV id.
func FetchTMDBCredits(tmdbID, mediaType, language, apiKey string) ([]CreditMember, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return nil, fmt.Errorf("tmdb_id required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tmdb api key missing")
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "tv" {
		mediaType = "movie"
	}
	language = normalizeMatchLanguage(language)
	u := fmt.Sprintf("%s/3/%s/%s/credits?api_key=%s&language=%s",
		tmdbAPIBase, mediaType, url.PathEscape(tmdbID), url.QueryEscape(apiKey), url.QueryEscape(language))
	body, err := httpGetJSONWithRetry(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Cast []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Character   string `json:"character"`
			Order       int    `json:"order"`
			ProfilePath string `json:"profile_path"`
		} `json:"cast"`
		Crew []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Job         string `json:"job"`
			Department  string `json:"department"`
			ProfilePath string `json:"profile_path"`
		} `json:"crew"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := make([]CreditMember, 0, len(resp.Cast)+len(resp.Crew))
	for _, c := range resp.Cast {
		roleType := "supporting"
		if c.Order <= 2 {
			roleType = "leading"
		}
		out = append(out, CreditMember{
			TMDBPersonID:  strconv.FormatInt(c.ID, 10),
			Name:          c.Name,
			ProfilePath:   c.ProfilePath,
			Occupation:    "actor",
			CharacterName: c.Character,
			RoleType:      roleType,
			SortOrder:     c.Order,
		})
	}
	for _, c := range resp.Crew {
		occ := mapTMDBJob(c.Job, c.Department)
		if occ == "" {
			continue
		}
		out = append(out, CreditMember{
			TMDBPersonID: strconv.FormatInt(c.ID, 10),
			Name:         c.Name,
			ProfilePath:  c.ProfilePath,
			Occupation:   occ,
			SortOrder:    9999,
		})
	}
	return out, nil
}

func mapTMDBJob(job, department string) string {
	job = strings.ToLower(strings.TrimSpace(job))
	switch {
	case job == "director":
		return "director"
	case strings.Contains(job, "writer"), strings.Contains(job, "screenplay"), strings.Contains(job, "story"):
		return "writer"
	case strings.Contains(job, "producer"):
		return "producer"
	case strings.Contains(job, "director of photography"), job == "cinematography":
		return "cinematographer"
	case strings.Contains(job, "editor"):
		return "editor"
	case strings.Contains(job, "production design"), strings.Contains(job, "art direction"):
		return "art_director"
	case strings.Contains(job, "composer"), strings.Contains(job, "music"):
		return "composer"
	case strings.Contains(job, "costume"):
		return "costume"
	}
	switch strings.TrimSpace(department) {
	case "Directing":
		return "director"
	case "Writing":
		return "writer"
	case "Production":
		return "producer"
	case "Camera":
		return "cinematographer"
	case "Editing":
		return "editor"
	case "Art":
		return "art_director"
	case "Sound":
		return "composer"
	case "Costume & Make-Up":
		return "costume"
	}
	return "other"
}
