package mcpserver

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"xmustard/api-go/internal/budget"
)

// Where a call's workspace came from, in resolution order: an explicit argument, the
// XMUSTARD_WORKSPACE_ID binding, an absolute path argument, the client's roots
// (roots/list), then the server's working directory.
const (
	SourceArgument = "argument"
	SourceEnv      = "env"
	SourcePath     = "path"
	SourceRoots    = "roots"
	SourceCWD      = "cwd"
)

// Workspace is the workspace a call ran against, echoed in every result.
type Workspace struct {
	ID         string `json:"workspace_id"`
	Root       string `json:"root,omitempty"`
	Source     string `json:"source"`
	Registered bool   `json:"registered,omitempty"` // registered with the API by this call
}

func (w Workspace) echo() string {
	s := "[xmustard workspace] " + w.ID
	if w.Root != "" {
		s += " (" + w.Root + ")"
	}
	s += ", resolved from " + w.Source
	if w.Registered {
		s += "; registered with the API by this call"
	}
	return s
}

const (
	// rootsTimeout bounds how long a tool call waits for the client's roots/list answer.
	rootsTimeout = 5 * time.Second
	// rootsRetryAfter is how long calls skip roots/list after a timed-out one, so a
	// client that never answers does not cost every call rootsTimeout.
	rootsRetryAfter = 15 * time.Second
	// listingMissRefresh is how old the cached listing must be before a workspace id
	// missing from it triggers a re-read (to learn the root of a new workspace).
	listingMissRefresh = 10 * time.Second
	// maxPathCache bounds the per-session cache of resolved roots and working
	// directories; it is cleared when full.
	maxPathCache = 32
)

// rootsState is the session's view of the client's roots. A definitive answer (the
// roots, an answer with no file roots, or method-not-found) is kept until
// roots/list_changed; a failed or timed-out request is transient and asked again.
type rootsState struct {
	paths     []string
	err       string    // why the client's roots are unusable, for the resolution error
	transient bool      // the request failed; the client's roots are unknown, not absent
	retryAt   time.Time // transient: roots/list is not asked again before this
}

type registeredWorkspace struct {
	ID   string `json:"workspace_id"`
	Root string `json:"root_path"`
}

// resolveWorkspace determines the workspace of a call whose workspace_id may be
// omitted. It fails with an error naming every signal it tried.
func (s *Session) resolveWorkspace(ctx context.Context, t *Tool, args map[string]string) (Workspace, error) {
	pathArg := ""
	if t.PathArg != "" && filepath.IsAbs(args[t.PathArg]) {
		pathArg = args[t.PathArg]
	}
	bound := func(id, source string) (Workspace, error) {
		return Workspace{ID: id, Root: s.rootFor(ctx, id), Source: source}, nil
	}
	if id := strings.TrimSpace(args["workspace_id"]); id != "" {
		return bound(id, SourceArgument)
	}
	if id := strings.TrimSpace(s.srv.opts.Getenv("XMUSTARD_WORKSPACE_ID")); id != "" {
		return bound(id, SourceEnv)
	}
	// Path-based signals may list or register workspaces: one at a time per session.
	select {
	case s.resolving <- struct{}{}:
		defer func() { <-s.resolving }()
	case <-ctx.Done():
		return Workspace{}, ctx.Err()
	}
	var tried []string
	tried = append(tried, "no workspace_id argument", "XMUSTARD_WORKSPACE_ID is unset")
	if pathArg != "" {
		// a model-supplied path only selects among registered workspaces; it never
		// registers (and scans) a repository on its own
		ws, err := s.byPathSignal(ctx, pathArg, SourcePath, "a path argument never registers a repository")
		if err == nil {
			return ws, nil
		}
		if isFatal(err) {
			return Workspace{}, err
		}
		tried = append(tried, fmt.Sprintf("%s %s: %v", t.PathArg, pathArg, err))
	}
	roots, rootsWhy, rootsUnknown := s.clientRoots(ctx)
	switch {
	case len(roots) == 1:
		ws, err := s.byPathSignal(ctx, roots[0], SourceRoots, "")
		if err == nil || isFatal(err) {
			return ws, err
		}
		tried = append(tried, fmt.Sprintf("client root %s: %v", roots[0], err))
	case len(roots) > 1:
		if pick := containing(roots, s.srv.opts.Cwd); pick != "" {
			ws, err := s.byPathSignal(ctx, pick, SourceRoots, "")
			if err == nil || isFatal(err) {
				return ws, err
			}
			tried = append(tried, fmt.Sprintf("client root %s: %v", pick, err))
		} else {
			tried = append(tried, fmt.Sprintf("the client has %d roots (%s) and none contains the working directory", len(roots), strings.Join(roots, ", ")))
		}
	default:
		tried = append(tried, rootsWhy)
	}
	// While the client's roots are unknown (its roots/list failed), the working
	// directory may still select a registered workspace, but it never registers one:
	// the roots the client declared may name a different repository.
	cwdRegister := ""
	if rootsUnknown {
		cwdRegister = "not auto-registered while the client's roots are unavailable"
	}
	if cwd := s.srv.opts.Cwd; cwd != "" {
		ws, err := s.byPathSignal(ctx, cwd, SourceCWD, cwdRegister)
		if err == nil || isFatal(err) {
			return ws, err
		}
		tried = append(tried, fmt.Sprintf("working directory %s: %v", cwd, err))
	} else {
		tried = append(tried, "the server has no working directory")
	}
	msg := "no workspace resolved (" + strings.Join(tried, "; ") + "). Pass workspace_id"
	if list, _, err := s.listWorkspaces(ctx, false); err == nil && len(list) > 0 {
		msg += "; registered: " + describe(list, 5)
	}
	return Workspace{}, errors.New(msg)
}

