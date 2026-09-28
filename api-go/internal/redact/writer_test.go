package redact

import (
	"bytes"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// writeChunks writes in into w in random-size writes.
func writeChunks(t *testing.T, w *Writer, in string, rng *rand.Rand, max int) {
	t.Helper()
	for len(in) > 0 {
		k := min(len(in), 1+rng.Intn(max))
		n, err := w.Write([]byte(in[:k]))
		if err != nil || n != k {
			t.Fatalf("write %d: %d %v", k, n, err)
		}
		in = in[k:]
	}
}

// The push-mode Writer produces exactly the one-shot redaction of each segment, for
// secrets at window boundaries and under any write sizes, and keeps one report across
// segments.
func TestWriterMatchesOneShotPerSegment(t *testing.T) {
	r := Default()
	corpus := secretCorpus()
	for seed := int64(1); seed <= 4; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var segments []string
		for range 3 {
			var b strings.Builder
			for b.Len() < 300<<10 {
				b.WriteString(filler(rng, rng.Intn(9000)))
				b.WriteString(" " + corpus[rng.Intn(len(corpus))].text + " ")
			}
			segments = append(segments, b.String())
		}
		// a secret straddling the first window boundary of the last segment
		s := corpus[int(seed)%len(corpus)]
		segments = append(segments, filler(rng, contextLen+windowSize-len(s.text)/2)+" "+s.text+" tail")
		var out bytes.Buffer
		w := r.NewWriter(&out)
		var want strings.Builder
		wantCount := 0
		for i, seg := range segments {
			writeChunks(t, w, seg, rng, []int{1 << 10, 64 << 10, 300 << 10, 7}[i%4])
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			red, rep, _ := oneShot(r, seg)
			want.WriteString(red)
			wantCount += rep.Count
		}
		if got := out.String(); got != want.String() {
			i := 0
			for i < len(got) && i < want.Len() && got[i] == want.String()[i] {
				i++
			}
			t.Fatalf("seed %d: writer differs from one-shot at byte %d", seed, i)
		}
		if w.Report().Count != wantCount || wantCount == 0 {
			t.Fatalf("seed %d: report %d, want %d", seed, w.Report().Count, wantCount)
		}
	}
}

// A secret written one byte at a time is still redacted, and nothing of it reaches
// dst before Flush decides it.
func TestWriterHoldsBackUndecidedBytes(t *testing.T) {
	r := Default()
	s := secretCorpus()[0]
	var out bytes.Buffer
	w := r.NewWriter(&out)
	for i := range len(s.text) {
		if _, err := w.Write([]byte{s.text[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("undecided bytes reached dst: %q", out.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), s.secret) || !strings.Contains(out.String(), "[REDACTED:") {
		t.Fatalf("flushed %q", out.String())
	}
}

type failingWriter struct{ n int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("disk full")
	}
	f.n--
	return len(p), nil
}

// A failing destination fails every later call.
func TestWriterKeepsTheFirstError(t *testing.T) {
	w := Default().NewWriter(&failingWriter{})
	big := strings.Repeat("plain text line\n", (contextLen+windowSize)/16+1)
	if _, err := w.Write([]byte(big)); err == nil {
		t.Fatal("write into a failing destination succeeded")
	}
	if err := w.Flush(); err == nil {
		t.Fatal("flush after a failure succeeded")
	}
}
