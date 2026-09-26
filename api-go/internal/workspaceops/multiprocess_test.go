package workspaceops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

// Multi-process tests re-execute the test binary: the child runs the same test
// function, which sees childRoleEnv and does its role's writes instead of the parent's
// assertions. Children wait on a start file so every process writes at the same time.

const (
	childRoleEnv = "XMUSTARD_TEST_CHILD_ROLE"
	childDirEnv  = "XMUSTARD_TEST_CHILD_DIR"
	startFile    = "start.barrier"
)

// startChild re-executes the test binary to run testName as role against dir.
func startChild(t *testing.T, testName, role, dir string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), childRoleEnv+"="+role, childDirEnv+"="+dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &out
}

// runWithChildren starts one child per role, releases them together with the parent's
// own writes, and fails the test if any child fails.
func runWithChildren(t *testing.T, testName, dir string, roles []string, parent func()) {
	t.Helper()
	type child struct {
		cmd *exec.Cmd
		out *bytes.Buffer
	}
	var children []child
	for _, role := range roles {
		cmd, out := startChild(t, testName, role, dir)
		children = append(children, child{cmd, out})
	}
	if err := os.WriteFile(filepath.Join(dir, startFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	parent()
	for i, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("child %s failed: %v\n%s", roles[i], err, c.out)
		}
	}
}

// awaitStart blocks a child until the parent releases every process.
func awaitStart(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, startFile)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("parent never released the children")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const multiProcessWrites = 15

// Two API processes and an ops-CLI-style writer on its own store handle propose into
// one governance.db at once: SQLite's cross-process locking loses nothing.
func TestMemoryStoreAcrossProcessesLosesNoUpdates(t *testing.T) {
	const ws = "wsTwoProcs"
	ctx := context.Background()
	if dir := os.Getenv(childDirEnv); dir != "" {
		awaitStart(t, dir)
		role := os.Getenv(childRoleEnv)
		for i := 0; i < multiProcessWrites; i++ {
			var err error
			switch role {
			case "api":
				_, err = ProposeContext(dir, ws, ProposeContextRequest{Content: fmt.Sprintf("api2 %d", i), Source: "bob"})
			case "ops":
				err = withOpsStore(ctx, dir, func(s *govstore.SQLStore) error {
					return s.Update(ctx, func(tx govstore.Tx) error {
						_, err := tx.InsertEntry(ctx, govstore.NewEntry{ID: fmt.Sprintf("ops_%d", i), WorkspaceID: ws,
							Content: fmt.Sprintf("ops %d", i)}, govstore.Actor{Principal: "ops"})
						return err
					})
				})
			default:
				t.Fatalf("unknown role %q", role)
			}
			if err != nil {
				t.Fatalf("%s write %d: %v", role, i, err)
			}
		}
		return
	}

	dir := t.TempDir()
	if _, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "first", Source: "alice"}); err != nil {
		t.Fatal(err)
	}
	runWithChildren(t, t.Name(), dir, []string{"api", "ops"}, func() {
		var wg sync.WaitGroup
		errs := make(chan error, multiProcessWrites)
		for i := 0; i < multiProcessWrites; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := ProposeContext(dir, ws, ProposeContextRequest{Content: fmt.Sprintf("api1 %d", i), Source: "alice"})
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
	})
	all, err := ListContextEntries(dir, ws, "all")
	if err != nil {
		t.Fatal(err)
	}
	if want := 3*multiProcessWrites + 1; len(all) != want {
		t.Fatalf("want %d entries after three concurrent processes, got %d (lost updates)", want, len(all))
	}
}

// withOpsStore opens its own handle on the data dir's store, as the ops CLI does.
func withOpsStore(ctx context.Context, dir string, fn func(*govstore.SQLStore) error) error {
	s, err := govstore.Open(ctx, memoryStorePath(dir), govstore.Options{})
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(s)
}

// The JSON stores that remain (feedback, runs, tokens) take a cross-process lock
// around their read-modify-write: three processes bumping the same feedback counter,
// the same run record and the same token file lose no update.
func TestJSONStoresLockAcrossProcesses(t *testing.T) {
	const ws, runID = "wsJSONLock", "run_shared"
	bump := func(dir, who string, i int) error {
		if err := RecordFeedback(dir, ws, "verify", []string{"shared.go"}); err != nil {
			return err
		}
		if _, err := mutateRun(dir, ws, runID, func(*runRecord) (bool, error) { return true, nil }); err != nil {
			return err
		}
		_, err := MintToken(dir, fmt.Sprintf("p-%s-%d", who, i), "agent")
		return err
	}
	if dir := os.Getenv(childDirEnv); dir != "" {
		awaitStart(t, dir)
		for i := 0; i < multiProcessWrites; i++ {
			if err := bump(dir, os.Getenv(childRoleEnv), i); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	dir := t.TempDir()
	if err := saveRunRecord(dir, runRecord{RunID: runID, WorkspaceID: ws, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	runWithChildren(t, t.Name(), dir, []string{"c1", "c2"}, func() {
		for i := 0; i < multiProcessWrites; i++ {
			if err := bump(dir, "parent", i); err != nil {
				t.Error(err)
			}
		}
	})
	const total = 3 * multiProcessWrites
	fb, err := loadFeedback(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := fb["shared.go"]; got == nil || got.VerifyCount != total {
		t.Fatalf("feedback verify_count: want %d, got %+v (lost updates)", total, got)
	}
	run, err := loadRun(dir, ws, runID)
	if err != nil || run == nil {
		t.Fatalf("load run: %v", err)
	}
	if run.MirrorRevision != total+1 {
		t.Fatalf("run revision: want %d, got %d (lost updates)", total+1, run.MirrorRevision)
	}
	recs, err := loadTokenRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != total {
		t.Fatalf("tokens: want %d, got %d (lost updates)", total, len(recs))
	}
}

// Every JSON store write is fsynced before it replaces the old file.
func TestJSONStoreWritesFsync(t *testing.T) {
	dir := t.TempDir()
	var synced []string
	orig := syncFile
	syncFile = func(f *os.File) error {
		synced = append(synced, filepath.Base(f.Name()))
		return orig(f)
	}
	t.Cleanup(func() { syncFile = orig })

	if err := RecordFeedback(dir, "wsSync", "verify", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if err := saveRunRecord(dir, runRecord{RunID: "r1", WorkspaceID: "wsSync", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if _, err := MintToken(dir, "alice", "agent"); err != nil {
		t.Fatal(err)
	}
	for i, prefix := range []string{"agent_feedback.json.", "r1.json.", "agent_tokens.json."} {
		if i >= len(synced) || !strings.HasPrefix(synced[i], prefix) {
			t.Fatalf("write %d: want an fsync of %s*, got %v", i, prefix, synced)
		}
	}
}
