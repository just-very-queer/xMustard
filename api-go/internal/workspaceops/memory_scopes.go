package workspaceops

import (
	"fmt"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// Memory scopes (PAR-SHARE-01). A memory's scope is DERIVED from the writing
// principal and the request; no payload field, metadata key or actor field can
// choose it (the identity smuggling rule, SEC-05's companion).
const (
	// ScopeWorkspace is the default: the memory serves the whole workspace.
	ScopeWorkspace = "workspace"
	// ScopePrivate serves only the principal that wrote it, whatever any other
	// principal's roles are. Admin is not an exception.
	ScopePrivate = "private"
	// ScopeDir serves the directory prefix it was derived from.
	ScopeDir = "dir"
	// ScopeRun serves one run, and ScopeSession one session, and dies with it.
	ScopeRun     = "run"
	ScopeSession = "session"
	// ScopeGlobal is user-level: one principal across every workspace it may reach
	// (PAR-SHARE-01). Its key is the principal, so it is derived and never supplied.
	ScopeGlobal = "global"
	// ScopeShared serves what a grant admits (WS-29c); this sub-task never derives it.
	ScopeShared = "shared"
)

// ScopeRequest is the input the scope is derived from. Visibility asks for a
// narrower scope; the keys it names must be present for it to be granted.
type ScopeRequest struct {
	Visibility string
	Dir        string
	RunID      string
	SessionID  string
}

// Scoped is a derived scope and the key that confines it.
type Scoped struct {
	Scope string
	Key   string
}

// scopeKeys names the request field each non-default scope is keyed by, so the
// derivation is one lookup rather than a ladder over the visibility values.
var scopeKeys = map[string]struct {
	field string
	key   func(ScopeRequest) string
}{
	ScopeRun:     {"run_id", func(r ScopeRequest) string { return r.RunID }},
	ScopeSession: {"session_id", func(r ScopeRequest) string { return r.SessionID }},
	ScopeDir:     {"dir", func(r ScopeRequest) string { return r.Dir }},
}

// DeriveScope returns the scope a memory written by actor from req is stored
// under. It refuses what it does not know rather than defaulting to the widest
// scope: an unrecognised visibility, or a narrower scope whose key is missing or
// escapes the workspace, is an error and nothing is written.
func DeriveScope(req ScopeRequest, actor ContextActor) (string, error) {
	scoped, err := DeriveScoped(req, actor)
	return scoped.Scope, err
}

// DeriveScoped is DeriveScope with the confining key.
func DeriveScoped(req ScopeRequest, actor ContextActor) (Scoped, error) {
	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		// A run or session key alone already narrows the memory to that execution:
		// it is why the request was made, not an extra assertion about visibility. A
		// directory is validated here exactly as the explicit dir scope is, so an
		// escaping or absolute directory is refused on both paths.
		switch {
		case strings.TrimSpace(req.RunID) != "":
			return Scoped{Scope: ScopeRun, Key: strings.TrimSpace(req.RunID)}, nil
		case strings.TrimSpace(req.SessionID) != "":
			return Scoped{Scope: ScopeSession, Key: strings.TrimSpace(req.SessionID)}, nil
		case strings.TrimSpace(req.Dir) != "":
			dir := strings.TrimSpace(req.Dir)
			if err := validScopeDir(dir); err != nil {
				return Scoped{}, err
			}
			return Scoped{Scope: ScopeDir, Key: dir}, nil
		}
		return Scoped{Scope: ScopeWorkspace}, nil
	}
	if visibility == ScopeWorkspace {
		return Scoped{Scope: ScopeWorkspace}, nil
	}
	if visibility == ScopePrivate {
		// A private memory is served to its author and to nobody else, so it needs an
		// author to be readable by. Open mode has no principal, and samePrincipal
		// rejects that identity for every reader, so accepting the write would store a
		// memory its own writer can never read back: the derivation refuses it, as it
		// refuses global for the same reason.
		who := strings.TrimSpace(actor.ID)
		if who == "" || IsOpenModeIdentity(who) {
			return Scoped{}, fmt.Errorf("%w: scope %q needs a named principal", ErrInvalidInput, visibility)
		}
		return Scoped{Scope: ScopePrivate, Key: who}, nil
	}
	if visibility == ScopeGlobal {
		// User-level and derived from the principal, exactly like private: the key is
		// who wrote it, so no caller can widen a global memory to another user. It is
		// served to nobody until a cross-workspace read path can scope a query to one
		// principal's key across workspaces (PAR-SHARE-01's merge step, and WS-30e).
		who := strings.TrimSpace(actor.ID)
		if who == "" || IsOpenModeIdentity(who) {
			return Scoped{}, fmt.Errorf("%w: scope %q needs a named principal", ErrInvalidInput, visibility)
		}
		return Scoped{Scope: ScopeGlobal, Key: who}, nil
	}
	keyed, known := scopeKeys[visibility]
	if !known {
		return Scoped{}, fmt.Errorf("%w: scope %q is not one of %s", ErrInvalidInput, visibility,
			strings.Join(ScopeNames(), ", "))
	}
	key := strings.TrimSpace(keyed.key(req))
	if key == "" {
		return Scoped{}, fmt.Errorf("%w: scope %q needs %s", ErrInvalidInput, visibility, keyed.field)
	}
	if keyed.field == "dir" {
		if err := validScopeDir(key); err != nil {
			return Scoped{}, err
		}
	}
	return Scoped{Scope: visibility, Key: key}, nil
}

