package photogeocode

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"knox-media/internal/photoparse"
)

const nominatimURL = "https://nominatim.openstreetmap.org/reverse"
const bigDataCloudURL = "https://api.bigdatacloud.net/data/reverse-geocode-client"

// Service resolves GPS coordinates to human-readable place names with DB cache.
type Service struct {
	DB         *sql.DB
	HTTPClient *http.Client
	UserAgent  string
	Language   string
	Enabled    bool

	mu       sync.Mutex
	lastCall time.Time
}

func New(db *sql.DB) *Service {
	return &Service{
		DB:         db,
		HTTPClient: &http.Client{Timeout: 12 * time.Second},
		UserAgent:  "KnoxMedia/1.0",
		Language:   "zh-CN",
		Enabled:    true,
	}
}

func (s *Service) EnsureSchema() error {
	_, err := s.DB.Exec(`
		CREATE TABLE IF NOT EXISTS photo_geocode_cache (
			cache_key TEXT PRIMARY KEY,
			place_id TEXT NOT NULL,
			location_name TEXT NOT NULL,
			city TEXT,
			province TEXT,
			country TEXT,
			raw_json TEXT,
			updated_at TEXT NOT NULL
		)`)
	return err
}

// EnrichMeta fills location fields when latitude/longitude are present.
func (s *Service) EnrichMeta(meta *photoparse.PhotoMeta) {
	if meta == nil || !meta.HasGPS {
		return
	}
	if meta.PlaceID != "" && meta.LocationName != "" &&
		!looksLikeCoordLabel(meta.LocationName) && !isCoordPlaceID(meta.PlaceID) {
		return
	}
	if !s.Enabled {
		meta.PlaceID = coordPlaceID(meta.Latitude, meta.Longitude)
		meta.LocationName = formatCoords(meta.Latitude, meta.Longitude)
		return
	}
	if s.DB != nil {
		_ = s.EnsureSchema()
	}
	place, err := s.Resolve(meta.Latitude, meta.Longitude)
	if err != nil {
		log.Printf("photogeocode: %.5f,%.5f: %v", meta.Latitude, meta.Longitude, err)
		meta.PlaceID = coordPlaceID(meta.Latitude, meta.Longitude)
		meta.LocationName = formatCoords(meta.Latitude, meta.Longitude)
		return
	}
	meta.PlaceID = place.PlaceID
	meta.LocationName = place.LocationName
	meta.LocationCity = place.City
	meta.LocationProvince = place.Province
	meta.LocationCountry = place.Country
}

// Place holds normalized reverse-geocode result.
type Place struct {
	PlaceID      string
	LocationName string
	City         string
	Province     string
	Country      string
}

func (s *Service) Resolve(lat, lon float64) (Place, error) {
	key := cacheKey(lat, lon)
	if s.DB != nil {
		if p, ok := s.loadCache(key); ok && !looksLikeCoordLabel(p.LocationName) {
			return p, nil
		}
	}
	if p, raw, err := s.reverseBigDataCloud(lat, lon); err == nil {
		if s.DB != nil {
			if err := s.saveCache(key, p, raw); err != nil {
				log.Printf("photogeocode cache save: %v", err)
			}
		}
		return p, nil
	} else {
		log.Printf("photogeocode bigdatacloud %.5f,%.5f: %v", lat, lon, err)
	}
	p, raw, err := s.reverseNominatim(lat, lon)
	if err != nil {
		return Place{}, err
	}
	if s.DB != nil {
		if err := s.saveCache(key, p, raw); err != nil {
			log.Printf("photogeocode cache save: %v", err)
		}
	}
	return p, nil
}

func cacheKey(lat, lon float64) string {
	return fmt.Sprintf("%.3f,%.3f", lat, lon)
}

func coordPlaceID(lat, lon float64) string {
	return fmt.Sprintf("place:%.3f,%.3f", lat, lon)
}

func isCoordPlaceID(id string) bool {
	return strings.HasPrefix(id, "place:") && strings.Contains(id, ",")
}

func formatCoords(lat, lon float64) string {
	return fmt.Sprintf("%.4f°, %.4f°", lat, lon)
}

func (s *Service) loadCache(key string) (Place, bool) {
	var placeID, name string
	var city, province, country sql.NullString
	err := s.DB.QueryRow(`
		SELECT place_id, location_name, city, province, country
		FROM photo_geocode_cache WHERE cache_key = ?`, key).Scan(&placeID, &name, &city, &province, &country)
	if err != nil {
		return Place{}, false
	}
	return Place{
		PlaceID:      placeID,
		LocationName: name,
		City:         city.String,
		Province:     province.String,
		Country:      country.String,
	}, true
}

func (s *Service) saveCache(key string, p Place, raw string) error {
	_, err := s.DB.Exec(`
		INSERT INTO photo_geocode_cache (cache_key, place_id, location_name, city, province, country, raw_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			place_id = excluded.place_id,
			location_name = excluded.location_name,
			city = excluded.city,
			province = excluded.province,
			country = excluded.country,
			raw_json = excluded.raw_json,
			updated_at = excluded.updated_at`,
		key, p.PlaceID, p.LocationName, nullStr(p.City), nullStr(p.Province), nullStr(p.Country), raw, time.Now().UTC().Format(time.RFC3339))
	return err
}

