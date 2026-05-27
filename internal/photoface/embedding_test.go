package photoface

import "testing"

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if cosineSimilarity(a, b) < 0.99 {
		t.Fatal("expected identical vectors")
	}
	c := []float32{0, 1, 0}
	if cosineSimilarity(a, c) > 0.01 {
		t.Fatal("expected orthogonal vectors")
	}
}

func TestMergeCentroid(t *testing.T) {
	prev := []float32{1, 0}
	next := []float32{0, 1}
	out := mergeCentroid(prev, 1, next)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
}
