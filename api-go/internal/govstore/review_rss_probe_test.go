//go:build govstore_rss

package govstore

// RSS probe for review findings at 50k rows (WS-66; requirements §7: "WS-66 carries an
// RSS test at 50k findings rows").
//
// A helper process writes 100 review records of 500 findings each (10 lineages, 200
// files) through RecordReview, so every finding runs the dedupe lookup against a
// growing store. Later helpers reopen the file and serve what a review surface does, in
// rounds on different lineages (reviewProbeRound). The store keeps no review cache: the
// rows live in SQLite, so what the reads leave resident is the page caches the store
// already caps (the sqlite_governance line), the code the queries run and the runtime's
// own. That serving state is measured settled after 3 rounds and after 9 (medians of 5
// helpers each). The budget line is the REV-06 figure, 0.5 MiB steady, for the live
// heap (SQLite's C heap plus the Go heap) the extra rows served keep, which is asserted
// and needs the memory.counters tag:
//
//	go test -tags govstore_rss,memory.counters -run TestReviewRSSProbe -v ./internal/govstore/
//
// The RSS of the serving state, its growth over the extra rounds (split into anonymous,
// file and shared pages on Linux) and the transient peak are reported, not asserted:
// the RSS also counts pages the allocators touched and kept, outside the live heaps
// (the WS-66 implementation record has the measurements).

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"modernc.org/libc"
)

const (
	// The serving state is measured after a few rounds and after three times as many.
	reviewProbeFewRounds  = 3
	reviewProbeManyRounds = 9
	reviewProbeRecords    = 100
	reviewProbePerRec     = 500
	reviewProbeLineages   = 10
	reviewProbeFiles      = 200
	// maxReviewSteady is the REV-06 budget line: what serving review findings leaves
	// resident beyond the store's own capped page caches.
	maxReviewSteady = mib / 2
)

func init() {
	extraHelpers["rss_review_insert"] = func() error { return rssProbe(probeReviewInsert) }
	extraHelpers["rss_review_reopen"] = func() error { return rssProbe(probeReviewReopen) }
}

// reviewProbeInput is record i: its lineage, its head (one per record, plus the head the
// reopen probe records at again) and n findings spread over the files.
func reviewProbeInput(i, n int) ReviewRecordInput {
	files := make([]string, reviewProbeFiles)
	for j := range files {
		files[j] = fmt.Sprintf("pkg%d/file%03d.go", j%20, j)
	}
	in := ReviewRecordInput{WorkspaceID: "ws1", Lineage: fmt.Sprintf("main@mb%02d", i%reviewProbeLineages),
		Change: ReviewChange{Repository: "/repo", BaseRef: "main", MergeBase: fmt.Sprintf("mb%02d", i%reviewProbeLineages),
			Head: "h" + strconv.Itoa(i), DiffSHA256: fmt.Sprintf("%064d", i), DiffBytes: 1 << 20},
		ChangedFiles: files, Producer: "agent", Coverage: make([]ReviewCoverage, 0, len(files))}
	for _, f := range files {
		in.Coverage = append(in.Coverage, ReviewCoverage{Path: f, Status: CoverageReviewed})
	}
	for j := range n {
		start := (j/reviewProbeFiles)*10 + 1
		in.Findings = append(in.Findings, ReviewFindingInput{Path: files[j%reviewProbeFiles], StartLine: start, EndLine: start + 2,
			Side: "new", AnchorStatus: "exact_new", Category: "bug", Severity: "medium", Support: "supported",
			Checks:       ReviewChecks{CodePresent: "yes", InChangedHunk: "yes", InScope: "yes", SymbolResolved: "unknown"},
			Content:      fmt.Sprintf("finding %d of record %d: the error from Close is dropped on this path", j, i),
			ExistingCode: fmt.Sprintf("defer f.Close()\nx%d := read(f, %d)", j, i%7)})
	}
	return in
}

