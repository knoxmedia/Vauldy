package libraryprocessing

import (
	"reflect"
	"testing"
)

func TestCloseIndependentOptions(t *testing.T) {
	tests := []struct {
		name     string
		explicit Options
	}{
		{name: "preview", explicit: Options{Preview: true}},
		{name: "keyframe extraction", explicit: Options{KeyframeExtract: true}},
		{name: "preview and keyframe extraction", explicit: Options{Preview: true, KeyframeExtract: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close("video", tt.explicit)
			if effective != tt.explicit {
				t.Fatalf("Close(%+v) effective = %+v, want unchanged", tt.explicit, effective)
			}
		})
	}
}

func TestCloseDependencyMatrix(t *testing.T) {
	tests := []struct {
		name           string
		explicit, want Options
	}{
		{name: "subtitle recognition adds extraction prerequisites", explicit: Options{SubtitleRecognize: true}, want: Options{SubtitleExtract: true, ATrackExtract: true, SubtitleRecognize: true}},
		{name: "AI analysis adds recognition and extraction prerequisites", explicit: Options{AIAnalysis: true}, want: Options{SubtitleExtract: true, ATrackExtract: true, SubtitleRecognize: true, KeyframeExtract: true, AIAnalysis: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close("video", tt.explicit)
			if effective != tt.want {
				t.Fatalf("Close(%+v) effective = %+v, want %+v", tt.explicit, effective, tt.want)
			}
		})
	}
}

func TestCloseIsIdempotentAndDeterministic(t *testing.T) {
	explicit := Options{Preview: true, AIAnalysis: true}
	firstEffective, firstProvenance := Close("video", explicit)
	secondEffective, secondProvenance := Close("video", explicit)
	closedAgain, _ := Close("video", firstEffective)
	if firstEffective != closedAgain {
		t.Fatalf("closing effective options changed them: first %+v, second %+v", firstEffective, closedAgain)
	}
	if firstEffective != secondEffective || !reflect.DeepEqual(firstProvenance, secondProvenance) {
		t.Fatalf("repeated Close was not deterministic: first (%+v, %+v), second (%+v, %+v)", firstEffective, firstProvenance, secondEffective, secondProvenance)
	}
}

func TestCloseReportsSortedProvenance(t *testing.T) {
	tests := []struct {
		name     string
		explicit Options
		want     Provenance
	}{
		{name: "explicit options use stable sorted names", explicit: Options{Preview: true, SubtitleExtract: true, ATrackExtract: true, SubtitleRecognize: true, KeyframeExtract: true, AIAnalysis: true}, want: Provenance{Explicit: []string{"ai_analysis", "atrack_extract", "keyframe_extract", "preview", "subtitle_extract", "subtitle_recognize"}}},
		{name: "closure lists only newly enabled options", explicit: Options{SubtitleExtract: true, AIAnalysis: true}, want: Provenance{Explicit: []string{"ai_analysis", "subtitle_extract"}, DependencyAdded: []string{"atrack_extract", "keyframe_extract", "subtitle_recognize"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := Close("video", tt.explicit)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Close(%+v) provenance = %#v, want %#v", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestRequiredByReportsSortedDirectEnabledDependents(t *testing.T) {
	effective, _ := Close("video", Options{AIAnalysis: true})
	tests := []struct {
		option string
		want   []string
	}{
		{option: OptionSubtitleExtract, want: []string{OptionSubtitleRecognize}},
		{option: OptionATrackExtract, want: []string{OptionSubtitleRecognize}},
		{option: OptionSubtitleRecognize, want: []string{OptionAIAnalysis}},
		{option: OptionPreview, want: nil}, {option: OptionKeyframeExtract, want: nil}, {option: OptionAIAnalysis, want: nil}, {option: "unknown", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.option, func(t *testing.T) {
			if got := RequiredBy(effective, tt.option); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RequiredBy(%+v, %q) = %#v, want %#v", effective, tt.option, got, tt.want)
			}
		})
	}
}

func TestRequiredByIgnoresDisabledAndTransitiveDependents(t *testing.T) {
	tests := []struct {
		name      string
		effective Options
		option    string
		want      []string
	}{
		{name: "disabled recognition does not lock subtitle extraction", effective: Options{SubtitleExtract: true}, option: "subtitle_extract", want: nil},
		{name: "AI is not a transitive lock reason for subtitle extraction", effective: Options{SubtitleExtract: true, AIAnalysis: true}, option: "subtitle_extract", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiredBy(tt.effective, tt.option); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RequiredBy(%+v, %q) = %#v, want %#v", tt.effective, tt.option, got, tt.want)
			}
		})
	}
}

func TestOptionNamesAreStable(t *testing.T) {
	got := []string{OptionPreview, OptionSubtitleExtract, OptionATrackExtract, OptionSubtitleRecognize, OptionKeyframeExtract, OptionAIAnalysis, OptionLyricRecognize, OptionAudioAnalysis, OptionPhotoClassify, OptionPhotoGeocode, OptionPhotoFace, OptionImageOCR, OptionDocumentConvert, OptionDocumentFulltext}
	want := []string{"preview", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis", "lyric_recognize", "audio_analysis", "photo_classify", "photo_geocode", "photo_face", "image_ocr", "document_convert", "document_fulltext"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option names = %#v, want %#v", got, want)
	}
}

