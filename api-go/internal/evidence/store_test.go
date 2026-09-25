package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type fixedKey struct {
	mu  sync.Mutex
	key string
	ok  bool
}

func (f *fixedKey) get(context.Context) Identity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Identity{Key: f.key, Complete: f.ok}
}

func (f *fixedKey) set(k string, ok bool) { f.mu.Lock(); f.key, f.ok = k, ok; f.mu.Unlock() }

func testStore(t *testing.T, mutate func(*Limits)) (*Store, *time.Time) {
	t.Helper()
	l := DefaultLimits()
	l.ProjectionTarget = 4 << 10
	if mutate != nil {
		mutate(&l)
	}
	s := NewStore(t.TempDir(), l)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func capture(t *testing.T, s *Store, ws, actor string, raw []byte, key *fixedKey) (*Delivery, error) {
	t.Helper()
	sp, err := s.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Write(raw); err != nil {
		t.Fatal(err)
	}
	req := CaptureRequest{WorkspaceID: ws, RepoScope: "/repo", Actor: actor, AuthEnforced: actor != "",
		Issuer: "test", SessionID: "s1", CallID: "c1", Tool: "search", ArgsDigest: "d", Status: 200,
		ContentType: "application/json"}
	if key != nil {
		// like the delivery middleware: identity sampled before execution, then again
		// at capture
		before := key.get(context.Background())
		req.BeforeKey, req.RepoKey = &before, key.get
	}
	return s.Capture(context.Background(), sp, req)
}

// readAll pages through an original and returns its bytes.
func readAll(t *testing.T, s *Store, ws, actor, handle string, key *fixedKey) ([]byte, []*Page) {
	t.Helper()
	var out []byte
	var pages []*Page
	var off int64
	for {
		req := ReadRequest{WorkspaceID: ws, Handle: handle, Actor: actor, AuthEnforced: actor != "", Offset: off}
		if key != nil {
			req.RepoKey = key.get
		}
		p, err := s.Read(context.Background(), req)
		if err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
		b, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
		pages = append(pages, p)
		if p.EOF {
			return out, pages
		}
		off = p.NextOffset
	}
}

func manyResults(n, failAt int) []byte {
	type hit struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	hits := make([]hit, n)
	for i := range hits {
		hits[i] = hit{Path: fmt.Sprintf("pkg/file_%04d.go", i), Line: i + 1, Status: "pass", Detail: strings.Repeat("ok ", 20)}
	}
	hits[failAt].Status = "FAILED"
	hits[failAt].Detail = "panic: runtime error: index out of range [5] with length 3"
	b, _ := json.Marshal(map[string]any{"workspace_id": "ws", "total": n, "hits": hits})
	return b
}

func TestSmallResultPassesThroughWithoutRetention(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := []byte(`{"hits":[{"path":"a.go","line":3}]}`)
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reduced || d.Handle != "" || d.Projection != string(raw) {
		t.Fatalf("small result must pass unchanged without a handle: %+v", d)
	}
	if used, _ := s.Retained("ws"); used != 0 {
		t.Fatalf("nothing should be retained, got %d", used)
	}
}

// One failure among many passes must survive reduction, the projection stays valid
// JSON of the same shape, omissions name byte ranges, and the original reads back
// byte-exact through 64 KiB pages.
func TestOneFailureAmongManyPassesSurvivesAndOriginalRecovers(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(2000, 1717)
	key := &fixedKey{key: "k1", ok: true}
	d, err := capture(t, s, "ws", "", raw, key)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Reduced || d.Handle == "" || !strings.HasPrefix(d.Handle, HandlePrefix) || d.ResourceURI != ResourceScheme+d.Handle+"?workspace_id=ws" {
		t.Fatalf("expected reduced delivery with handle: %+v", d)
	}
	if d.ProjectedBytes > s.limits.ProjectionTarget {
		t.Fatalf("projection %d bytes over target %d", d.ProjectedBytes, s.limits.ProjectionTarget)
	}
	var proj struct {
		Total int              `json:"total"`
		Hits  []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(d.Projection), &proj); err != nil {
		t.Fatalf("projection is not valid JSON: %v\n%s", err, d.Projection)
	}
	found := false
	for _, h := range proj.Hits {
		if h["path"] == "pkg/file_1717.go" && strings.Contains(h["detail"].(string), "panic") {
			found = true
		}
	}
	if !found || proj.Total != 2000 {
		t.Fatalf("failure element or total lost in projection (%d hits kept)", len(proj.Hits))
	}
	if len(d.Omissions) == 0 || d.Omissions[0].Kind != "array_items" || d.Omissions[0].Pointer != "/hits" {
		t.Fatalf("omissions must name the reduced array: %+v", d.Omissions)
	}
	for _, o := range d.Omissions {
		if o.Start >= 0 && (o.End > int64(len(raw)) || o.Start > o.End) {
			t.Fatalf("bad omission range %+v", o)
		}
	}
	got, pages := readAll(t, s, "ws", "", d.Handle, key)
	if !bytes.Equal(got, raw) {
		t.Fatalf("paged original differs from captured bytes")
	}
	for _, p := range pages {
		if p.Length > DefaultPageSize || p.Encoding != "base64" || p.Stale || p.Freshness != "current" {
			t.Fatalf("bad page %+v", p)
		}
	}
	sum := sha256.Sum256(raw)
	if pages[0].RawSHA256 != hex.EncodeToString(sum[:]) || d.RawSHA256 != pages[0].RawSHA256 {
		t.Fatalf("raw hash mismatch")
	}
}

func TestReductionIsDeterministic(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(500, 7)
	a, _ := capture(t, s, "ws", "", raw, nil)
	b, _ := capture(t, s, "ws", "", raw, nil)
	if a.Projection != b.Projection || a.Handle == b.Handle {
		t.Fatalf("same input must give the same projection and distinct handles")
	}
}

func TestContradictionAndStackTraceSurviveTextReduction(t *testing.T) {
	s, _ := testStore(t, nil)
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "ok   test_%04d passed in 0.01s\n", i)
		if i == 1200 {
			b.WriteString("memory m1 says the API base is /api but config.go says /v2: contradiction\n")
		}
		if i == 2100 {
			b.WriteString("panic: nil map write\ngoroutine 7 [running]:\n\tmain.go:42 +0x1d\n\tserver.go:88 +0x2a\n")
		}
	}
	raw := []byte(b.String())
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(raw)
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain", Status: 500, IsError: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"contradiction", "panic: nil map write", "main.go:42", "server.go:88", "test_0000", "test_2999"} {
		if !strings.Contains(d.Projection, want) {
			t.Fatalf("text projection lost %q", want)
		}
	}
	if !d.IsError || d.Status != 500 || !strings.Contains(d.Projection, "lines omitted, bytes") {
		t.Fatalf("error status or omission markers lost: %+v", d)
	}
}

