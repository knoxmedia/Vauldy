package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/doctrans"
)

type SystemOptionsDocTrans struct {
	Enabled         bool     `json:"enabled"`
	EngineOrder     []string `json:"engine_order"`
	LibreOfficePath string   `json:"libreoffice_path"`
	SofficePath     string   `json:"soffice_path"`
	OfficePath      string   `json:"office_path"`
	WPSPath         string   `json:"wps_path"`
	CacheDir        string   `json:"cache_dir"`
	CacheTTLDays    int      `json:"cache_ttl_days"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
}

type docTransTestBody struct {
	DocTrans *SystemOptionsDocTrans `json:"doc_trans"`
}

type docTransInstallResult struct {
	OK       bool                   `json:"ok"`
	Message  string                 `json:"message"`
	DocTrans *SystemOptionsDocTrans `json:"doc_trans,omitempty"`
	Engines  []doctrans.EngineStatus `json:"engines,omitempty"`
}

func defaultDocTransOptions() SystemOptionsDocTrans {
	return SystemOptionsDocTrans{
		Enabled:         true,
		EngineOrder:     []string{"office", "wps", "libreoffice"},
		LibreOfficePath: doctrans.DefaultSofficeRel(),
		SofficePath:     doctrans.DefaultSofficeRel(),
		CacheDir:        "",
		CacheTTLDays:    30,
		TimeoutSeconds:  180,
	}
}

func docTransFromConfig(cfg *config.Config) SystemOptionsDocTrans {
	def := defaultDocTransOptions()
	if cfg == nil {
		return def
	}
	dt := cfg.DocTrans
	order := dt.EngineOrder
	if len(order) == 0 {
		order = def.EngineOrder
	}
	lo := strings.TrimSpace(dt.LibreOfficePath)
	if lo == "" {
		lo = strings.TrimSpace(dt.SofficePath)
	}
	out := SystemOptionsDocTrans{
		Enabled:         cfg.DocTransEnabled(),
		EngineOrder:     order,
		LibreOfficePath: lo,
		SofficePath:     lo,
		OfficePath:      strings.TrimSpace(dt.OfficePath),
		WPSPath:         strings.TrimSpace(dt.WPSPath),
		CacheDir:        strings.TrimSpace(dt.CacheDir),
		CacheTTLDays:    cfg.DocTransCacheTTLDays(),
		TimeoutSeconds:  cfg.DocTransTimeoutSeconds(),
	}
	if out.LibreOfficePath == "" {
		out.LibreOfficePath = def.LibreOfficePath
		out.SofficePath = def.SofficePath
	}
	return normalizeDocTransOptions(out)
}

func docTransToConfig(o SystemOptionsDocTrans) config.DocTransConfig {
	o = normalizeDocTransOptions(o)
	enabled := o.Enabled
	order := make([]string, 0, len(o.EngineOrder))
	for _, k := range doctrans.NormalizeEngineOrder(o.EngineOrder) {
		order = append(order, string(k))
	}
	lo := o.LibreOfficePath
	if lo == "" {
		lo = o.SofficePath
	}
	return config.DocTransConfig{
		Enabled:         &enabled,
		EngineOrder:     order,
		LibreOfficePath: lo,
		SofficePath:     lo,
		OfficePath:      o.OfficePath,
		WPSPath:         o.WPSPath,
		CacheDir:        o.CacheDir,
		CacheTTLDays:    o.CacheTTLDays,
		TimeoutSeconds:  o.TimeoutSeconds,
	}
}

func normalizeDocTransOptions(o SystemOptionsDocTrans) SystemOptionsDocTrans {
	o.LibreOfficePath = strings.TrimSpace(o.LibreOfficePath)
	o.SofficePath = strings.TrimSpace(o.SofficePath)
	o.OfficePath = strings.TrimSpace(o.OfficePath)
	o.WPSPath = strings.TrimSpace(o.WPSPath)
	o.CacheDir = strings.TrimSpace(o.CacheDir)
	def := defaultDocTransOptions()
	if o.LibreOfficePath == "" {
		o.LibreOfficePath = def.LibreOfficePath
	}
	if o.SofficePath == "" {
		o.SofficePath = o.LibreOfficePath
	}
	if len(o.EngineOrder) == 0 {
		o.EngineOrder = def.EngineOrder
	} else {
		norm := make([]string, 0, len(o.EngineOrder))
		for _, k := range doctrans.NormalizeEngineOrder(o.EngineOrder) {
			norm = append(norm, string(k))
		}
		o.EngineOrder = norm
	}
	if o.CacheTTLDays <= 0 {
		o.CacheTTLDays = def.CacheTTLDays
	}
	if o.TimeoutSeconds <= 0 {
		o.TimeoutSeconds = def.TimeoutSeconds
	}
	if o.TimeoutSeconds > 600 {
		o.TimeoutSeconds = 600
	}
	return o
}

func fillDocTransDefaults(o *SystemOptionsDocTrans, def SystemOptionsDocTrans) {
	if o == nil {
		return
	}
	if len(o.EngineOrder) == 0 {
		o.EngineOrder = def.EngineOrder
	}
	if strings.TrimSpace(o.LibreOfficePath) == "" {
		o.LibreOfficePath = def.LibreOfficePath
	}
	if strings.TrimSpace(o.SofficePath) == "" {
		o.SofficePath = o.LibreOfficePath
	}
	if o.CacheTTLDays <= 0 {
		o.CacheTTLDays = def.CacheTTLDays
	}
	if o.TimeoutSeconds <= 0 {
		o.TimeoutSeconds = def.TimeoutSeconds
	}
}

func (h *Handler) applyDocTransConfig(o SystemOptionsDocTrans) error {
	if h == nil || h.App == nil || h.App.Config == nil {
		return fmt.Errorf("config unavailable")
	}
	dt := docTransToConfig(o)
	cfgPath := strings.TrimSpace(h.App.ConfigPath)
	if cfgPath == "" {
		return fmt.Errorf("config path not set")
	}
	if err := config.SaveDocTrans(cfgPath, dt); err != nil {
		return err
	}
	h.App.Config.DocTrans = dt
	return nil
}

func (h *Handler) resolveDocTransForTest(body *docTransTestBody) (string, config.DocTransConfig) {
	mediaRoot := ""
	if h != nil && h.App != nil {
		if p, err := h.mediaRoot(); err == nil {
			mediaRoot = p
		}
	}
	if body != nil && body.DocTrans != nil {
		return mediaRoot, docTransToConfig(normalizeDocTransOptions(*body.DocTrans))
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		return mediaRoot, h.App.Config.DocTrans
	}
	return mediaRoot, docTransToConfig(defaultDocTransOptions())
}

func (h *Handler) docConverter() (*doctrans.Converter, error) {
	if h == nil || h.App == nil || h.App.Config == nil {
		return nil, fmt.Errorf("config unavailable")
	}
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		return nil, err
	}
	preview := h.App.Config.Data.Preview
	return doctrans.NewConverter(mediaRoot, preview, h.App.Config.DocTrans), nil
}

// TestSystemOptionsDocTrans checks all engines and reports priority.
func (h *Handler) TestSystemOptionsDocTrans(c *gin.Context) {
	var body docTransTestBody
	_ = c.ShouldBindJSON(&body)
	mediaRoot, cfg := h.resolveDocTransForTest(&body)
	result := doctrans.CheckConfig(c.Request.Context(), mediaRoot, cfg)
	c.JSON(http.StatusOK, result)
}

// InstallSystemOptionsDocTrans detects all engines and writes config.
func (h *Handler) InstallSystemOptionsDocTrans(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	deploy, err := doctrans.Install(mediaRoot)
	current := docTransFromConfig(h.App.Config)
	if len(deploy.EngineOrder) > 0 {
		current.EngineOrder = deploy.EngineOrder
	}
	if deploy.LibreOfficePath != "" {
		current.LibreOfficePath = deploy.LibreOfficePath
		current.SofficePath = deploy.LibreOfficePath
	}
	if deploy.OfficePath != "" {
		current.OfficePath = deploy.OfficePath
	}
	if deploy.WPSPath != "" {
		current.WPSPath = deploy.WPSPath
	}
	if err := h.applyDocTransConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := doctrans.CheckConfig(c.Request.Context(), mediaRoot, docTransToConfig(current))
	msg := check.Message
	if err != nil {
		msg = err.Error() + "；" + msg
	}
	c.JSON(http.StatusOK, docTransInstallResult{
		OK:       check.OK,
		Message:  msg,
		DocTrans: &current,
		Engines:  check.Engines,
	})
}

// InstallLibreOfficeDocTrans one-click installs LibreOffice.
func (h *Handler) InstallLibreOfficeDocTrans(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()

	deploy, err := doctrans.InstallLibreOffice(ctx, mediaRoot)
	current := docTransFromConfig(h.App.Config)
	if deploy.LibreOfficePath != "" {
		current.LibreOfficePath = deploy.LibreOfficePath
		current.SofficePath = deploy.LibreOfficePath
	}
	if err := h.applyDocTransConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := doctrans.CheckConfig(ctx, mediaRoot, docTransToConfig(current))
	msg := "LibreOffice 安装完成"
	if check.Engines != nil {
		for _, e := range check.Engines {
			if e.Kind == doctrans.EngineLibreOffice {
				if e.Available {
					msg = "LibreOffice 已就绪: " + e.Path
				} else {
					msg = e.Message
				}
			}
		}
	}
	if err != nil {
		msg = err.Error()
	}
	c.JSON(http.StatusOK, docTransInstallResult{
		OK:       err == nil && check.OK,
		Message:  msg,
		DocTrans: &current,
		Engines:  check.Engines,
	})
}
