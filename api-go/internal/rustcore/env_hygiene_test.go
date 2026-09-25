package rustcore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Rust children inherit the environment unchanged except on Linux, where glibc arenas
// are capped.
func TestCoreCommandEnvHygiene(t *testing.T) {
	cmd := coreCommandContext(context.Background(), "search", "x")
	if runtime.GOOS != "linux" && cmd.Env != nil {
		t.Fatalf("non-Linux core command must inherit the environment, got %d vars", len(cmd.Env))
	}
	if runtime.GOOS == "linux" && os.Getenv("MALLOC_ARENA_MAX") == "" {
		found := false
		for _, kv := range cmd.Env {
			found = found || kv == "MALLOC_ARENA_MAX=2"
		}
		if !found {
			t.Fatal("Linux core command must cap glibc arenas")
		}
	}
}

// The arena cap is for the Rust core alone. Subcommands that run external programs (a
// managed or verification command, a live language server) inherit the operator's
// environment unchanged, because the core passes its environment to what it spawns:
// with the Linux answer stood in, a managed command's core never sees the variable,
// while a search core does.
func TestArenaCapNeverReachesExternalPrograms(t *testing.T) {
	withPool(t, 64<<20)
	t.Setenv("MALLOC_ARENA_MAX", "")
	os.Unsetenv("MALLOC_ARENA_MAX")
	prev := childEnv
	childEnv = func() []string { return append(os.Environ(), "MALLOC_ARENA_MAX=2") }
	t.Cleanup(func() { childEnv = prev })
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\nprintf '%s' \"${MALLOC_ARENA_MAX-unset}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)

	out, err := runCoreCtx(context.Background(), "search", "x")
	if err != nil || string(out) != "2" {
		t.Fatalf("a search core gets the cap: %q %v", out, err)
	}
	for _, sub := range []string{"run-managed-command", "run-verification-profile", "run-verification-command"} {
		stdout, _, _, err := runBoundedCmd(coreCommandContext(context.Background(), sub, "x"))
		if err != nil || string(stdout) != "unset" {
			t.Fatalf("%s must inherit the environment unchanged: %q %v", sub, stdout, err)
		}
	}
	out, err = runCoreCtx(context.Background(), "lsp-hover", "x")
	if err != nil || string(out) != "unset" {
		t.Fatalf("a live LSP session must inherit the environment unchanged: %q %v", out, err)
	}
	for _, sub := range []string{"normalize-lsp-references", "symbolgraph", "goal"} {
		if env := coreCommandContext(context.Background(), sub).Env; !strings.Contains(strings.Join(env, "\n"), "MALLOC_ARENA_MAX=2") {
			t.Fatalf("%s runs no external program and keeps the cap", sub)
		}
	}
}
