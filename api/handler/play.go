package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/mediautil"
)

func (h *Handler) PlayMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	if strings.TrimSpace(c.Query("download")) == "1" && !middleware.IsAPIClient(c) {
		uid := middleware.UserID(c)
		if uid <= 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		profile, err := h.loadUserPermissionProfile(uid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !profile.CanDownload {
			c.JSON(http.StatusForbidden, gin.H{"error": "download denied"})
			return
		}
	}
	var p string
	var title sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, title FROM media WHERE id = ?`, id).Scan(&p, &title); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	preferSource := strings.TrimSpace(c.Query("prefer_source")) == "1"
	if ready, _, _ := h.latestEncryptedManifest(id); ready {
		if preferSource {
			goto serveSource
		}
		target := "/api/v1/media/" + c.Param("id") + "/hls/master.m3u8"
		if q := strings.TrimSpace(c.Request.URL.RawQuery); q != "" {
			target += "?" + q
		}
		c.Redirect(http.StatusTemporaryRedirect, target)
		return
	}
	if ready, _ := h.latestTranscodeManifestByMediaID(id); ready {
		if preferSource {
			goto serveSource
		}
		target := "/api/v1/media/" + c.Param("id") + "/hls/master.m3u8"
		if q := strings.TrimSpace(c.Request.URL.RawQuery); q != "" {
			target += "?" + q
		}
		c.Redirect(http.StatusTemporaryRedirect, target)
		return
	}
serveSource:
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "file missing"})
		return
	}
	name := filepath.Base(p)
	if title.Valid && title.String != "" {
		name = title.String + filepath.Ext(p)
	}
	disposition := "inline"
	if strings.TrimSpace(c.Query("download")) == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(name, `"`, ``)+`"`)
	http.ServeFile(c.Writer, c.Request, p)
}

func (h *Handler) PlaybackStart(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	uid := middleware.UserID(c)
	isClient := middleware.IsAPIClient(c)
	if !isClient && uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	username := middleware.Username(c)
	var body playbackLogBody
	_ = c.ShouldBindJSON(&body)
	var fileID string
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, id).Scan(&fileID); err == nil && strings.TrimSpace(fileID) != "" {
		if !isClient && uid > 0 {
			_ = h.touchPlayProgressOnStart(uid, fileID)
		}
	}
	pos := int64(0)
	if body.Position != nil && *body.Position > 0 {
		pos = *body.Position
	}
	completed := 0
	if body.Completed != nil && *body.Completed > 0 {
		completed = 1
	}
	ua := strings.TrimSpace(c.GetHeader("User-Agent"))
	logUID := uid
	if isClient {
		logUID = 0
	}
	h.logActivity(logUID, username, "playback_start", &id, fmt.Sprintf("playback start; pos=%d; completed=%d; ip=%s; ua=%s", pos, completed, c.ClientIP(), ua))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) PlaybackEnd(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	uid := middleware.UserID(c)
	isClient := middleware.IsAPIClient(c)
	if !isClient && uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	username := middleware.Username(c)
	var body playbackLogBody
	_ = c.ShouldBindJSON(&body)
	var fileID string
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, id).Scan(&fileID); err == nil && strings.TrimSpace(fileID) != "" {
		if !isClient && uid > 0 {
			_ = h.touchPlayProgressOnEnd(uid, fileID)
		}
	}
	pos := int64(0)
	if body.Position != nil && *body.Position > 0 {
		pos = *body.Position
	}
	completed := 0
	if body.Completed != nil && *body.Completed > 0 {
		completed = 1
	}
	ua := strings.TrimSpace(c.GetHeader("User-Agent"))
	logUID := uid
	if isClient {
		logUID = 0
	}
	h.logActivity(logUID, username, "playback_end", &id, fmt.Sprintf("playback end; pos=%d; completed=%d; ip=%s; ua=%s", pos, completed, c.ClientIP(), ua))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) HLSInfo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	var fileID, filePath, metaJSON sql.NullString
	var srcHeight sql.NullInt64
	if err := h.App.DB.QueryRow(`SELECT file_id, file_path, meta_json, height FROM media WHERE id = ?`, id).Scan(&fileID, &filePath, &metaJSON, &srcHeight); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	base := ""
	if c.Request.TLS != nil {
		base = "https://" + c.Request.Host
	} else {
		base = "http://" + c.Request.Host
	}
	playURL := base + "/api/v1/media/" + c.Param("id") + "/play"
	accessToken := strings.TrimSpace(c.Query("access_token"))
	widevineURL := base + "/api/v1/drm/widevine/license"
	powerDRMKeyURL := base + "/api/v1/drm/powerdrm/key"
	fairplayCertURL := base + "/api/v1/drm/fairplay/cert"
	fairplayLicenseURL := base + "/api/v1/drm/fairplay/license"
	widevineServiceCertURL := ""
	if h != nil && h.App != nil && h.App.Config != nil {
		wv := h.App.Config.DRM.Widevine
		if wv.EmitServiceCertURL && strings.TrimSpace(wv.PrivateModuleURL) != "" {
			widevineServiceCertURL = base + "/api/v1/drm/widevine/service-cert"
		}
	}
	if accessToken != "" {
		widevineURL = appendQueryValue(widevineURL, "access_token", accessToken)
		powerDRMKeyURL = appendQueryValue(powerDRMKeyURL, "access_token", accessToken)
		fairplayCertURL = appendQueryValue(fairplayCertURL, "access_token", accessToken)
		fairplayLicenseURL = appendQueryValue(fairplayLicenseURL, "access_token", accessToken)
		if widevineServiceCertURL != "" {
			widevineServiceCertURL = appendQueryValue(widevineServiceCertURL, "access_token", accessToken)
		}
	}
	if encReady, encMaster, encType := h.latestEncryptedManifest(id); encReady {
		switch encType {
		case "hls_aes_128":
			c.JSON(http.StatusOK, gin.H{
				"mode":       "hls_aes_128",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     "done",
				"fallback":   playURL,
			})
			_ = encMaster
			return
		case "hls_powerdrm":
			c.JSON(http.StatusOK, gin.H{
				"mode":       "hls_powerdrm",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     "done",
				"drm": gin.H{
					"powerdrm_key_url": powerDRMKeyURL,
				},
				"fallback": playURL,
			})
			return
		}
		dashURL := ""
		if ok, _ := h.drmDashManifestByMediaID(id); ok {
			dashURL = fmt.Sprintf("%s/api/v1/media/%s/dash/manifest.mpd", base, c.Param("id"))
		}
		clearKeys, _ := h.clearkeyMapByMediaID(id)
		widevineTransport := "json_local"
		if h != nil && h.App != nil && h.App.Config != nil && strings.TrimSpace(h.App.Config.DRM.Widevine.PrivateModuleURL) != "" {
			widevineTransport = "raw"
		}
		drmPayload := gin.H{
			"widevine_license_url": widevineURL,
			"widevine_transport":   widevineTransport,
			"powerdrm_key_url":     powerDRMKeyURL,
			"fairplay_cert_url":    fairplayCertURL,
			"fairplay_license_url": fairplayLicenseURL,
			"dash_mpd_url":         dashURL,
			"clearkey_keys":        clearKeys,
		}
		if widevineServiceCertURL != "" {
			drmPayload["widevine_service_cert_url"] = widevineServiceCertURL
		}
		c.JSON(http.StatusOK, gin.H{
			"mode":       "hls_drm",
			"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
			"status":     "done",
			"drm":        drmPayload,
			"fallback":   playURL,
		})
		return
	}
	if hlsReady, _ := h.latestTranscodeManifestByMediaID(id); hlsReady {
		c.JSON(http.StatusOK, gin.H{
			"mode":       "hls",
			"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
			"status":     "done",
			"fallback":   playURL,
			"message":    "Use completed transcoded stream",
		})
		return
	}
	caps := readClientCaps(c)
	media := detectMediaProfile(metaJSON.String)
	canDirect := canDirectPlay(media, caps)
	if canDirect {
		c.JSON(http.StatusOK, gin.H{
			"mode":          "native",
			"playUrl":       playURL,
			"media_profile": media,
			"client_caps":   caps,
			"message":       "Client can decode source directly",
		})
		return
	}

	if h.Instant != nil && fileID.Valid && strings.TrimSpace(fileID.String) != "" && filePath.Valid && strings.TrimSpace(filePath.String) != "" {
		sessionID := c.GetHeader("X-Session-ID")
		if strings.TrimSpace(sessionID) == "" {
			sessionID = c.ClientIP() + "-" + c.Request.UserAgent()
		}
		if err := h.Instant.PrepareVideoMeta(fileID.String, filePath.String, media.Container, media.Video, media.Audio); err == nil {
			go func(fid, sid string) {
				_ = h.Instant.TriggerSlicing(fid, sid)
			}(fileID.String, sessionID)
			jitPauseURL := base + "/api/v1/jit/session/pause"
			jitResumeURL := base + "/api/v1/jit/session/resume"
			if accessToken != "" {
				jitPauseURL = appendQueryValue(jitPauseURL, "access_token", accessToken)
				jitResumeURL = appendQueryValue(jitResumeURL, "access_token", accessToken)
			}
			c.JSON(http.StatusOK, gin.H{
				"mode":                   "jit_hls",
				"hls_master":             fmt.Sprintf("%s/api/v1/jit/master/%s", base, fileID.String),
				"status":                 "processing",
				"fallback":               playURL,
				"media_profile":          media,
				"client_caps":            caps,
				"jit_session_pause_url":  jitPauseURL,
				"jit_session_resume_url": jitResumeURL,
				"message":                "Source codec/container unsupported, switched to instant transcoding pipeline",
			})
			return
		}
	}

	playlist, status, taskID, terr := h.Worker.EnsureHLS(c.Request.Context(), fileID.String, filePath.String, int(srcHeight.Int64), caps.MaxHeight, caps.Qualities)
	if terr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": terr.Error()})
		return
	}
	playlistURL := ""
	if playlist != "" {
		playlistURL = fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id"))
	}
	c.JSON(http.StatusOK, gin.H{
		"mode":          "hls",
		"hls_master":    playlistURL,
		"status":        status,
		"task_id":       taskID,
		"fallback":      playURL,
		"media_profile": media,
		"client_caps":   caps,
		"message":       "Source codec/container unsupported for direct playback, switching to adaptive HLS",
	})
}

func (h *Handler) latestDRMManifest(mediaID int64) (bool, string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return false, ""
	}
	var out sql.NullString
	var status sql.NullString
	err := h.App.DB.QueryRow(`
		SELECT output_path, status
		FROM package_task
		WHERE media_id = ? AND pipeline_type = 'cmaf_drm'
		ORDER BY id DESC
		LIMIT 1
	`, mediaID).Scan(&out, &status)
	if err != nil {
		return false, ""
	}
	if status.String != "done" || !out.Valid || strings.TrimSpace(out.String) == "" {
		return false, ""
	}
	return true, out.String
}

func (h *Handler) latestEncryptedManifest(mediaID int64) (bool, string, string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return false, "", ""
	}
	var out sql.NullString
	var status sql.NullString
	var pipeline sql.NullString
	err := h.App.DB.QueryRow(`
		SELECT output_path, status, pipeline_type
		FROM package_task
		WHERE media_id = ? AND pipeline_type IN ('cmaf_drm','hls_aes_128','hls_powerdrm')
		ORDER BY id DESC
		LIMIT 1
	`, mediaID).Scan(&out, &status, &pipeline)
	if err != nil {
		return false, "", ""
	}
	if status.String != "done" || !out.Valid || strings.TrimSpace(out.String) == "" {
		return false, "", ""
	}
	return true, out.String, strings.TrimSpace(pipeline.String)
}

func (h *Handler) HLSMaster(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	playlist := h.hlsMasterPath(c)
	if playlist == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "hls output not ready"})
		return
	}
	noAudio := strings.TrimSpace(c.Query("no_audio")) == "1"
	log.Printf("hls master request: media_id=%s no_audio=%v uri=%s", c.Param("id"), noAudio, c.Request.URL.String())
	c.Header("X-Knox-No-Audio-Applied", "0")
	if noAudio {
		body, err := os.ReadFile(playlist)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "hls output not ready"})
			return
		}
		out := stripAudioGroupFromMasterM3U8(string(body))
		if !strings.Contains(out, "#KNOX-NO-AUDIO-MASTER") {
			out = strings.Replace(out, "#EXTM3U", "#EXTM3U\n#KNOX-NO-AUDIO-MASTER", 1)
		}
		if token := strings.TrimSpace(c.Query("access_token")); token != "" {
			out = injectAccessTokenIntoM3U8(out, token)
		}
		c.Header("X-Knox-No-Audio-Applied", "1")
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = c.Writer.Write([]byte(out))
		return
	}
	if token := strings.TrimSpace(c.Query("access_token")); token != "" {
		body, err := os.ReadFile(playlist)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "hls output not ready"})
			return
		}
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = c.Writer.Write([]byte(injectAccessTokenIntoM3U8(string(body), token)))
		return
	}
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	http.ServeFile(c.Writer, c.Request, playlist)
}

func (h *Handler) HLSAsset(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	playlist := h.hlsMasterPath(c)
	if playlist == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "hls output not ready"})
		return
	}
	baseDir := filepath.Dir(playlist)
	asset := strings.TrimPrefix(c.Param("asset"), "/")
	asset = filepath.Clean(asset)
	if strings.EqualFold(asset, "master.m3u8") {
		h.HLSMaster(c)
		return
	}
	if strings.HasPrefix(asset, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid asset path"})
		return
	}
	full := filepath.Join(baseDir, asset)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		if strings.HasSuffix(strings.ToLower(asset), "_init.mp4") {
			// Backward compatibility: older tasks may have init file emitted into
			// server working directory instead of playlist directory.
			alt := filepath.Join(".", filepath.Base(asset))
			if st2, err2 := os.Stat(alt); err2 == nil && !st2.IsDir() {
				full = alt
			} else {
				c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
				return
			}
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
			return
		}
	}
	switch strings.ToLower(filepath.Ext(full)) {
	case ".m3u8":
		if token := strings.TrimSpace(c.Query("access_token")); token != "" {
			body, err := os.ReadFile(full)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
				return
			}
			c.Header("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = c.Writer.Write([]byte(injectAccessTokenIntoM3U8(string(body), token)))
			return
		}
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
	case ".ts":
		c.Header("Content-Type", "video/mp2t")
	}
	http.ServeFile(c.Writer, c.Request, full)
}

func (h *Handler) DashManifest(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	mpd := h.dashManifestPath(c)
	if mpd == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "dash output not ready"})
		return
	}
	c.Header("Content-Type", "application/dash+xml")
	http.ServeFile(c.Writer, c.Request, mpd)
}

func (h *Handler) DashAsset(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	mpd := h.dashManifestPath(c)
	if mpd == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "dash output not ready"})
		return
	}
	baseDir := filepath.Dir(mpd)
	asset := strings.TrimPrefix(c.Param("asset"), "/")
	asset = filepath.Clean(asset)
	if strings.EqualFold(asset, "manifest.mpd") {
		h.DashManifest(c)
		return
	}
	if strings.HasPrefix(asset, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid asset path"})
		return
	}
	full := filepath.Join(baseDir, asset)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "asset not found"})
		return
	}
	ext := strings.ToLower(filepath.Ext(full))
	switch ext {
	case ".mpd":
		c.Header("Content-Type", "application/dash+xml")
	case ".m4s":
		c.Header("Content-Type", "video/iso.segment")
	case ".mp4":
		c.Header("Content-Type", "video/mp4")
	}
	http.ServeFile(c.Writer, c.Request, full)
}

var m3u8URIAttrPattern = regexp.MustCompile(`URI="([^"]+)"`)
var m3u8AudioAttrPattern = regexp.MustCompile(`,?AUDIO="[^"]*"`)
var m3u8CodecsAttrPattern = regexp.MustCompile(`CODECS="([^"]+)"`)

