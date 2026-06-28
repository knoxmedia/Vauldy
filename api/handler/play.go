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
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/mediautil"
	"knox-media/internal/playback"
	"knox-media/internal/storage"
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
	var fileType sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, title, file_type FROM media WHERE id = ?`, id).Scan(&p, &title, &fileType); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	preferSource := strings.TrimSpace(c.Query("prefer_source")) == "1"
	pol := h.loadStreamPolicy(id)
	if preferSource {
		goto serveSource
	}
	if fileType.String != "document" && !preferSource {
		if pol.DRMEnabled {
			if ready, _, _ := h.latestEncryptedManifest(id); ready {
				target := "/api/v1/media/" + c.Param("id") + "/hls/master.m3u8"
				if q := strings.TrimSpace(c.Request.URL.RawQuery); q != "" {
					target += "?" + q
				}
				c.Redirect(http.StatusTemporaryRedirect, target)
				return
			}
		} else {
			if ready, _, _ := h.latestEncryptedManifest(id); ready {
				target := "/api/v1/media/" + c.Param("id") + "/hls/master.m3u8"
				if q := strings.TrimSpace(c.Request.URL.RawQuery); q != "" {
					target += "?" + q
				}
				c.Redirect(http.StatusTemporaryRedirect, target)
				return
			}
			if ready, _, _, _ := h.latestTranscodeManifestByMediaID(id); ready {
				target := "/api/v1/media/" + c.Param("id") + "/hls/master.m3u8"
				if q := strings.TrimSpace(c.Request.URL.RawQuery); q != "" {
					target += "?" + q
				}
				c.Redirect(http.StatusTemporaryRedirect, target)
				return
			}
		}
	}
serveSource:
	p = filepath.Clean(p)
	name := playbackDownloadName(p, title)
	disposition := "inline"
	if strings.TrimSpace(c.Query("download")) == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", disposition+"; "+contentDispositionFilename(name))
	h.serveMediaSource(c, id, p, name)
}

func playbackDownloadName(filePath string, title sql.NullString) string {
	name := filepath.Base(filePath)
	if title.Valid {
		if t := sanitizeContentFilename(title.String); t != "" {
			ext := filepath.Ext(filePath)
			if ext == ".enc" {
				if plainExt := plainExtFromEncPath(filePath); plainExt != "" {
					ext = plainExt
				} else {
					ext = ""
				}
			}
			if ext != "" && strings.EqualFold(filepath.Ext(t), ext) {
				name = t
			} else if ext != "" {
				name = t + ext
			} else {
				name = t
			}
		}
	}
	return name
}

func plainExtFromEncPath(encPath string) string {
	base := filepath.Base(encPath)
	if !strings.HasSuffix(strings.ToLower(base), ".enc") {
		return ""
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if i := strings.LastIndex(stem, "."); i > 0 {
		return stem[i:]
	}
	return ""
}

// sanitizeContentFilename drops titles that are empty, invalid UTF-8, or contain
// replacement/control characters (common when metadata was decoded with the wrong charset).
func sanitizeContentFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || strings.ContainsRune(s, '\uFFFD') {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(cleaned)
}

// contentDispositionFilename builds a safe RFC 6266 filename parameter for HTTP headers.
func contentDispositionFilename(name string) string {
	const fallback = "download"
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if cleaned == "" {
		cleaned = fallback
	}
	asciiOnly := true
	for _, r := range cleaned {
		if r > 0x7e {
			asciiOnly = false
			break
		}
	}
	if asciiOnly {
		return `filename="` + strings.ReplaceAll(cleaned, `"`, ``) + `"`
	}
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7e || r < 0x20 {
			return '_'
		}
		return r
	}, cleaned)
	ascii = strings.Trim(ascii, "._- ")
	if ascii == "" {
		ascii = fallback
	}
	return fmt.Sprintf(`filename="%s"; filename*=UTF-8''%s`, strings.ReplaceAll(ascii, `"`, ``), url.PathEscape(cleaned))
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
	sid := ""
	if body.SessionID != nil {
		sid = strings.TrimSpace(*body.SessionID)
	}
	msg := fmt.Sprintf("playback start; pos=%d; completed=%d; ip=%s; ua=%s", pos, completed, c.ClientIP(), ua)
	if sid != "" {
		msg += "; session_id=" + sid
	}
	h.logActivity(logUID, username, "playback_start", &id, msg)
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

	// 通知 JIT 调度器结束会话以便立即停止转码进程，避免 35s TTL 自然过期带来的资源浪费
	if h.Instant != nil {
		sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID"))
		if sessionID == "" {
			sessionID = c.ClientIP() + "-" + c.Request.UserAgent()
		}
		h.Instant.EndSession(sessionID)
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
	sid := ""
	if body.SessionID != nil {
		sid = strings.TrimSpace(*body.SessionID)
	}
	msg := fmt.Sprintf("playback end; pos=%d; completed=%d; ip=%s; ua=%s", pos, completed, c.ClientIP(), ua)
	if sid != "" {
		msg += "; session_id=" + sid
	}
	h.logActivity(logUID, username, "playback_end", &id, msg)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// powerPlayerPlaybackPlan returns defaults merged with server YAML (powerplayer:).
func (h *Handler) powerPlayerPlaybackPlan() gin.H {
	baseURL := "/static/powerplayer6"
	skin := "skin.zip"
	clientCert := "powerplayer"
	var powerdrmURL, weburlparam, stats string
	if h != nil && h.App != nil && h.App.Config != nil {
		p := h.App.Config.PowerPlayer
		if s := strings.TrimSpace(p.BaseURL); s != "" {
			baseURL = s
		}
		if s := strings.TrimSpace(p.Skin); s != "" {
			skin = s
		}
		if s := strings.TrimSpace(p.ClientCert); s != "" {
			clientCert = s
		}
		powerdrmURL = strings.TrimSpace(p.PowerDRMURL)
		weburlparam = strings.TrimSpace(p.WebURLParam)
		stats = strings.TrimSpace(p.StatisticsServer)
	}
	return gin.H{
		"base_url":          baseURL,
		"skin":              skin,
		"powerdrm_url":      powerdrmURL,
		"weburlparam":       weburlparam,
		"statistics_server": stats,
		"client_cert":       clientCert,
	}
}

func (h *Handler) withPlaybackPlan(base gin.H) gin.H {
	if base == nil {
		base = gin.H{}
	}
	base["powerplayer"] = h.powerPlayerPlaybackPlan()
	return base
}

func defaultPlayerEngineOrder(planMode string) []string {
	switch planMode {
	case "hls_powerdrm":
		return []string{"powerplayer"}
	case "hls_drm":
		return []string{"powerplayer", "shaka", "xgplayer"}
	default:
		// native, hls, jit_hls, hls_aes_128, and any future clear modes
		return []string{"powerplayer", "xgplayer"}
	}
}

func normalizePlayerEngineList(in []string, planMode string) []string {
	allowed := map[string]struct{}{"powerplayer": {}, "shaka": {}, "xgplayer": {}}
	var out []string
	seen := map[string]struct{}{}
	for _, s := range in {
		x := strings.ToLower(strings.TrimSpace(s))
		if x == "" {
			continue
		}
		if _, ok := allowed[x]; !ok {
			continue
		}
		if planMode == "hls_powerdrm" && x != "powerplayer" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

func (h *Handler) resolvePlayerEngineOrder(planMode string) []string {
	fallback := defaultPlayerEngineOrder(planMode)
	if h == nil || h.App == nil || h.App.Config == nil {
		return fallback
	}
	var fromCfg []string
	switch planMode {
	case "hls_powerdrm":
		fromCfg = h.App.Config.Playback.Engines.HLSPowerDRM
	case "hls_drm":
		fromCfg = h.App.Config.Playback.Engines.DRM
	default:
		fromCfg = h.App.Config.Playback.Engines.ProgressiveHLS
	}
	if len(fromCfg) == 0 {
		return fallback
	}
	norm := normalizePlayerEngineList(fromCfg, planMode)
	if len(norm) == 0 {
		return fallback
	}
	return norm
}

func (h *Handler) withPlaybackPlanForMode(planMode string, base gin.H) gin.H {
	base = h.withPlaybackPlan(base)
	base["player_engine_order"] = h.resolvePlayerEngineOrder(planMode)
	return base
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
	var srcHeight, srcWidth, srcDuration, srcBitrate sql.NullInt64
	if err := h.App.DB.QueryRow(`SELECT file_id, file_path, meta_json, height, width, duration, COALESCE(bitrate, 0) FROM media WHERE id = ?`, id).Scan(&fileID, &filePath, &metaJSON, &srcHeight, &srcWidth, &srcDuration, &srcBitrate); err != nil {
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
	caps := readClientCaps(c)
	homeLimit, disableStreamTranscode := h.loadHomeStreamPlayback()
	needsQualityTranscode := !disableStreamTranscode && playback.SourceExceedsLimit(
		int(srcHeight.Int64), int(srcWidth.Int64), srcBitrate.Int64, homeLimit,
	)
	pickJIT := func() (string, string) {
		return playback.PickJITParams(int(srcHeight.Int64), int(srcWidth.Int64), caps.MaxHeight, homeLimit)
	}
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

	pol := h.loadStreamPolicy(id)
	if pol.DRMEnabled {
		if encReady, _, encType := h.latestEncryptedManifest(id); encReady {
			switch encType {
			case "hls_aes_128":
				c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_aes_128", gin.H{
					"mode":       "hls_aes_128",
					"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
					"status":     "done",
					"fallback":   playURL,
				}))
				return
			case "hls_powerdrm":
				c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_powerdrm", gin.H{
					"mode":       "hls_powerdrm",
					"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
					"status":     "done",
					"drm": gin.H{
						"powerdrm_key_url": powerDRMKeyURL,
					},
					"fallback": playURL,
				}))
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
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_drm", gin.H{
				"mode":       "hls_drm",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     "done",
				"drm":        drmPayload,
				"fallback":   playURL,
			}))
			return
		}
		if h.SessionManager != nil && fileID.Valid && strings.TrimSpace(fileID.String) != "" && filePath.Valid && strings.TrimSpace(filePath.String) != "" {
			bitrate, resolution := pickJIT()
			s, err := h.createJITSession(c, id, fileID.String, filePath.String, bitrate, resolution, float64(srcDuration.Int64))
			if err != nil {
				if c.Writer.Written() {
					return
				}
				log.Printf("stream drm jit session create failed media=%d: %v", id, err)
			} else {
				if streamEnc, encErr := h.ensureStreamJITEncryption(id, pol, s.TempDir); encErr == nil {
					s.StreamEncryption = streamEnc
				} else {
					log.Printf("stream jit encryption setup failed media=%d: %v", id, encErr)
					h.SessionManager.CancelSession(s.ID)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "stream encryption setup failed"})
					return
				}
				masterBase := fmt.Sprintf("%s/api/v1/jit/session/%s/master.m3u8", base, s.ID)
				mq := url.Values{}
				mq.Set("media_id", strconv.FormatInt(id, 10))
				if accessToken != "" {
					mq.Set("access_token", accessToken)
				}
				hlsMaster := masterBase + "?" + mq.Encode()
				planMode := pol.PlaybackPlanMode()
				payload := gin.H{
					"mode":            planMode,
					"hls_master":      hlsMaster,
					"status":          "processing",
					"fallback":        playURL,
					"stream_drm":      true,
					"encryption_mode": pol.EncryptionMode,
					"session_id":      s.ID,
					"message":         "Real-time stream encryption at playback (JIT)",
				}
				switch planMode {
				case "hls_powerdrm":
					payload["drm"] = gin.H{"powerdrm_key_url": powerDRMKeyURL}
				case "hls_drm":
					widevineTransport := "json_local"
					if h.App != nil && h.App.Config != nil && strings.TrimSpace(h.App.Config.DRM.Widevine.PrivateModuleURL) != "" {
						widevineTransport = "raw"
					}
					drmPayload := gin.H{
						"widevine_license_url": widevineURL,
						"widevine_transport":   widevineTransport,
						"powerdrm_key_url":     powerDRMKeyURL,
						"fairplay_cert_url":    fairplayCertURL,
						"fairplay_license_url": fairplayLicenseURL,
					}
					if widevineServiceCertURL != "" {
						drmPayload["widevine_service_cert_url"] = widevineServiceCertURL
					}
					payload["drm"] = drmPayload
				}
				c.JSON(http.StatusOK, h.withPlaybackPlanForMode(planMode, payload))
				return
			}
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stream encryption playback unavailable"})
		return
	}

	if encReady, encMaster, encType := h.latestEncryptedManifest(id); encReady {
		switch encType {
		case "hls_aes_128":
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_aes_128", gin.H{
				"mode":       "hls_aes_128",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     "done",
				"fallback":   playURL,
			}))
			_ = encMaster
			return
		case "hls_powerdrm":
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_powerdrm", gin.H{
				"mode":       "hls_powerdrm",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     "done",
				"drm": gin.H{
					"powerdrm_key_url": powerDRMKeyURL,
				},
				"fallback": playURL,
			}))
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
		c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls_drm", gin.H{
			"mode":       "hls_drm",
			"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
			"status":     "done",
			"drm":        drmPayload,
			"fallback":   playURL,
		}))
		return
	}
	media := detectMediaProfile(metaJSON.String)
	atRestEnc := h.KeyVault != nil && storage.IsMediaEncrypted(h.App.DB, id, filePath.String)

	// Check for existing optimized batch transcode output (non-stream-DRM libraries only).
	if !pol.DRMEnabled {
		if hlsReady, _, hlsStatus, hlsTaskID := h.latestTranscodeManifestByMediaID(id); hlsReady {
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls", gin.H{
				"mode":       "hls",
				"hls_master": fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id")),
				"status":     hlsStatus,
				"task_id":    hlsTaskID,
				"fallback":   playURL,
				"message":    "Use transcoded stream",
			}))
			return
		}
	}

	// Knox .enc at rest: prefer JIT (pipe decrypt) when the client cannot direct-play; otherwise native decrypt stream.
	if atRestEnc && (!canDirectPlay(media, caps) || needsQualityTranscode) {
		if h.SessionManager != nil && fileID.Valid && strings.TrimSpace(fileID.String) != "" && filePath.Valid && strings.TrimSpace(filePath.String) != "" {
			bitrate, resolution := pickJIT()
			s, err := h.createJITSession(c, id, fileID.String, filePath.String, bitrate, resolution, float64(srcDuration.Int64))
			if err != nil {
				if c.Writer.Written() {
					return
				}
			} else {
				masterBase := fmt.Sprintf("%s/api/v1/jit/session/%s/master.m3u8", base, s.ID)
				mq := url.Values{}
				mq.Set("media_id", strconv.FormatInt(id, 10))
				if accessToken != "" {
					mq.Set("access_token", accessToken)
				}
				hlsMaster := masterBase + "?" + mq.Encode()
				c.JSON(http.StatusOK, h.withPlaybackPlanForMode("jit_hls", gin.H{
					"mode":       "jit_hls",
					"hls_master": hlsMaster,
					"status":     "processing",
					"fallback":   playURL,
					"message":    "JIT transcode from encrypted asset (pipe decrypt)",
					"session_id": s.ID,
				}))
				return
			}
		}
	}

	// Check if client can decode the source directly (plaintext or decrypted progressive).
	if (canDirectPlay(media, caps) && !needsQualityTranscode) || (atRestEnc && !pol.DRMEnabled && !needsQualityTranscode) {
		c.JSON(http.StatusOK, h.withPlaybackPlanForMode("native", gin.H{
			"mode":          "native",
			"playUrl":       playURL,
			"mime_type":     containerMimeType(media.Container),
			"media_profile": media,
			"client_caps":   caps,
			"message":       "Client can decode source directly",
		}))
		return
	}

	// Fallback: JIT transcoding when client cannot play source directly.
	// New Redis-free session JIT.
	if h.SessionManager != nil && fileID.Valid && strings.TrimSpace(fileID.String) != "" && filePath.Valid && strings.TrimSpace(filePath.String) != "" {
		bitrate, resolution := pickJIT()
		s, err := h.createJITSession(c, id, fileID.String, filePath.String, bitrate, resolution, float64(srcDuration.Int64))
		if err != nil {
			if c.Writer.Written() {
				return
			}
			log.Printf("JIT session create failed for media=%d: %v, falling back", id, err)
		} else {
			masterBase := fmt.Sprintf("%s/api/v1/jit/session/%s/master.m3u8", base, s.ID)
			mq := url.Values{}
			mq.Set("media_id", strconv.FormatInt(id, 10))
			if accessToken != "" {
				mq.Set("access_token", accessToken)
			}
			hlsMaster := masterBase + "?" + mq.Encode()
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("jit_hls", gin.H{
				"mode":       "jit_hls",
				"hls_master": hlsMaster,
				"status":     "processing",
				"fallback":   playURL,
				"message":    "JIT session (Redis-free)",
				"session_id": s.ID,
			}))
			return
		}
	}

	// Legacy JIT scheduler: Redis-based per-segment transcode.
	if h.Instant != nil && fileID.Valid && strings.TrimSpace(fileID.String) != "" && filePath.Valid && strings.TrimSpace(filePath.String) != "" {
		sessionID := c.GetHeader("X-Session-ID")
		if strings.TrimSpace(sessionID) == "" {
			sessionID = c.ClientIP() + "-" + c.Request.UserAgent()
		}
		if err := h.Instant.PrepareVideoMeta(fileID.String, filePath.String, media.Container, media.Video, media.Audio); err == nil {
			h.Instant.PrepareVideoMetaExt(fileID.String, int(srcWidth.Int64), int(srcHeight.Int64), float64(srcDuration.Int64))
			go func(fid, sid string) {
				_ = h.Instant.TriggerSlicing(fid, sid)
			}(fileID.String, sessionID)
			jitPauseURL := base + "/api/v1/jit/session/pause"
			jitResumeURL := base + "/api/v1/jit/session/resume"
			jitEndURL := base + "/api/v1/jit/session/end"
			jitSeekURL := base + "/api/v1/jit/session/seek"
			if accessToken != "" {
				jitPauseURL = appendQueryValue(jitPauseURL, "access_token", accessToken)
				jitResumeURL = appendQueryValue(jitResumeURL, "access_token", accessToken)
				jitEndURL = appendQueryValue(jitEndURL, "access_token", accessToken)
				jitSeekURL = appendQueryValue(jitSeekURL, "access_token", accessToken)
			}
			c.JSON(http.StatusOK, h.withPlaybackPlanForMode("jit_hls", gin.H{
				"mode":                   "jit_hls",
				"hls_master":             h.jitLegacyMasterURL(base, fileID.String, homeLimit),
				"status":                 "processing",
				"fallback":               playURL,
				"media_profile":          media,
				"client_caps":            caps,
				"jit_session_pause_url":  jitPauseURL,
				"jit_session_resume_url": jitResumeURL,
				"jit_session_end_url":    jitEndURL,
				"jit_session_seek_url":   jitSeekURL,
				"message":                "JIT transcoding pipeline (per-segment on demand)",
			}))
			return
		}
		log.Printf("JIT PrepareVideoMeta failed for media=%d file=%s, falling back", id, fileID.String)
	}

	// Final fallback: trigger batch transcode.
	playlist, status, taskID, terr := h.Worker.EnsureHLS(fileID.String, filePath.String, int(srcHeight.Int64), caps.MaxHeight, caps.Qualities)
	if terr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": terr.Error()})
		return
	}
	if status == "waiting" {
		h.kickTranscodeWorker()
	}
	playlistURL := ""
	if playlist != "" {
		playlistURL = fmt.Sprintf("%s/api/v1/media/%s/hls/master.m3u8", base, c.Param("id"))
	}
	c.JSON(http.StatusOK, h.withPlaybackPlanForMode("hls", gin.H{
		"mode":          "hls",
		"hls_master":    playlistURL,
		"status":        status,
		"task_id":       taskID,
		"fallback":      playURL,
		"media_profile": media,
		"client_caps":   caps,
		"message":       "Source codec/container unsupported for direct playback, switching to adaptive HLS",
	}))
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
	if ready, master, _, _ := h.latestTranscodeManifestByMediaID(id); ready {
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

func (h *Handler) latestTranscodeManifestByMediaID(mediaID int64) (ready bool, manifestPath string, status string, taskID int64) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return false, "", "", 0
	}
	var fileID sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, mediaID).Scan(&fileID); err != nil || !fileID.Valid || strings.TrimSpace(fileID.String) == "" {
		return false, "", "", 0
	}
	var out sql.NullString
	var taskStatus sql.NullString
	var tid sql.NullInt64
	_ = h.App.DB.QueryRow(
		`SELECT id, output_path, status FROM transcode_task WHERE file_id = ? AND quality LIKE 'abr:%' ORDER BY id DESC LIMIT 1`,
		fileID.String,
	).Scan(&tid, &out, &taskStatus)
	if !out.Valid || (taskStatus.String != "done" && taskStatus.String != "running") {
		return false, "", "", 0
	}
	// Verify the master file still exists on disk (stale DB records can point to deleted files).
	if st, err := os.Stat(out.String); err != nil || st.IsDir() {
		return false, "", "", 0
	}
	return true, out.String, taskStatus.String, tid.Int64
}

