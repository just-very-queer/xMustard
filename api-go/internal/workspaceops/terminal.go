package workspaceops

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type TerminalOpenRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	Cols        int     `json:"cols"`
	Rows        int     `json:"rows"`
	TerminalID  *string `json:"terminal_id"`
}

type TerminalWriteRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Data        string `json:"data"`
}

type TerminalResizeRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
}

type TerminalSessionRecord struct {
	TerminalID string `json:"terminal_id"`
	PID        int    `json:"pid"`
}

type TerminalReadResult struct {
	Offset      int64  `json:"offset"`
	Content     string `json:"content"`
	EOF         bool   `json:"eof"`
	WorkspaceID string `json:"workspace_id"`
	TerminalID  string `json:"terminal_id"`
}

type terminalSession struct {
	terminalID   string
	workspaceID  string
	process      *exec.Cmd
	shellStart   uint64 // the shell's process start time, 0 if unknown; see terminalSessionSweep
	pty          *os.File
	logPath      string
	ttyPath      string // the replica's device path; teardown looks for processes holding it
	mu           sync.RWMutex
	closed       bool
	lastActivity time.Time
	closeOnce    sync.Once
	teardownOnce sync.Once
	pumpDone     chan struct{} // closed once the pump has released the PTY and log
	tornDown     chan struct{} // closed once shutdown has finished
}

const (
	// terminalTermDelay is how long a closing terminal's processes get after
	// SIGHUP before they are also sent SIGTERM.
	terminalTermDelay = 300 * time.Millisecond
	// terminalKillGrace is how long a closing terminal's processes get to exit
	// after SIGHUP before they are sent SIGKILL.
	terminalKillGrace = 2 * time.Second
	// terminalPumpDrain bounds each wait for the pump to reach the PTY's end. It
	// runs out only while a process teardown did not end still holds the replica.
	terminalPumpDrain = time.Second
)

// terminalTeardown says how far teardown goes with the processes a terminal's
// shell leaves behind.
type terminalTeardown int

const (
	// terminalHangUp is for a shell that exited on its own: what it left running
	// gets SIGHUP and SIGCONT, as from a real terminal hang-up, and may outlive it.
	terminalHangUp terminalTeardown = iota
	// terminalKill is for a close, the idle reaper and server shutdown: SIGHUP,
	// then SIGTERM, then SIGKILL until no process of the session is left.
	terminalKill
)

// terminalIdleTTL closes a terminal session abandoned (no write/resize/read) past
// this duration, so an opened-but-forgotten session can't leak its shell child, PTY,
// log handle, pipes, and pump goroutine forever (XM-POST-003).
const terminalIdleTTL = 30 * time.Minute

var terminalReaperOnce sync.Once

func startTerminalReaper() {
	terminalReaperOnce.Do(func() {
		go func() {
			t := time.NewTicker(terminalIdleTTL)
			defer t.Stop()
			for range t.C {
				reapIdleTerminals()
			}
		}()
	})
}

// reapIdleTerminals closes and removes sessions idle past the TTL.
func reapIdleTerminals() {
	var toClose []*terminalSession
	terminalSessions.Range(func(k, v any) bool {
		if s, ok := v.(*terminalSession); ok && s.idleBeyond(terminalIdleTTL) {
			toClose = append(toClose, s)
		}
		return true
	})
	shutdownTerminals(toClose)
}

// shutdownTerminals tears the sessions down in parallel, since each may wait out
// its grace period, and removes each from the map only once it is down, so a read
// meanwhile does not report EOF early. A session opened under the same id in the
// meantime stays.
func shutdownTerminals(sessions []*terminalSession) {
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.shutdown(terminalKill)
			terminalSessions.CompareAndDelete(s.terminalID, s)
		}()
	}
	wg.Wait()
}

func (session *terminalSession) touch() {
	session.mu.Lock()
	session.lastActivity = time.Now()
	session.mu.Unlock()
}

func (session *terminalSession) idleBeyond(d time.Duration) bool {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return !session.lastActivity.IsZero() && time.Since(session.lastActivity) > d
}

type synchronizedLogWriter struct {
	mu   sync.Mutex
	file *os.File
}

var terminalSessions sync.Map

