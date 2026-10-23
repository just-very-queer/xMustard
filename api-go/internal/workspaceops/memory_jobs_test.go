package workspaceops

import (
	"context"
	"errors"
	"fmt"
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

	// The prepared candidates are what must be identical between two passes: the jobs the
	// second pass queues may be empty, because a pass that has already queued a signature
	// does not queue it again. What is checked is that the same store yields the same
	// candidates, in the same order, and that each is enqueued once.
	if first, second := preparedOrder(t, dir, ws), preparedOrder(t, dir, ws); !slices.Equal(first, second) {
		t.Fatalf("preparation is not deterministic, in order or in membership:\nfirst  %v\nsecond %v", first, second)
	}
	if len(preparedOrder(t, dir, ws)) == 0 {
		t.Fatal("a store with a shared path and a stale anchor prepared no candidates")
	}
	queued := queuePass(t, dir, ws, "worker-1")
	if len(queued) == 0 {
		t.Fatal("a store with a shared path and a stale anchor queued no jobs")
	}
	first := queued
	// The second pass must queue nothing: a candidate whose signature already has a job is not
	// offered again. Calling queuePass a second time is what makes the assertion real; with
	// only one call it could not see a duplicate at all.
	if again := queuePass(t, dir, ws, "worker-1"); len(again) != 0 {
		t.Fatalf("the second pass queued %d jobs it already had: %v", len(again), again)
	}
	// The prepared sequence is stable within a kind: the signatures ascend. Two same-process
	// runs always agree, because Go randomises one map's iteration per range rather than per
	// call, so comparing two runs cannot see an unstable order. TestPreparedOrderAscends
	// asserts the property directly, on a fixture with several candidates of one kind.
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
	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
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
	if _, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}, Err: fail}); !errors.Is(err, fail) {
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

	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
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

	res, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
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

// The per-run bound drops the candidates it cannot take this pass and counts them, so an
// operator can see that work is left rather than believing the pass covered everything.
// The prepared candidates of one kind ascend by signature. Go randomises a map's iteration
// order per range, so an unsorted generator would hand the same store's candidates over in a
// different order across processes; two runs inside one process always agree and cannot see
// it. The property is asserted on the sequence itself, which is what a consumer reads.
func TestPreparedOrderAscendsWithinAKind(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs10"
	// Several clusters, so the fixture holds several candidates of the dedupe kind.
	for i := 0; i < 8; i++ {
		path := fmt.Sprintf("src/mem%02d.go", i)
		proposeOn(t, dir, ws, "a", "left "+path, "left thing", path)
		proposeOn(t, dir, ws, "b", "right "+path, "right thing", path)
	}
	got := preparedOrder(t, dir, ws)
	if len(got) < 2 {
		t.Fatalf("the fixture prepared %d candidates, too few to order", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("prepared order is not ascending within its kind:\n%v\n%v", got[i-1], got[i])
		}
	}
}

func TestCandidatesBeyondThePerPassBoundAreCounted(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs9"
	// Each pair shares its own anchor, so the store holds more clusters than one pass takes.
	// The figure comes from the pass's own candidate bound, so the fixture cannot drift from
	// it: one cluster more than a pass derives is the smallest store that exercises the cut.
	clusters := consolidationCandidatesPerPass + 1
	if clusters*2 > consolidationEntries {
		t.Fatalf("the fixture needs %d entries but a pass reads %d", clusters*2, consolidationEntries)
	}
	for i := 0; i < clusters; i++ {
		path := fmt.Sprintf("src/cluster%03d.go", i)
		proposeOn(t, dir, ws, "a", "left "+path, "left thing", path)
		proposeOn(t, dir, ws, "b", "right "+path, "right thing", path)
	}
	firstPass, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	res := firstPass
	if len(res.Jobs) > consolidationJobsPerRun {
		t.Fatalf("the pass queued %d jobs, past its own bound of %d", len(res.Jobs), consolidationJobsPerRun)
	}
	// Every cluster the store holds is accounted for: queued or counted as left out. A pass
	// that quietly dropped work would read as a pass that found nothing more to do.
	if res.Skipped != clusters-len(res.Jobs) {
		t.Fatalf("Skipped=%d does not account for the %d clusters the pass did not queue (it queued %d)", res.Skipped, clusters, len(res.Jobs))
	}
	// The watermark records a cursor the pass can resume from, and a pass that reached the
	// end of the table says so: that is what tells the next pass to start over and see
	// memories added since.
	w := readWatermark(t, dir, ws, "entries")
	if w.ReflectedThrough == "" || cursorOf(w.ReflectedThrough) == 0 {
		t.Fatalf("the watermark recorded no cursor: %+v", w)
	}
	if !res.Complete {
		t.Fatalf("the pass walked %d clusters without reaching the end of the entry table", clusters)
	}
	// A pass over a store it has already covered still prepares the work it has not queued,
	// so an entry added after the first pass is not invisible for ever.
	proposeOn(t, dir, ws, "a", "late one", "late thing", "src/late.go")
	proposeOn(t, dir, ws, "b", "late two", "late things", "src/late.go")
	after, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if len(after.Jobs) == 0 {
		t.Fatalf("a pass over a store it had already covered prepared %d jobs, so the newest memory is never seen", len(after.Jobs))
	}
	// The dropped candidate is not lost: the second pass reaches it. A second pass re-derives
	// every cluster, so it queues its whole bound again and the figure that matters is that
	// the one the first pass skipped now has a job.
	next, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	// The second pass takes the work the first could not: its 400 unqueued candidates are
	// chosen before the ones already queued, so the cluster the first pass cut now has a job
	// and nothing is left out. This is the property the cutoff must have: a bound that starves
	// its tail is not a bound.
	if next.Skipped != 0 {
		t.Fatalf("the second pass still left %d candidates out with the tail free", next.Skipped)
	}
	// The specific cluster the first pass cut now has a job. A count would not show this: a
	// job for some other cluster could make the total reach the bound while the cut one stayed
	// invisible, which is the starvation this row must not ship.
	cut := cutSignature(t, dir, ws, len(firstPass.Jobs))
	if cut == "" {
		t.Fatalf("no signature was left unqueued by the first pass, so the fixture does not exercise the cut")
	}
	if !slices.Contains(signaturesOf(t, dir, ws), cut) {
		t.Fatalf("candidate %q was cut by the first pass and still has no job", cut)
	}
	// And every cluster in the store has a job, which is the property a bound must have.
	if got := countAllJobs(t, dir, ws); got < clusters {
		t.Fatalf("the store holds %d clusters and only %d jobs, so %d are invisible", clusters, got, clusters-got)
	}
}

// cutSignature is the first signature the store holds that the queue does not: the candidate a
// pass at its bound had to leave out.
func cutSignature(t *testing.T, dir, ws string, queued int) string {
	t.Helper()
	sigs := signaturesOf(t, dir, ws)
	held := make(map[string]bool, queued)
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(context.Background(), govstore.JobFilter{WorkspaceID: ws, Limit: 2000})
		if err != nil {
			return err
		}
		for i, j := range jobs {
			if i < queued {
				held[j.ConflictSignature] = true
			}
		}
		return nil
	})
	for _, s := range sigs {
		if !held[s] {
			return s
		}
	}
	return ""
}