// fatalError aborts resolution instead of falling through to the next signal
// (admission refusal, cancellation).
type fatalError struct{ error }

func (e fatalError) Unwrap() error { return e.error }

func isFatal(err error) bool {
	var f fatalError
	return errors.As(err, &f) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// byPathSignal maps a filesystem path (a root, the cwd, or an absolute path argument)
// to the registered workspace that contains it. When none does, the enclosing git
// repository is registered if auto-registration is on and noRegister is empty (roots
// and cwd are the client's and the operator's statements; noRegister says why a
// signal may not register).
//
// Roots and working directories are cached per session. A path argument is not: it
// is model-supplied, so each one is matched against the cached listing instead of
// growing resident state.
func (s *Session) byPathSignal(ctx context.Context, p, source, noRegister string) (Workspace, error) {
	p = canonicalPath(p)
	s.mu.Lock()
	if ws, ok := s.byPath[p]; ok {
		s.mu.Unlock()
		ws.Source, ws.Registered = source, false
		return ws, nil
	}
	s.mu.Unlock()
	remember := func(ws Workspace) Workspace {
		if source == SourcePath {
			return ws
		}
		s.mu.Lock()
		if len(s.byPath) >= maxPathCache {
			clear(s.byPath)
		}
		s.byPath[p] = ws
		s.mu.Unlock()
		return ws
	}
	for refresh := false; ; refresh = true {
		list, fresh, err := s.listWorkspaces(ctx, refresh)
		if err != nil {
			return Workspace{}, err
		}
		if w, ok := match(list, p); ok {
			return remember(Workspace{ID: w.ID, Root: w.Root, Source: source}), nil
		}
		if fresh { // a miss on a just-read listing is final
			break
		}
	}
	repo := repoRoot(p)
	switch {
	case repo == "":
		return Workspace{}, errors.New("not inside a registered workspace or a git repository")
	case repo == "/" || (s.srv.opts.HomeDir != "" && repo == canonicalPath(s.srv.opts.HomeDir)):
		return Workspace{}, fmt.Errorf("git repository %s is not registered, and a home or root directory is never auto-registered", repo)
	case noRegister != "":
		return Workspace{}, fmt.Errorf("git repository %s is not registered (%s)", repo, noRegister)
	case !s.srv.opts.AutoRegister:
		return Workspace{}, fmt.Errorf("git repository %s is not registered (auto-registration is off: XMUSTARD_MCP_AUTO_REGISTER=0)", repo)
	}
	if err := s.register(ctx, repo); err != nil {
		return Workspace{}, err
	}
	list, _, err := s.listWorkspaces(ctx, true)
	if err != nil {
		return Workspace{}, err
	}
	w, ok := match(list, p)
	if !ok {
		return Workspace{}, fmt.Errorf("registered %s but the API does not list it (expected id %s)", repo, WorkspaceIDForPath(repo))
	}
	ws := remember(Workspace{ID: w.ID, Root: w.Root, Source: source})
	ws.Registered = true
	return ws, nil
}

// register asks the API to load (and, when it has no cached snapshot, scan) the
// repository; the answer is a full snapshot, so it is drained rather than buffered.
func (s *Session) register(ctx context.Context, repo string) error {
	body, _ := json.Marshal(map[string]any{"root_path": repo, "auto_scan": true, "prefer_cached_snapshot": true})
	resp, err := s.srv.opts.Backend.Do(ctx, Request{Method: "POST", Path: "/api/workspaces/load", Body: string(body), Discard: true})
	if err != nil {
		return fatalOn(fmt.Errorf("registering git repository %s: %w", repo, err))
	}
	if isOverloadBody(resp.Status, resp.Body) { // admission refusal: retryable, not a verdict
		return fatalOn(fmt.Errorf("registering git repository %s: %w", repo, budget.ErrOverloaded))
	}
	if resp.Status >= 400 {
		// the API's message names the rule that refused it (a non-admin token
		// registers only under the operator's XMUSTARD_REGISTER_ROOTS)
		return fmt.Errorf("git repository %s is not registered and the API refused to register it (%s: %s)", repo, statusText(resp.Status), clip(apiErrorText(resp.Body), 200))
	}
	return nil
}

// apiErrorText is the "error" message of an API error body, else the body itself.
func apiErrorText(body string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) == nil && strings.TrimSpace(e.Error) != "" {
		return e.Error
	}
	return body
}

