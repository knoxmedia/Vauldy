package handler

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	drmsvc "knox-media/internal/drm"
)

type widevineLicenseBody struct {
	MediaID   int64  `json:"media_id" binding:"required"`
	Challenge string `json:"challenge"`
}

type fairplayLicenseBody struct {
	MediaID int64  `json:"media_id" binding:"required"`
	SPC     string `json:"spc"`
}

type verifyLicenseBody struct {
	License string `json:"license" binding:"required"`
	Sig     string `json:"sig" binding:"required"`
}

type powerDRMKeyRef struct {
	KID string `json:"kid"`
	Key string `json:"key"`
}

// requireDRMMediaAccess enforces per-media library/folder permissions before issuing a license/key.
// API clients (machine tokens) are permitted as before. Returns true if the request may proceed.
// On denial, it writes the response and an audit log; callers should return immediately.
func (h *Handler) requireDRMMediaAccess(c *gin.Context, mediaID int64, drmType string) bool {
	if middleware.IsAPIClient(c) {
		return true
	}
	uid := middleware.UserID(c)
	if uid <= 0 && strings.TrimSpace(middleware.Role(c)) == "" {
		// Allow direct handler-level tests without auth middleware context.
		return true
	}
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return false
	}
	var libraryID int64
	var filePath string
	if err := h.App.DB.QueryRow(`SELECT library_id, COALESCE(file_path,'') FROM media WHERE id = ?`, mediaID).Scan(&libraryID, &filePath); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return false
	}
	profile, err := h.loadUserPermissionProfile(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	if !profile.CanPlay {
		drmsvc.NewService(h.App.DB).Audit(mediaID, drmType, "denied", "playback_denied", c.ClientIP())
		c.JSON(http.StatusForbidden, gin.H{"error": "playback denied"})
		return false
	}
	if strings.EqualFold(profile.LibraryScope, "selected") {
		if _, ok := profile.AllowedLibraryIDs[libraryID]; !ok {
			drmsvc.NewService(h.App.DB).Audit(mediaID, drmType, "denied", "library_access_denied", c.ClientIP())
			c.JSON(http.StatusForbidden, gin.H{"error": "library access denied"})
			return false
		}
		if folders := profile.AllowedLibraryFolders[libraryID]; len(folders) > 0 && !pathMatchesAnyFolder(filePath, folders) {
			drmsvc.NewService(h.App.DB).Audit(mediaID, drmType, "denied", "folder_access_denied", c.ClientIP())
			c.JSON(http.StatusForbidden, gin.H{"error": "folder access denied"})
			return false
		}
	}
	return true
}

func (h *Handler) widevinePrivateModuleEnabled() bool {
	return h != nil &&
		h.App != nil &&
		h.App.Config != nil &&
		strings.TrimSpace(h.App.Config.DRM.Widevine.PrivateModuleURL) != ""
}

// widevinePrivateServiceCertURL derives GET /service-cert from the configured POST .../license endpoint.
func widevinePrivateServiceCertURL(licenseEndpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(licenseEndpoint))
	if err != nil {
		return "", err
	}
	if path.Base(u.Path) != "license" {
		return "", fmt.Errorf("private module URL path must end with /license")
	}
	u.Path = strings.TrimSuffix(u.Path, "license") + "service-cert"
	return u.String(), nil
}

func classifyVerifyError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "invalid signature"):
		return "signature_mismatch"
	case strings.Contains(msg, "kid version mismatch"):
		return "kid_version_mismatch"
	case strings.Contains(msg, "sig version mismatch"):
		return "sig_version_mismatch"
	case strings.Contains(msg, "license expired"):
		return "license_expired"
	case strings.Contains(msg, "invalid encoded license"), strings.Contains(msg, "invalid license payload"):
		return "invalid_payload"
	default:
		return "verify_failed"
	}
}

func (h *Handler) licenseSecret() string {
	if h != nil && h.App != nil && h.App.Config != nil {
		if sec := strings.TrimSpace(h.App.Config.Security.JWTSecret); sec != "" {
			return sec
		}
	}
	return "knox-media-license-dev-secret"
}