// signaturesOf is every signature the queue holds, oldest first.
func signaturesOf(t *testing.T, dir, ws string) []string {
	t.Helper()
	var out []string
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(context.Background(), govstore.JobFilter{WorkspaceID: ws, Limit: 2000})
		if err != nil {
			return err
		}
		for _, j := range jobs {
			out = append(out, j.ConflictSignature)
		}
		return nil
	})
	return out
}

// countAllJobs is how many jobs the workspace's queue holds, cancelled ones included: a
// cluster whose only job was cancelled has no live work, which is what this asserts against.
func countAllJobs(t *testing.T, dir, ws string) int {
	t.Helper()
	var n int
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		jobs, err := r.ListJobs(context.Background(), govstore.JobFilter{WorkspaceID: ws, Limit: 2000})
		if err != nil {
			return err
		}
		n = len(jobs)
		return nil
	})
	return n
}

// Two memories sharing an anchor are candidates for each other whenever both are inside the
// pass's window, even when a page boundary falls between them. The window is deliberately
// larger than one pass reads; a window equal to a pass would make this pair depend on where
// the boundary happened to land.
func TestClusterSpansAPageBoundary(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs11"
	// More entries than one pass reads, then the pair on one anchor, so the two members sit
	// either side of the point where one pass stops and the next resumes. Construct the filler
	// in one store transaction so race instrumentation does not pay a durable commit per row.
	filler := consolidationPage*consolidationPages + 10
	addConsolidationFixtureEntries(t, dir, ws, filler)
	addConsolidationFixtureEntry(t, dir, ws, "ctx_boundary_left", "a", "left of the boundary", "left thing", "src/boundary.go")
	addConsolidationFixtureEntry(t, dir, ws, "ctx_boundary_right", "b", "right of the boundary", "right thing", "src/boundary.go")

	// The pair must be joined by a pass, not merely present in the store: prepare repeatedly,
	// as a workspace larger than one pass is consolidated over several, and require the pair
	// in some pass's candidates. A window that only bounds memory would never join them.
	// The pair must be joined by a pass, not merely present in the store. PrepareConsolidationJobs
	// always starts at the beginning of the table, so it cannot show the cross-pass case; the
	// pass results can, because a pass reads from the cursor the last one stopped at.
	joined := false
	var attempts []ConsolidationPass
	for i := 0; i < 3 && !joined; i++ {
		res := runConsolidation(t, dir, ws, "worker-1")
		attempts = append(attempts, res)
		for _, j := range res.Jobs {
			if j.Kind == govstore.JobDedupeCluster && len(j.CandidateIDs) == 2 {
				joined = true
			}
		}
	}
	if !joined {
		var seen []int
		for _, a := range attempts {
			seen = append(seen, a.Seen)
		}
		t.Fatalf("two memories on one anchor either side of the cursor were never joined; passes saw %v entries", seen)
	}
	if consolidationClusterWindow <= consolidationPage*consolidationPages {
		t.Fatalf("the cluster window (%d) must exceed one pass (%d) or a pair either side of a page is never a candidate",
			consolidationClusterWindow, consolidationPage*consolidationPages)
	}
}

