package rustcore

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestRunManagedCommandExecutesDirectArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed command test expects a POSIX shell")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := RunManagedCommand(ctx, t.TempDir(), 2, []string{
		"sh",
		"-lc",
		"printf '%s|%s\\n' \"$1\" \"$2\"",
		"ignored-script-name",
		"one two",
		"three",
	})
	if err != nil {
		t.Fatalf("run managed command: %v", err)
	}
	if !result.Success || result.TimedOut {
		t.Fatalf("expected success without timeout: %#v", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %#v", result)
	}
	if result.StdoutExcerpt != "one two|three\n" {
		t.Fatalf("unexpected stdout excerpt: %#v", result)
	}
}

func TestRunManagedCommandReportsTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed command test expects a POSIX shell")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := RunManagedCommand(ctx, t.TempDir(), 1, []string{"sh", "-lc", "sleep 2"})
	if err != nil {
		t.Fatalf("run managed command with timeout: %v", err)
	}
	if result.Success || !result.TimedOut {
		t.Fatalf("expected timeout result: %#v", result)
	}
	if result.DurationMS < 1000 {
		t.Fatalf("expected timeout duration to be recorded: %#v", result)
	}
}
