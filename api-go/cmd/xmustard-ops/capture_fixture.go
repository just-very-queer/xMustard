package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/redact"
)

const (
	fixtureMaxFiles = 100
	fixtureMaxFile  = 1 << 20
	fixtureMaxTotal = 10 << 20
)

type capturedFixture struct {
	Client  string          `json:"client"`
	Version string          `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// runCaptureFixture records one supplied JSON payload. Capture is opt-in and the
// stored payload is redacted before it reaches disk.
func runCaptureFixture(args []string) int {
	flags := flag.NewFlagSet("capture-fixture", flag.ContinueOnError)
	input := flags.String("input", "", "JSON adapter payload to capture")
	outDir := flags.String("out-dir", "", "fixture directory")
	client := flags.String("client", "", "client name")
	version := flags.String("version", "", "client version")
	enabled := flags.Bool("enable", false, "explicitly enable fixture capture")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if !*enabled || *input == "" || *outDir == "" || *client == "" || *version == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: xmustard-ops capture-fixture --enable --input FILE --out-dir DIR --client NAME --version VERSION")
		return 2
	}
	inputFile, err := os.Open(*input)
	if err != nil {
		return captureFixtureError(err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(inputFile, fixtureMaxFile+1))
	closeErr := inputFile.Close()
	if readErr != nil {
		return captureFixtureError(readErr)
	}
	if closeErr != nil {
		return captureFixtureError(closeErr)
	}
	if len(payload) > fixtureMaxFile {
		return captureFixtureError(errors.New("fixture input exceeds per-file quota"))
	}
	if !json.Valid(payload) {
		return captureFixtureError(errors.New("fixture input must be valid JSON"))
	}
	sanitizer := redact.New(redact.WithEnv(redact.SecretEnv(os.Environ())...), redact.WithPII())
	clean, _ := sanitizer.Bytes(payload)
	payloadJSON := json.RawMessage(clean)
	fixture, err := json.Marshal(capturedFixture{Client: *client, Version: *version, Payload: payloadJSON})
	if err != nil {
		return captureFixtureError(err)
	}
	fixture, _ = sanitizer.Bytes(fixture)
	if len(fixture) > fixtureMaxFile {
		return captureFixtureError(errors.New("fixture exceeds per-file quota"))
	}
	if !filepath.IsAbs(*outDir) {
		return captureFixtureError(errors.New("fixture directory must be absolute"))
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return captureFixtureError(err)
	}
	info, err := os.Lstat(*outDir)
	if err != nil {
		return captureFixtureError(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return captureFixtureError(errors.New("fixture directory must be private"))
	}
	entries, err := os.ReadDir(*outDir)
	if err != nil {
		return captureFixtureError(err)
	}
	var total int64
	files := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return captureFixtureError(err)
		}
		if !info.Mode().IsRegular() {
			return captureFixtureError(errors.New("fixture directory contains a non-regular JSON entry"))
		}
		files++
		total += info.Size()
	}
	if files >= fixtureMaxFiles || total+int64(len(fixture)) > fixtureMaxTotal {
		return captureFixtureError(errors.New("fixture retention quota reached"))
	}
	for sequence := 1; ; sequence++ {
		path := filepath.Join(*outDir, fmt.Sprintf("fixture-%06d.json", sequence))
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return captureFixtureError(err)
		}
		tmp, err := os.CreateTemp(*outDir, ".fixture-*.tmp")
		if err != nil {
			return captureFixtureError(err)
		}
		tmpName := tmp.Name()
		fail := func(err error) int {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return captureFixtureError(err)
		}
		if err := tmp.Chmod(0o600); err != nil {
			return fail(err)
		}
		if _, err := tmp.Write(append(fixture, '\n')); err != nil {
			return fail(err)
		}
		if err := tmp.Sync(); err != nil {
			return fail(err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return captureFixtureError(err)
		}
		if err := os.Link(tmpName, path); err != nil {
			_ = os.Remove(tmpName)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return captureFixtureError(err)
		}
		_ = os.Remove(tmpName)
		fmt.Println(path)
		return 0
	}
}

func captureFixtureError(err error) int {
	fmt.Fprintln(os.Stderr, "capture-fixture:", err)
	return 1
}
