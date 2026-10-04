package govstore

import (
	"context"
	"maps"
	"slices"
	"testing"
)

// The store-level scope predicate is the rule itself, not a pre-filter recall adds on
// top of it: a consumer that ranks with no author check of its own must still not be
// served another principal's confined entry (round 3, r3-4). Two authors share each key
// in this fixture, so a predicate that admits run, session or dir by key alone serves
// one of the other's entries.
func TestScopeWhereAdmitsNoOtherAuthorsScopedEntry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	for _, e := range []struct {
		id, scope, key string
		author         Actor
		peers          []Actor
	}{
		{"ws_note", "workspace", "", alice, []Actor{bob, carol}},
		{"mine_dir", "dir", "api-go", alice, []Actor{bob, carol}},
		{"mine_run", "run", "run-1", alice, []Actor{bob, carol}},
		{"mine_session", "session", "claude:s1", alice, []Actor{bob, carol}},
		{"mine_private", "private", "alice", alice, []Actor{bob, carol}},
		{"theirs_dir", "dir", "api-go", bob, []Actor{alice, carol}},
		{"theirs_run", "run", "run-1", bob, []Actor{alice, carol}},
		{"theirs_session", "session", "claude:s1", bob, []Actor{alice, carol}},
		{"theirs_private", "private", "bob", bob, []Actor{alice, carol}},
	} {
		mustUpdate(t, s, func(tx Tx) error {
			_, err := tx.InsertEntry(ctx, NewEntry{ID: e.id, WorkspaceID: "ws1", Title: e.id,
				Content: e.id + " body", Scope: e.scope, ScopeKey: e.key}, e.author)
			return err
		})
		// peer_verified needs two distinct peer approvals and neither may be the author
		for _, p := range e.peers {
			vote(t, s, e.id, p, VerdictApprove)
		}
		promotePeer(t, s, e.id)
	}

	// alice reads inside her own dir, run and session keys: her confined entries and the
	// workspace note are served, and none of bob's is.
	served := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "alice",
		ScopeDir: "api-go/internal", ScopeRunID: "run-1", ScopeSessionID: "claude:s1"})
	for _, want := range []string{"ws_note", "mine_dir", "mine_run", "mine_session", "mine_private"} {
		if !served[want] {
			t.Fatalf("Ranking = %v, want it to admit %s for its own author", slices.Sorted(maps.Keys(served)), want)
		}
	}
	for _, forbidden := range []string{"theirs_dir", "theirs_run", "theirs_session", "theirs_private"} {
		if served[forbidden] {
			t.Fatalf("Ranking served %s, another principal's entry of the same key: %v", forbidden, slices.Sorted(maps.Keys(served)))
		}
	}

	// a caller naming no key is served the workspace entry and nothing else
	anon := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "carol"})
	if len(anon) != 1 {
		t.Fatalf("a caller naming no key was served %v, want only the workspace entry", slices.Sorted(maps.Keys(anon)))
	}
	if _, ok := anon["ws_note"]; !ok {
		t.Fatalf("a caller naming no key was served %v, want the workspace entry", slices.Sorted(maps.Keys(anon)))
	}

	// and the dir prefix still admits a directory below the key, for its own author
	nested := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "alice", ScopeDir: "api-go/internal/govstore"})
	if !nested["mine_dir"] {
		t.Fatalf("a directory below the key was refused to its own author: %v", slices.Sorted(maps.Keys(nested)))
	}
}

// A dir key is a plain repo-relative path, and % and _ are ordinary characters in
// one. The predicate matched it with LIKE, where they are wildcards, so a memory
// confined to api_go entered the ranking for apiXgo/subdir: an entry the Go rule
// (ScopesVisible) refuses, served because the two halves disagreed on what a
// directory prefix is (round 4, r4-3).
func TestScopeWhereDoesNotTreatAWildcardInADirKeyAsAPattern(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	for _, e := range []struct{ id, key string }{
		{"under_score", "api_go"},
		{"under_pct", "api%go"},
		{"plain", "api-go"},
	} {
		mustUpdate(t, s, func(tx Tx) error {
			_, err := tx.InsertEntry(ctx, NewEntry{ID: e.id, WorkspaceID: "ws1", Title: e.id,
				Content: e.id + " body", Scope: "dir", ScopeKey: e.key}, alice)
			return err
		})
		vote(t, s, e.id, bob, VerdictApprove)
		vote(t, s, e.id, carol, VerdictApprove)
		promotePeer(t, s, e.id)
	}

	// The _ in api_go is a wildcard under LIKE and one character under GLOB, so a
	// memory confined to api_go must not enter the ranking for apiXgo. api%go is the
	// other way round and stays a literal prefix, which is the rule: the metacharacter
	// is only a metacharacter to the matcher that reads it as one.
	if served := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "alice", ScopeDir: "apiXgo/subdir"}); len(served) != 0 {
		t.Fatalf("Ranking for dir apiXgo/subdir served %v, want nothing: api_go is not a prefix of it",
			slices.Sorted(maps.Keys(served)))
	}
	if served := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "alice", ScopeDir: "api%go/subdir"}); !served["under_pct"] {
		t.Fatalf("Ranking for dir api%%go/subdir served %v, want the entry confined to api%%go",
			slices.Sorted(maps.Keys(served)))
	}
	// The keys still confine their own author, so the escaping did not widen them.
	for _, e := range []struct{ key, id string }{{"api_go", "under_score"}, {"api%go", "under_pct"}} {
		served := rankingIDs(t, s, RankQuery{WorkspaceID: "ws1", Caller: "alice", ScopeDir: e.key})
		if !served[e.id] {
			t.Fatalf("a memory confined to %q was refused to its own author: %v", e.key, slices.Sorted(maps.Keys(served)))
		}
	}
}

func rankingIDs(t *testing.T, s *SQLStore, q RankQuery) map[string]bool {
	t.Helper()
	view, err := s.Ranking(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range view {
		out[e.ID] = true
	}
	return out
}
