package workspaceops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// A scope is derived from the writing principal and the request. Every row of the
// table names the input that decides it, so an unlisted combination is refused
// rather than defaulted to the workspace scope.
func TestDeriveScopeCoversEveryPrincipalAndRunInput(t *testing.T) {
	cases := []struct {
		name   string
		req    ScopeRequest
		actor  ContextActor
		want   string
		refuse bool
	}{
		{name: "a plain agent memory is workspace scoped", req: ScopeRequest{}, want: ScopeWorkspace},
		{name: "a run scoped memory", req: ScopeRequest{RunID: "run-1"}, want: ScopeRun},
		{name: "a session scoped memory", req: ScopeRequest{SessionID: "claude:s1"}, want: ScopeSession},
		{name: "a dir scoped memory", req: ScopeRequest{Dir: "api-go/internal"}, want: ScopeDir},
		{
			name:  "a human approver does not widen the default",
			req:   ScopeRequest{},
			actor: ContextActor{Kind: PrincipalHuman, Approver: true, Admin: true},
			want:  ScopeWorkspace,
		},
		{
			name:  "a human may keep a memory private",
			req:   ScopeRequest{Visibility: ScopePrivate},
			actor: ContextActor{ID: "owner-a", Kind: PrincipalHuman, Approver: true},
			want:  ScopePrivate,
		},
		{name: "an agent may ask for private", req: ScopeRequest{Visibility: ScopePrivate},
			actor: ContextActor{ID: "author-a"}, want: ScopePrivate},
		{
			name:   "a run id with no run is refused",
			req:    ScopeRequest{Visibility: ScopeRun},
			refuse: true,
		},
		{
			name:   "a dir outside the workspace is refused",
			req:    ScopeRequest{Visibility: ScopeDir, Dir: "../elsewhere"},
			refuse: true,
		},
		{
			name:   "an absolute dir is refused",
			req:    ScopeRequest{Visibility: ScopeDir, Dir: "/etc"},
			refuse: true,
		},
		{
			name:   "an unknown visibility is refused",
			req:    ScopeRequest{Visibility: "everyone"},
			refuse: true,
		},
		{
			name:   "a run id with no run key is refused",
			req:    ScopeRequest{Visibility: ScopeRun, RunID: "  "},
			refuse: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DeriveScope(c.req, c.actor)
			if c.refuse {
				if err == nil {
					t.Fatalf("DeriveScope(%+v) = %q, want a refusal", c.req, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DeriveScope(%+v): %v", c.req, err)
			}
			if got != c.want {
				t.Fatalf("DeriveScope(%+v) = %q, want %q", c.req, got, c.want)
			}
		})
	}
}

// The identity smuggling test (PAR-SHARE-01): a request cannot name the scope its
// memory is stored under. The payload field, the metadata and the actor are all
// caller-supplied, and none of them reaches the stored row.
func TestScopeCannotBeSmuggledInFromTheRequest(t *testing.T) {
	dir := singleAgentDir(t)
	for _, smuggling := range []struct {
		name  string
		apply func(*RememberRequest, *ContextActor)
	}{
		{"a payload scope field", func(r *RememberRequest, _ *ContextActor) { r.Scope = "global" }},
		{"a payload scope key", func(r *RememberRequest, _ *ContextActor) { r.ScopeKey = "everyone" }},
		{"the metadata", func(r *RememberRequest, _ *ContextActor) {
			r.Metadata = map[string]string{"scope": "private", govstore.MetaQuarantine: ""}
		}},
		{"the actor's id naming another principal", func(_ *RememberRequest, a *ContextActor) { a.ID = "" }},
	} {
		t.Run(smuggling.name, func(t *testing.T) {
			req := RememberRequest{ProposeContextRequest: ProposeContextRequest{
				Title: "smuggled " + smuggling.name, Content: "the scope is derived, not asked for", Permission: "readwrite",
			}}
			actor := ContextActor{ID: "writer-a"}
			smuggling.apply(&req, &actor)
			e, err := Remember(dir, "ws-1", req, actor)
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			if e.Scope != ScopeWorkspace {
				t.Fatalf("stored scope = %q, want %q: %s reached the entry", e.Scope, ScopeWorkspace, smuggling.name)
			}
		})
	}
}

// A private memory is served to its author and to nobody else. The reader holds the
// verifier role, which is the strongest role that is not admin, and the admin check is
// that it is not an exception either.
func TestAPrivateMemoryIsServedToItsAuthorAndToNobodyElse(t *testing.T) {
	dir := singleAgentDir(t)
	e := propose(t, dir, "ws-1", "author-a", "private note", "the staging credentials live in vault",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })

	if e.Scope != ScopePrivate {
		t.Fatalf("stored scope = %q, want %q", e.Scope, ScopePrivate)
	}
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "staging credentials vault", Caller: "author-a"})
	if !slices.Contains(ids(res), e.ID) {
		t.Fatalf("the author must read its own private memory, got %v", ids(res))
	}
	for _, other := range []struct {
		name, caller string
	}{
		{"a verifier", "verifier-b"},
		{"another agent", "agent-b"},
		{"an open mode caller", OpenModeIdentity},
	} {
		t.Run(other.name, func(t *testing.T) {
			res := recallWith(t, dir, "ws-1", RecallRequest{Query: "staging credentials vault", Caller: other.caller})
			if slices.Contains(ids(res), e.ID) {
				t.Fatalf("%s must not read another principal's private memory, got %v", other.caller, ids(res))
			}
		})
	}
}