func TestLargeStringAndUnsupportedStructuredOutput(t *testing.T) {
	s, _ := testStore(t, nil)
	raw, _ := json.Marshal(map[string]any{"image": strings.Repeat("iVBORw0KGgo", 20000), "kind": "png"})
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(d.Projection), &m); err != nil || m["kind"] != "png" || !strings.Contains(m["image"], "bytes omitted") {
		t.Fatalf("large string must be truncated in valid JSON: %v %q", err, d.Projection[:min(200, len(d.Projection))])
	}
	// beyond the capture cap: an explicit size error, never a silent truncation
	s2, _ := testStore(t, func(l *Limits) { l.MaxOriginal = 1 << 20 })
	if _, err := capture(t, s2, "ws", "", bytes.Repeat([]byte("x"), 2<<20), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

// Pages are standard base64 bytes: a 4-byte UTF-8 character split across a page
// boundary and an invalid-UTF-8 original both round-trip exactly.
func TestPagesAreByteSafe(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.PageSize = 1000 })
	raw := append(bytes.Repeat([]byte("a"), 998), []byte("😀tail\n")...) // emoji spans bytes 998-1001
	for i := 0; i < 200; i++ {
		raw = append(raw, []byte("plain log line b b b b b b b b b\n")...)
	}
	// invalid UTF-8 after the 8 KiB sniff window: the text projection must not carry it
	// raw (a JSON transport would rewrite it), but the original pages it back exactly
	raw = append(raw, 0xff, 0xfe, 0x80, '\n')
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(raw)
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"})
	if err != nil || d.Handle == "" {
		t.Fatalf("capture: %v %+v", err, d)
	}
	wire, _ := json.Marshal(d)
	var back Delivery
	if err := json.Unmarshal(wire, &back); err != nil || back.Projection != d.Projection || !utf8.ValidString(d.Projection) {
		t.Fatalf("projection must be valid UTF-8 that survives the JSON wire exactly")
	}
	found := false
	for _, o := range d.Omissions {
		if o.Kind == "invalid_utf8" && o.End == int64(len(raw)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the invalid line must be listed as an invalid_utf8 omission: %+v", d.Omissions)
	}
	got, pages := readAll(t, s, "ws", "", d.Handle, nil)
	wire, _ = json.Marshal(pages[0])
	var p0 Page
	_ = json.Unmarshal(wire, &p0)
	if !bytes.Equal(got, raw) || pages[0].Length != 1000 || p0.Data != pages[0].Data {
		t.Fatalf("byte-exact paging failed (first page %d bytes)", pages[0].Length)
	}
}

