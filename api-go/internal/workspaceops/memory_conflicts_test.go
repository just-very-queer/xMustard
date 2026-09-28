package workspaceops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// conflictWith finds the conflict naming id in e's possible conflicts.
func conflictWith(e *ContextEntry, id string) (MemoryConflict, bool) {
	i := slices.IndexFunc(e.PossibleConflicts, func(c MemoryConflict) bool { return slices.Contains(c.EntryIDs, id) })
	if i < 0 {
		return MemoryConflict{}, false
	}
	return e.PossibleConflicts[i], true
}

func withPaths(paths ...string) func(*RememberRequest) {
	return func(r *RememberRequest) { r.Paths = paths }
}

func withClaims(claims ...MemoryClaim) func(*RememberRequest) {
	return func(r *RememberRequest) { r.Claims = claims }
}

// T1: claims on one subject exclude each other when a single-valued predicate takes
// another object, or when one requires what the other forbids; requires is
// multi-valued. The served entry in conflict is suggested for supersession.
func TestClaimConflictSameSubjectAndPredicateDifferentObject(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	pinned := promoted(t, dir, ws, "author", "The toolchain is pinned to Go 1.22 for the whole repository.",
		withClaims(MemoryClaim{Subject: "go", Predicate: "pinned_to", Object: "1.22"}))
	needs := promoted(t, dir, ws, "author", "The API needs a running Postgres for the ops layer.",
		withClaims(MemoryClaim{Subject: "api", Predicate: "requires", Object: "postgres"}))
	newer := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, "We moved the toolchain to Go 1.23 last week.",
		withClaims(MemoryClaim{Subject: "Go", Predicate: "pinned_to", Object: "1.23"}))
	c, ok := conflictWith(newer, pinned.ID)
	if !ok || c.Tier != 1 || c.Kind != ConflictClaim || !strings.Contains(c.Reason, `pinned_to "1.23" vs "1.22"`) {
		t.Fatalf("claim conflict: %+v (all %+v)", c, newer.PossibleConflicts)
	}
	if !slices.Contains(newer.SuggestedSupersedes, pinned.ID) {
		t.Fatalf("the contradicted served entry is not suggested: %v", newer.SuggestedSupersedes)
	}
	also := rememberAs(t, dir, ws, ContextActor{ID: "agent-c"}, "The API also needs Redis for the job queue.",
		withClaims(MemoryClaim{Subject: "api", Predicate: "requires", Object: "redis"}))
	if _, ok := conflictWith(also, needs.ID); ok {
		t.Fatalf("two requirements of one subject conflict: %+v", also.PossibleConflicts)
	}
	forbid := rememberAs(t, dir, ws, ContextActor{ID: "agent-d"}, "The API must not depend on Postgres any more.",
		withClaims(MemoryClaim{Subject: "api", Predicate: "forbids", Object: "Postgres"}))
	if c, ok := conflictWith(forbid, needs.ID); !ok || c.Tier != 1 {
		t.Fatalf("requires and forbids on one object: %+v", forbid.PossibleConflicts)
	}
	_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "x",
		Claims: []MemoryClaim{{Subject: "go", Predicate: "prefers", Object: "1.23"}}}}, ContextActor{ID: "agent-e"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a predicate outside the vocabulary: %v", err)
	}
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Subject: "go", Predicate: "pinned_to", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := res["entries"].([]ContextEntry); len(got) != 1 || got[0].ID != pinned.ID {
		t.Fatalf("recall(subject, predicate) returned %v", got)
	}
}

