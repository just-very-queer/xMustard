//go:build memory_rss

package workspaceops

// Resident cost of propose-time review at 50k memories (WS-27 critic correction: the
// MinHash/LSH postings and the anchor postings live in SQLite, nothing about the
// memories stays resident). Run it on the build box:
//
//	go test -tags memory_rss -run TestProposeReviewRSSAt50kMemories -v ./internal/workspaceops/
//
// A child process seeds 50k promoted memories straight into the governance store
// (postings included), so this process starts from a clean baseline. It then opens the
// store with one proposal and runs 500 more through Remember: exact restatements
// (corroborations), near duplicates, anchored neighbours and fresh text. RSS is the
// current resident set (VmRSS, or ps on macOS) after a GC; the review must not grow it
// with the proposals, and the whole process must stay inside the daemon's envelope.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

const (
	rssMemories  = 50_000
	rssProposals = 500
	rssWorkspace = "wsRSS"
	// maxReviewGrowth bounds RSS growth over the proposals, after the first opened the
	// store; maxReviewRSS bounds the process.
	maxReviewGrowth = 8 << 20
	maxReviewRSS    = 96 << 20
)

func rssContent(i int) string {
	return fmt.Sprintf("Memory %d: the %s service in pkg/mod%d/handler.go reads CONFIG_KEY_%d and listens on port %d; "+
		"run `go test ./pkg/mod%d/...` after touching it.", i, []string{"billing", "search", "auth", "export"}[i%4], i%1000, i%97, 8000+i%500, i%1000)
}

func seedRSSStore(dir string) error {
	ctx := context.Background()
	s, err := govstore.Open(ctx, memoryStorePath(dir), govstore.Options{})
	if err != nil {
		return err
	}
	defer s.Close()
	author := govstore.Actor{Principal: "seeder"}
	for start := 0; start < rssMemories; start += 5000 {
		if err := s.Update(ctx, func(tx govstore.Tx) error {
			for i := start; i < start+5000; i++ {
				content := rssContent(i)
				e, err := tx.InsertEntry(ctx, govstore.NewEntry{ID: fmt.Sprintf("ctx_seed%06d", i), WorkspaceID: rssWorkspace,
					Title: "t", Content: content, Permission: "readwrite", RequiredVerifications: 1,
					Paths: []string{fmt.Sprintf("pkg/mod%d/handler.go", i%1000)}, SearchTokens: memoryTokenList(content),
					Anchors: []govstore.AnchorInput{{Kind: govstore.AnchorEnv, Value: fmt.Sprintf("CONFIG_KEY_%d", i%97), Extracted: true}},
				}, author)
				if err != nil {
					return err
				}
				if _, err := tx.SetPromotion(ctx, e.ID, govstore.Promotion{Status: govstore.StatusVerified, Promoted: true,
					VerificationMode: govstore.ModeSingleAgent}, author); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return s.Checkpoint(ctx)
}

func currentRSS(t *testing.T) int64 {
	t.Helper()
	runtime.GC()
	debug.FreeOSMemory()
	if raw, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb << 10
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb << 10
}

func TestProposeReviewRSSAt50kMemories(t *testing.T) {
	if dir := os.Getenv("XM_RSS_SEED_DIR"); dir != "" {
		if err := seedRSSStore(dir); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	started := time.Now()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestProposeReviewRSSAt50kMemories$")
	cmd.Env = append(os.Environ(), "XM_RSS_SEED_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	seeded := time.Since(started)
	info, err := os.Stat(memoryStorePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	base := currentRSS(t)
	who := ContextActor{ID: "agent"}
	rememberAs(t, dir, rssWorkspace, who, "The first proposal opens the store: exports run nightly at 02:00 UTC.")
	opened := currentRSS(t)
	outcomes := map[string]int{}
	var dups, conflicts int
	started = time.Now()
	for i := range rssProposals {
		content, mod := fmt.Sprintf("Fresh fact %d: the nightly job %d archives logs older than %d days.", i, i*13, 30+i%60), i%1000
		switch i % 4 {
		case 0: // an exact restatement of a seeded memory, about its file
			content, mod = strings.ToUpper(rssContent(i*97)), (i*97)%1000
		case 1: // a near duplicate
			content, mod = rssContent(i*89)+"!", (i*89)%1000
		case 2: // an anchored neighbour with another port
			content = fmt.Sprintf("The handler in pkg/mod%d/handler.go must listen on port %d instead.", mod, 9000+i)
		}
		e := rememberAs(t, dir, rssWorkspace, who, content, withPaths(fmt.Sprintf("pkg/mod%d/handler.go", mod)))
		outcomes[e.Outcome]++
		dups += len(e.PossibleDuplicates)
		conflicts += len(e.PossibleConflicts)
	}
	perPropose := time.Since(started) / rssProposals
	after := currentRSS(t)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("store %.1f MiB for %d memories (seeded in %s); RSS base %.1f MiB, store open %.1f MiB, after %d proposals %.1f MiB "+
		"(growth %.2f MiB); Go heap in use %.1f MiB; %s per proposal; outcomes %v, %d possible duplicates, %d possible conflicts",
		float64(info.Size())/(1<<20), rssMemories, seeded.Round(time.Millisecond), float64(base)/(1<<20), float64(opened)/(1<<20),
		rssProposals, float64(after)/(1<<20), float64(after-opened)/(1<<20), float64(ms.HeapInuse)/(1<<20),
		perPropose.Round(time.Microsecond), outcomes, dups, conflicts)
	if outcomes[OutcomeCorroborated] < rssProposals/4 || dups == 0 || conflicts == 0 {
		t.Fatalf("the proposals did not exercise the review: outcomes %v, %d duplicates, %d conflicts", outcomes, dups, conflicts)
	}
	if after-opened > maxReviewGrowth {
		t.Fatalf("RSS grew %.2f MiB over %d proposals, want at most %d MiB", float64(after-opened)/(1<<20), rssProposals, maxReviewGrowth>>20)
	}
	if after > maxReviewRSS {
		t.Fatalf("RSS %.1f MiB after the proposals, want at most %d MiB", float64(after)/(1<<20), maxReviewRSS>>20)
	}
}
