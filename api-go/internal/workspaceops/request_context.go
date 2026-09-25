package workspaceops

import (
	"context"
	"sync"
)

// RequestContext is the per-request context kernel (PAR-FRESH-02): the workspace is
// resolved once from the registry and the repository identity is sampled at most
// once, then both are handed to the handler through context.Context. Write tools
// (remember, verify) never ask for the identity, so they never sample it.
type RequestContext struct {
	DataDir     string
	WorkspaceID string

	wsOnce sync.Once
	ws     ResolvedWorkspace
	wsErr  error

	idOnce sync.Once
	id     RepoIdentity
	idObs  IdentityObservation
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

// Identity samples the repository identity once per request (through the identity
// cache) and returns that same observation to every later caller in the request.
func (rc *RequestContext) Identity(ctx context.Context) (RepoIdentity, IdentityObservation) {
	rc.idOnce.Do(func() {
		scope := rc.Scope()
		if scope == "" {
			rc.id = RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "workspace_root_unavailable"}}}
			return
		}
		rc.id, rc.idObs = CurrentRepoIdentity(ctx, scope)
	})
	return rc.id, rc.idObs
}
