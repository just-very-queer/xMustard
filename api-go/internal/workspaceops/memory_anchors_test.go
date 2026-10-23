package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/govstore"
)

func anchorSet(anchors []govstore.AnchorInput) map[string]string {
	out := map[string]string{}
	for _, a := range anchors {
		out[a.Value] = a.Kind
	}
	return out
}

func writeRepoFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Extraction reads each kind once, in table order: a URL is never also a path, an error
// code never also an env key, prose abbreviations stay prose, and a path must exist in
// the workspace.
func TestExtractAnchorsReadsEachKind(t *testing.T) {
	root := t.TempDir()
	writeRepoFile(t, root, "server/config.go", "package server\n")
	text := "See https://example.com/docs/run.html, e.g. run `make check-backend` or\n" +
		"$ go test ./internal/govstore/\nthen fix E0502 and ERR_MODULE_NOT_FOUND (ENOENT, not EXAMPLE); " +
		"XMUSTARD_DATA_DIR and ${HOME} matter. server/config.go:42 sets server.port = 8081, " +
		"`ListenPort` in govstore.InsertEntry; missing/file.go is gone."
	anchors, quotes := extractAnchors(root, text)
	want := map[string]string{
		"https://example.com/docs/run.html": govstore.AnchorURL,
		"make check-backend":                govstore.AnchorCommand,
		"go test ./internal/govstore/":      govstore.AnchorCommand,
		"E0502":                             govstore.AnchorErrorCode,
		"ERR_MODULE_NOT_FOUND":              govstore.AnchorErrorCode,
		"ENOENT":                            govstore.AnchorErrorCode,
		"XMUSTARD_DATA_DIR":                 govstore.AnchorEnv,
		"HOME":                              govstore.AnchorEnv,
		"server/config.go":                  govstore.AnchorPath,
		"server.port":                       govstore.AnchorConfig,
		"ListenPort":                        govstore.AnchorIdentifier,
		"govstore.InsertEntry":              govstore.AnchorIdentifier,
	}
	got := anchorSet(anchors)
	for v, kind := range want {
		if got[v] != kind {
			t.Errorf("%q: kind %q, want %q", v, got[v], kind)
		}
	}
	for _, noise := range []string{"e.g", "EXAMPLE", "missing/file.go", "example.com/docs/run.html"} {
		if _, ok := got[noise]; ok {
			t.Errorf("%q was read as an anchor (%s)", noise, got[noise])
		}
	}
	if len(quotes) != 0 || !slices.ContainsFunc(anchors, func(a govstore.AnchorInput) bool { return a.Extracted }) {
		t.Fatalf("quotes %v, anchors %+v", quotes, anchors)
	}
	var keys []string
	for i := range 60 {
		keys = append(keys, fmt.Sprintf("KEY_%d", i))
	}
	if capped, _ := extractAnchors("", strings.Join(keys, " ")); len(capped) != maxExtractedAnchors {
		t.Fatalf("%d anchors extracted, want the cap %d", len(capped), maxExtractedAnchors)
	}
	if _, ok := got["file.go"]; ok {
		t.Error("a slashed token naming no file was read again as an identifier")
	}
}

// Quoted code is placed on the lines of the file that holds it, through WS-65's
// anchoring; code that appears twice there anchors nothing.
func TestQuotedCodeIsAnchoredOnFileLines(t *testing.T) {
	root := t.TempDir()
	writeRepoFile(t, root, "server/timeouts.go", "package server\n\nconst (\n\tReadTimeout = 5\n\n\tWriteTimeout = 10\n)\n\nfunc a() {}\nfunc a() {}\n")
	text := "The read timeout is set here:\n```go\nReadTimeout = 5\n\nWriteTimeout = 10\n```\nand ```\nfunc a() {}\n``` twice."
	extracted, quotes := extractAnchors(root, text)
	if len(quotes) != 2 {
		t.Fatalf("quotes %q", quotes)
	}
	got, warn := quoteAnchors(root, quotes, []string{"server/timeouts.go"})
	if warn != "" || len(got) != 1 || got[0].Kind != govstore.AnchorCode || got[0].Value != "server/timeouts.go#L4-L6" {
		t.Fatalf("quoted code anchors %+v", got)
	}
	if _, ok := anchorSet(extracted)["ReadTimeout"]; ok {
		t.Fatal("identifiers inside a quoted block were extracted as well")
	}
}

