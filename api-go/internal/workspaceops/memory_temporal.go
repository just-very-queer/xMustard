package workspaceops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/gitdiff"
	"xmustard/api-go/internal/govstore"
)

// Bi-temporal recall (WS-28, PAR-PROV-02). A memory's world time is the interval from
// valid_from (set when it is promoted) to invalidated_at (set when it is superseded,
// merged, retracted or archived), each end with the commit HEAD was at. Intervals are
// closed, never deleted. recall(as_of=<commit|time>) keeps the memories whose interval
// holds that point, superseded and expired ones included, and valid_only keeps those
// whose interval is still open. A commit point is decided by ancestry, cached: a
// commit is immutable, so an answer never goes stale.

// gitTimeout bounds one git child of the temporal checks.
const gitTimeout = 10 * time.Second

// asOfPoint is a resolved as_of: a world time and, for a commit, the commit itself.
type asOfPoint struct {
	at     time.Time
	commit string
	root   string
	ctx    context.Context
}

// resolveAsOf reads an as_of argument: an RFC 3339 time, a date, or a commit of the
// workspace's repository (full or abbreviated id, or a ref).
func resolveAsOf(ctx context.Context, root, s string) (asOfPoint, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return asOfPoint{at: t.UTC(), ctx: ctx}, nil
		}
	}
	bad := fmt.Errorf("as_of %q is not an RFC 3339 time, a date or a commit of this repository: %w", s, ErrInvalidInput)
	if root == "" {
		return asOfPoint{}, bad
	}
	sha, err := gitdiff.ResolveCommit(ctx, root, s, gitTimeout)
	if err != nil {
		return asOfPoint{}, bad
	}
	var out bytes.Buffer
	if err := gitdiff.Run(ctx, root, &out, gitTimeout, "show", "-s", "--format=%cI", "--end-of-options", sha); err != nil {
		return asOfPoint{}, bad
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(out.String()))
	if err != nil {
		return asOfPoint{}, bad
	}
	return asOfPoint{at: at.UTC(), commit: sha, root: root, ctx: ctx}, nil
}

// reached reports whether the point is at or after an interval end recorded as time ts
// and commit: by ancestry when both the point and the record name a commit, else by
// time. An ancestry check that cannot run (an unknown commit) falls back to time.
func (p asOfPoint) reached(ts, commit string) bool {
	if p.commit != "" && commit != "" {
		if ancestor, err := isAncestor(p.ctx, p.root, commit, p.commit); err == nil {
			return ancestor
		}
	}
	t, ok := storeTime(ts)
	return ok && !t.After(p.at)
}

// holds reports whether the entry's world-time interval contains the point. An entry
// never promoted has no interval; an expiry ends it like an invalidation does.
func (p asOfPoint) holds(e govstore.RankEntry) bool {
	if e.ValidFrom == "" || !p.reached(e.ValidFrom, e.ValidFromCommit) {
		return false
	}
	for _, end := range []struct{ ts, commit string }{{e.InvalidatedAt, e.InvalidatedCommit}, {e.ExpiresAt, ""}} {
		if end.ts != "" && p.reached(end.ts, end.commit) {
			return false
		}
	}
	return true
}

// ancestryCache remembers whether one commit is an ancestor of another. It is bounded
// and reset whole when full.
var ancestryCache = struct {
	sync.Mutex
	known map[[3]string]bool
}{known: map[[3]string]bool{}}

const ancestryCacheMax = 4096

// isAncestor reports whether commit a is b or an ancestor of it, in root's repository.
func isAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	key := [3]string{root, a, b}
	ancestryCache.Lock()
	v, ok := ancestryCache.known[key]
	ancestryCache.Unlock()
	if ok {
		return v, nil
	}
	cmd := gitdiff.Command(ctx, root, "merge-base", "--is-ancestor", "--end-of-options", a, b)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	switch code := exitCode(err); {
	case err == nil:
		v = true
	case code == 1:
		v = false // a real answer: not an ancestor
	default:
		return false, fmt.Errorf("ancestry of %s and %s: %w", a, b, err)
	}
	ancestryCache.Lock()
	if len(ancestryCache.known) >= ancestryCacheMax {
		clear(ancestryCache.known)
	}
	ancestryCache.known[key] = v
	ancestryCache.Unlock()
	return v, nil
}

// exitCode is a git child's exit status, -1 when it did not run to an exit.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
