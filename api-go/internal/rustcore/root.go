package rustcore

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// coreCallTimeout bounds a single Go→Rust bridge call so a hung child can't wedge
	// the HTTP/MCP handler indefinitely.
	coreCallTimeout = 120 * time.Second
	// maxCoreStdout caps the result buffer so a flooding child can't OOM the API
	// (large but finite — a graph/search result beyond this is itself a problem).
	maxCoreStdout = 64 << 20 // 64 MiB
	maxCoreStderr = 64 << 10 // 64 KiB
)

// capWriter buffers up to `max` bytes (dropping the overflow but never blocking the
// child, and flagging that it happened) so bridge output is bounded.
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil // pretend full write so the child doesn't block on a full pipe
	}
	return c.buf.Write(p)
}

// runCoreContext is the single hardened bridge runner: binary-first resolution,
// context timeout, bounded stdout/stderr, full server-side logging on failure, and a
// SANITIZED error to the caller (no raw child stderr / host paths leaked). The rust
// bin parses args positionally (args.next()), not via a flag parser, so a
// `-`-leading query/path/seed is read literally — no `--` delimiter is required.
func runCoreContext(sub string, args ...string) ([]byte, error) {
	return runCoreCtx(context.Background(), sub, args...)
}

// runCoreCtx is runCoreContext honoring a caller context (whichever deadline — the
// caller's or coreCallTimeout — fires first), for bridges that already thread a ctx.
func runCoreCtx(parent context.Context, sub string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, coreCallTimeout)
	defer cancel()
	cmd := coreCommandContext(ctx, sub, args...)
	out := &capWriter{max: maxCoreStdout}
	errb := &capWriter{max: maxCoreStderr}
	cmd.Stdout = out
	cmd.Stderr = errb
	runErr := cmd.Run()
	if out.over {
		log.Printf("rust-core %s: stdout exceeded %d bytes (dropped)", sub, maxCoreStdout)
		return nil, fmt.Errorf("rust-core %s: output too large", sub)
	}
	if runErr != nil {
		log.Printf("rust-core %s failed: %v: %s", sub, runErr, errb.buf.String())
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("rust-core %s timed out", sub)
		}
		return nil, fmt.Errorf("rust-core %s failed", sub)
	}
	return out.buf.Bytes(), nil
}

// runBoundedCmd runs an already-built rust-core command with BOUNDED stdout/stderr
// capture (same caps as runCoreCtx: 64 MiB / 64 KiB), for the few bridge helpers
// that must keep their own command + timeout construction (e.g. managed/verification
// commands whose own timeout exceeds coreCallTimeout). It returns the captured
// stdout, the (bounded) stderr for error context, whether stdout overflowed the cap,
// and the raw run error so callers can inspect exit codes. This replaces the
// unbounded bytes.Buffer captures those helpers used (XM-PRO-005).
func runBoundedCmd(cmd *exec.Cmd) (stdout []byte, stderr string, over bool, err error) {
	out := &capWriter{max: maxCoreStdout}
	errb := &capWriter{max: maxCoreStderr}
	cmd.Stdout = out
	cmd.Stderr = errb
	err = cmd.Run()
	return out.buf.Bytes(), errb.buf.String(), out.over, err
}

func rustCoreDir() string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "../rust-core"
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "rust-core"))
}

// coreCommand builds a command to run the rust core for one subcommand. It
// prefers the prebuilt release binary (fast) and falls back to `cargo run`
// (debug) when it is not present — so heavy commands like symbolgraph/search
// stay responsive in production.
// coreInvocation resolves how to invoke the rust core for one subcommand,
// binary-first: an explicit XMUSTARD_CORE_BIN, then a `xmustard-core` on PATH, then
// the source-relative release binary, and only as a last resort `cargo run` (which
// requires the source tree + Cargo at runtime and is wrong for an installed,
// no-Docker deployment — XM-NEW-016). It returns the program, full argv, and the
// working dir ("" = inherit).
func coreInvocation(sub string, args ...string) (name string, full []string, dir string) {
	if bin := strings.TrimSpace(os.Getenv("XMUSTARD_CORE_BIN")); bin != "" {
		return bin, append([]string{sub}, args...), ""
	}
	if bin, err := exec.LookPath("xmustard-core"); err == nil {
		return bin, append([]string{sub}, args...), ""
	}
	d := rustCoreDir()
	bin := filepath.Join(d, "target", "release", "xmustard-core")
	if info, err := os.Stat(bin); err == nil && !info.IsDir() {
		return bin, append([]string{sub}, args...), d
	}
	return "cargo", append([]string{"run", "--quiet", "--bin", "xmustard-core", "--", sub}, args...), d
}

func coreCommand(sub string, args ...string) *exec.Cmd {
	name, full, dir := coreInvocation(sub, args...)
	cmd := exec.Command(name, full...)
	cmd.Dir = dir
	return cmd
}

// coreCommandContext is coreCommand with a context, so callers can bound a hung
// child with a deadline (and route every Rust invocation through one binary-first
// resolver instead of hard-coding `cargo run`).
func coreCommandContext(ctx context.Context, sub string, args ...string) *exec.Cmd {
	name, full, dir := coreInvocation(sub, args...)
	cmd := exec.CommandContext(ctx, name, full...)
	cmd.Dir = dir
	return cmd
}
