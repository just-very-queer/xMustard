package govstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recordRun(t *testing.T, s Store, in RunOutcomeInput) (RunOutcome, bool) {
	t.Helper()
	var out RunOutcome
	var created bool
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		out, created, err = tx.RecordRunOutcome(context.Background(), in, alice)
		return err
	})
	return out, created
}

func listRuns(t *testing.T, s Store, f RunOutcomeFilter) []RunOutcome {
	t.Helper()
	out, err := s.ListRunOutcomes(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A source key records once: a second report of the same log or evidence returns the
// first outcome unchanged.
func TestRunOutcomeSourceKeyIsIdempotent(t *testing.T) {
	s := openTestStore(t, newClock())
	one := 1
	in := RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: "log:abc", Status: RunFailed, ExitCode: &one,
		OutputBytes: 10, AnalyzedBytes: 10, Analysis: json.RawMessage(`{"error_lines":["boom"]}`), Tail: "boom"}
	first, created := recordRun(t, s, in)
	if !created || !strings.HasPrefix(first.ID, RunOutcomePrefix) || !first.Failed || first.Principal != "alice" ||
		first.ExitCode == nil || *first.ExitCode != 1 || string(first.Analysis) != `{"error_lines":["boom"]}` {
		t.Fatalf("first record = %+v created=%v", first, created)
	}
	in.Tail = "different"
	second, created := recordRun(t, s, in)
	if created || second.ID != first.ID || second.Tail != "boom" {
		t.Fatalf("second report must return the first outcome: %+v created=%v", second, created)
	}
	if got := listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1"}); len(got) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(got))
	}
	// the key is per workspace
	in.WorkspaceID = "ws2"
	if _, created := recordRun(t, s, in); !created {
		t.Fatal("the same key in another workspace is a new outcome")
	}
}

// A newer outcome of a subject resolves its open failures; ground lists only what is
// still open, newest first.
func TestRunOutcomeSubjectResolvesOpenFailures(t *testing.T) {
	s := openTestStore(t, newClock())
	rec := func(key, subject, status string) RunOutcome {
		o, _ := recordRun(t, s, RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceCommand, SourceKey: key,
			SubjectKey: subject, Status: status})
		return o
	}
	fail1 := rec("c1", "go-test", RunFailed)
	other := rec("c2", "cargo-test", RunTimedOut)
	fail2 := rec("c3", "go-test", RunFailed)
	open := listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1", Open: true})
	if len(open) != 2 || open[0].ID != fail2.ID || open[1].ID != other.ID {
		t.Fatalf("open = %+v, want the newer go-test failure and the cargo timeout", open)
	}
	if got, _ := s.GetRunOutcome(context.Background(), "ws1", fail1.ID); got.ResolvedBy != fail2.ID || got.ResolvedAt == "" {
		t.Fatalf("the older failure is resolved by the newer one: %+v", got)
	}
	pass := rec("c4", "go-test", RunPassed)
	open = listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1", Open: true})
	if len(open) != 1 || open[0].ID != other.ID {
		t.Fatalf("a pass resolves its subject: open = %+v", open)
	}
	if pass.Failed || pass.Status != RunPassed {
		t.Fatalf("pass = %+v", pass)
	}
	// subject-less failures are never resolved by another outcome
	logged := rec("l1", "", RunFailed)
	rec("l2", "", RunPassed)
	if got, _ := s.GetRunOutcome(context.Background(), "ws1", logged.ID); got.ResolvedAt != "" {
		t.Fatalf("a failure without a subject stays open: %+v", got)
	}
	if got := listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1", SubjectKey: "go-test"}); len(got) != 3 {
		t.Fatalf("subject filter = %d, want 3", len(got))
	}
}

func TestRunOutcomeSinceAndLimit(t *testing.T) {
	clk := newClock()
	s := openTestStore(t, clk)
	recordRun(t, s, RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: "old", Status: RunFailed})
	clk.Advance(48 * time.Hour)
	cut := clk.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for _, k := range []string{"a", "b", "c"} {
		recordRun(t, s, RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: k, Status: RunFailed})
	}
	if got := listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1", Since: cut}); len(got) != 3 {
		t.Fatalf("since = %d, want 3", len(got))
	}
	if got := listRuns(t, s, RunOutcomeFilter{WorkspaceID: "ws1", Limit: 2}); len(got) != 2 {
		t.Fatalf("limit = %d, want 2", len(got))
	}
	if _, err := s.ListRunOutcomes(context.Background(), RunOutcomeFilter{WorkspaceID: "ws1", Since: "yesterday"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad since: %v", err)
	}
	rep, err := s.ApplyRetention(context.Background(), RetentionPolicy{RunOutcomes: 24 * time.Hour})
	if err != nil || rep.RunOutcomes != 1 {
		t.Fatalf("retention removed %d (err %v), want the one outcome older than a day", rep.RunOutcomes, err)
	}
}

func TestRunOutcomeRejectsInvalidInput(t *testing.T) {
	s := openTestStore(t, newClock())
	base := RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: "k", Status: RunFailed}
	bad := map[string]func(*RunOutcomeInput){
		"source":   func(in *RunOutcomeInput) { in.Source = "platform" },
		"status":   func(in *RunOutcomeInput) { in.Status = "flaky" },
		"key":      func(in *RunOutcomeInput) { in.SourceKey = " " },
		"tail":     func(in *RunOutcomeInput) { in.Tail = strings.Repeat("x", maxRunTail+1) },
		"analysis": func(in *RunOutcomeInput) { in.Analysis = json.RawMessage(`{`) },
		"bytes":    func(in *RunOutcomeInput) { in.OutputBytes, in.AnalyzedBytes = 1, 2 },
		"ws":       func(in *RunOutcomeInput) { in.WorkspaceID = "../x" },
	}
	for name, mutate := range bad {
		in := base
		mutate(&in)
		err := s.Update(context.Background(), func(tx Tx) error {
			_, _, err := tx.RecordRunOutcome(context.Background(), in, alice)
			return err
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.GetRunOutcome(context.Background(), "ws1", "oc_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing outcome: %v", err)
	}
}

// A file created by a build that knew only migration 1 upgrades in place: the later
// migrations are applied once, the fingerprint is re-recorded, and the older data stays.
func TestMigrationUpgradesV1FileToRunOutcomes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gov.db")
	all := migrations
	migrations = all[:1]
	old, err := Open(ctx, path, Options{})
	if err != nil {
		migrations = all
		t.Fatal(err)
	}
	mustUpdate(t, old, func(tx Tx) error {
		_, err := tx.BumpFeedback(ctx, "ws1", FeedbackRunFail, []string{"a.go"})
		return err
	})
	_ = old.Close()
	migrations = all

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()
	info, err := s.SchemaInfo(ctx)
	if err != nil || info.Version != LatestSchemaVersion() {
		t.Fatalf("schema after upgrade = %+v, %v", info, err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM schema_migrations"); n != len(migrations) {
		t.Fatalf("migration rows = %d, want %d", n, len(migrations))
	}
	if fb, _ := s.GetFeedback(ctx, "ws1", []string{"a.go"}); fb["a.go"].RunFail != 1 {
		t.Fatalf("v1 data lost in the upgrade: %+v", fb)
	}
	if _, created := recordRun(t, s, RunOutcomeInput{WorkspaceID: "ws1", Source: RunSourceLog, SourceKey: "k", Status: RunFailed}); !created {
		t.Fatal("run outcomes are writable after the upgrade")
	}
	_ = s.Close()
	again, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	_ = again.Close()
}
