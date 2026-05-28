package handler

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"knox-media/api/middleware"
)

// PlayerPrefs is stored as JSON on user.player_prefs_json and echoed in /user/info.
type SubtitleAppearance struct {
	TextSize   string `json:"text_size"`
	TextColor  string `json:"text_color"`
	Shadow     string `json:"shadow"`
	BgColor    string `json:"bg_color"`
	BgOpacity  int    `json:"bg_opacity"`
	PosBottom  int    `json:"pos_bottom"`
	PosTop     int    `json:"pos_top"`
}

type PlayerPrefs struct {
	AutoSelect            bool                 `json:"auto_select"`
	PreferredAudioLang    string               `json:"preferred_audio_lang"`
	PreferredSubtitleLang string               `json:"preferred_subtitle_lang"`
	SubtitleMode          string               `json:"subtitle_mode"`
	SDHSearch             string               `json:"sdh_search"`
	ForcedSearch          string               `json:"forced_search"`
	SubtitleAppearance    SubtitleAppearance   `json:"subtitle_appearance"`
}

func defaultSubtitleAppearance() SubtitleAppearance {
	return SubtitleAppearance{
		TextSize:   "normal",
		TextColor:  "white",
		Shadow:     "shadow",
		BgColor:    "blue",
		BgOpacity:  100,
		PosBottom:  5,
		PosTop:     5,
	}
}

func mergeSubtitleAppearance(in SubtitleAppearance) SubtitleAppearance {
	if strings.TrimSpace(in.TextSize) == "" {
		return defaultSubtitleAppearance()
	}
	d := defaultSubtitleAppearance()
	out := in
	if strings.TrimSpace(out.TextColor) == "" {
		out.TextColor = d.TextColor
	}
	if strings.TrimSpace(out.Shadow) == "" {
		out.Shadow = d.Shadow
	}
	if strings.TrimSpace(out.BgColor) == "" {
		out.BgColor = d.BgColor
	}
	if out.BgOpacity < 0 {
		out.BgOpacity = 0
	}
	if out.BgOpacity > 100 {
		out.BgOpacity = 100
	}
	if out.PosBottom < 0 || out.PosBottom > 30 {
		out.PosBottom = d.PosBottom
	}
	if out.PosTop < 0 || out.PosTop > 30 {
		out.PosTop = d.PosTop
	}
	return out
}

func defaultPlayerPrefs() PlayerPrefs {
	return PlayerPrefs{
		AutoSelect:            true,
		PreferredAudioLang:    "",
		PreferredSubtitleLang: "",
		SubtitleMode:          "foreign",
		SDHSearch:             "prefer_non_sdh",
		ForcedSearch:          "prefer_non_forced",
		SubtitleAppearance:    defaultSubtitleAppearance(),
	}
}

func decodePlayerPrefs(raw string) PlayerPrefs {
	out := defaultPlayerPrefs()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var p PlayerPrefs
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return out
	}
	if p.SubtitleMode == "" {
		p.SubtitleMode = out.SubtitleMode
	}
	if p.SDHSearch == "" {
		p.SDHSearch = out.SDHSearch
	}
	if p.ForcedSearch == "" {
		p.ForcedSearch = out.ForcedSearch
	}
	p.SubtitleAppearance = mergeSubtitleAppearance(p.SubtitleAppearance)
	return p
}

// localeToTrackLang derives a BCP-47 primary language tag from a UI locale code.
// It accepts both legacy short codes (zh, en) and full BCP-47 tags
// (zh-CN, zh-TW, zh-Hans, en-US, ...).
func localeToTrackLang(locale string) string {
	s := strings.TrimSpace(strings.ToLower(locale))
	if s == "" {
		return "zh"
	}
	switch s {
	case "中文":
		return "zh"
	case "english":
		return "en"
	case "日本語":
		return "ja"
	case "한국어":
		return "ko"
	}
	// Take the primary subtag (zh-CN -> zh, en-US -> en).
	primary := s
	if i := strings.IndexAny(s, "-_"); i > 0 {
		primary = s[:i]
	}
	switch primary {
	case "zh", "cmn", "yue":
		return "zh"
	case "en", "ja", "jp", "ko", "fr", "de", "es", "ru", "pt", "it":
		if primary == "jp" {
			return "ja"
		}
		return primary
	default:
		return "zh"
	}
}

