// SPDX-License-Identifier: MIT
// Copyright 2026 xMustard contributors

package dedupe

import (
	"fmt"
	"math"
	"testing"
)

func TestNormalizeAndShingle(t *testing.T) {
	cases := []struct{ in, exact, fuzzy string }{
		{"  Use\tPORT 8081\n now ", "use port 8081 now", "use port 8081 now"},
		{"read_timeout = 5s; don't!", "read_timeout = 5s; don't!", "read timeout 5s don't"},
	}
	for _, c := range cases {
		if got := NormalizeExact(c.in); got != c.exact {
			t.Errorf("NormalizeExact(%q) = %q, want %q", c.in, got, c.exact)
		}
		if got := NormalizeFuzzy(c.in); got != c.fuzzy {
			t.Errorf("NormalizeFuzzy(%q) = %q, want %q", c.in, got, c.fuzzy)
		}
	}
	// upstream's edge cases: one character is its own shingle, two have none
	for in, want := range map[string]int{"": 0, "a": 1, "ab": 0, "abc": 1, "abcd": 2, "ab cd": 2, "aaaa": 1} {
		if got := len(Shingles(in)); got != want {
			t.Errorf("len(Shingles(%q)) = %d, want %d", in, got, want)
		}
	}
}

// The entropy gate keeps short or repetitive text out of fuzzy matching.
func TestEntropyGate(t *testing.T) {
	for text, want := range map[string]bool{
		"":                         false,
		"fix":                      false, // one short token
		"ok ok ok ok":              false, // two symbols: 1 bit
		"aaaaaaaaaaaa":             false,
		"use port 8081":            true,
		"deploy via make release.": true,
	} {
		if got := HighEntropy(NormalizeFuzzy(text)); got != want {
			t.Errorf("HighEntropy(%q) = %v (entropy %.2f), want %v", text, got, Entropy(NormalizeFuzzy(text)), want)
		}
	}
	if h := Entropy("ab"); math.Abs(h-1) > 1e-9 {
		t.Fatalf("Entropy(ab) = %v, want 1", h)
	}
}

func TestJaccardAndSignature(t *testing.T) {
	a := Shingles(NormalizeFuzzy("the service listens on port 8081 behind the proxy"))
	b := Shingles(NormalizeFuzzy("the service listens on port 8081 behind the proxy!"))
	c := Shingles(NormalizeFuzzy("tests must never reach the network; use the fake transport"))
	if Jaccard(a, b) != 1 || Jaccard(nil, nil) != 1 || Jaccard(a, nil) != 0 {
		t.Fatal("Jaccard edge cases")
	}
	if j := Jaccard(a, c); j > 0.2 {
		t.Fatalf("unrelated texts have Jaccard %.2f", j)
	}
	sa, sb := Signature(a), Signature(b)
	if len(sa) != Permutations || fmt.Sprint(sa) != fmt.Sprint(sb) || Signature(nil) != nil {
		t.Fatal("signature is not deterministic in the shingle set")
	}
	if got := len(Bands(sa)); got != Permutations/BandSize {
		t.Fatalf("%d bands, want %d", got, Permutations/BandSize)
	}
}

// MinHash agreement estimates Jaccard: over many pairs, the fraction of equal
// signature slots tracks the true similarity.
func TestSignatureEstimatesJaccard(t *testing.T) {
	base := "the governance store keeps every revision and event of a memory in sqlite"
	var worst float64
	for i := range 20 {
		other := fmt.Sprintf("%s and revision %d adds note %d", base, i, i*7)
		sa, sb := Shingles(NormalizeFuzzy(base)), Shingles(NormalizeFuzzy(other))
		x, y := Signature(sa), Signature(sb)
		equal := 0
		for k := range x {
			if x[k] == y[k] {
				equal++
			}
		}
		worst = max(worst, math.Abs(float64(equal)/Permutations-Jaccard(sa, sb)))
	}
	if worst > 0.3 {
		t.Fatalf("MinHash estimate off by %.2f", worst)
	}
}

// Posting keys: one exact key always, eight band keys past the entropy gate, all
// scoped to the namespace; the stored signature is at most 9 keys (72 bytes).
func TestKeysOf(t *testing.T) {
	p := NewProfile("The payments service reads PAYMENTS_DB_PASSWORD at startup.")
	k := KeysOf("ws1", p)
	if len(k.Bands) != Permutations/BandSize || len(k.All()) != 9 || k.All()[0] != k.Exact {
		t.Fatalf("keys %+v", k)
	}
	if KeysOf("ws2", p).Exact == k.Exact {
		t.Fatal("exact keys collide across namespaces")
	}
	same := KeysOf("ws1", NewProfile("the payments service   reads payments_db_password at STARTUP."))
	if same.Exact != k.Exact {
		t.Fatal("normalization does not reach the exact key")
	}
	low := KeysOf("ws1", NewProfile("ok ok ok"))
	if len(low.Bands) != 0 {
		t.Fatalf("low-entropy text has band keys: %+v", low)
	}
}
