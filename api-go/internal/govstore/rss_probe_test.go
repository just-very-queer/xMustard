//go:build govstore_rss

package govstore

// RSS probe for the governance store's budget line (PAR-STORE-01, §7.2: steady 3–5
// MiB, bulk import +12–14 MiB in the heavy slot). Run it with:
//
//	go test -tags govstore_rss,memory.counters -run TestRSSProbe -v ./internal/govstore/
//
// The memory.counters tag only turns on modernc's allocator counters, which split the
// growth into SQLite's C heap and the Go heap; the assertions do not depend on it.
//
// Every measurement runs in a fresh helper process, so the baseline is a clean test
// binary. RSS is the ps-RSS basis the budget gate uses: current resident set for the
// open deltas, max(getrusage peak, sampled RSS) for bulk peaks. On macOS an open
// delta is taken as the larger of the ps-RSS delta and vmmap's footprint plus
// __TEXT delta, because ps-RSS deltas read low when other pages are reclaimed under
// memory pressure between samples. Budgets asserted, as the WS-01 test line states
// them ("open adds ≤3 MiB; a 100k-row bulk insert inside a single scope stays
// ≤14 MiB above baseline"), with one baseline for both: the process before Open.
//   - Open() on an existing store, and Open() plus the first read, each add at most
//     3 MiB. Reads served one at a time borrow the idle writer connection, so the first
//     read opens no second connection. Creating a new file also runs the migration and
//     is reported only;
//   - a 100k-row bulk write inside one Update (InsertEntry, and the legacy importer)
//     peaks at most 14 MiB above the pre-open process. The peak above the open store
//     (§7.2's heavy-slot view) is reported too.
// Also measured on the restarted store, and reported rather than asserted because the
// WS-01 line does not set it: the serving state after three FTS searches that match
// most of the 100k rows, a list and a content fetch, with one reader connection opened
// by a read that overlapped a write. That is what a daemon under concurrent requests
// holds: both page caches at their caps, FTS5 working memory the allocator keeps, and
// the code of those queries. Compare it with §7.2's steady 3-5 MiB for the store.
// The legacy import fixture has the pre-w0 shape (no verification_mode), the worst
// case for the importer: every promoted entry is relabelled.
//
// Runs under /usr/bin/time -l should set GOVSTORE_NO_VMMAP=1: time reports the peak
// over waited-for children as well, and vmmap is larger than the probe.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"modernc.org/libc"
)

const (
	probeRows    = 100_000
	mib          = 1024 * 1024
	maxOpenDelta = 3 * mib
	// maxBulkDelta is measured from the process before Open (OverBase).
	maxBulkDelta = 14 * mib
)

func init() {
	extraHelpers["rss_insert"] = func() error { return rssProbe(probeInsert) }
	extraHelpers["rss_reopen"] = func() error { return rssProbe(probeReopen) }
	extraHelpers["rss_import"] = func() error { return rssProbe(probeImport) }
}

type rssReport struct {
	Mode      string `json:"mode"`
	BaseRSS   int64  `json:"base_rss"`
	OpenRSS   int64  `json:"open_rss"`
	OpenDelta int64  `json:"open_delta"` // Open plus a first read (opens a reader connection)
	// OpenOnlyDelta is Open alone: writer connection, pragmas, migration check.
	OpenOnlyDelta int64   `json:"open_only_delta"`
	PeakRSS       int64   `json:"peak_rss"`
	OverOpen      int64   `json:"peak_over_open"`
	OverBase      int64   `json:"peak_over_base"`
	AfterRSS      int64   `json:"after_close_rss"`
	Seconds       float64 `json:"seconds"`
	DBBytes       int64   `json:"db_bytes"`
	LibcBase      int64   `json:"libc_base"`
	LibcOpen      int64   `json:"libc_open"`
	LibcPeak      int64   `json:"libc_peak"`
	GoHeapBase    uint64  `json:"go_heap_base"`
	GoHeapPeak    uint64  `json:"go_heap_peak"`
	SampledPeak   int64   `json:"sampled_peak"`
	MaxRSS        int64   `json:"maxrss"`
	// macOS attribution from vmmap: private physical footprint versus the resident
	// __TEXT (clean, file-backed machine code) that ps-RSS also counts.
	FootprintBase     int64 `json:"footprint_base"`
	FootprintOpen     int64 `json:"footprint_open"`
	TextBase          int64 `json:"text_base"`
	TextOpen          int64 `json:"text_open"`
	FootprintOpenOnly int64 `json:"footprint_open_only"`
	TextOpenOnly      int64 `json:"text_open_only"`
	// Serving state (reopen mode): after the reads, plus one reader connection.
	ServingDelta     int64 `json:"serving_delta"`
	FootprintServing int64 `json:"footprint_serving"`
	TextServing      int64 `json:"text_serving"`
	ReaderConns      int   `json:"reader_conns"`
	// SettledRSS is the resident set after the run, once the Go heap is returned: what
	// the run left resident (review probe).
	SettledRSS int64 `json:"settled_rss"`
	// Timings a probe reports, in milliseconds, and settled RSS marks it takes (review
	// probe).
	Millis map[string]float64 `json:"millis,omitempty"`
	Marks  map[string]int64   `json:"marks,omitempty"`
}

