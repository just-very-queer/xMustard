// SPDX-License-Identifier: MIT
// Copyright 2026 xMustard contributors

// Package dedupe finds duplicate memories deterministically (WS-27, PAR-GOV-03). It
// normalizes a text, hashes its exact form, gates fuzzy matching on entropy, and
// signs its character 3-grams with a 32-permutation MinHash split into LSH bands of 4.
// minhash.go is an Apache-2.0 translation of Graphiti's dedup_helpers (see NOTICE at
// the repository root); this file is xMustard's own, under MIT.
//
// A Profile is computed once per text. Its posting keys (the exact key and, past the
// entropy gate, one key per band) are what the governance store keeps per memory, so
// candidate lookup is an indexed read of the store and nothing stays resident.
package dedupe

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// Profile is one text as dedupe compares it.
type Profile struct {
	Exact    string   // NormalizeExact
	Fuzzy    string   // NormalizeFuzzy
	Shingles []uint64 // Shingles(Fuzzy)
	// Gated: the text passes the entropy gate, so fuzzy matching may use it.
	Gated bool
}

// NewProfile profiles text.
func NewProfile(text string) Profile {
	p := Profile{Exact: NormalizeExact(text), Fuzzy: NormalizeFuzzy(text)}
	p.Shingles, p.Gated = Shingles(p.Fuzzy), HighEntropy(p.Fuzzy)
	return p
}

// Similarity is the Jaccard similarity of two profiles' shingle sets.
func Similarity(a, b Profile) float64 { return Jaccard(a.Shingles, b.Shingles) }

// Keys are the posting keys of a text within one namespace (a workspace): the exact key
// and, when the text passes the entropy gate, one key per LSH band. Keys of different
// namespaces, and exact keys and band keys, never collide by construction (up to the
// 64-bit hash).
type Keys struct {
	Exact int64
	Bands []int64
}

// KeysOf returns p's posting keys in namespace.
func KeysOf(namespace string, p Profile) Keys {
	k := Keys{Exact: key(namespace, "exact", []byte(p.Exact))}
	if !p.Gated {
		return k
	}
	for i, band := range Bands(Signature(p.Shingles)) {
		buf := make([]byte, 0, 8*len(band))
		for _, v := range band {
			buf = binary.BigEndian.AppendUint64(buf, v)
		}
		k.Bands = append(k.Bands, key(namespace, "band"+strconv.Itoa(i), buf))
	}
	return k
}

// All is every key, the exact key first.
func (k Keys) All() []int64 { return append([]int64{k.Exact}, k.Bands...) }

func key(namespace, kind string, value []byte) int64 {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(namespace), []byte(kind), value} {
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(part))))
		h.Write(part)
	}
	return int64(binary.BigEndian.Uint64(h.Sum(nil)))
}