func OpenTerminal(dataDir string, request TerminalOpenRequest) (*TerminalSessionRecord, error) {
	// Validate caller input first: a supplied terminal id must not escape the
	// terminal log directory via `..`/separators (XM-NEW-012).
	terminalID := strings.TrimSpace(firstNonEmptyPtr(request.TerminalID))
	if terminalID != "" {
		if err := validateSafeID("terminal", terminalID); err != nil {
			return nil, err
		}
	}
	workspace, err := getWorkspaceRecord(dataDir, request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if terminalID == "" {
		terminalID = "term_" + hashID(request.WorkspaceID, nowUTC())[:12]
	}
	logPath := filepath.Join(dataDir, "workspaces", request.WorkspaceID, "terminals", terminalID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	logHandle, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	ptyHandle, replicaHandle, err := openTerminalPTY(request.Cols, request.Rows)
	if err != nil {
		_ = logHandle.Close()
		return nil, err
	}

	shell := os.Getenv("SHELL")
	if strings.TrimSpace(shell) == "" {
		if _, lookErr := os.Stat("/bin/zsh"); lookErr == nil {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/sh"
		}
	}
	cmd := exec.Command(shell, "-l")
	cmd.Dir = workspace.RootPath
	configureTerminalCommand(cmd, replicaHandle)
	if err := cmd.Start(); err != nil {
		_ = replicaHandle.Close()
		_ = ptyHandle.Close()
		_ = logHandle.Close()
		return nil, err
	}
	_ = replicaHandle.Close()
	// Recorded before anything can reap the shell, so the pid names it here.
	shellStart, _ := terminalProcStart(cmd.Process.Pid)

	session := &terminalSession{
		terminalID:   terminalID,
		workspaceID:  request.WorkspaceID,
		process:      cmd,
		shellStart:   shellStart,
		pty:          ptyHandle,
		logPath:      logPath,
		ttyPath:      replicaHandle.Name(),
		lastActivity: time.Now(),
		pumpDone:     make(chan struct{}),
		tornDown:     make(chan struct{}),
	}
	// reject a duplicate LIVE id rather than overwriting (and orphaning) its process
	// handle (XM-POST-002). LoadOrStore is atomic; a stale closed entry is replaced.
	if prev, loaded := terminalSessions.LoadOrStore(terminalID, session); loaded {
		if existing, ok := prev.(*terminalSession); ok && !existing.isClosed() {
			// never published and no pump started: tear down, reap, close the log
			close(session.pumpDone)
			session.shutdown(terminalKill)
			_ = cmd.Wait()
			_ = logHandle.Close()
			return nil, fmt.Errorf("terminal %s already active", terminalID)
		}
		terminalSessions.Store(terminalID, session) // replace the closed entry
	}
	startTerminalReaper()

	writer := &synchronizedLogWriter{file: logHandle}
	go pumpTerminalStream(session, writer)
	go func() {
		// Teardown runs before the shell is reaped: until then its pid, and with it
		// the session id teardown looks processes up by, cannot be reused. On a
		// close, this waits for that teardown to finish. CloseTerminal stays
		// idempotent; the idle reaper removes the closed map entry.
		if waitTerminalShellExit(cmd.Process.Pid) {
			session.shutdown(terminalHangUp)
			_ = cmd.Wait()
			return
		}
		_ = cmd.Wait()
		session.shutdown(terminalHangUp)
	}()

	return &TerminalSessionRecord{
		TerminalID: terminalID,
		PID:        cmd.Process.Pid,
	}, nil
}

func WriteTerminal(workspaceID, terminalID string, data string) error {
	session, err := requireTerminalSession(workspaceID, terminalID)
	if err != nil {
		return err
	}
	if session.isClosed() {
		return errTerminalClosed(terminalID)
	}
	session.touch()
	_, err = io.WriteString(session.pty, data)
	return err
}

func ResizeTerminal(workspaceID, terminalID string, cols int, rows int) error {
	session, err := requireTerminalSession(workspaceID, terminalID)
	if err != nil {
		return err
	}
	if session.isClosed() {
		return errTerminalClosed(terminalID)
	}
	session.touch()
	return resizeTerminalPTY(session.pty, cols, rows)
}

func errTerminalClosed(terminalID string) error {
	return fmt.Errorf("terminal %s: %w", terminalID, os.ErrClosed)
}

// CloseTerminal ends every process of the terminal's session and returns once
// they are gone and the log holds the last output. The session leaves the map
// only then, so a read during teardown does not report EOF early.
func CloseTerminal(workspaceID, terminalID string) error {
	session, err := requireTerminalSession(workspaceID, terminalID)
	if err != nil {
		return err
	}
	session.shutdown(terminalKill)
	terminalSessions.CompareAndDelete(terminalID, session)
	return nil
}

// maxTerminalReadBytes bounds a single terminal log read so a long-lived shell's
// log can't be slurped into one response (a read from an old offset would otherwise
// allocate the whole remaining log). The caller continues from the returned Offset.
const maxTerminalReadBytes = 256 << 10 // 256 KiB

func ReadTerminal(dataDir string, workspaceID string, terminalID string, offset int64) (*TerminalReadResult, error) {
	// Validate both caller-controlled identifiers BEFORE building a filesystem path:
	// the historical-read path below joins them directly, so an unvalidated id could
	// select another workspace's log or escape the terminals dir (XM-PRO-003).
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("terminal", terminalID); err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	logPath := filepath.Join(dataDir, "workspaces", workspaceID, "terminals", terminalID+".log")
	eof := true
	if sessionValue, ok := terminalSessions.Load(terminalID); ok {
		// only adopt the live session's log path when it belongs to the requesting
		// workspace — otherwise a caller could read another workspace's terminal
		// output by guessing its id (XM-NEW-013).
		if session, ok := sessionValue.(*terminalSession); ok && session.workspaceID == workspaceID {
			session.touch()
			logPath = session.logPath
			eof = session.isTornDown()
		}
	}
	handle, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &TerminalReadResult{
				Offset:      offset,
				Content:     "",
				EOF:         true,
				WorkspaceID: workspaceID,
				TerminalID:  terminalID,
			}, nil
		}
		return nil, err
	}
	defer handle.Close()
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	// Bounded read: at most maxTerminalReadBytes per call. A full chunk means there
	// is more to come, so the caller polls again from the returned Offset and EOF
	// stays false until the tail is drained (XM-PRO-003).
	content, err := io.ReadAll(io.LimitReader(handle, maxTerminalReadBytes))
	if err != nil {
		return nil, err
	}
	more := len(content) == maxTerminalReadBytes
	return &TerminalReadResult{
		Offset:      offset + int64(len(content)),
		Content:     string(content),
		EOF:         eof && !more,
		WorkspaceID: workspaceID,
		TerminalID:  terminalID,
	}, nil
}

