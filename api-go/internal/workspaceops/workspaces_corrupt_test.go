package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// WS-FIX-14: a corrupt <dataDir>/workspaces.json must name the file and the parse
// offset, so an operator repairing a registry is told which file and where in it.
// Before the row, readJSON returned json.Unmarshal's bare error, so the message was
// "unexpected end of JSON input" or "invalid character 'x' looking for beginning of
// value" with no path and no position.

// corruptRegistry writes body to the registry and returns the data dir and path.
func corruptRegistry(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workspaces.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write corrupt registry: %v", err)
	}
	return dir, path
}

// assertNamesFileAndOffset asserts that err names path and carries the byte offset
// wantOffset. wantOffset is computed by the test from the fixture string itself
// (strings.Index), never from the decoder under test.
func assertNamesFileAndOffset(t *testing.T, err error, path string, wantOffset int, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error for a corrupt registry", context)
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Fatalf("%s: error does not name the file %q: %v", context, path, err)
	}
	if !strings.Contains(msg, "offset") {
		t.Fatalf("%s: error does not name an offset: %v", context, err)
	}
	if !strings.Contains(msg, strconv.Itoa(wantOffset)) {
		t.Fatalf("%s: error does not carry the parse offset %d: %v", context, wantOffset, err)
	}
}

// TestCorruptWorkspacesRegistryNamesTheFileAndOffset is the row's core assertion. Each
// fixture is corrupt in a different way and the expected offset is the index of the
// offending byte in the fixture, read off the string.
func TestCorruptWorkspacesRegistryNamesTheFileAndOffset(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		offset int // index of the offending byte, computed with strings.Index
		at     string
	}{
		{
			// valid prefix, then a byte that cannot start a value
			name:   "bad token after a valid element",
			body:   `[{"workspace_id":"ws-a","name":"Alpha"},oops]`,
			offset: strings.Index(`[{"workspace_id":"ws-a","name":"Alpha"},oops]`, "oops") + 1,
			at:     "the second element's first byte",
		},
		{
			name:   "truncated object",
			body:   `[{"workspace_id":"ws-a"`,
			offset: len(`[{"workspace_id":"ws-a"`),
			at:     "the end of the truncated object",
		},
		{
			name:   "truncated array",
			body:   `[{"workspace_id":"ws-a","name":"Alpha"},`,
			offset: len(`[{"workspace_id":"ws-a","name":"Alpha"},`),
			at:     "the end of the truncated array",
		},
		{
			name:   "well-formed but wrongly typed",
			body:   `{"workspace_id":"ws-a","name":"Alpha"}`,
			offset: 1,
			at:     "the '{' where an array element was expected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := corruptRegistry(t, tc.body)

			// both readers of the registry must carry the same message
			_, listErr := ListWorkspaces(dir)
			assertNamesFileAndOffset(t, listErr, path, tc.offset, "ListWorkspaces")

			mark, statErr := statMark(path)
			if statErr != nil {
				t.Fatalf("statMark: %v", statErr)
			}
			_, recErr := registryRecords(dir, mark, registryNow())
			assertNamesFileAndOffset(t, recErr, path, tc.offset, "registryRecords")
		})
	}
}

// TestMissingWorkspacesRegistryIsNotAnError pins the sentinel the wrap must not break:
// ListWorkspaces and registryRecords both treat a missing file as an empty registry,
// because both branch on errors.Is(err, os.ErrNotExist).
func TestMissingWorkspacesRegistryIsNotAnError(t *testing.T) {
	dir := t.TempDir()

	got, err := ListWorkspaces(dir)
	if err != nil {
		t.Fatalf("a missing registry is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a missing registry lists %d workspaces, want 0: %+v", len(got), got)
	}

	mark, statErr := statMark(filepath.Join(dir, "workspaces.json"))
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("statMark on a missing registry = %v, want os.ErrNotExist", statErr)
	}
	byID, err := registryRecords(dir, mark, registryNow())
	if err != nil {
		t.Fatalf("a missing registry is not an error: %v", err)
	}
	if byID != nil {
		t.Fatalf("a missing registry yields %v, want nil", byID)
	}
}

// TestReadJSONKeepsNotExistAsItsOwnError asserts the sentinel at the helper, so every
// other readJSON caller (about fifty files) keeps its errors.Is(err, os.ErrNotExist)
// branch working after the decode wrap.
func TestReadJSONKeepsNotExistAsItsOwnError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")

	var target map[string]any
	err := readJSON(missing, &target)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readJSON on a missing file = %v, want it to satisfy errors.Is(err, os.ErrNotExist)", err)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("readJSON on a missing file = %v, want os.IsNotExist to hold too", err)
	}
}

// TestReadJSONNamesTheFileAndOffsetOnADecodeError is the helper-level assertion that the
// fix lives in readJSON: a corrupt file produces a message naming it, whatever the
// target type is.
func TestReadJSONNamesTheFileAndOffsetOnADecodeError(t *testing.T) {
	body := `[{"n":1},]`
	_, path := corruptRegistry(t, body)

	var target []struct {
		N int `json:"n"`
	}
	err := readJSON(path, &target)
	// the offending byte is the ']' where a value was expected; its index is the last
	// byte of the fixture, which the test reads off the string
	assertNamesFileAndOffset(t, err, path, strings.Index(body, "]"), "readJSON")
}
