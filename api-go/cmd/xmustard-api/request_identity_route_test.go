package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// identityFixture serves the real handler stack over a workspace whose root is a
// Git repository, with a fake Rust core whose `repo-key` appends a line to a log
// (one line per spawn) and reports the content of keyFile as a complete identity.
type identityFixture struct {
	*evidenceFixture
	root   string
	keyLog string
}

func newIdentityFixture(t *testing.T, searchBytes int) *identityFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if canon, err := filepath.EvalSymlinks(root); err == nil {
		root = canon
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "f.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "init")

	f := &identityFixture{evidenceFixture: &evidenceFixture{ws: "wsId"}, root: root}
	f.dir = t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", f.dir)
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": f.ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(f.dir, "workspaces", f.ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "workspaces", f.ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	// a search result of about searchBytes as the search handler re-encodes it
	// (fixed-width hits), with one failure among many passes
	type hit struct {
		Kind   string  `json:"kind"`
		Name   string  `json:"name"`
		Path   string  `json:"path"`
		Line   int     `json:"line"`
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	one, _ := json.Marshal(hit{"symbol", "ok0000000", "src/f0000000.go", 1, 1, "lexical match"})
	n := max(searchBytes/(len(one)+1), 10)
	hits := make([]hit, n)
	for i := range hits {
		hits[i] = hit{"symbol", fmt.Sprintf("ok%07d", i), fmt.Sprintf("src/f%07d.go", i), 1, 1, "lexical match"}
	}
	hits[n/2].Reason = "panic: assertion failed"
	f.big, _ = json.Marshal(map[string]any{"query": "x", "hits": hits})
	bigFile := filepath.Join(t.TempDir(), "big.json")
	_ = os.WriteFile(bigFile, f.big, 0o644)
	f.keyFile = filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(f.keyFile, []byte("rev-1"), 0o644)
	f.keyLog = filepath.Join(t.TempDir(), "repo-key.log")
	core := writeScript(t, `case "$1" in
search) cat `+bigFile+` ;;
repo-key) echo spawn >> `+f.keyLog+`; printf '{"key":"%s","identity_complete":true}' "$(cat `+f.keyFile+`)" ;;
changetrack) printf '{"head_sha":"abc","content_hash":"h","dirty":false,"changed_files":[]}' ;;
*) echo '{}' ;;
esac
`)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	workspaceops.InvalidateRepoIdentity("")
	t.Cleanup(func() { workspaceops.InvalidateRepoIdentity("") })
	f.srv = httptest.NewServer(bodyLimitMiddleware(authMiddleware(f.dir, "auto", newAPIHandler())))
	t.Cleanup(f.srv.Close)
	return f
}

// repoKeySpawns counts repo-key processes started so far.
func (f *identityFixture) repoKeySpawns() int {
	b, _ := os.ReadFile(f.keyLog)
	return strings.Count(string(b), "spawn")
}

// PAR-FRESH-02: each core read tool samples the repository identity exactly once
// (cold cache), including the capture-time re-check; remember and verify never do.
func TestIdentitySampledOncePerReadToolAndNeverForWrites(t *testing.T) {
	f := newIdentityFixture(t, 200<<10)
	ws := "/api/workspaces/" + f.ws
	read := []struct{ tool, method, path string }{
		{"ground", "GET", ws + "/session-grounding"},
		{"recall", "GET", ws + "/context/active?query=x"},
		{"search", "GET", ws + "/search?q=x"},
		{"explain", "GET", ws + "/explain-path?path=src/f.go"},
		{"impact", "GET", ws + "/changes/since-index"},
		{"diagnostics", "GET", ws + "/diagnostics"},
		{"why_failed", "GET", ws + "/runs/run_x/why-failed"},
	}
	for _, c := range read {
		workspaceops.InvalidateRepoIdentity("")
		before := f.repoKeySpawns()
		code, b, _ := f.do(t, c.method, c.path, "", nil, deliver)
		var d evidence.Delivery
		_ = json.Unmarshal(b, &d)
		if code != 200 || d.Tool != c.tool {
			t.Fatalf("%s: %d %s", c.tool, code, b[:min(len(b), 200)])
		}
		if n := f.repoKeySpawns() - before; n != 1 {
			t.Fatalf("%s sampled identity %d times; want exactly 1", c.tool, n)
		}
		if c.tool == "search" && (!d.Reduced || d.CapturedIdentity != "bound") {
			t.Fatalf("search must be reduced and bound through the cached re-check: %+v", d)
		}
	}
	for _, c := range []struct{ tool, path string }{
		{"remember", ws + "/context?content=a+durable+fact&title=t"},
		{"verify", ws + "/context/ctx_missing/verify?approve=true"},
	} {
		workspaceops.InvalidateRepoIdentity("")
		before := f.repoKeySpawns()
		_, b, _ := f.do(t, "POST", c.path, "", nil, deliver)
		var d evidence.Delivery
		_ = json.Unmarshal(b, &d)
		if d.Tool != c.tool || d.CapturedIdentity != "unknown" {
			t.Fatalf("%s: %s", c.tool, b[:min(len(b), 200)])
		}
		if n := f.repoKeySpawns() - before; n != 0 {
			t.Fatalf("write tool %s sampled identity %d times; want 0", c.tool, n)
		}
	}
	// warm: an unchanged repository answers from the identity cache
	for i, want := range []int{1, 0} {
		before := f.repoKeySpawns()
		if code, _, _ := f.do(t, "GET", ws+"/search?q=x", "", nil, deliver); code != 200 {
			t.Fatal(code)
		}
		if n := f.repoKeySpawns() - before; n != want {
			t.Fatalf("search %d sampled identity %d times; want %d", i+1, n, want)
		}
	}
}

// Evidence pages inside the identity window reuse the identity with no repo-key
// spawn while HEAD and the stat digest are unchanged; a change re-samples and the
// page is labelled stale.
func TestEvidencePagesReuseIdentityUntilTheRepositoryChanges(t *testing.T) {
	f := newIdentityFixture(t, 400<<10)
	_, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	if d.Handle == "" || d.CapturedIdentity != "bound" {
		t.Fatalf("capture: %+v", d)
	}
	before := f.repoKeySpawns()
	orig, last := f.expandAll(t, d.Handle, "")
	if sum := sha256.Sum256(orig); int64(len(orig)) != d.RawBytes || hex.EncodeToString(sum[:]) != d.RawSHA256 {
		t.Fatal("expanded original differs from the capture")
	}
	if n := f.repoKeySpawns() - before; n != 0 {
		t.Fatalf("expanding %d pages spawned repo-key %d times; want 0", len(orig)/evidence.DefaultPageSize+1, n)
	}
	if last["freshness"] != "current" || last["current_key_cached"] != true {
		t.Fatalf("cached page: %v", last)
	}
	// the repository changes: a tracked file is edited (and its identity moves)
	if err := os.WriteFile(filepath.Join(f.root, "src", "f.go"), []byte("package src // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f.keyFile, []byte("rev-2"), 0o644)
	code, body, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/"+d.Handle+"?offset=0", "", nil, nil)
	var page map[string]any
	_ = json.Unmarshal(body, &page)
	if code != 200 || page["freshness"] != "stale" || page["current_key"] != "rev-2" || page["current_key_cached"] != false {
		t.Fatalf("after a change the page must re-sample and be stale: %d %v", code, page)
	}
	if n := f.repoKeySpawns() - before; n != 1 {
		t.Fatalf("the change re-sampled %d times; want 1", n)
	}
	// a result posted after the fact is never bound, so its pages need no identity
	_, b, _ = f.do(t, "POST", "/api/workspaces/"+f.ws+"/evidence?tool=search", "", bytes.NewReader(f.big), nil)
	_ = json.Unmarshal(b, &d)
	before = f.repoKeySpawns()
	if _, last = f.expandAll(t, d.Handle, ""); last["freshness"] != "unknown" {
		t.Fatalf("posted evidence: %v", last)
	}
	if n := f.repoKeySpawns() - before; n != 0 {
		t.Fatalf("pages of unbound evidence spawned repo-key %d times", n)
	}
}

// Acceptance: expanding a 16 MiB-class original no longer spawns a process per page.
func TestExpanding16MiBOriginalSpawnsNoProcessPerPage(t *testing.T) {
	f := newIdentityFixture(t, evidence.DefaultMaxOriginal-(512<<10))
	_, b, _ := f.do(t, "GET", "/api/workspaces/"+f.ws+"/search?q=x", "", nil, deliver)
	var d evidence.Delivery
	_ = json.Unmarshal(b, &d)
	if d.Handle == "" || d.RawBytes < 15<<20 {
		t.Fatalf("capture of %d bytes: %s", d.RawBytes, b[:min(len(b), 200)])
	}
	before := f.repoKeySpawns()
	start := time.Now()
	orig, _ := f.expandAll(t, d.Handle, "")
	elapsed := time.Since(start)
	pages := (len(orig) + evidence.DefaultPageSize - 1) / evidence.DefaultPageSize
	if n := f.repoKeySpawns() - before; n != 0 {
		t.Fatalf("%d pages spawned repo-key %d times; want 0", pages, n)
	}
	t.Logf("expanded %d bytes in %d pages in %v with 0 repo-key spawns (fake core)", len(orig), pages, elapsed)
}
