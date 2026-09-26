//go:build darwin || linux

package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTerminalOpenWriteReadResizeAndLogReplay(t *testing.T) {
	dataDir, workspaceID, session := newTerminalTestSession(t, 100, 28)
	live := liveTerminalSession(t, session.TerminalID)
	t.Cleanup(func() {
		_ = CloseTerminal(workspaceID, session.TerminalID)
	})

	if err := WriteTerminal(workspaceID, session.TerminalID, "IFS= read -r line; printf '__RAW__:%s:__ENDRAW__\\n' \"$line\"\n"); err != nil {
		t.Fatalf("write read command: %v", err)
	}
	if err := WriteTerminal(workspaceID, session.TerminalID, "   \n"); err != nil {
		t.Fatalf("write raw whitespace: %v", err)
	}

	var offset int64
	rawOutput, _ := waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return strings.Contains(content, "__RAW__:   :__ENDRAW__")
	})
	if !strings.Contains(rawOutput, "__RAW__:   :__ENDRAW__") {
		t.Fatalf("expected whitespace payload to round-trip, got %q", rawOutput)
	}

	if err := WriteTerminal(workspaceID, session.TerminalID, "printf '__TERM_OK__\\n'\n"); err != nil {
		t.Fatalf("write terminal marker: %v", err)
	}
	output, _ := waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return strings.Count(content, "__TERM_OK__") >= 2
	})
	if !strings.Contains(output, "__TERM_OK__") {
		t.Fatalf("expected terminal marker, got %q", output)
	}

	if err := WriteTerminal(workspaceID, session.TerminalID, "size=$(stty size); printf '__SIZE1__:%s:__END1__\\n' \"$size\"\n"); err != nil {
		t.Fatalf("write size command: %v", err)
	}
	initialSizeOutput, _ := waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return terminalSizePattern("__SIZE1__", "__END1__").MatchString(content)
	})
	if got := extractTerminalSize(t, initialSizeOutput, "__SIZE1__", "__END1__"); got != "28 100" {
		t.Fatalf("expected initial size 28 100, got %q from %q", got, initialSizeOutput)
	}

	if err := ResizeTerminal(workspaceID, session.TerminalID, 120, 40); err != nil {
		t.Fatalf("resize terminal: %v", err)
	}
	if err := WriteTerminal(workspaceID, session.TerminalID, "size=$(stty size); printf '__SIZE2__:%s:__END2__\\n' \"$size\"\n"); err != nil {
		t.Fatalf("write resized size command: %v", err)
	}
	resizedOutput, _ := waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return terminalSizePattern("__SIZE2__", "__END2__").MatchString(content)
	})
	if got := extractTerminalSize(t, resizedOutput, "__SIZE2__", "__END2__"); got != "40 120" {
		t.Fatalf("expected resized size 40 120, got %q from %q", got, resizedOutput)
	}

	if err := CloseTerminal(workspaceID, session.TerminalID); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	requireTerminalTornDown(t, live)

	read, err := ReadTerminal(dataDir, workspaceID, session.TerminalID, 0)
	if err != nil {
		t.Fatalf("read terminal after close: %v", err)
	}
	if read.TerminalID != session.TerminalID {
		t.Fatalf("unexpected terminal read payload: %#v", read)
	}
	if !strings.Contains(read.Content, "__TERM_OK__") || !strings.Contains(read.Content, "__SIZE2__:40 120:__END2__") {
		t.Fatalf("expected log replay content, got %q", read.Content)
	}
}

// When the shell exits on its own, a job it left running (disowned, so the shell
// does not hang it up) gets SIGHUP from teardown, as from a real hang-up, and dies
// of it. A close afterwards still succeeds.
func TestCloseTerminalSucceedsAfterNaturalExit(t *testing.T) {
	dataDir, workspaceID, session := newTerminalTestSession(t, 100, 28)
	live := liveTerminalSession(t, session.TerminalID)

	var offset int64
	if err := WriteTerminal(workspaceID, session.TerminalID, "sleep 60 & printf '__PID_LEFT__:%s:\\n' $!; disown\n"); err != nil {
		t.Fatalf("write background job: %v", err)
	}
	left := waitForTerminalPIDs(t, dataDir, workspaceID, session.TerminalID, &offset, "LEFT")["LEFT"]

	if err := WriteTerminal(workspaceID, session.TerminalID, "exit\n"); err != nil {
		t.Fatalf("write exit: %v", err)
	}

	_, read := waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return read.EOF
	})
	if !read.EOF {
		t.Fatalf("expected terminal EOF after natural exit, got %#v", read)
	}
	requireTerminalTornDown(t, live, left)

	if err := CloseTerminal(workspaceID, session.TerminalID); err != nil {
		t.Fatalf("close terminal after exit: %v", err)
	}

	replayed, err := ReadTerminal(dataDir, workspaceID, session.TerminalID, 0)
	if err != nil {
		t.Fatalf("read terminal after natural exit close: %v", err)
	}
	if replayed.TerminalID != session.TerminalID {
		t.Fatalf("unexpected replay payload: %#v", replayed)
	}
}

