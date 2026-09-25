package rustcore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"xmustard/api-go/internal/budget"
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

// runCoreCtx is the single hardened bridge runner: binary-first resolution,
// context timeout, bounded stdout/stderr, full server-side logging on failure, and a
// SANITIZED error to the caller (no raw child stderr / host paths leaked). The rust
// bin parses args positionally (args.next()), not via a flag parser, so a
// `-`-leading query/path/seed is read literally — no `--` delimiter is required.
// The child is killed when the caller's ctx is cancelled or coreCallTimeout elapses,
// whichever comes first.
//
// Admission: the child takes a slot from budget.Children, and its captured output is
// reserved against budget.TransientBytes in chunks BEFORE it is buffered. When ctx
// carries a request scope the reservation is held until the request finishes (through
// decode and response construction); otherwise it is released on return. A refused
// reservation kills the child and returns budget.ErrOverloaded.
func runCoreCtx(parent context.Context, sub string, args ...string) ([]byte, error) {
	release, err := budget.Children.Acquire(parent)
	if err != nil {
		return nil, fmt.Errorf("rust-core %s: %w", sub, err)
	}
	defer release()
	scope, owned := budget.ScopeFor(parent)
	if owned {
		defer scope.Close()
	}
	ctx, cancel := context.WithTimeout(parent, coreCallTimeout)
	defer cancel()
	cmd := coreCommandContext(ctx, sub, args...)
	cmd.WaitDelay = 2 * time.Second
	out := budget.NewCaptureWriter(scope, maxCoreStdout)
	errb := budget.NewCaptureWriter(scope, maxCoreStderr)
	// first overflow/refusal cancels ctx, which kills the child (CommandContext)
	out.OnStop, errb.OnStop = cancel, cancel
	cmd.Stdout = out
	cmd.Stderr = errb
	runErr := runTracked(cmd)
	// Reap any descendant the child left in its group (normal exit included): the
	// owned tree ends with the call.
	KillProcessTree(cmd)
	if out.Refused() || errb.Refused() {
		log.Printf("rust-core %s: output refused by transient budget after %d bytes", sub, out.Len())
		cause := out.RefusalErr()
		if cause == nil {
			cause = errb.RefusalErr()
		}
		if !errors.Is(cause, budget.ErrAdmissionLimit) {
			cause = budget.ErrOverloaded
		}
		return nil, fmt.Errorf("rust-core %s: %w", sub, cause)
	}
	if out.Over() {
		log.Printf("rust-core %s: stdout exceeded %d bytes (dropped)", sub, maxCoreStdout)
		return nil, fmt.Errorf("rust-core %s: output too large", sub)
	}
	if runErr != nil {
		log.Printf("rust-core %s failed: %v: %s", sub, runErr, errb.String())
		if parent.Err() != nil {
			return nil, fmt.Errorf("rust-core %s: %w", sub, parent.Err())
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("rust-core %s timed out", sub)
		}
		return nil, fmt.Errorf("rust-core %s failed", sub)
	}
	// Without a request scope (owned), the reservation ends when this call returns;
	// with one, it is held until the request's response has been written, and the
	// caller's decode and response construction (about two more copies of the output)
	// are admitted now, before they are allocated.
	if !owned {
		if err := scope.Acquire(2 * int64(out.Len())); err != nil {
			if !errors.Is(err, budget.ErrAdmissionLimit) {
				err = budget.ErrOverloaded
			}
			return nil, fmt.Errorf("rust-core %s: %w", sub, err)
		}
	}
	return out.Bytes(), nil
}

// runBoundedCmd runs an already-built rust-core command with BOUNDED stdout/stderr
// capture (same caps as runCoreCtx: 64 MiB / 64 KiB), for the few bridge helpers
// that must keep their own command + timeout construction (e.g. managed/verification
// commands whose own timeout exceeds coreCallTimeout). It takes a helper-child slot and
// reserves captured bytes against the shared pool as they arrive; a refused
// reservation returns budget.ErrOverloaded (over=false). It returns the captured
// stdout, the (bounded) stderr for error context, whether stdout overflowed the cap,
// and the raw run error so callers can inspect exit codes.
func runBoundedCmd(cmd *exec.Cmd) (stdout []byte, stderr string, over bool, err error) {
	release, aerr := budget.Children.Acquire(context.Background())
	if aerr != nil {
		return nil, "", false, aerr
	}
	defer release()
	scope := budget.NewScope(nil)
	defer scope.Close()
	out := budget.NewCaptureWriter(scope, maxCoreStdout)
	errb := budget.NewCaptureWriter(scope, maxCoreStderr)
	// first overflow/refusal kills the child (and its process group when it has one)
	out.OnStop = func() { KillProcessTree(cmd) }
	errb.OnStop = out.OnStop
	cmd.Stdout = out
	cmd.Stderr = errb
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	if cmd.SysProcAttr == nil {
		isolateProcessGroup(cmd)
	}
	err = runTracked(cmd)
	KillProcessTree(cmd)
	if out.Refused() || errb.Refused() {
		return nil, errb.String(), false, budget.ErrOverloaded
	}
	return out.Bytes(), errb.String(), out.Over(), err
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
	IsolateProcessTree(cmd)
	return cmd
}