type clientCaps struct {
	VideoCodecs []string `json:"video_codecs"`
	AudioCodecs []string `json:"audio_codecs"`
	MaxHeight   int      `json:"max_height"`
	Qualities   []string `json:"qualities"`
	// Containers lists MIME-derived container tokens the browser reports (mp4/mkv/webm/ogg/flv).
	Containers []string `json:"containers,omitempty"`
	// Mcap is a compact MediaCapabilities summary from the web player (H.264/H.265 × ladder).
	Mcap string `json:"mcap,omitempty"`
}

type mediaProfile struct {
	Container string `json:"container"`
	Video     string `json:"video_codec"`
	Audio     string `json:"audio_codec"`
}

func (h *Handler) loadHomeStreamPlayback() (playback.HomeStreamLimit, bool) {
	if h == nil || h.App == nil || h.App.DB == nil {
		return playback.HomeStreamLimit{Auto: true}, false
	}
	var raw sql.NullString
	if err := h.App.DB.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		return playback.HomeStreamLimit{Auto: true}, false
	}
	opts := decodeSystemOptions(raw.String)
	return playback.ParseHomeStreamQuality(opts.Playback.HomeStreamQuality), opts.Transcoder.DisableVideoStreamTranscoding
}

func (h *Handler) jitLegacyMasterURL(base, fileID string, limit playback.HomeStreamLimit) string {
	u := fmt.Sprintf("%s/api/v1/jit/master/%s", base, fileID)
	if !limit.Auto && limit.MaxHeight > 0 {
		u = appendQueryValue(u, "max_height", strconv.Itoa(limit.MaxHeight))
	}
	return u
}

