//go:build !xmustard_e2e

package main

import (
	"io"
	"os"
	"sync"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/redact"
)

// The production capture redactor (WS-23 wires WS-05's streaming redactor): every
// captured original passes through it on its way into the spool, so what is retained,
// paged and searched is redacted (PAR-CTX-01 "Redaction applies"). It holds the default
// secret rules plus the literal values of the daemon's own secret-bearing environment
// variables (redact.SecretEnv), read once. Tests that are not about redaction install
// another redactor (withCaptureRedactor); the build-tagged e2e redactor replaces this
// file in -tags xmustard_e2e builds.

var captureRedactorRules = sync.OnceValue(func() *redact.Redactor {
	return redact.New(redact.WithEnv(redact.SecretEnv(os.Environ())...))
})

func init() {
	captureRedactor = func(w io.Writer) evidence.StreamRedactor { return captureRedactorRules().NewWriter(w) }
}