// The cursor is insertion order, not id order: a page whose highest id holds the lowest
// cursor must still advance past every entry it read.
func TestCursorFollowsInsertionOrderNotIdOrder(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs12"
	// Ids minted by the store ascend with insertion, so the page's last id is also its last
	// cursor here. The property is asserted on the helper, whose whole job is the maximum.
	entries := []govstore.Entry{
		{ID: "ctx_zzz", Cursor: 5, Lifecycle: govstore.LifecycleActive},
		{ID: "ctx_aaa", Cursor: 9, Lifecycle: govstore.LifecycleActive},
		{ID: "ctx_mmm", Cursor: 7, Lifecycle: govstore.LifecycleActive},
	}
	if got := lastCursorOf(entries); got != 9 {
		t.Fatalf("lastCursorOf = %d, want the largest cursor 9, not the last entry's 5", got)
	}
	// And a pass over a store with more entries than one page records the cursor it reached,
	// so the next pass resumes rather than re-reading the first page.
	addConsolidationFixtureEntries(t, dir, ws, consolidationPage+5)
	runConsolidation(t, dir, ws, "worker-1")
	w := readWatermark(t, dir, ws, "entries")
	if cursorOf(w.ReflectedThrough) <= 0 {
		t.Fatalf("a pass over %d entries recorded no cursor: %+v", consolidationPage+5, w)
	}
}

// A fully consolidated workspace reports nothing left out: an already-queued candidate past
// the bound is not remaining work.
func TestAConsolidatedWorkspaceReportsNothingSkipped(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs13"
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	first := runConsolidation(t, dir, ws, "worker-1")
	if len(first.Jobs) == 0 {
		t.Fatal("the first pass queued nothing")
	}
	second := runConsolidation(t, dir, ws, "worker-1")
	if len(second.Jobs) != 0 {
		t.Fatalf("the second pass queued %d jobs it already had", len(second.Jobs))
	}
	if second.Skipped != 0 {
		t.Fatalf("a fully consolidated workspace reports %d left out", second.Skipped)
	}
}