// ScopeNames lists every derivable scope, canonical order.
func ScopeNames() []string {
	return []string{ScopeWorkspace, ScopePrivate, ScopeDir, ScopeRun, ScopeSession, ScopeGlobal, ScopeShared}
}

// validScopeDir refuses a directory key that is not a plain repo-relative path
// inside the workspace. It is a guard clause, not a tidy-up: an absolute path, a
// parent step or a scheme would confine the memory to nothing, or to everything.
func validScopeDir(dir string) error {
	switch {
	case strings.HasPrefix(dir, "/"), strings.Contains(dir, "://"):
		return fmt.Errorf("%w: scope dir %q must be repo-relative", ErrInvalidInput, dir)
	case dir == ".", dir == "..":
		return fmt.Errorf("%w: scope dir %q must name a directory inside the workspace", ErrInvalidInput, dir)
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: scope dir %q must be a plain repo-relative path", ErrInvalidInput, dir)
		}
	}
	return nil
}

// ScopesVisible reports whether reader may see an entry of scope scope, keyed by
// key and authored by author. It is the one rule that reads the derivation, so
// private is invisible here by construction rather than by a filter above.
//
//   - workspace: everyone in the workspace
//   - private: the author only, whatever roles the reader holds (admin included)
//   - run, session: the author inside the matching key, and nobody else
//   - dir: the author, inside the directory or one below it
//   - global: the author, in whichever workspace the read reaches (WS-30e merges
//     them; nothing here reaches across workspaces yet)
//   - shared: WS-29c decides from grants; this sub-task never serves it
func ScopesVisible(scope, key, author, reader string, req ScopeRequest) bool {
	if scope == ScopeShared {
		return false // no grant can reach an entry whose scope was never shared
	}
	if scope == ScopeWorkspace || scope == "" {
		return true
	}
	// Every remaining scope is the author's alone, so the author identity is checked
	// first and never bypassed by a role.
	if !samePrincipal(author, reader) {
		return false
	}
	switch scope {
	case ScopePrivate:
		return true
	case ScopeGlobal:
		// User-level: the author, in any workspace this read reaches. Merging it across
		// workspaces is WS-30e's job; until a cross-workspace read exists this rule
		// simply never fires for another workspace, which is the fail-closed direction.
		return samePrincipal(author, reader)
	case ScopeRun:
		return strings.TrimSpace(req.RunID) != "" && req.RunID == key
	case ScopeSession:
		return strings.TrimSpace(req.SessionID) != "" && req.SessionID == key
	case ScopeDir:
		dir := strings.TrimSpace(req.Dir)
		return dir != "" && (dir == key || strings.HasPrefix(dir, key+"/"))
	}
	return false
}

// samePrincipal compares two principal ids. Open mode has no principal of its own,
// so an entry written by it is nobody's private memory.
func samePrincipal(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" || IsOpenModeIdentity(a) || IsOpenModeIdentity(b) {
		return false
	}
	return a == b
}

// ScopeLeaks is the scope-leak metric a fixture suite collects: how many of the
// entries a reader was SERVED its scope does not admit. It is 0 or the row has a
// leak, so a reader served more than it was admitted is a leak, and a reader served
// fewer is an under-serve, not a leak.
func ScopeLeaks(served, admitted int) int {
	if served > admitted {
		return served - admitted
	}
	return 0
}

