package doctrans

import (
	"context"
	"fmt"

	"knox-media/internal/config"
)

// TestResult is returned by document conversion connectivity checks.
type TestResult struct {
	OK            bool           `json:"ok"`
	Message       string         `json:"message"`
	ActiveEngine  string         `json:"active_engine,omitempty"`
	Engines       []EngineStatus `json:"engines"`
	SofficePath   string         `json:"soffice_path,omitempty"`
	Version       string         `json:"version,omitempty"`
}

// CheckConfig verifies engines per priority and reports all statuses.
func CheckConfig(ctx context.Context, mediaRoot string, cfg config.DocTransConfig) TestResult {
	_ = ctx
	if !docTransEnabled(cfg) {
		return TestResult{OK: false, Message: "文档转换未启用，请在系统选项中开启"}
	}
	_ = ensureConvertScript(mediaRoot)
	engines := DetectEngines(mediaRoot, cfg)
	res := TestResult{Engines: engines}
	kind, active := firstAvailableEngine(mediaRoot, cfg)
	if kind == "" {
		res.OK = false
		res.Message = "没有可用的转换引擎。请安装 Microsoft Office、WPS 或 LibreOffice，或调整引擎优先级"
		return res
	}
	res.OK = true
	res.ActiveEngine = string(kind)
	res.Message = fmt.Sprintf("将使用 %s 进行转换", active.Label)
	if kind == EngineLibreOffice {
		res.SofficePath = active.Path
		res.Version = active.Version
	}
	return res
}

// Deploy holds recommended paths after install/detect.
type Deploy struct {
	Enabled         bool
	EngineOrder     []string
	LibreOfficePath string
	SofficePath     string
	OfficePath      string
	WPSPath         string
	CacheDir        string
}

// DetectLibreOffice finds LibreOffice installation paths.
func DetectLibreOffice(mediaRoot string) Deploy {
	conv := NewConverter(mediaRoot, "", config.DocTransConfig{})
	p := conv.resolveLibreOffice()
	rel := relIfUnder(mediaRoot, p)
	if rel == "" {
		rel = p
	}
	if rel == "" {
		rel = DefaultSofficeRel()
	}
	return Deploy{
		Enabled:         true,
		LibreOfficePath: rel,
		SofficePath:     rel,
	}
}

func DeployToConfig(d Deploy) config.DocTransConfig {
	enabled := d.Enabled
	order := d.EngineOrder
	if len(order) == 0 {
		order = []string{string(EngineOffice), string(EngineWPS), string(EngineLibreOffice)}
	}
	lo := d.LibreOfficePath
	if lo == "" {
		lo = d.SofficePath
	}
	return config.DocTransConfig{
		Enabled:         &enabled,
		EngineOrder:     order,
		LibreOfficePath: lo,
		SofficePath:     lo,
		OfficePath:      d.OfficePath,
		WPSPath:         d.WPSPath,
		CacheDir:        d.CacheDir,
		CacheTTLDays:    30,
		TimeoutSeconds:  180,
	}
}

func relIfUnder(base, abs string) string {
	return relPathUnder(base, abs)
}