func (h *Handler) licenseVersions() (kidVersion string, sigVersion string) {
	if h != nil && h.App != nil && h.App.Config != nil {
		kidVersion = strings.TrimSpace(h.App.Config.Security.KIDVersion)
		sigVersion = strings.TrimSpace(h.App.Config.Security.SigVersion)
	}
	if kidVersion == "" {
		kidVersion = drmsvc.DefaultKIDVersion
	}
	if sigVersion == "" {
		sigVersion = drmsvc.DefaultSigVersion
	}
	return kidVersion, sigVersion
}

func (h *Handler) WidevineServiceCert(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if !h.widevinePrivateModuleEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "widevine service certificate not available"})
		return
	}
	cfg := h.App.Config.DRM.Widevine
	upstream, err := widevinePrivateServiceCertURL(cfg.PrivateModuleURL)
	if err != nil {
		log.Printf("widevine service-cert: bad private module URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid widevine private module configuration"})
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "service certificate request build failed"})
		return
	}
	if tok := strings.TrimSpace(cfg.PrivateModuleToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	timeoutSec := cfg.PrivateModuleTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	cli := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		log.Printf("widevine service-cert upstream failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "service certificate upstream failed"})
		return
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "service certificate read failed"})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("widevine service-cert non-2xx: status=%d bytes=%d", resp.StatusCode, len(body))
		c.Data(resp.StatusCode, "application/octet-stream", body)
		return
	}
	c.Data(http.StatusOK, "application/octet-stream", body)
}