// listWorkspaces returns the workspaces the caller's token may see, cached for the
// session; refresh re-reads them. fresh reports that this call read them.
func (s *Session) listWorkspaces(ctx context.Context, refresh bool) (list []registeredWorkspace, fresh bool, err error) {
	s.mu.Lock()
	cached := s.listing
	s.mu.Unlock()
	if cached != nil && !refresh {
		return cached, false, nil
	}
	resp, err := s.srv.opts.Backend.Do(ctx, Request{Method: "GET", Path: "/api/workspaces"})
	if err != nil {
		return nil, false, fatalOn(fmt.Errorf("listing workspaces: %w", err))
	}
	if isOverloadBody(resp.Status, resp.Body) {
		return nil, false, fatalOn(fmt.Errorf("listing workspaces: %w", budget.ErrOverloaded))
	}
	if resp.Status != http.StatusOK {
		return nil, false, fmt.Errorf("listing workspaces failed (%s: %s)", statusText(resp.Status), clip(resp.Body, 200))
	}
	if err := json.Unmarshal([]byte(resp.Body), &list); err != nil {
		return nil, false, fmt.Errorf("listing workspaces: %w", err)
	}
	if list == nil {
		list = []registeredWorkspace{}
	}
	s.mu.Lock()
	s.listing, s.listedAt = list, time.Now()
	s.mu.Unlock()
	return list, true, nil
}

// fatalOn marks errors that must stop resolution rather than fall through to the next
// signal: admission refusal and cancellation.
func fatalOn(err error) error {
	if isFatal(err) || errors.Is(err, budget.ErrOverloaded) {
		return fatalError{err}
	}
	return err
}

// rootFor returns the registered root of workspace id, so a workspace named by
// argument or XMUSTARD_WORKSPACE_ID is echoed with its root like any other. It reads
// the listing when the session has none, or when id is missing from one older than
// listingMissRefresh. Best effort: a failed read leaves the root unknown, and the
// call itself reports whether the workspace exists.
func (s *Session) rootFor(ctx context.Context, id string) string {
	s.mu.Lock()
	list, at := s.listing, s.listedAt
	s.mu.Unlock()
	if root := rootOf(list, id); root != "" {
		return root
	}
	if list != nil && time.Since(at) < listingMissRefresh {
		return ""
	}
	if list, _, err := s.listWorkspaces(ctx, true); err == nil {
		return rootOf(list, id)
	}
	return ""
}

func rootOf(list []registeredWorkspace, id string) string {
	for _, w := range list {
		if w.ID == id {
			return w.Root
		}
	}
	return ""
}

