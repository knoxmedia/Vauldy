package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// SystemOptionsJSON is persisted in system_options.options_json (single row id=1).
type SystemOptionsJSON struct {
	General    SystemOptionsGeneral    `json:"general"`
	Playback   SystemOptionsPlayback   `json:"playback"`
	Transcoder SystemOptionsTranscoder `json:"transcoder"`
}

type SystemOptionsGeneral struct {
	DisplayLanguage         string `json:"display_language"`
	StartOnBoot             bool   `json:"start_on_boot"`
	OpenBrowserOnFirstStart bool   `json:"open_browser_on_first_start"`
	MaintenanceMode         bool   `json:"maintenance_mode"`
	CachePath               string `json:"cache_path"`
	AutoUpdateEnabled       bool   `json:"auto_update_enabled"`
}

type SystemOptionsPlayback struct {
	HomeStreamQuality string `json:"home_stream_quality"`
	ScreenOrientation string `json:"screen_orientation"`
}

type SystemOptionsTranscoder struct {
	Quality                       string `json:"quality"`
	TempDir                       string `json:"temp_dir"`
	DownloadTempDir               string `json:"download_temp_dir"`
	ThrottleBufferSeconds         int    `json:"throttle_buffer_seconds"`
	BackgroundX264Preset          string `json:"background_x264_preset"`
	DisableVideoStreamTranscoding bool   `json:"disable_video_stream_transcoding"`
	MaxCPUConcurrent              string `json:"max_cpu_concurrent"`
	MaxBackgroundConcurrent       string `json:"max_background_concurrent"`
}

func defaultSystemOptions() SystemOptionsJSON {
	return SystemOptionsJSON{
		General: SystemOptionsGeneral{
			DisplayLanguage:         "zh-Hans",
			StartOnBoot:             false,
			OpenBrowserOnFirstStart: true,
			MaintenanceMode:         false,
			CachePath:               "",
			AutoUpdateEnabled:       false,
		},
		Playback: SystemOptionsPlayback{
			HomeStreamQuality: "auto",
			ScreenOrientation: "auto",
		},
		Transcoder: SystemOptionsTranscoder{
			Quality:                       "auto",
			TempDir:                       "",
			DownloadTempDir:               "",
			ThrottleBufferSeconds:         60,
			BackgroundX264Preset:          "veryfast",
			DisableVideoStreamTranscoding: false,
			MaxCPUConcurrent:              "unlimited",
			MaxBackgroundConcurrent:       "1",
		},
	}
}

func fillSystemOptionsDefaults(o *SystemOptionsJSON, def SystemOptionsJSON) {
	if o == nil {
		return
	}
	if strings.TrimSpace(o.General.DisplayLanguage) == "" {
		o.General.DisplayLanguage = def.General.DisplayLanguage
	}
	if strings.TrimSpace(o.Playback.HomeStreamQuality) == "" {
		o.Playback.HomeStreamQuality = def.Playback.HomeStreamQuality
	}
	if strings.TrimSpace(o.Playback.ScreenOrientation) == "" {
		o.Playback.ScreenOrientation = def.Playback.ScreenOrientation
	}
	if strings.TrimSpace(o.Transcoder.Quality) == "" {
		o.Transcoder.Quality = def.Transcoder.Quality
	}
	if strings.TrimSpace(o.Transcoder.BackgroundX264Preset) == "" {
		o.Transcoder.BackgroundX264Preset = def.Transcoder.BackgroundX264Preset
	}
	if o.Transcoder.ThrottleBufferSeconds <= 0 {
		o.Transcoder.ThrottleBufferSeconds = def.Transcoder.ThrottleBufferSeconds
	}
	if strings.TrimSpace(o.Transcoder.MaxCPUConcurrent) == "" {
		o.Transcoder.MaxCPUConcurrent = def.Transcoder.MaxCPUConcurrent
	}
	if strings.TrimSpace(o.Transcoder.MaxBackgroundConcurrent) == "" {
		o.Transcoder.MaxBackgroundConcurrent = def.Transcoder.MaxBackgroundConcurrent
	}
}