// Extraction does bounded filesystem work: at most maxPathProbes path candidates are
// checked, only the first maxAnchorText bytes are read, and a full extraction checks
// nothing more, so a megabyte of slashed tokens costs at most maxPathProbes checks.
func TestExtractAnchorsBoundsFilesystemProbes(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; b.Len() < 1<<20; i++ {
		fmt.Fprintf(&b, "d%d/f%d ", i, i)
	}
	x := &anchorExtraction{root: root, seen: map[govstore.AnchorRef]bool{}}
	start := time.Now()
	x.run(b.String())
	if x.probes != maxPathProbes || len(x.anchors) != 0 {
		t.Fatalf("%d probes and %d anchors, want %d probes and none", x.probes, len(x.anchors), maxPathProbes)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("extracting from 1 MiB of path-like tokens took %v", took)
	}
	var names []string
	for i := range maxExtractedAnchors + 8 {
		names = append(names, fmt.Sprintf("pkg/f%d.go", i))
		writeRepoFile(t, root, names[i], "package pkg\n")
	}
	x = &anchorExtraction{root: root, seen: map[govstore.AnchorRef]bool{}}
	x.run(strings.Join(names, " ") + " " + b.String())
	if len(x.anchors) != maxExtractedAnchors || x.probes != maxExtractedAnchors {
		t.Fatalf("%d anchors after %d probes; a full extraction must stop checking at %d", len(x.anchors), x.probes,
			maxExtractedAnchors)
	}
}

// allocated reports the bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Quote placement reads nothing when the proposal quotes no code, never reads a file
// over its cap, and reads the rest one at a time only until each block is placed.
func TestQuoteAnchorsReadLazilyWithinBounds(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation makes the large-file allocation and time bounds non-representative")
	}
	root := t.TempDir()
	var big []string
	for i := range maxQuoteFiles {
		rel := fmt.Sprintf("big/f%d.go", i)
		writeRepoFile(t, root, rel, "")
		if err := os.Truncate(filepath.Join(root, rel), 7<<20); err != nil {
			t.Fatal(err)
		}
		big = append(big, rel)
	}
	var anchors []govstore.AnchorInput
	if n := allocated(func() {
		anchors, _ = proposalAnchors(context.Background(), root, "ws", "The timeouts live in the big files.", big)
	}); n > 256<<10 || slices.ContainsFunc(anchors, func(a govstore.AnchorInput) bool { return a.Kind == govstore.AnchorCode }) {
		t.Fatalf("a proposal naming four 7 MiB files and quoting no code allocated %d bytes (anchors %+v)", n, anchors)
	}
	if n := allocated(func() { anchors, _ = quoteAnchors(root, []string{"const ReadTimeout = 5\n"}, big) }); n > 256<<10 || len(anchors) != 0 {
		t.Fatalf("placement over files past the cap allocated %d bytes (anchors %+v)", n, anchors)
	}
	body := "package server\n\nconst ReadTimeout = 5\n" + strings.Repeat("// padding line\n", 20_000)
	writeRepoFile(t, root, "server/a.go", body)
	writeRepoFile(t, root, "server/b.go", body)
	n := allocated(func() {
		anchors, _ = quoteAnchors(root, []string{"const ReadTimeout = 5\n"}, []string{"server/a.go", "server/b.go"})
	})
	if len(anchors) != 1 || anchors[0].Value != "server/a.go#L3-L3" {
		t.Fatalf("quoted code anchors %+v", anchors)
	}
	// one file's text (1x) and the line index Locate builds over it (about 1.5x); reading
	// the second file as well would double it
	if limit := uint64(len(body)) * 7 / 2; n > limit {
		t.Fatalf("placing one block allocated %d bytes, over %d: a second file was read", n, limit)
	}
}