func injectAccessTokenIntoM3U8(content string, token string) string {
	if strings.TrimSpace(content) == "" || strings.TrimSpace(token) == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = m3u8URIAttrPattern.ReplaceAllStringFunc(line, func(m string) string {
				sub := m3u8URIAttrPattern.FindStringSubmatch(m)
				if len(sub) != 2 {
					return m
				}
				return `URI="` + appendQueryValue(sub[1], "access_token", token) + `"`
			})
			continue
		}
		lines[i] = appendQueryValue(line, "access_token", token)
	}
	return strings.Join(lines, "\n")
}

func appendQueryValue(raw string, key string, value string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	lower := strings.ToLower(raw)
	// Do not append access token to non-fetch DRM URIs.
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "skd:") || strings.HasPrefix(lower, "urn:") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + key + "=" + url.QueryEscape(value)
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return raw
	}
	q := u.Query()
	if q.Get(key) == "" {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func stripAudioGroupFromMasterM3U8(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#EXT-X-MEDIA:") && strings.Contains(t, "TYPE=AUDIO") {
			continue
		}
		if strings.HasPrefix(t, "#EXT-X-STREAM-INF:") && strings.Contains(ln, `AUDIO="`) {
			ln = m3u8AudioAttrPattern.ReplaceAllString(ln, "")
			ln = strings.ReplaceAll(ln, ",,", ",")
		}
		if strings.HasPrefix(t, "#EXT-X-STREAM-INF:") && strings.Contains(ln, `CODECS="`) {
			ln = m3u8CodecsAttrPattern.ReplaceAllStringFunc(ln, func(m string) string {
				sub := m3u8CodecsAttrPattern.FindStringSubmatch(m)
				if len(sub) != 2 {
					return m
				}
				parts := strings.Split(sub[1], ",")
				outParts := make([]string, 0, len(parts))
				for _, p := range parts {
					pt := strings.TrimSpace(p)
					if strings.HasPrefix(strings.ToLower(pt), "mp4a.") {
						continue
					}
					if pt != "" {
						outParts = append(outParts, pt)
					}
				}
				if len(outParts) == 0 {
					return ""
				}
				return `CODECS="` + strings.Join(outParts, ",") + `"`
			})
			ln = strings.ReplaceAll(ln, ",,", ",")
			ln = strings.TrimSuffix(ln, ",")
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func (h *Handler) hlsMasterPath(c *gin.Context) string {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	if ready, master, _ := h.latestEncryptedManifest(id); ready {
		if st, err := os.Stat(master); err == nil && !st.IsDir() {
			return master
		}
	}
	if ready, master := h.latestTranscodeManifestByMediaID(id); ready {
		if st, err := os.Stat(master); err == nil && !st.IsDir() {
			return master
		}
	}
	return ""
}

func (h *Handler) dashManifestPath(c *gin.Context) string {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	if ready, p := h.drmDashManifestByMediaID(id); ready {
		return p
	}
	return ""
}

func (h *Handler) drmDashManifestByMediaID(mediaID int64) (bool, string) {
	if ready, master := h.latestDRMManifest(mediaID); ready {
		mpd := filepath.Join(filepath.Dir(master), "manifest.mpd")
		if st, err := os.Stat(mpd); err == nil && !st.IsDir() {
			return true, mpd
		}
	}
	return false, ""
}

func (h *Handler) latestTranscodeManifestByMediaID(mediaID int64) (bool, string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return false, ""
	}
	var fileID sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, mediaID).Scan(&fileID); err != nil || !fileID.Valid || strings.TrimSpace(fileID.String) == "" {
		return false, ""
	}
	var out sql.NullString
	var status sql.NullString
	_ = h.App.DB.QueryRow(
		`SELECT output_path, status FROM transcode_task WHERE file_id = ? AND quality LIKE 'abr:%' ORDER BY id DESC LIMIT 1`,
		fileID.String,
	).Scan(&out, &status)
	if !out.Valid || status.String != "done" {
		return false, ""
	}
	return true, out.String
}