// openCost is the conservative open delta: on macOS, ps-RSS deltas drop below what
// an operation really added when other pages are reclaimed under memory pressure
// between the samples, so the probe also sums what vmmap attributes to the step
// (private footprint plus newly resident machine code) and takes the larger.
func openCost(ps, fpBase, fpAfter, textBase, textAfter int64) int64 {
	if fpBase == 0 || fpAfter == 0 {
		return ps
	}
	return max(ps, (fpAfter-fpBase)+(textAfter-textBase))
}

// vmmapFootprint returns (physical footprint, resident __TEXT) in bytes on macOS, or
// zeros where vmmap is unavailable.
func vmmapFootprint() (int64, int64) {
	// GOVSTORE_NO_VMMAP=1 skips it, for runs under /usr/bin/time -l: that reports the
	// maximum over waited-for children too, and vmmap itself is ~38 MiB.
	if runtime.GOOS != "darwin" || os.Getenv("GOVSTORE_NO_VMMAP") == "1" {
		return 0, 0
	}
	out, err := exec.Command("vmmap", "-summary", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, 0
	}
	var footprint, text int64
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "Physical footprint:") && len(f) >= 3:
			footprint = parseSize(f[2])
		case strings.HasPrefix(line, "__TEXT ") && len(f) >= 3:
			text = parseSize(f[2]) // VIRTUAL, RESIDENT, ...
		}
	}
	return footprint, text
}

func parseSize(s string) int64 {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1024, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = mib, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1024*mib, strings.TrimSuffix(s, "G")
	}
	v, _ := strconv.ParseFloat(s, 64)
	return int64(v * float64(mult))
}

// currentRSS is this process's resident set in bytes (ps-RSS basis).
func currentRSS() int64 {
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 1 {
			pages, _ := strconv.ParseInt(f[1], 10, 64)
			return pages * int64(os.Getpagesize())
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	kib, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kib * 1024
}

// maxRSS is getrusage's peak resident set in bytes.
func maxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

func settle() int64 {
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(150 * time.Millisecond)
	return currentRSS()
}

func goHeapResident() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapSys - ms.HeapReleased
}

type probeFn func(ctx context.Context, s *SQLStore, sample func()) error

// probeMillis and probeMarks collect the timings and settled RSS marks a probe reports.
var (
	probeMillis = map[string]float64{}
	probeMarks  = map[string]int64{}
)