// Identifiers resolve through the resolver: one declaration makes a symbol anchor with
// its uid, several leave the identifier, and a failed resolution keeps the anchors and
// says so.
func TestResolveAnchorsToSymbols(t *testing.T) {
	orig := resolveSymbols
	t.Cleanup(func() { resolveSymbols = orig })
	var asked []string
	resolveSymbols = func(_ context.Context, _, _ string, names []string) ([]symbolMatch, error) {
		asked = names
		var out []symbolMatch
		for _, n := range names {
			m := symbolMatch{Name: n, Count: map[string]int{"ListenPort": 1, "Handle": 2}[n]}
			for i := range m.Count {
				m.Definitions = append(m.Definitions, symbolDefinition{Path: fmt.Sprintf("pkg%d/file.go", i), Line: 3})
			}
			out = append(out, m)
		}
		return out, nil
	}
	in := []govstore.AnchorInput{{Kind: govstore.AnchorIdentifier, Value: "ListenPort"}, {Kind: govstore.AnchorIdentifier, Value: "Handle"},
		{Kind: govstore.AnchorEnv, Value: "HOME"}}
	out, warn := resolveAnchors(context.Background(), "/repo", "ws", in)
	if warn != "" || !slices.Equal(asked, []string{"ListenPort", "Handle"}) {
		t.Fatalf("asked %v, warning %q", asked, warn)
	}
	if out[0].Kind != govstore.AnchorSymbol || out[0].SymbolUID != "pkg0/file.go#ListenPort" || out[1].Kind != govstore.AnchorIdentifier {
		t.Fatalf("resolved %+v", out)
	}
	resolveSymbols = func(context.Context, string, string, []string) ([]symbolMatch, error) {
		return nil, errors.New("worker busy")
	}
	out, warn = resolveAnchors(context.Background(), "/repo", "ws", in)
	if !strings.Contains(warn, "worker busy") || !slices.Equal(out, in) {
		t.Fatalf("a failed resolution: %+v %q", out, warn)
	}
}

