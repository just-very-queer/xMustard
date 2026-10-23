//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLockAPIDataDirRecordsOwnerUntilUnlock(t *testing.T) {
	t.Setenv("XMUSTARD_API_PORT", "9123")
	dir := t.TempDir()
	unlock, err := lockAPIDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlocked := false
	defer func() {
		if !unlocked {
			unlock()
		}
	}()
	path := filepath.Join(dir, "api.owner")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("owner record mode = %o, want 600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var owner apiOwner
	if err := json.Unmarshal(b, &owner); err != nil {
		t.Fatal(err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := procStartTime(os.Getpid())
	if !ok || owner.PID != os.Getpid() || owner.StartTime != start || owner.DataDir != absDir || owner.Port != 9123 {
		t.Fatalf("owner record = %+v; process start = %d, want current API identity", owner, start)
	}
	otherUnlock, err := lockAPIDataDir(dir)
	if err != errAPIAlreadyRunning {
		if err == nil {
			otherUnlock()
		}
		t.Fatalf("second API admission error = %v, want duplicate", err)
	}
	unlock()
	unlocked = true
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owner record remains after unlock: %v", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "api.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("API lock was not released: %v", err)
	}
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
}