func rssProbe(run probeFn) error {
	ctx := context.Background()
	rep := rssReport{Mode: os.Getenv(helperEnv)}
	rep.BaseRSS = settle()
	rep.LibcBase = int64(libc.MemStat().Bytes)
	rep.GoHeapBase = goHeapResident()
	rep.FootprintBase, rep.TextBase = vmmapFootprint()

	s, err := Open(ctx, filepath.Join(os.Getenv("GOVSTORE_DIR"), "probe.db"), Options{QuickCheckOnOpen: os.Getenv("GOVSTORE_QC") == "1"})
	if err != nil {
		return err
	}
	rep.OpenOnlyDelta = settle() - rep.BaseRSS
	rep.FootprintOpenOnly, rep.TextOpenOnly = vmmapFootprint()
	// a first read, as the daemon's first request would do; it borrows the idle writer
	if _, err := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", ServedOnly: true, Limit: 8}); err != nil {
		return err
	}
	rep.OpenRSS = settle()
	rep.OpenDelta = rep.OpenRSS - rep.BaseRSS
	rep.LibcOpen = int64(libc.MemStat().Bytes)
	rep.FootprintOpen, rep.TextOpen = vmmapFootprint()

	sample := func() {
		rep.SampledPeak = max(rep.SampledPeak, currentRSS())
		rep.LibcPeak = max(rep.LibcPeak, int64(libc.MemStat().Bytes))
		rep.GoHeapPeak = max(rep.GoHeapPeak, goHeapResident())
	}
	start := time.Now()
	if err := run(ctx, s, sample); err != nil {
		return err
	}
	sample()
	rep.Seconds = time.Since(start).Seconds()
	rep.MaxRSS = maxRSS()
	rep.PeakRSS = max(rep.MaxRSS, rep.SampledPeak)
	rep.OverOpen = rep.PeakRSS - rep.OpenRSS
	rep.OverBase = rep.PeakRSS - rep.BaseRSS
	rep.SettledRSS = settle()
	rep.Millis, rep.Marks = probeMillis, probeMarks
	if rep.Mode == "rss_reopen" {
		if err := readDuringWrite(ctx, s); err != nil {
			return err
		}
		rep.ReaderConns = s.readers.Stats().OpenConnections
		rep.ServingDelta = settle() - rep.BaseRSS
		rep.FootprintServing, rep.TextServing = vmmapFootprint()
	}
	if info, err := s.SchemaInfo(ctx); err == nil {
		rep.DBBytes = info.PageCount * info.PageSize
	}
	_ = s.Close()
	rep.AfterRSS = settle()
	out, _ := json.Marshal(rep)
	fmt.Printf("report %s\n", out)
	return nil
}

// probeInsert writes probeRows proposals through InsertEntry in one Update: per row an
// entry, its revision, two path anchors, an FTS row and a propose event.
func probeInsert(ctx context.Context, s *SQLStore, sample func()) error {
	return s.Update(ctx, func(tx Tx) error {
		for i := range probeRows {
			if _, err := tx.InsertEntry(ctx, NewEntry{
				ID: fmt.Sprintf("bulk_%06d", i), WorkspaceID: "ws1",
				Title:   fmt.Sprintf("Convention %d for module %d", i, i%97),
				Content: fmt.Sprintf("Memory %d: run make check-backend before pushing changes to module %d; the retry policy for service %d uses capped exponential backoff with jitter.", i, i%97, i%13),
				Paths:   []string{fmt.Sprintf("api-go/internal/mod%d/file%d.go", i%97, i%31), "Makefile"},
			}, alice); err != nil {
				return err
			}
			if i%2000 == 0 {
				sample()
			}
		}
		sample()
		return nil
	})
}

// readDuringWrite runs a read while a write holds the writer connection, so the read
// opens a reader connection, as overlapping requests do in a daemon.
func readDuringWrite(ctx context.Context, s *SQLStore) error {
	holding, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Update(ctx, func(Tx) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	_, err := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "retry backoff", Limit: 8})
	close(release)
	if werr := <-done; err == nil {
		err = werr
	}
	return err
}

// probeReopen serves reads from an existing 100k-entry store: the daemon's restart.
func probeReopen(ctx context.Context, s *SQLStore, sample func()) error {
	for _, q := range []string{"retry backoff jitter", "make check-backend", "module 42"} {
		if _, err := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: q, Limit: 8}); err != nil {
			return err
		}
		sample()
	}
	page, err := s.ListEntries(ctx, EntryFilter{WorkspaceID: "ws1", Limit: 50})
	if err != nil || len(page) != 50 {
		return fmt.Errorf("reopen read: %d entries, %v", len(page), err)
	}
	ids := make([]string, len(page))
	for i, e := range page {
		ids[i] = e.ID
	}
	if _, err := s.EntryContents(ctx, ids); err != nil {
		return err
	}
	sample()
	return nil
}

