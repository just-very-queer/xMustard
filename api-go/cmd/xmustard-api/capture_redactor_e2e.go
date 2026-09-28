//go:build xmustard_e2e

package main

import (
	"bytes"
	"io"
	"regexp"

	"xmustard/api-go/internal/evidence"
)

// Test-only capture redactor for the Pi adapter e2e (scripts/e2e/pi-adapter.sh builds
// the API with -tags xmustard_e2e). It is not a secret scanner and no production
// build contains it: production builds wire the WS-05 streaming redactor
// (capture_redactor.go, excluded by the tag). It replaces one fixed marker, e2eSecret,
// so the e2e can prove that Pi's built-in tool output passes through the redaction
// seam before it is retained or projected.

var e2eSecret = regexp.MustCompile(`XM_E2E_SECRET_[A-Za-z0-9]+`)

func init() {
	captureRedactor = func(w io.Writer) evidence.StreamRedactor { return &e2eRedactor{dst: w} }
}

// e2eRedactor redacts whole lines. A line longer than e2eHold is written in pieces,
// keeping the last 256 bytes back so a marker split across writes is still seen.
type e2eRedactor struct {
	dst     io.Writer
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
	_, err := r.dst.Write(e2eSecret.ReplaceAll(r.pending[:n], []byte("[REDACTED:e2e]")))
	r.pending = append(r.pending[:0], r.pending[n:]...)
	return err
}

func (r *e2eRedactor) Flush() error { return r.emit(len(r.pending)) }