func TestCrossWorkspacePrincipalAndTamperedHandlesDenied(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(400, 3)
	d, err := capture(t, s, "wsA", "alice", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := func(ws, actor, handle string, enforced bool) error {
		_, err := s.Read(context.Background(), ReadRequest{WorkspaceID: ws, Handle: handle, Actor: actor, AuthEnforced: enforced})
		return err
	}
	if err := read("wsA", "alice", d.Handle, true); err != nil {
		t.Fatalf("issuer read: %v", err)
	}
	if err := read("wsB", "alice", d.Handle, true); !errors.Is(err, ErrMissing) {
		t.Fatalf("cross-workspace read must be missing, got %v", err)
	}
	if err := read("wsA", "mallory", d.Handle, true); !errors.Is(err, ErrDenied) {
		t.Fatalf("other principal must be denied, got %v", err)
	}
	if err := read("wsA", "", d.Handle, false); !errors.Is(err, ErrDenied) {
		t.Fatalf("anonymous read of principal-bound evidence must be denied, got %v", err)
	}
	tampered := d.Handle[:len(d.Handle)-2] + "AA"
	if tampered == d.Handle {
		tampered = d.Handle[:len(d.Handle)-2] + "BB"
	}
	if err := read("wsA", "alice", tampered, true); !errors.Is(err, ErrMissing) {
		t.Fatalf("tampered handle must not resolve, got %v", err)
	}
	for _, bad := range []string{"xm1.short", "xm2." + d.Handle[4:], "../../etc/passwd", ""} {
		if err := read("wsA", "alice", bad, true); !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("malformed handle %q: want ErrInvalidHandle, got %v", bad, err)
		}
	}
	// unauthenticated issuance is workspace-scoped: readable in that workspace only
	// while auth is not enforced
	d2, _ := capture(t, s, "wsA", "", raw, nil)
	if err := read("wsA", "", d2.Handle, false); err != nil {
		t.Fatalf("workspace-scoped read: %v", err)
	}
	if err := read("wsA", "alice", d2.Handle, true); !errors.Is(err, ErrDenied) {
		t.Fatalf("anonymous-issued evidence under enforced auth must be denied, got %v", err)
	}
}

func TestQuotaFullRejectsNewRetentionWithoutEviction(t *testing.T) {
	raw := manyResults(400, 3)
	s, _ := testStore(t, func(l *Limits) { l.WorkspaceQuota = int64(len(raw))*2 + 10 })
	d1, err1 := capture(t, s, "ws", "", raw, nil)
	d2, err2 := capture(t, s, "ws", "", raw, nil)
	if err1 != nil || err2 != nil {
		t.Fatalf("two captures should fit: %v %v", err1, err2)
	}
	if _, err := capture(t, s, "ws", "", raw, nil); !errors.Is(err, ErrQuotaFull) {
		t.Fatalf("third capture must be rejected explicitly, got %v", err)
	}
	for _, h := range []string{d1.Handle, d2.Handle} {
		if got, _ := readAll(t, s, "ws", "", h, nil); !bytes.Equal(got, raw) {
			t.Fatalf("promised original was evicted")
		}
	}
	if _, err := capture(t, s, "other", "", raw, nil); err != nil {
		t.Fatalf("quota is per workspace: %v", err)
	}
}

// A projection passes only if its original reads until expiry and is denied after.
func TestExpiryAndRestart(t *testing.T) {
	s, now := testStore(t, func(l *Limits) { l.Retention = time.Hour })
	raw := manyResults(400, 3)
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	// restart: a new store over the same directory still serves and accounts it
	s2 := NewStore(filepath.Dir(s.root), s.limits)
	clock := *now
	s2.SetClock(func() time.Time { return clock })
	if got, _ := readAll(t, s2, "ws", "", d.Handle, nil); !bytes.Equal(got, raw) {
		t.Fatalf("original not readable after restart")
	}
	if used, _ := s2.Retained("ws"); used != int64(len(raw)) {
		t.Fatalf("restart must rebuild quota accounting, got %d", used)
	}
	clock = now.Add(59 * time.Minute)
	if _, err := s2.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); err != nil {
		t.Fatalf("read just before expiry: %v", err)
	}
	clock = now.Add(time.Hour)
	if _, err := s2.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); !errors.Is(err, ErrExpired) {
		t.Fatalf("read at expiry must be ErrExpired, got %v", err)
	}
	if _, err := s2.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); !errors.Is(err, ErrMissing) {
		t.Fatalf("expired original must stay unreadable (no substitution), got %v", err)
	}
	if used, _ := s2.Retained("ws"); used != 0 {
		t.Fatalf("expired bytes must leave the quota, got %d", used)
	}
}