func TestCloseAlreadyEffectiveOptionsHaveNoDependencyProvenance(t *testing.T) {
	effective, _ := Close("video", Options{Preview: true, AIAnalysis: true})
	_, provenance := Close("video", effective)
	want := Provenance{Explicit: []string{"ai_analysis", "atrack_extract", "keyframe_extract", "preview", "subtitle_extract", "subtitle_recognize"}}
	if !reflect.DeepEqual(provenance, want) {
		t.Fatalf("Close(already effective) provenance = %#v, want %#v", provenance, want)
	}
}

// Phase 5 audio policy tests: audio AI summary → lyric_recognize,
// audio classification/tags → audio_analysis.

func TestPhase5AudioPolicyMatrix(t *testing.T) {
	tests := []struct {
		name     string
		explicit Options
		want     Options
	}{
		{
			name:     "lyric recognize is independent",
			explicit: Options{LyricRecognize: true},
			want:     Options{LyricRecognize: true},
		},
		{
			name:     "audio analysis is independent",
			explicit: Options{AudioAnalysis: true},
			want:     Options{AudioAnalysis: true},
		},
		{
			name:     "audio AI summary pulls in lyric",
			explicit: Options{AIAnalysis: true},
			want:     Options{LyricRecognize: true, AIAnalysis: true},
		},
		{
			name:     "audio classification subsumes analysis",
			explicit: Options{AudioAnalysis: true, AIAnalysis: true},
			want:     Options{LyricRecognize: true, AudioAnalysis: true, AIAnalysis: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close("audio", tt.explicit)
			if effective != tt.want {
				t.Fatalf("Close(%+v) effective = %+v, want %+v", tt.explicit, effective, tt.want)
			}
		})
	}
}

// Phase 5 image policy tests per frozen policy.

func TestPhase5ImagePolicyMatrix(t *testing.T) {
	tests := []struct {
		name     string
		explicit Options
		want     Options
	}{
		{
			name:     "photo classify independent",
			explicit: Options{PhotoClassify: true},
			want:     Options{PhotoClassify: true},
		},
		{
			name:     "photo geocode independent",
			explicit: Options{PhotoGeocode: true},
			want:     Options{PhotoGeocode: true},
		},
		{
			name:     "photo face independent",
			explicit: Options{PhotoFace: true},
			want:     Options{PhotoFace: true},
		},
		{
			name:     "image OCR independent",
			explicit: Options{ImageOCR: true},
			want:     Options{ImageOCR: true},
		},
		{
			name:     "image AI pulls classify/geocode/face prerequisites",
			explicit: Options{PhotoClassify: true, AIAnalysis: true},
			want:     Options{PhotoClassify: true, PhotoGeocode: true, PhotoFace: true, AIAnalysis: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close("image", tt.explicit)
			if effective != tt.want {
				t.Fatalf("Close(%+v) effective = %+v, want %+v", tt.explicit, effective, tt.want)
			}
		})
	}
}

// Phase 5 document policy tests

func TestPhase5DocumentPolicyMatrix(t *testing.T) {
	tests := []struct {
		name     string
		explicit Options
		want     Options
	}{
		{
			name:     "document convert enables fulltext (office)",
			explicit: Options{DocumentConvert: true},
			want:     Options{DocumentConvert: true, DocumentFulltext: true},
		},
		{
			name:     "document fulltext enables conversion",
			explicit: Options{DocumentFulltext: true},
			want:     Options{DocumentConvert: true, DocumentFulltext: true},
		},
		{
			name:     "document AI enables fulltext which enables conversion",
			explicit: Options{AIAnalysis: true},
			want:     Options{AIAnalysis: true, DocumentConvert: true, DocumentFulltext: true},
		},
		{
			name:     "document OCR enables fulltext which enables conversion",
			explicit: Options{ImageOCR: true},
			want:     Options{ImageOCR: true, DocumentConvert: true, DocumentFulltext: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close("document", tt.explicit)
			if effective != tt.want {
				t.Fatalf("Close(%+v) effective = %+v, want %+v", tt.explicit, effective, tt.want)
			}
		})
	}
}

// Phase 5 cross-media leakage: audio flags must not enable image or doc options.

func TestPhase5NoCrossMediaLeakage(t *testing.T) {
	tests := []struct {
		name     string
		libType  string
		explicit Options
		forbid  []string
	}{
		{
			name:    "audio lyric must not pull image or doc",
			libType: "audio",
			explicit: Options{LyricRecognize: true},
			forbid:  []string{"photo_classify", "photo_geocode", "photo_face", "image_ocr", "document_convert", "document_fulltext"},
		},
		{
			name:    "audio analysis must not pull image or doc",
			libType: "audio",
			explicit: Options{AudioAnalysis: true},
			forbid:  []string{"photo_classify", "photo_geocode", "photo_face", "image_ocr", "document_convert", "document_fulltext"},
		},
		{
			name:    "image classify must not pull audio or doc",
			libType: "image",
			explicit: Options{PhotoClassify: true},
			forbid:  []string{"lyric_recognize", "audio_analysis", "document_convert", "document_fulltext"},
		},
		{
			name:    "document convert must not pull audio or image",
			libType: "document",
			explicit: Options{DocumentConvert: true},
			forbid:  []string{"lyric_recognize", "audio_analysis", "photo_classify", "photo_geocode", "photo_face", "image_ocr"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close(tt.libType, tt.explicit)
			for _, forbid := range tt.forbid {
				if optionEnabled(effective, forbid) {
					t.Errorf("Close(%+v) leaked %q across media families", tt.explicit, forbid)
				}
			}
		})
	}
}
