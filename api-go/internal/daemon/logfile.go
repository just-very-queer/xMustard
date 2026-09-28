package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Log rotation defaults (XMUSTARD_LOG_MAX_BYTES, XMUSTARD_LOG_KEEP).
const (
	DefaultLogMaxBytes = 10 << 20
	DefaultLogKeep     = 3
)

// LogFile is a size-capped log with numbered generations. A write that would take the
// file past its cap first renames it to <path>.1 (older generations move up to
// <path>.<keep>, and the oldest is dropped) and starts a new file. With stdio set, the
// process's stdout and stderr follow the current file, so a panic's trace lands in the
// rotated log, not in a file nothing rotates. Rotation reads no file and buffers
// nothing, so it adds no resident memory.
type LogFile struct {
	mu    sync.Mutex
	path  string
	max   int64
	keep  int
	stdio bool
	f     *os.File
	size  int64
}

// OpenLog opens (creating it 0600, and its directory 0700) the log at path, rotating it
// first when it is already at its cap.
func OpenLog(path string, maxBytes int64, keep int, stdio bool) (*LogFile, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultLogMaxBytes
	}
	if keep < 1 {
		keep = DefaultLogKeep
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &LogFile{path: path, max: maxBytes, keep: keep, stdio: stdio}
	if fi, err := os.Stat(path); err == nil && fi.Size() >= maxBytes {
		if err := l.shift(); err != nil {
			return nil, err
		}
	}
	f, err := l.open()
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

// LogConfig reads XMUSTARD_LOG_FILE, XMUSTARD_LOG_MAX_BYTES and XMUSTARD_LOG_KEEP; path
// is "" when the daemon should keep logging to stderr.
func LogConfig(getenv func(string) string) (path string, maxBytes int64, keep int, err error) {
	path = strings.TrimSpace(getenv("XMUSTARD_LOG_FILE"))
	if path == "" {
		return "", 0, 0, nil
	}
	if !filepath.IsAbs(path) {
		return "", 0, 0, fmt.Errorf("XMUSTARD_LOG_FILE=%q must be an absolute path", path)
	}
	if maxBytes, err = positiveEnv(getenv, "XMUSTARD_LOG_MAX_BYTES"); err != nil {
		return "", 0, 0, err
	}
	k, err := positiveEnv(getenv, "XMUSTARD_LOG_KEEP")
	if err != nil {
		return "", 0, 0, err
	}
	return path, maxBytes, int(min(k, 1000)), nil
}

// positiveEnv parses an optional positive integer; 0 when unset.
func positiveEnv(getenv func(string) string, key string) (int64, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("%s=%q must be a positive integer", key, raw)
	}
	return v, nil
}

// Write appends p, rotating first when p would take the file past its cap. A failed
// rotation keeps appending to the current file rather than losing the line.
func (l *LogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		_ = l.rotate()
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the current file.
func (l *LogFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

func (l *LogFile) rotate() error {
	if err := l.shift(); err != nil {
		return err
	}
	f, err := l.open()
	if err != nil {
		return err
	}
	old := l.f
	l.f, l.size = f, 0
	return old.Close()
}

// shift moves every generation up by one and the live file to <path>.1.
func (l *LogFile) shift() error {
	for i := l.keep - 1; i >= 1; i-- {
		_ = os.Rename(generation(l.path, i), generation(l.path, i+1)) // a missing generation is skipped
	}
	return os.Rename(l.path, generation(l.path, 1))
}

// open opens the live file for appending and, with stdio, points stdout and stderr at it.
func (l *LogFile) open() (*os.File, error) {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	l.size = fi.Size()
	if l.stdio {
		if err := redirectStdio(f); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func generation(path string, i int) string { return path + "." + strconv.Itoa(i) }
