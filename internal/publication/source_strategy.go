package publication

import "fmt"

// StrategyRegistry is a typed map of validated encrypted-source contracts.
type StrategyRegistry map[StepType]EncryptedSourceContract

// Contract implements EncryptedSourceRegistry.
func (r StrategyRegistry) Contract(step StepType) (EncryptedSourceContract, bool) {
	if r == nil {
		return EncryptedSourceContract{}, false
	}
	c, ok := r[step]
	return c, ok
}

// DefaultEncryptedSourceStrategies registers exactly one validated strategy per
// Phase 1 cleanup-compatible / retry-after-retirement executor.
func DefaultEncryptedSourceStrategies() StrategyRegistry {
	return StrategyRegistry{
		StepPoster:            {Strategy: EncryptedSourceDerivative, Validated: true},
		StepThumbnail:         {Strategy: EncryptedSourceDerivative, Validated: true},
		StepScrape:            {Strategy: EncryptedSourceDerivative, Validated: true},
		StepPreview:           {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
		StepSubtitleExtract:   {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
		StepAtrackExtract:     {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
		// Recognition uses decrypting ffmpeg/ffprobe pipes and audio extract; it does not
		// materialize the full encrypted movie for external CLIs.
		StepSubtitleRecognize: {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
		StepKeyframeExtract:   {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
		StepAIAnalysis:        {Strategy: EncryptedSourceDerivative, Validated: true},
		StepPrepare:           {Strategy: EncryptedSourceStreamDecrypt, Validated: true},
	}
}

// ValidateStrategyRegistry ensures every cleanup-compatible retryable executor
// has exactly one allowed strategy and no unknown entries.
func ValidateStrategyRegistry(registry EncryptedSourceRegistry) error {
	defaults := DefaultEncryptedSourceStrategies()
	for step := range defaults {
		contract, ok := EncryptedSourceContract{}, false
		if registry != nil {
			contract, ok = registry.Contract(step)
		}
		if !ok || !contract.Validated {
			return fmt.Errorf("source strategy: missing validated contract for %s", step)
		}
		if contract.Strategy != EncryptedSourceStreamDecrypt && contract.Strategy != EncryptedSourceMaterializeTemp && contract.Strategy != EncryptedSourceDerivative {
			return fmt.Errorf("source strategy: invalid strategy %q for %s", contract.Strategy, step)
		}
		if contract.Strategy != defaults[step].Strategy {
			return fmt.Errorf("source strategy: %s must use %s (got %s)", step, defaults[step].Strategy, contract.Strategy)
		}
	}
	return nil
}

// ExecutableAdapterMap is a simple ExecutableAdapterRegistry.
type ExecutableAdapterMap map[StepType]ExecutableTaskAdapter

// Adapter implements ExecutableAdapterRegistry.
func (m ExecutableAdapterMap) Adapter(step StepType) (ExecutableTaskAdapter, bool) {
	if m == nil {
		return nil, false
	}
	a, ok := m[step]
	return a, ok && a != nil
}