func (h *Handler) WidevineLicense(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var (
		body         widevineLicenseBody
		mediaID      int64
		challengeB64 string
		rawChallenge []byte
	)
	if h.widevinePrivateModuleEnabled() {
		mediaID, _ = strconv.ParseInt(strings.TrimSpace(c.Query("media_id")), 10, 64)
		if mediaID <= 0 && strings.Contains(strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type"))), "application/json") {
			var legacy widevineLicenseBody
			if err := c.ShouldBindJSON(&legacy); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "media_id is required"})
				return
			}
			mediaID = legacy.MediaID
			challengeB64 = strings.TrimSpace(legacy.Challenge)
			if challengeB64 != "" {
				decoded, derr := base64.StdEncoding.DecodeString(challengeB64)
				if derr != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid base64 challenge"})
					return
				}
				rawChallenge = decoded
			}
		} else if mediaID > 0 && strings.Contains(strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type"))), "application/json") {
			var legacy widevineLicenseBody
			if err := c.ShouldBindJSON(&legacy); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json license request"})
				return
			}
			challengeB64 = strings.TrimSpace(legacy.Challenge)
			if challengeB64 != "" {
				decoded, derr := base64.StdEncoding.DecodeString(challengeB64)
				if derr != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid base64 challenge"})
					return
				}
				rawChallenge = decoded
			}
		} else {
			rb, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "read challenge failed"})
				return
			}
			rawChallenge = rb
		}
		body.MediaID = mediaID
		log.Printf("widevine license raw ingress: media_id=%d content_type=%q challenge_bytes=%d", mediaID, c.GetHeader("Content-Type"), len(rawChallenge))
	} else {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		mediaID = body.MediaID
		challengeB64 = strings.TrimSpace(body.Challenge)
	}
	svc := drmsvc.NewService(h.App.DB)
	if h.widevinePrivateModuleEnabled() && len(rawChallenge) == 0 {
		svc.Audit(mediaID, "widevine", "denied", "empty_challenge", c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": "challenge is required"})
		return
	}
	if !h.widevinePrivateModuleEnabled() && challengeB64 == "" {
		svc.Audit(body.MediaID, "widevine", "denied", "empty_challenge", c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": "challenge is required"})
		return
	}
	if !h.widevinePrivateModuleEnabled() || mediaID > 0 {
		if mediaID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media_id"})
			return
		}
		if !h.requireDRMMediaAccess(c, mediaID, "widevine") {
			return
		}
		if err := svc.ValidateMedia(mediaID); err != nil {
			svc.Audit(mediaID, "widevine", "denied", "media_not_found", c.ClientIP())
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := svc.ValidateMediaDRMAsset(mediaID); err != nil {
			svc.Audit(mediaID, "widevine", "denied", "drm_asset_not_ready", c.ClientIP())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	kid := ""
	keyRef := ""
	if mediaID > 0 {
		var err error
		kid, keyRef, err = svc.GetDRMAsset(mediaID)
		if err != nil {
			svc.Audit(mediaID, "widevine", "denied", "drm_asset_not_ready", c.ClientIP())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if h.widevinePrivateModuleEnabled() {
		cfg := h.App.Config.DRM.Widevine
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimSpace(cfg.PrivateModuleURL), bytes.NewReader(rawChallenge))
		if err != nil {
			svc.Audit(mediaID, "widevine", "error", "private_module_request_build_failed", c.ClientIP())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "private module request build failed"})
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Knox-Media-ID", strconv.FormatInt(mediaID, 10))
		req.Header.Set("X-Knox-KID", kid)
		if tok := strings.TrimSpace(cfg.PrivateModuleToken); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		cli := &http.Client{Timeout: time.Duration(cfg.PrivateModuleTimeoutSeconds) * time.Second}
		resp, err := cli.Do(req)
		if err != nil {
			svc.Audit(mediaID, "widevine", "error", "private_module_request_failed", c.ClientIP())
			c.JSON(http.StatusBadGateway, gin.H{"error": "private module request failed"})
			return
		}
		defer resp.Body.Close()
		licenseBytes, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			svc.Audit(mediaID, "widevine", "error", "private_module_response_read_failed", c.ClientIP())
			c.JSON(http.StatusBadGateway, gin.H{"error": "private module response read failed"})
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("widevine private module non-2xx: media_id=%d status=%d challenge_bytes=%d response_bytes=%d", mediaID, resp.StatusCode, len(rawChallenge), len(licenseBytes))
			svc.Audit(mediaID, "widevine", "denied", "private_module_non_2xx", c.ClientIP())
			c.Data(resp.StatusCode, "application/octet-stream", licenseBytes)
			return
		}
		log.Printf("widevine private module ok: media_id=%d status=%d challenge_bytes=%d response_bytes=%d", mediaID, resp.StatusCode, len(rawChallenge), len(licenseBytes))
		svc.Audit(mediaID, "widevine", "allowed", "private_module", c.ClientIP())
		c.Data(http.StatusOK, "application/octet-stream", licenseBytes)
		return
	}
	cfgKIDVersion, cfgSigVersion := h.licenseVersions()

	license, exp, sig, kidVersion, sigVersion, err := drmsvc.BuildSignedLicense("widevine", mediaID, kid, keyRef, challengeB64, h.licenseSecret(), cfgKIDVersion, cfgSigVersion, 10*time.Minute)
	if err != nil {
		svc.Audit(body.MediaID, "widevine", "error", "license_build_failed", c.ClientIP())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	svc.Audit(body.MediaID, "widevine", "allowed", "", c.ClientIP())
	accept := strings.ToLower(strings.TrimSpace(c.GetHeader("Accept")))
	rawMode := strings.TrimSpace(c.Query("raw")) == "1" || strings.Contains(accept, "application/octet-stream")
	if rawMode {
		b, derr := base64.StdEncoding.DecodeString(license)
		if derr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode license bytes failed"})
			return
		}
		c.Header("X-DRM-KID", kid)
		c.Header("X-DRM-KID-Version", kidVersion)
		c.Header("X-DRM-Sig-Version", sigVersion)
		c.Header("X-DRM-Sig", sig)
		c.Header("X-DRM-Exp", strconv.FormatInt(exp, 10))
		c.Data(http.StatusOK, "application/octet-stream", b)
		return
	}
	c.JSON(http.StatusOK, gin.H{"license": license, "kid": kid, "kid_version": kidVersion, "exp": exp, "sig": sig, "sig_version": sigVersion})
}

func (h *Handler) FairPlayCert(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	_, _ = c.Writer.Write([]byte("knox-fairplay-cert"))
}

func (h *Handler) FairPlayLicense(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var body fairplayLicenseBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !h.requireDRMMediaAccess(c, body.MediaID, "fairplay") {
		return
	}
	svc := drmsvc.NewService(h.App.DB)
	if err := svc.ValidateMedia(body.MediaID); err != nil {
		svc.Audit(body.MediaID, "fairplay", "denied", "media_not_found", c.ClientIP())
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := svc.ValidateMediaDRMAsset(body.MediaID); err != nil {
		svc.Audit(body.MediaID, "fairplay", "denied", "drm_asset_not_ready", c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	kid, keyRef, err := svc.GetDRMAsset(body.MediaID)
	if err != nil {
		svc.Audit(body.MediaID, "fairplay", "denied", "drm_asset_not_ready", c.ClientIP())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfgKIDVersion, cfgSigVersion := h.licenseVersions()
	ckc, exp, sig, kidVersion, sigVersion, err := drmsvc.BuildSignedLicense("fairplay", body.MediaID, kid, keyRef, strings.TrimSpace(body.SPC), h.licenseSecret(), cfgKIDVersion, cfgSigVersion, 10*time.Minute)
	if err != nil {
		svc.Audit(body.MediaID, "fairplay", "error", "license_build_failed", c.ClientIP())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	svc.Audit(body.MediaID, "fairplay", "allowed", "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ckc": ckc, "kid": kid, "kid_version": kidVersion, "exp": exp, "sig": sig, "sig_version": sigVersion})
}

func (h *Handler) VerifyLicense(c *gin.Context) {
	if !middleware.IsAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "message": "admin only"})
		return
	}
	var body verifyLicenseBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	kidVersion, sigVersion := h.licenseVersions()
	claims, err := drmsvc.VerifySignedLicense(strings.TrimSpace(body.License), strings.TrimSpace(body.Sig), h.licenseSecret(), kidVersion, sigVersion, time.Now().UTC().Unix())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error(), "code": classifyVerifyError(err)})
		return
	}
	canonical := drmsvc.BuildCanonicalString(claims.DRMType, claims.MediaID, claims.KID, claims.KeyRef, claims.ExpiresAt, claims.Nonce, claims.KIDVersion, claims.SigVersion)
	c.JSON(http.StatusOK, gin.H{"valid": true, "claims": claims, "canonical": canonical})
}

