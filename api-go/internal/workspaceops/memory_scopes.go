package workspaceops

import (
	"fmt"
	"strings"
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
		// it is why the request was made, not an extra assertion about visibility.
		switch {
		case strings.TrimSpace(req.RunID) != "":
			return Scoped{Scope: ScopeRun, Key: strings.TrimSpace(req.RunID)}, nil
		case strings.TrimSpace(req.SessionID) != "":
			return Scoped{Scope: ScopeSession, Key: strings.TrimSpace(req.SessionID)}, nil
		case strings.TrimSpace(req.Dir) != "":
			return Scoped{Scope: ScopeDir, Key: strings.TrimSpace(req.Dir)}, nil
		}
		return Scoped{Scope: ScopeWorkspace}, nil
	}
	if visibility == ScopeWorkspace {
		return Scoped{Scope: ScopeWorkspace}, nil
	}
	if visibility == ScopePrivate {
		return Scoped{Scope: ScopePrivate, Key: strings.TrimSpace(actor.ID)}, nil
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
	return []string{ScopeWorkspace, ScopePrivate, ScopeDir, ScopeRun, ScopeSession, ScopeShared}
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

// ScopeLeaks reports how many more entries a reader was served than its scope
// admits: the scope-leak metric a fixture suite collects. It is 0 or the row has a
// leak.
func ScopeLeaks(total, admitted int) int {
	if admitted > total {
		return admitted - total
	}
	return 0
}

// scopeRequestOf is the reader's own narrowing keys, so a recall that names a run,
// a session or a directory reads inside it.
func scopeRequestOf(req RecallRequest) ScopeRequest {
	return ScopeRequest{Dir: strings.TrimSpace(req.Dir), RunID: strings.TrimSpace(req.RunID),
		SessionID: strings.TrimSpace(req.SessionID)}
}
