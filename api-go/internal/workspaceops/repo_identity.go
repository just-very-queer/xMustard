package workspaceops

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/rustcore"
)

// RepoIdentity is a repository identity observation used to label captured evidence
// current, stale or unknown. Contract of the Rust `repo-key` command:
//
//	xmustard-core repo-key <root> -> {"key","head","parser_version","identity_complete","limitations":[]}
//
// Complete is true only when the key covers the exact working state (HEAD, dirty and
// untracked content). Anything else — a failed command, bounded/oversized reads, the
// fingerprint fallback — is incomplete and can never make evidence look current.
type RepoIdentity struct {
	Key           string               `json:"key"`
	Head          string               `json:"head,omitempty"`
	ParserVersion string               `json:"parser_version,omitempty"`
	Complete      bool                 `json:"identity_complete"`
	Limitations   []IdentityLimitation `json:"limitations,omitempty"`
	RepoMode      string               `json:"repo_mode,omitempty"`
	Root          string               `json:"root,omitempty"`
	Source        string               `json:"source"` // repo-key | fingerprint-fallback | unavailable
}

// IdentityLimitation is the Rust wire type (rust-core indexcache.rs IdentityLimitation):
// why an identity is incomplete, e.g. reason oversized | unreadable | not_regular |
// identity_budget | git_unavailable | git_status_failed.
type IdentityLimitation struct {
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// String renders a limitation for delivery metadata.
func (l IdentityLimitation) String() string {
	s := l.Reason
	if l.Path != "" {
		s += " " + l.Path
	}
	if l.Detail != "" {
		s += ": " + l.Detail
	}
	return s
}

// WorkspaceRepoIdentity returns the identity of a workspace's repository (through
// the identity cache) and its canonical root (the evidence trust scope).
func WorkspaceRepoIdentity(ctx context.Context, dataDir, workspaceID string) (RepoIdentity, string) {
	root := WorkspaceRepoScope(dataDir, workspaceID)
	if root == "" {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "workspace_root_unavailable"}}}, ""
	}
	id, _ := CurrentRepoIdentity(ctx, root)
	return id, root
}

// WorkspaceRepoScope returns a workspace's canonical repository root (the evidence
// trust scope) from the workspace registry, without sampling its identity, or ""
// when unavailable.
func WorkspaceRepoScope(dataDir, workspaceID string) string {
	ws, err := resolveWorkspace(dataDir, workspaceID)
	if err != nil || ws.Root == "" {
		return ""
	}
	return ws.Scope
}

// repoIdentity asks the Rust core for the repository identity (one `repo-key` run;
// callers go through CurrentRepoIdentity). There is no fallback:
// if `repo-key` fails or answers something undecodable, the identity is unavailable
// and incomplete, so evidence freshness is "unknown" — never inferred from a weaker key.
func repoIdentity(ctx context.Context, root string) RepoIdentity {
	out, err := rustcore.RunRepoKey(ctx, root)
	if err != nil {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_unavailable", Detail: err.Error()}}}
	}
	var id RepoIdentity
	if json.Unmarshal(out, &id) != nil || id.Key == "" {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_undecodable"}}}
	}
	id.Source = "repo-key"
	if len(id.Limitations) > 0 {
		id.Complete = false // the contract: any limitation makes the identity incomplete
	}
	return id
}

// Repository identity cache (PAR-FRESH-02, PAR-RT-12). A `repo-key` run spawns the
// Rust core and three git processes (p50 ~52 ms; 139 MB peak measured for git status
// on a large repo). The cache keeps the last identity per canonical root and reuses
// it without a spawn while the spawn-free stat fingerprint (repo_stat_unix.go) is
// unchanged and the sample is younger than the TTL. Anything else re-samples:
//   - no fingerprint (non-unix, split/sparse index, submodules, bounds exceeded);
//   - a fingerprint that changed during the sample (a concurrent edit): the fresh
//     identity is returned but not cached;
//   - a complete identity outside a Git worktree (the "nogit" fingerprint cannot see
//     content), so only incomplete identities are reused there; they can never make
//     evidence current;
//   - a failed sample (unavailable), which is never cached.
// Concurrent callers for one root share a single in-flight sample. Watcher and edit
// events call InvalidateRepoIdentity.

const (
	defaultIdentityTTL  = 5 * time.Second
	maxIdentityTTL      = time.Minute
	identityCacheMaxLen = 64
)

// IdentityObservation says how an identity was obtained.
type IdentityObservation struct {
	// Cached is true when the identity came from the cache without a repo-key run.
	Cached bool
	// Age is how long ago the identity was sampled (0 for a fresh sample).
	Age time.Duration
}

type identityEntry struct {
	id        RepoIdentity
	fp        repoFingerprint
	sampledAt time.Time
	lastUsed  time.Time
}

