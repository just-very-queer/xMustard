package workspaceops

import (
	"context"
	"sync"
)

// RequestContext is the per-request context kernel (PAR-FRESH-02): the workspace is
// resolved once from the registry and the repository identity is observed once
// before the handler runs, then both are available through context.Context. Write
// tools (remember, verify) never ask for the identity, so they never sample it.
// Today the evidence middleware is the identity's only consumer; handlers take the
// resolved root, and the Rust core still computes its own source identity.
type RequestContext struct {
	DataDir     string
	WorkspaceID string

	wsOnce sync.Once
	ws     ResolvedWorkspace
	wsErr  error

	idOnce sync.Once
	basis  identityBasis
}

type requestContextKey struct{}

// NewRequestContext starts a request's context for one workspace. Nothing is
// resolved or sampled until asked.
func NewRequestContext(dataDir, workspaceID string) *RequestContext {
	return &RequestContext{DataDir: dataDir, WorkspaceID: workspaceID}
}

// WithRequestContext attaches rc to ctx.
func WithRequestContext(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, requestContextKey{}, rc)
}

// RequestContextFrom returns the request context attached to ctx, or nil.
func RequestContextFrom(ctx context.Context) *RequestContext {
	rc, _ := ctx.Value(requestContextKey{}).(*RequestContext)
	return rc
}

// Workspace resolves the workspace once per request.
func (rc *RequestContext) Workspace() (ResolvedWorkspace, error) {
	rc.wsOnce.Do(func() { rc.ws, rc.wsErr = resolveWorkspace(rc.DataDir, rc.WorkspaceID) })
	return rc.ws, rc.wsErr
}

// Scope is the canonical repository root (the evidence trust scope), or "".
func (rc *RequestContext) Scope() string {
	ws, err := rc.Workspace()
	if err != nil || ws.Root == "" {
		return ""
	}
	return ws.Scope
}

// Identity observes the repository identity once per request (through the identity
// cache) and returns that same observation to every later caller in the request.
func (rc *RequestContext) Identity(ctx context.Context) (RepoIdentity, IdentityObservation) {
	rc.idOnce.Do(func() { rc.basis = identityCache.observe(ctx, rc.Scope(), false) })
	return rc.basis.id, rc.basis.obs
}

// IdentityAfter observes the identity again after the handler ran, for binding
// captured evidence: without a spawn when a fingerprint shows the tree did not move
// since Identity, else from one repo-key run begun now. A workspace root that
// resolves to another directory than when the request began (a re-pointed symlink)
// has no identity, so nothing produced across that move can bind.
func (rc *RequestContext) IdentityAfter(ctx context.Context) (RepoIdentity, IdentityObservation) {
	rc.Identity(ctx)
	if rc.basis.root == "" {
		return rc.basis.id, IdentityObservation{}
	}
	if ws, err := rc.Workspace(); err != nil || canonicalRoot(ws.Root) != rc.basis.root {
		return unavailableIdentity("workspace_root_moved", "the workspace root resolves to another directory than when the request began"), IdentityObservation{}
	}
	return identityCache.after(ctx, rc.basis)
}