// Every job the shell started dies with the terminal: each background job has its
// own process group, one ignores SIGHUP (so it needs the SIGKILL escalation), one is
// stopped, and one has a child of its own.
func TestCloseTerminalTearsDownBackgroundChild(t *testing.T) {
	dataDir, workspaceID, session := newTerminalTestSession(t, 100, 28)
	live := liveTerminalSession(t, session.TerminalID)
	childOutputPath := filepath.Join(t.TempDir(), "child-alive.txt")
	command := fmt.Sprintf(
		"sh -c 'sleep 2; printf bg > \"$1\"' _ %s >/dev/null 2>&1 & printf '__BG_STARTED__\\n'\n",
		shellQuote(childOutputPath),
	)
	if err := WriteTerminal(workspaceID, session.TerminalID, command); err != nil {
		t.Fatalf("write background command: %v", err)
	}

	var offset int64
	_, _ = waitForTerminal(t, dataDir, workspaceID, session.TerminalID, &offset, func(read *TerminalReadResult, content string) bool {
		return strings.Count(content, "__BG_STARTED__") >= 2
	})

	jobs := strings.Join([]string{
		"sh -c 'trap \"\" HUP; exec sleep 61' & printf '__PID_NOHUP__:%s:\\n' $!",
		"sleep 62 & kill -STOP $!; printf '__PID_STOPPED__:%s:\\n' $!",
		"sh -c 'sleep 63 & wait' & printf '__PID_NESTED__:%s:\\n' $!",
	}, "\n") + "\n"
	if err := WriteTerminal(workspaceID, session.TerminalID, jobs); err != nil {
		t.Fatalf("write jobs: %v", err)
	}
	pids := waitForTerminalPIDs(t, dataDir, workspaceID, session.TerminalID, &offset, "NOHUP", "STOPPED", "NESTED")

	if err := CloseTerminal(workspaceID, session.TerminalID); err != nil {
		t.Fatalf("close terminal with background child: %v", err)
	}
	requireTerminalTornDown(t, live, pids["NOHUP"], pids["STOPPED"], pids["NESTED"])

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(childOutputPath); err == nil {
			t.Fatalf("expected background child to be terminated before writing %s", childOutputPath)
		} else if os.IsNotExist(err) {
			time.Sleep(100 * time.Millisecond)
			continue
		} else {
			t.Fatalf("stat child output: %v", err)
		}
	}
}

func TestOpenTerminalRequiresWorkspace(t *testing.T) {
	_, err := OpenTerminal(t.TempDir(), TerminalOpenRequest{WorkspaceID: "missing"})
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
}

func newTerminalTestSession(t *testing.T, cols int, rows int) (string, string, *TerminalSessionRecord) {
	t.Helper()
	dataDir, workspaceID := newTerminalTestWorkspace(t)
	return dataDir, workspaceID, openTerminalTestSession(t, dataDir, workspaceID, cols, rows)
}

func newTerminalTestWorkspace(t *testing.T) (string, string) {
	t.Helper()
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces.json"), []workspaceRecord{snapshot.Workspace}); err != nil {
		t.Fatalf("write workspaces: %v", err)
	}
	return dataDir, workspaceID
}

func openTerminalTestSession(t *testing.T, dataDir string, workspaceID string, cols int, rows int) *TerminalSessionRecord {
	t.Helper()
	session, err := OpenTerminal(dataDir, TerminalOpenRequest{
		WorkspaceID: workspaceID,
		Cols:        cols,
		Rows:        rows,
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	// a failing test must not leave its shell running
	t.Cleanup(func() { _ = CloseTerminal(workspaceID, session.TerminalID) })
	return session
}

func waitForTerminal(
	t *testing.T,
	dataDir string,
	workspaceID string,
	terminalID string,
	offset *int64,
	predicate func(read *TerminalReadResult, content string) bool,
) (string, *TerminalReadResult) {
	t.Helper()
	var content strings.Builder
	var latest *TerminalReadResult
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		read, err := ReadTerminal(dataDir, workspaceID, terminalID, *offset)
		if err != nil {
			t.Fatalf("read terminal: %v", err)
		}
		*offset = read.Offset
		content.WriteString(read.Content)
		latest = read
		if predicate(read, content.String()) {
			return content.String(), read
		}
		time.Sleep(75 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal condition; collected %q", content.String())
	return "", latest
}

func extractTerminalSize(t *testing.T, content string, prefix string, suffix string) string {
	t.Helper()
	pattern := terminalSizePattern(prefix, suffix)
	match := pattern.FindStringSubmatch(content)
	if len(match) != 2 {
		t.Fatalf("expected size markers %s/%s in %q", prefix, suffix, content)
	}
	return match[1]
}

func terminalSizePattern(prefix string, suffix string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(prefix) + `:(\d+ \d+):` + regexp.QuoteMeta(suffix))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