// probeReviewInsert writes the 50k findings, one record per transaction.
func probeReviewInsert(ctx context.Context, s *SQLStore, sample func()) error {
	start := time.Now()
	for i := range reviewProbeRecords {
		if err := s.Update(ctx, func(tx Tx) error {
			_, _, err := tx.RecordReview(ctx, reviewProbeInput(i, reviewProbePerRec), Actor{Principal: "rev-" + strconv.Itoa(i%3)})
			return err
		}); err != nil {
			return err
		}
		sample()
	}
	probeMillis["insert_per_record"] = float64(time.Since(start).Milliseconds()) / reviewProbeRecords
	return nil
}

// probeReviewReopen serves review reads and records from the 50k-row file in
// GOVSTORE_REVIEW_ROUNDS rounds, each on another lineage, and marks SQLite's C heap (0
// without the memory.counters tag) and the Go heap once settled.
func probeReviewReopen(ctx context.Context, s *SQLStore, sample func()) error {
	rounds, err := strconv.Atoi(os.Getenv("GOVSTORE_REVIEW_ROUNDS"))
	if err != nil || rounds < 1 || rounds > reviewProbeLineages {
		return fmt.Errorf("GOVSTORE_REVIEW_ROUNDS must be 1 to %d", reviewProbeLineages)
	}
	for r := range rounds {
		if err := reviewProbeRound(ctx, s, sample, r); err != nil {
			return fmt.Errorf("round %d: %w", r, err)
		}
	}
	settle()
	probeMarks["libc"], probeMarks["go_heap"] = int64(libc.MemStat().Bytes), int64(goHeapResident())
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	probeMarks["go_sys"] = int64(ms.Sys - ms.HeapReleased)
	if b, err := os.ReadFile("/proc/self/status"); err == nil { // Linux: split the resident set by kind
		for l := range strings.SplitSeq(string(b), "\n") {
			for _, k := range []string{"RssAnon", "RssFile", "RssShmem"} {
				if v, ok := strings.CutPrefix(l, k+":"); ok {
					kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
					probeMarks[k] = kb << 10
				}
			}
		}
	}
	return nil
}

// reviewProbeRound serves what a review surface does, on lineage 5+r: a record with its
// 500 findings, a page of the lineage, a new record of 50 findings (the per-call quota)
// at a head that already holds findings, so the dedupe finds exact and possible
// duplicates, and ten triage verdicts. The second round's timings are reported.
func reviewProbeRound(ctx context.Context, s *SQLStore, sample func(), r int) error {
	lineage := fmt.Sprintf("main@mb%02d", (5+r)%reviewProbeLineages)
	timed := func(name string, fn func() error) error {
		start := time.Now()
		err := fn()
		if r == 1 { // the first round after the store's caches warmed
			probeMillis[name] = float64(time.Since(start).Microseconds()) / 1000
		}
		sample()
		return err
	}
	var findings []ReviewFinding
	steps := []struct {
		name string
		fn   func() error
	}{
		{"record_with_500_findings", func() error {
			fs, err := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: lineage, Limit: 1})
			if err != nil || len(fs) != 1 {
				return fmt.Errorf("first finding of a lineage: %d, %v", len(fs), err)
			}
			if _, err := s.GetReviewRecord(ctx, "ws1", fs[0].RecordID); err != nil {
				return err
			}
			findings, err = s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", RecordID: fs[0].RecordID})
			if err == nil && len(findings) != reviewProbePerRec {
				err = fmt.Errorf("record holds %d findings", len(findings))
			}
			return err
		}},
		{"lineage_page_500", func() error {
			fs, err := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: lineage, AfterSeq: 1000, Limit: 500})
			if err == nil && len(fs) != 500 {
				err = fmt.Errorf("lineage page: %d findings", len(fs))
			}
			return err
		}},
		{"record_50_findings_with_dedupe", func() error {
			in := reviewProbeInput(5+r, reviewProbeCall)
			nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
			for j := range in.Findings {
				if j%2 == 1 { // half quote other code at the same place: possible duplicates
					in.Findings[j].ExistingCode = "other code " + nonce + strconv.Itoa(j)
				}
			}
			return s.Update(ctx, func(tx Tx) error {
				_, fs, err := tx.RecordReview(ctx, in, Actor{Principal: "rev-9"})
				if err == nil && (fs[0].Dedupe.Result != DedupeDuplicateOf || fs[1].Dedupe.Result != DedupePossibleDuplicate) {
					err = fmt.Errorf("dedupe at the probe head: %+v, %+v", fs[0].Dedupe, fs[1].Dedupe)
				}
				return err
			})
		}},
		{"triage_10", func() error {
			return s.Update(ctx, func(tx Tx) error {
				for _, f := range findings[:10] {
					if _, err := tx.TriageReviewFinding(ctx, ReviewTriageInput{WorkspaceID: "ws1", FindingID: f.ID, Verdict: "confirm"},
						Actor{Principal: "rev-9"}); err != nil {
						return err
					}
				}
				return nil
			})
		}},
	}
	for _, st := range steps {
		if err := timed(st.name, st.fn); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}
	return nil
}

