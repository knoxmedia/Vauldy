package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"knox-media/api/middleware"
	"knox-media/internal/auth"
)

type loginBody struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func nullString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func (h *Handler) Login(c *gin.Context) {
	var body loginBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var id int64
	var hash, role string
	var canManage int
	err := h.App.DB.QueryRow(`SELECT id, password, role, COALESCE(can_manage,0) FROM user WHERE username = ?`, body.Username).Scan(&id, &hash, &role, &canManage)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	effectiveRole := role
	if canManage == 1 {
		effectiveRole = "admin"
	}
	token, err := auth.SignToken(h.App.Config.Security.JWTSecret, h.App.Config.Security.TokenHours, id, body.Username, effectiveRole)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ua := strings.TrimSpace(c.GetHeader("User-Agent"))
	h.logActivity(id, body.Username, "login", nil, fmt.Sprintf("device login; ip=%s; ua=%s", c.ClientIP(), ua))
	c.JSON(http.StatusOK, gin.H{"token": token, "expires_in_hours": h.App.Config.Security.TokenHours})
}

func (h *Handler) Logout(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	username := middleware.Username(c)
	ua := strings.TrimSpace(c.GetHeader("User-Agent"))
	h.logActivity(uid, username, "logout", nil, fmt.Sprintf("device logout; ip=%s; ua=%s", c.ClientIP(), ua))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) UserInfo(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusOK, gin.H{
			"id":             0,
			"username":       middleware.Username(c),
			"role":           "api_client",
			"can_play":       true,
			"can_download":   true,
		})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var username, role string
	var canManage, canPlay, canDownload int
	var avatarURL, uiLocale, prefsJSON sql.NullString
	if err := h.App.DB.QueryRow(`
		SELECT username, role, COALESCE(can_manage,0), COALESCE(can_play,1), COALESCE(can_download,0),
		       COALESCE(avatar_url,''), COALESCE(ui_locale,'zh'), COALESCE(player_prefs_json,'')
		FROM user WHERE id = ?`, uid).Scan(&username, &role, &canManage, &canPlay, &canDownload, &avatarURL, &uiLocale, &prefsJSON); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if canManage == 1 {
		role = "admin"
	}
	playOK := canPlay == 1 || strings.EqualFold(role, "admin")
	downloadOK := canDownload == 1 || strings.EqualFold(role, "admin")
	prefs := decodePlayerPrefs(prefsJSON.String)
	c.JSON(http.StatusOK, gin.H{
		"id": uid, "username": username, "role": role,
		"can_play": playOK, "can_download": downloadOK,
		"avatar_url":    strings.TrimSpace(avatarURL.String),
		"ui_locale":     strings.TrimSpace(uiLocale.String),
		"player_prefs": prefs,
	})
}

func parseLibraryTypesQuery(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func libraryTypeAllowed(libType string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	libType = strings.ToLower(strings.TrimSpace(libType))
	for _, a := range allowed {
		if libType == a {
			return true
		}
	}
	return false
}

func (h *Handler) UserHistory(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	limit := 50
	if ls := c.Query("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	libraryTypesFilter := parseLibraryTypesQuery(c.Query("library_types"))
	profile, _ := h.loadUserPermissionProfile(uid)
	scanLimit := limit * 4
	if len(libraryTypesFilter) > 0 {
		scanLimit = limit * 10
	}
	if scanLimit > 500 {
		scanLimit = 500
	}
	q := `
		SELECT p.file_id, p.position, p.update_at, m.id, m.title, m.file_path, m.duration, m.library_id,
		       COALESCE(p.play_start_at,''), COALESCE(p.play_end_at,''), COALESCE(p.completed,0), COALESCE(p.play_count,0),
		       COALESCE(l.type, '')
		FROM play_progress p
		LEFT JOIN media m ON m.file_id = p.file_id
		LEFT JOIN library l ON l.id = m.library_id
		WHERE p.user_id = ?
		ORDER BY p.update_at DESC
		LIMIT ` + strconv.Itoa(scanLimit)
	rows, err := h.App.DB.Query(q, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	seenMedia := make(map[int64]struct{})
	seenFile := make(map[string]struct{})
	for rows.Next() {
		var fid, upd sql.NullString
		var pos sql.NullInt64
		var mid sql.NullInt64
		var title, fpath sql.NullString
		var dur sql.NullInt64
		var libID sql.NullInt64
		var playStartAt, playEndAt sql.NullString
		var completed, playCount sql.NullInt64
		var libType sql.NullString
		if err := rows.Scan(&fid, &pos, &upd, &mid, &title, &fpath, &dur, &libID, &playStartAt, &playEndAt, &completed, &playCount, &libType); err != nil {
			continue
		}
		if len(libraryTypesFilter) > 0 && !libraryTypeAllowed(libType.String, libraryTypesFilter) {
			continue
		}
		if mid.Valid && mid.Int64 > 0 && strings.EqualFold(profile.LibraryScope, "selected") {
			if _, ok := profile.AllowedLibraryIDs[libID.Int64]; !ok {
				continue
			}
			if folders := profile.AllowedLibraryFolders[libID.Int64]; len(folders) > 0 && !pathMatchesAnyFolder(fpath.String, folders) {
				continue
			}
		}
		if mid.Valid && mid.Int64 > 0 {
			if _, dup := seenMedia[mid.Int64]; dup {
				continue
			}
			seenMedia[mid.Int64] = struct{}{}
		} else if fid.Valid && fid.String != "" {
			if _, dup := seenFile[fid.String]; dup {
				continue
			}
			seenFile[fid.String] = struct{}{}
		}
		if len(items) >= limit {
			continue
		}
		items = append(items, gin.H{
			"file_id": fid.String, "position": pos.Int64, "update_at": upd.String,
			"media_id": mid.Int64, "title": title.String, "file_path": fpath.String,
			"duration": dur.Int64,
			"play_start_at": nullString(playStartAt),
			"play_end_at":   nullString(playEndAt),
			"completed":     completed.Int64,
			"play_count":    playCount.Int64,
			"library_type":  strings.TrimSpace(libType.String),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