func (h *Handler) loadUserPrefsRow(uid int64) (uiLocale string, prefsJSON string, err error) {
	var loc sql.NullString
	var pj sql.NullString
	err = h.App.DB.QueryRow(`SELECT COALESCE(ui_locale,''), COALESCE(player_prefs_json,'') FROM user WHERE id = ?`, uid).Scan(&loc, &pj)
	if err != nil {
		return "", "", err
	}
	return loc.String, pj.String, nil
}

func (h *Handler) saveUserPrefs(uid int64, uiLocale string, prefs PlayerPrefs) error {
	b, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	_, err = h.App.DB.Exec(`UPDATE user SET ui_locale = ?, player_prefs_json = ? WHERE id = ?`, uiLocale, string(b), uid)
	return err
}

// UpdateUserProfile updates ui_locale and/or player preferences for the current user.
func (h *Handler) UpdateUserProfile(c *gin.Context) {
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
		UILocale    *string          `json:"ui_locale"`
		PlayerPrefs *json.RawMessage `json:"player_prefs"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	curLocale, curJSON, err := h.loadUserPrefsRow(uid)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	prefs := decodePlayerPrefs(curJSON)
	newLocale := curLocale

	if body.PlayerPrefs != nil && len(*body.PlayerPrefs) > 0 {
		var incoming PlayerPrefs
		if err := json.Unmarshal(*body.PlayerPrefs, &incoming); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid player_prefs"})
			return
		}
		prefs = incoming
	}
	if body.UILocale != nil {
		s := strings.TrimSpace(*body.UILocale)
		if s != "" {
			newLocale = s
			lang := localeToTrackLang(s)
			prefs.PreferredAudioLang = lang
			prefs.PreferredSubtitleLang = lang
		}
	}
	if body.UILocale == nil && (body.PlayerPrefs == nil || len(*body.PlayerPrefs) == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to update"})
		return
	}
	if err := h.saveUserPrefs(uid, newLocale, prefs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ui_locale": newLocale, "player_prefs": prefs})
}

type changePasswordBody struct {
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

// ChangeUserPassword sets a new password for the current user (no old password gate in this deployment).
func (h *Handler) ChangeUserPassword(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var body changePasswordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(strings.TrimSpace(body.NewPassword)) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 6 characters"})
		return
	}
	if body.NewPassword != body.ConfirmPassword {
		c.JSON(http.StatusBadRequest, gin.H{"error": "passwords do not match"})
		return
	}
	hh, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(body.NewPassword)), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(`UPDATE user SET password = ? WHERE id = ?`, string(hh), uid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

const maxAvatarBytes = 4 << 20 // 4 MiB

// UploadUserAvatar accepts a cropped avatar image (POST multipart field "file").
func (h *Handler) UploadUserAvatar(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	if fh.Size > maxAvatarBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large"})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read file"})
		return
	}
	defer src.Close()
	lim := io.LimitReader(src, maxAvatarBytes+1)
	data, err := io.ReadAll(lim)
	if err != nil || len(data) > maxAvatarBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large"})
		return
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fh.Filename)))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" {
		ext = ".png"
	}
	filename := "user-" + strconv.FormatInt(uid, 10) + ext
	destDir := filepath.Join(h.Upload.UploadDir, "avatars")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create directory"})
		return
	}
	dest := filepath.Join(destDir, filename)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	url := "/uploads/avatars/" + filename
	if _, err := h.App.DB.Exec(`UPDATE user SET avatar_url = ? WHERE id = ?`, url, uid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "url": url})
}

// DeleteUserAvatar clears the avatar URL for the current user (file may remain on disk).
func (h *Handler) DeleteUserAvatar(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if _, err := h.App.DB.Exec(`UPDATE user SET avatar_url = '' WHERE id = ?`, uid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