func TestStaleLabelAfterRepositoryMutation(t *testing.T) {
	s, _ := testStore(t, nil)
	key := &fixedKey{key: "rev-A", ok: true}
	d, _ := capture(t, s, "ws", "", manyResults(400, 3), key)
	if d.CapturedKey != "rev-A" {
		t.Fatalf("captured key not recorded: %+v", d)
	}
	key.set("rev-B", true)
	p, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, RepoKey: key.get})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Stale || p.Freshness != "stale" || p.CapturedKey != "rev-A" || p.CurrentKey != "rev-B" {
		t.Fatalf("mutation must be labeled stale: %+v", p)
	}
	// an incomplete identity is never "current", even when the keys are equal
	key.set("rev-A", false)
	p, _ = s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, RepoKey: key.get})
	if !p.Stale || p.Freshness != "unknown" {
		t.Fatalf("incomplete identity must be unknown, got %+v", p)
	}
}

func TestRevokeAndWorkspaceRevocation(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(400, 3)
	d, _ := capture(t, s, "ws", "alice", raw, nil)
	if err := s.Revoke(ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "bob", AuthEnforced: true}); !errors.Is(err, ErrDenied) {
		t.Fatalf("other principal cannot revoke: %v", err)
	}
	if err := s.Revoke(ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "alice", AuthEnforced: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "alice", AuthEnforced: true}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked handle must be refused, got %v", err)
	}
	d2, _ := capture(t, s, "ws", "alice", raw, nil)
	if err := s.RevokeWorkspace("ws"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d2.Handle, Actor: "alice", AuthEnforced: true}); !errors.Is(err, ErrMissing) {
		t.Fatalf("workspace revocation must remove handles, got %v", err)
	}
}

func TestConcurrentSimilarCallsGetDistinctRecoverableHandles(t *testing.T) {
	s, _ := testStore(t, nil)
	var wg sync.WaitGroup
	handles := make([]string, 16)
	raws := make([][]byte, 16)
	for i := range handles {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raws[i] = manyResults(300+i, i)
			d, err := capture(t, s, "ws", "", raws[i], nil)
			if err != nil {
				t.Errorf("capture %d: %v", i, err)
				return
			}
			handles[i] = d.Handle
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, h := range handles {
		if seen[h] {
			t.Fatalf("duplicate handle")
		}
		seen[h] = true
		if got, _ := readAll(t, s, "ws", "", h, nil); !bytes.Equal(got, raws[i]) {
			t.Fatalf("handle %d returned another call's original", i)
		}
	}
}

func TestLimitsFromEnvOnlyLower(t *testing.T) {
	t.Setenv("XMUSTARD_EVIDENCE_PAGE_BYTES", "1048576")          // raise: ignored
	t.Setenv("XMUSTARD_EVIDENCE_PROJECTION_BYTES", "8192")       // lower: applied
	t.Setenv("XMUSTARD_EVIDENCE_WORKSPACE_QUOTA_BYTES", "-5")    // invalid: ignored
	t.Setenv("XMUSTARD_EVIDENCE_RETENTION_SECONDS", "999999999") // raise: ignored
	l := LimitsFromEnv()
	if l.PageSize != DefaultPageSize || l.ProjectionTarget != 8192 || l.WorkspaceQuota != DefaultWorkspaceQuota || l.Retention != DefaultRetention {
		t.Fatalf("limits: %+v", l)
	}
}

func TestAbandonedSpoolIsSwept(t *testing.T) {
	s, now := testStore(t, nil)
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write([]byte("partial"))
	name := sp.f.Name()
	_ = sp.f.Close()
	_ = now
	sp.store.mu.Lock()
	delete(sp.store.active, name) // simulate a crashed capture: no longer open
	sp.store.mu.Unlock()
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(name, old, old)
	if _, err := s.Retained("ws"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("abandoned spool not swept")
	}
}