// clientRoots returns the client's filesystem roots (roots/list), or why there are
// none. unknown reports that the request failed, so the client's roots are unknown
// rather than absent. Definitive answers are cached until roots/list_changed; a
// failure is asked again on a later call (after rootsRetryAfter when it timed out).
func (s *Session) clientRoots(ctx context.Context) (paths []string, why string, unknown bool) {
	s.mu.Lock()
	supported, ready, cached, gen := s.rootsCap, s.initialized, s.roots, s.rootsGen
	s.mu.Unlock()
	switch {
	case s.client == nil || !supported:
		return nil, "the client offers no roots", false
	case !ready:
		return nil, "the client has not finished initialization, so its roots were not requested", false
	case cached != nil && (!cached.transient || time.Now().Before(cached.retryAt)):
		return cached.paths, cached.err, cached.transient
	}
	rctx, cancel := context.WithTimeout(ctx, rootsTimeout)
	defer cancel()
	st := &rootsState{}
	raw, err := s.client.Request(rctx, "roots/list", map[string]any{})
	var rpcErr *RPCError
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err().Error(), true
	case err != nil && errors.As(err, &rpcErr) && rpcErr.Code == CodeMethodNotFound:
		st.err = "the client does not implement roots/list"
	case err != nil:
		st.err, st.transient = "roots/list failed: "+err.Error(), true
		if errors.Is(err, context.DeadlineExceeded) {
			st.retryAt = time.Now().Add(rootsRetryAfter)
			st.err += fmt.Sprintf(" (asked again after %s)", rootsRetryAfter)
		}
	default:
		var r struct {
			Roots []struct {
				URI string `json:"uri"`
			} `json:"roots"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			st.err = "roots/list answer is malformed: " + err.Error()
		}
		for _, root := range r.Roots {
			if u, err := url.Parse(root.URI); err == nil && u.Scheme == "file" && filepath.IsAbs(u.Path) {
				st.paths = append(st.paths, filepath.Clean(u.Path))
			}
		}
		if len(st.paths) == 0 && st.err == "" {
			st.err = "the client listed no file:// roots"
		}
	}
	s.mu.Lock()
	// An answer overtaken by roots/list_changed (or a new initialize) describes roots
	// the client has since replaced: use it for this call, but do not keep it.
	if s.rootsGen == gen && (!st.transient || !st.retryAt.IsZero()) {
		s.roots = st
	}
	s.mu.Unlock()
	return st.paths, st.err, st.transient
}

// relativizePath rewrites an absolute path argument inside the workspace root to the
// repo-relative form the API expects, and reports the rewrite.
func (s *Session) relativizePath(t *Tool, args map[string]string, ws Workspace) (Normalization, bool) {
	p := args[t.PathArg]
	if t.PathArg == "" || ws.Root == "" || !filepath.IsAbs(p) {
		return Normalization{}, false
	}
	rel, ok := within(canonicalPath(ws.Root), canonicalPath(p))
	if !ok {
		return Normalization{}, false
	}
	args[t.PathArg] = rel
	return Normalization{Argument: t.PathArg, Kind: "relative_path", From: p, To: rel}, true
}

// match returns the registered workspace whose root is the longest one containing p.
func match(list []registeredWorkspace, p string) (registeredWorkspace, bool) {
	best, found := registeredWorkspace{}, false
	for _, w := range list {
		if w.Root == "" {
			continue
		}
		root := canonicalPath(w.Root)
		if _, ok := within(root, p); ok && (!found || len(root) > len(canonicalPath(best.Root))) {
			best, found = w, true
		}
	}
	return best, found
}

// within reports p relative to root when p is root or below it.
func within(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func containing(roots []string, p string) string {
	if p == "" {
		return ""
	}
	p = canonicalPath(p)
	best := ""
	for _, r := range roots {
		if _, ok := within(canonicalPath(r), p); ok && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// canonicalPath cleans p and resolves symlinks where it exists (macOS /tmp is
// /private/tmp), so a registered root and a client path compare equal.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// repoRoot walks up from p to the nearest directory holding .git (a directory, or a
// file for worktrees and submodules). It stats only; no git process is spawned.
func repoRoot(p string) string {
	dir := p
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for i := 0; i < 128; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// WorkspaceIDForPath is the API's deterministic id for a newly registered root
// (workspaceops.workspaceIDForPath, duplicated so the shim does not link the
// workspace operations package; a test keeps the two identical).
func WorkspaceIDForPath(rootPath string) string {
	normalized := filepath.Clean(rootPath)
	digest := sha1.Sum([]byte(normalized))
	stem := strings.ToLower(filepath.Base(normalized))
	stem = strings.ReplaceAll(stem, " ", "-")
	stem = strings.ReplaceAll(stem, "_", "-")
	var b strings.Builder
	for _, ch := range stem {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			b.WriteRune(ch)
		}
	}
	value := strings.Trim(b.String(), "-")
	if value == "" {
		value = "workspace"
	}
	return value + "-" + hex.EncodeToString(digest[:])[:10]
}

func describe(list []registeredWorkspace, n int) string {
	sorted := append([]registeredWorkspace(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var parts []string
	for i, w := range sorted {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(sorted)-n))
			break
		}
		parts = append(parts, w.ID+" ("+w.Root+")")
	}
	return strings.Join(parts, ", ")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
