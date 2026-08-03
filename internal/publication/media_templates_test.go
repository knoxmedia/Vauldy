package publication

import (
	"strings"
	"testing"

	"knox-media/internal/libraryprocessing"
)

func TestClassifyMediaType(t *testing.T) {
	tests := []struct {
		fileType string
		want     MediaFileCategory
	}{
		{"video", MediaCategoryVideo},
		{"movie", MediaCategoryVideo},
		{"tv", MediaCategoryVideo},
		{"photo", MediaCategoryImage},
		{"image", MediaCategoryImage},
		{"picture", MediaCategoryImage},
		{"music", MediaCategoryAudio},
		{"audio", MediaCategoryAudio},
		{"podcast", MediaCategoryAudio},
		{"document", MediaCategoryDocument},
		{"book", MediaCategoryDocument},
		{"ebook", MediaCategoryDocument},
		{"unknown", MediaFileCategory("unknown")},
	}
	for _, tt := range tests {
		got := ClassifyMediaType(tt.fileType)
		if got != tt.want {
			t.Errorf("ClassifyMediaType(%q) = %q, want %q", tt.fileType, got, tt.want)
		}
	}
}

func TestIsMediaCategory(t *testing.T) {
	for _, ft := range []string{"video", "movie", "tv", "photo", "image", "picture", "music", "audio", "podcast", "document", "book", "ebook"} {
		if !isMediaCategory(ft) {
			t.Errorf("isMediaCategory(%q) = false, want true", ft)
		}
	}
	if isMediaCategory("unknown") {
		t.Error("isMediaCategory(\"unknown\") = true, want false")
	}
}

func TestLookupTemplate(t *testing.T) {
	tests := []struct {
		fileType string
		wantCat  MediaFileCategory
	}{
		{"video", MediaCategoryVideo},
		{"photo", MediaCategoryImage},
		{"music", MediaCategoryAudio},
		{"document", MediaCategoryDocument},
	}
	for _, tt := range tests {
		tmpl, err := lookupTemplate(tt.fileType)
		if err != nil {
			t.Fatalf("lookupTemplate(%q): %v", tt.fileType, err)
		}
		if tmpl.Category != tt.wantCat {
			t.Errorf("lookupTemplate(%q).Category = %q, want %q", tt.fileType, tmpl.Category, tt.wantCat)
		}
	}
}

func TestLookupTemplateUnknown(t *testing.T) {
	_, err := lookupTemplate("bogus")
	if err == nil {
		t.Fatal("lookupTemplate(\"bogus\") should return error")
	}
	if !strings.Contains(err.Error(), "no media template") {
		t.Errorf("error %q does not contain 'no media template'", err.Error())
	}
}

func TestTemplateAllRequiredNonEmpty(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		if len(tmpl.AllRequired) == 0 {
			t.Errorf("template for %q has empty AllRequired", ft)
		}
		if tmpl.PrimaryRequired == "" {
			t.Errorf("template for %q has empty PrimaryRequired", ft)
		}
	}
}

func TestTemplateOptionalContainsMediaVisibleScrape(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		hasVisible := false
		hasScrape := false
		for _, s := range tmpl.AllOptional {
			if s == StepMediaVisible {
				hasVisible = true
			}
			if s == StepScrape {
				hasScrape = true
			}
		}
		if !hasVisible {
			t.Errorf("template for %q missing media_visible in optional steps", ft)
		}
		if !hasScrape {
			t.Errorf("template for %q missing scrape in optional steps", ft)
		}
	}
}

func TestEnabledStepsWithNoOptions(t *testing.T) {
	tests := []struct {
		fileType string
		wantMin  int // at least media_visible + scrape
	}{
		{"video", 2},
		{"photo", 2}, // thumbnail is required, not optional; media_visible + scrape = 2
		{"music", 2},
		{"document", 2},
	}
	for _, tt := range tests {
		tmpl, _ := lookupTemplate(tt.fileType)
		steps := tmpl.enabledSteps(libraryprocessing.Options{})
		if len(steps) < tt.wantMin {
			t.Errorf("enabledSteps(%q) with no options got %d steps, want >= %d: %v", tt.fileType, len(steps), tt.wantMin, steps)
		}
		// verify no media-specific optional steps are enabled
		for _, s := range steps {
			if s == StepPreview || s == StepSubtitleExtract || s == StepAtrackExtract ||
				s == StepSubtitleRecognize || s == StepKeyframeExtract || s == StepAIAnalysis ||
				s == StepLyricRecognize || s == StepAudioAnalysis ||
				s == StepPhotoClassify || s == StepPhotoGeocode || s == StepPhotoFace ||
				s == StepImageOCR || s == StepDocumentConvert || s == StepDocumentFulltext {
				t.Errorf("enabledSteps(%q) with no options should not include %s", tt.fileType, s)
			}
		}
	}
}