// A private memory is not served through the pending queue either: include_pending
// is the path a verifier takes, so it must not be a way round the scope.
func TestAPrivateMemoryIsNotServedThroughThePendingQueue(t *testing.T) {
	dir := t.TempDir()
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "private and pending", Content: "unverified private text", Permission: "readwrite", Visibility: ScopePrivate,
	}}
	e, err := Remember(dir, "ws-1", req, ContextActor{ID: "author-a"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "unverified private text", Caller: "verifier-b", IncludePending: true})
	if slices.Contains(ids(res), e.ID) {
		t.Fatalf("include_pending must not serve another principal's private memory, got %v", ids(res))
	}
}

// A run scoped memory lives and dies with its run: a later run in the same workspace
// does not see it, and neither does the same run read by another principal.
func TestARunScopedMemoryIsInvisibleOutsideItsRun(t *testing.T) {
	dir := singleAgentDir(t)
	trustRunKeys(t, dir, "author-a", "author-b", "author-c")
	e := propose(t, dir, "ws-1", "author-a", "run scoped", "the migration needs a lock",
		func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" })
	if e.Scope != ScopeRun {
		t.Fatalf("stored scope = %q, want %q", e.Scope, ScopeRun)
	}
	if e.ScopeKey != "run-1" {
		t.Fatalf("scope key = %q, want the run it was derived from", e.ScopeKey)
	}
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "migration needs lock", Caller: "author-a", RunID: "run-1"})
	if !slices.Contains(ids(res), e.ID) {
		t.Fatalf("inside its own run the memory must be served, got %v", ids(res))
	}
	for _, outside := range []struct {
		name, caller, run string
	}{
		{"a later run", "author-a", "run-2"},
		{"no run at all", "author-a", ""},
		{"another principal in the same run", "other-a", "run-1"},
	} {
		t.Run(outside.name, func(t *testing.T) {
			res := recallWith(t, dir, "ws-1", RecallRequest{Query: "migration needs lock", Caller: outside.caller, RunID: outside.run})
			if slices.Contains(ids(res), e.ID) {
				t.Fatalf("outside its run the memory must not be served, got %v", ids(res))
			}
		})
	}
}

// A dir scoped memory is served inside the directory it was derived from and refused
// outside it, for its own author too.
func TestADirScopedMemoryIsInvisibleOutsideItsDirectory(t *testing.T) {
	dir := singleAgentDir(t)
	e := propose(t, dir, "ws-1", "author-a", "dir scoped", "the resolver drops unexported edges",
		func(r *ProposeContextRequest) { r.Visibility = ScopeDir; r.Dir = "api-go/internal/workspaceops" })
	if e.Scope != ScopeDir {
		t.Fatalf("stored scope = %q, want %q", e.Scope, ScopeDir)
	}
	if e.ScopeKey != "api-go/internal/workspaceops" {
		t.Fatalf("scope key = %q, want the directory it was derived from", e.ScopeKey)
	}
	inside := recallWith(t, dir, "ws-1", RecallRequest{Query: "resolver drops unexported edges", Caller: "author-a", Dir: "api-go/internal/workspaceops"})
	if !slices.Contains(ids(inside), e.ID) {
		t.Fatalf("inside its directory the memory must be served, got %v", ids(inside))
	}
	for _, outside := range []struct{ name, dir string }{
		{"a sibling directory", "api-go/internal/govstore"},
		{"the workspace root", ""},
		{"a prefix of the directory", "api-go/internal"},
	} {
		t.Run(outside.name, func(t *testing.T) {
			res := recallWith(t, dir, "ws-1", RecallRequest{Query: "resolver drops unexported edges", Caller: "author-a", Dir: outside.dir})
			if slices.Contains(ids(res), e.ID) {
				t.Fatalf("outside its directory the memory must not be served, got %v", ids(res))
			}
		})
	}
}

