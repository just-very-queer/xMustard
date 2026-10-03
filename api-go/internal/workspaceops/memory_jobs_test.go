package workspaceops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// A preparation pass is idempotent: the same store yields the same candidates in the
// same order, and running it twice enqueues each once.
func TestPrepareConsolidationJobsIsIdempotentAndOrdered(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs1"
	// Two promoted memories on one shared path anchor: a dedupe cluster. One memory
	// whose code anchor went stale: a stale anchor.
	proposeOn(t, dir, ws, "a", "parser caches tokens", "the parser caches tokenized results", "src/parser.go")
	proposeOn(t, dir, ws, "b", "parser caches tokens too", "the parser caches tokenized results", "src/parser.go")
	lease := proposeOn(t, dir, ws, "a", "worker lease", "the worker renews its lease", "src/worker.go")
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		// Mark the worker's anchor stale, as a drift check would.
		_, err := tx.MarkStale(ctx, lease.ID, []govstore.AnchorRef{{Kind: govstore.AnchorPath, Value: "src/worker.go"}},
			"deadbeef", govstore.Actor{Principal: "indexer"})
		return err
	})

	first := prepareJobs(t, dir, ws, "worker-1")
	second := prepareJobs(t, dir, ws, "worker-1")
	if len(first) == 0 {
		t.Fatal("a store with a shared path and a stale anchor prepared no jobs")
	}
	if !slices.Equal(first, second) {
		t.Fatalf("preparation is not deterministic:\nfirst  %v\nsecond %v", first, second)
	}
	// Every candidate is enqueued exactly once across the two passes.
	for _, id := range first {
		if n := countJobsWithSignature(t, dir, ws, signatureOfJob(t, dir, ws, id)); n != 1 {
			t.Fatalf("job %s enqueued %d times, want 1", id, n)
		}
	}
}

// The conflict signature identifies the work: it is stable across passes and it
// distinguishes two clusters that differ in any one candidate id.
func TestConsolidationConflictSignaturesAreStableAndDistinct(t *testing.T) {
	ids := []string{"mem_a", "mem_b", "mem_c"}

	// The three memories share one anchor value, so they are one dedupe cluster.
	snap := consolidationSnapshot{Entries: entriesByID(ids), Anchors: sharedAnchorSnapshot(ids, "src/shared.go")}
	clusters := dedupeClusterCandidates(snap)
	if len(clusters) != 1 || len(clusters[0].Members) != 3 {
		t.Fatalf("three memories on one path are one cluster, got %v", clusters)
	}
	full := clusters[0].Members
	sig := clusterSignature(govstore.JobDedupeCluster, full)
	if sig != clusterSignature(govstore.JobDedupeCluster, slices.Clone(full)) {
		t.Fatal("signature depends on the slice, not its contents")
	}
	// Dropping any one candidate changes the signature.
	for i := range full {
		short := slices.Delete(slices.Clone(full), i, i+1)
		if clusterSignature(govstore.JobDedupeCluster, short) == sig {
			t.Fatalf("signature %q survives dropping candidate %s", sig, full[i])
		}
	}
	// Order inside the signature does not change it.
	reversed := slices.Clone(full)
	slices.Reverse(reversed)
	if clusterSignature(govstore.JobDedupeCluster, reversed) != sig {
		t.Fatal("signature depends on candidate order")
	}
}

// The watermark advances only on success. A failed pass leaves the reflected figures
// and the version untouched; only last_started_at moves.
func TestWatermarkAdvancesOnlyOnSuccess(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs3"
	// Two memories on one anchor, so the pass has a reflected count of its own to record.
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("successful pass: %v", err)
	}
	if res.Seen != 2 {
		t.Fatalf("the pass reflected over %d entries, want the 2 it stored", res.Seen)
	}
	w := readWatermark(t, dir, ws, "entries")
	if w.Version != 1 || w.ReflectedCount != int64(res.Seen) || w.LastSucceededAt == "" {
		t.Fatalf("a successful pass did not advance the watermark: %+v pass=%+v", w, res)
	}
	before := w

	// A failing pass must not move it.
	fail := errors.New("the store refused the read")
	if _, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}, Err: fail}); !errors.Is(err, fail) {
		t.Fatalf("failing pass returned %v, want %v", err, fail)
	}
	after := readWatermark(t, dir, ws, "entries")
	if after.Version != before.Version || after.ReflectedCount != before.ReflectedCount || after.ReflectedThrough != before.ReflectedThrough {
		t.Fatalf("a failed pass advanced the watermark: %+v -> %+v", before, after)
	}
	if after.LastSucceededAt != before.LastSucceededAt {
		t.Fatalf("a failed pass stamped last_succeeded_at: %q -> %q", before.LastSucceededAt, after.LastSucceededAt)
	}
	if after.LastStartedAt == "" {
		t.Fatal("a failed pass did not stamp last_started_at")
	}
}

