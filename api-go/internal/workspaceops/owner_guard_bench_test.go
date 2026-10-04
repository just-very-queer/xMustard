package workspaceops

import (
	"strconv"
	"testing"
)

// BenchmarkAwaitingMeOwnerGuard measures the owner-distinct path over pending queues of
// 100, 1,000 and 10,000, the shapes WS-FIX-22's blocker was measured on
// (decisions/ws-fix-22.md): every row reaches the guard, the worst case for a routine
// ground. The token policy is measured beside it because it is the floor the batched
// path approaches, not reaches — the token policy reads no owner at all.
//
// The queue must still list every pending row. Asserting q.Total == n here is what
// makes this benchmark also the row's no-cap guard: a future cap would fail the
// benchmark rather than quietly make it faster. It also asserts that every row is
// either listed or skipped for a named reason, so a batching change that altered which
// rows were listed, or why one was skipped, fails the benchmark instead of only
// changing its timings.
func BenchmarkAwaitingMeOwnerGuard(b *testing.B) {
	for _, policy := range []string{DistinctToken, DistinctOwner} {
		for _, n := range []int{100, 1000, 10000} {
			b.Run(policy+"/"+strconv.Itoa(n), func(b *testing.B) {
				dir := b.TempDir()
				ws := "wsGuardBench"
				require := true
				writeTestSettings(b, dir, appSettings{RequireMultiAgentVerification: &require,
					ContextVerificationThreshold: 2, PrincipalDistinctness: policy})
				h := humanApprover("bob", "bob")
				// the pending quota is 500 per author, so a large queue has many authors
				for i := 0; i < n; i++ {
					author := "author-" + strconv.Itoa(i/400)
					if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{
						Title: "t" + strconv.Itoa(i), Content: "the build uses make " + strconv.Itoa(i)}},
						ContextActor{ID: author, Owner: author}); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					q, err := HumanApprovalQueue(dir, ws, h, 0, 0)
					if err != nil {
						b.Fatal(err)
					}
					if q.Total == 0 {
						b.Fatalf("bob is shown none of the %d pending entries, so the guard never ran", n)
					}
					if q.Total != n {
						b.Fatalf("the queue listed %d of %d pending entries: a row was capped or withheld",
							q.Total, n)
					}
					// Every pending row is somebody else's, so each is either listed for approval
					// (q.Total, which is the eligible count and is not bounded by limit) or
					// skipped for a named reason, and the two must add up. Without this a
					// guard that changed which reason a row was skipped by, or that guarded
					// only part of a page, would still report Total == n. q.Items is NOT
					// the row count: it is the rendered page, bounded by limit.
					if got := q.Total + q.Skipped[skipOwn] + q.Skipped[skipVoted] +
						q.Skipped[skipSameOwner] + q.Skipped[skipNotVisible]; got != n {
						b.Fatalf("%d rows accounted for (%d listed, skipped %v), want %d: batching "+
							"changed which rows were listed or why they were skipped",
							got, q.Total, q.Skipped, n)
					}
				}
			})
		}
	}
}
