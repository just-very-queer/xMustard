package evidence

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var ledgerNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func newTestLedger(limits LedgerLimits) *Ledger {
	l := NewLedger(limits)
	l.SetClock(func() time.Time { return ledgerNow })
	return l
}

func testHandle(t *testing.T) string {
	t.Helper()
	h, err := NewHandle()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// delivered is a successful 4 KiB search result with a live handle.
func delivered(t *testing.T, callID string) Delivered {
	return Delivered{
		Scope: Scope{Workspace: "ws", Principal: "agent-a", Session: "s1"},
		Tool:  "search", Args: []byte(`{"q":"Double","limit":5}`), Rev: completeRev(), CallID: callID,
		Digest: ContentDigestOf("search", []byte(searchBody)), Bytes: 4096,
		Handle: testHandle(t), ExpiresAt: ledgerNow.Add(24 * time.Hour),
	}
}

func TestLedgerRepeatReturnsUnchangedSinceCallWithHandle(t *testing.T) {
	l := newTestLedger(LedgerLimits{})
	first := delivered(t, "toolu_01")
	if d := l.Observe(first); d.Unchanged || d.Exempt {
		t.Fatalf("first call: %+v", d)
	}
	again := delivered(t, "toolu_02")
	d := l.Observe(again)
	if !d.Unchanged || d.SinceCall != "toolu_01" || d.SinceSeq != 1 || d.Handle != first.Handle || d.Repeats != 1 {
		t.Fatalf("repeat: %+v", d)
	}
	note := d.Notice("ws")
	for _, want := range []string{NoticePrefix, `"unchanged":true`, `"since_call":"toolu_01"`, first.Handle,
		ResourceURI(first.Handle, "ws")} {
		if !strings.Contains(note, want) {
			t.Fatalf("notice lacks %q: %s", want, note)
		}
	}
	if d := l.Observe(delivered(t, "toolu_03")); !d.Unchanged || d.Repeats != 2 || d.Handle != first.Handle {
		t.Fatalf("third call: %+v", d)
	}
}

func TestLedgerNeverSuppressesAChangedCall(t *testing.T) {
	for name, change := range map[string]func(*Delivered){
		"result":       func(o *Delivered) { o.Digest = ContentDigestOf("search", []byte(`{"total":2}`)) },
		"arguments":    func(o *Delivered) { o.Args = []byte(`{"q":"Double","limit":6}`) },
		"repository":   func(o *Delivered) { o.Rev.Repo.Key = "repo-2" },
		"memory store": func(o *Delivered) { o.Rev.Memory = "mem-2" },
		"parser":       func(o *Delivered) { o.Rev.Parser = "xm-index-3" },
		"principal":    func(o *Delivered) { o.Scope.Principal = "agent-b" },
		"workspace":    func(o *Delivered) { o.Scope.Workspace = "ws2" },
		"session":      func(o *Delivered) { o.Scope.Session = "s2" },
		"small result": func(o *Delivered) { o.Bytes = 100 },
		"tool":         func(o *Delivered) { o.Tool = "explain" },
	} {
		l := newTestLedger(LedgerLimits{})
		l.Observe(delivered(t, "toolu_01"))
		o := delivered(t, "toolu_02")
		change(&o)
		if d := l.Observe(o); d.Unchanged {
			t.Errorf("%s changed, yet the call was reported unchanged: %+v", name, d)
		}
	}
}

func TestLedgerNeedsALiveHandleAndACompleteIdentity(t *testing.T) {
	l := newTestLedger(LedgerLimits{})
	noHandle := delivered(t, "toolu_01")
	noHandle.Handle = ""
	l.Observe(noHandle)
	if d := l.Observe(noHandle); d.Unchanged {
		t.Fatal("a notice pointed at no handle")
	}
	// a handle within noticeMinTTL of its expiry is not pointed at; the new one is kept
	l = newTestLedger(LedgerLimits{})
	expiring := delivered(t, "toolu_01")
	expiring.ExpiresAt = ledgerNow.Add(30 * time.Second)
	l.Observe(expiring)
	fresh := delivered(t, "toolu_02")
	if d := l.Observe(fresh); d.Unchanged {
		t.Fatal("a notice pointed at an expiring handle")
	}
	if d := l.Observe(delivered(t, "toolu_03")); !d.Unchanged || d.Handle != fresh.Handle || d.SinceCall != "toolu_02" {
		t.Fatalf("the refreshed handle was not kept: %+v", d)
	}
	// an invalid handle string is no handle
	l = newTestLedger(LedgerLimits{})
	bad := delivered(t, "toolu_01")
	bad.Handle = HandlePrefix + "short"
	l.Observe(bad)
	if d := l.Observe(bad); d.Unchanged {
		t.Fatal("a malformed handle was pointed at")
	}
	inc := delivered(t, "toolu_01")
	inc.Rev.Repo.Complete = false
	if d := l.Observe(inc); !d.Exempt || d.Reason != "identity_incomplete" {
		t.Fatalf("incomplete identity: %+v", d)
	}
	noMem := delivered(t, "toolu_01")
	noMem.Rev.Memory = ""
	if d := l.Observe(noMem); !d.Exempt || d.Reason != "revision_missing" {
		t.Fatalf("missing memory revision: %+v", d)
	}
	badArgs := delivered(t, "toolu_01")
	badArgs.Args = []byte(`[1]`)
	if d := l.Observe(badArgs); !d.Exempt || d.Reason != "args_invalid" {
		t.Fatalf("invalid args: %+v", d)
	}
}

func TestLedgerNeverDedupesSideEffectingCalls(t *testing.T) {
	for _, tool := range []string{"remember", "verify", "mcp__xmustard__verify", "Bash", "exec_command", "Edit", "Write",
		"TodoWrite", "mcp__github__create_issue"} {
		l := newTestLedger(LedgerLimits{})
		o := delivered(t, "toolu_01")
		o.Tool, o.ReadOnly = tool, true // a read-only declaration never overrides a known tool
		if tool == "mcp__github__create_issue" {
			o.ReadOnly = false
		}
		for i := 0; i < 5; i++ {
			if d := l.Observe(o); !d.Exempt || d.Reason != "side_effecting" || d.Unchanged || d.Failing != nil {
				t.Fatalf("%s call %d: %+v", tool, i, d)
			}
		}
		if _, n := l.Len(); n != 0 {
			t.Fatalf("%s: side-effecting calls were recorded (%d)", tool, n)
		}
	}
	// another server's tool its caller declares read-only is guarded
	l := newTestLedger(LedgerLimits{})
	o := delivered(t, "toolu_01")
	o.Tool, o.ReadOnly = "mcp__docs__lookup", true
	l.Observe(o)
	if d := l.Observe(o); !d.Unchanged {
		t.Fatalf("declared read-only MCP tool: %+v", d)
	}
}

func TestClassifyError(t *testing.T) {
	for _, c := range []struct {
		isError bool
		status  int
		text    string
		want    ErrorClass
	}{
		{false, 200, "", ClassSuccess},
		{true, 400, "", ClassValidation},
		{true, 422, "anything", ClassValidation},
		{true, 0, "q is required", ClassValidation},
		{true, 0, "invalid character 'x' looking for beginning of value", ClassValidation},
		{true, 404, "", ClassNotFound},
		{true, 0, "open missing.go: no such file or directory", ClassNotFound},
		{true, 0, "Workspace not found", ClassNotFound},
		{true, 403, "", ClassPermission},
		{true, 401, "", ClassPermission},
		{true, 0, "verify requires the verifier role: permission denied", ClassPermission},
		{true, 504, "", ClassTimeout},
		{true, 0, "context deadline exceeded", ClassTimeout},
		{true, 0, "command timed out after 30s", ClassTimeout},
		{true, 500, "core exited with status 3", ClassToolError},
		{true, 0, "something odd", ClassToolError},
		{false, 500, "internal", ClassToolError},
	} {
		if got := ClassifyError(c.isError, c.status, c.text); got != c.want {
			t.Errorf("(%v,%d,%q) = %s, want %s", c.isError, c.status, c.text, got, c.want)
		}
	}
}

func failing(t *testing.T, tool, args, text string) Delivered {
	o := delivered(t, "")
	o.Tool, o.Args, o.IsError, o.ErrorText, o.Bytes = tool, []byte(args), true, text, 80
	o.Digest = ContentDigestOf(tool, []byte(text))
	o.Handle = ""
	return o
}

func TestLedgerAnnotatesRepeatedFailuresByClass(t *testing.T) {
	l := newTestLedger(LedgerLimits{})
	o := failing(t, "explain", `{"path":"missing.go"}`, "path not found")
	for i := 1; i <= 3; i++ {
		d := l.Observe(o)
		if d.Class != ClassNotFound || d.Unchanged {
			t.Fatalf("call %d: %+v", i, d)
		}
		if i <= 2 && d.Failing != nil {
			t.Fatalf("call %d annotated early: %+v", i, d.Failing)
		}
		if i == 3 && (d.Failing == nil || d.Failing.Scope != "strict" || d.Failing.Count != 3 || d.Failing.Limit != 2) {
			t.Fatalf("third identical failure not annotated: %+v", d.Failing)
		}
	}
	note := l.Observe(o).Notice("ws")
	if !strings.Contains(note, `"repeated_failure":"not_found"`) || !strings.Contains(note, "does not exist") {
		t.Fatalf("annotation: %s", note)
	}
	// a changed class restarts the strict count
	if d := l.Observe(failing(t, "explain", `{"path":"missing.go"}`, "timed out")); d.Class != ClassTimeout || d.Failing != nil {
		t.Fatalf("class change: %+v", d)
	}

	// coarse: one tool, one class, different arguments; 3x MaxRepeat for a
	// non-exploratory tool, 6x for an exploratory one
	for _, c := range []struct {
		tool  string
		limit int
	}{{"diagnostics", 6}, {"search", 12}} {
		l := newTestLedger(LedgerLimits{})
		var d Decision
		for i := 1; i <= c.limit+1; i++ {
			o := failing(t, c.tool, fmt.Sprintf(`{"q":"x%d"}`, i), "invalid argument")
			o.Rev.Source = "diag-1"
			d = l.Observe(o)
			if i <= c.limit && d.Failing != nil {
				t.Fatalf("%s call %d annotated early: %+v", c.tool, i, d.Failing)
			}
		}
		if d.Failing == nil || d.Failing.Scope != "coarse" || d.Failing.Limit != c.limit {
			t.Fatalf("%s: coarse limit not annotated: %+v", c.tool, d.Failing)
		}
		// a success of the tool resets its coarse count
		ok := delivered(t, "")
		ok.Tool, ok.Rev.Source = c.tool, "diag-1"
		l.Observe(ok)
		o := failing(t, c.tool, `{"q":"fresh"}`, "invalid argument")
		o.Rev.Source = "diag-1"
		if d := l.Observe(o); d.Failing != nil {
			t.Fatalf("%s: coarse count survived a success: %+v", c.tool, d.Failing)
		}
	}
}

func TestLedgerIsBoundedPerSessionAndInTotal(t *testing.T) {
	l := newTestLedger(LedgerLimits{Sessions: 4, PerSession: 8, MaxEntries: 20})
	for s := 0; s < 6; s++ {
		for i := 0; i < 10; i++ {
			o := delivered(t, "")
			o.Scope.Session = fmt.Sprintf("s%d", s)
			o.Args = []byte(fmt.Sprintf(`{"q":"%d"}`, i))
			l.Observe(o)
			sessions, entries := l.Len()
			if sessions > 4 || entries > 20 {
				t.Fatalf("over the limits: %d sessions, %d entries", sessions, entries)
			}
		}
	}
	// the most recent session kept its 8 most recent fingerprints
	o := delivered(t, "")
	o.Scope.Session, o.Args = "s5", []byte(`{"q":"9"}`)
	if d := l.Observe(o); !d.Unchanged {
		t.Fatalf("recent fingerprint evicted: %+v", d)
	}
	o.Args = []byte(`{"q":"0"}`)
	if d := l.Observe(o); d.Unchanged {
		t.Fatal("the session's oldest fingerprint outlived its per-session limit")
	}
	// the oldest sessions were dropped whole
	o.Scope.Session, o.Args = "s0", []byte(`{"q":"9"}`)
	if d := l.Observe(o); d.Unchanged {
		t.Fatal("an evicted session still answered")
	}
}

func TestLedgerConcurrentObservers(t *testing.T) {
	l := NewLedger(LedgerLimits{Sessions: 8, PerSession: 16, MaxEntries: 64})
	h := testHandle(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				o := Delivered{Scope: Scope{Workspace: "ws", Session: fmt.Sprintf("s%d", (g+i)%12)}, Tool: "search",
					Args: []byte(fmt.Sprintf(`{"q":"%d"}`, i%20)), Rev: completeRev(), Bytes: 4096,
					Handle: h, ExpiresAt: time.Now().Add(time.Hour)}
				o.Digest[0] = byte(i % 3)
				l.Observe(o)
			}
		}(g)
	}
	wg.Wait()
	if s, e := l.Len(); s > 8 || e > 64 {
		t.Fatalf("over the limits: %d sessions, %d entries", s, e)
	}
}

