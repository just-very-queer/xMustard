package workspaceops

import (
	"crypto/sha256"
	"crypto/subtle"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseRoleSpec(t *testing.T) {
	ok := map[string]string{
		"":                           "agent",
		"agent":                      "agent",
		"readonly":                   "readonly",
		"verifier":                   "verifier",
		" Proposer + VERIFIER ":      "verifier+proposer",
		"verifier+proposer+verifier": "verifier+proposer",
		"reader+admin":               "admin",
		"human-approver+indexer":     "human-approver+indexer",
	}
	for in, want := range ok {
		got, err := ParseRoleSpec(in)
		if err != nil || got != want {
			t.Errorf("ParseRoleSpec(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"superuser", "proposer+root", "proposer,verifier", "+", "admin*"} {
		if _, err := ParseRoleSpec(bad); err == nil {
			t.Errorf("ParseRoleSpec(%q) must fail", bad)
		}
	}
}

func TestExpandRolesAndHas(t *testing.T) {
	cases := map[string][]string{
		"agent":          {RoleVerifier, RoleProposer, RoleReader},
		"readonly":       {RoleReader},
		"reader":         {RoleReader},
		"proposer":       {RoleProposer, RoleReader},
		"verifier":       {RoleVerifier, RoleReader},
		"indexer":        {RoleIndexer, RoleReader},
		"human-approver": {RoleHumanApprover, RoleReader},
		"admin":          {RoleAdmin, RoleHumanApprover, RoleIndexer, RoleVerifier, RoleProposer, RoleReader},
		"superuser":      {RoleReader}, // unknown: least privilege, never wider
	}
	for spec, want := range cases {
		if got := ExpandRoles(spec); !slices.Equal(got, want) {
			t.Errorf("ExpandRoles(%q) = %v, want %v", spec, got, want)
		}
	}
	proposer := &Principal{ID: "p", Role: "proposer"}
	if !proposer.Has(RoleProposer) || !proposer.Has(RoleReader) || proposer.Has(RoleVerifier) || proposer.Has(RoleAdmin) {
		t.Fatalf("proposer role set wrong: %v", proposer.RoleSet())
	}
	// the legacy "agent" gate is satisfied by proposer, and "readonly" by anyone
	if !proposer.Has("agent") || !proposer.Has("readonly") {
		t.Fatal("legacy gate names must map to proposer and reader")
	}
	admin := &Principal{ID: "a", Role: "admin"}
	for _, r := range Roles() {
		if !admin.Has(r) {
			t.Fatalf("admin must hold %s", r)
		}
	}
	if !(&Principal{Role: "readonly"}).ReadOnly() || !(&Principal{Role: "superuser"}).ReadOnly() || proposer.ReadOnly() {
		t.Fatal("ReadOnly must be true exactly for principals holding only reader")
	}
	// Roles, when set at resolve time, is authoritative
	if (&Principal{Role: "admin", Roles: []string{RoleReader}}).Has(RoleProposer) {
		t.Fatal("an explicit role set must not be widened by Role")
	}
}

func TestMintedRolesResolve(t *testing.T) {
	dir := t.TempDir()
	raw, err := MintScopedToken(dir, "checker", "proposer+verifier", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := ResolveToken(dir, raw)
	if p == nil || p.Role != "verifier+proposer" || !slices.Equal(p.Roles, []string{RoleVerifier, RoleProposer, RoleReader}) {
		t.Fatalf("resolved principal: %+v", p)
	}
	if _, err := MintToken(dir, "x", "root"); err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("an unknown role must be refused naming it: %v", err)
	}
	// rotation keeps the role spec
	rot, err := RotateToken(dir, "checker", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p := ResolveToken(dir, rot); p == nil || !p.Has(RoleVerifier) || p.Has(RoleIndexer) {
		t.Fatalf("rotated principal must keep its roles: %+v", p)
	}
	// the listing shows roles, never secrets
	for _, lp := range ListPrincipals(dir) {
		if lp.ID == "checker" && !slices.Contains(lp.Roles, RoleVerifier) {
			t.Fatalf("ListPrincipals must report roles: %+v", lp)
		}
	}
}

func TestEnvTokenRoles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_AUTH_TOKENS", "ci:indexer:ci-token-0123456789abcdefghij,typo:superuser:typo-token-0123456789abcdefgh")
	if p := ResolveToken(dir, "ci-token-0123456789abcdefghij"); p == nil || !p.Has(RoleIndexer) || p.Has(RoleProposer) {
		t.Fatalf("env indexer token: %+v", p)
	}
	// an unknown env role falls to the least privilege
	if p := ResolveToken(dir, "typo-token-0123456789abcdefgh"); p == nil || !p.ReadOnly() {
		t.Fatalf("an unknown env role must resolve read-only: %+v", p)
	}
	// the env value is re-read when it changes
	t.Setenv("XMUSTARD_AUTH_TOKENS", "")
	if ResolveToken(dir, "ci-token-0123456789abcdefghij") != nil {
		t.Fatal("a removed env token must stop resolving")
	}
}

// ageTokenFile moves the token file's mtime outside the racy window, so the cache
// may trust it (a real deployment reaches this state two seconds after a write).
func ageTokenFile(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(tokensPath(dir), old, old); err != nil {
		t.Fatal(err)
	}
}

func cached(dir string) bool {
	tokenCacheMu.Lock()
	defer tokenCacheMu.Unlock()
	_, ok := tokenCache[tokensPath(dir)]
	return ok
}

// PAR-SEC-07: the store is parsed once while unchanged, and mint, rotate and revoke
// drop the cached copy so the next request sees the change.
func TestTokenStoreCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	first, err := MintToken(dir, "alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	ageTokenFile(t, dir)
	if ResolveToken(dir, first) == nil {
		t.Fatal("minted token must resolve")
	}
	before := tokenStoreParses.Load()
	for i := 0; i < 5; i++ {
		if ResolveToken(dir, first) == nil || !HasAuthConfigured(dir) {
			t.Fatal("cached token must keep resolving")
		}
	}
	if n := tokenStoreParses.Load() - before; n != 0 {
		t.Fatalf("an unchanged store must not be re-parsed; parsed %d times", n)
	}

	second, err := MintToken(dir, "bob", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	if cached(dir) {
		t.Fatal("mint must invalidate the cache")
	}
	if p := ResolveToken(dir, second); p == nil || p.ID != "bob" {
		t.Fatal("a token minted after caching must resolve at once")
	}

	rotated, err := RotateToken(dir, "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	if cached(dir) {
		t.Fatal("rotate must invalidate the cache")
	}
	if ResolveToken(dir, first) != nil || ResolveToken(dir, rotated) == nil {
		t.Fatal("after rotation the old secret must fail and the new one resolve at once")
	}

	if err := RevokeToken(dir, "bob"); err != nil {
		t.Fatal(err)
	}
	if cached(dir) {
		t.Fatal("revoke must invalidate the cache")
	}
	if ResolveToken(dir, second) != nil {
		t.Fatal("a revoked token must stop resolving at once")
	}
}

// Another process (the mint-token CLI) rewriting the file is picked up by the
// identity/mtime/size check; a corrupt file fails closed until it is repaired.
func TestTokenStoreCacheSeesExternalChanges(t *testing.T) {
	dir := t.TempDir()
	raw, err := MintToken(dir, "alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	ageTokenFile(t, dir)
	if ResolveToken(dir, raw) == nil {
		t.Fatal("token must resolve")
	}
	recs, _ := loadTokenRecords(dir)
	if err := writeJSON(tokensPath(dir), recs[:0]); err != nil { // external revoke, cache untouched
		t.Fatal(err)
	}
	if ResolveToken(dir, raw) != nil {
		t.Fatal("an external rewrite must be seen without explicit invalidation")
	}
	if err := os.WriteFile(filepath.Join(dir, "agent_tokens.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, configured := ResolveAuth(dir, raw); p != nil || !configured {
		t.Fatalf("a corrupt store must fail closed: %v %v", p, configured)
	}
	fresh, err := MintToken(dir, "carol", "agent")
	if err == nil {
		t.Fatalf("mint over a corrupt store must fail, not overwrite it (got %q)", fresh)
	}
	if err := os.Remove(tokensPath(dir)); err != nil {
		t.Fatal(err)
	}
	if HasAuthConfigured(dir) {
		t.Fatal("a removed store means no file-backed tokens")
	}
}

// matchCredential compares every credential with crypto/subtle and does not stop at
// the first match, so the time taken does not reveal which credential matched.
func TestMatchCredentialComparesEveryCredential(t *testing.T) {
	if reflect.ValueOf(digestCompare).Pointer() != reflect.ValueOf(subtle.ConstantTimeCompare).Pointer() {
		t.Fatal("credential digests must be compared with subtle.ConstantTimeCompare")
	}
	creds := make([]credential, 6)
	for i := range creds {
		creds[i].digest = sha256.Sum256([]byte("token-" + itoa(i)))
	}
	creds[4].digest = creds[0].digest // a later duplicate must not win
	var compared []int
	orig := digestCompare
	digestCompare = func(x, y []byte) int {
		for i := range creds {
			if &creds[i].digest[0] == &x[0] {
				compared = append(compared, i)
			}
		}
		return orig(x, y)
	}
	t.Cleanup(func() { digestCompare = orig })

	for _, tc := range []struct {
		raw  string
		want int // index of the expected match, -1 for none
	}{{"token-0", 0}, {"token-5", 5}, {"no-such-token", -1}} {
		compared = nil
		got := matchCredential(creds, sha256.Sum256([]byte(tc.raw)))
		if !slices.Equal(compared, []int{0, 1, 2, 3, 4, 5}) {
			t.Fatalf("%s: every credential must be compared once, in order; compared %v", tc.raw, compared)
		}
		switch {
		case tc.want < 0 && got != nil:
			t.Fatalf("%s: unexpected match", tc.raw)
		case tc.want >= 0 && got != &creds[tc.want]:
			t.Fatalf("%s: want credential %d", tc.raw, tc.want)
		}
	}
}

// Resolution keeps env precedence, rejects an expired file token, and returns a copy
// of the cached principal.
func TestResolvePrecedenceExpiryAndCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_AUTH_TOKENS", "ops:admin:shared-secret-token-0123456789")
	for i := 0; i < 4; i++ {
		if _, err := MintToken(dir, "agent-"+itoa(i), "agent"); err != nil {
			t.Fatal(err)
		}
	}
	if p := ResolveToken(dir, "shared-secret-token-0123456789"); p == nil || p.ID != "ops" || !p.Has(RoleAdmin) {
		t.Fatalf("env token must resolve with precedence: %+v", p)
	}
	raw, _ := MintTokenTTL(dir, "short", "agent", 1)
	recs, _ := loadTokenRecords(dir)
	for i := range recs {
		if recs[i].ID == "short" {
			recs[i].ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		}
	}
	if err := writeJSON(tokensPath(dir), recs); err != nil {
		t.Fatal(err)
	}
	if ResolveToken(dir, raw) != nil {
		t.Fatal("an expired token must not resolve")
	}
	// a returned principal is a copy: mutating it can't widen the cached one
	p := ResolveToken(dir, "shared-secret-token-0123456789")
	p.Roles[0] = "tampered"
	if q := ResolveToken(dir, "shared-secret-token-0123456789"); q.Roles[0] != RoleAdmin {
		t.Fatalf("cached principal was mutated through a resolved copy: %v", q.Roles)
	}
}
