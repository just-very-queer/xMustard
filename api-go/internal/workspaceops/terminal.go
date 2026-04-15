package workspaceops

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type TerminalOpenRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	Cols        int     `json:"cols"`
	Rows        int     `json:"rows"`
	TerminalID  *string `json:"terminal_id"`
}

type TerminalWriteRequest struct {
	Data string `json:"data"`
}

type TerminalResizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
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
	terminalID  string
	workspaceID string
	process     *exec.Cmd
	stdin       io.WriteCloser
	logPath     string
	mu          sync.RWMutex
	closed      bool
}

type synchronizedLogWriter struct {
	mu   sync.Mutex
	file *os.File
}

var terminalSessions sync.Map

func OpenTerminal(dataDir string, request TerminalOpenRequest) (*TerminalSessionRecord, error) {
	workspace, err := getWorkspaceRecord(dataDir, request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	terminalID := strings.TrimSpace(firstNonEmptyPtr(request.TerminalID))
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

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = logHandle.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = logHandle.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = logHandle.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = logHandle.Close()
		return nil, err
	}

	session := &terminalSession{
		terminalID:  terminalID,
		workspaceID: request.WorkspaceID,
		process:     cmd,
		stdin:       stdin,
		logPath:     logPath,
	}
	terminalSessions.Store(terminalID, session)

	writer := &synchronizedLogWriter{file: logHandle}
	go pumpTerminalStream(stdout, writer)
	go pumpTerminalStream(stderr, writer)
	go func() {
		_ = cmd.Wait()
		session.markClosed()
		terminalSessions.Delete(terminalID)
		_ = stdin.Close()
		_ = logHandle.Close()
	}()

	return &TerminalSessionRecord{
		TerminalID: terminalID,
		PID:        cmd.Process.Pid,
	}, nil
}

func WriteTerminal(terminalID string, data string) error {
	session, err := requireTerminalSession(terminalID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(data) == "" && data != "\n" {
		return nil
	}
	_, err = io.WriteString(session.stdin, data)
	return err
}

func ResizeTerminal(terminalID string, cols int, rows int) error {
	_, err := requireTerminalSession(terminalID)
	if err != nil {
		return err
	}
	return nil
}

func CloseTerminal(terminalID string) error {
	session, err := requireTerminalSession(terminalID)
	if err != nil {
		return err
	}
	session.markClosed()
	terminalSessions.Delete(terminalID)
	if session.process.Process != nil {
		_ = session.process.Process.Kill()
	}
	_ = session.stdin.Close()
	return nil
}

func ReadTerminal(dataDir string, workspaceID string, terminalID string, offset int64) (*TerminalReadResult, error) {
	if offset < 0 {
		offset = 0
	}
	logPath := filepath.Join(dataDir, "workspaces", workspaceID, "terminals", terminalID+".log")
	eof := true
	if sessionValue, ok := terminalSessions.Load(terminalID); ok {
		if session, ok := sessionValue.(*terminalSession); ok {
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

func requireTerminalSession(terminalID string) (*terminalSession, error) {
	value, ok := terminalSessions.Load(terminalID)
	if !ok {
		return nil, os.ErrNotExist
	}
	session, ok := value.(*terminalSession)
	if !ok {
		return nil, os.ErrNotExist
	}
	return session, nil
}

func pumpTerminalStream(reader io.Reader, writer io.Writer) {
	_, _ = io.Copy(writer, reader)
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

func (writer *synchronizedLogWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.file.Write(payload)
}
