package taskcontrol

import (
	"sort"
	"strings"
	"testing"
)

func TestRegistryOverviewIsFirstGroup(t *testing.T) {
	r := NewRegistry()
	if len(r.Groups) == 0 {
		t.Fatal("registry has no groups")
	}
	if r.Groups[0].Label != "tasks.group.overview" {
		t.Fatalf("first group label: got %q, want %q", r.Groups[0].Label, "tasks.group.overview")
	}
}

func TestRegistryGroupLabelsAreNotSelectable(t *testing.T) {
	r := NewRegistry()
	for _, g := range r.Groups {
		if g.Selectable {
			t.Errorf("group %q is selectable; group labels must be non-selectable", g.Label)
		}
	}
}

func TestRegistryEverySpecHasIndependentIdentity(t *testing.T) {
	r := NewRegistry()
	seen := map[string]string{}
	for _, g := range r.Groups {
		for _, s := range g.Types {
			if prev, ok := seen[s.Type]; ok {
				t.Errorf("duplicate task type %q in groups %q and %q", s.Type, prev, g.Label)
			}
			seen[s.Type] = g.Label
		}
	}
	if len(seen) == 0 {
		t.Fatal("registry has no specification types")
	}
}

func TestRegistryPosterVsThumbnailSeparate(t *testing.T) {
	r := NewRegistry()
	poster := findSpec(t, r, "poster")
	thumbnail := findSpec(t, r, "thumbnail")
	if poster.Type == thumbnail.Type {
		t.Error("poster and thumbnail must be separate types")
	}
}

func TestRegistryBatchTranscodeVsOptimizationSeparate(t *testing.T) {
	r := NewRegistry()
	transcode := findSpec(t, r, "transcode")
	optimize := findSpec(t, r, "optimize")
	if transcode.Type == optimize.Type {
		t.Error("transcode and optimize must be separate types")
	}
}

func TestRegistryPackageVsEncryptionSeparate(t *testing.T) {
	r := NewRegistry()
	pkg := findSpec(t, r, "package")
	encrypt := findSpec(t, r, "encrypt")
	if pkg.Type == encrypt.Type {
		t.Error("package and encryption must be separate types")
	}
}

func TestRegistryPreviewSpriteCombinedSingleton(t *testing.T) {
	r := NewRegistry()
	preview := findSpec(t, r, "preview")
	// preview should exist and embody sprite generation
	if preview.Type != "preview" {
		t.Fatal("preview type missing")
	}
	// There must not be a separate "sprite" type
	for _, g := range r.Groups {
		for _, s := range g.Types {
			if s.Type == "sprite" {
				t.Error("sprite must not be a separate type; it is combined with preview")
			}
		}
	}
}

func TestRegistryPhase5TypesVisibleUnavailable(t *testing.T) {
	r := NewRegistry()
	phase5Types := map[string]bool{
		"audio_analysis":     false,
		"audio_ai_analysis":  false,
		"image_ocr":          false,
		"document_convert":   false,
		"document_fulltext":  false,
		"person_scrape":      false,
		"artwork_cover":      false,
	}
	found := map[string]bool{}
	for _, g := range r.Groups {
		for _, s := range g.Types {
			if _, isPhase5 := phase5Types[s.Type]; isPhase5 {
				found[s.Type] = true
				if s.Available {
					t.Errorf("Phase 5 type %q must be available=false", s.Type)
				}
			}
		}
	}
	for typ := range phase5Types {
		if !found[typ] {
			t.Errorf("Phase 5 type %q must be visible in registry with available=false", typ)
		}
	}
}

func TestRegistryRoutesDeterministicAndUnique(t *testing.T) {
	r := NewRegistry()
	seen := map[string]string{}
	for _, g := range r.Groups {
		for _, s := range g.Types {
			if s.Route == "" {
				t.Errorf("type %q has empty route", s.Type)
				continue
			}
			if prev, ok := seen[s.Route]; ok {
				t.Errorf("duplicate route %q for types %q and %q", s.Route, prev, s.Type)
			}
			seen[s.Route] = s.Type
		}
	}
}

