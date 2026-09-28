package redact

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// writeAll redacts in through a Writer, in writes of size() bytes, then flushes.
func writeAll(t *testing.T, r *Redactor, in string, size func() int) (string, Report) {
	t.Helper()
	var out bytes.Buffer
	w := r.NewWriter(&out)
	for rest := []byte(in); len(rest) > 0; {
		k := min(size(), len(rest))
		if n, err := w.Write(rest[:k]); n != k || err != nil {
			t.Fatalf("Write = %d, %v; want %d, nil", n, err, k)
		}
		rest = rest[k:]
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String(), w.Report()
}

func randomSizes(rng *rand.Rand, most int) func() int {
	return func() int { return 1 + rng.Intn(most) }
}

func oneByte() int { return 1 }

// splitAt writes k bytes, then the rest.
func splitAt(k int) func() int {
	next := k
	return func() int {
		n := next
		next = 1 << 30
		return n
	}
}

// Every golden secret, split by every write boundary: a Writer buffers writes into
// the Reader's windows, so the boundaries that matter are where a window ends (the
// input a step sees stops there) and where its decisions stop (the lookahead). Each
// sample is placed so that each boundary falls at every byte of it, and is written
// in random-size and one-byte writes. The output is the one-window output, byte for
// byte, and the report counts the same redactions.
func TestWriterSplitsEveryGoldenSecretAtEveryBoundary(t *testing.T) {
	r := Default()
	first := contextLen + windowSize - 1 // input bytes in the first window
	boundaries := map[string]int{"window end": first, "decision limit": first - lookahead}
	prefix := filler(rand.New(rand.NewSource(11)), first+1)
	tail := " " + filler(rand.New(rand.NewSource(12)), 2000)
	for _, s := range secretCorpus() {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(int64(len(s.text))))
			stride := 1
			if testing.Short() {
				stride = 13
			}
			// alone, the sample is one final window whatever the writes
			want, wantRep, _ := oneShot(r, s.text)
			for k := 1; k < len(s.text); k += stride {
				got, rep := writeAll(t, r, s.text, splitAt(k))
				if got != want || rep.Count != wantRep.Count {
					t.Fatalf("split at %d: got %q want %q", k, got, want)
				}
			}
			for name, b := range boundaries {
				for d := 0; d <= len(s.text); d += stride {
					in := prefix[:b-d-1] + "\n" + s.text + tail // the boundary falls before s.text[d]
					want, wantRep, _ := oneShot(r, in)
					if strings.Contains(want, s.secret) {
						t.Fatalf("%s at %d: the one-window reference leaks", name, d)
					}
					size := randomSizes(rng, 5000)
					if d%4 == 0 {
						size = oneByte
					}
					got, rep := writeAll(t, r, in, size)
					if got != want || rep.Count != wantRep.Count {
						t.Fatalf("%s at byte %d: Writer differs from one window (%d vs %d redactions)", name, d, rep.Count, wantRep.Count)
					}
				}
			}
		})
	}
}

// Randomized: long inputs whose secrets and regions straddle window ends, written in
// random sizes, give the one-window output and report.
func TestWriterMatchesOneShotRandomized(t *testing.T) {
	r := Default()
	seeds := int64(20)
	if testing.Short() {
		seeds = 4
	}
	inputs := map[string]func(*rand.Rand) string{
		"dense":  func(rng *rand.Rand) string { return denseInput(rng, 300<<10+rng.Intn(300<<10)) },
		"sticky": func(rng *rand.Rand) string { return stickyInput(rng, windowSize/2+rng.Intn(3*windowSize)) },
	}
	for name, gen := range inputs {
		for seed := int64(1); seed <= seeds; seed++ {
			rng := rand.New(rand.NewSource(seed))
			in := gen(rng)
			want, wantRep, _ := oneShot(r, in)
			got, rep := writeAll(t, r, in, randomSizes(rng, 1+rng.Intn(40000)))
			if got != want || rep.Count != wantRep.Count {
				t.Fatalf("%s seed %d: Writer differs from one window (%d vs %d redactions)", name, seed, rep.Count, wantRep.Count)
			}
		}
	}
}

// Flush ends an input: the output is each flushed input redacted on its own, a
// Flush with nothing written writes nothing, and the report counts every input.
func TestWriterFlushEndsAnInput(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(21))
	corpus := secretCorpus()
	var out bytes.Buffer
	w := r.NewWriter(&out)
	var want strings.Builder
	total := 0
	for i := 0; i < 12; i++ {
		s := corpus[rng.Intn(len(corpus))]
		in := filler(rng, rng.Intn(3*windowSize/(1+i))) + " " + s.text + " " + filler(rng, rng.Intn(500))
		ref, rep, _ := oneShot(r, in)
		want.WriteString(ref)
		total += rep.Count
		for rest := []byte(in); len(rest) > 0; {
			k := min(1+rng.Intn(9000), len(rest))
			if _, err := w.Write(rest[:k]); err != nil {
				t.Fatal(err)
			}
			rest = rest[k:]
		}
		for range 1 + i%2 { // a second Flush has nothing left to write
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out.String() != want.String() || w.Report().Count != total {
		t.Fatalf("flushed inputs differ from their one-window outputs (%d vs %d redactions)", w.Report().Count, total)
	}
}

// failAfter accepts limit bytes, then fails.
type failAfter struct{ limit int }

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.limit {
		return 0, errors.New("spool full")
	}
	f.limit -= len(p)
	return len(p), nil
}