// checkRunKeyTrust refuses a run- or session-scoped write whose asserting principal the
// operator has not listed, and audits the refusal. A key alone is not authority: the run
// id comes from a query parameter or a request field, so a later run that supplies an
// earlier run's id would otherwise read that run's memory. Whether an assertion is
// allowed is an operator setting (owner decision 2026-10-03) and its default trusts
// nobody, which is the fail-closed direction; a principal that IS listed is served
// normally. A workspace-scoped write is untouched, so citing a run as provenance stays
// free.
func checkRunKeyTrust(dataDir, workspaceID, principal string, scoped Scoped) error {
	if scoped.Scope != ScopeRun && scoped.Scope != ScopeSession {
		return nil
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	if trustsRunKey(settings, principal) {
		return nil
	}
	// The audit record is best-effort: the refusal stands whether or not it is written,
	// because a failed write must not turn into a permitted run key.
	_ = recordAuditEventNoGuard(dataDir, workspaceID, AuditEvent{
		Actor: principal, Action: AuditRunKeyRefused, TargetType: "memory_scope",
		TargetID: scoped.Scope + ":" + scoped.Key,
		Details:  "principal " + ledgerPrincipal(principal) + " is not a trusted run key principal",
	})
	return fmt.Errorf("%w: run key %q needs a trusted run key principal (set trusted_run_key_principals)",
		ErrInvalidInput, scoped.Key)
}

// atRiskPage is the first window readAtRisk reads per store call, and
// atRiskScanCeiling the widest it will widen to. The window grows by doubling until the
// caller's page is full or the store runs out, because the store orders at-risk rows by
// staleness while its cursor pages by insertion, so the two cannot be combined to read
// "the rest of this order".
const (
	atRiskPage        = 100
	atRiskScanCeiling = 4000
)

// dropUntrustedRunKey narrows the READER's run and session keys with the same operator
// setting that guards a write. A key arrives as a query parameter or a request field,
// so a later run under the same principal could name an earlier run's id and read that
// run's memory; checking only the write left the read unguarded (round 4, r4-2). An
// untrusted key is DROPPED rather than refused: the read still answers, and narrowing by
// nothing serves no confined entry, which is the fail-closed direction. The attempt is
// audited exactly as the write refusal is, so an operator can see it.
//
// It returns the narrowed request and whether anything was dropped, and touches nothing
// else, because a recall request's SessionID is ALSO its session-seen key and clearing
// that would switch suppression off rather than narrow a scope; the caller writes the
// result to the separate scope view (RecallRequest.scopeSessionKey).
func dropUntrustedRunKey(dataDir, workspaceID, principal string, req ScopeRequest) (ScopeRequest, bool) {
	run, session := strings.TrimSpace(req.RunID), strings.TrimSpace(req.SessionID)
	if run == "" && session == "" {
		return req, false
	}
	settings, err := loadSettings(dataDir)
	if err != nil || trustsRunKey(settings, principal) {
		return req, false
	}
	// The audit record is best-effort, for the same reason the write's is: a failed
	// write of the event must not fail the read, and a failed read must not become a
	// permitted key.
	_ = recordAuditEventNoGuard(dataDir, workspaceID, AuditEvent{
		Actor: principal, Action: AuditRunKeyRefused, TargetType: "memory_scope",
		TargetID: run + "/" + session,
		Details:  "principal " + ledgerPrincipal(principal) + " named an untrusted run or session key on a read",
	})
	req.RunID, req.SessionID = "", ""
	return req, true
}

// visibleEntries is the one list-side filter: entries is what a query returned, and the
// result is the ones reader's scope admits. Every surface that LISTS entries passes
// through it rather than reading the store directly, because a listing is a disclosure
// of the same class as a read: an id, title, path or anchor returned for an entry the
// reader may not see leaks exactly what the content would have (round 3: the ranking,
// readAtRisk, the settle-and-report path and the handoff projection each listed entries
// on their own, and each was found by a separate round).
func visibleEntries(entries []govstore.Entry, reader string, req ScopeRequest) []govstore.Entry {
	out := entries[:0]
	for _, e := range entries {
		if ScopesVisible(e.Scope, e.ScopeKey, e.Source, reader, req) {
			out = append(out, e)
		}
	}
	return out
}

// scopeRequestOf is the reader's own narrowing keys, so a recall that names a run,
// a session or a directory reads inside it.
func scopeRequestOf(req RecallRequest) ScopeRequest {
	return ScopeRequest{Dir: strings.TrimSpace(req.Dir), RunID: strings.TrimSpace(req.RunID),
		SessionID: req.sessionScopeKey()}
}
