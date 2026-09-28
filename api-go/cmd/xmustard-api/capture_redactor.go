package main

import (
	"errors"
	"io"
	"log"
	"runtime/debug"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// Capture redaction (WS-FIX-03; PAR-CTX-01 "Redaction applies"). Every captured tool
// output streams through the WS-05 secret rules (redact.Writer) on its way into the
// spool, so an original is retained, paged, searched and projected only in redacted
// form. The rules are the ones why_failed applies to command output
// (workspaceops.OutputRedactor): the default secret patterns, the key-aware detector
// and the literal values of this daemon's secret environment variables. The Writer
// buffers 128 KiB and decides it 8 KiB at a time, each window read with the 33 KiB
// lookahead after it, so it never holds the stream, and a window's scratch and output
// do not grow with the density of secrets. On 16 MiB of the densest secret units, in
// the decoder's 32 KiB writes, its live heap peaks at 0.4 MiB at most (an 8-byte
// secret environment value repeated; redact.TestWriterMemoryIsBounded and
// TestCaptureRedactorFitsTheCaptureWindow), under a quarter of captureWindowBytes.
// The decoder flushes it at every section boundary, so each output string is
// redacted as one input. Every path the capture names is checked against the
// secret-path denylist once the body is read (evidence.ErrSecretPath, 422
// secret_path).

// captureRedactor wraps the capture spool writer with the streaming secret redactor.
// Every build sets it (the xmustard_e2e build chains a test marker in front, see
// capture_redactor_e2e.go). Capture refuses while it is nil (503
// redaction_unavailable) and when it fails (503 redaction_failed, see failClosed).
var captureRedactor = func(w io.Writer) evidence.StreamRedactor {
	return workspaceops.OutputRedactor().NewWriter(w)
}

// errRedactionFailed refuses a capture whose redactor failed.
var errRedactionFailed = errors.New("capture redaction failed; nothing was retained")

// failClosed runs a capture's redactor so that its failure refuses the capture: a
// panic becomes errRedactionFailed, which Observe returns after discarding the spool,
// and every later call returns it, so no byte written after the failure reaches the
// spool.
type failClosed struct {
	next evidence.StreamRedactor
	err  error
}

func (f *failClosed) Write(p []byte) (n int, err error) {
	if f.err != nil {
		return 0, f.err
	}
	defer f.catch(&err)
	return f.next.Write(p)
}

func (f *failClosed) Flush() (err error) {
	if f.err != nil {
		return f.err
	}
	defer f.catch(&err)
	return f.next.Flush()
}

// catch is deferred by Write and Flush.
func (f *failClosed) catch(err *error) {
	if v := recover(); v != nil {
		log.Printf("evidence capture: redactor failed: %v\n%s", v, debug.Stack())
		f.err, *err = errRedactionFailed, errRedactionFailed
	}
}
