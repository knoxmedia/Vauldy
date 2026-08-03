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
	// Phase 5 audio options
	OptionLyricRecognize = "lyric_recognize"
	OptionAudioAnalysis  = "audio_analysis"
	// Phase 5 image options
	OptionPhotoClassify = "photo_classify"
	OptionPhotoGeocode  = "photo_geocode"
	OptionPhotoFace     = "photo_face"
	OptionImageOCR      = "image_ocr"
	// Phase 5 document options
	OptionDocumentConvert  = "document_convert"
	OptionDocumentFulltext = "document_fulltext"
)

const (
	optionPreview           = OptionPreview
	optionSubtitleExtract   = OptionSubtitleExtract
	optionATrackExtract     = OptionATrackExtract
	optionSubtitleRecognize = OptionSubtitleRecognize
	optionKeyframeExtract   = OptionKeyframeExtract
	optionAIAnalysis        = OptionAIAnalysis
	// Phase 5 audio
	optionLyricRecognize = OptionLyricRecognize
	optionAudioAnalysis  = OptionAudioAnalysis
	// Phase 5 image
	optionPhotoClassify = OptionPhotoClassify
	optionPhotoGeocode  = OptionPhotoGeocode
	optionPhotoFace     = OptionPhotoFace
	optionImageOCR      = OptionImageOCR
	// Phase 5 document
	optionDocumentConvert  = OptionDocumentConvert
	optionDocumentFulltext = OptionDocumentFulltext
)

type Options struct {
	Preview, SubtitleExtract, ATrackExtract        bool
	SubtitleRecognize, KeyframeExtract, AIAnalysis bool
	// Phase 5 audio
	LyricRecognize, AudioAnalysis bool
	// Phase 5 image
	PhotoClassify, PhotoGeocode, PhotoFace, ImageOCR bool
	// Phase 5 document
	DocumentConvert, DocumentFulltext bool
}

type Provenance struct {
	Explicit        []string `json:"explicit"`
	DependencyAdded []string `json:"dependency_added"`
}

// allOptionNames returns every known option identifier in stable order.
func allOptionNames() []string {
	return []string{
		optionPreview,
		optionSubtitleExtract,
		optionATrackExtract,
		optionSubtitleRecognize,
		optionKeyframeExtract,
		optionAIAnalysis,
		// Phase 5 audio
		optionLyricRecognize,
		optionAudioAnalysis,
		// Phase 5 image
		optionPhotoClassify,
		optionPhotoGeocode,
		optionPhotoFace,
		optionImageOCR,
		// Phase 5 document
		optionDocumentConvert,
		optionDocumentFulltext,
	}
}

func Close(typ string, explicit Options) (effective Options, provenance Provenance) {
	effective = explicit
	switch typ {
	case "movie", "tv", "video", "anime":
		// Video dependency closure: AI → subtitle_recognize → subtitle_extract + atrack_extract
		if effective.AIAnalysis {
			effective.SubtitleRecognize = true
			effective.KeyframeExtract = true
		}
		if effective.SubtitleRecognize {
			effective.SubtitleExtract = true
			effective.ATrackExtract = true
		}
	case "music", "audio", "podcast":
		// Audio dependency closure: AI → lyric_recognize
		if effective.AIAnalysis {
			effective.LyricRecognize = true
		}
	case "photo", "image", "picture":
		// Image dependency closure: AI → photo_classify, photo_geocode, photo_face
		if effective.AIAnalysis {
			effective.PhotoClassify = true
			effective.PhotoGeocode = true
			effective.PhotoFace = true
		}
	case "document", "book", "ebook":
		// Document dependency closure: AI → fulltext, OCR → fulltext, convert ↔ fulltext
		if effective.AIAnalysis {
			effective.DocumentFulltext = true
		}
		if effective.ImageOCR {
			effective.DocumentFulltext = true
		}
		if effective.DocumentFulltext {
			effective.DocumentConvert = true
		}
		if effective.DocumentConvert {
			effective.DocumentFulltext = true
		}
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
	// Phase 5: cross-media guarding — only report within the same media family.
	case optionLyricRecognize:
		if effective.AIAnalysis {
			requiredBy = append(requiredBy, optionAIAnalysis)
		}
	case optionAudioAnalysis:
		if effective.AIAnalysis {
			requiredBy = append(requiredBy, optionAIAnalysis)
		}
	case optionPhotoClassify, optionPhotoGeocode, optionPhotoFace:
		if effective.AIAnalysis {
			requiredBy = append(requiredBy, optionAIAnalysis)
		}
	case optionDocumentFulltext:
		if effective.AIAnalysis {
			requiredBy = append(requiredBy, optionAIAnalysis)
		}
		if effective.ImageOCR {
			requiredBy = append(requiredBy, optionImageOCR)
		}
	case optionDocumentConvert:
		if effective.DocumentFulltext {
			requiredBy = append(requiredBy, optionDocumentFulltext)
		}
	}
	sort.Strings(requiredBy)
	return requiredBy
}

func enabledOptionNames(options Options) []string {
	var names []string
	for _, option := range allOptionNames() {
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
	// Phase 5 audio
	case optionLyricRecognize:
		return options.LyricRecognize
	case optionAudioAnalysis:
		return options.AudioAnalysis
	// Phase 5 image
	case optionPhotoClassify:
		return options.PhotoClassify
	case optionPhotoGeocode:
		return options.PhotoGeocode
	case optionPhotoFace:
		return options.PhotoFace
	case optionImageOCR:
		return options.ImageOCR
	// Phase 5 document
	case optionDocumentConvert:
		return options.DocumentConvert
	case optionDocumentFulltext:
		return options.DocumentFulltext
	default:
		return false
	}
}
