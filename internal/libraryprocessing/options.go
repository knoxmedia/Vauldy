package libraryprocessing

import "sort"

// Stable option identifiers accepted by RequiredBy.
const (
	OptionPreview           = "preview"
	OptionSubtitleExtract   = "subtitle_extract"
	OptionATrackExtract     = "atrack_extract"
	OptionSubtitleRecognize = "subtitle_recognize"
	OptionKeyframeExtract   = "keyframe_extract"
	OptionAIAnalysis        = "ai_analysis"
)

const (
	optionPreview           = OptionPreview
	optionSubtitleExtract   = OptionSubtitleExtract
	optionATrackExtract     = OptionATrackExtract
	optionSubtitleRecognize = OptionSubtitleRecognize
	optionKeyframeExtract   = OptionKeyframeExtract
	optionAIAnalysis        = OptionAIAnalysis
)

type Options struct {
	Preview, SubtitleExtract, ATrackExtract        bool
	SubtitleRecognize, KeyframeExtract, AIAnalysis bool
}

type Provenance struct {
	Explicit        []string `json:"explicit"`
	DependencyAdded []string `json:"dependency_added"`
}

func Close(explicit Options) (effective Options, provenance Provenance) {
	effective = explicit
	if effective.AIAnalysis {
		effective.SubtitleRecognize = true
	}
	if effective.SubtitleRecognize {
		effective.SubtitleExtract = true
		effective.ATrackExtract = true
	}

	provenance.Explicit = enabledOptionNames(explicit)
	for _, option := range enabledOptionNames(effective) {
		if !optionEnabled(explicit, option) {
			provenance.DependencyAdded = append(provenance.DependencyAdded, option)
		}
	}
	return effective, provenance
}

// RequiredBy returns enabled direct dependents of option. Unknown option strings return no requirements.
func RequiredBy(effective Options, option string) []string {
	var requiredBy []string
	switch option {
	case optionSubtitleExtract, optionATrackExtract:
		if effective.SubtitleRecognize {
			requiredBy = append(requiredBy, optionSubtitleRecognize)
		}
	case optionSubtitleRecognize:
		if effective.AIAnalysis {
			requiredBy = append(requiredBy, optionAIAnalysis)
		}
	}
	sort.Strings(requiredBy)
	return requiredBy
}

func enabledOptionNames(options Options) []string {
	var names []string
	for _, option := range []string{
		optionPreview,
		optionSubtitleExtract,
		optionATrackExtract,
		optionSubtitleRecognize,
		optionKeyframeExtract,
		optionAIAnalysis,
	} {
		if optionEnabled(options, option) {
			names = append(names, option)
		}
	}
	sort.Strings(names)
	return names
}

func optionEnabled(options Options, option string) bool {
	switch option {
	case optionPreview:
		return options.Preview
	case optionSubtitleExtract:
		return options.SubtitleExtract
	case optionATrackExtract:
		return options.ATrackExtract
	case optionSubtitleRecognize:
		return options.SubtitleRecognize
	case optionKeyframeExtract:
		return options.KeyframeExtract
	case optionAIAnalysis:
		return options.AIAnalysis
	default:
		return false
	}
}