func homeStreamQualityValues() []string {
	var vals []string
	for _, mbps := range []int{200, 160, 140, 120, 100, 80, 60, 40} {
		vals = append(vals, fmt.Sprintf("4k-%dmbps", mbps))
	}
	for _, mbps := range []int{60, 50, 40, 30, 25, 20, 15, 12, 10, 8, 6, 5} {
		vals = append(vals, fmt.Sprintf("1080p-%dmbps", mbps))
	}
	for _, mbps := range []int{8, 6, 4, 3, 2} {
		vals = append(vals, fmt.Sprintf("720p-%dmbps", mbps))
	}
	for _, mbps := range []int{4, 3, 2} {
		vals = append(vals, fmt.Sprintf("480p-%dmbps", mbps))
	}
	vals = append(vals, "480p-1_5mbps")
	return vals
}

func normalizeSystemOptions(o SystemOptionsJSON) SystemOptionsJSON {
	allowedLang := map[string]struct{}{
		"zh-Hans": {}, "zh-Hant": {}, "en": {}, "ja": {}, "ko": {},
	}
	if _, ok := allowedLang[o.General.DisplayLanguage]; !ok {
		o.General.DisplayLanguage = "zh-Hans"
	}
	if o.Transcoder.ThrottleBufferSeconds < 1 {
		o.Transcoder.ThrottleBufferSeconds = 1
	}
	if o.Transcoder.ThrottleBufferSeconds > 600 {
		o.Transcoder.ThrottleBufferSeconds = 600
	}
	validStream := map[string]struct{}{"auto": {}}
	for _, v := range homeStreamQualityValues() {
		validStream[v] = struct{}{}
	}
	if _, ok := validStream[o.Playback.HomeStreamQuality]; !ok {
		o.Playback.HomeStreamQuality = "auto"
	}
	switch o.Playback.ScreenOrientation {
	case "auto", "lock_landscape", "device":
	default:
		o.Playback.ScreenOrientation = "auto"
	}
	switch o.Transcoder.Quality {
	case "auto", "max", "high", "medium", "low":
	default:
		o.Transcoder.Quality = "auto"
	}
	validPreset := map[string]struct{}{
		"ultrafast": {}, "superfast": {}, "veryfast": {}, "faster": {}, "fast": {}, "medium": {}, "slow": {}, "slower": {}, "veryslow": {},
	}
	if _, ok := validPreset[o.Transcoder.BackgroundX264Preset]; !ok {
		o.Transcoder.BackgroundX264Preset = "veryfast"
	}
	if o.Transcoder.MaxCPUConcurrent != "unlimited" && o.Transcoder.MaxCPUConcurrent != "" {
		ok := false
		for i := 1; i <= 16; i++ {
			if o.Transcoder.MaxCPUConcurrent == fmt.Sprintf("%d", i) {
				ok = true
				break
			}
		}
		if !ok {
			o.Transcoder.MaxCPUConcurrent = "unlimited"
		}
	}
	if o.Transcoder.MaxCPUConcurrent == "" {
		o.Transcoder.MaxCPUConcurrent = "unlimited"
	}
	if o.Transcoder.MaxBackgroundConcurrent == "" {
		o.Transcoder.MaxBackgroundConcurrent = "1"
	} else {
		ok := false
		for i := 1; i <= 8; i++ {
			if o.Transcoder.MaxBackgroundConcurrent == fmt.Sprintf("%d", i) {
				ok = true
				break
			}
		}
		if !ok {
			o.Transcoder.MaxBackgroundConcurrent = "1"
		}
	}
	return o
}

func decodeSystemOptions(raw string) SystemOptionsJSON {
	def := defaultSystemOptions()
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return def
	}
	var body SystemOptionsJSON
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return def
	}
	fillSystemOptionsDefaults(&body, def)
	return normalizeSystemOptions(body)
}

// GetSystemOptions returns merged server system options (admin).
func (h *Handler) GetSystemOptions(c *gin.Context) {
	if h == nil || h.App == nil || h.App.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	var raw sql.NullString
	if err := h.App.DB.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	opts := decodeSystemOptions(raw.String)
	c.JSON(http.StatusOK, opts)
}

// PutSystemOptions replaces system options (admin). Client should send the full document from GET after edits.
func (h *Handler) PutSystemOptions(c *gin.Context) {
	if h == nil || h.App == nil || h.App.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	var body SystemOptionsJSON
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fillSystemOptionsDefaults(&body, defaultSystemOptions())
	merged := normalizeSystemOptions(body)
	out, err := json.Marshal(merged)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(
		`UPDATE system_options SET options_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
		string(out),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "options": merged})
}