type clientCaps struct {
	VideoCodecs []string `json:"video_codecs"`
	AudioCodecs []string `json:"audio_codecs"`
	MaxHeight   int      `json:"max_height"`
	Qualities   []string `json:"qualities"`
}

type mediaProfile struct {
	Container string `json:"container"`
	Video     string `json:"video_codec"`
	Audio     string `json:"audio_codec"`
}

func readClientCaps(c *gin.Context) clientCaps {
	cap := clientCaps{
		VideoCodecs: parseCSV(c.Query("video_codecs")),
		AudioCodecs: parseCSV(c.Query("audio_codecs")),
		Qualities:   parseCSV(c.Query("qualities")),
		MaxHeight:   1080,
	}
	if cap.MaxHeight <= 0 {
		cap.MaxHeight = 1080
	}
	if mh, err := strconv.Atoi(c.Query("max_height")); err == nil && mh > 0 {
		cap.MaxHeight = mh
	}
	return cap
}

func detectMediaProfile(metaJSON string) mediaProfile {
	p := mediautil.CodecsFromMetaJSON(metaJSON)
	return mediaProfile{Container: p.Container, Video: p.Video, Audio: p.Audio}
}

func canDirectPlay(media mediaProfile, caps clientCaps) bool {
	if media.Video == "" {
		return false
	}
	if len(caps.VideoCodecs) == 0 {
		return fallbackDirectPlayHeuristic(media)
	}
	if !codecInSet(media.Video, caps.VideoCodecs) {
		return false
	}
	if media.Audio != "" && len(caps.AudioCodecs) > 0 && !codecInSet(media.Audio, caps.AudioCodecs) {
		return false
	}
	if media.Container != "" {
		if strings.Contains(media.Container, "matroska") || strings.Contains(media.Container, "flv") {
			return false
		}
	}
	return true
}