type identityFlight struct {
	done   chan struct{}
	id     RepoIdentity
	fp     repoFingerprint
	at     time.Time
	stable bool
}

type identityCacheT struct {
	mu      sync.Mutex
	entries map[string]*identityEntry
	flights map[string]*identityFlight
	epoch   uint64 // bumped by invalidation; a sample started earlier is not stored
}

var (
	identityCache = &identityCacheT{entries: map[string]*identityEntry{}, flights: map[string]*identityFlight{}}
	// identityNow is the cache clock (tests).
	identityNow = time.Now
	// sampleRepoIdentity is the sampler (tests replace it).
	sampleRepoIdentity = repoIdentity
	// identityWaitHook runs when a caller starts waiting on another's sample (tests).
	identityWaitHook func()
)

// identityTTL is XMUSTARD_IDENTITY_CACHE_MS (0 disables reuse), default 5 s, max 60 s.
func identityTTL() time.Duration {
	v := strings.TrimSpace(os.Getenv("XMUSTARD_IDENTITY_CACHE_MS"))
	if v == "" {
		return defaultIdentityTTL
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 {
		return defaultIdentityTTL
	}
	if d := time.Duration(ms) * time.Millisecond; d < maxIdentityTTL {
		return d
	}
	return maxIdentityTTL
}

// InvalidateRepoIdentity drops the cached identity of root ("" drops every root), so
// the next read samples. For watcher and edit events.
func InvalidateRepoIdentity(root string) {
	c := identityCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	if root == "" {
		clear(c.entries)
		return
	}
	delete(c.entries, root)
}

// reusable reports whether e may answer for the fingerprint fp at now.
func (e *identityEntry) reusable(fp repoFingerprint, now time.Time, ttl time.Duration) bool {
	if !fp.ok || !e.fp.ok || fp != e.fp || now.Sub(e.sampledAt) >= ttl {
		return false
	}
	return fp.git || !e.id.Complete
}

// CurrentRepoIdentity returns the identity of the repository at the canonical root,
// from the cache when its fingerprint and TTL allow, else from one repo-key run.
func CurrentRepoIdentity(ctx context.Context, root string) (RepoIdentity, IdentityObservation) {
	c := identityCache
	ttl := identityTTL()
	fp := repoStatFingerprint(root)
	for {
		now := identityNow()
		c.mu.Lock()
		if e := c.entries[root]; e != nil && e.reusable(fp, now, ttl) {
			e.lastUsed = now
			id, age := e.id, now.Sub(e.sampledAt)
			c.mu.Unlock()
			return id, IdentityObservation{Cached: true, Age: age}
		}
		f := c.flights[root]
		if f == nil {
			f = &identityFlight{done: make(chan struct{})}
			c.flights[root] = f
			epoch := c.epoch
			c.mu.Unlock()
			return c.sample(ctx, root, fp, f, epoch, ttl)
		}
		c.mu.Unlock()
		if identityWaitHook != nil {
			identityWaitHook()
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_unavailable", Detail: ctx.Err().Error()}}}, IdentityObservation{}
		}
		// a stable shared sample taken while the state matched ours answers for us
		if f.stable && f.fp == fp {
			return f.id, IdentityObservation{Age: identityNow().Sub(f.at)}
		}
		fp = repoStatFingerprint(root)
	}
}

// sample runs repo-key once for the flight f and caches the result when the
// fingerprint held still across it and nothing invalidated the root meanwhile.
// Waiters share the result only under the same conditions.
func (c *identityCacheT) sample(ctx context.Context, root string, before repoFingerprint, f *identityFlight, epoch uint64, ttl time.Duration) (id RepoIdentity, obs IdentityObservation) {
	at := identityNow()
	id = RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_unavailable"}}}
	after := repoFingerprint{}
	defer func() { // settle the flight even if the sampler panics
		stable := before.ok && after == before && id.Source == "repo-key" && (after.git || !id.Complete)
		c.mu.Lock()
		stable = stable && c.epoch == epoch
		f.id, f.fp, f.at, f.stable = id, after, at, stable
		delete(c.flights, root)
		if stable && ttl > 0 {
			if len(c.entries) >= identityCacheMaxLen {
				c.evictOldest()
			}
			c.entries[root] = &identityEntry{id: id, fp: after, sampledAt: at, lastUsed: at}
		} else if !stable {
			delete(c.entries, root)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	id = sampleRepoIdentity(ctx, root)
	after = repoStatFingerprint(root)
	return id, IdentityObservation{}
}

// evictOldest drops the least recently used entry. Caller holds mu.
func (c *identityCacheT) evictOldest() {
	var oldest string
	var at time.Time
	for k, e := range c.entries {
		if oldest == "" || e.lastUsed.Before(at) {
			oldest, at = k, e.lastUsed
		}
	}
	delete(c.entries, oldest)
}
