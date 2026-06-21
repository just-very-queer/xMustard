package rustcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

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
