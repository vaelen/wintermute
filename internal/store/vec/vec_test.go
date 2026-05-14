// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package vec

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, -0.25, float32(math.Pi), float32(math.E)}

	blob := Encode(in)
	if want := 4 * len(in); len(blob) != want {
		t.Fatalf("Encode length = %d, want %d (4 bytes per float32)", len(blob), want)
	}

	out, err := Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("Decode length = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("Decode[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestEncodeEmpty(t *testing.T) {
	blob := Encode(nil)
	if len(blob) != 0 {
		t.Errorf("Encode(nil) length = %d, want 0", len(blob))
	}
	out, err := Decode(blob)
	if err != nil {
		t.Fatalf("Decode(empty): %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Decode(empty) length = %d, want 0", len(out))
	}
}

func TestDecodeRejectsBadLength(t *testing.T) {
	if _, err := Decode([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Errorf("Decode of 3-byte blob: err = nil, want non-nil (not a multiple of 4)")
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if got := CosineSimilarity(a, b); math.Abs(float64(got)-1.0) > 1e-6 {
		t.Errorf("Cosine of identical vectors = %v, want 1.0", got)
	}

	c := []float32{0, 1, 0}
	if got := CosineSimilarity(a, c); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("Cosine of orthogonal vectors = %v, want 0.0", got)
	}

	d := []float32{-1, 0, 0}
	if got := CosineSimilarity(a, d); math.Abs(float64(got)-(-1.0)) > 1e-6 {
		t.Errorf("Cosine of opposite vectors = %v, want -1.0", got)
	}

	// Different magnitudes, same direction → still similarity 1.
	e := []float32{2, 0, 0}
	if got := CosineSimilarity(a, e); math.Abs(float64(got)-1.0) > 1e-6 {
		t.Errorf("Cosine of co-directional vectors = %v, want 1.0", got)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	zero := []float32{0, 0, 0}
	other := []float32{1, 2, 3}
	if got := CosineSimilarity(zero, other); got != 0 {
		t.Errorf("Cosine with zero vector = %v, want 0", got)
	}
}

func TestCosineSimilarityMismatchedDims(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0, 0}
	if got := CosineSimilarity(a, b); got != 0 {
		t.Errorf("Cosine of mismatched dims = %v, want 0 (defensive)", got)
	}
}