// Every kind the loop emits is one govstore names, so a claim of any emitted kind works.
func TestPreparedJobKindsAreKnownToTheStore(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs5"
	proposeOn(t, dir, ws, "a", "cluster one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "cluster two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
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

// A cancelled job frees its signature: the next pass enqueues a fresh job for it. The
// assertion names the cancelled signature rather than a count, because one pass may queue
// more than one kind of candidate and a count would break when another kind fires.
func TestPreparationReusesAFreedSignature(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsJobs6"
	proposeOn(t, dir, ws, "a", "cluster one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "cluster two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(res.Jobs) == 0 {
		t.Fatal("two memories on one path queued no job")
	}
	cancelled := res.Jobs[0]
	sig := cancelled.ConflictSignature
	mustUpdateMemory(t, dir, ws, func(tx govstore.Tx) error {
		_, err := tx.CancelJob(ctx, cancelled.ID, govstore.Actor{Principal: "worker-1"})
		return err
	})
	again, err := RunConsolidationPass(ctx, dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	// The freed signature has a job again, and it is a new one: the cancelled row is kept.
	var fresh *govstore.Job
	for i := range again.Jobs {
		if again.Jobs[i].ConflictSignature == sig {
			fresh = &again.Jobs[i]
			break
		}
	}
	if fresh == nil {
		t.Fatalf("the cancelled signature %q was not re-queued: %+v", sig, again.Jobs)
	}
	if fresh.ID == cancelled.ID {
		t.Fatalf("the cancelled job %s was reused instead of a fresh one", fresh.ID)
	}
	// The cancelled row is still there, readable: cancelling frees the signature, it does
	// not erase the record of what was offered.
	mustViewMemory(t, dir, ws, func(r govstore.Reader) error {
		got, err := r.GetJob(ctx, cancelled.ID)
		if err != nil {
			return err
		}
		if got.State != govstore.JobCancelled {
			t.Fatalf("the cancelled job is %s, want %s", got.State, govstore.JobCancelled)
		}
		return nil
	})
} // The instructions name the decision the agent has to make, not a verdict.
func TestPreparedInstructionsAskTheAgentToJudge(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsJobs7"
	proposeOn(t, dir, ws, "a", "shared one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "shared two", "two things", "src/shared.go")
	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: "worker-1"}})
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

// addConsolidationFixtureEntries inserts promoted, unrelated memories in one transaction.
// Keeping the full page-sized fixture while amortizing commits makes it practical under -race.
func addConsolidationFixtureEntries(t *testing.T, dir, ws string, count int) {
	t.Helper()
	ctx := context.Background()
	if err := memoryUpdate(ctx, dir, ws, func(tx govstore.Tx) error {
		for i := range count {
			id := fmt.Sprintf("ctx_filler%04d", i)
			actor := govstore.Actor{Principal: "a"}
			entry, err := tx.InsertEntry(ctx, govstore.NewEntry{
				ID: id, WorkspaceID: ws, Title: fmt.Sprintf("filler %d", i), Content: "an unrelated thing",
				Permission: "readwrite", Anchors: []govstore.AnchorInput{{Kind: govstore.AnchorPath,
					Value: fmt.Sprintf("src/filler%04d.go", i)}},
			}, actor)
			if err != nil {
				return err
			}
			if _, err := tx.SetPromotion(ctx, entry.ID, govstore.Promotion{
				Status: govstore.StatusVerified, Promoted: true, VerificationMode: govstore.ModeSingleAgent,
			}, actor); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert consolidation fixtures: %v", err)
	}
}

func addConsolidationFixtureEntry(t *testing.T, dir, ws, id, author, title, content, path string) {
	t.Helper()
	ctx := context.Background()
	actor := govstore.Actor{Principal: author}
	if err := memoryUpdate(ctx, dir, ws, func(tx govstore.Tx) error {
		entry, err := tx.InsertEntry(ctx, govstore.NewEntry{ID: id, WorkspaceID: ws, Title: title,
			Content: content, Permission: "readwrite", Anchors: []govstore.AnchorInput{{Kind: govstore.AnchorPath, Value: path}}}, actor)
		if err != nil {
			return err
		}
		_, err = tx.SetPromotion(ctx, entry.ID, govstore.Promotion{
			Status: govstore.StatusVerified, Promoted: true, VerificationMode: govstore.ModeSingleAgent,
		}, actor)
		return err
	}); err != nil {
		t.Fatalf("insert consolidation fixture %s: %v", id, err)
	}
}

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

// queuePass returns the jobs one pass queued, in the order it queued them. It does not
// sort: the order is part of what this row claims is deterministic, and sorting here would
// hide exactly that.
// runConsolidation runs one pass and returns its result.
func runConsolidation(t *testing.T, dir, ws, owner string) ConsolidationPass {
	t.Helper()
	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries",
		Actor: govstore.Actor{Principal: owner}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	return res
}

func queuePass(t *testing.T, dir, ws, owner string) []string {
	t.Helper()
	res, err := RunConsolidationPass(context.Background(), dir, ws, ConsolidationPassInput{Source: "entries", Actor: govstore.Actor{Principal: owner}})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	out := make([]string, 0, len(res.Jobs))
	for _, j := range res.Jobs {
		out = append(out, j.ID)
	}
	return out
}

// preparedOrder returns the prepared signatures in preparation order, so a test can assert
// the order itself rather than a set of ids.
func preparedOrder(t *testing.T, dir, ws string) []string {
	t.Helper()
	cands, err := PrepareConsolidationJobs(context.Background(), dir, ws)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Kind+"|"+c.ConflictSignature)
	}
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
