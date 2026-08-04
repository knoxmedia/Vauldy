package publication

import (
	"errors"
	"fmt"

	"knox-media/internal/libraryprocessing"
)

// ErrCapabilityUnavailable is re-exported from plan_graph.go

// MediaFileCategory identifies the broad media group for template selection.
type MediaFileCategory string

const (
	MediaCategoryVideo    MediaFileCategory = "video"
	MediaCategoryImage    MediaFileCategory = "image"
	MediaCategoryAudio    MediaFileCategory = "audio"
	MediaCategoryDocument MediaFileCategory = "document"
)

// ClassifyMediaType maps a file_type string to a MediaFileCategory.
func ClassifyMediaType(fileType string) MediaFileCategory {
	switch fileType {
	case "video", "movie", "tv":
		return MediaCategoryVideo
	case "photo", "image", "picture":
		return MediaCategoryImage
	case "music", "audio", "podcast":
		return MediaCategoryAudio
	case "document", "book", "ebook":
		return MediaCategoryDocument
	default:
		return MediaFileCategory(fileType)
	}
}

// isMediaCategory returns true if the file type maps to video, image, audio, or document.
func isMediaCategory(fileType string) bool {
	c := ClassifyMediaType(fileType)
	return c == MediaCategoryVideo || c == MediaCategoryImage || c == MediaCategoryAudio || c == MediaCategoryDocument
}

// MediaTemplate defines the DAG template for a media category.
type MediaTemplate struct {
	Category         MediaFileCategory
	PrimaryRequired  StepType
	AllRequired      []StepType
	AllOptional      []StepType
	// optionStep maps library processing option to the step that should be included.
	optionStep map[string]StepType
}

// audioTemplate defines the DAG for audio (music, audio, podcast) files.
var audioTemplate = MediaTemplate{
	Category:        MediaCategoryAudio,
	PrimaryRequired: StepPoster,
	AllRequired:     []StepType{StepPoster},
	AllOptional:     []StepType{StepMediaVisible, StepScrape, StepLyricRecognize, StepAudioAnalysis, StepAIAnalysis},
	optionStep: map[string]StepType{
		libraryprocessing.OptionLyricRecognize: StepLyricRecognize,
		libraryprocessing.OptionAudioAnalysis:  StepAudioAnalysis,
		libraryprocessing.OptionAIAnalysis:     StepAIAnalysis,
	},
}

// imageTemplate defines the DAG for image (photo, image, picture) files.
var imageTemplate = MediaTemplate{
	Category:        MediaCategoryImage,
	PrimaryRequired: StepThumbnail,
	AllRequired:     []StepType{StepThumbnail},
	AllOptional:     []StepType{StepMediaVisible, StepScrape, StepPhotoClassify, StepPhotoGeocode, StepPhotoFace, StepImageOCR, StepAIAnalysis},
	optionStep: map[string]StepType{
		libraryprocessing.OptionPhotoClassify: StepPhotoClassify,
		libraryprocessing.OptionPhotoGeocode:  StepPhotoGeocode,
		libraryprocessing.OptionPhotoFace:     StepPhotoFace,
		libraryprocessing.OptionImageOCR:      StepImageOCR,
		libraryprocessing.OptionAIAnalysis:    StepAIAnalysis,
	},
}

// documentTemplate defines the DAG for document files.
var documentTemplate = MediaTemplate{
	Category:        MediaCategoryDocument,
	PrimaryRequired: StepPoster,
	AllRequired:     []StepType{StepPoster},
	AllOptional:     []StepType{StepMediaVisible, StepScrape, StepDocumentConvert, StepDocumentFulltext, StepImageOCR, StepAIAnalysis},
	optionStep: map[string]StepType{
		libraryprocessing.OptionDocumentConvert:  StepDocumentConvert,
		libraryprocessing.OptionDocumentFulltext: StepDocumentFulltext,
		libraryprocessing.OptionImageOCR:         StepImageOCR,
		libraryprocessing.OptionAIAnalysis:       StepAIAnalysis,
	},
}

// videoTemplate defines the DAG for video files (unchanged from V2/V3).
var videoTemplate = MediaTemplate{
	Category:        MediaCategoryVideo,
	PrimaryRequired: StepPoster,
	AllRequired:     []StepType{StepPoster},
	AllOptional:     []StepType{StepMediaVisible, StepScrape, StepPreview, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepAIAnalysis},
	optionStep: map[string]StepType{
		libraryprocessing.OptionPreview:           StepPreview,
		libraryprocessing.OptionSubtitleExtract:   StepSubtitleExtract,
		libraryprocessing.OptionATrackExtract:     StepAtrackExtract,
		libraryprocessing.OptionSubtitleRecognize: StepSubtitleRecognize,
		libraryprocessing.OptionKeyframeExtract:   StepKeyframeExtract,
		libraryprocessing.OptionAIAnalysis:        StepAIAnalysis,
	},
}