// A refused derivation writes nothing: the memory is absent from the store, not
// present with the default scope.
func TestARefusedDerivationWritesNoEntry(t *testing.T) {
	dir := singleAgentDir(t)
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{
		Title: "asks for a dir outside the workspace", Content: "should never be stored",
		Permission: "readwrite", Visibility: ScopeDir, Dir: "../elsewhere",
	}}
	if _, err := Remember(dir, "ws-1", req, ContextActor{ID: "author-a"}); err == nil {
		t.Fatal("a refused derivation must refuse the write")
	}
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "should never be stored", Caller: "author-a", IncludePending: true})
	if len(ids(res)) != 0 {
		t.Fatalf("a refused derivation must leave no row, got %v", ids(res))
	}
}

// The scope is part of what recall reports, so a caller can see the label it was
// served under without reading the store.
func TestRecallReportsTheScopeItServedUnder(t *testing.T) {
	dir := singleAgentDir(t)
	e := propose(t, dir, "ws-1", "author-a", "labelled", "the budget line is per build profile",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "budget line build profile", Caller: "author-a"})
	for _, got := range entriesOf(res) {
		if got.ID != e.ID {
			continue
		}
		if got.Scope != ScopePrivate {
			t.Fatalf("recall reported scope %q, want %q", got.Scope, ScopePrivate)
		}
		return
	}
	t.Fatalf("the memory was not served, got %v", ids(res))
}

// The refusals are distinguished, not merely present: an unknown visibility and a
// missing key are different faults and each message names its own cause.
func TestEachRefusalNamesItsOwnCause(t *testing.T) {
	cases := []struct {
		name string
		req  ScopeRequest
		want string
	}{
		{"an unknown visibility", ScopeRequest{Visibility: "everyone"}, `scope "everyone" is not one of`},
		{"a run with no run id", ScopeRequest{Visibility: ScopeRun}, `scope "run" needs run_id`},
		{"a session with no session id", ScopeRequest{Visibility: ScopeSession}, `scope "session" needs session_id`},
		{"a dir with no dir", ScopeRequest{Visibility: ScopeDir}, `scope "dir" needs dir`},
		{"an escaping dir", ScopeRequest{Visibility: ScopeDir, Dir: "../elsewhere"}, `must be a plain repo-relative path`},
		{"an absolute dir", ScopeRequest{Visibility: ScopeDir, Dir: "/etc"}, `must be repo-relative`},
		{"a dir named .", ScopeRequest{Visibility: ScopeDir, Dir: "."}, `must name a directory inside the workspace`},
		{"a dir with an empty component", ScopeRequest{Visibility: ScopeDir, Dir: "api-go//internal"}, `must be a plain repo-relative path`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DeriveScope(c.req, ContextActor{ID: "author-a"})
			if err == nil {
				t.Fatalf("%+v must be refused", c.req)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refusal %q does not name %q", err, c.want)
			}
		})
	}
}

// No role reads a private memory, admin included. The rule is in ScopesVisible, so
// this asserts it as the unit the rule is written in rather than through a store.
func TestNoRoleReadsAnotherPrincipalsPrivateMemory(t *testing.T) {
	for _, reader := range []string{
		"reader-b", "proposer-b", "verifier-b", "human-approver-b", "admin-b", "indexer-b", "operator-b",
	} {
		t.Run(reader, func(t *testing.T) {
			if ScopesVisible(ScopePrivate, "author-a", "author-a", reader, ScopeRequest{}) {
				t.Fatalf("role %q must not read another principal's private memory", reader)
			}
			if !ScopesVisible(ScopePrivate, "author-a", "author-a", "author-a", ScopeRequest{}) {
				t.Fatal("the author must read its own private memory")
			}
		})
	}
}