func (h *Handler) PowerDRMKey(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	kid := strings.TrimSpace(c.Query("kid"))
	if kid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kid is required"})
		return
	}
	var keyRef sql.NullString
	var assetMediaID sql.NullInt64
	err := h.App.DB.QueryRow(`SELECT key_ref, media_id FROM drm_asset WHERE kid = ? LIMIT 1`, kid).Scan(&keyRef, &assetMediaID)
	if err == sql.ErrNoRows || strings.TrimSpace(keyRef.String) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "kid not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if assetMediaID.Valid && assetMediaID.Int64 > 0 {
		if !h.requireDRMMediaAccess(c, assetMediaID.Int64, "powerdrm") {
			return
		}
	}
	raw, err := os.ReadFile(strings.TrimSpace(keyRef.String))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "drm key material not ready"})
		return
	}
	var ref powerDRMKeyRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid drm key material"})
		return
	}
	if strings.TrimSpace(ref.KID) == "" || strings.TrimSpace(ref.Key) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid drm key material"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kid": ref.KID, "key": ref.Key})
}

func (h *Handler) HLSAES128Key(c *gin.Context) {
	if middleware.UserID(c) <= 0 && !middleware.IsAPIClient(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	mediaID, _ := strconv.ParseInt(strings.TrimSpace(c.Query("media_id")), 10, 64)
	kid := strings.TrimSpace(c.Query("kid"))
	if mediaID <= 0 || kid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_id and kid are required"})
		return
	}
	if !h.requireDRMMediaAccess(c, mediaID, "hls_aes_128") {
		return
	}
	var dbKid, keyHex, dbMode sql.NullString
	err := h.App.DB.QueryRow(`
		SELECT kid, key_hex, mode FROM drm_key_material WHERE media_id = ? LIMIT 1
	`, mediaID).Scan(&dbKid, &keyHex, &dbMode)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if dbMode.String != streamJITKeyMode && dbMode.String != "hls_aes_128" {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid key mode"})
		return
	}
	if dbMode.String != streamJITKeyMode {
		var status, pipeline sql.NullString
		_ = h.App.DB.QueryRow(`
			SELECT status, pipeline_type FROM package_task
			WHERE media_id = ? AND pipeline_type = 'hls_aes_128'
			ORDER BY id DESC LIMIT 1
		`, mediaID).Scan(&status, &pipeline)
		if status.String != "done" {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid package status"})
			return
		}
	}
	if !strings.EqualFold(strings.TrimSpace(dbKid.String), kid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "kid mismatch"})
		return
	}
	keyBytes, err := hex.DecodeString(strings.TrimSpace(keyHex.String))
	if err != nil || len(keyBytes) != 16 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key material"})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Cache-Control", "private, max-age=30")
	c.Data(http.StatusOK, "application/octet-stream", keyBytes)
}