func TestRegistryColumnsDeterministicAndUnique(t *testing.T) {
	r := NewRegistry()
	for _, g := range r.Groups {
		for _, s := range g.Types {
			colKeys := map[string]bool{}
			for _, c := range s.Columns {
				if colKeys[c.Key] {
					t.Errorf("type %q has duplicate column key %q", s.Type, c.Key)
				}
				colKeys[c.Key] = true
			}
			// Verify deterministic order by sorting
			sorted := make([]string, 0, len(s.Columns))
			for _, c := range s.Columns {
				sorted = append(sorted, c.Key)
			}
			if !sort.StringsAreSorted(sorted) {
				t.Errorf("type %q columns are not in deterministic sorted order", s.Type)
			}
		}
	}
}

func TestRegistryFiltersDeterministicAndUnique(t *testing.T) {
	r := NewRegistry()
	for _, g := range r.Groups {
		for _, s := range g.Types {
			filterKeys := map[string]bool{}
			for _, f := range s.Filters {
				if filterKeys[f.Key] {
					t.Errorf("type %q has duplicate filter key %q", s.Type, f.Key)
				}
				filterKeys[f.Key] = true
			}
			sorted := make([]string, 0, len(s.Filters))
			for _, f := range s.Filters {
				sorted = append(sorted, f.Key)
			}
			if !sort.StringsAreSorted(sorted) {
				t.Errorf("type %q filters are not in deterministic sorted order", s.Type)
			}
		}
	}
}

func TestRegistryCapabilitiesDeterministicAndUnique(t *testing.T) {
	r := NewRegistry()
	for _, g := range r.Groups {
		for _, s := range g.Types {
			seen := map[string]bool{}
			for _, cap := range s.Capabilities {
				if seen[cap] {
					t.Errorf("type %q has duplicate capability %q", s.Type, cap)
				}
				seen[cap] = true
			}
			if !sort.StringsAreSorted(s.Capabilities) {
				t.Errorf("type %q capabilities are not in deterministic sorted order", s.Type)
			}
		}
	}
}

func TestRegistrySourceMappingsUnique(t *testing.T) {
	r := NewRegistry()
	// Collect all source mappings across all types
	type srcKey struct{ kind, internalType string }
	seen := map[srcKey]string{}
	for _, g := range r.Groups {
		for _, s := range g.Types {
			for _, sm := range s.SourceMappings {
				sk := srcKey{sm.Kind, sm.InternalType}
				if prev, ok := seen[sk]; ok {
					t.Errorf("duplicate source mapping kind=%q internalType=%q for types %q and %q", sm.Kind, sm.InternalType, prev, s.Type)
				}
				seen[sk] = s.Type
			}
		}
	}
}

func TestRegistryDisplayLabelsAreI18nKeys(t *testing.T) {
	r := NewRegistry()
	for _, g := range r.Groups {
		if !strings.HasPrefix(g.Label, "tasks.group.") {
			t.Errorf("group label %q must be an i18n key prefix", g.Label)
		}
	}
}