func nullStr(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

type nominatimResp struct {
	DisplayName string            `json:"display_name"`
	Address     map[string]string `json:"address"`
}

type bigDataCloudResp struct {
	CountryCode            string `json:"countryCode"`
	CountryName            string `json:"countryName"`
	PrincipalSubdivision   string `json:"principalSubdivision"`
	PrincipalSubdivisionCode string `json:"principalSubdivisionCode"`
	City                   string `json:"city"`
	Locality               string `json:"locality"`
	LocalityInfo           struct {
		Administrative []struct {
			Name       string `json:"name"`
			AdminLevel int    `json:"adminLevel"`
		} `json:"administrative"`
	} `json:"localityInfo"`
}

func (s *Service) reverseBigDataCloud(lat, lon float64) (Place, string, error) {
	s.mu.Lock()
	if wait := time.Second - time.Since(s.lastCall); wait > 0 {
		time.Sleep(wait)
	}
	s.lastCall = time.Now()
	s.mu.Unlock()

	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.6f", lat))
	q.Set("longitude", fmt.Sprintf("%.6f", lon))
	q.Set("localityLanguage", "zh")

	req, err := http.NewRequest(http.MethodGet, bigDataCloudURL+"?"+q.Encode(), nil)
	if err != nil {
		return Place{}, "", err
	}
	req.Header.Set("User-Agent", s.UserAgent)

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return Place{}, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Place{}, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return Place{}, "", fmt.Errorf("bigdatacloud status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed bigDataCloudResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Place{}, "", err
	}
	p := placeFromBigDataCloud(parsed)
	if strings.TrimSpace(p.LocationName) == "" {
		return Place{}, "", fmt.Errorf("empty location name")
	}
	return p, string(body), nil
}

func placeFromBigDataCloud(r bigDataCloudResp) Place {
	country := simplifyCN(r.CountryName)
	province := simplifyCN(r.PrincipalSubdivision)
	city := simplifyCN(r.City)
	district := simplifyCN(r.Locality)
	if district == "" {
		district = adminDistrictName(r)
	}

	var name string
	if strings.EqualFold(r.CountryCode, "CN") || strings.Contains(country, "中国") {
		name = formatChinaPlaceName(province, city, district)
		if name == "" {
			name = province
		}
	} else {
		name = firstNonEmptyString(city, district, province, country)
		if name == "" {
			name = "未知地点"
		}
	}

	pid := buildPlaceID(name, province, country)
	return Place{
		PlaceID:      pid,
		LocationName: name,
		City:         city,
		Province:     province,
		Country:      country,
	}
}

func adminDistrictName(r bigDataCloudResp) string {
	for _, a := range r.LocalityInfo.Administrative {
		if a.AdminLevel == 6 {
			if v := simplifyCN(a.Name); v != "" {
				return v
			}
		}
	}
	return ""
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Service) reverseNominatim(lat, lon float64) (Place, string, error) {
	s.mu.Lock()
	if wait := time.Second - time.Since(s.lastCall); wait > 0 {
		time.Sleep(wait)
	}
	s.lastCall = time.Now()
	s.mu.Unlock()

	q := url.Values{}
	q.Set("lat", fmt.Sprintf("%.6f", lat))
	q.Set("lon", fmt.Sprintf("%.6f", lon))
	q.Set("format", "json")
	q.Set("accept-language", s.Language)
	q.Set("zoom", "10")

	req, err := http.NewRequest(http.MethodGet, nominatimURL+"?"+q.Encode(), nil)
	if err != nil {
		return Place{}, "", err
	}
	req.Header.Set("User-Agent", s.UserAgent)

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return Place{}, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Place{}, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return Place{}, "", fmt.Errorf("nominatim status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed nominatimResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Place{}, "", err
	}
	p := placeFromNominatim(parsed)
	return p, string(body), nil
}

func placeFromNominatim(r nominatimResp) Place {
	addr := r.Address
	country := firstNonEmpty(addr, "country")
	province := firstNonEmpty(addr, "state", "province", "region")
	district := firstNonEmpty(addr,
		"district", "city_district", "suburb", "borough", "neighbourhood", "quarter")
	city := firstNonEmpty(addr,
		"city", "town", "county", "state_district", "municipality", "village", "hamlet")

	var name string
	if strings.Contains(country, "中国") || strings.EqualFold(country, "China") {
		name = formatChinaPlaceName(province, city, district)
	} else {
		name = city
		if name == "" {
			name = district
		}
		if name == "" {
			name = province
		}
		if name == "" && r.DisplayName != "" {
			parts := strings.Split(r.DisplayName, ",")
			if len(parts) > 0 {
				name = strings.TrimSpace(parts[0])
			}
		}
		if name == "" {
			name = country
		}
	}
	if name == "" {
		name = "未知地点"
	}
	pid := buildPlaceID(name, province, country)
	return Place{
		PlaceID:      pid,
		LocationName: name,
		City:         city,
		Province:     province,
		Country:      country,
	}
}

func firstNonEmpty(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

func buildPlaceID(name, province, country string) string {
	slug := slugify(name)
	if province != "" && slugify(province) != slug {
		slug = slug + "_" + slugify(province)
	} else if country != "" && slugify(country) != slug {
		slug = slug + "_" + slugify(country)
	}
	if slug == "" {
		return "place:unknown"
	}
	return "place:" + slug
}

func slugify(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			b.WriteRune(r)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		case r == ' ' || r == '_' || r == '-':
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return out
}
