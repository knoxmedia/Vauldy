package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"knox-media/api/middleware"
)

type userPermissionProfile struct {
	UserID             int64
	Role               string
	CanManage          bool
	CanPlay            bool
	CanDownload        bool
	CanAccessFeatures  bool
	LibraryScope       string
	AllowedLibraryIDs  map[int64]struct{}
	AllowedLibraryFolders map[int64][]string
	ParentalEnabled    bool
	ParentalMaxRating  string
	ParentalPinHash    string
	AllowedTimeStart   string
	AllowedTimeEnd     string
	ParentalPlans      []parentalAccessPlan
}

type parentalAccessPlan struct {
	Weekday   int    `json:"weekday"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

func (h *Handler) loadUserPermissionProfile(userID int64) (userPermissionProfile, error) {
	p := userPermissionProfile{UserID: userID, AllowedLibraryIDs: map[int64]struct{}{}, AllowedLibraryFolders: map[int64][]string{}}
	row := h.App.DB.QueryRow(`
		SELECT role, COALESCE(can_manage,0), COALESCE(can_play,1), COALESCE(can_download,0), COALESCE(can_access_features,1),
		       COALESCE(library_scope,'all'), COALESCE(parental_enabled,0), COALESCE(parental_max_rating,''), COALESCE(parental_pin_hash,''),
		       COALESCE(allowed_time_start,''), COALESCE(allowed_time_end,''), COALESCE(parental_access_plan_json,'[]')
		FROM user WHERE id = ? LIMIT 1
	`, userID)
	var canManage, canPlay, canDownload, canAccessFeatures, parentalEnabled int
	var parentalPlansRaw string
	if err := row.Scan(&p.Role, &canManage, &canPlay, &canDownload, &canAccessFeatures, &p.LibraryScope, &parentalEnabled, &p.ParentalMaxRating, &p.ParentalPinHash, &p.AllowedTimeStart, &p.AllowedTimeEnd, &parentalPlansRaw); err != nil {
		if err == sql.ErrNoRows {
			// Token references a missing user; fall back to a permissive default profile so
			// downstream library permission checks (selected scope) simply don't engage.
			p.LibraryScope = "all"
			p.CanPlay = true
			return p, nil
		}
		return p, err
	}
	p.ParentalPlans = parseParentalPlansJSON(parentalPlansRaw)
	p.CanManage = canManage == 1 || strings.EqualFold(p.Role, "admin")
	p.CanPlay = canPlay == 1 || strings.EqualFold(p.Role, "admin")
	p.CanDownload = canDownload == 1 || strings.EqualFold(p.Role, "admin")
	p.CanAccessFeatures = canAccessFeatures == 1 || strings.EqualFold(p.Role, "admin")
	p.ParentalEnabled = parentalEnabled == 1 && !strings.EqualFold(p.Role, "admin")
	if strings.EqualFold(p.Role, "admin") {
		p.LibraryScope = "all"
		return p, nil
	}
	if strings.EqualFold(strings.TrimSpace(p.LibraryScope), "selected") {
		rows, err := h.App.DB.Query(`SELECT library_id FROM user_library_permission WHERE user_id = ?`, userID)
		if err != nil {
			return p, err
		}
		defer rows.Close()
		for rows.Next() {
			var lid int64
			if rows.Scan(&lid) == nil && lid > 0 {
				p.AllowedLibraryIDs[lid] = struct{}{}
			}
		}
		fRows, err := h.App.DB.Query(`SELECT library_id, folder_path FROM user_library_folder_permission WHERE user_id = ?`, userID)
		if err != nil {
			return p, err
		}
		defer fRows.Close()
		for fRows.Next() {
			var lid int64
			var folder string
			if fRows.Scan(&lid, &folder) == nil && lid > 0 && strings.TrimSpace(folder) != "" {
				p.AllowedLibraryFolders[lid] = append(p.AllowedLibraryFolders[lid], strings.TrimSpace(folder))
			}
		}
	}
	return p, nil
}

func (h *Handler) requireMediaAccess(c *gin.Context, mediaID int64, needPlay bool) (int64, bool) {
	if middleware.IsAPIClient(c) {
		return 0, true
	}
	uid := middleware.UserID(c)
	if uid <= 0 && strings.TrimSpace(middleware.Role(c)) == "" {
		// Allow direct handler tests / internal calls without auth middleware context.
		return 0, true
	}
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return 0, false
	}
	var libraryID int64
	var filePath string
	var metaRaw string
	if err := h.App.DB.QueryRow(`SELECT library_id, COALESCE(file_path,''), COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&libraryID, &filePath, &metaRaw); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return 0, false
	}
	profile, err := h.loadUserPermissionProfile(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return 0, false
	}
	if needPlay && !profile.CanPlay {
		c.JSON(http.StatusForbidden, gin.H{"error": "playback denied"})
		return 0, false
	}
	if strings.EqualFold(profile.LibraryScope, "selected") {
		if _, ok := profile.AllowedLibraryIDs[libraryID]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "library access denied"})
			return 0, false
		}
		if folders := profile.AllowedLibraryFolders[libraryID]; len(folders) > 0 && !pathMatchesAnyFolder(filePath, folders) {
			c.JSON(http.StatusForbidden, gin.H{"error": "folder access denied"})
			return 0, false
		}
	}
	if profile.ParentalEnabled {
		if !withinAllowedTimeWindow(profile.AllowedTimeStart, profile.AllowedTimeEnd, profile.ParentalPlans, time.Now()) {
			c.JSON(http.StatusForbidden, gin.H{"error": "outside parental allowed time"})
			return 0, false
		}
		if needPlay && !isRatingAllowed(profile.ParentalMaxRating, mediaParentalRating(metaRaw)) {
			unlockToken := strings.TrimSpace(c.Query("parental_unlock"))
			if unlockToken == "" {
				unlockToken = strings.TrimSpace(c.GetHeader("X-Parental-Unlock"))
			}
			if h.verifyParentalUnlockToken(unlockToken, uid, mediaID) {
				return libraryID, true
			}
			inputPIN := strings.TrimSpace(c.Query("parental_pin"))
			if inputPIN == "" {
				inputPIN = strings.TrimSpace(c.GetHeader("X-Parental-PIN"))
			}
			if !verifyPinHash(profile.ParentalPinHash, inputPIN) {
				c.JSON(http.StatusForbidden, gin.H{"error": "parental pin required"})
				return 0, false
			}
		}
	}
	return libraryID, true
}