// Open mode has no principal, so a memory it writes is nobody's private memory and
// an open-mode caller reads nobody's.
func TestOpenModeOwnsNoPrivateMemory(t *testing.T) {
	if ScopesVisible(ScopePrivate, OpenModeIdentity, OpenModeIdentity, OpenModeIdentity, ScopeRequest{}) {
		t.Fatal("open mode must not read a memory written in open mode as private")
	}
	if ScopesVisible(ScopePrivate, OpenModeIdentity, OpenModeIdentity, "author-a", ScopeRequest{}) {
		t.Fatal("an open-mode caller must not read a private memory it did not write")
	}
}

// shared is unserved until a grant admits it (WS-29c). This row never grants, so the
// scope is invisible even to its own author, which is what stops an ungranted entry
// being served by the workspace default.
func TestSharedIsServedToNobodyUntilAGrantAdmitsIt(t *testing.T) {
	for _, reader := range []string{"author-a", "admin-a", OpenModeIdentity, ""} {
		if ScopesVisible(ScopeShared, "col-1", "author-a", reader, ScopeRequest{}) {
			t.Fatalf("shared must not be served to %q before a grant admits it", reader)
		}
	}
}

// The dir boundary is a path boundary: only the key itself or a path BELOW it is
// inside. A parent of the key is outside (a memory confined to a directory does not
// widen to its parents), and a sibling whose name merely starts with the key's is
// outside too.
func TestADirKeyMatchesOnPathSegmentsOnly(t *testing.T) {
	const key = "api-go/internal/workspaceops"
	for _, dir := range []string{key, key + "/x.go", key + "/memory"} {
		if !ScopesVisible(ScopeDir, key, "author-a", "author-a", ScopeRequest{Dir: dir}) {
			t.Fatalf("%q is inside the key and must be served", dir)
		}
	}
	for _, dir := range []string{key + "-extras", "api-go/internals", "api-go", "api-go/internal", key + "x", "", "other"} {
		if ScopesVisible(ScopeDir, key, "author-a", "author-a", ScopeRequest{Dir: dir}) {
			t.Fatalf("%q is outside the key and must not be served", dir)
		}
	}
}

// A confined entry with no key is served to nobody, including the author: a stored
// row whose key is missing is not a wildcard. Each case pairs an empty stored key
// with a reader that also names nothing, which is the combination a dropped
// "the reader named a key" check would let through.
func TestAConfinedEntryWithNoKeyIsServedToNobody(t *testing.T) {
	for _, scope := range []string{ScopeRun, ScopeSession, ScopeDir} {
		t.Run(scope, func(t *testing.T) {
			if ScopesVisible(scope, "", "author-a", "author-a", ScopeRequest{}) {
				t.Fatalf("scope %q with an empty key and a reader naming none must be served to nobody", scope)
			}
			if ScopesVisible(scope, "", "author-a", "author-a",
				ScopeRequest{RunID: "run-1", Dir: "api-go", SessionID: "claude:s1"}) {
				t.Fatalf("scope %q with an empty key must be served to nobody, reader keys or not", scope)
			}
		})
	}
}