// lookupTemplate returns the appropriate template for a file type.
func lookupTemplate(fileType string) (*MediaTemplate, error) {
	c := ClassifyMediaType(fileType)
	switch c {
	case MediaCategoryVideo:
		return &videoTemplate, nil
	case MediaCategoryImage:
		return &imageTemplate, nil
	case MediaCategoryAudio:
		return &audioTemplate, nil
	case MediaCategoryDocument:
		return &documentTemplate, nil
	default:
		return nil, fmt.Errorf("no media template for %q", fileType)
	}
}

// enabledSteps returns the optional steps that are enabled by the given options.
func (t *MediaTemplate) enabledSteps(options libraryprocessing.Options) []StepType {
	var steps []StepType
	for _, step := range t.AllOptional {
		if step == StepMediaVisible || step == StepScrape {
			steps = append(steps, step)
			continue
		}
		for optName, optStep := range t.optionStep {
			if optStep == step && libraryprocessing.OptionEnabled(options, optName) {
				steps = append(steps, step)
				break
			}
		}
	}
	return steps
}

// phase5AIEdges returns the dependency edges for AI analysis with capability subtasks.
// ai_analysis depends on enabled recognition/analysis steps when AI is enabled.
func phase5AIEdges(options libraryprocessing.Options, category MediaFileCategory) []Dependency {
	if !options.AIAnalysis {
		return nil
	}
	switch category {
	case MediaCategoryVideo:
		return []Dependency{{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepSubtitleRecognize)}}
	case MediaCategoryAudio:
		edges := []Dependency{}
		if options.LyricRecognize {
			edges = append(edges, Dependency{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepLyricRecognize)})
		}
		if options.AudioAnalysis {
			edges = append(edges, Dependency{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepAudioAnalysis)})
		}
		return edges
	case MediaCategoryImage:
		edges := []Dependency{}
		if options.PhotoClassify {
			edges = append(edges, Dependency{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepPhotoClassify)})
		}
		if options.PhotoGeocode {
			edges = append(edges, Dependency{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepPhotoGeocode)})
		}
		if options.PhotoFace {
			edges = append(edges, Dependency{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepPhotoFace)})
		}
		return edges
	case MediaCategoryDocument:
		if options.DocumentFulltext {
			return []Dependency{{Step: StepAIAnalysis, Kind: DependencySuccess, DependsOn: stepPtr(StepDocumentFulltext)}}
		}
		return nil
	default:
		return nil
	}
}

// validatePhase5Template checks that all steps in the template are known and valid.
func validatePhase5Template(t *MediaTemplate) error {
	if t == nil {
		return errors.New("nil template")
	}
	for _, step := range t.AllRequired {
		if !knownPhase1Nodes[step] && !knownPhase5Nodes[step] {
			return fmt.Errorf("unknown required step %q in template %s", step, t.Category)
		}
	}
	for _, step := range t.AllOptional {
		if !knownPhase1Nodes[step] && !knownPhase5Nodes[step] {
			return fmt.Errorf("unknown optional step %q in template %s", step, t.Category)
		}
	}
	return nil
}

// nodeKeyForStep returns the canonical node_key value for a step.
// For backward compatibility, this defaults to the step_type value.
func nodeKeyForStep(step StepType) string {
	return string(step)
}

// ValidateTemplateEdges checks that all edges in a DAG are acyclic and reference valid nodes.
func ValidateTemplateEdges(steps []StepType, edges []Dependency) error {
	stepSet := map[StepType]bool{}
	for _, s := range steps {
		stepSet[s] = true
	}
	for _, e := range edges {
		if !stepSet[e.Step] {
			return fmt.Errorf("edge source %q not in step set", e.Step)
		}
		if e.DependsOn != nil && !stepSet[*e.DependsOn] {
			return fmt.Errorf("edge target %q not in step set", *e.DependsOn)
		}
	}
	// Simple cycle detection via visited/stack DFS
	adj := map[StepType][]StepType{}
	for _, e := range edges {
		if e.DependsOn != nil {
			adj[e.Step] = append(adj[e.Step], *e.DependsOn)
		}
	}
	visited := map[StepType]bool{}
	stack := map[StepType]bool{}
	var dfs func(s StepType) error
	dfs = func(s StepType) error {
		visited[s] = true
		stack[s] = true
		for _, t := range adj[s] {
			if stack[t] {
				return fmt.Errorf("cycle detected at %s -> %s", s, t)
			}
			if !visited[t] {
				if err := dfs(t); err != nil {
					return err
				}
			}
		}
		stack[s] = false
		return nil
	}
	for _, s := range steps {
		if !visited[s] {
			if err := dfs(s); err != nil {
				return err
			}
		}
	}
	return nil
}
