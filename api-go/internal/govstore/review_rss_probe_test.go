//go:build govstore_rss

package govstore

// RSS probe for review findings at 50k rows (WS-66; requirements §7: "WS-66 carries an
// RSS test at 50k findings rows"). Run it with the store's probe:
//
//	go test -tags govstore_rss -run TestReviewRSSProbe -v ./internal/govstore/
//
// A helper process writes 100 review records of 500 findings each (10 lineages, 200
// files) through RecordReview, so every finding runs the dedupe lookup against a
// growing store. A second helper reopens the file and serves what a review surface
// does: a record with its 500 findings, a page of a lineage, a new record of 50 findings
// (the per-call quota) at a head that already holds findings, so the dedupe finds exact
// and possible duplicates, and ten triage verdicts. The store keeps no review cache: the
// rows live in SQLite, so what the reads leave resident is the page cache the store
// already caps. The budget line is the REV-06 figure, 0.5 MiB steady: RSS after the
// review work, settled, over the open store. The transient above the open store is
// reported.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"
)

const (
	reviewProbeRecords  = 100
	reviewProbePerRec   = 500
	reviewProbeLineages = 10
	reviewProbeFiles    = 200
	// maxReviewSteady is the REV-06 budget line: what serving review findings leaves
	// resident over the open store.
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
			Side: "new", AnchorStatus: "exact_new", Category: "bug", Severity: "medium",
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

// probeReviewReopen serves review reads and one more record from the 50k-row file.
func probeReviewReopen(ctx context.Context, s *SQLStore, sample func()) error {
	timed := func(name string, fn func() error) error {
		start := time.Now()
		err := fn()
		probeMillis[name] = float64(time.Since(start).Microseconds()) / 1000
		sample()
		return err
	}
	var recordID string
	var findings []ReviewFinding
	steps := []struct {
		name string
		fn   func() error
	}{
		{"record_with_500_findings", func() error {
			fs, err := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb05", Limit: 1})
			if err != nil || len(fs) != 1 {
				return fmt.Errorf("first finding of a lineage: %d, %v", len(fs), err)
			}
			recordID = fs[0].RecordID
			if _, err := s.GetReviewRecord(ctx, "ws1", recordID); err != nil {
				return err
			}
			findings, err = s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", RecordID: recordID})
			if err == nil && len(findings) != reviewProbePerRec {
				err = fmt.Errorf("record holds %d findings", len(findings))
			}
			return err
		}},
		{"lineage_page_500", func() error {
			fs, err := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb05", AfterSeq: 1000, Limit: 500})
			if err == nil && len(fs) != 500 {
				err = fmt.Errorf("lineage page: %d findings", len(fs))
			}
			return err
		}},
		{"record_50_findings_with_dedupe", func() error {
			in := reviewProbeInput(5, reviewProbeCall)
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
	var steady []int64
	for range 5 {
		rep := runProbe(t, "rss_review_reopen", dir)
		steady = append(steady, rep.SettledRSS-rep.OpenRSS)
		t.Logf("reopen: settled %+.2f MiB, peak %+.2f MiB over the open store; ms %v", float64(rep.SettledRSS-rep.OpenRSS)/mib,
			float64(rep.OverOpen)/mib, rep.Millis)
	}
	slices.Sort(steady)
	m := steady[len(steady)/2]
	t.Logf("review serving leaves %.2f MiB resident over the open store (median of 5; budget %.1f MiB)", float64(m)/mib,
		float64(maxReviewSteady)/mib)
	if m > maxReviewSteady {
		t.Errorf("review serving left %.2f MiB resident over the open store (median), budget %.1f MiB", float64(m)/mib,
			float64(maxReviewSteady)/mib)
	}
}
