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
			effective, _ := Close(tt.explicit)
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
		{name: "AI analysis adds recognition and extraction prerequisites", explicit: Options{AIAnalysis: true}, want: Options{SubtitleExtract: true, ATrackExtract: true, SubtitleRecognize: true, AIAnalysis: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective, _ := Close(tt.explicit)
			if effective != tt.want {
				t.Fatalf("Close(%+v) effective = %+v, want %+v", tt.explicit, effective, tt.want)
			}
		})
	}
}

func TestCloseIsIdempotentAndDeterministic(t *testing.T) {
	explicit := Options{Preview: true, AIAnalysis: true}
	firstEffective, firstProvenance := Close(explicit)
	secondEffective, secondProvenance := Close(explicit)
	closedAgain, _ := Close(firstEffective)
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
		{name: "closure lists only newly enabled options", explicit: Options{SubtitleExtract: true, AIAnalysis: true}, want: Provenance{Explicit: []string{"ai_analysis", "subtitle_extract"}, DependencyAdded: []string{"atrack_extract", "subtitle_recognize"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := Close(tt.explicit)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Close(%+v) provenance = %#v, want %#v", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestRequiredByReportsSortedDirectEnabledDependents(t *testing.T) {
	effective, _ := Close(Options{AIAnalysis: true})
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
	got := []string{OptionPreview, OptionSubtitleExtract, OptionATrackExtract, OptionSubtitleRecognize, OptionKeyframeExtract, OptionAIAnalysis}
	want := []string{"preview", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option names = %#v, want %#v", got, want)
	}
}

func TestCloseAlreadyEffectiveOptionsHaveNoDependencyProvenance(t *testing.T) {
	effective, _ := Close(Options{Preview: true, AIAnalysis: true})
	_, provenance := Close(effective)
	want := Provenance{Explicit: []string{"ai_analysis", "atrack_extract", "preview", "subtitle_extract", "subtitle_recognize"}}
	if !reflect.DeepEqual(provenance, want) {
		t.Fatalf("Close(already effective) provenance = %#v, want %#v", provenance, want)
	}
}
