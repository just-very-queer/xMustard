package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// toolsListBudgetPath holds the tools/list byte caps and the role profiles they are
// measured for. cmd/xmustard-api reads the same file to keep the profiles equal to
// what its route gates let each role call.
const toolsListBudgetPath = "testdata/tools_list_budget.json"

type toolsListBudget struct {
	RoleProfiles map[string][]string                  `json:"role_profiles"`
	Caps         map[string]map[string]map[string]int `json:"caps"` // schema -> version -> role -> bytes
}

func loadToolsListBudget(t *testing.T) toolsListBudget {
	t.Helper()
	raw, err := os.ReadFile(toolsListBudgetPath)
	if err != nil {
		t.Fatal(err)
	}
	var b toolsListBudget
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// profileAPI answers whoami with a role profile's usable tools, as the API does.
func profileAPI(tools []string) *fakeAPI {
	return &fakeAPI{handle: func(r Request) *APIResponse {
		if r.Path != "/api/auth/whoami" {
			return nil
		}
		b, _ := json.Marshal(map[string]any{"id": "p", "tools": tools})
		return &APIResponse{Status: 200, Body: string(b)}
	}}
}

// listedBytes is the tools/list result exactly as a session answers it: filtered by
// the caller's posture and encoded as the stdio transport encodes it.
func listedBytes(t *testing.T, profile SchemaProfile, version string, tools []string) ([]byte, []string) {
	t.Helper()
	s := newSession(t, profileAPI(tools), Options{Schema: profile}, nil, version)
	res, rerr := s.Handle(context.Background(), "tools/list", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return b, listedNames(res)
}

// tools/list is in every session's prompt prefix. Its size is a tested contract per
// schema profile, protocol version and role profile (WS-54): an addition that does
// not fit must be advertised less (an Advanced argument documented at DocsURI), not
// fit by raising a cap.
func TestToolsListStaysWithinByteCaps(t *testing.T) {
	budget := loadToolsListBudget(t)
	for _, profile := range []SchemaProfile{SchemaLean, SchemaFull} {
		for _, version := range supportedVersions {
			caps := budget.Caps[string(profile)][version]
			if len(caps) != len(budget.RoleProfiles) {
				t.Fatalf("%s %s: caps for %d role profiles, want one per profile (%d)", profile, version, len(caps), len(budget.RoleProfiles))
			}
			for role, tools := range budget.RoleProfiles {
				limit, ok := caps[role]
				if !ok {
					t.Fatalf("%s %s: no cap for role profile %s", profile, version, role)
				}
				b, names := listedBytes(t, profile, version, tools)
				if !slices.Equal(names, tools) {
					t.Fatalf("%s %s %s: listed %v, want %v", profile, version, role, names, tools)
				}
				if len(b) > limit {
					t.Errorf("%s tools/list for %s at %s is %d bytes, over its %d-byte cap by %d: declare new arguments Advanced "+
						"(documented at %s) or trim; do not raise the cap", profile, role, version, len(b), limit, len(b)-limit, DocsURI)
				} else if len(b) < limit {
					t.Logf("%s %s %s: %d bytes, %d under the cap; lower the cap to lock the saving in", profile, version, role, len(b), limit-len(b))
				}
			}
		}
	}
}

// The role profiles cover every tool, and the full-schema caps sit above the lean
// ones only by what the advanced arguments cost.
func TestToolsListBudgetProfilesAreComplete(t *testing.T) {
	budget := loadToolsListBudget(t)
	all := budget.RoleProfiles["agent"]
	var names []string
	for _, tl := range Tools() {
		names = append(names, tl.Name)
	}
	if !slices.Equal(all, names) {
		t.Fatalf("the agent profile lists every tool in order: %v, want %v", all, names)
	}
	for role, tools := range budget.RoleProfiles {
		for _, name := range tools {
			tl, ok := ToolByName(name)
			if !ok {
				t.Fatalf("profile %s names unknown tool %s", role, name)
			}
			if role == "reader" && !tl.Annotations.ReadOnly {
				t.Fatalf("the reader profile lists write tool %s", name)
			}
		}
	}
	for _, version := range supportedVersions {
		for role := range budget.RoleProfiles {
			lean, full := budget.Caps["lean"][version][role], budget.Caps["full"][version][role]
			if full < lean {
				t.Fatalf("%s %s: full cap %d below lean cap %d", version, role, full, lean)
			}
		}
	}
}

// Advanced arguments are never listed in the lean profile and always in the full one;
// both profiles accept them on tools/call.
func TestAdvancedArgumentsAreAcceptedNotAdvertised(t *testing.T) {
	for _, profile := range []SchemaProfile{SchemaLean, SchemaFull} {
		s := newSession(t, &fakeAPI{}, Options{Schema: profile}, nil, LatestProtocolVersion)
		for _, entry := range s.ToolsList()["tools"].([]map[string]any) {
			tl, _ := ToolByName(entry["name"].(string))
			props := entry["inputSchema"].(map[string]any)["properties"].(map[string]any)
			for _, a := range tl.Advanced {
				_, listed := props[a.Name]
				if listed != (profile == SchemaFull) {
					t.Fatalf("%s: %s.%s listed=%v", profile, tl.Name, a.Name, listed)
				}
			}
		}
		api := &fakeAPI{}
		s = newSession(t, api, Options{Schema: profile}, nil, LatestProtocolVersion)
		res, rerr := call(t, s, "ground", map[string]any{"workspace_id": "ws", "sections": []any{"runs", " index"}, "max_chars": 3000})
		if rerr != nil || res["isError"] == true {
			t.Fatalf("%s: advanced arguments refused: %v %v", profile, rerr, res)
		}
		if p := api.lastTool(t).Path; p != "/api/workspaces/ws/session-grounding?sections=runs%2Cindex&max_chars=3000" {
			t.Fatalf("%s: ground forwarded %q", profile, p)
		}
		// the MCP tool always budgets: over HTTP the budget is opt-in, so an omitted
		// max_chars is sent as the documented default
		_, _ = call(t, s, "ground", map[string]any{"workspace_id": "ws", "sections": "runs"})
		if p := api.lastTool(t).Path; p != "/api/workspaces/ws/session-grounding?sections=runs&max_chars=6000" {
			t.Fatalf("%s: ground without max_chars forwarded %q", profile, p)
		}
	}
	// the full profile's extra schema still uses only portable keywords
	for _, tl := range Tools() {
		if len(tl.Advanced) == 0 {
			continue
		}
		in := tl.inputSchema(SchemaFull)
		if in["additionalProperties"] != false {
			t.Fatalf("%s full schema must stay closed", tl.Name)
		}
		for _, a := range tl.Advanced {
			p := in["properties"].(map[string]any)[a.Name].(map[string]any)
			for k := range p {
				if !slices.Contains([]string{"type", "description", "enum", "minimum", "maximum", "maxLength"}, k) {
					t.Fatalf("%s.%s uses keyword %s", tl.Name, a.Name, k)
				}
			}
			if a.List && p["enum"] != nil {
				t.Fatalf("%s.%s is a list; an enum on its string would reject valid lists", tl.Name, a.Name)
			}
		}
	}
}

// Advanced arguments are validated like listed ones: out of range is rejected, never
// clamped, and a list element outside the enum names itself.
func TestAdvancedArgumentsAreValidated(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, LatestProtocolVersion)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"max_chars": 1999}, "not clamped"},
		{map[string]any{"max_chars": 65537}, "not clamped"},
		{map[string]any{"max_chars": "6000"}, "must be an integer"},
		{map[string]any{"sections": "runs,bogus"}, `"bogus"`},
		{map[string]any{"sections": []any{"runs", 3}}, "array of strings"},
		{map[string]any{"sections": true}, "array of strings"},
		{map[string]any{"sections": strings.Repeat("runs,", 60)}, "at most 256"},
	} {
		_, rerr := call(t, s, "ground", tc.args)
		if rerr == nil || rerr.Code != CodeInvalidParams || !strings.Contains(rerr.Message, tc.want) {
			t.Fatalf("ground %v: want -32602 mentioning %q, got %v", tc.args, tc.want, rerr)
		}
	}
	// blank and duplicate elements are canonicalized, not rejected
	tl, _ := ToolByName("ground")
	args, _, rerr := BuildArgs(tl, map[string]any{"sections": " runs,,memory "})
	if rerr != nil || args["sections"] != "runs,memory" {
		t.Fatalf("canonical sections: %v %v", args, rerr)
	}
}