// Preparation is non-destructive and advisory: it writes job and watermark rows only.
// No entry's lifecycle, promotion, votes, content or anchors change.
func TestPrepareConsolidationJobsChangesNoMemory(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs4"
	proposeOn(t, dir, ws, "a", "shared one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "shared two", "two things", "src/shared.go")
	middle := snapshotEntries(t, dir, ws)

	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(res.Jobs) == 0 {
		t.Fatal("two memories on one path prepared no job")
	}
	if got := snapshotEntries(t, dir, ws); !sameEntries(middle, got) {
		t.Fatalf("the pass changed the memory store:\nbefore %+v\nafter  %+v", middle, got)
	}
	// Every job is a proposal for the agent: a cluster names its candidates and the
	// store still holds both of them, neither merged nor superseded.
	for _, j := range res.Jobs {
		if len(j.CandidateIDs) < 2 {
			t.Fatalf("job %s names %d candidates, a cluster needs at least 2: %v", j.Kind, len(j.CandidateIDs), j.CandidateIDs)
		}
	}
}

// A retired entry is not a consolidation candidate. Two live memories on one anchor are a
// cluster; a third that was superseded, merged or retracted is not, so preparing work
// never re-asks a question the store already settled.
func TestRetiredEntriesAreNotConsolidationCandidates(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs8"
	a := proposeOn(t, dir, ws, "a", "live one", "one thing", "src/shared.go")
	b := proposeOn(t, dir, ws, "b", "live two", "two things", "src/shared.go")
	retired := proposeOn(t, dir, ws, "c", "retired three", "three things", "src/shared.go")
	// Retire the third entry through the store's own lifecycle, so the fixture is not a
	// hand-built row. Retraction is the transition an entry takes without a successor.
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		_, err := tx.Transition(ctx, retired.ID, govstore.TransitionInput{To: govstore.LifecycleRetracted, Reason: "superseded by practice"}, govstore.Actor{Principal: "human-approver"})
		return err
	})
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		got, err := r.GetEntry(ctx, retired.ID)
		if err != nil {
			return err
		}
		if got.Lifecycle != govstore.LifecycleRetracted {
			return errors.New("the fixture did not retire the entry, it is " + got.Lifecycle)
		}
		return nil
	})

	res, err := RunConsolidationPass(ctx, dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	// The two live memories are one cluster of exactly two: the retired one is absent from
	// every job's candidates, not merely last in the list.
	live := map[string]bool{a.ID: true, b.ID: true}
	clusters := 0
	for _, j := range res.Jobs {
		if j.Kind != govstore.JobDedupeCluster {
			continue
		}
		clusters++
		if len(j.CandidateIDs) != 2 {
			t.Fatalf("cluster names %d candidates, want the 2 live ones: %v", len(j.CandidateIDs), j.CandidateIDs)
		}
		for _, id := range j.CandidateIDs {
			if !live[id] {
				t.Fatalf("cluster names retired entry %s: %v", id, j.CandidateIDs)
			}
		}
	}
	if clusters != 1 {
		t.Fatalf("two live memories on one anchor prepared %d clusters, want 1", clusters)
	}
}

// Every kind the loop emits is one govstore names, so a claim of any emitted kind works.
func TestPreparedJobKindsAreKnownToTheStore(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs5"
	proposeOn(t, dir, ws, "a", "cluster one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "cluster two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	for _, j := range res.Jobs {
		// EnqueueJob is the store's own validator, and it accepted every prepared kind and
		// signature to create these rows: a kind outside jobKindSubjects or a malformed
		// signature would have failed the pass instead.
		if j.Kind == "" || j.ConflictSignature == "" {
			t.Fatalf("prepared job %s carries no kind or signature", j.ID)
		}
	}
	// Every prepared job is claimable by the agent that will do the reasoning.
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		for _, j := range res.Jobs {
			got, err := tx.GetJob(ctx, j.ID)
			if err != nil {
				return err
			}
			if got.Kind != j.Kind || got.State != govstore.JobQueued {
				return errors.New("job " + got.ID + " is " + got.State)
			}
		}
		return nil
	})
}

// A cancelled job frees its signature: preparing again enqueues a fresh job.
func TestPreparationReusesAFreedSignature(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs6"
	proposeOn(t, dir, ws, "a", "cluster one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "cluster two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(res.Jobs) != 1 {
		t.Fatalf("want one job, got %d", len(res.Jobs))
	}
	first := res.Jobs[0].ID
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		_, err := tx.CancelJob(ctx, first, govstore.Actor{Principal: "worker-1"})
		return err
	})
	again, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(again.Jobs) != 1 || again.Jobs[0].ID == first {
		t.Fatalf("a cancelled signature was not freed: %+v", again.Jobs)
	}
}

