package workspaceops

import "testing"

// A workspace-scoped (per-worker) token is confined to its workspaces; an unscoped
// token is unrestricted; rotation preserves the scope (XM-NEW-022).
func TestWorkspaceScopedToken(t *testing.T) {
	dir := t.TempDir()
	raw, err := MintScopedToken(dir, "worker-a", "agent", 0, []string{"ws-A"})
	if err != nil {
		t.Fatalf("mint scoped: %v", err)
	}
	p := ResolveToken(dir, raw)
	if p == nil || !p.AllowsWorkspace("ws-A") {
		t.Fatalf("scoped token must allow its workspace: %+v", p)
	}
	if p.AllowsWorkspace("ws-B") {
		t.Fatal("scoped token must deny a workspace outside its scope")
	}

	rawU, _ := MintToken(dir, "admin-x", "admin")
	if pu := ResolveToken(dir, rawU); pu == nil || !pu.AllowsWorkspace("anything") {
		t.Fatal("an unscoped token must allow all workspaces (backward compatible)")
	}

	rot, err := RotateToken(dir, "worker-a", 0)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	pr := ResolveToken(dir, rot)
	if pr == nil || !pr.AllowsWorkspace("ws-A") || pr.AllowsWorkspace("ws-B") {
		t.Fatalf("rotation must preserve the workspace scope: %+v", pr)
	}
	if ResolveToken(dir, raw) != nil {
		t.Fatal("the pre-rotation secret must be invalid")
	}
}
