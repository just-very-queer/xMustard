package workspaceops

import (
	"errors"
	"fmt"
	"io"
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
	pty          *os.File
	logPath      string
	mu           sync.RWMutex
	closed       bool
	lastActivity time.Time
	closeOnce    sync.Once
}

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

// reapIdleTerminals closes + removes sessions idle past the TTL. Collected under the
// map iteration, fully released (shell kill + PTY close → pump goroutine ends + log
// handle closed) afterwards.
func reapIdleTerminals() {
	var toClose []*terminalSession
	terminalSessions.Range(func(k, v any) bool {
		if s, ok := v.(*terminalSession); ok && s.idleBeyond(terminalIdleTTL) {
			toClose = append(toClose, s)
			terminalSessions.Delete(k)
		}
		return true
	})
	for _, s := range toClose {
		s.markClosed()
		terminateTerminalProcess(s.process)
		s.closePTY()
	}
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

	session := &terminalSession{
		terminalID:   terminalID,
		workspaceID:  request.WorkspaceID,
		process:      cmd,
		pty:          ptyHandle,
		logPath:      logPath,
		lastActivity: time.Now(),
	}
	// reject a duplicate LIVE id rather than overwriting (and orphaning) its process
	// handle (XM-POST-002). LoadOrStore is atomic; a stale closed entry is replaced.
	if prev, loaded := terminalSessions.LoadOrStore(terminalID, session); loaded {
		if existing, ok := prev.(*terminalSession); ok && !existing.isClosed() {
			session.markClosed()
			terminateTerminalProcess(cmd)
			_ = ptyHandle.Close()
			_ = logHandle.Close()
			return nil, fmt.Errorf("terminal %s already active", terminalID)
		}
		terminalSessions.Store(terminalID, session) // replace the closed entry
	}
	startTerminalReaper()

	writer := &synchronizedLogWriter{file: logHandle}
	go pumpTerminalStream(session, writer)
	go func() {
		_ = cmd.Wait()
		// On natural exit the pump goroutine's deferred closePTY + writer.Close
		// release the PTY/log; we just mark closed. The lingering (closed) map entry
		// is removed by the idle reaper, while CloseTerminal stays idempotent.
		session.markClosed()
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
	session.touch()
	_, err = io.WriteString(session.pty, data)
	return err
}

func ResizeTerminal(workspaceID, terminalID string, cols int, rows int) error {
	session, err := requireTerminalSession(workspaceID, terminalID)
	if err != nil {
		return err
	}
	session.touch()
	return resizeTerminalPTY(session.pty, cols, rows)
}

func CloseTerminal(workspaceID, terminalID string) error {
	session, err := requireTerminalSession(workspaceID, terminalID)
	if err != nil {
		return err
	}
	session.markClosed()
	terminalSessions.Delete(terminalID)
	terminateTerminalProcess(session.process)
	session.closePTY()
	return nil
}

func ReadTerminal(dataDir string, workspaceID string, terminalID string, offset int64) (*TerminalReadResult, error) {
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
			eof = session.isClosed()
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
	content, err := io.ReadAll(handle)
	if err != nil {
		return nil, err
	}
	nextOffset, err := handle.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	return &TerminalReadResult{
		Offset:      nextOffset,
		Content:     string(content),
		EOF:         eof,
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

func pumpTerminalStream(session *terminalSession, writer io.WriteCloser) {
	defer writer.Close()
	defer session.closePTY()
	_, _ = io.Copy(writer, session.pty)
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
