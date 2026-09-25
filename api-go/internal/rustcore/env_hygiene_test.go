package rustcore

import (
	"context"
	"os"
	"runtime"
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