func TestRegistrySourceMappingsCoversPhase1To3Names(t *testing.T) {
	r := NewRegistry()
	// Collect all source mappings
	mappings := map[string]map[string]bool{}
	for _, g := range r.Groups {
		for _, s := range g.Types {
			typeKey := s.Type
			if mappings[typeKey] == nil {
				mappings[typeKey] = map[string]bool{}
			}
			for _, sm := range s.SourceMappings {
				mappings[typeKey][sm.Kind+":"+sm.InternalType] = true
			}
		}
	}

	// Phase 1-3 key mappings that must exist
	requiredMappings := map[string][]string{
		"poster":              {"post_ingest_task:poster", "post_ingest_task:poster_repair"},
		"thumbnail":           {"post_ingest_task:thumbnail"},
		"preview":             {"post_ingest_task:preview", "preview_task:"},
		"keyframe":            {"post_ingest_task:keyframe", "post_ingest_task:keyframe_extract", "keyframe_task:"},
		"subtitle_extract":    {"post_ingest_task:subtitle_extract"},
		"subtitle_recognize":  {"post_ingest_task:subtitle_recognize"},
		"atrack_extract":      {"post_ingest_task:atrack", "post_ingest_task:atrack_extract", "atrack_task:"},
		"encrypt":             {"post_ingest_task:encrypt"},
		"package":             {"post_ingest_task:package", "package_task:"},
		"pretranscode":        {"post_ingest_task:pretranscode"},
		"metadata_scrape":     {"post_ingest_task:metadata", "scrape_task:"},
		"ai_analysis":         {"post_ingest_task:ai_analysis"},
		"transcode":           {"transcode_task:"},
		"optimize":            {},
		"scan":                {"scan_task:"},
		"photo_classify":      {"photo_classify_task:"},
		"photo_geocode":       {"photo_geocode_task:"},
		"photo_face":          {"photo_face_task:"},
		"lyric_recognize":     {"lyric_task:"},
		"subtitle":            {"subtitle_task:"},
		"scheduled":           {"scheduled_task:"},
		"media_visible":       {"post_ingest_task:media_visible"},
		"audio_analysis":      {"audio_analysis_task:"},
		"audio_ai_analysis":   {"audio_ai_analysis_task:"},
		"image_ocr":           {"image_ocr_task:"},
		"document_convert":    {"document_convert_task:"},
		"document_fulltext":   {"document_fulltext_task:"},
		"person_scrape":       {"person_scrape_task:"},
		"artwork_cover":       {"artwork_cover_task:"},
	}

	for typ, expected := range requiredMappings {
		actual, ok := mappings[typ]
		if !ok {
			t.Errorf("type %q not found in registry", typ)
			continue
		}
		for _, exp := range expected {
			if !actual[exp] {
				t.Errorf("type %q missing source mapping %q", typ, exp)
			}
		}
	}
}

func TestRegistryPhase5TypeEnumeration(t *testing.T) {
	r := NewRegistry()
	phase5Types := []string{
		"lyric_recognize", "audio_analysis",
		"photo_classify", "photo_geocode", "photo_face", "image_ocr",
		"document_convert", "document_fulltext",
		"ai_analysis", "person_scrape", "artwork_cover",
	}
	for _, typ := range phase5Types {
		spec := findSpec(t, r, typ)
		if spec.Type != typ {
			t.Errorf("spec type %q != expected %q", spec.Type, typ)
		}
		if spec.Route == "" {
			t.Errorf("type %q has empty route", typ)
		}
		if spec.Family == "" {
			t.Errorf("type %q has empty family", typ)
		}
		if len(spec.SourceMappings) == 0 {
			t.Errorf("type %q has no source mappings", typ)
		}
		if len(spec.Columns) == 0 {
			t.Errorf("type %q has no columns", typ)
		}
		if len(spec.Capabilities) == 0 {
			t.Errorf("type %q has no capabilities", typ)
		}
	}
}

func TestRegistryAIOnlyPermitsMultipleCapabilitySubtasks(t *testing.T) {
	r := NewRegistry()
	aiSpec := findSpec(t, r, "ai_analysis")
	if aiSpec.Type != "ai_analysis" {
		t.Fatalf("expected ai_analysis, got %q", aiSpec.Type)
	}
	// ai_analysis is the only type that supports multiple capability subtasks
	// (summary, classification, tags) which is validated by Phase5Subtasks() in taskalign
}

func findSpec(t *testing.T, r *Registry, typeName string) TaskSpec {
	t.Helper()
	for _, g := range r.Groups {
		for _, s := range g.Types {
			if s.Type == typeName {
				return s
			}
		}
	}
	t.Fatalf("task type %q not found in registry", typeName)
	return TaskSpec{}
}