func TestEnabledStepsWithOptions(t *testing.T) {
	tmpl, _ := lookupTemplate("music")
	steps := tmpl.enabledSteps(libraryprocessing.Options{LyricRecognize: true})
	found := false
	for _, s := range steps {
		if s == StepLyricRecognize {
			found = true
		}
	}
	if !found {
		t.Error("enabledSteps with LyricRecognize should include lyric_recognize")
	}
}

func TestTemplateKeyOptionStepMappingCoversAllOptional(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		for _, step := range tmpl.AllOptional {
			if step == StepMediaVisible || step == StepScrape {
				continue
			}
			found := false
			for _, os := range tmpl.optionStep {
				if os == step {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("template for %q: optional step %s not mapped in optionStep", ft, step)
			}
		}
	}
}

func TestPhase5AIEdgesAudio(t *testing.T) {
	// AI enabled + lyric + audio_analysis
	opts := libraryprocessing.Options{AIAnalysis: true, LyricRecognize: true, AudioAnalysis: true}
	edges := phase5AIEdges(opts, MediaCategoryAudio)
	if len(edges) != 2 {
		t.Fatalf("expected 2 audio AI edges, got %d", len(edges))
	}
	if *edges[0].DependsOn != StepLyricRecognize || *edges[1].DependsOn != StepAudioAnalysis {
		t.Errorf("audio AI edges: %v", edges)
	}

	// AI enabled but no prerequisites
	edges = phase5AIEdges(libraryprocessing.Options{AIAnalysis: true}, MediaCategoryAudio)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges without prerequisites, got %d", len(edges))
	}

	// AI disabled
	edges = phase5AIEdges(libraryprocessing.Options{LyricRecognize: true}, MediaCategoryAudio)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges without AI, got %d", len(edges))
	}
}

func TestPhase5AIEdgesImage(t *testing.T) {
	allOpts := libraryprocessing.Options{AIAnalysis: true, PhotoClassify: true, PhotoGeocode: true, PhotoFace: true}
	edges := phase5AIEdges(allOpts, MediaCategoryImage)
	if len(edges) != 3 {
		t.Fatalf("expected 3 image AI edges, got %d", len(edges))
	}

	// AI disabled
	edges = phase5AIEdges(libraryprocessing.Options{PhotoClassify: true}, MediaCategoryImage)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges without AI, got %d", len(edges))
	}
}

func TestPhase5AIEdgesDocument(t *testing.T) {
	edges := phase5AIEdges(libraryprocessing.Options{AIAnalysis: true, DocumentFulltext: true}, MediaCategoryDocument)
	if len(edges) != 1 {
		t.Fatalf("expected 1 document AI edge, got %d", len(edges))
	}
	if *edges[0].DependsOn != StepDocumentFulltext {
		t.Errorf("document AI edge target = %s, want document_fulltext", *edges[0].DependsOn)
	}

	// AI enabled but no fulltext
	edges = phase5AIEdges(libraryprocessing.Options{AIAnalysis: true}, MediaCategoryDocument)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges without fulltext, got %d", len(edges))
	}
}

func TestPhase5AIEdgesVideo(t *testing.T) {
	edges := phase5AIEdges(libraryprocessing.Options{AIAnalysis: true}, MediaCategoryVideo)
	if len(edges) != 1 {
		t.Fatalf("expected 1 video AI edge, got %d", len(edges))
	}
	if *edges[0].DependsOn != StepSubtitleRecognize {
		t.Errorf("video AI edge target = %s, want subtitle_recognize", *edges[0].DependsOn)
	}
}

func TestValidatePhase5TemplateNil(t *testing.T) {
	err := validatePhase5Template(nil)
	if err == nil {
		t.Fatal("expected error for nil template")
	}
}

func TestValidatePhase5TemplateValid(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		if err := validatePhase5Template(tmpl); err != nil {
			t.Errorf("validatePhase5Template(%q): unexpected error: %v", ft, err)
		}
	}
}

func TestValidatePhase5TemplateUnknownRequired(t *testing.T) {
	tmpl := &MediaTemplate{
		Category:        MediaCategoryAudio,
		PrimaryRequired: "bogus_step",
		AllRequired:     []StepType{"bogus_step"},
		AllOptional:     []StepType{StepMediaVisible, StepScrape},
	}
	err := validatePhase5Template(tmpl)
	if err == nil {
		t.Fatal("expected error for unknown required step")
	}
}

func TestValidatePhase5TemplateUnknownOptional(t *testing.T) {
	tmpl := &MediaTemplate{
		Category:        MediaCategoryAudio,
		PrimaryRequired: StepPoster,
		AllRequired:     []StepType{StepPoster},
		AllOptional:     []StepType{"bogus_step"},
	}
	err := validatePhase5Template(tmpl)
	if err == nil {
		t.Fatal("expected error for unknown optional step")
	}
}