// probeImport streams a probeRows-entry legacy context_entries.json through the
// importer, the WS-12 cutover path, in one transaction.
func probeImport(ctx context.Context, s *SQLStore, sample func()) error {
	f, err := os.Open(os.Getenv("GOVSTORE_IMPORT"))
	if err != nil {
		return err
	}
	defer f.Close()
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				sample()
			}
		}
	}()
	rep, err := s.ImportContextEntriesJSON(ctx, "ws1", f, ImportOptions{})
	close(done)
	if err != nil {
		return err
	}
	if rep.Imported != probeRows || rep.RelabelledCount != probeRows {
		return fmt.Errorf("imported %d, relabelled %d", rep.Imported, rep.RelabelledCount)
	}
	return nil
}

// writeLegacyFixture streams a large legacy file without holding it in memory. The
// entries have the pre-w0 shape: promoted, with no verification_mode, as every entry
// written before w0-kernel is stored on disk.
func writeLegacyFixture(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	_, _ = w.WriteString("[\n")
	for i := range probeRows {
		content := fmt.Sprintf("Memory %d: run make check-backend before pushing changes to module %d; the retry policy uses capped backoff.", i, i%97)
		path := fmt.Sprintf("api-go/internal/mod%d/file.go", i%97)
		b, _ := json.Marshal(LegacyEntry{
			ID: fmt.Sprintf("ctx_%010d", i), WorkspaceID: "ws1", Title: fmt.Sprintf("Convention %d", i), Content: content,
			Source: "agent-a", Permission: "readonly", Status: StatusVerified, Promoted: true,
			Verifications: []LegacyVerification{
				{Agent: "agent-b", Approve: true, At: "2026-09-20T08:16:00Z"},
				{Agent: "agent-c", Approve: true, At: "2026-09-20T08:17:00Z"},
			},
			RequiredVerifications: 2, CreatedAt: "2026-09-20T08:15:00Z", UpdatedAt: "2026-09-20T08:17:00Z",
			Paths: []string{path}, PathHashes: map[string]string{path: Digest(content)},
		})
		if i > 0 {
			_, _ = w.WriteString(",\n")
		}
		_, _ = w.Write(b)
	}
	_, _ = w.WriteString("\n]\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func mibs(v []int64) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = strconv.FormatFloat(float64(x)/mib, 'f', 2, 64)
	}
	return out
}

func runProbe(t *testing.T, mode, dir string, env ...string) rssReport {
	t.Helper()
	h := startHelper(t, mode, append([]string{"GOVSTORE_DIR=" + dir}, env...)...)
	line := h.waitFor(t, "report ", 15*time.Minute)
	var rep rssReport
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "report ")), &rep); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
	m := func(v int64) float64 { return float64(v) / mib }
	t.Logf("%s: base %.2f MiB | Open() +%.2f MiB | Open()+first read +%.2f MiB | peak %.2f MiB = +%.2f over open, +%.2f over base | after close %.2f MiB | %.1fs, db %.1f MiB",
		mode, m(rep.BaseRSS), m(rep.OpenOnlyDelta), m(rep.OpenDelta), m(rep.PeakRSS), m(rep.OverOpen), m(rep.OverBase), m(rep.AfterRSS), rep.Seconds, m(rep.DBBytes))
	if rep.FootprintBase > 0 {
		t.Logf("%s: vmmap split, Open(): private footprint +%.2f MiB, machine code (__TEXT, clean file-backed) +%.2f MiB; with first read: +%.2f and +%.2f MiB",
			mode, m(rep.FootprintOpenOnly-rep.FootprintBase), m(rep.TextOpenOnly-rep.TextBase),
			m(rep.FootprintOpen-rep.FootprintBase), m(rep.TextOpen-rep.TextBase))
		if rep.FootprintServing > 0 {
			t.Logf("%s: vmmap split, serving with %d reader connection(s): +%.2f and +%.2f MiB",
				mode, rep.ReaderConns, m(rep.FootprintServing-rep.FootprintBase), m(rep.TextServing-rep.TextBase))
		}
	}
	t.Logf("%s: SQLite C heap (libc) %.2f -> %.2f at open -> %.2f peak MiB | Go heap resident %.2f -> %.2f peak MiB | maxrss %.2f, sampled %.2f MiB",
		mode, m(rep.LibcBase), m(rep.LibcOpen), m(rep.LibcPeak), m(int64(rep.GoHeapBase)), m(int64(rep.GoHeapPeak)), m(rep.MaxRSS), m(rep.SampledPeak))
	return rep
}

