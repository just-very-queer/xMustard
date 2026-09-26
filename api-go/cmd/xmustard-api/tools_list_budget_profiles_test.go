package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// The role profiles the tools/list byte caps are measured for are exactly what the
// route gates let each role call.
func TestToolsListBudgetProfilesMatchRoleGates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "mcpserver", "testdata", "tools_list_budget.json"))
	if err != nil {
		t.Fatal(err)
	}
	var budget struct {
		RoleProfiles map[string][]string `json:"role_profiles"`
	}
	if err := json.Unmarshal(raw, &budget); err != nil {
		t.Fatal(err)
	}
	core := &exposurePosture{}
	for role, want := range budget.RoleProfiles {
		c := callerView{Roles: workspaceops.ExpandRoles(role)}
		if got := usableTools(core, c); !slices.Equal(got, want) {
			t.Errorf("role %s can call %v; the tools/list budget profile says %v", role, got, want)
		}
	}
	if got := usableTools(core, callerView{Roles: workspaceops.ExpandRoles(workspaceops.RoleAdmin)}); !slices.Equal(got, budget.RoleProfiles["agent"]) {
		t.Errorf("admin sees %v, not the agent profile", got)
	}
	readOnly := &exposurePosture{ReadOnly: true}
	if got := usableTools(readOnly, callerView{Roles: workspaceops.ExpandRoles(workspaceops.RoleAdmin)}); !slices.Equal(got, budget.RoleProfiles["reader"]) {
		t.Errorf("read-only mode lists %v, not the reader profile", got)
	}
}
