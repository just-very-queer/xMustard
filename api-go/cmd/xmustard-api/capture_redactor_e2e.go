//go:build xmustard_e2e

package main

import (
	"bytes"
	"io"
	"regexp"

	"xmustard/api-go/internal/evidence"
)

// Test-only capture marker for the Pi adapter e2e (scripts/e2e/pi-adapter.sh builds
// the API with -tags xmustard_e2e). It is chained in front of the production
// redactor, which still sees every byte: it replaces one fixed marker, e2eSecret, that
// no production rule matches, so the e2e can prove that Pi's built-in tool output
// passes through the redaction seam by a marker of its own choosing, besides the
// production rules' "[REDACTED:<rule>]" markers. No production build contains it.

var e2eSecret = regexp.MustCompile(`XM_E2E_SECRET_[A-Za-z0-9]+`)

func init() {
	production := captureRedactor
	captureRedactor = func(w io.Writer) evidence.StreamRedactor { return &e2eRedactor{next: production(w)} }
}

// e2eRedactor redacts whole lines. A line longer than e2eHold is written in pieces,
// keeping the last 256 bytes back so a marker split across writes is still seen.
type e2eRedactor struct {
	next    evidence.StreamRedactor
	pending []byte
}

const e2eHold = 64 << 10

func (r *e2eRedactor) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	if i := bytes.LastIndexByte(r.pending, '\n'); i >= 0 {
		if err := r.emit(i + 1); err != nil {
			return 0, err
		}
	}
	if len(r.pending) > e2eHold {
		if err := r.emit(len(r.pending) - 256); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (r *e2eRedactor) emit(n int) error {
	_, err := r.next.Write(e2eSecret.ReplaceAll(r.pending[:n], []byte("[REDACTED:e2e]")))
	r.pending = append(r.pending[:0], r.pending[n:]...)
	return err
}

func (r *e2eRedactor) Flush() error {
	if err := r.emit(len(r.pending)); err != nil {
		return err
	}
	return r.next.Flush()
}