func TestRSSProbe(t *testing.T) {
	dir := t.TempDir()
	insert := runProbe(t, "rss_insert", dir) // creates and migrates probe.db, then writes 100k rows
	t.Logf("create: opening a new file (with migration) added %.2f MiB", float64(insert.OpenDelta)/mib)
	if insert.OverBase > maxBulkDelta {
		t.Errorf("100k-row InsertEntry transaction peaked %.2f MiB above the pre-open process, budget %d MiB",
			float64(insert.OverBase)/mib, maxBulkDelta/mib)
	}
	// Reported, not asserted: the same restart with the opt-in open-time quick_check.
	qc := runProbe(t, "rss_reopen", dir, "GOVSTORE_QC=1")
	t.Logf("reopen with QuickCheckOnOpen: Open() added %.2f MiB (conservative %.2f), with the first read %.2f MiB (conservative %.2f)",
		float64(qc.OpenOnlyDelta)/mib, float64(openCost(qc.OpenOnlyDelta, qc.FootprintBase, qc.FootprintOpenOnly, qc.TextBase, qc.TextOpenOnly))/mib,
		float64(qc.OpenDelta)/mib, float64(openCost(qc.OpenDelta, qc.FootprintBase, qc.FootprintOpen, qc.TextBase, qc.TextOpen))/mib)
	// The same 100k-row file as a restarted daemon sees it. Single ps-RSS samples vary
	// by about 1 MiB between runs, so the budget applies to the median of five.
	var openOnly, withRead, serving []int64
	for range 5 {
		rep := runProbe(t, "rss_reopen", dir)
		if rep.ReaderConns != 1 {
			t.Errorf("the overlapping read left %d reader connections open, want 1", rep.ReaderConns)
		}
		openOnly = append(openOnly, openCost(rep.OpenOnlyDelta, rep.FootprintBase, rep.FootprintOpenOnly, rep.TextBase, rep.TextOpenOnly))
		withRead = append(withRead, openCost(rep.OpenDelta, rep.FootprintBase, rep.FootprintOpen, rep.TextBase, rep.TextOpen))
		serving = append(serving, openCost(rep.ServingDelta, rep.FootprintBase, rep.FootprintServing, rep.TextBase, rep.TextServing))
	}
	medianOf := func(v []int64) int64 { slices.Sort(v); return v[len(v)/2] }
	t.Logf("reopen, conservative cost (max of ps-RSS delta and vmmap footprint+__TEXT delta), Open() (MiB): %v; Open()+first read (MiB): %v; serving with a reader connection (MiB): %v",
		mibs(openOnly), mibs(withRead), mibs(serving))
	if m := medianOf(openOnly); m > maxOpenDelta {
		t.Errorf("Open() on the existing store added %.2f MiB (median), budget %d MiB", float64(m)/mib, maxOpenDelta/mib)
	}
	// Reported against the same line, and asserted: serving the first read is part of
	// having the store open.
	if m := medianOf(withRead); m > maxOpenDelta {
		t.Errorf("Open() plus the first read added %.2f MiB (median), budget %d MiB", float64(m)/mib, maxOpenDelta/mib)
	}
	t.Logf("serving with a reader connection: %.2f MiB (median) over the pre-open process; reported, §7.2 puts the store at 3-5 MiB steady",
		float64(medianOf(serving))/mib)
	src := filepath.Join(t.TempDir(), "context_entries.json")
	writeLegacyFixture(t, src)
	imp := runProbe(t, "rss_import", t.TempDir(), "GOVSTORE_IMPORT="+src)
	if imp.OverBase > maxBulkDelta {
		t.Errorf("100k-entry legacy import peaked %.2f MiB above the pre-open process, budget %d MiB",
			float64(imp.OverBase)/mib, maxBulkDelta/mib)
	}
}