func readClientCaps(c *gin.Context) clientCaps {
	cap := clientCaps{
		VideoCodecs: parseCSV(c.Query("video_codecs")),
		AudioCodecs: parseCSV(c.Query("audio_codecs")),
		Qualities:   parseCSV(c.Query("qualities")),
		Containers:  parseCSV(c.Query("containers")),
		Mcap:        strings.TrimSpace(c.Query("mcap")),
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

// containerMimeType maps container format names (from ffprobe format_name) to MIME types.
// The input is typically a comma-separated list like "mov,mp4,m4a,3gp,3g2,mj2"; we return the
// first recognized MIME type. WebM files often report "matroska,webm"; in that case we return
// video/webm, not video/x-matroska.
func containerMimeType(container string) string {
	c := strings.ToLower(strings.TrimSpace(container))
	if c == "" {
		return "video/mp4"
	}
	hasWebM, hasMatroska := false, false
	for _, fragment := range strings.Split(c, ",") {
		fragment = strings.TrimSpace(fragment)
		if fragment == "" {
			continue
		}
		for _, part := range strings.Split(fragment, "+") {
			p := strings.TrimSpace(part)
			switch p {
			case "webm":
				hasWebM = true
			case "matroska", "mkv":
				hasMatroska = true
			}
		}
	}
	if hasWebM {
		return "video/webm"
	}
	if hasMatroska {
		return "video/x-matroska"
	}
	for _, fragment := range strings.Split(c, ",") {
		fragment = strings.TrimSpace(fragment)
		if fragment == "" {
			continue
		}
		for _, part := range strings.Split(fragment, "+") {
			p := strings.TrimSpace(part)
			switch p {
			case "mp4", "m4a", "m4v", "3gp", "3g2", "mov", "mj2":
				return "video/mp4"
			case "mpegts", "mts", "m2ts", "ts":
				return "video/mp2t"
			case "mpeg", "mpg", "mpe", "vob":
				return "video/mpeg"
			case "ogg", "ogv":
				return "video/ogg"
			case "flv":
				return "video/x-flv"
			case "avi":
				return "video/x-msvideo"
			case "wmv":
				return "video/x-ms-wmv"
			case "asf":
				return "video/x-ms-asf"
			case "isom", "iso2", "iso5", "iso6":
				return "video/mp4"
			}
		}
	}
	return "video/mp4" // fallback
}

// pickBitrate selects a single bitrate based on source resolution.
func pickBitrate(media mediaProfile, width, height int) string {
	maxH := height
	if width > height {
		maxH = width
	}
	switch {
	case maxH >= 1080:
		return "4000k"
	case maxH >= 720:
		return "2000k"
	case maxH >= 480:
		return "1000k"
	default:
		return "500k"
	}
}

// resolutionForBitrate maps bitrate to WxH.
func resolutionForBitrate(bitrate string) string {
	switch bitrate {
	case "8000k":
		return "3840:2160"
	case "4000k":
		return "1920:1080"
	case "2000k":
		return "1280:720"
	case "1000k":
		return "854:480"
	case "500k":
		return "640:360"
	default:
		return "1280:720"
	}
}

// directPlayContainerNeeds maps ffprobe format_name (comma-separated) to a client "containers" token
// (mp4 / mkv / webm / ogg / flv). The second value is false if the mux cannot be direct-played in a typical
// HTMLMediaElement path (e.g. MPEG-TS). FLV is direct-playable when Knox players (PowerPlayer / xgplayer)
// demux in JS and the client advertises matching codec + flv container support. When the first value is empty
// and the second is true, there is no token to match against the client's list (empty meta or only unrecognized tags).
func directPlayContainerNeeds(formatName string) (need string, ok bool) {
	c := strings.ToLower(strings.TrimSpace(formatName))
	if c == "" {
		return "", true
	}
	toks := make(map[string]struct{})
	for _, fragment := range strings.Split(c, ",") {
		fragment = strings.TrimSpace(fragment)
		if fragment == "" {
			continue
		}
		for _, part := range strings.Split(fragment, "+") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			switch p {
			case "flv":
				return "flv", true
			case "mpegts", "mts", "m2ts", "ts", "avi", "wmv", "asf", "mpeg", "mpg", "mpe", "vob":
				return "", false
			case "matroska", "mkv":
				toks["mkv"] = struct{}{}
			case "webm":
				toks["webm"] = struct{}{}
			case "ogg", "ogv":
				toks["ogg"] = struct{}{}
			case "mp4", "m4a", "m4v", "mov", "mj2", "3gp", "3g2", "isom", "iso2", "iso5", "iso6":
				toks["mp4"] = struct{}{}
			case "dash", "crypto", "cenc":
				// auxiliary hints; ignore
			default:
				// unknown fragment
			}
		}
	}
	if len(toks) == 0 {
		return "", true
	}
	// ffprobe often reports WebM as "matroska,webm". Browsers advertise `webm` in <video> probes,
	// not `mkv`, so prefer webm whenever that tag is present; use mkv only for matroska-only sources.
	switch {
	case containsToken(toks, "webm"):
		return "webm", true
	case containsToken(toks, "mkv"):
		return "mkv", true
	case containsToken(toks, "ogg"):
		return "ogg", true
	case containsToken(toks, "mp4"):
		return "mp4", true
	default:
		return "", true
	}
}

func containsToken(m map[string]struct{}, k string) bool {
	_, v := m[k]
	return v
}

func clientContainersInclude(list []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return true
	}
	for _, it := range list {
		if strings.ToLower(strings.TrimSpace(it)) == want {
			return true
		}
	}
	return false
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
	need, allow := directPlayContainerNeeds(media.Container)
	if !allow {
		return false
	}
	if len(caps.Containers) > 0 {
		if need != "" && !clientContainersInclude(caps.Containers, need) {
			return false
		}
	} else {
		// Legacy clients (no container probe): keep conservative blocks.
		mc := strings.ToLower(media.Container)
		if strings.Contains(mc, "matroska") {
			return false
		}
		if strings.Contains(mc, "flv") {
			return false
		}
	}
	return true
}

