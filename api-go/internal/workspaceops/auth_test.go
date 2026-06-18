package workspaceops

import (
	"strings"
	"testing"
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
