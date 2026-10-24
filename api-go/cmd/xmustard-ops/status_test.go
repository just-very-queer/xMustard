package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRepositoryPathStatusCallsRustIndexWithWorktreeRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "src", "nested.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	output, err := repositoryPathStatus(file, func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(`{"indexed":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != `{"indexed":true}` {
		t.Fatalf("unexpected core output %s", output)
	}
	want := []string{"path-status", root, "--path", "src/nested.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunIndex args = %#v, want %#v", got, want)
	}
}
