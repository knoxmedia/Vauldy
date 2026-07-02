package doctrans

import (
	"strings"

	"knox-media/internal/config"
)

// EngineKind identifies a document conversion backend.
type EngineKind string

const (
	EngineOffice       EngineKind = "office"
	EngineWPS          EngineKind = "wps"
	EngineLibreOffice  EngineKind = "libreoffice"
)

var defaultEngineOrder = []EngineKind{EngineOffice, EngineWPS, EngineLibreOffice}

// EngineStatus describes detect/availability of one engine.
type EngineStatus struct {
	Kind      EngineKind `json:"kind"`
	Label     string     `json:"label"`
	Available bool       `json:"available"`
	Path      string     `json:"path,omitempty"`
	Version   string     `json:"version,omitempty"`
	Message   string     `json:"message,omitempty"`
}

func engineLabel(k EngineKind) string {
	switch k {
	case EngineOffice:
		return "Microsoft Office"
	case EngineWPS:
		return "WPS Office"
	case EngineLibreOffice:
		return "LibreOffice"
	default:
		return string(k)
	}
}

// NormalizeEngineOrder deduplicates and validates engine priority list.
func NormalizeEngineOrder(raw []string) []EngineKind {
	if len(raw) == 0 {
		out := append([]EngineKind(nil), defaultEngineOrder...)
		return out
	}
	seen := map[EngineKind]struct{}{}
	out := make([]EngineKind, 0, 3)
	for _, r := range raw {
		k := EngineKind(strings.ToLower(strings.TrimSpace(r)))
		switch k {
		case EngineOffice, EngineWPS, EngineLibreOffice:
		default:
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, k := range defaultEngineOrder {
		if _, ok := seen[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

func engineOrderFromConfig(cfg config.DocTransConfig) []EngineKind {
	return NormalizeEngineOrder(cfg.EngineOrder)
}

func libreOfficePath(cfg config.DocTransConfig) string {
	if p := strings.TrimSpace(cfg.LibreOfficePath); p != "" {
		return p
	}
	return strings.TrimSpace(cfg.SofficePath)
}
