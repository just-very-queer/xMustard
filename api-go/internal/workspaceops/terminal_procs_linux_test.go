//go:build linux

package workspaceops

import (
	"os"
	"strconv"
	"testing"
)

// terminalProcCwd returns pid's working directory; the teardown tests use it to
// find leftovers without the sweep. darwin's version needs cgo, so it lives with
// the other proc_pidinfo(3) calls.
func terminalProcCwd(pid int) (string, bool) {
	cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return cwd, err == nil
}

// The command name in /proc/<pid>/stat is free text, so a name holding spaces and
// ')' must not shift the numeric fields.
func TestParseProcStat(t *testing.T) {
	stat := []byte("4242 (we ird) (x) S 17 4242 4200 34817 4242 4194560 1 0 0 0 0 0 0 0 20 0 1 0 987654 1 2 3\n")
	proc, ok := parseProcStat(4242, stat)
	if !ok {
		t.Fatal("stat line should parse")
	}
	want := terminalProc{pid: 4242, ppid: 17, pgid: 4242, sid: 4200, start: 987654}
	if proc != want {
		t.Fatalf("got %+v, want %+v", proc, want)
	}

	zombie, ok := parseProcStat(7, []byte("7 (sh) Z 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 55 0 0\n"))
	if !ok || !zombie.exited {
		t.Fatalf("a Z-state process is exited, got %+v ok=%v", zombie, ok)
	}
	if _, ok := parseProcStat(8, []byte("8 (truncated) S 1 8")); ok {
		t.Fatal("a truncated stat line must be rejected")
	}
}