// The docs resource is listed, readable with or without an evidence provider, and
// documents every advanced argument, the ground sections and every hidden alias.
func TestDocsResourceDocumentsUnlistedArguments(t *testing.T) {
	s := newSession(t, &fakeAPI{}, Options{}, nil, LatestProtocolVersion)
	res, rerr := s.Handle(context.Background(), "resources/list", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	list := res.(map[string]any)["resources"].([]map[string]any)
	if len(list) == 0 || list[0]["uri"] != DocsURI {
		t.Fatalf("docs resource not listed: %v", list)
	}
	res, rerr = s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+DocsURI+`"}`))
	if rerr != nil {
		t.Fatal(rerr)
	}
	contents := res.(map[string]any)["contents"].([]map[string]any)
	text := contents[0]["text"].(string)
	if contents[0]["mimeType"] != "text/markdown" {
		t.Fatalf("docs mime type: %v", contents[0]["mimeType"])
	}
	for _, tl := range Tools() {
		for _, a := range tl.Advanced {
			if !strings.Contains(text, "`"+a.Name+"`") || !strings.Contains(text, a.Desc) {
				t.Errorf("docs miss %s.%s", tl.Name, a.Name)
			}
		}
		for alias, canon := range tl.Aliases {
			if !strings.Contains(text, tl.Name+": `"+alias+"` for `"+canon+"`") {
				t.Errorf("docs miss alias %s.%s", tl.Name, alias)
			}
		}
	}
	for _, want := range []string{"| runs |", "| summary (always) |", "trimmed", "counts", "omitted", "output_budget", "signals",
		"XMUSTARD_MCP_SCHEMA=full", "never clamped", "2000 to 65536"} {
		if !strings.Contains(text, want) {
			t.Errorf("docs must state %q", want)
		}
	}
	for _, uri := range []string{"xmustard://docs/nope", "xmustard://evidence/xm1.H"} {
		_, rerr = s.Handle(context.Background(), "resources/read", json.RawMessage(`{"uri":"`+uri+`"}`))
		if rerr == nil || rerr.Code != CodeResourceNotFound {
			t.Fatalf("%s without a provider: want -32002, got %v", uri, rerr)
		}
	}
	res, _ = s.Handle(context.Background(), "resources/templates/list", nil)
	if tpl := res.(map[string]any)["resourceTemplates"].([]map[string]any); len(tpl) != 0 {
		t.Fatalf("no templates without a provider: %v", tpl)
	}
}

func TestParseSchemaProfile(t *testing.T) {
	for in, want := range map[string]SchemaProfile{"": SchemaLean, " lean ": SchemaLean, "FULL": SchemaFull} {
		if got, err := ParseSchemaProfile(in); err != nil || got != want {
			t.Fatalf("ParseSchemaProfile(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseSchemaProfile("verbose"); err == nil {
		t.Fatal("an unknown schema profile must be an error")
	}
}
