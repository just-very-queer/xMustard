package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xmustard/api-go/internal/rustcore"
)

// RepoIdentity is a repository identity observation used to label captured evidence
// current, stale or unknown. Contract of the Rust `repo-key` command:
//
//	xmustard-core repo-key <root> -> {"key","head","parser_version","identity_complete","limitations":[],"ignored_dirs":[]}
//
// Complete is true only when the key covers the exact working state (HEAD, dirty and
// untracked content). Anything else — a failed command, bounded/oversized reads, the
// fingerprint fallback — is incomplete and can never make evidence look current.
// ignored_dirs (the directories git ignores as a whole) is kept out of RepoIdentity:
// the identity cache holds it to fingerprint the tree.
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

func unavailableIdentity(reason, detail string) RepoIdentity {
	return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: reason, Detail: detail}}}
}

// WorkspaceRepoIdentity returns the identity of a workspace's repository (through
// the identity cache) and its canonical root (the evidence trust scope).
func WorkspaceRepoIdentity(ctx context.Context, dataDir, workspaceID string) (RepoIdentity, string) {
	root := WorkspaceRepoScope(dataDir, workspaceID)
	if root == "" {
		return unavailableIdentity("workspace_root_unavailable", ""), ""
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

// identitySample is one repo-key answer: the identity and the directories git
// ignores as a whole (nil when not reported).
type identitySample struct {
	id  RepoIdentity
	ign *ignoreSet
}

// repoIdentity asks the Rust core for the repository identity (one `repo-key` run;
// callers go through the identity cache). There is no fallback: if `repo-key` fails
// or answers something undecodable, the identity is unavailable and incomplete, so
// evidence freshness is "unknown" — never inferred from a weaker key.
func repoIdentity(ctx context.Context, root string) identitySample {
	out, err := rustcore.RunRepoKey(ctx, root)
	if err != nil {
		return identitySample{id: unavailableIdentity("repo_key_unavailable", err.Error())}
	}
	var wire struct {
		RepoIdentity
		IgnoredDirs *[]string `json:"ignored_dirs"`
	}
	if json.Unmarshal(out, &wire) != nil || wire.Key == "" {
		return identitySample{id: unavailableIdentity("repo_key_undecodable", "")}
	}
	id := wire.RepoIdentity
	id.Source = "repo-key"
	if len(id.Limitations) > 0 {
		id.Complete = false // the contract: any limitation makes the identity incomplete
	}
	s := identitySample{id: id}
	if wire.IgnoredDirs != nil {
		s.ign = newIgnoreSet(*wire.IgnoredDirs)
	}
	return s
}

// ignoreSet is repo-key's listing of the directories git ignores as a whole (top-
// level relative, no trailing slash), held as one sorted NUL-separated string with
// offsets: 4 bytes per directory beyond the path bytes.
type ignoreSet struct {
	joined string
	offs   []uint32
	digest string
}

// maxIgnoreSetBytes bounds one retained listing (repo-key caps it at 256 KiB of paths).
const maxIgnoreSetBytes = 320 << 10

// newIgnoreSet builds a listing, or nil when it is too large to retain.
func newIgnoreSet(dirs []string) *ignoreSet {
	clean := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d = strings.TrimSuffix(d, "/"); d != "" {
			clean = append(clean, d)
		}
	}
	slices.Sort(clean)
	clean = slices.Compact(clean)
	var b strings.Builder
	offs := make([]uint32, len(clean))
	h := sha256.New()
	for i, d := range clean {
		offs[i] = uint32(b.Len())
		b.WriteString(d)
		b.WriteByte(0)
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	s := &ignoreSet{joined: b.String(), offs: offs, digest: hex.EncodeToString(h.Sum(nil))}
	if s.residentBytes() > maxIgnoreSetBytes {
		return nil
	}
	return s
}

func (s *ignoreSet) len() int { return len(s.offs) }

func (s *ignoreSet) at(i int) string {
	end := len(s.joined)
	if i+1 < len(s.offs) {
		end = int(s.offs[i+1])
	}
	return s.joined[s.offs[i] : end-1]
}

// has reports whether rel (top-level relative, no trailing slash) is listed.
func (s *ignoreSet) has(rel string) bool {
	i := sort.Search(len(s.offs), func(i int) bool { return s.at(i) >= rel })
	return i < len(s.offs) && s.at(i) == rel
}

// subsetOf reports whether every directory s lists is listed by o.
func (s *ignoreSet) subsetOf(o *ignoreSet) bool {
	if s.digest == o.digest {
		return true
	}
	for i := range s.offs {
		if !o.has(s.at(i)) {
			return false
		}
	}
	return true
}

func (s *ignoreSet) residentBytes() int {
	if s == nil {
		return 0
	}
	return len(s.joined) + 4*len(s.offs) + len(s.digest) + 64
}

// repoFingerprint is one reading of the spawn-free working-tree fingerprint
// (repo_stat_unix.go). ok is false when it could not be computed; git is false when
// no Git worktree contains the root (the digest then only proves that is still so).
type repoFingerprint struct {
	digest string
	ok     bool
	git    bool
	// newestNs is the newest mtime/ctime among the stat keys digested; startedAt is
	// when the walk began (identity clock). Together they drive the racy rule.
	newestNs  int64
	startedAt time.Time
	// epoch is the identity-cache epoch of the root when the walk began.
	epoch uint64
}

// same reports whether two readings describe the same state.
func (f repoFingerprint) same(o repoFingerprint) bool {
	return f.ok && o.ok && f.git == o.git && f.digest == o.digest
}

// quietBefore is Git's racy rule: every stat key in the fingerprint is older than ref
// by fingerprintRacyWindow, so any change at or after ref would have produced a
// different stat key. Only then can the fingerprint vouch for the state from ref on.
func (f repoFingerprint) quietBefore(ref time.Time) bool {
	return f.ok && f.newestNs < ref.Add(-fingerprintRacyWindow).UnixNano()
}

// settled: the fingerprint vouches for every change after its own walk began.
func (f repoFingerprint) settled() bool { return f.quietBefore(f.startedAt) }

// Repository identity cache (PAR-FRESH-02, PAR-RT-12). A `repo-key` run spawns the
// Rust core and three git processes (0.3-0.5 s on a 37k-file repository). The cache
// keeps the last identity per canonical root, paired with a settled fingerprint of
// the tree taken around the sample, and reuses it without a spawn while a new
// fingerprint of the tree is identical and the sample is younger than the TTL.
//
// Pairing an identity with a fingerprint is sound only when the fingerprint's stat
// keys are all older than the earlier of the sample's and the walk's start by the racy
// window, the walk skipped only directories the sample itself reports ignored, and
// nothing invalidated the root since either began. A request's own before and after
// observations use the same rule: the after-identity equals the before-identity
// without a spawn when a walk after execution matches the settled walk that
// validated the before-identity, or, for a freshly sampled one, is quiet since that
// sample began (a file removed meanwhile moves its directory's stat key, so the tree
// is not quiet). That check has no TTL: it depends on the fingerprint, not on how
// long the handler ran.
//
// The fingerprint is not used (every observation samples, as without the cache)
// when: the TTL is 0; the platform cannot fingerprint; the root's repo-key reports
// no ignored-directory listing; a walk failed or exceeded its bounds (retried after
// fingerprintRetryAfter); or the last walk was not clearly cheaper than the last
// repo-key run (walk > repo-key/2, re-measured after fingerprintRetryAfter).
// Concurrent callers share one in-flight sample and one in-flight walk per root.
// Watcher and edit events can call InvalidateRepoIdentity.

const (
	defaultIdentityTTL    = 5 * time.Second
	maxIdentityTTL        = time.Minute
	identityCacheMaxRoots = 32
	// maxRetainedIgnoreBytes bounds the ignored-directory listings cache entries hold.
	maxRetainedIgnoreBytes = 512 << 10
	fingerprintRetryAfter  = time.Minute
)

// fingerprintRacyWindow covers the coarsest common timestamp granularity (FAT's 2 s).
const fingerprintRacyWindow = 2 * time.Second

var (
	// fingerprintCostJudged: the cache bypasses a fingerprint that is not clearly
	// cheaper than repo-key (tests with an instant fake sampler turn it off).
	fingerprintCostJudged = true
	// fingerprintWalks counts Git fingerprint walks (tests).
	fingerprintWalks atomic.Int64
)

// IdentityObservation says how an identity was obtained.
type IdentityObservation struct {
	// Cached is true when the identity came from the cache without a repo-key run.
	Cached bool
	// Age is how long ago the identity was sampled (0 for a sample this call ran).
	Age time.Duration
}

type identityEntry struct {
	id        RepoIdentity
	fp        repoFingerprint // settled
	ign       *ignoreSet      // the listing fp was walked with
	sampledAt time.Time
	lastUsed  time.Time
}

type sampleFlight struct {
	done  chan struct{}
	start time.Time
	epoch uint64
	res   identitySample
}

type walkFlight struct {
	done  chan struct{}
	start time.Time
	ign   *ignoreSet
	fp    repoFingerprint
}

// rootState is everything the cache keeps for one canonical root.
type rootState struct {
	entry      *identityEntry
	epoch      uint64 // bumped by invalidation: nothing begun earlier is paired or reused
	sample     *sampleFlight
	walk       *walkFlight
	walkNs     int64 // last walk duration (0 = not measured)
	keyNs      int64 // repo-key duration, smoothed
	fpOffUntil time.Time
	lastUsed   time.Time
}

type identityCacheT struct {
	mu    sync.Mutex
	roots map[string]*rootState
}

var (
	identityCache = &identityCacheT{roots: map[string]*rootState{}}
	// identityNow is the cache clock (tests shift it).
	identityNow = time.Now
	// sampleRepoIdentity is the sampler (tests replace it).
	sampleRepoIdentity = repoIdentity
	// identityWaitHook runs when a caller starts waiting on another's sample (tests).
	identityWaitHook func()
)

// identityTTL is XMUSTARD_IDENTITY_CACHE_MS (0 disables the cache and the
// fingerprint entirely), default 5 s, max 60 s.
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
// the next read samples; samples and walks already running are not paired with it.
func InvalidateRepoIdentity(root string) {
	c := identityCache
	c.mu.Lock()
	defer c.mu.Unlock()
	for r, st := range c.roots {
		if root == "" || r == root {
			st.epoch++
			st.entry = nil
		}
	}
}

// state returns root's state, creating it (and evicting the least recently used
// idle root beyond the bound). Caller holds mu.
func (c *identityCacheT) state(root string) *rootState {
	st := c.roots[root]
	if st == nil {
		if len(c.roots) >= identityCacheMaxRoots {
			var oldest string
			var at time.Time
			for r, s := range c.roots {
				if s.sample == nil && s.walk == nil && (oldest == "" || s.lastUsed.Before(at)) {
					oldest, at = r, s.lastUsed
				}
			}
			delete(c.roots, oldest)
		}
		st = &rootState{}
		c.roots[root] = st
	}
	st.lastUsed = identityNow()
	return st
}

// CurrentRepoIdentity returns the identity of the repository at the canonical root:
// from the cache when a fingerprint of the tree matches the cached pairing inside
// the TTL, else from one repo-key run, which it then pairs with a fingerprint so the
// next call (an evidence page, say) can reuse it.
func CurrentRepoIdentity(ctx context.Context, root string) (RepoIdentity, IdentityObservation) {
	b := identityCache.observe(ctx, root, true)
	return b.id, b.obs
}

// identityBasis is an observed identity and what it was validated against, so a
// later observation in the same request can tell whether anything moved.
type identityBasis struct {
	root string
	id   RepoIdentity
	obs  IdentityObservation
	ign  *ignoreSet
	// fp is the settled walk that validated (or was paired with) the identity; zero
	// for a fresh sample with no walk yet.
	fp        repoFingerprint
	sampledAt time.Time
	epoch     uint64
}

type sampleResult struct {
	identitySample
	at     time.Time
	epoch  uint64
	joined bool
}

func (s sampleResult) basis(root string) identityBasis {
	b := identityBasis{root: root, id: s.id, ign: s.ign, sampledAt: s.at, epoch: s.epoch}
	if s.joined {
		b.obs.Age = identityNow().Sub(s.at)
	}
	return b
}

func (b identityBasis) sample() sampleResult {
	return sampleResult{identitySample: identitySample{id: b.id, ign: b.ign}, at: b.sampledAt, epoch: b.epoch}
}

// observe returns root's identity. populate pairs a fresh sample with a walk right
// away (for callers that read again soon); a request's before-identity defers that
// walk to its after-check, which only reduced results need.
func (c *identityCacheT) observe(ctx context.Context, root string, populate bool) identityBasis {
	if root == "" {
		return identityBasis{id: unavailableIdentity("workspace_root_unavailable", "")}
	}
	start := identityNow()
	if !c.fingerprintOn(root, start) {
		return c.sampleCoalesced(ctx, root, time.Time{}).basis(root)
	}
	c.mu.Lock()
	e := c.state(root).entry
	if e != nil && start.Sub(e.sampledAt) >= identityTTL() {
		e = nil
	}
	c.mu.Unlock()
	if e != nil {
		fp := c.walkCoalesced(root, e.ign, start)
		if fp.same(e.fp) && fp.settled() && c.epochIs(root, fp.epoch) {
			c.mu.Lock()
			e.lastUsed = identityNow()
			c.mu.Unlock()
			return identityBasis{root: root, id: e.id, obs: IdentityObservation{Cached: true, Age: identityNow().Sub(e.sampledAt)},
				ign: e.ign, fp: fp, sampledAt: e.sampledAt, epoch: fp.epoch}
		}
		s := c.sampleCoalesced(ctx, root, time.Time{})
		if c.store(root, s, fp, e.ign) {
			b := s.basis(root)
			b.fp, b.ign = fp, e.ign
			return b
		}
		c.dropEntry(root, e)
		if populate {
			return c.populate(root, s)
		}
		return s.basis(root)
	}
	s := c.sampleCoalesced(ctx, root, time.Time{})
	if populate {
		return c.populate(root, s)
	}
	return s.basis(root)
}

// populate pairs a fresh sample with a walk begun after it.
func (c *identityCacheT) populate(root string, s sampleResult) identityBasis {
	b := s.basis(root)
	if s.id.Source != "repo-key" || !c.fingerprintOn(root, identityNow()) {
		return b
	}
	fp := c.walkCoalesced(root, s.ign, s.at)
	if c.store(root, s, fp, s.ign) {
		b.fp = fp
	}
	return b
}

// after observes the identity again after the request's handler ran (the capture-
// time check). It costs one walk and no spawn when the tree provably did not move
// since b was observed; otherwise it samples once, beginning after the handler.
func (c *identityCacheT) after(ctx context.Context, b identityBasis) (RepoIdentity, IdentityObservation) {
	start := identityNow()
	if b.id.Source != "repo-key" || !c.fingerprintOn(b.root, start) {
		return c.fresh(ctx, b.root, start, repoFingerprint{}, nil)
	}
	fp := c.walkCoalesced(b.root, b.ign, start)
	var unchanged bool
	if b.fp.ok {
		unchanged = fp.same(b.fp) // b.fp is settled: any later change alters the digest
	} else {
		unchanged = fp.quietBefore(b.sampledAt) && (fp.git || !b.id.Complete)
		if unchanged {
			c.store(b.root, b.sample(), fp, b.ign)
		}
	}
	if unchanged && c.epochIs(b.root, b.epoch) {
		return b.id, IdentityObservation{Cached: true, Age: identityNow().Sub(b.sampledAt)}
	}
	return c.fresh(ctx, b.root, start, fp, b.ign)
}

// fresh samples once (a sample begun at or after notBefore) and pairs it with walked,
// a walk taken just before, when that is sound.
func (c *identityCacheT) fresh(ctx context.Context, root string, notBefore time.Time, walked repoFingerprint, walkIgn *ignoreSet) (RepoIdentity, IdentityObservation) {
	s := c.sampleCoalesced(ctx, root, notBefore)
	if walked.ok {
		c.store(root, s, walked, walkIgn)
	}
	return s.id, s.basis(root).obs
}

// store caches the pairing of sample s with fingerprint fp (walked skipping
// walkIgn) when it is sound (see the rules above). It reports whether the pairing is
// sound; a sound one is cached unless a newer pairing already is.
func (c *identityCacheT) store(root string, s sampleResult, fp repoFingerprint, walkIgn *ignoreSet) bool {
	if !fp.ok || s.id.Source != "repo-key" || identityTTL() == 0 {
		return false
	}
	if fp.git {
		if walkIgn == nil || s.ign == nil || !walkIgn.subsetOf(s.ign) {
			return false
		}
	} else if s.id.Complete {
		return false // outside Git the fingerprint cannot see content
	}
	ref := s.at
	if fp.startedAt.Before(ref) {
		ref = fp.startedAt
	}
	if !fp.quietBefore(ref) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state(root)
	if st.epoch != s.epoch || st.epoch != fp.epoch {
		return false
	}
	if st.entry != nil && st.entry.sampledAt.After(s.at) {
		return true // a newer pairing is already cached
	}
	st.entry = &identityEntry{id: s.id, fp: fp, ign: walkIgn, sampledAt: s.at, lastUsed: identityNow()}
	c.boundRetained(root)
	return true
}

// boundRetained evicts the least recently used entries of other roots until the
// retained ignored-directory listings fit maxRetainedIgnoreBytes. Caller holds mu.
func (c *identityCacheT) boundRetained(keep string) {
	for {
		total := 0
		var victim *rootState
		for r, st := range c.roots {
			if st.entry == nil {
				continue
			}
			total += st.entry.ign.residentBytes()
			if r != keep && (victim == nil || st.entry.lastUsed.Before(victim.entry.lastUsed)) {
				victim = st
			}
		}
		if total <= maxRetainedIgnoreBytes || victim == nil {
			return
		}
		victim.entry = nil
	}
}

func (c *identityCacheT) dropEntry(root string, e *identityEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.state(root); st.entry == e {
		st.entry = nil
	}
}

func (c *identityCacheT) epochIs(root string, epoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state(root).epoch == epoch
}

// fingerprintOn reports whether observations of root may use the fingerprint.
func (c *identityCacheT) fingerprintOn(root string, now time.Time) bool {
	if !fingerprintSupported || identityTTL() == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !now.Before(c.state(root).fpOffUntil)
}

// judge turns the fingerprint off for a while when the last walk was not clearly
// cheaper than repo-key (it replaces a spawn only if it costs less than half of
// one). Caller holds mu.
func (st *rootState) judge(now time.Time) {
	if fingerprintCostJudged && st.walkNs > 0 && st.keyNs > 0 && 2*st.walkNs > st.keyNs {
		st.fpOffUntil = now.Add(fingerprintRetryAfter)
		st.walkNs = 0 // the next probe measures afresh
	}
}

// sampleCoalesced returns a repo-key sample begun at or after notBefore, joining one
// in flight when it qualifies (and waiting out one that does not).
func (c *identityCacheT) sampleCoalesced(ctx context.Context, root string, notBefore time.Time) sampleResult {
	for {
		c.mu.Lock()
		st := c.state(root)
		f := st.sample
		if f == nil {
			f = &sampleFlight{done: make(chan struct{}), start: identityNow(), epoch: st.epoch}
			st.sample = f
			c.mu.Unlock()
			c.runSample(ctx, root, f)
			return sampleResult{identitySample: f.res, at: f.start, epoch: f.epoch}
		}
		c.mu.Unlock()
		if identityWaitHook != nil {
			identityWaitHook()
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return sampleResult{identitySample: identitySample{id: unavailableIdentity("repo_key_unavailable", ctx.Err().Error())}, at: identityNow()}
		}
		if !f.start.Before(notBefore) {
			return sampleResult{identitySample: f.res, at: f.start, epoch: f.epoch, joined: true}
		}
	}
}

// runSample runs repo-key for flight f and settles it, even if the sampler panics.
func (c *identityCacheT) runSample(ctx context.Context, root string, f *sampleFlight) {
	f.res = identitySample{id: unavailableIdentity("repo_key_unavailable", "")}
	began := time.Now()
	defer func() {
		took := time.Since(began).Nanoseconds()
		c.mu.Lock()
		st := c.state(root)
		if st.sample == f {
			st.sample = nil
		}
		if f.res.id.Source == "repo-key" {
			if st.keyNs == 0 {
				st.keyNs = took
			} else {
				st.keyNs = (st.keyNs + took) / 2
			}
			st.judge(identityNow())
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.res = sampleRepoIdentity(ctx, root)
}

// walkCoalesced returns a fingerprint of root walked with ign and begun at or after
// notBefore, joining a qualifying walk in flight. One walk per root runs at a time.
func (c *identityCacheT) walkCoalesced(root string, ign *ignoreSet, notBefore time.Time) repoFingerprint {
	for waits := 0; ; waits++ {
		c.mu.Lock()
		st := c.state(root)
		f := st.walk
		if f == nil || waits >= 2 {
			own := &walkFlight{done: make(chan struct{}), start: identityNow(), ign: ign}
			registered := f == nil
			if registered {
				st.walk = own
			}
			epoch := st.epoch
			c.mu.Unlock()
			c.runWalk(root, own, epoch, registered)
			return own.fp
		}
		c.mu.Unlock()
		<-f.done
		if f.ign == ign && !f.start.Before(notBefore) {
			return f.fp
		}
	}
}

// runWalk computes the fingerprint for flight f, records its cost and settles it.
func (c *identityCacheT) runWalk(root string, f *walkFlight, epoch uint64, registered bool) {
	began := time.Now()
	defer func() {
		took := time.Since(began).Nanoseconds()
		c.mu.Lock()
		st := c.state(root)
		if registered && st.walk == f {
			st.walk = nil
		}
		switch {
		case !f.fp.ok:
			st.fpOffUntil = identityNow().Add(fingerprintRetryAfter) // unavailable here: retry later
		case f.fp.git:
			st.walkNs = took
			st.judge(identityNow())
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.fp = repoStatFingerprint(root, f.ign)
	f.fp.epoch = epoch
}

// canonicalRoot resolves symlinks in a workspace root (the root itself when that
// fails): the evidence trust scope and the identity cache key.
func canonicalRoot(root string) string {
	if canon, err := filepath.EvalSymlinks(root); err == nil {
		return canon
	}
	return root
}
