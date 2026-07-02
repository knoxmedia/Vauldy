package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/jit/hwenc"
	"knox-media/internal/subtitle"
)

// SystemOptionsJSON is persisted in system_options.options_json (single row id=1).
// Recognition (ASR/OCR) and photo_classify are stored in config.yml.
type SystemOptionsJSON struct {
	General       SystemOptionsGeneral       `json:"general"`
	Playback      SystemOptionsPlayback      `json:"playback"`
	Transcoder    SystemOptionsTranscoder    `json:"transcoder"`
	Recognition   SystemOptionsRecognition   `json:"recognition"`
	PhotoClassify SystemOptionsPhotoClassify `json:"photo_classify"`
	PhotoFace     SystemOptionsPhotoFace     `json:"photo_face"`
	DocTrans      SystemOptionsDocTrans      `json:"doc_trans"`
}

type systemOptionsGetResponse struct {
	SystemOptionsJSON
	AvailableHardwareAcceleration []string `json:"available_hardware_acceleration"`
}

type SystemOptionsRecognition struct {
	ASR          SystemOptionsASR `json:"asr"`
	OCR          SystemOptionsOCR `json:"ocr"`
	AIProofread  bool             `json:"ai_proofread"`
}

type SystemOptionsASR struct {
	AutoOnScan  bool     `json:"auto_on_scan"`
	Provider    string   `json:"provider"`
	WhisperPath string   `json:"whisper_path"`
	ExtraArgs   []string `json:"extra_args"`
	Shell       string   `json:"shell"`
}

type SystemOptionsOCR struct {
	Enabled        bool   `json:"enabled"`
	TesseractPath  string `json:"tesseract_path"`
	TessdataPrefix string `json:"tessdata_prefix"`
	Languages      string `json:"languages"`
	PythonPath     string `json:"python_path"`
	ScriptPath     string `json:"script_path"`
	PgsripPath     string `json:"pgsrip_path"`
	MkvextractPath string `json:"mkvextract_path"`
	MkvmergePath   string `json:"mkvmerge_path"`
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
	HardwareAcceleration          string `json:"hardware_acceleration"`
	EnableHardwareEncoding        bool   `json:"enable_hardware_encoding"`
	DisableVideoStreamTranscoding bool   `json:"disable_video_stream_transcoding"`
	MaxCPUConcurrent              string `json:"max_cpu_concurrent"`
	MaxBackgroundConcurrent       string `json:"max_background_concurrent"`
}

func defaultSystemOptions() SystemOptionsJSON {
	return SystemOptionsJSON{
		General: SystemOptionsGeneral{
			DisplayLanguage:         "zh-CN",
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
			HardwareAcceleration:          "none",
			EnableHardwareEncoding:        false,
			DisableVideoStreamTranscoding: false,
			MaxCPUConcurrent:              "unlimited",
			MaxBackgroundConcurrent:       "1",
		},
		Recognition: defaultRecognitionOptions(),
		PhotoClassify: defaultPhotoClassifyOptions(),
		PhotoFace:     defaultPhotoFaceOptions(),
		DocTrans:      defaultDocTransOptions(),
	}
}