// A destination error is returned by the write that met it and by every later call.
func TestWriterDestinationErrorSticks(t *testing.T) {
	w := Default().NewWriter(&failAfter{limit: 1000})
	in := []byte(strings.Repeat("safe words ", 40000)) // more than one window
	if _, err := w.Write(in); err == nil || err.Error() != "spool full" {
		t.Fatalf("Write error = %v", err)
	}
	if n, err := w.Write([]byte("x")); n != 0 || err == nil {
		t.Fatalf("a later Write = %d, %v", n, err)
	}
	if err := w.Flush(); err == nil {
		t.Fatal("Flush after a destination error succeeded")
	}
}

// writeGenerated streams size bytes of generated text through w in writes of
// random size, from one reused buffer, calling sample every 256 writes.
func writeGenerated(t *testing.T, w *Writer, g io.Reader, rng *rand.Rand, sample func()) {
	t.Helper()
	buf := make([]byte, 64<<10)
	for i := 0; ; i++ {
		n, err := io.ReadFull(g, buf[:1+rng.Intn(len(buf))])
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				t.Fatal(werr)
			}
		}
		if err != nil {
			break
		}
		if i%256 == 0 && sample != nil {
			sample()
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

// A Writer never holds the stream: redacting 32 MiB in random-size writes grows the
// live heap by no more than its fixed buffers (sampled throughout), and allocates a
// bounded amount whatever the input size or its density of secrets.
func TestWriterMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 32 MiB")
	}
	const size = 32 << 20
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak uint64
	sample := func() {
		runtime.GC()
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapAlloc)
	}
	g := &genReader{left: size, rng: rand.New(rand.NewSource(1))}
	w := Default().NewWriter(io.Discard)
	writeGenerated(t, w, g, rand.New(rand.NewSource(2)), sample)
	sample()
	growth := int64(peak) - int64(base)
	t.Logf("wrote %d MiB, %d redactions; live heap growth peak %d KiB", size>>20, w.Report().Count, growth>>10)
	if w.Report().Count != 2*g.secrets {
		t.Fatalf("redactions = %d, want %d", w.Report().Count, 2*g.secrets)
	}
	if growth > 1<<20 {
		t.Fatalf("live heap grew by %d KiB while writing 32 MiB; want O(1) (< 1 MiB)", growth>>10)
	}

	// dense trigger literals and secrets: allocations stay bounded, the live heap too
	const dense = 16 << 20
	const allocBound, heapBound = 4 << 20, 2 << 20
	fill := func(unit string) []byte { return bytes.Repeat([]byte(unit), dense/len(unit)+1)[:dense] }
	for _, unit := range []string{"hf_", "sk-", "://", "token:", `"password":"`, "password=Zx9!Zx9!\n",
		join("-----BEGIN ", "PRIVATE", " KEY-----"), "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789\n"} {
		in := fill(unit)
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		w := Default().NewWriter(io.Discard)
		for rest := in; len(rest) > 0; {
			k := min(32<<10, len(rest)) // the capture sink's write size
			_, _ = w.Write(rest[:k])
			rest = rest[k:]
		}
		_ = w.Flush()
		runtime.ReadMemStats(&b)
		runtime.GC()
		var c runtime.MemStats
		runtime.ReadMemStats(&c)
		alloc, live := b.TotalAlloc-a.TotalAlloc, int64(c.HeapAlloc)-int64(a.HeapAlloc)
		runtime.KeepAlive(w)
		runtime.KeepAlive(in)
		t.Logf("%-12.12q x16 MiB: %d redactions, allocated %d KiB, retained %d KiB", unit, w.Report().Count, alloc>>10, live>>10)
		if alloc > allocBound || live > heapBound {
			t.Errorf("%q: allocated %d KiB, retained %d KiB; want under %d and %d KiB", unit, alloc>>10, live>>10, allocBound>>10, heapBound>>10)
		}
	}
}

// BenchmarkWriter16MiB redacts a 16 MiB stream (a secret pair every ~4 KiB) in the
// capture sink's 32 KiB writes.
func BenchmarkWriter16MiB(b *testing.B) {
	const size = 16 << 20
	in, err := io.ReadAll(&genReader{left: size, rng: rand.New(rand.NewSource(1))})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := Default().NewWriter(io.Discard)
		for rest := in; len(rest) > 0; {
			k := min(32<<10, len(rest))
			_, _ = w.Write(rest[:k])
			rest = rest[k:]
		}
		if err := w.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