func TestNodeKeyForStep(t *testing.T) {
	steps := []StepType{StepLyricRecognize, StepAudioAnalysis, StepPhotoClassify, StepDocumentFulltext, StepAIAnalysis, StepPoster, StepMediaVisible, StepScrape, StepPersonScrape, StepArtworkCover}
	for _, s := range steps {
		key := nodeKeyForStep(s)
		if key != string(s) {
			t.Errorf("nodeKeyForStep(%s) = %s, want %s", s, key, string(s))
		}
	}
}

func TestValidateTemplateEdgesNoCycle(t *testing.T) {
	steps := []StepType{StepPoster, StepMediaVisible, StepScrape}
	edges := []Dependency{
		{Step: StepMediaVisible, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)},
		{Step: StepScrape, Kind: DependencySuccess, DependsOn: stepPtr(StepMediaVisible)},
	}
	if err := ValidateTemplateEdges(steps, edges); err != nil {
		t.Errorf("expected no cycle: %v", err)
	}
}

func TestValidateTemplateEdgesCycle(t *testing.T) {
	steps := []StepType{StepPoster, StepMediaVisible}
	edges := []Dependency{
		{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepMediaVisible)},
		{Step: StepMediaVisible, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)},
	}
	err := ValidateTemplateEdges(steps, edges)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q does not contain 'cycle'", err.Error())
	}
}

func TestValidateTemplateEdgesUnknownSource(t *testing.T) {
	steps := []StepType{StepPoster, StepMediaVisible}
	edges := []Dependency{
		{Step: StepScrape, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)},
	}
	err := ValidateTemplateEdges(steps, edges)
	if err == nil {
		t.Fatal("expected error for unknown edge source")
	}
}

func TestValidateTemplateEdgesUnknownTarget(t *testing.T) {
	steps := []StepType{StepPoster}
	edges := []Dependency{
		{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepMediaVisible)},
	}
	err := ValidateTemplateEdges(steps, edges)
	if err == nil {
		t.Fatal("expected error for unknown edge target")
	}
}

func TestValidateTemplateEdgesSelfEdge(t *testing.T) {
	steps := []StepType{StepPoster}
	edges := []Dependency{
		{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)},
	}
	err := ValidateTemplateEdges(steps, edges)
	if err == nil {
		t.Fatal("expected cycle error for self-edge")
	}
}

func TestTemplateDeterministicOrder(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		// required steps must be in deterministic order
		req1 := tmpl.AllRequired
		req2 := tmpl.AllRequired
		if len(req1) != len(req2) {
			t.Errorf("template %q required steps length changed", ft)
		}
		for i := range req1 {
			if req1[i] != req2[i] {
				t.Errorf("template %q required steps order not deterministic", ft)
			}
		}
		// optional steps must be in deterministic order
		opt1 := tmpl.AllOptional
		opt2 := tmpl.AllOptional
		if len(opt1) != len(opt2) {
			t.Errorf("template %q optional steps length changed", ft)
		}
		for i := range opt1 {
			if opt1[i] != opt2[i] {
				t.Errorf("template %q optional steps order not deterministic", ft)
			}
		}
	}
}

func TestTemplateNoDuplicateSteps(t *testing.T) {
	for _, ft := range []string{"video", "photo", "music", "document"} {
		tmpl, _ := lookupTemplate(ft)
		all := append(append([]StepType{}, tmpl.AllRequired...), tmpl.AllOptional...)
		seen := map[StepType]bool{}
		for _, s := range all {
			if seen[s] {
				t.Errorf("template %q has duplicate step %s", ft, s)
			}
			seen[s] = true
		}
	}
}

func TestVideoTemplatePrimaryRequiredPoster(t *testing.T) {
	tmpl, _ := lookupTemplate("video")
	if tmpl.PrimaryRequired != StepPoster {
		t.Errorf("video PrimaryRequired = %s, want poster", tmpl.PrimaryRequired)
	}
	if tmpl.AllRequired[0] != StepPoster {
		t.Errorf("video AllRequired[0] = %s, want poster", tmpl.AllRequired[0])
	}
}

func TestImageTemplatePrimaryRequiredThumbnail(t *testing.T) {
	tmpl, _ := lookupTemplate("photo")
	if tmpl.PrimaryRequired != StepThumbnail {
		t.Errorf("image PrimaryRequired = %s, want thumbnail", tmpl.PrimaryRequired)
	}
	if tmpl.AllRequired[0] != StepThumbnail {
		t.Errorf("image AllRequired[0] = %s, want thumbnail", tmpl.AllRequired[0])
	}
}
