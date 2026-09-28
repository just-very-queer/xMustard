package redact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Stream sizing. A Reader holds one buffer of contextLen+windowSize bytes, an
// output buffer and per-window scratch sized by one window, so a stream of any
// length is redacted in memory bounded by the window.
const (
	windowSize = 128 << 10 // input bytes buffered per step
	// contextLen is how many consumed bytes the next window keeps: the part of
	// the lookahead that a region carried past it consumed, which is evaluated
	// again, plus room for the detectors' lookbehind (at most 256 bytes).
	contextLen = lookahead + 256
)

// Reader redacts another reader as it is read. Output is identical to
// Bytes over the whole input, whatever the chunking of the source's reads:
// the input is processed in fixed windows, and the last 33 KiB or so of each
// window are held back until the next one arrives, so a secret that straddles
// a read or window boundary is seen whole before any of it is emitted.
//
// If the source fails with an error other than io.EOF, the Reader emits what
// it had already decided, withholds the undecided tail (it may hold part of a
// secret), and returns the error.
type Reader struct {
	src    io.Reader
	eng    *engine
	buf    []byte
	n      int // valid bytes in buf
	from   int // first unprocessed byte; buf[:from] is context
	eof    bool
	err    error
	out    []byte
	outPos int
	done   bool
}

// NewReader returns a Reader that redacts src.
func (r *Redactor) NewReader(src io.Reader) *Reader {
	return newReader(src, &engine{r: r})
}

func newReader(src io.Reader, e *engine) *Reader {
	rd := &Reader{src: src, eng: e, buf: make([]byte, contextLen+windowSize)}
	rd.buf[0] = '\n' // synthetic left context: the start of input is a boundary
	rd.n, rd.from = 1, 1
	e.base = -1
	return rd
}

// Read implements io.Reader.
func (rd *Reader) Read(p []byte) (int, error) {
	for rd.outPos == len(rd.out) {
		if rd.done {
			if rd.err != nil {
				return 0, rd.err
			}
			return 0, io.EOF
		}
		rd.step()
	}
	k := copy(p, rd.out[rd.outPos:])
	rd.outPos += k
	return k, nil
}

// Report returns the counts so far; after io.EOF it covers the whole stream.
func (rd *Reader) Report() Report { return rd.eng.rep.clone() }

func (rd *Reader) step() {
	rd.out, rd.outPos = rd.out[:0], 0
	for zero := 0; rd.n < len(rd.buf) && !rd.eof; {
		k, err := rd.src.Read(rd.buf[rd.n:])
		rd.n += k
		switch {
		case errors.Is(err, io.EOF):
			rd.eof = true
		case err != nil:
			rd.err, rd.eof = err, true
		case k == 0:
			if zero++; zero > 100 {
				rd.err, rd.eof = io.ErrNoProgress, true
			}
		}
	}
	final := rd.eof && rd.err == nil
	var consumed int
	rd.out, consumed = rd.eng.window(rd.out, rd.buf, rd.from, rd.n, final)
	if rd.eof && (rd.err != nil || consumed >= rd.n) {
		rd.done = true
		return
	}
	keep := min(contextLen, consumed)
	copy(rd.buf, rd.buf[consumed-keep:rd.n])
	rd.eng.base += consumed - keep
	rd.n = keep + rd.n - consumed
	rd.from = keep
}

// Writer is the push-mode counterpart of Reader, for producers that write (the
// evidence spool, WS-23): what is written to it reaches dst redacted. Flush ends a
// segment: the held-back tail is decided as the end of input and written, and the
// next Write starts a new segment. Each segment's output is identical to Bytes over
// that segment, whatever the sizes of the writes, and a secret never spans two
// segments (a caller flushes at boundaries a secret cannot cross, such as between two
// JSON strings). Memory is bounded by the window, as for Reader.
type Writer struct {
	r   *Redactor
	dst io.Writer
	eng *engine
	buf []byte
	n   int // valid bytes in buf
	// from is the first byte not yet presented to the engine as new input.
	from int
	out  []byte
	err  error
}

// NewWriter returns a Writer that writes the redaction of what it is given to dst.
func (r *Redactor) NewWriter(dst io.Writer) *Writer {
	w := &Writer{r: r, dst: dst, buf: make([]byte, contextLen+windowSize)}
	w.reset(Report{})
	return w
}

// reset starts a segment, keeping the report so far.
func (w *Writer) reset(rep Report) {
	w.eng = &engine{r: w.r, rep: rep}
	w.eng.base = -1
	w.buf[0] = '\n' // synthetic left context: the start of a segment is a boundary
	w.n, w.from = 1, 1
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if w.err != nil {
			return written, w.err
		}
		k := copy(w.buf[w.n:], p)
		w.n, p, written = w.n+k, p[k:], written+k
		if w.n == len(w.buf) {
			w.window(false)
		}
	}
	return written, w.err
}

// Flush writes out the held-back tail of the segment and starts a new one.
func (w *Writer) Flush() error {
	if w.err == nil {
		w.window(true)
	}
	if w.err == nil {
		w.reset(w.eng.rep)
	}
	return w.err
}

// Report returns the counts so far, across segments.
func (w *Writer) Report() Report { return w.eng.rep.clone() }

// window decides one window (final: every remaining window of the segment), writes
// what it decided, and keeps the consumed context and the undecided lookahead.
func (w *Writer) window(final bool) {
	for {
		var consumed int
		w.out, consumed = w.eng.window(w.out[:0], w.buf, w.from, w.n, final)
		if len(w.out) > 0 {
			if _, err := w.dst.Write(w.out); err != nil {
				w.err = err
				return
			}
		}
		if final && consumed >= w.n {
			return
		}
		keep := min(contextLen, consumed)
		copy(w.buf, w.buf[consumed-keep:w.n])
		w.eng.base += consumed - keep
		w.n = keep + w.n - consumed
		w.from = keep
		if !final {
			return
		}
	}
}

// Copy redacts src into dst and returns the bytes written and the report.
func (r *Redactor) Copy(dst io.Writer, src io.Reader) (int64, Report, error) {
	rd := r.NewReader(src)
	n, err := io.Copy(dst, rd)
	return n, rd.Report(), err
}

// WriteFile redacts src into the file at path with mode 0600. It writes a
// temporary file in the same directory, syncs it, and renames it over path, so
// readers never see a partial or unredacted file.
func (r *Redactor) WriteFile(path string, src io.Reader) (Report, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return Report{}, err
	}
	name := tmp.Name()
	fail := func(err error) (Report, error) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return Report{}, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	_, rep, err := r.Copy(tmp, src)
	if err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return Report{}, err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return Report{}, err
	}
	return rep, nil
}