func fallbackDirectPlayHeuristic(media mediaProfile) bool {
	video := strings.ToLower(strings.TrimSpace(media.Video))
	audio := strings.ToLower(strings.TrimSpace(media.Audio))
	container := strings.ToLower(strings.TrimSpace(media.Container))
	if strings.Contains(container, "matroska") || strings.Contains(container, "flv") {
		return false
	}
	if !(video == "h264" || video == "avc1") {
		return false
	}
	if audio == "" || audio == "aac" || audio == "mp3" {
		return true
	}
	return false
}

func codecInSet(codec string, set []string) bool {
	if len(set) == 0 {
		return false
	}
	codec = strings.ToLower(codec)
	for _, it := range set {
		n := strings.ToLower(strings.TrimSpace(it))
		if n == codec {
			return true
		}
		if n == "h264" && (codec == "h264" || codec == "avc1") {
			return true
		}
		if (n == "h265" || n == "hevc") && (codec == "h265" || codec == "hevc") {
			return true
		}
	}
	return false
}

func parseCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (h *Handler) clearkeyMapByMediaID(mediaID int64) (map[string]string, error) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return nil, nil
	}
	var keyRef sql.NullString
	if err := h.App.DB.QueryRow(`SELECT COALESCE(key_ref,'') FROM drm_asset WHERE media_id = ? LIMIT 1`, mediaID).Scan(&keyRef); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(keyRef.String)
	if ref == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(ref)
	if err != nil {
		return nil, err
	}
	var payload struct {
		KID string `json:"kid"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	kid := strings.ToLower(strings.TrimSpace(payload.KID))
	key := strings.ToLower(strings.TrimSpace(payload.Key))
	if kid == "" || key == "" {
		return nil, nil
	}
	return map[string]string{kid: key}, nil
}

type progressBody struct {
	Position  int64 `json:"position" binding:"required"`
	Completed *int  `json:"completed"`
}

type playbackLogBody struct {
	Position  *int64 `json:"position"`
	Completed *int   `json:"completed"`
}

func (h *Handler) SaveProgress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	var body progressBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "API client credentials cannot sync user progress"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "login required for progress sync"})
		return
	}
	var fileID string
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, id).Scan(&fileID); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM play_progress WHERE user_id = ? AND file_id = ?`, uid, fileID).Scan(&n)
	completed := 0
	if body.Completed != nil && *body.Completed > 0 {
		completed = 1
	}
	var execErr error
	if n == 0 {
		_, execErr = h.App.DB.Exec(`INSERT INTO play_progress (user_id, file_id, position, completed) VALUES (?, ?, ?, ?)`, uid, fileID, body.Position, completed)
	} else {
		_, execErr = h.App.DB.Exec(`UPDATE play_progress SET position = ?, completed = ?, update_at = CURRENT_TIMESTAMP WHERE user_id = ? AND file_id = ?`, body.Position, completed, uid, fileID)
	}
	if execErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": execErr.Error()})
		return
	}
	var username sql.NullString
	_ = h.App.DB.QueryRow(`SELECT username FROM user WHERE id = ?`, uid).Scan(&username)
	h.logActivity(uid, username.String, "progress", &id, "save playback progress")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) touchPlayProgressOnStart(userID int64, fileID string) error {
	var n int
	if err := h.App.DB.QueryRow(`SELECT COUNT(1) FROM play_progress WHERE user_id = ? AND file_id = ?`, userID, fileID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := h.App.DB.Exec(`
			INSERT INTO play_progress (user_id, file_id, position, play_start_at, completed, play_count, update_at)
			VALUES (?, ?, 0, CURRENT_TIMESTAMP, 0, 1, CURRENT_TIMESTAMP)
		`, userID, fileID)
		return err
	}
	_, err := h.App.DB.Exec(`
		UPDATE play_progress
		SET play_start_at = CURRENT_TIMESTAMP, completed = 0, play_count = COALESCE(play_count,0) + 1, update_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND file_id = ?
	`, userID, fileID)
	return err
}