func defaultRecognitionOptions() SystemOptionsRecognition {
	return SystemOptionsRecognition{
		ASR: SystemOptionsASR{
			AutoOnScan:  true,
			Provider:    "none",
			WhisperPath: "whisper",
			ExtraArgs:   nil,
			Shell:       "",
		},
		OCR: SystemOptionsOCR{
			Enabled:        false,
			TesseractPath:  "tesseract",
			TessdataPrefix: "",
			Languages:      "chi_sim+eng",
			PythonPath:     "",
			ScriptPath:     "tools/subtitle_ocr/bitmap_subtitle_ocr.py",
			PgsripPath:     "",
			MkvextractPath: "",
			MkvmergePath:   "",
		},
		AIProofread: true,
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
	if strings.TrimSpace(o.Transcoder.HardwareAcceleration) == "" {
		o.Transcoder.HardwareAcceleration = def.Transcoder.HardwareAcceleration
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
	fillRecognitionDefaults(&o.Recognition, def.Recognition)
	fillPhotoClassifyDefaults(&o.PhotoClassify, def.PhotoClassify)
	fillPhotoFaceDefaults(&o.PhotoFace, def.PhotoFace)
	fillDocTransDefaults(&o.DocTrans, def.DocTrans)
}

func fillRecognitionDefaults(o *SystemOptionsRecognition, def SystemOptionsRecognition) {
	if o == nil {
		return
	}
	if strings.TrimSpace(o.ASR.Provider) == "" {
		o.ASR.Provider = def.ASR.Provider
	}
	if strings.TrimSpace(o.ASR.WhisperPath) == "" {
		o.ASR.WhisperPath = def.ASR.WhisperPath
	}
	if strings.TrimSpace(o.OCR.TesseractPath) == "" {
		o.OCR.TesseractPath = def.OCR.TesseractPath
	}
	if strings.TrimSpace(o.OCR.Languages) == "" {
		o.OCR.Languages = def.OCR.Languages
	}
	if strings.TrimSpace(o.OCR.ScriptPath) == "" {
		o.OCR.ScriptPath = def.OCR.ScriptPath
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
	// Allow both new BCP-47 codes (zh-CN, zh-TW) and legacy aliases that the
	// older clients may still send. They map to a canonical code so the
	// admin UI dropdown stays consistent.
	canonicalLang := map[string]string{
		"zh-CN":   "zh-CN",
		"zh-cn":   "zh-CN",
		"zh-Hans": "zh-CN",
		"zh-hans": "zh-CN",
		"zh":      "zh-CN",
		"zh-TW":   "zh-TW",
		"zh-tw":   "zh-TW",
		"zh-Hant": "zh-TW",
		"zh-hant": "zh-TW",
		"zh-HK":   "zh-TW",
		"zh-hk":   "zh-TW",
		"en":      "en",
		"en-US":   "en",
		"en-us":   "en",
		"en-GB":   "en",
		"en-gb":   "en",
		"ja":      "ja",
		"ja-JP":   "ja",
		"ja-jp":   "ja",
		"ko":      "ko",
		"ko-KR":   "ko",
		"ko-kr":   "ko",
	}
	if canonical, ok := canonicalLang[o.General.DisplayLanguage]; ok {
		o.General.DisplayLanguage = canonical
	} else {
		o.General.DisplayLanguage = "zh-CN"
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
	switch o.Transcoder.HardwareAcceleration {
	case "none", "amf", "nvenc", "qsv", "vaapi":
	default:
		o.Transcoder.HardwareAcceleration = "none"
	}
	if o.Transcoder.HardwareAcceleration == "none" {
		o.Transcoder.EnableHardwareEncoding = false
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
	o.Recognition = normalizeRecognitionOptions(o.Recognition)
	o.PhotoClassify = normalizePhotoClassifyOptions(o.PhotoClassify)
	o.PhotoFace = normalizePhotoFaceOptions(o.PhotoFace)
	o.DocTrans = normalizeDocTransOptions(o.DocTrans)
	return o
}

func (h *Handler) availableHardwareAcceleration() []string {
	if h == nil || h.App == nil {
		return nil
	}
	if len(h.App.AvailableHardwareAcceleration) > 0 {
		return append([]string(nil), h.App.AvailableHardwareAcceleration...)
	}
	if h.App.Config != nil {
		return hwenc.ListAvailableHardwareAcceleration(h.App.Config.FFmpeg.FFmpegPath)
	}
	return nil
}

func clampTranscoderHardware(t *SystemOptionsTranscoder, available []string) {
	if t == nil {
		return
	}
	if t.HardwareAcceleration == "none" {
		t.EnableHardwareEncoding = false
		return
	}
	for _, a := range available {
		if a == t.HardwareAcceleration {
			return
		}
	}
	t.HardwareAcceleration = "none"
	t.EnableHardwareEncoding = false
}

func normalizeRecognitionOptions(r SystemOptionsRecognition) SystemOptionsRecognition {
	switch strings.ToLower(strings.TrimSpace(r.ASR.Provider)) {
	case "none", "whisper_cli", "shell":
		r.ASR.Provider = strings.ToLower(strings.TrimSpace(r.ASR.Provider))
	default:
		r.ASR.Provider = "none"
	}
	r.ASR.WhisperPath = strings.TrimSpace(r.ASR.WhisperPath)
	if r.ASR.WhisperPath == "" {
		r.ASR.WhisperPath = "whisper"
	}
	r.ASR.Shell = strings.TrimSpace(r.ASR.Shell)
	if r.ASR.ExtraArgs == nil {
		r.ASR.ExtraArgs = []string{}
	}
	cleanArgs := make([]string, 0, len(r.ASR.ExtraArgs))
	for _, a := range r.ASR.ExtraArgs {
		a = strings.TrimSpace(a)
		if a != "" {
			cleanArgs = append(cleanArgs, a)
		}
	}
	r.ASR.ExtraArgs = cleanArgs

	r.OCR.TesseractPath = strings.TrimSpace(r.OCR.TesseractPath)
	if r.OCR.TesseractPath == "" {
		r.OCR.TesseractPath = "tesseract"
	}
	r.OCR.TessdataPrefix = strings.TrimSpace(r.OCR.TessdataPrefix)
	r.OCR.Languages = strings.TrimSpace(r.OCR.Languages)
	if r.OCR.Languages == "" {
		r.OCR.Languages = "chi_sim+eng"
	}
	r.OCR.PythonPath = strings.TrimSpace(r.OCR.PythonPath)
	r.OCR.ScriptPath = strings.TrimSpace(r.OCR.ScriptPath)
	if r.OCR.ScriptPath == "" {
		r.OCR.ScriptPath = defaultRecognitionOptions().OCR.ScriptPath
	}
	r.OCR.PgsripPath = strings.TrimSpace(r.OCR.PgsripPath)
	r.OCR.MkvextractPath = strings.TrimSpace(r.OCR.MkvextractPath)
	r.OCR.MkvmergePath = strings.TrimSpace(r.OCR.MkvmergePath)
	return r
}

func recognitionFromConfig(cfg *config.Config) SystemOptionsRecognition {
	def := defaultRecognitionOptions()
	if cfg == nil {
		return def
	}
	return SystemOptionsRecognition{
		ASR: SystemOptionsASR{
			AutoOnScan:  cfg.SubtitleAutoOnScan(),
			Provider:    cfg.Subtitle.ASR.Provider,
			WhisperPath: cfg.Subtitle.ASR.WhisperPath,
			ExtraArgs:   append([]string(nil), cfg.Subtitle.ASR.ExtraArgs...),
			Shell:       cfg.Subtitle.ASR.Shell,
		},
		OCR: SystemOptionsOCR{
			Enabled:        cfg.Subtitle.GraphicalOCR.Enabled,
			TesseractPath:  cfg.Subtitle.GraphicalOCR.TesseractPath,
			TessdataPrefix: cfg.Subtitle.GraphicalOCR.TessdataPrefix,
			Languages:      cfg.Subtitle.GraphicalOCR.Languages,
			PythonPath:     cfg.Subtitle.GraphicalOCR.PythonPath,
			ScriptPath:     cfg.Subtitle.GraphicalOCR.ScriptPath,
			PgsripPath:     cfg.Subtitle.GraphicalOCR.PgsripPath,
			MkvextractPath: cfg.Subtitle.GraphicalOCR.MkvextractPath,
			MkvmergePath:   cfg.Subtitle.GraphicalOCR.MkvmergePath,
		},
		AIProofread: cfg.SubtitleAIProofreadEnabled(),
	}
}

func recognitionToConfig(r SystemOptionsRecognition) (config.ASRConfig, config.GraphicalOCRConfig) {
	r = normalizeRecognitionOptions(r)
	asr := config.ASRConfig{
		Provider:    r.ASR.Provider,
		WhisperPath: r.ASR.WhisperPath,
		ExtraArgs:   append([]string(nil), r.ASR.ExtraArgs...),
		Shell:       r.ASR.Shell,
	}
	ocr := config.GraphicalOCRConfig{
		Enabled:        r.OCR.Enabled,
		TesseractPath:  r.OCR.TesseractPath,
		TessdataPrefix: r.OCR.TessdataPrefix,
		Languages:      r.OCR.Languages,
		PythonPath:     r.OCR.PythonPath,
		ScriptPath:     r.OCR.ScriptPath,
		PgsripPath:     r.OCR.PgsripPath,
		MkvextractPath: r.OCR.MkvextractPath,
		MkvmergePath:   r.OCR.MkvmergePath,
	}
	return asr, ocr
}

func (h *Handler) applyRecognitionConfig(r SystemOptionsRecognition) error {
	if h == nil || h.App == nil || h.App.Config == nil {
		return fmt.Errorf("config unavailable")
	}
	asr, ocr := recognitionToConfig(r)
	cfgPath := strings.TrimSpace(h.App.ConfigPath)
	if cfgPath == "" {
		return fmt.Errorf("config path not set")
	}
	if err := config.SaveSubtitleRecognition(cfgPath, r.ASR.AutoOnScan, asr, ocr, r.AIProofread); err != nil {
		return err
	}
	autoOnScan := r.ASR.AutoOnScan
	h.App.Config.Subtitle.AutoOnScan = &autoOnScan
	h.App.Config.Subtitle.ASR = asr
	h.App.Config.Subtitle.GraphicalOCR = ocr
	aiProofread := r.AIProofread
	h.App.Config.Subtitle.AIProofread = &aiProofread
	if h.Subtitle != nil {
		h.Subtitle.ApplyRecognition(subtitle.ASRConfig{
			Provider:    asr.Provider,
			WhisperPath: asr.WhisperPath,
			ExtraArgs:   append([]string(nil), asr.ExtraArgs...),
			Shell:       asr.Shell,
		}, subtitle.OCRConfig{
			Enabled:        ocr.Enabled,
			TesseractPath:  ocr.TesseractPath,
			TessdataPrefix: ocr.TessdataPrefix,
			Languages:      ocr.Languages,
			PythonPath:     ocr.PythonPath,
			ScriptPath:     ocr.ScriptPath,
			PgsripPath:     ocr.PgsripPath,
			MkvextractPath: ocr.MkvextractPath,
			MkvmergePath:   ocr.MkvmergePath,
		})
		h.Subtitle.ApplyAIProofread(aiProofread)
	}
	return nil
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
	if h.App != nil && h.App.Config != nil {
		opts.Recognition = normalizeRecognitionOptions(recognitionFromConfig(h.App.Config))
		opts.PhotoClassify = photoClassifyFromConfig(h.App.Config)
		opts.PhotoFace = photoFaceFromConfig(h.App.Config)
		opts.DocTrans = docTransFromConfig(h.App.Config)
	}
	available := h.availableHardwareAcceleration()
	clampTranscoderHardware(&opts.Transcoder, available)
	c.JSON(http.StatusOK, systemOptionsGetResponse{
		SystemOptionsJSON:             opts,
		AvailableHardwareAcceleration: available,
	})
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
	available := h.availableHardwareAcceleration()
	merged := normalizeSystemOptions(body)
	clampTranscoderHardware(&merged.Transcoder, available)
	if err := h.applyRecognitionConfig(merged.Recognition); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存识别配置失败: " + err.Error()})
		return
	}
	if err := h.applyPhotoClassifyConfig(merged.PhotoClassify); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存智能分类配置失败: " + err.Error()})
		return
	}
	if err := h.applyPhotoFaceConfig(merged.PhotoFace); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存人脸检测配置失败: " + err.Error()})
		return
	}
	if err := h.applyDocTransConfig(merged.DocTrans); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文档转换配置失败: " + err.Error()})
		return
	}
	if h.App != nil && h.App.Config != nil {
		merged.Recognition = normalizeRecognitionOptions(recognitionFromConfig(h.App.Config))
		merged.PhotoClassify = photoClassifyFromConfig(h.App.Config)
		merged.PhotoFace = photoFaceFromConfig(h.App.Config)
		merged.DocTrans = docTransFromConfig(h.App.Config)
	}
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

type recognitionTestBody struct {
	ASR *SystemOptionsASR `json:"asr"`
	OCR *SystemOptionsOCR `json:"ocr"`
}

func (h *Handler) resolveASRForTest(body *recognitionTestBody) subtitle.ASRConfig {
	if body != nil && body.ASR != nil {
		r := normalizeRecognitionOptions(SystemOptionsRecognition{ASR: *body.ASR})
		return subtitle.ASRConfig{
			Provider:    r.ASR.Provider,
			WhisperPath: r.ASR.WhisperPath,
			ExtraArgs:   append([]string(nil), r.ASR.ExtraArgs...),
			Shell:       r.ASR.Shell,
		}
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		a := h.App.Config.Subtitle.ASR
		return subtitle.ASRConfig{
			Provider:    a.Provider,
			WhisperPath: a.WhisperPath,
			ExtraArgs:   append([]string(nil), a.ExtraArgs...),
			Shell:       a.Shell,
		}
	}
	return subtitle.ASRConfig{Provider: "none"}
}

func (h *Handler) resolveOCRForTest(body *recognitionTestBody) subtitle.OCRConfig {
	if body != nil && body.OCR != nil {
		r := normalizeRecognitionOptions(SystemOptionsRecognition{OCR: *body.OCR})
		return subtitle.OCRConfig{
			Enabled:        r.OCR.Enabled,
			TesseractPath:  r.OCR.TesseractPath,
			TessdataPrefix: r.OCR.TessdataPrefix,
			Languages:      r.OCR.Languages,
			PythonPath:     r.OCR.PythonPath,
			ScriptPath:     r.OCR.ScriptPath,
			PgsripPath:     r.OCR.PgsripPath,
			MkvextractPath: r.OCR.MkvextractPath,
			MkvmergePath:   r.OCR.MkvmergePath,
		}
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		o := h.App.Config.Subtitle.GraphicalOCR
		return subtitle.OCRConfig{
			Enabled:        o.Enabled,
			TesseractPath:  o.TesseractPath,
			TessdataPrefix: o.TessdataPrefix,
			Languages:      o.Languages,
			PythonPath:     o.PythonPath,
			ScriptPath:     o.ScriptPath,
			PgsripPath:     o.PgsripPath,
			MkvextractPath: o.MkvextractPath,
			MkvmergePath:   o.MkvmergePath,
		}
	}
	return subtitle.OCRConfig{}
}

// TestSystemOptionsASR checks ASR connectivity (optional body overrides saved config).
func (h *Handler) TestSystemOptionsASR(c *gin.Context) {
	var body recognitionTestBody
	_ = c.ShouldBindJSON(&body)
	result := subtitle.CheckASRConfig(c.Request.Context(), h.resolveASRForTest(&body))
	c.JSON(http.StatusOK, result)
}

// TestSystemOptionsOCR checks OCR tool chain (optional body overrides saved config).
func (h *Handler) TestSystemOptionsOCR(c *gin.Context) {
	var body recognitionTestBody
	_ = c.ShouldBindJSON(&body)
	mediaRoot := ""
	if h != nil {
		if p, err := h.mediaRoot(); err == nil {
			mediaRoot = p
		}
	}
	result := subtitle.CheckOCRConfig(c.Request.Context(), mediaRoot, h.resolveOCRForTest(&body))
	c.JSON(http.StatusOK, result)
}
