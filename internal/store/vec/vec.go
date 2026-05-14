// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package vec

import (
	"encoding/binary"
	"errors"
	"math"
)

// Enabled reports whether a native vector-search extension (sqlite-vec) is
// loaded for the current database connection. It is always false on the
// pure-Go modernc.org/sqlite driver this project uses, which has no
// mechanism for loading native C extensions. Callers must use
// CosineSimilarity (or, eventually, NativeSearch when a loader exists) to
// perform retrieval, rather than issuing vec_* SQL functions.
//
// Kept as a package-level variable so that a future loader integration can
// flip it at DB open without changing every caller.
var Enabled = false

// ErrInvalidBlobLength is returned by Decode when the blob is not a whole
// multiple of 4 bytes (the size of one little-endian float32).
var ErrInvalidBlobLength = errors.New("vec: blob length is not a multiple of 4")

// Encode packs v as little-endian float32 bytes — the same on-disk format
// the sqlite-vec extension expects, so a future migration to native
// vector search needs no data rewrite.
func Encode(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// Decode unpacks a little-endian float32 blob produced by Encode.
func Decode(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, ErrInvalidBlobLength
	}
	if len(blob) == 0 {
		return nil, nil
	}
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out, nil
}

// CosineSimilarity returns the cosine similarity of a and b in [-1, 1].
// Mismatched dimensions or a zero-magnitude vector both yield 0 — these
// inputs have no meaningful direction, and the caller's threshold check
// will filter them out alongside other low-similarity rows.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
