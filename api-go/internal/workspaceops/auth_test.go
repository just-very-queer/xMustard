package workspaceops

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMintResolveRevokeToken(t *testing.T) {
	dir := t.TempDir()
	if HasAuthConfigured(dir) {
		t.Fatal("fresh dir should have no auth configured")
	}
	raw, err := MintToken(dir, "agent-a", "agent")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !strings.HasPrefix(raw, "xmt_") || len(raw) < 20 {
		t.Fatalf("unexpected token format: %q", raw)
	}
	if !HasAuthConfigured(dir) {
		t.Fatal("auth should now be configured")
	}
	// the raw token is NOT stored — only its hash
	recs, _ := loadTokenRecords(dir)
	if len(recs) != 1 || recs[0].TokenSHA256 == raw || strings.Contains(recs[0].TokenSHA256, raw) {
		t.Fatalf("raw token must not be persisted: %+v", recs)
	}

	p := ResolveToken(dir, raw)
	if p == nil || p.ID != "agent-a" || p.Role != "agent" {
		t.Fatalf("resolve failed: %+v", p)
	}
	if ResolveToken(dir, "xmt_wrong") != nil {
		t.Fatal("unknown token must not resolve")
	}
	if ResolveToken(dir, "") != nil {
		t.Fatal("empty token must not resolve")
	}

	// re-mint replaces (old token invalid)
	raw2, _ := MintToken(dir, "agent-a", "admin")
	if ResolveToken(dir, raw) != nil {
		t.Fatal("old token should be invalid after re-mint")
	}
	if p := ResolveToken(dir, raw2); p == nil || p.Role != "admin" {
		t.Fatalf("re-minted admin token: %+v", p)
	}

	if err := RevokeToken(dir, "agent-a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if ResolveToken(dir, raw2) != nil {
		t.Fatal("revoked token must not resolve")
	}
}

func TestMintTokenValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := MintToken(dir, "../evil", "agent"); err == nil {
		t.Fatal("path-traversal id must be rejected")
	}
	if _, err := MintToken(dir, "ok", "superuser"); err == nil {
		t.Fatal("unknown role must be rejected")
	}
	if _, err := MintToken(dir, "ok", "admin"); err != nil {
		t.Fatalf("valid admin mint should succeed: %v", err)
	}
}

func TestConcurrentTokenStoreNoLostUpdate(t *testing.T) {
	dir := t.TempDir()
	// Concurrent mints of distinct ids must all persist (no lost-update). Without
	// tokenStoreMu the load-modify-write races and most records vanish.
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = MintToken(dir, "agent-"+itoa(i), "agent")
		}(i)
	}
	wg.Wait()
	recs, _ := loadTokenRecords(dir)
	if len(recs) != n {
		t.Fatalf("expected %d persisted tokens after concurrent mint, got %d", n, len(recs))
	}

	// A revoke racing a concurrent mint of a different id must not be lost.
	revoked, _ := MintToken(dir, "victim", "agent")
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go func() { defer wg2.Done(); _ = RevokeToken(dir, "victim") }()
	go func() { defer wg2.Done(); _, _ = MintToken(dir, "bystander", "agent") }()
	wg2.Wait()
	if ResolveToken(dir, revoked) != nil {
		t.Fatal("revoke must not be resurrected by a concurrent mint")
	}
}

func TestTokenExpiry(t *testing.T) {
	dir := t.TempDir()
	// a token that expired one second ago must not resolve.
	raw, err := MintTokenTTL(dir, "ephemeral", "agent", -1) // negative ttl is rejected
	if err == nil {
		t.Fatal("negative ttl must be rejected")
	}
	// mint with a 1s TTL, then force its ExpiresAt into the past on disk.
	raw, err = MintTokenTTL(dir, "ephemeral", "agent", 1)
	if err != nil {
		t.Fatalf("mint ttl: %v", err)
	}
	if ResolveToken(dir, raw) == nil {
		t.Fatal("freshly minted token within TTL should resolve")
	}
	recs, _ := loadTokenRecords(dir)
	for i := range recs {
		if recs[i].ID == "ephemeral" {
			recs[i].ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
		}
	}
	if err := writeJSON(tokensPath(dir), recs); err != nil {
		t.Fatalf("write: %v", err)
	}
	if ResolveToken(dir, raw) != nil {
		t.Fatal("expired token must be rejected")
	}
	// a non-expiring token (ttl 0) keeps resolving.
	raw0, _ := MintTokenTTL(dir, "forever", "agent", 0)
	if ResolveToken(dir, raw0) == nil {
		t.Fatal("non-expiring token should resolve")
	}
}

