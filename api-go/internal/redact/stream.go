package redact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Stream sizing. A Reader or Writer holds one buffer of contextLen+windowSize
// bytes and redacts it window by window: each window decides at most
// decideSpan bytes and reads the lookahead after them. Its output and its
// scratch (the candidates its detectors find) are therefore sized by
// decideSpan, not by the buffer, so a stream of any length and any density of
// secrets is redacted in memory bounded by the window.
const (
	windowSize = 128 << 10 // input bytes buffered per refill
	// decideSpan is the most input one stream window decides. A window also
	// reads the lookahead after it, and evaluates again the part of the
	// previous lookahead that a region carried past what it consumed; its
	// detectors run over that in slices of decideSpan (engine.span), so its
	// scratch holds one slice's worth of candidates.
	decideSpan = 8 << 10
	// contextLen is how many consumed bytes the next window keeps: the part of
	// the lookahead that a region carried past it consumed, which is evaluated
	// again, plus room for the detectors' lookbehind (at most 256 bytes).
	contextLen = lookahead + 256
)

// streamBuf is the input buffer a Reader fills from its source and a Writer
// from its writes: buf[:from] is context kept from consumed input, and
// buf[from:n] is input not yet consumed.
type streamBuf struct {
	eng  *engine
	buf  []byte
	n    int
	from int
}

func newStreamBuf(e *engine) streamBuf {
	e.span = decideSpan
	s := streamBuf{eng: e, buf: make([]byte, contextLen+windowSize)}
	s.reset()
	return s
}

// reset makes the next byte the start of an input.
func (s *streamBuf) reset() {
	s.buf[0] = '\n' // synthetic left context: the start of input is a boundary
	s.n, s.from = 1, 1
	s.eng.base = -1
}

// ready reports whether buf holds a whole window past from: decideSpan bytes
// and the lookahead after them.
func (s *streamBuf) ready() bool { return s.n-s.from >= decideSpan+lookahead }

// step redacts the next window of buf[from:n], appending the output to out. It
// shows the engine at most decideSpan bytes and the lookahead after them; a
// final step (no input follows buf[:n]) that reaches n tells the engine that
// the input ends there. Every step consumes at least one byte, unless it is
// not final and buf holds no more than the lookahead past from.
func (s *streamBuf) step(out []byte, final bool) []byte {
	n := min(s.n, s.from+decideSpan+lookahead)
	out, s.from = s.eng.window(out, s.buf, s.from, n, final && n == s.n)
	return out
}

// compact makes room for input: the undecided input, and the consumed context
// the next window reads, move to the front of buf.
func (s *streamBuf) compact() {
	keep := min(contextLen, s.from)
	copy(s.buf, s.buf[s.from-keep:s.n])
	s.eng.base += s.from - keep
	s.n -= s.from - keep
	s.from = keep
}

// Reader redacts another reader as it is read. Output is identical to
// Bytes over the whole input, whatever the chunking of the source's reads:
// the input is processed in windows, and the 33 KiB or so after what each
// window decides are read with it and held back until the next one, so a
// secret that straddles a read or window boundary is seen whole before any of
// it is emitted.
//
// If the source fails with an error other than io.EOF, the Reader emits what
// it had already decided, withholds the undecided tail (it may hold part of a
// secret), and returns the error.
type Reader struct {
	src    io.Reader
	in     streamBuf
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
	return &Reader{src: src, in: newStreamBuf(e)}
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
func (rd *Reader) Report() Report { return rd.in.eng.rep.clone() }

func (rd *Reader) step() {
	rd.out, rd.outPos = rd.out[:0], 0
	in := &rd.in
	if !rd.eof && !in.ready() {
		in.compact()
		for zero := 0; in.n < len(in.buf) && !rd.eof; {
			k, err := rd.src.Read(in.buf[in.n:])
			in.n += k
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
	}
	// The final input is decided to its end. After a source error, whole
	// windows are decided while buf holds them, then one more up to the
	// lookahead, and the undecided tail stays withheld.
	final, whole := rd.eof && rd.err == nil, in.ready()
	rd.out = in.step(rd.out, final)
	rd.done = rd.eof && (in.from == in.n || !final && !whole)
}

// Writer redacts what is written to it into another writer. What it writes
// is what Bytes returns for the input written since it was created or last
// flushed, whatever the sizes of the writes: writes fill the buffer a Reader
// reads into, each window is decided as soon as the buffer holds it and its
// lookahead, and the undecided tail (the lookahead, which covers the longest
// span any detector reads past a match) is held back until more input arrives
// or Flush ends the input. It holds one buffer and one window's output and
// scratch, never the stream.
//
// Flush ends an input: everything held back is redacted and written, and the
// next write starts a new input, whose start is a boundary as the start of any
// input is. A secret is therefore never joined across a Flush. The Report keeps
// counting across inputs.
//
// An error from the destination is returned by that call and every later one.
type Writer struct {
	dst io.Writer
	in  streamBuf
	out []byte
	err error
}

// NewWriter returns a Writer that redacts into dst.
func (r *Redactor) NewWriter(dst io.Writer) *Writer {
	return &Writer{dst: dst, in: newStreamBuf(&engine{r: r})}
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	n := 0
	for w.err == nil && n < len(p) {
		if w.in.n == len(w.in.buf) {
			w.in.compact() // the windows it held are decided
		}
		k := copy(w.in.buf[w.in.n:], p[n:])
		w.in.n += k
		n += k
		for w.err == nil && w.in.ready() {
			w.emit(false)
		}
	}
	return n, w.err
}

// Flush redacts and writes everything held back, and ends the input.
func (w *Writer) Flush() error {
	for w.err == nil {
		w.emit(true)
		if w.in.from == w.in.n {
			w.in.eng.restart()
			w.in.reset()
			break
		}
	}
	return w.err
}

// Report returns the counts so far.
func (w *Writer) Report() Report { return w.in.eng.rep.clone() }

func (w *Writer) emit(final bool) {
	w.out = w.in.step(w.out[:0], final)
	if len(w.out) > 0 {
		_, w.err = w.dst.Write(w.out)
	}
}

// restart readies e for a new input after its final window, keeping the
// report, the window span and the scratch slices.
func (e *engine) restart() {
	clear(e.resume)
	*e = engine{r: e.r, rep: e.rep, order: e.order, discard: e.discard, span: e.span,
		cands: e.cands[:0], skip: e.skip, memo: e.memo, resume: e.resume, pending: e.pending[:0], open: e.open[:0]}
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
