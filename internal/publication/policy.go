package publication

// CapabilityMatrix is an immutable, concurrency-safe registry of supported
// publication steps.
type CapabilityMatrix struct {
	steps map[string]struct{}
}

// NewCapabilityMatrix registers a snapshot of the supplied publication steps.
func NewCapabilityMatrix(steps []string) CapabilityMatrix {
	registered := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if step != "" {
			registered[step] = struct{}{}
		}
	}
	return CapabilityMatrix{steps: registered}
}

// Available reports whether step was registered in the matrix.
func (m CapabilityMatrix) Available(step string) bool {
	if step == "" {
		return false
	}
	_, ok := m.steps[step]
	return ok
}