// The leakage metric over this sub-task's fixture suite is 0. Every fixture carries
// an INDEPENDENT expected-visibility table, keyed by the reader AND the keys that
// reader's request names, because a run- or dir-scoped memory is served only inside
// them. Nothing here asks ScopesVisible what it decides: restating the implementation
// cannot detect it being wrong.
func TestScopeLeaksOverTheFixtureSuiteIsZero(t *testing.T) {
	dir := singleAgentDir(t)
	trustRunKeys(t, dir, "author-a", "author-b", "verifier-c")
	type fixture struct {
		id       string
		servable map[string]bool // "caller|run|dir" -> may read it
	}
	var fixtures []fixture
	for i, row := range []struct {
		mutate   func(*ProposeContextRequest)
		servable map[string]bool
	}{
		// workspace scoped: every principal in the workspace, whatever keys it names
		{func(r *ProposeContextRequest) {}, map[string]bool{
			"author-a|run-1|api-go/internal": true, "author-b|run-1|api-go/internal": true,
			"author-a||api-go/internal": true, "author-a|run-1|docs": true, "verifier-c||": true,
			OpenModeIdentity + "||": true}},
		// private: the author alone, whatever keys it names
		{func(r *ProposeContextRequest) { r.Visibility = ScopePrivate }, map[string]bool{
			"author-a|run-1|api-go/internal": true, "author-a||api-go/internal": true,
			"author-a|run-1|docs": true}},
		// run scoped: the author, inside run-1 only
		{func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" }, map[string]bool{
			"author-a|run-1|api-go/internal": true, "author-a|run-1|docs": true}},
		// dir scoped: the author, inside api-go/internal only
		{func(r *ProposeContextRequest) { r.Visibility = ScopeDir; r.Dir = "api-go/internal" }, map[string]bool{
			"author-a|run-1|api-go/internal": true, "author-a||api-go/internal": true,
			"author-a|run-1|docs": false}},
		// global: the author, in whatever workspace the read reaches (this one)
		{func(r *ProposeContextRequest) { r.Visibility = ScopeGlobal }, map[string]bool{
			"author-a|run-1|api-go/internal": true, "author-a||api-go/internal": true,
			"author-a|run-1|docs": true}},
		// another author's private memory
		{func(r *ProposeContextRequest) { r.Visibility = ScopePrivate }, nil}, // author-b, fixed below
	} {
		req := ProposeContextRequest{Title: fmt.Sprintf("fixture %d", i),
			Content: fmt.Sprintf("scope fixture body number %d", i), Permission: "readwrite"}
		row.mutate(&req)
		author := "author-a"
		if i == 5 {
			author = "author-b"
			row.servable = map[string]bool{"author-b|run-1|api-go/internal": true}
		}
		e, err := Remember(dir, "ws-1", RememberRequest{ProposeContextRequest: req}, ContextActor{ID: author})
		if err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		fixtures = append(fixtures, fixture{id: e.ID, servable: row.servable})
	}
	for _, reader := range []struct{ name, caller, run, dir string }{
		{"the author of most of them", "author-a", "run-1", "api-go/internal"},
		{"another author", "author-b", "run-1", "api-go/internal"},
		{"a reader with no run key", "author-a", "", "api-go/internal"},
		{"a reader in a directory holding none of them", "author-a", "run-1", "docs"},
		{"a verifier with no keys", "verifier-c", "", ""},
		{"open mode", OpenModeIdentity, "", ""},
	} {
		who := reader.caller + "|" + reader.run + "|" + reader.dir
		admitted := map[string]bool{}
		for _, f := range fixtures {
			if f.servable[who] {
				admitted[f.id] = true
			}
		}
		res := recallWith(t, dir, "ws-1", RecallRequest{Query: "scope fixture body", Caller: reader.caller,
			RunID: reader.run, Dir: reader.dir})
		served := map[string]bool{}
		for _, got := range entriesOf(res) {
			if !admitted[got.ID] {
				t.Fatalf("%s was served entry %s, which the fixture table does not admit", reader.name, got.ID)
			}
			served[got.ID] = true
		}
		for id := range admitted {
			if !served[id] {
				t.Fatalf("%s was not served entry %s, which the fixture table admits", reader.name, id)
			}
		}
		if n := ScopeLeaks(len(served), len(admitted)); n != 0 {
			t.Fatalf("%s: scope leak metric = %d over %d served and %d admitted",
				reader.name, n, len(served), len(admitted))
		}
	}
}

