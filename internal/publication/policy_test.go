package publication

import (
	"sync"
	"testing"
)

func TestCapabilityMatrixAvailability(t *testing.T) {
	matrix := NewCapabilityMatrix([]string{"poster", "prepare"})

	if !matrix.Available("poster") {
		t.Fatal("poster capability should be available")
	}
	if !matrix.Available("prepare") {
		t.Fatal("prepare capability should be available")
	}
}

func TestCapabilityMatrixIsImmutable(t *testing.T) {
	steps := []string{"poster", "prepare"}
	matrix := NewCapabilityMatrix(steps)
	steps[0] = "changed"
	steps = append(steps, "new")

	if !matrix.Available("poster") {
		t.Fatal("matrix should retain the originally registered capability after input mutation")
	}
	if matrix.Available("new") {
		t.Fatal("matrix should not observe mutations to the input slice")
	}
}

func TestCapabilityMatrixConcurrentReads(t *testing.T) {
	matrix := NewCapabilityMatrix([]string{"poster", "prepare"})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if !matrix.Available("poster") || matrix.Available("unknown") {
					t.Error("concurrent capability lookup returned an invalid result")
				}
			}
		}()
	}
	wg.Wait()
}

func TestCapabilityMatrixUnknownFalse(t *testing.T) {
	matrix := NewCapabilityMatrix([]string{"poster"})

	for _, step := range []string{"", "unknown", " poster ", "POSTER"} {
		if matrix.Available(step) {
			t.Fatalf("capability %q should not be available", step)
		}
	}
}