func TestRotateToken(t *testing.T) {
	dir := t.TempDir()
	old, err := MintToken(dir, "rot", "agent")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	fresh, err := RotateToken(dir, "rot", 0)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if fresh == old {
		t.Fatal("rotate must issue a different secret")
	}
	if ResolveToken(dir, old) != nil {
		t.Fatal("old secret must be invalid after rotation")
	}
	p := ResolveToken(dir, fresh)
	if p == nil || p.ID != "rot" || p.Role != "agent" {
		t.Fatalf("rotated token keeps id+role: %+v", p)
	}
	// rotating an unknown id fails.
	if _, err := RotateToken(dir, "nope", 0); err == nil {
		t.Fatal("rotating a non-existent id must fail")
	}
}

func TestAuthAuditLog(t *testing.T) {
	dir := t.TempDir()
	// reset the process-global denied-event throttle so this test is order-independent.
	deniedThrottleMu.Lock()
	lastDeniedWrite = time.Time{}
	deniedSuppressed = 0
	deniedThrottleMu.Unlock()

	RecordAuthAudit(dir, AuthAuditEvent{Action: "mint", Actor: "admin", TokenID: "a"})
	RecordAuthAudit(dir, AuthAuditEvent{Action: "denied", Actor: "anonymous", Detail: "no token"})
	RecordAuthAudit(dir, AuthAuditEvent{Action: ""}) // empty action is dropped
	events := ListAuthAudit(dir, 0)
	if len(events) != 2 {
		t.Fatalf("expected 2 recorded events, got %d: %+v", len(events), events)
	}
	// newest-first ordering, and the denial is captured.
	if events[0].Action != "denied" {
		t.Fatalf("newest event should be the denial: %+v", events[0])
	}
	if events[0].EventID == "" || events[0].CreatedAt == "" {
		t.Fatal("audit events must be stamped with id + time")
	}
}

func TestAuthAuditDeniedThrottle(t *testing.T) {
	dir := t.TempDir()
	deniedThrottleMu.Lock()
	lastDeniedWrite = time.Time{}
	deniedSuppressed = 0
	deniedThrottleMu.Unlock()

	// a burst of denied events within the interval persists only the first, but the
	// suppressed count is preserved on a later write so the flood stays visible.
	for i := 0; i < 50; i++ {
		RecordAuthAudit(dir, AuthAuditEvent{Action: "denied", Actor: "anonymous", Detail: "flood"})
	}
	events := ListAuthAudit(dir, 0)
	if len(events) != 1 {
		t.Fatalf("burst should persist exactly one denied event, got %d", len(events))
	}
	// a mint (not throttled) always lands.
	RecordAuthAudit(dir, AuthAuditEvent{Action: "mint", Actor: "admin", TokenID: "x"})
	if got := len(ListAuthAudit(dir, 0)); got != 2 {
		t.Fatalf("mint must not be throttled, got %d events", got)
	}
}

func TestAuthAuditFieldClip(t *testing.T) {
	dir := t.TempDir()
	deniedThrottleMu.Lock()
	lastDeniedWrite = time.Time{}
	deniedThrottleMu.Unlock()
	huge := strings.Repeat("A", 100000)
	RecordAuthAudit(dir, AuthAuditEvent{Action: "denied", Path: huge, RemoteAddr: huge})
	ev := ListAuthAudit(dir, 0)[0]
	if len(ev.Path) > authFieldMax+4 || len(ev.RemoteAddr) > authFieldMax+4 {
		t.Fatalf("attacker fields must be clipped: path=%d addr=%d", len(ev.Path), len(ev.RemoteAddr))
	}
}