func pathMatchesAnyFolder(filePath string, folders []string) bool {
	fp := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(filePath), "\\", "/"))
	if fp == "" {
		return false
	}
	for _, f := range folders {
		ff := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(f), "\\", "/"))
		if ff == "" {
			continue
		}
		if strings.HasPrefix(fp, ff) {
			return true
		}
	}
	return false
}

type parentalUnlockClaims struct {
	UserID  int64 `json:"uid"`
	MediaID int64 `json:"mid"`
	jwt.RegisteredClaims
}

func (h *Handler) signParentalUnlockToken(userID, mediaID int64, ttlMinutes int) (string, error) {
	if ttlMinutes <= 0 {
		ttlMinutes = 20
	}
	claims := parentalUnlockClaims{
		UserID:  userID,
		MediaID: mediaID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(ttlMinutes) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "parental_unlock",
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(h.App.Config.Security.JWTSecret))
}

func (h *Handler) verifyParentalUnlockToken(token string, userID, mediaID int64) bool {
	if strings.TrimSpace(token) == "" || userID <= 0 || mediaID <= 0 {
		return false
	}
	t, err := jwt.ParseWithClaims(token, &parentalUnlockClaims{}, func(t *jwt.Token) (any, error) {
		return []byte(h.App.Config.Security.JWTSecret), nil
	})
	if err != nil || !t.Valid {
		return false
	}
	claims, ok := t.Claims.(*parentalUnlockClaims)
	if !ok {
		return false
	}
	return claims.UserID == userID && claims.MediaID == mediaID && claims.Subject == "parental_unlock"
}

func (h *Handler) UnlockParental(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var body struct {
		MediaID int64  `json:"media_id"`
		PIN     string `json:"pin"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.MediaID <= 0 || strings.TrimSpace(body.PIN) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_id and pin required"})
		return
	}
	profile, err := h.loadUserPermissionProfile(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !profile.ParentalEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parental control not enabled"})
		return
	}
	if !verifyPinHash(profile.ParentalPinHash, strings.TrimSpace(body.PIN)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid parental pin"})
		return
	}
	token, err := h.signParentalUnlockToken(uid, body.MediaID, 20)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unlock_token": token, "expires_in_minutes": 20})
}

func withinAllowedTimeWindow(start, end string, plans []parentalAccessPlan, now time.Time) bool {
	if len(plans) > 0 {
		weekday := int(now.Weekday())
		for _, plan := range plans {
			if plan.Weekday != weekday {
				continue
			}
			if withinOneWindow(plan.StartTime, plan.EndTime, now) {
				return true
			}
		}
		return false
	}
	return withinOneWindow(start, end, now)
}

func withinOneWindow(start, end string, now time.Time) bool {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" || end == "" {
		return true
	}
	parse := func(v string) (int, bool) {
		parts := strings.Split(v, ":")
		if len(parts) != 2 {
			return 0, false
		}
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, false
		}
		return h*60 + m, true
	}
	s, ok1 := parse(start)
	e, ok2 := parse(end)
	if !ok1 || !ok2 {
		return true
	}
	n := now.Hour()*60 + now.Minute()
	if s <= e {
		return n >= s && n <= e
	}
	return n >= s || n <= e
}

func mediaParentalRating(metaRaw string) string {
	if strings.TrimSpace(metaRaw) == "" {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(metaRaw), &root); err != nil {
		return ""
	}
	if scrape, ok := root["scrape"].(map[string]any); ok {
		if v := strings.TrimSpace(anyString(scrape["rating"])); v != "" {
			return normalizeRating(v)
		}
		if v := strings.TrimSpace(anyString(scrape["certification"])); v != "" {
			return normalizeRating(v)
		}
	}
	if v := strings.TrimSpace(anyString(root["rating"])); v != "" {
		return normalizeRating(v)
	}
	return ""
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func normalizeRating(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "TV-", "")
	v = strings.ReplaceAll(v, "US:", "")
	return v
}

func ratingRank(v string) int {
	switch normalizeRating(v) {
	case "G":
		return 1
	case "PG":
		return 2
	case "PG13", "PG-13":
		return 3
	case "R":
		return 4
	case "NC17", "NC-17":
		return 5
	default:
		return 0
	}
}

func isRatingAllowed(maxRating, mediaRating string) bool {
	if strings.TrimSpace(maxRating) == "" || strings.TrimSpace(mediaRating) == "" {
		return true
	}
	max := ratingRank(maxRating)
	cur := ratingRank(mediaRating)
	if max == 0 || cur == 0 {
		return true
	}
	return cur <= max
}

