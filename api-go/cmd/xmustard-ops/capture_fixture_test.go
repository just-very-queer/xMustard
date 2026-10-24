package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureFixtureRequiresExplicitOptInAndRedacts(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	out := filepath.Join(dir, "fixtures")
	token := "ghp_" + "abcdefghijklmnopqrst" + "uvwxyz0123456789AB"
	payload := `{"authorization":"Bearer ` + token + `","email":"person@example.com","ok":true}`
	if err := os.WriteFile(input, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--input", input, "--out-dir", out, "--client", "codex", "--version", "1.2.3"}
	if got := runCaptureFixture(args); got == 0 {
		t.Fatal("capture without --enable succeeded")
	}
	if got := runCaptureFixture(append(args, "--enable")); got != 0 {
		t.Fatalf("capture failed: %d", got)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	dirInfo, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%v", dirInfo.Mode())
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode=%v", info.Mode())
	}
	b, err := os.ReadFile(filepath.Join(out, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), token) || strings.Contains(string(b), "person@example.com") {
		t.Fatalf("secret remained in fixture: %s", b)
	}
	var got struct {
		Client  string          `json:"client"`
		Version string          `json:"version"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.Client != "codex" || got.Version != "1.2.3" || !strings.Contains(string(got.Payload), "REDACTED") {
		t.Fatalf("fixture=%s err=%v", b, err)
	}
}

func TestCaptureFixtureRejectsInvalidAndQuotaExceededInputs(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	out := filepath.Join(dir, "fixtures")
	if err := os.WriteFile(input, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--enable", "--input", input, "--out-dir", out, "--client", "pi", "--version", "1"}
	if got := runCaptureFixture(args); got == 0 {
		t.Fatal("invalid JSON capture succeeded")
	}
	if err := os.WriteFile(input, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < fixtureMaxFiles; i++ {
		name := filepath.Join(out, "existing-"+strings.Repeat("x", i+1)+".json")
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := runCaptureFixture(args); got == 0 {
		t.Fatal("capture beyond file quota succeeded")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(out, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "large.json"), make([]byte, fixtureMaxTotal), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCaptureFixture(args); got == 0 {
		t.Fatal("capture beyond byte quota succeeded")
	}
}