func (h *Handler) touchPlayProgressOnEnd(userID int64, fileID string) error {
	_, err := h.App.DB.Exec(`
		UPDATE play_progress
		SET play_end_at = CURRENT_TIMESTAMP, update_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND file_id = ?
	`, userID, fileID)
	return err
}

func (h *Handler) PreviewInfo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	var filePath sql.NullString
	var duration sql.NullInt64
	var enabled sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT m.file_path, m.duration, COALESCE(l.preview_extract,0)
		FROM media m LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ? LIMIT 1
	`, id).Scan(&filePath, &duration, &enabled); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if enabled.Int64 != 1 || h.PreviewWorker == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "status": "disabled"})
		return
	}
	info, err := h.PreviewWorker.Ensure(c.Request.Context(), id, filePath.String, duration.Int64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	base := "http://" + c.Request.Host
	if c.Request.TLS != nil {
		base = "https://" + c.Request.Host
	}
	token := strings.TrimSpace(c.Query("access_token"))
	qs := ""
	if token != "" {
		qs = "?access_token=" + token
	}
	spriteURL := ""
	vttURL := ""
	thumb := gin.H{}
	if info.Status == "ready" {
		spriteURL = fmt.Sprintf("%s/api/v1/media/%d/preview/sprite.jpg%s", base, id, qs)
		vttURL = fmt.Sprintf("%s/api/v1/media/%d/preview/thumbs.vtt%s", base, id, qs)
		thumb = gin.H{
			"urls":    []string{spriteURL},
			"pic_num": info.ThumbCount,
			"width":   info.Width,
			"height":  info.Height,
			"col":     10,
			"row":     10,
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":       true,
		"status":        info.Status,
		"interval_sec":  info.Interval,
		"thumb_count":   info.ThumbCount,
		"thumb_width":   info.Width,
		"thumb_height":  info.Height,
		"sprite_url":    spriteURL,
		"vtt_url":       vttURL,
		"thumbnail":     thumb,
		"error_message": info.Error,
	})
}

func (h *Handler) PreviewSprite(c *gin.Context) {
	h.servePreviewAsset(c, "sprite_path", "image/jpeg")
}

func (h *Handler) PreviewVTT(c *gin.Context) {
	h.servePreviewAsset(c, "vtt_path", "text/vtt")
}

func (h *Handler) servePreviewAsset(c *gin.Context, col string, contentType string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	q := `SELECT ` + col + ` FROM preview_task WHERE media_id = ? AND status = 'ready' LIMIT 1`
	var p sql.NullString
	if err := h.App.DB.QueryRow(q, id).Scan(&p); err != nil || !p.Valid || strings.TrimSpace(p.String) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview not ready"})
		return
	}
	fp := filepath.Clean(p.String)
	if st, err := os.Stat(fp); err != nil || st.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview file missing"})
		return
	}
	c.Header("Content-Type", contentType)
	http.ServeFile(c.Writer, c.Request, fp)
}
