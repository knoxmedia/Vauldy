package photoface

import (
	"encoding/binary"
	"math"
)

func packEmbedding(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func unpackEmbedding(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(na))) * float32(math.Sqrt(float64(nb))))
}

func normalizeEmbedding(v []float32) []float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(float64(sum)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func mergeCentroid(prev []float32, prevCount int, next []float32) []float32 {
	if len(next) == 0 {
		return prev
	}
	next = normalizeEmbedding(next)
	if len(prev) == 0 || prevCount <= 0 {
		return next
	}
	prev = normalizeEmbedding(prev)
	out := make([]float32, len(next))
	total := float32(prevCount + 1)
	for i := range out {
		out[i] = (prev[i]*float32(prevCount) + next[i]) / total
	}
	return normalizeEmbedding(out)
}
