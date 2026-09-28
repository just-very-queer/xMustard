package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	got := quoteAnchors(root, quotes, []string{"server/timeouts.go"})
	if len(got) != 1 || got[0].Kind != govstore.AnchorCode || got[0].Value != "server/timeouts.go#L4-L6" {
		t.Fatalf("quoted code anchors %+v", got)
	}
	if _, ok := anchorSet(extracted)["ReadTimeout"]; ok {
		t.Fatal("identifiers inside a quoted block were extracted as well")
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