func requireTerminalSession(workspaceID, terminalID string) (*terminalSession, error) {
	value, ok := terminalSessions.Load(terminalID)
	if !ok {
		return nil, os.ErrNotExist
	}
	session, ok := value.(*terminalSession)
	if !ok {
		return nil, os.ErrNotExist
	}
	// Ownership: a session may only be addressed by its owning workspace, so one
	// agent cannot read/write/resize/close another workspace's shell (XM-NEW-013).
	if session.workspaceID != workspaceID {
		return nil, os.ErrNotExist
	}
	return session, nil
}

// pumpTerminalStream copies the shell's output to the log until the master reports
// that no process holds the replica any more, or until shutdown closes the master.
// The master stays open until shutdown, so the replica's device path cannot pass to
// another terminal while teardown looks for processes holding it.
func pumpTerminalStream(session *terminalSession, writer io.WriteCloser) {
	defer close(session.pumpDone)
	defer writer.Close()
	_, _ = io.Copy(writer, session.pty)
}

// shutdown marks the session closed, ends the processes its shell left as mode
// says (endTerminalSession), lets the pump copy what they wrote, then closes the
// master. It runs once, and the first caller's mode applies; a concurrent caller
// blocks until that run has finished, so a return means the session is down.
func (session *terminalSession) shutdown(mode terminalTeardown) {
	session.markClosed()
	session.teardownOnce.Do(func() {
		endTerminalSession(session, mode)
		// The pump ends at the master's EOF, once no process holds the replica.
		// If one still does after the drain, closing the master hangs the replica
		// up; on Linux it also ends the pump's pending read.
		drained := session.waitPump(terminalPumpDrain)
		session.closePTY()
		if !drained && !session.waitPump(terminalPumpDrain) {
			log.Printf("terminal %s: output pump still running after teardown", session.terminalID)
		}
		if session.tornDown != nil {
			close(session.tornDown)
		}
	})
}

// waitPump reports whether the pump finished within d.
func (session *terminalSession) waitPump(d time.Duration) bool {
	if session.pumpDone == nil {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-session.pumpDone:
		return true
	case <-timer.C:
		return false
	}
}

// isTornDown reports whether shutdown has finished: teardown is over and the pump
// has written the last output to the log.
func (session *terminalSession) isTornDown() bool {
	if session.tornDown == nil {
		return session.isClosed()
	}
	select {
	case <-session.tornDown:
		return true
	default:
		return false
	}
}

func (session *terminalSession) markClosed() {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closed = true
}

func (session *terminalSession) isClosed() bool {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.closed
}

func (session *terminalSession) closePTY() {
	session.closeOnce.Do(func() {
		if session.pty != nil {
			_ = session.pty.Close()
		}
	})
}

func (writer *synchronizedLogWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.file.Write(payload)
}

func (writer *synchronizedLogWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}
