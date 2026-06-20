package rustcore

import (
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
func coreCommand(sub string, args ...string) *exec.Cmd {
	// An installed (e.g. Homebrew) deployment has no source tree, so honor an
	// explicit path to the xmustard-core binary, or one found on PATH, before
	// falling back to the source-relative release binary and finally `cargo run`.
	if bin := strings.TrimSpace(os.Getenv("XMUSTARD_CORE_BIN")); bin != "" {
		cmd := exec.Command(bin, append([]string{sub}, args...)...)
		return cmd
	}
	if bin, err := exec.LookPath("xmustard-core"); err == nil {
		return exec.Command(bin, append([]string{sub}, args...)...)
	}
	dir := rustCoreDir()
	bin := filepath.Join(dir, "target", "release", "xmustard-core")
	if info, err := os.Stat(bin); err == nil && !info.IsDir() {
		cmd := exec.Command(bin, append([]string{sub}, args...)...)
		cmd.Dir = dir
		return cmd
	}
	full := append([]string{"run", "--quiet", "--bin", "xmustard-core", "--", sub}, args...)
	cmd := exec.Command("cargo", full...)
	cmd.Dir = dir
	return cmd
}