// reviewProbeCall is the per-call quota the probe's new record uses (requirements §7: at
// most 50 findings per verify call).
const reviewProbeCall = 50

func TestReviewRSSProbe(t *testing.T) {
	dir := t.TempDir()
	insert := runProbe(t, "rss_review_insert", dir)
	t.Logf("50k findings: %.0f ms per 500-finding record; peak %.2f MiB over the open store; db %.1f MiB",
		insert.Millis["insert_per_record"], float64(insert.OverOpen)/mib, float64(insert.DBBytes)/mib)
	median := func(v []int64) int64 { slices.Sort(v); return v[len(v)/2] }
	type state struct{ rss, heaps int64 }
	serving := map[int]state{}
	for _, rounds := range []int{reviewProbeFewRounds, reviewProbeManyRounds} {
		var rss, heaps []int64
		for range 5 {
			rep := runProbe(t, "rss_review_reopen", dir, "GOVSTORE_REVIEW_ROUNDS="+strconv.Itoa(rounds))
			rss = append(rss, rep.SettledRSS-rep.OpenRSS)
			heaps = append(heaps, rep.Marks["libc"]+rep.Marks["go_heap"])
			t.Logf("reopen, %d rounds: settled %+.2f MiB over the open store (SQLite C heap %.2f MiB, Go heap %.2f MiB, Go runtime %.2f MiB; "+
				"anon %.2f, file %.2f, shmem %.2f MiB); peak %+.2f MiB; ms %v",
				rounds, float64(rep.SettledRSS-rep.OpenRSS)/mib, float64(rep.Marks["libc"])/mib, float64(rep.Marks["go_heap"])/mib,
				float64(rep.Marks["go_sys"])/mib, float64(rep.Marks["RssAnon"])/mib, float64(rep.Marks["RssFile"])/mib,
				float64(rep.Marks["RssShmem"])/mib, float64(rep.OverOpen)/mib, rep.Millis)
		}
		serving[rounds] = state{median(rss), median(heaps)}
	}
	few, many := serving[reviewProbeFewRounds], serving[reviewProbeManyRounds]
	t.Logf("review serving holds %.2f MiB over the open store after %d rounds and %.2f MiB after %d (medians of 5): %+.2f MiB of RSS "+
		"and %+.2f MiB of live SQLite and Go heap for three times the rows served (budget %.1f MiB)", float64(few.rss)/mib,
		reviewProbeFewRounds, float64(many.rss)/mib, reviewProbeManyRounds, float64(many.rss-few.rss)/mib,
		float64(many.heaps-few.heaps)/mib, float64(maxReviewSteady)/mib)
	if many.heaps == 0 {
		t.Skip("the SQLite C heap reads 0 without the memory.counters tag: run with -tags govstore_rss,memory.counters")
	}
	if added := many.heaps - few.heaps; added > maxReviewSteady {
		t.Errorf("serving three times the review rows kept %.2f MiB more live heap (medians), budget %.1f MiB", float64(added)/mib,
			float64(maxReviewSteady)/mib)
	}
}