// The by-id read is not a one-way door: the author of a run, session or directory
// scoped memory must still be able to fetch it by id from inside that key, because
// the check that refuses everyone else would otherwise make it unreadable to
// everyone.
func TestTheByIDReadServesTheAuthorInsideTheConfiningKey(t *testing.T) {
	dir := singleAgentDir(t)
	trustRunKeys(t, dir, "author-a", "author-b", "author-c")
	run := propose(t, dir, "ws-1", "author-a", "by id run", "the run holds an open migration",
		func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" })
	dirScoped := propose(t, dir, "ws-1", "author-a", "by id dir", "the resolver drops unexported edges",
		func(r *ProposeContextRequest) { r.Visibility = ScopeDir; r.Dir = "api-go/internal" })
	sess := propose(t, dir, "ws-1", "author-a", "by id session", "the handoff names the next step",
		func(r *ProposeContextRequest) { r.Visibility = ScopeSession; r.SessionID = "claude:s1" })

	for _, c := range []struct {
		name  string
		id    string
		keys  []string
		serve bool
	}{
		{"a run entry inside its run", run.ID, []string{"author-a", "", "run-1", ""}, true},
		{"a run entry in another run", run.ID, []string{"author-a", "", "run-2", ""}, false},
		{"a run entry with no run", run.ID, []string{"author-a"}, false},
		{"a dir entry inside its directory", dirScoped.ID, []string{"author-a", "api-go/internal", "", ""}, true},
		{"a dir entry in a child directory", dirScoped.ID, []string{"author-a", "api-go/internal/govstore", "", ""}, true},
		{"a dir entry in a sibling of its directory", dirScoped.ID, []string{"author-a", "api-go/cmd", "", ""}, false},
		{"a dir entry in a parent of its directory", dirScoped.ID, []string{"author-a", "api-go", "", ""}, false},
		{"a dir entry with no directory", dirScoped.ID, []string{"author-a"}, false},
		{"a session entry inside its session", sess.ID, []string{"author-a", "", "", "claude:s1"}, true},
		{"a session entry in another session", sess.ID, []string{"author-a", "", "", "claude:s2"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := GetContextEntry(dir, "ws-1", c.id, false, true, c.keys...)
			if c.serve && err != nil {
				t.Fatalf("the author must fetch it inside the key: %v", err)
			}
			if !c.serve && err == nil {
				t.Fatal("the by-id read must refuse it outside the key")
			}
		})
	}
}

// Knowing or guessing an entry id is not a way round a private memory: the by-id
// read refuses with the not-found shape, and the listing paths leave it out.
func TestTheByIDAndListingReadPathsEnforceTheSameScope(t *testing.T) {
	dir := singleAgentDir(t)
	trustRunKeys(t, dir, "author-a", "author-b", "author-c")
	private := propose(t, dir, "ws-1", "author-a", "by id", "the token rotation path mints a secret",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	run := propose(t, dir, "ws-1", "author-a", "by id run", "the run holds an open migration",
		func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" })

	other := ContextActor{ID: "verifier-b", Verifier: true}
	admin := ContextActor{ID: "admin-b", Admin: true, Verifier: true}
	for _, reader := range []struct {
		name   string
		caller string
		id     string
	}{
		{"a verifier fetching by id", "verifier-b", private.ID},
		{"an admin fetching by id", "admin-b", private.ID},
		{"a verifier fetching a run entry with no run", "verifier-b", run.ID},
		{"an admin fetching a run entry with no run", "admin-b", run.ID},
	} {
		t.Run(reader.name, func(t *testing.T) {
			if _, err := GetContextEntry(dir, "ws-1", reader.id, false, true, reader.caller); err == nil {
				t.Fatal("a by-id fetch must not disclose an entry its scope does not admit")
			}
		})
	}
	for _, actor := range []ContextActor{other, admin} {
		t.Run(actor.ID+" listing", func(t *testing.T) {
			for _, filter := range []string{"all", "pending", "promoted"} {
				entries, err := ListContextEntries(dir, "ws-1", filter, actor.ID)
				if err != nil {
					t.Fatalf("list %s: %v", filter, err)
				}
				for _, e := range entries {
					if e.ID == private.ID || e.ID == run.ID {
						t.Fatalf("%s was listed under %q at scope %q, which its scope does not admit",
							actor.ID, filter, e.Scope)
					}
				}
			}
		})
	}
	// The author still reads its own entries by id, and the operator's own view
	// (no reader named) still counts what exists.
	if _, err := GetContextEntry(dir, "ws-1", private.ID, false, true, "author-a"); err != nil {
		t.Fatalf("the author must fetch its own private entry by id: %v", err)
	}
	if _, err := GetContextEntry(dir, "ws-1", private.ID, false, true); err != nil {
		t.Fatalf("the operator view must fetch any entry by id: %v", err)
	}
	if entries, err := ListContextEntries(dir, "ws-1", "all"); err != nil || len(entries) < 2 {
		t.Fatalf("the operator view must list every entry: %v (%d entries)", err, len(entries))
	}
}

// An escaping or absolute directory is refused on the implicit path too, so a
// memory is never stored under a key that confines it to nothing.
func TestAnImplicitDirectoryKeyIsValidatedLikeAnExplicitOne(t *testing.T) {
	for _, dir := range []string{"../elsewhere", "/etc", ".", "api-go//internal", "..", "a/../b"} {
		if _, err := DeriveScope(ScopeRequest{Dir: dir}, ContextActor{ID: "author-a"}); err == nil {
			t.Fatalf("implicit dir %q must be refused, not stored", dir)
		}
	}
	if _, err := DeriveScope(ScopeRequest{Dir: "api-go/internal"}, ContextActor{ID: "author-a"}); err != nil {
		t.Fatalf("a plain repo-relative dir must be accepted: %v", err)
	}
}

// remember's run_id and session_id are PROVENANCE, not scope keys: they name where a
// write came from and are recorded either way, but they do not make the memory
// confined, because the MCP recall tool has no run or session key to read it back
// with (review round 2, P2). A caller that wants run scope asks for it explicitly.
func TestARememberCallKeepsItsRunAndSessionKeysAsProvenanceNotScope(t *testing.T) {
	dir := singleAgentDir(t)
	for _, c := range []struct {
		name string
		req  RememberRequest
	}{
		{"run_id", RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "provenance run",
			Content: "the run is recorded, not the scope", Permission: "readwrite"}}},
		{"session_id", RememberRequest{SessionID: "claude:s9",
			ProposeContextRequest: ProposeContextRequest{Title: "provenance session",
				Content: "the session is recorded, not the scope", Permission: "readwrite"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, err := Remember(dir, "ws-1", c.req, ContextActor{ID: "author-a"})
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			if e.Scope != ScopeWorkspace {
				t.Fatalf("provenance %s made the memory %q scoped, want %q", c.name, e.Scope, ScopeWorkspace)
			}
		})
	}
	// The keys are recorded as provenance, which is where they were before this row.
	if p := (RememberRequest{RunID: "run-9", SessionID: "claude:s9"}).proposal(); p.RunID != "" || p.SessionID != "" {
		t.Fatalf("proposal() leaked the provenance keys into the scope request: run=%q session=%q", p.RunID, p.SessionID)
	}
	t.Run("visibility=run still confines", func(t *testing.T) {
		scoped, err := DeriveScoped(ScopeRequest{Visibility: ScopeRun, RunID: "run-9"}, ContextActor{ID: "author-a"})
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		if scoped.Scope != ScopeRun || scoped.Key != "run-9" {
			t.Fatalf("derived %q/%q, want run/run-9", scoped.Scope, scoped.Key)
		}
	})
}

// global is derived from the principal like private, so it can never be widened to
// another user. Its author reads it in whichever workspace the read reaches, which
// today means its own: nothing in this row reaches across workspaces (WS-30e).
func TestGlobalIsDerivedFromThePrincipalAndReadOnlyByItsAuthor(t *testing.T) {
	dir := singleAgentDir(t)
	e := propose(t, dir, "ws-1", "author-a", "global", "the toolchain lives in the home image",
		func(r *ProposeContextRequest) { r.Visibility = ScopeGlobal })
	if e.Scope != ScopeGlobal || e.ScopeKey != "author-a" {
		t.Fatalf("stored scope %q/%q, want global keyed by the author", e.Scope, e.ScopeKey)
	}
	if !ScopesVisible(ScopeGlobal, "author-a", "author-a", "author-a", ScopeRequest{}) {
		t.Fatal("the author must read its own global memory")
	}
	for _, reader := range []string{"admin-a", "verifier-a", "author-b", OpenModeIdentity, ""} {
		if ScopesVisible(ScopeGlobal, "author-a", "author-a", reader, ScopeRequest{}) {
			t.Fatalf("global must not be served to %q", reader)
		}
	}
	res := recallWith(t, dir, "ws-1", RecallRequest{Query: "toolchain home image", Caller: "author-a"})
	if !slices.Contains(ids(res), e.ID) {
		t.Fatalf("the author must find its global memory by recall, got %v", ids(res))
	}
	res = recallWith(t, dir, "ws-1", RecallRequest{Query: "toolchain home image", Caller: "admin-a"})
	if slices.Contains(ids(res), e.ID) {
		t.Fatalf("another principal must not find a global memory, got %v", ids(res))
	}
	// Open mode has no principal, so it owns no user-level namespace.
	if _, err := DeriveScope(ScopeRequest{Visibility: ScopeGlobal}, ContextActor{ID: OpenModeIdentity}); err == nil {
		t.Fatal("open mode must not derive a global scope")
	}
	if _, err := DeriveScope(ScopeRequest{Visibility: ScopeGlobal}, ContextActor{}); err == nil {
		t.Fatal("an anonymous caller must not derive a global scope")
	}
}

// The standing-memory projection is pushed into every session and cached per
// workspace with no reader identity, so a confined memory must not be in it at all:
// its author cannot be told apart from anybody else there (WS-29b, review round 2).
func TestTheStandingMemoryProjectionCarriesOnlyWorkspaceScopedEntries(t *testing.T) {
	dir := singleAgentDir(t)
	trustRunKeys(t, dir, "author-a", "author-b", "author-c")
	workspace := propose(t, dir, "ws-1", "author-a", "projected", "the budget line is per build profile omega")
	private := propose(t, dir, "ws-1", "author-a", "not projected", "the staging credentials live in vault omega",
		func(r *ProposeContextRequest) { r.Visibility = ScopePrivate })
	run := propose(t, dir, "ws-1", "author-a", "not projected either", "the run holds an open migration omega",
		func(r *ProposeContextRequest) { r.Visibility = ScopeRun; r.RunID = "run-1" })

	proj, err := CoreProjection(context.Background(), dir, "ws-1", ProjectionDefaultChars)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	// The index lists served entries by title and description, so a confined memory
	// would show up here rather than in Core; both the rendered text and the count are
	// checked, because a count alone could pass with the right number and the wrong
	// entries.
	for _, confined := range []struct {
		id    string
		title string
		what  string
	}{
		{private.ID, "not projected", "a private memory"},
		{run.ID, "not projected either", "a run-scoped memory"},
	} {
		if strings.Contains(proj.Text, confined.id) || strings.Contains(proj.Text, confined.title) {
			t.Errorf("%s reached the projection text:\n%s", confined.what, proj.Text)
		}
	}
	if proj.Deferred.Entries != 1 {
		t.Errorf("the deferred index counted %d served entries, want only the workspace-scoped one",
			proj.Deferred.Entries)
	}
	if !strings.Contains(proj.Text, workspace.ID) && proj.Deferred.Entries != 1 {
		t.Errorf("the workspace-scoped memory must still be projected:\n%s", proj.Text)
	}
}

// open mode has no principal, so a private or global memory would be one its writer
// can never read back; the derivation refuses both rather than storing a dead entry.
func TestAScopeThatNeedsAPrincipalIsRefusedInOpenMode(t *testing.T) {
	for _, scope := range []string{ScopePrivate, ScopeGlobal} {
		t.Run(scope, func(t *testing.T) {
			if _, err := DeriveScope(ScopeRequest{Visibility: scope}, ContextActor{ID: OpenModeIdentity}); err == nil {
				t.Fatalf("%s must be refused for the open-mode identity", scope)
			}
			if _, err := DeriveScope(ScopeRequest{Visibility: scope}, ContextActor{}); err == nil {
				t.Fatalf("%s must be refused for an anonymous caller", scope)
			}
		})
	}
}

// The derivation is one table and refuses what it does not know: a guard clause, not
// a ladder over the visibility values.
func TestAnUnknownVisibilityIsRefusedRatherThanWidened(t *testing.T) {
	for _, vis := range []string{"", "everyone", "workspace ", "GLOBAL", "private; drop table"} {
		if _, err := DeriveScope(ScopeRequest{Visibility: vis}, ContextActor{ID: "author-a"}); err != nil &&
			strings.TrimSpace(vis) == ScopeWorkspace {
			t.Fatalf("workspace must stay accepted, got %v", err)
		} else if err == nil && strings.TrimSpace(vis) != "" && strings.TrimSpace(vis) != ScopeWorkspace {
			t.Fatalf("visibility %q must be refused, not defaulted", vis)
		}
	}
}
