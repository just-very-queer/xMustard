package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveAuth resolves the principal AND reports configured-ness in one read, with
// the same fail-closed-on-corrupt behavior as HasAuthConfigured (goal E).
func TestResolveAuthSingleReadCases(t *testing.T) {
	dir := t.TempDir()
	// unconfigured: no tokens
	if p, configured := ResolveAuth(dir, ""); p != nil || configured {
		t.Fatalf("empty store: want nil/false, got %v/%v", p, configured)
	}
	// configured: mint a token
	raw, err := MintToken(dir, "agent-a", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if p, configured := ResolveAuth(dir, raw); p == nil || p.ID != "agent-a" || !configured {
		t.Fatalf("valid token: want agent-a/true, got %v/%v", p, configured)
	}
	if p, configured := ResolveAuth(dir, "xmt_wrong"); p != nil || !configured {
		t.Fatalf("unknown token w/ store present: want nil/true, got %v/%v", p, configured)
	}
	// rotated: old secret invalid, configured stays true
	rot, _ := RotateToken(dir, "agent-a", 0)
	if p, _ := ResolveAuth(dir, raw); p != nil {
		t.Fatal("old secret must be invalid after rotation")
	}
	if p, _ := ResolveAuth(dir, rot); p == nil {
		t.Fatal("rotated secret must resolve")
	}
	// corrupt store: fail closed (configured=true) even though nothing resolves
	if err := os.WriteFile(filepath.Join(dir, "agent_tokens.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, configured := ResolveAuth(dir, rot); p != nil || !configured {
		t.Fatalf("corrupt store must fail closed: want nil/true, got %v/%v", p, configured)
	}
}
