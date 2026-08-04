package publication

import (
	"testing"
)

// Validated:true strategies must name a truthful durable contract. Stream-decrypt
// executors prove encrypted-after-plaintext-removal in storage/preview/atrack/keyframe/subtitle/pretranscode tests.
func TestEncryptedSourceValidatedStrategiesAreTruthful(t *testing.T) {
	registry := DefaultEncryptedSourceStrategies()
	for _, step := range []StepType{StepPreview, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepPrepare} {
		c, ok := registry.Contract(step)
		if !ok || !c.Validated || c.Strategy != EncryptedSourceStreamDecrypt {
			t.Fatalf("%s contract=%+v ok=%v want stream_decrypt", step, c, ok)
		}
	}
	for _, step := range []StepType{StepPoster, StepThumbnail, StepScrape, StepAIAnalysis} {
		c, ok := registry.Contract(step)
		if !ok || !c.Validated || c.Strategy != EncryptedSourceDerivative {
			t.Fatalf("%s want encrypted_derivative got %+v", step, c)
		}
	}
	// No Phase 1 cleanup-eligible task currently claims materialize_temp.
	for step, c := range registry {
		if c.Strategy == EncryptedSourceMaterializeTemp {
			t.Fatalf("%s falsely claims materialize_temp without a verified materialize path", step)
		}
	}
}