// generatedAt matches a generated_at member.
var generatedAt = regexp.MustCompile(`"generated_at"\s*:\s*"[^"]*"`)

// TestLedgerNoticeSavingsOnToolFixtures measures what the repeat guard saves on the
// recorded results of xMustard's tools: a repeated identical call is delivered as a
// notice instead of the result. Each first call is given a handle, as a caller that
// retains guarded results would (a result is replaced only when its original is
// recoverable).
func TestLedgerNoticeSavingsOnToolFixtures(t *testing.T) {
	var delivered, saved int
	for name, f := range resultFixtures(t) {
		pol := ToolPolicyFor(f.tool)
		if pol.SideEffecting {
			continue
		}
		l := newTestLedger(LedgerLimits{})
		o := Delivered{Scope: Scope{Workspace: "ws", Session: "s"}, Tool: f.tool, Args: []byte(`{"q":"fixture"}`),
			Rev: completeRev(), CallID: "toolu_first", Digest: ContentDigestOf(f.tool, f.body), Bytes: len(f.body),
			Handle: testHandle(t), ExpiresAt: ledgerNow.Add(24 * time.Hour)}
		l.Observe(o)
		// the repeat carries a new generated_at (and, for search, new work counters)
		again := o
		again.CallID = "toolu_again"
		later := generatedAt.ReplaceAll(f.body, []byte(`"generated_at": "2027-01-01T00:00:00Z"`))
		if bytes.Equal(later, f.body) && bytes.Contains(f.body, []byte("generated_at")) {
			t.Fatalf("%s: generated_at not rewritten", name)
		}
		again.Digest = ContentDigestOf(f.tool, later)
		d := l.Observe(again)
		switch {
		case len(f.body) < DefaultLedgerLimits().MinBytes:
			if d.Unchanged {
				t.Errorf("%s: a %d-byte result was replaced by a notice", name, len(f.body))
			}
			continue
		case !d.Unchanged:
			t.Errorf("%s: an identical repeat was not recognized: %+v", name, d)
			continue
		}
		note := d.Notice("ws")
		delivered += len(f.body)
		saved += len(f.body) - len(note)
		t.Logf("%s: %d-byte result -> %d-byte notice", name, len(f.body), len(note))
	}
	t.Logf("repeats of the fixtures: %d bytes delivered as notices of %d bytes (-%.0f%%)",
		delivered, delivered-saved, 100*float64(saved)/float64(max(delivered, 1)))
}