// Anchors resolve symbols through the core worker: remember on a registered repository
// stores a symbol anchor whose uid names the declaring file.
func TestAnchorsResolveSymbolsThroughTheWorker(t *testing.T) {
	core := realCoreForOutcomes(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, ws, root := seedBaselineWorkspace(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	writeRepoFile(t, root, "server/config.go", "package server\n\n// ListenPort is the service port.\nfunc ListenPort() int { return 8081 }\n")
	writeRepoFile(t, root, "client/dial.go", "package client\n\nfunc Dial() {}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	e := rememberAs(t, dir, ws, ContextActor{ID: "author"}, "`ListenPort` in server/config.go returns 8081; `NoSuchSymbol` is gone.")
	byValue := map[string]MemoryAnchor{}
	for _, a := range e.Anchors {
		byValue[a.Value] = a
	}
	if a := byValue["ListenPort"]; a.Kind != govstore.AnchorSymbol || a.SymbolUID != "server/config.go#ListenPort" || a.Fanout != 1 {
		t.Fatalf("ListenPort anchor %+v (all %+v, warnings %v)", a, e.Anchors, e.Warnings)
	}
	if a := byValue["NoSuchSymbol"]; a.Kind != govstore.AnchorIdentifier {
		t.Fatalf("an unknown identifier: %+v", a)
	}
	if a := byValue["server/config.go"]; a.Kind != govstore.AnchorPath || a.Declared {
		t.Fatalf("the extracted path %+v", a)
	}
}

// remember's fan-out counts only memory the proposer may read: a pending entry sharing
// the anchor counts for a reviewer and for no one else, and the new entry always counts
// itself.
func TestAnchorFanoutCountsReadableMemoryOnly(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	const url = "https://wiki.example.com/runbooks/rotate-keys"
	promoted(t, dir, ws, "author", "Signing keys rotate every quarter; the steps are at "+url)
	rememberAs(t, dir, ws, ContextActor{ID: "author"}, "Rotating the webhook secret follows "+url+" as well.")
	fanout := func(who ContextActor, text string) int {
		t.Helper()
		e := rememberAs(t, dir, ws, who, text)
		i := slices.IndexFunc(e.Anchors, func(a MemoryAnchor) bool { return a.Value == url })
		if i < 0 {
			t.Fatalf("no anchor for the URL: %+v", e.Anchors)
		}
		return e.Anchors[i].Fanout
	}
	if n := fanout(ContextActor{ID: "agent-b"}, "Our key rotation runbook is "+url); n != 2 {
		t.Fatalf("a proposer without the verifier role sees fan-out %d, want 2 (the served entry and its own)", n)
	}
	if n := fanout(ContextActor{ID: "agent-c", Verifier: true}, "The runbook for rotating deploy keys: "+url); n != 4 {
		t.Fatalf("a reviewer sees fan-out %d, want 4 (served, two pending and its own)", n)
	}
}

// Anchor lookups read active memory only: retired entries neither damp recall's anchor
// boost nor take the review's neighbour slots from a live overlap.
func TestAnchorLookupsIgnoreRetiredMemory(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	author := ContextActor{ID: "author"}
	live := rememberAs(t, dir, ws, author, "The payments service reads PAYMENTS_PORT at start.", withPaths("server/config.go"))
	for i := range maxConflictSides + 4 {
		e := rememberAs(t, dir, ws, author, fmt.Sprintf("Retired note %d: the old gateway also read PAYMENTS_PORT.", i),
			withPaths("server/config.go"))
		storeUpdate(t, dir, ws, func(tx govstore.Tx) error {
			_, err := tx.Transition(context.Background(), e.ID, govstore.TransitionInput{To: govstore.LifecycleRetracted}, govstore.Actor{Principal: "admin"})
			return err
		})
	}
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Query: "PAYMENTS_PORT", Explain: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	entries := res["entries"].([]ContextEntry)
	if len(entries) != 1 || entries[0].ID != live.ID || entries[0].ScoreDetails.Anchor != 0.5 {
		t.Fatalf("recall damped the live memory by retired ones: %+v", entries)
	}
	for _, who := range []ContextActor{{ID: "agent-b"}, {ID: "agent-c", Verifier: true}} {
		other := rememberAs(t, dir, ws, who, "Lint settings for the "+who.ID+" build are unrelated to ports.",
			withPaths("server/config.go"))
		if c, ok := conflictWith(other, live.ID); !ok || c.Kind != ConflictPathOverlap {
			t.Fatalf("%s: retired entries hid the live path overlap: %+v", who.ID, other.PossibleConflicts)
		}
	}
}

// The anchor boost is damped by fan-out: sim·0.5/(1+0.001(n−1)²). In recall a memory
// that alone holds an anchor the query names outranks memories sharing a common one.
func TestHighFanoutAnchorIsDamped(t *testing.T) {
	for _, c := range []struct {
		n    int
		want float64
	}{{1, 0.5}, {2, 0.5 / 1.001}, {11, 0.5 / 1.1}, {101, 0.5 / 11}} {
		if got := anchorBoost(1, c.n); math.Abs(got-c.want) > 1e-12 {
			t.Fatalf("anchorBoost(1, %d) = %v, want %v", c.n, got, c.want)
		}
	}
	dir, ws := t.TempDir(), "ws"
	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	var common []*ContextEntry
	for i := range 40 {
		common = append(common, rememberAs(t, dir, ws, ContextActor{ID: "author"},
			fmt.Sprintf("Deploy step %d of the rollout reads SHARED_DEPLOY_TOKEN from the vault.", i)))
	}
	rare := rememberAs(t, dir, ws, ContextActor{ID: "author"}, "The canary job alone reads RARE_CANARY_FLAG before it starts.")
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{Query: "SHARED_DEPLOY_TOKEN RARE_CANARY_FLAG", Explain: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	anchorScore := map[string]float64{}
	for _, e := range res["entries"].([]ContextEntry) {
		anchorScore[e.ID] = e.ScoreDetails.Anchor
	}
	if got := anchorScore[rare.ID]; got != 0.5 {
		t.Fatalf("a unique anchor scores %v, want 0.5", got)
	}
	damped := math.Round(anchorBoost(1, 40)*1000) / 1000
	if got := anchorScore[common[0].ID]; got != damped || got >= 0.2 {
		t.Fatalf("an anchor 40 memories share scores %v, want the damped %v", got, damped)
	}
}
