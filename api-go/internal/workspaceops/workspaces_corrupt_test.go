package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
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

// assertNamesFileAndOffset asserts that err names path and carries exactly the byte
// offset wantOffset. wantOffset is computed by the test from the fixture string itself
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
	// The offset is compared as a parsed integer, not a substring: "at offset 1" is a
	// prefix of "at offset 10", so a low expected offset would otherwise accept a
	// wrong one.
	got, ok := parseOffset(msg)
	if !ok {
		t.Fatalf("%s: error carries no offset: %v", context, err)
	}
	if got != wantOffset {
		t.Fatalf("%s: error reports offset %d, want %d: %v", context, got, wantOffset, err)
	}
}

// parseOffset extracts the integer after the last "at offset " in msg.
func parseOffset(msg string) (int, bool) {
	const marker = "at offset "
	i := strings.LastIndex(msg, marker)
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len(marker):]
	n, digits := 0, 0
	for ; digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9'; digits++ {
		n = n*10 + int(rest[digits]-'0')
	}
	if digits == 0 {
		return 0, false
	}
	return n, true
}

// TestCorruptWorkspacesRegistryNamesTheFileAndOffset is the row's core assertion. Each
// fixture is corrupt in a different way and the expected offset is read off the fixture
// string. json's offsets are one-based, so a byte index is raised by one.
func TestCorruptWorkspacesRegistryNamesTheFileAndOffset(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		offset int // 1-based byte offset, computed with strings.Index or len
	}{
		{
			// valid prefix, then a byte that cannot start a value
			name:   "bad token after a valid element",
			body:   `[{"workspace_id":"ws-a","name":"Alpha"},oops]`,
			offset: strings.Index(`[{"workspace_id":"ws-a","name":"Alpha"},oops]`, "oops") + 1,
		},
		{
			name:   "truncated object",
			body:   `[{"workspace_id":"ws-a"`,
			offset: len(`[{"workspace_id":"ws-a"`),
		},
		{
			name:   "truncated array",
			body:   `[{"workspace_id":"ws-a","name":"Alpha"},`,
			offset: len(`[{"workspace_id":"ws-a","name":"Alpha"},`),
		},
		{
			name:   "well-formed but wrongly typed",
			body:   `{"workspace_id":"ws-a","name":"Alpha"}`,
			offset: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := corruptRegistry(t, tc.body)

			_, listErr := ListWorkspaces(dir)
			assertNamesFileAndOffset(t, listErr, path, tc.offset, "ListWorkspaces")

			mark, statErr := statMark(path)
			if statErr != nil {
				t.Fatalf("statMark: %v", statErr)
			}
			_, recErr := registryRecords(dir, mark, registryNow())
			assertNamesFileAndOffset(t, recErr, path, tc.offset, "registryRecords")

			// Both readers go through the one loader, so a message that differed
			// between them would mean the fix had leaked into a caller.
			if listErr.Error() != recErr.Error() {
				t.Fatalf("the two readers disagree:\n ListWorkspaces: %v\n registryRecords: %v", listErr, recErr)
			}
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
	// The offending byte is the ']' where a value was expected. json.SyntaxError.Offset
	// is one-based, so the 0-based index of that byte plus one is what the message
	// carries; the index comes off the fixture string, not from the decoder.
	assertNamesFileAndOffset(t, err, path, strings.Index(body, "]")+1, "readJSON")
}
