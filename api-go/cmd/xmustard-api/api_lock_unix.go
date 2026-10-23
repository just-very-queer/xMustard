//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errAPIAlreadyRunning = errors.New("another instance owns this data directory")

type apiOwner struct {
	PID       int    `json:"pid"`
	StartTime int64  `json:"start_time"`
	DataDir   string `json:"data_dir"`
	Port      int    `json:"port"`
}

func lockAPIDataDir(dataDir string, port int) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "api.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errAPIAlreadyRunning
		}
		return nil, err
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	start, ok := procStartTime(os.Getpid())
	if !ok {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("read API process start time")
	}
	if port <= 0 || port > 65535 {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("invalid API port for owner record: %d", port)
	}
	owner := apiOwner{PID: os.Getpid(), StartTime: start, DataDir: absDataDir, Port: port}
	if err := writeAPIOwner(filepath.Join(dataDir, "api.owner"), owner); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return func() {
		removeAPIOwner(filepath.Join(dataDir, "api.owner"), owner)
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

func writeAPIOwner(path string, owner apiOwner) error {
	b, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".api-owner-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeAPIOwner(path string, owner apiOwner) {
	var current apiOwner
	b, err := os.ReadFile(path)
	if err == nil && json.Unmarshal(b, &current) == nil && current == owner {
		_ = os.Remove(path)
	}
}