// The instructions name the decision the agent has to make, not a verdict.
func TestPreparedInstructionsAskTheAgentToJudge(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs7"
	proposeOn(t, dir, ws, "a", "shared one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "shared two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	j := res.Jobs[0]
	if strings.TrimSpace(j.Instructions) == "" {
		t.Fatal("a prepared job carries no instructions")
	}
	// The wording asks; it does not decide. A verdict word would make the job, not the
	// agent, the reasoner.
	for _, decided := range []string{"merged", "duplicate of", "superseded by", "approved"} {
		if strings.Contains(strings.ToLower(j.Instructions), decided) {
			t.Fatalf("instructions decide rather than ask: %q", j.Instructions)
		}
	}
}

// --- fixture helpers -------------------------------------------------------------

// proposeOn proposes a memory with a declared path anchor, so the fixture holds a real
// anchor row: an extracted path anchor only survives when the file exists in a repository
// root, which a bare temp data dir is not.
func proposeOn(t *testing.T, dir, ws, author, title, content, path string) *ContextEntry {
	t.Helper()
	return propose(t, dir, ws, author, title, content, func(p *ProposeContextRequest) { p.Paths = []string{path} })
}

// mustUpdateMemory runs one write transaction against the workspace's memory store.
func mustUpdateMemory(t *testing.T, dir, ws string, fn func(govstore.Tx) error) {
	t.Helper()
	if err := memoryUpdate(context.Background(), dir, ws, fn); err != nil {
		t.Fatalf("store update: %v", err)
	}
}

// mustViewMemory runs one read snapshot against the workspace's memory store.
func mustViewMemory(t *testing.T, dir, ws string, fn func(govstore.Reader) error) {
	t.Helper()
	if err := memoryView(context.Background(), dir, ws, fn); err != nil {
		t.Fatalf("store view: %v", err)
	}
}

func prepareJobs(t *testing.T, dir, ws, owner string) []string {
	t.Helper()
	res, err := RunConsolidationPass(context.Background(), dir, ws, passInput{Source: "entries", Actor: govstore.Actor{Principal: owner}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	out := make([]string, 0, len(res.Jobs))
	for _, j := range res.Jobs {
		out = append(out, j.ID)
	}
	slices.Sort(out)
	return out
}

func signatureOfJob(t *testing.T, dir, ws, id string) string {
	t.Helper()
	var sig string
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		j, err := tx.GetJob(context.Background(), id)
		sig = j.ConflictSignature
		return err
	})
	return sig
}

func countJobsWithSignature(t *testing.T, dir, ws, sig string) int {
	t.Helper()
	var n int
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(context.Background(), govstore.JobFilter{WorkspaceID: ws, Limit: 1000})
		if err != nil {
			return err
		}
		for _, j := range jobs {
			if j.ConflictSignature == sig {
				n++
			}
		}
		return nil
	})
	return n
}

func readWatermark(t *testing.T, dir, ws, source string) govstore.Watermark {
	t.Helper()
	var w govstore.Watermark
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		var err error
		w, err = r.GetWatermark(context.Background(), ws, source)
		return err
	})
	return w
}

// entrySnapshot is the promoted-visible shape of one entry, for the non-destructiveness
// assertion. Content and votes are named because a preparation pass must not touch them.
type entrySnapshot struct {
	ID         string
	Status     string
	Lifecycle  string
	Promoted   bool
	Revision   int64
	Digest     string
	Anchors    []string
	Votes      int
	Superseded string
}

func snapshotEntries(t *testing.T, dir, ws string) []entrySnapshot {
	t.Helper()
	var out []entrySnapshot
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		entries, err := r.ListEntries(context.Background(), govstore.EntryFilter{WorkspaceID: ws, Lifecycles: []string{"*"}, Limit: 1000})
		if err != nil {
			return err
		}
		for _, e := range entries {
			anchors, err := r.ListAnchors(context.Background(), e.ID)
			if err != nil {
				return err
			}
			refs := make([]string, 0, len(anchors))
			for _, a := range anchors {
				refs = append(refs, a.Kind+":"+a.Value)
			}
			slices.Sort(refs)
			votes, err := r.ListVotes(context.Background(), e.ID, 0)
			if err != nil {
				return err
			}
			out = append(out, entrySnapshot{ID: e.ID, Status: e.Status, Lifecycle: e.Lifecycle, Promoted: e.Promoted,
				Revision: e.Revision, Digest: e.ContentDigest, Anchors: refs, Votes: len(votes), Superseded: e.SupersededBy})
		}
		return nil
	})
	slices.SortFunc(out, func(a, b entrySnapshot) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func sameEntries(a, b []entrySnapshot) bool {
	return slices.EqualFunc(a, b, func(x, y entrySnapshot) bool {
		return x.ID == y.ID && x.Status == y.Status && x.Lifecycle == y.Lifecycle && x.Promoted == y.Promoted &&
			x.Revision == y.Revision && x.Digest == y.Digest && x.Votes == y.Votes &&
			x.Superseded == y.Superseded && slices.Equal(x.Anchors, y.Anchors)
	})
}

// entriesByID and sharedAnchorSnapshot build the generator's input without a store, so a
// signature test names a figure instead of reading it back from the code under test.
func entriesByID(ids []string) []govstore.Entry {
	out := make([]govstore.Entry, 0, len(ids))
	for _, id := range ids {
		out = append(out, govstore.Entry{ID: id, Lifecycle: govstore.LifecycleActive})
	}
	return out
}

func sharedAnchorSnapshot(ids []string, value string) map[string][]govstore.Anchor {
	out := map[string][]govstore.Anchor{}
	for _, id := range ids {
		out[id] = []govstore.Anchor{{EntryID: id, Kind: govstore.AnchorPath, Value: value}}
	}
	return out
}
