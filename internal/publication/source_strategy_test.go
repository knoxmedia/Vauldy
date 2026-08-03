package publication

import (
	"context"
	"strings"
	"testing"
)

func TestSourceStrategyRegistryCoversRetryableExecutors(t *testing.T) {
	registry := DefaultEncryptedSourceStrategies()
	if err := ValidateStrategyRegistry(registry); err != nil {
		t.Fatal(err)
	}
	want := map[StepType]EncryptedSourceStrategy{
		StepPoster:            EncryptedSourceDerivative,
		StepThumbnail:         EncryptedSourceDerivative,
		StepScrape:            EncryptedSourceDerivative,
		StepPreview:           EncryptedSourceStreamDecrypt,
		StepSubtitleExtract:   EncryptedSourceStreamDecrypt,
		StepAtrackExtract:     EncryptedSourceStreamDecrypt,
		StepSubtitleRecognize: EncryptedSourceStreamDecrypt,
		StepKeyframeExtract:   EncryptedSourceStreamDecrypt,
		StepAIAnalysis:        EncryptedSourceDerivative,
		StepPrepare:           EncryptedSourceStreamDecrypt,
	}
	for step, strategy := range want {
		got, ok := registry.Contract(step)
		if !ok || !got.Validated || got.Strategy != strategy {
			t.Fatalf("%s: got=%+v ok=%v", step, got, ok)
		}
	}
	selected := make([]StepType, 0, len(want)+2)
	for step := range want {
		selected = append(selected, step)
	}
	selected = append(selected, StepMediaVisible, StepEncrypt)
	contracts, err := ValidateEncryptedSourceContracts(selected, true, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != len(want) {
		t.Fatalf("contracts=%d want %d", len(contracts), len(want))
	}
}

func TestSourceStrategyRegistryRejectsMissingOrWrongStrategy(t *testing.T) {
	base := DefaultEncryptedSourceStrategies()
	delete(base, StepPreview)
	if err := ValidateStrategyRegistry(base); err == nil || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("err=%v", err)
	}
	wrong := DefaultEncryptedSourceStrategies()
	wrong[StepPreview] = EncryptedSourceContract{Strategy: EncryptedSourceDerivative, Validated: true}
	if err := ValidateStrategyRegistry(wrong); err == nil || !strings.Contains(err.Error(), "stream_decrypt") {
		t.Fatalf("err=%v", err)
	}
}

func TestSourceStrategyExactlyOnePerTaskType(t *testing.T) {
	registry := DefaultEncryptedSourceStrategies()
	seen := map[StepType]bool{}
	for step, contract := range registry {
		if seen[step] {
			t.Fatalf("duplicate %s", step)
		}
		seen[step] = true
		if !contract.Validated {
			t.Fatalf("%s not validated", step)
		}
		switch contract.Strategy {
		case EncryptedSourceStreamDecrypt, EncryptedSourceMaterializeTemp, EncryptedSourceDerivative:
		default:
			t.Fatalf("%s strategy %q", step, contract.Strategy)
		}
	}
}

type recordingExecutableAdapter struct {
	typ   StepType
	calls int
}

func (a *recordingExecutableAdapter) TaskType() StepType { return a.typ }
func (a *recordingExecutableAdapter) Execute(context.Context, int64) error {
	a.calls++
	return nil
}

func TestExecutableAdapterMapAdmissionHandles(t *testing.T) {
	recog := &recordingExecutableAdapter{typ: StepSubtitleRecognize}
	ai := &recordingExecutableAdapter{typ: StepAIAnalysis}
	reg := ExecutableAdapterMap{
		StepSubtitleRecognize: recog,
		StepAIAnalysis:        ai,
	}
	if !hasExecutableAdapter(reg, StepSubtitleRecognize) || !hasExecutableAdapter(reg, StepAIAnalysis) {
		t.Fatal("expected admission handles")
	}
	if hasExecutableAdapter(reg, StepPreview) {
		t.Fatal("preview should not be forced through typed admission map")
	}
}
