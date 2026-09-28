// SPDX-License-Identifier: Apache-2.0
// Copyright 2024, Zep Software, Inc.
// Copyright 2026 xMustard contributors
//
// Translated from graphiti@47f6482 graphiti_core/utils/maintenance/dedup_helpers.py
// (_normalize_string_exact, _normalize_name_for_fuzzy, _name_entropy,
// _has_high_entropy, _shingles, _minhash_signature, _lsh_bands, _jaccard_similarity and
// the thresholds they use). Changes by xMustard contributors:
//   - a shingle is hashed once (FNV-1a 64 and a splitmix64 finalizer) and each of the
//     32 permutations is splitmix64 of that hash XOR a fixed seed; upstream hashes
//     "seed:shingle" with blake2b per permutation, which Go's standard library lacks;
//   - shingle sets are sorted hash slices, so the Jaccard similarity compares hashes,
//     not strings (a 64-bit collision merges two shingles);
//   - the text is a memory's content instead of an entity name, and the exact form is
//     hashed into a posting key by keys.go instead of looked up in a dict.

package dedupe

import (
	"hash/fnv"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Thresholds, as upstream.
const (
	EntropyThreshold = 1.5
	MinLength        = 6
	MinTokens        = 2
	FuzzyThreshold   = 0.9
	Permutations     = 32
	BandSize         = 4
)

var (
	spaces   = regexp.MustCompile(`\s+`)
	nonFuzzy = regexp.MustCompile(`[^a-z0-9' ]`)
)

// NormalizeExact lower-cases text and collapses whitespace, so equal texts map to one
// key.
func NormalizeExact(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(strings.ToLower(s), " "))
}

// NormalizeFuzzy keeps ASCII letters, digits and apostrophes of the exact form, for
// shingling.
func NormalizeFuzzy(s string) string {
	n := strings.TrimSpace(nonFuzzy.ReplaceAllString(NormalizeExact(s), " "))
	return spaces.ReplaceAllString(n, " ")
}

// Entropy is the Shannon entropy, in bits, of the characters of normalized with its
// spaces removed. Short or repetitive text has low entropy.
func Entropy(normalized string) float64 {
	counts := map[rune]int{}
	total := 0
	for _, r := range strings.ReplaceAll(normalized, " ", "") {
		counts[r]++
		total++
	}
	if total == 0 {
		return 0
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(total)
		h -= p * math.Log2(p)
	}
	return h
}

// HighEntropy is the entropy gate: text that is both short and a single token, or
// whose entropy is under EntropyThreshold, is unreliable for fuzzy matching.
func HighEntropy(normalized string) bool {
	if len(normalized) < MinLength && len(strings.Fields(normalized)) < MinTokens {
		return false
	}
	return Entropy(normalized) >= EntropyThreshold
}

// Shingles hashes the character 3-grams of normalized with its spaces removed, sorted
// and de-duplicated. As upstream, a one-character text is its own shingle and a
// two-character text has none.
func Shingles(normalized string) []uint64 {
	cleaned := strings.ReplaceAll(normalized, " ", "")
	if len(cleaned) < 2 {
		if cleaned == "" {
			return nil
		}
		return []uint64{hashShingle(cleaned)}
	}
	out := make([]uint64, 0, max(len(cleaned)-2, 0))
	for i := 0; i+3 <= len(cleaned); i++ {
		out = append(out, hashShingle(cleaned[i:i+3]))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func hashShingle(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return mix(h.Sum64())
}

// mix is the splitmix64 finalizer, a bijection on 64-bit words.
func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ x>>31
}

// seeds are the permutations' fixed seeds: the splitmix64 sequence from 0.
var seeds = func() (s [Permutations]uint64) {
	var state uint64
	for i := range s {
		state += 0x9e3779b97f4a7c15
		s[i] = mix(state)
	}
	return s
}()

// Signature is the MinHash signature of a shingle set: per permutation, the least
// permuted hash. An empty set has no signature.
func Signature(shingles []uint64) []uint64 {
	if len(shingles) == 0 {
		return nil
	}
	sig := make([]uint64, Permutations)
	for i, seed := range seeds {
		least := uint64(math.MaxUint64)
		for _, h := range shingles {
			least = min(least, mix(h^seed))
		}
		sig[i] = least
	}
	return sig
}

// Bands splits a signature into LSH bands of BandSize values; a short last band is
// dropped.
func Bands(sig []uint64) [][]uint64 {
	var out [][]uint64
	for start := 0; start+BandSize <= len(sig); start += BandSize {
		out = append(out, sig[start:start+BandSize])
	}
	return out
}

// Jaccard is the Jaccard similarity of two sorted shingle sets: 1 when both are empty,
// 0 when one is.
func Jaccard(a, b []uint64) float64 {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 1
	case len(a) == 0 || len(b) == 0:
		return 0
	}
	inter := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			inter++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
