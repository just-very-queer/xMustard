//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLockAPIDataDirRecordsEffectiveListenerPort(t *testing.T) {
	dir := t.TempDir()
	publish, unlock, err := lockAPIDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := publish(49001); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "api.owner"))
	if err != nil {
		t.Fatal(err)
	}
	var owner apiOwner
	if err := json.Unmarshal(b, &owner); err != nil {
		t.Fatal(err)
	}
	if owner.Port != 49001 {
		t.Fatalf("owner port = %d, want effective listener port 49001", owner.Port)
	}
}

func TestLockAPIDataDirCanonicalizesSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	publish, unlock, err := lockAPIDataDir(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := publish(8042); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "api.owner"))
	if err != nil {
		t.Fatal(err)
	}
	var owner apiOwner
	if err := json.Unmarshal(b, &owner); err != nil {
		t.Fatal(err)
	}
	resolvedDir, err := canonicalDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if owner.DataDir != resolvedDir {
		t.Fatalf("owner data dir = %q, want resolved directory %q", owner.DataDir, resolvedDir)
	}
}

func TestLockAPIDataDirRecordsOwnerUntilUnlock(t *testing.T) {
	dir := t.TempDir()
	publish, unlock, err := lockAPIDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(9123); err != nil {
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
	absDir, err := canonicalDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := procStartTime(os.Getpid())
	if !ok || owner.PID != os.Getpid() || owner.StartTime != start || owner.DataDir != absDir || owner.Port != 9123 {
		t.Fatalf("owner record = %+v; process start = %d, want current API identity", owner, start)
	}
	otherPublish, otherUnlock, err := lockAPIDataDir(dir)
	if err != errAPIAlreadyRunning {
		if err == nil {
			_ = otherPublish(9123)
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