// T2 flags memories that share an anchor and most of their words but state different
// numbers, versions or commands, or where only one negates; T3 keeps the plain path
// overlap, labeled; memories that agree conflict at T3 at most.
func TestContradictionTiersAndPathOverlap(t *testing.T) {
	cases := []struct {
		name, served, proposed string
		tier                   int
		reason                 string
	}{
		{"number", "The service listens on port 8081 in server/config.go.",
			"The service should listen on port 9090 in server/config.go.", 2, "number: port"},
		{"version", "CI runs Go 1.22 for the server/config.go package tests.",
			"CI runs Go 1.23 for the server/config.go package tests.", 2, "version: go 1.23 vs 1.22"},
		{"command", "Before committing server/config.go run `make check` from the repository root.",
			"Before committing server/config.go run `make check-backend` from the repository root.", 2, "command:"},
		{"negation", "Cache the parsed server/config.go values across requests.",
			"Never cache the parsed server/config.go values across requests.", 2, "negation:"},
		{"agreement", "The service listens on port 8081 in server/config.go.",
			"Reminder: server/config.go keeps the service on port 8081.", 3, "both reference server/config.go"},
		{"unrelated", "server/config.go reads the listen address from the environment.",
			"Tests for server/config.go live next to it and use a temp dir.", 3, "both reference server/config.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, ws := multiAgentDir(t), "ws"
			served := promoted(t, dir, ws, "author", c.served, withPaths("server/config.go"))
			got := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, c.proposed, withPaths("server/config.go"))
			conflict, ok := conflictWith(got, served.ID)
			if !ok || conflict.Tier != c.tier || !strings.Contains(conflict.Reason, c.reason) {
				t.Fatalf("got %+v, want tier %d with %q", got.PossibleConflicts, c.tier, c.reason)
			}
			kind := map[int]string{2: ConflictContradiction, 3: ConflictPathOverlap}[c.tier]
			if conflict.Kind != kind || (c.tier == 3) != (conflict.Path == "server/config.go") {
				t.Fatalf("label %+v", conflict)
			}
			if suggested := slices.Contains(got.SuggestedSupersedes, served.ID); suggested != (c.tier == 2) {
				t.Fatalf("suggested_supersedes %v for tier %d", got.SuggestedSupersedes, c.tier)
			}
		})
	}
}

// Recall labels its conflicts by tier: a contradiction between returned entries is
// named with its reason, and the path overlap keeps its pre-WS-27 shape.
func TestRecallLabelsConflictTiers(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	a := promoted(t, dir, ws, "author", "The service listens on port 8081 in server/config.go.", withPaths("server/config.go"))
	b := promoted(t, dir, ws, "author", "The service should listen on port 9090 in server/config.go.", withPaths("server/config.go"))
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Query: "service port", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	conflicts := res["conflicts"].([]MemoryConflict)
	byKind := map[string]MemoryConflict{}
	for _, c := range conflicts {
		byKind[c.Kind] = c
	}
	if c := byKind[ConflictContradiction]; c.Tier != 2 || !slices.Contains(c.EntryIDs, a.ID) || !slices.Contains(c.EntryIDs, b.ID) {
		t.Fatalf("contradiction in recall: %+v", conflicts)
	}
	if c := byKind[ConflictPathOverlap]; c.Tier != 3 || c.Path != "server/config.go" || len(c.Titles) != 2 {
		t.Fatalf("path overlap in recall: %+v", conflicts)
	}
}

// Relations are recorded as the author states them, to entries of the workspace only.
func TestProposalRelations(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	base := promoted(t, dir, ws, "author", "Releases are cut from main every Tuesday.")
	refined := rememberAs(t, dir, ws, ContextActor{ID: "agent-b"}, "Releases are cut from main every Tuesday at 10:00 UTC.",
		func(r *RememberRequest) { r.Relations = []MemoryRelation{{Kind: "refines", Target: base.ID}} })
	_, res := entryView(t, dir, ws, refined.ID, true)
	rels, _ := res["relations"].([]govstore.Relation)
	if len(rels) != 1 || rels[0].Kind != govstore.RelationRefines || rels[0].ToID != base.ID {
		t.Fatalf("relations %+v", res["relations"])
	}
	for _, bad := range []MemoryRelation{{Kind: "supersedes", Target: base.ID}, {Kind: "refines", Target: "ctx_missing"}} {
		_, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Content: "another release note",
			Relations: []MemoryRelation{bad}}}, ContextActor{ID: "agent-c"})
		if err == nil {
			t.Fatalf("relation %+v was accepted", bad)
		}
	}
}