func fallbackDirectPlayHeuristic(media mediaProfile) bool {
	video := strings.ToLower(strings.TrimSpace(media.Video))
	audio := strings.ToLower(strings.TrimSpace(media.Audio))
	container := strings.ToLower(strings.TrimSpace(media.Container))
	if strings.Contains(container, "matroska") {
		return false
	}
	videoOK := video == "h264" || video == "avc1" || video == "h265" || video == "hevc"
	if strings.Contains(container, "flv") {
		if !videoOK {
			return false
		}
		return audio == "" || audio == "aac" || audio == "mp3"
	}
	if !videoOK {
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
		if n == "vp9" && (codec == "vp9" || codec == "vp09") {
			return true
		}
		if n == "ac3" && (codec == "ac3" || codec == "ac-3") {
			return true
		}
		if (n == "eac3" || n == "ec-3") && (codec == "eac3" || codec == "ec-3") {
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

// Position 使用指针：binding:"required" 在数值类型上会拒绝 0，而 position=0 用于「未观看」等合法场景。
type progressBody struct {
	Position  *int64  `json:"position" binding:"required"`
	Completed *int    `json:"completed"`
	SessionID *string `json:"session_id"`
}

type playbackLogBody struct {
	Position  *int64  `json:"position"`
	Completed *int    `json:"completed"`
	SessionID *string `json:"session_id"`
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
	pos := *body.Position
	if pos < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid position"})
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
		_, execErr = h.App.DB.Exec(`INSERT INTO play_progress (user_id, file_id, position, completed) VALUES (?, ?, ?, ?)`, uid, fileID, pos, completed)
	} else {
		_, execErr = h.App.DB.Exec(`UPDATE play_progress SET position = ?, completed = ?, update_at = CURRENT_TIMESTAMP WHERE user_id = ? AND file_id = ?`, pos, completed, uid, fileID)
	}
	if execErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": execErr.Error()})
		return
	}
	var username sql.NullString
	_ = h.App.DB.QueryRow(`SELECT username FROM user WHERE id = ?`, uid).Scan(&username)
	sid := ""
	if body.SessionID != nil {
		sid = strings.TrimSpace(*body.SessionID)
	}
	msg := "save playback progress"
	if sid != "" {
		msg += "; session_id=" + sid
	}
	h.logActivity(uid, username.String, "progress", &id, msg)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) ClearProgress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
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
	if _, err := h.App.DB.Exec(`DELETE FROM play_progress WHERE user_id = ? AND file_id = ?`, uid, fileID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
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
	h.serveDerivedAsset(c, id, fp, contentType)
}
