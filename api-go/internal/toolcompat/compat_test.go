package toolcompat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func mustArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return args
}

func hasNorm(res Result, op, field, from string, applied bool) bool {
	for _, n := range res.Normalizations {
		if n.Op == op && n.Field == field && n.From == from && n.Applied == applied {
			return true
		}
	}
	return false
}

// The cases below are ported from the owner's cursor-bridge
// (research/cursor-bridge/bridge_orchestrator_test.go:152-399). Kind names are
// xMustard's: run_terminal_command_v2 → shell, glob_file_search → glob.
func TestPortedBridgeNormalization(t *testing.T) {
	type check func(t *testing.T, res Result)
	eq := func(field string, want any) check {
		return func(t *testing.T, res Result) {
			t.Helper()
			if got := res.Args[field]; !reflect.DeepEqual(got, want) {
				t.Fatalf("%s = %#v, want %#v (args %#v)", field, got, want, res.Args)
			}
		}
	}
	absent := func(field string) check {
		return func(t *testing.T, res Result) {
			t.Helper()
			if v, ok := res.Args[field]; ok {
				t.Fatalf("%s should be removed, got %#v", field, v)
			}
		}
	}
	valid := func(t *testing.T, res Result) {
		t.Helper()
		if res.Err != nil {
			t.Fatalf("unexpected validation error: %+v", res.Err)
		}
	}
	nested := func(list string, idx int, want map[string]any) check {
		return func(t *testing.T, res Result) {
			t.Helper()
			items, ok := res.Args[list].([]any)
			if !ok || len(items) <= idx {
				t.Fatalf("%s = %#v", list, res.Args[list])
			}
			if got := items[idx]; !reflect.DeepEqual(got, want) {
				t.Fatalf("%s[%d] = %#v, want %#v", list, idx, got, want)
			}
		}
	}

	cases := []struct {
		name   string
		kind   Kind
		args   string
		checks []check
	}{
		{"RepairsCommandAlias", KindShell, `{"cmd":"pwd","workdir":"/tmp/demo"}`,
			[]check{eq("command", "pwd"), eq("cwd", "/tmp/demo"), valid}},
		{"RepairsReadFilePathAliases", KindReadFile, `{"relative_workspace_path":"./docs/../README.md"}`,
			[]check{eq("path", "README.md"), valid}},
		{"RepairsMalformedGlobPattern", KindGlob, `{"glob_pattern":"**/.md","target_directory":"./docs/../docs"}`,
			[]check{eq("glob_pattern", "**/*.md"), eq("target_directory", "docs"), valid}},
		{"RepairsAwaitShellTaskIDAlias", KindShellStdin, `{"task_id":"sh-7","block_until_ms":1000}`,
			[]check{eq("session_id", "sh-7"), valid}},
		{"RepairsApplyPatchNestedAliases", KindApplyPatch, `{
			"path":"sample.txt",
			"stringReplacements":[{"oldString":"alpha","newString":"ALPHA","replaceAll":true}],
			"edits":[{"insertAfter":"beta","newString":"\ngamma"}]}`,
			[]check{
				nested("string_replacements", 0, map[string]any{"old_string": "alpha", "new_string": "ALPHA", "replace_all": true}),
				nested("edits", 0, map[string]any{"insert_after": "beta", "new_string": "\ngamma"}),
				absent("cell_source"), valid,
			}},
		{"AllowsUnifiedDiffWithoutPath", KindApplyPatch,
			`{"patch":"*** Begin Patch\n*** Update File: sample.txt\n@@\n-old\n+new\n*** End Patch\n"}`,
			[]check{valid}},
		{"RepairsEditNotebookAliases", KindApplyPatch, `{
			"targetNotebook":"notes.ipynb","cellIdx":1,"isNewCell":true,
			"cellLanguage":"markdown","oldString":"","newString":"# hello"}`,
			[]check{
				eq("path", "notes.ipynb"), eq("cell_index", float64(1)), eq("cell_type", "markdown"),
				eq("is_new_cell", true), eq("insert_cell_after", 0), eq("cell_source", "# hello"),
				eq("create", true), valid,
			}},
		{"RepairsAwaitTaskFromNestedTaskObject", KindAwaitTask, `{"task":{"id":"task-22"},"timeout_ms":"1500"}`,
			[]check{eq("task_id", "task-22"), eq("timeout_ms", 1500), valid}},
		{"RepairsMCPToolRef", KindCallMCPTool, `{"name":"read_file@filesystem"}`,
			[]check{eq("tool", "read_file"), eq("server", "filesystem"), valid}},
		{"DropsPlaceholderMCPServer", KindListMCPResources, `{"server":"dummy"}`,
			[]check{absent("server"), valid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Normalize(Spec{Kind: tc.kind}, mustArgs(t, tc.args))
			for _, c := range tc.checks {
				c(t, res)
			}
		})
	}
}

// Ported from cursor-bridge TestNormalizeAndValidateBridgeToolCallRejectsMissingRequiredArgs,
// with the structured error it now carries.
func TestPortedRejectsMissingRequiredArgs(t *testing.T) {
	res := Normalize(Spec{Kind: KindCallMCPTool}, mustArgs(t, `{"server":"filesystem"}`))
	if res.Err == nil {
		t.Fatal("expected validation error")
	}
	if res.Err.Message != "tool is required" || res.Err.Field != "tool" || res.Err.Code != CodeRequired || res.Err.Tool != "call_mcp_tool" {
		t.Fatalf("unexpected error: %+v", res.Err)
	}
	if len(res.Err.Signature) != 64 {
		t.Fatalf("signature should be sha256 hex, got %q", res.Err.Signature)
	}
}

func TestHarnessRepairs(t *testing.T) {
	cases := []struct {
		name  string
		kind  Kind
		args  string
		field string
		want  any
	}{
		{"server/tool split", KindCallMCPTool, `{"tool":"filesystem/read_file"}`, "server", "filesystem"},
		{"server/tool split keeps tool", KindCallMCPTool, `{"tool":"filesystem/read_file"}`, "tool", "read_file"},
		{"tool object", KindCallMCPTool, `{"tool":{"name":"list","server":"git"}}`, "server", "git"},
		{"bare extension glob", KindGlob, `{"pattern":".go"}`, "glob_pattern", "*.go"},
		{"leading slash glob", KindGlob, `{"glob_pattern":"/*.ts"}`, "glob_pattern", "*.ts"},
		{"file uri stripped", KindReadFile, `{"file_path":"file:///repo/src/a%20b.go"}`, "path", "/repo/src/a b.go"},
		{"remote uri is not a path", KindReadFile, `{"path":"https://example.com/a.go"}`, "path", "https://example.com/a.go"},
		{"claude grep glob", KindGrep, `{"pattern":"TODO","glob":".go"}`, "glob", "*.go"},
		{"opencode filePath", KindWriteFile, `{"filePath":"./x/../y.go","content":"package y"}`, "path", "y.go"},
		{"pi oldText", KindApplyPatch, `{"path":"a.go","oldText":"a","newText":"b"}`, "old_string", "a"},
		{"prompt server not name", KindGetMCPPrompt, `{"name":"summarize","server":"docs"}`, "prompt", "summarize"},
		{"resource object uri", KindReadMCPResource, `{"server":"docs","resource":{"uri":"docs://a"}}`, "uri", "docs://a"},
		{"model provider join", KindTask, `{"description":"d","model":"m1","provider":"acme"}`, "model", "acme/m1"},
		{"codex workdir", KindShell, `{"command":["bash","-lc","ls"],"workdir":"/r"}`, "cwd", "/r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Normalize(Spec{Kind: tc.kind}, mustArgs(t, tc.args))
			if res.Err != nil {
				t.Fatalf("unexpected error %+v", res.Err)
			}
			if got := res.Args[tc.field]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s = %#v, want %#v", tc.field, got, tc.want)
			}
		})
	}
}

func TestHarnessRequiredFields(t *testing.T) {
	cases := []struct {
		kind  Kind
		args  string
		field string
	}{
		{KindReadFile, `{}`, "path"},
		{KindWriteFile, `{"content":"x"}`, "path"},
		{KindApplyPatch, `{"old_string":"a"}`, "path"},
		{KindGrep, `{"path":"."}`, "pattern"},
		{KindShell, `{"command":""}`, "command"},
		{KindShell, `{"command":[]}`, "command"},
		{KindShellStdin, `{"text":"\n"}`, "session_id"},
		{KindWebFetch, `{"prompt":"x"}`, "url"},
		{KindReadMCPResource, `{"server":"docs"}`, "uri"},
		{KindGetMCPPrompt, `{"server":"docs"}`, "prompt"},
		{KindTask, `{"subagent_type":"general"}`, "description"},
		{KindAwaitTask, `{"timeout_ms":5}`, "task_id"},
		{KindSearchSymbols, `{}`, "query"},
		{KindSemanticSearch, `{}`, "query"},
	}
	for _, tc := range cases {
		res := Normalize(Spec{Kind: tc.kind}, mustArgs(t, tc.args))
		if res.Err == nil || res.Err.Field != tc.field || res.Err.Code != CodeRequired {
			t.Errorf("%s %s: want required %q, got %+v", tc.kind, tc.args, tc.field, res.Err)
		}
	}
	if res := Normalize(Spec{Kind: KindShell}, mustArgs(t, `{"command":["bash","-lc","ls"]}`)); res.Err != nil {
		t.Errorf("argv command should validate: %+v", res.Err)
	}
	if res := Normalize(Spec{Kind: KindApplyPatch}, mustArgs(t, `{"input":"*** Begin Patch"}`)); res.Err != nil {
		t.Errorf("codex apply_patch input should validate: %+v", res.Err)
	}
}

// Fixtures mirroring the nine MCP tools' argument surface at the time of
// writing (api-go/cmd/xmustard-mcp tools()). The MCP server owns the real table
// and builds its specs from it.
func xmSpecs() map[Kind]Spec {
	ws := Field{Name: "workspace_id", Type: TypeString, Required: true}
	s := func(k Kind, fields ...Field) Spec { return Spec{Kind: k, Fields: append([]Field{ws}, fields...)} }
	return map[Kind]Spec{
		KindGround: s(KindGround),
		KindRecall: s(KindRecall, Field{Name: "query", Type: TypeString}, Field{Name: "paths", Type: TypeString}),
		KindRemember: s(KindRemember, Field{Name: "content", Type: TypeString, Required: true},
			Field{Name: "title", Type: TypeString}, Field{Name: "paths", Type: TypeString}),
		KindVerify: s(KindVerify, Field{Name: "entry_id", Type: TypeString, Required: true}, Field{Name: "approve", Type: TypeBoolean}),
		KindSearch: s(KindSearch, Field{Name: "query", Type: TypeString, Required: true},
			Field{Name: "mode", Type: TypeString, Enum: []string{"hybrid", "pattern"}},
			Field{Name: "lang", Type: TypeString}, Field{Name: "seed", Type: TypeString}),
		KindExplain: s(KindExplain, Field{Name: "path", Type: TypeString, Required: true}),
		KindImpact: s(KindImpact, Field{Name: "symbol", Type: TypeString}, Field{Name: "from", Type: TypeString},
			Field{Name: "to", Type: TypeString}),
		KindDiagnostics: s(KindDiagnostics),
		KindWhyFailed:   s(KindWhyFailed, Field{Name: "run_id", Type: TypeString, Required: true}),
	}
}

func TestNineToolAliasesAndRepairs(t *testing.T) {
	specs := xmSpecs()
	cases := []struct {
		kind Kind
		args string
		want map[string]any
	}{
		{KindGround, `{"workspaceId":" ws1 "}`, map[string]any{"workspace_id": "ws1"}},
		{KindGround, `{"workspace":"ws1"}`, map[string]any{"workspace_id": "ws1"}},
		{KindRecall, `{"workspace_id":"w","q":"auth flow","files":["./a.go","b/../c.go"]}`,
			map[string]any{"workspace_id": "w", "query": "auth flow", "paths": "a.go,c.go"}},
		{KindRecall, `{"workspace_id":"w","path":"file:///r/x.go, ./y.go"}`,
			map[string]any{"workspace_id": "w", "paths": "/r/x.go,y.go"}},
		{KindSearch, `{"workspace_id":"w","text":"TODO","mode":" Hybrid","language":"go"}`,
			map[string]any{"workspace_id": "w", "query": "TODO", "mode": "hybrid", "lang": "go"}},
		{KindExplain, `{"workspace_id":"w","file":"file:///repo/src/../main.go"}`,
			map[string]any{"workspace_id": "w", "path": "/repo/main.go"}},
		{KindExplain, `{"workspace_id":"w","filePath":"./docs/"}`,
			map[string]any{"workspace_id": "w", "path": "docs"}},
		{KindImpact, `{"workspace_id":"w","symbolName":" Foo ","source":"A","dest":"B"}`,
			map[string]any{"workspace_id": "w", "symbol": "Foo", "from": "A", "to": "B"}},
		{KindWhyFailed, `{"workspace_id":"w","runId":"r-1"}`, map[string]any{"workspace_id": "w", "run_id": "r-1"}},
		{KindWhyFailed, `{"workspace_id":"w","run_id":42}`, map[string]any{"workspace_id": "w", "run_id": "42"}},
	}
	for _, tc := range cases {
		res := Normalize(specs[tc.kind], mustArgs(t, tc.args))
		if res.Err != nil {
			t.Errorf("%s %s: unexpected error %+v", tc.kind, tc.args, res.Err)
			continue
		}
		if !reflect.DeepEqual(res.Args, tc.want) {
			t.Errorf("%s %s: got %#v, want %#v", tc.kind, tc.args, res.Args, tc.want)
		}
		if !res.Applied() {
			t.Errorf("%s %s: normalizations should be recorded", tc.kind, tc.args)
		}
	}
}

func TestNineToolValidationNamesCanonicalField(t *testing.T) {
	specs := xmSpecs()
	cases := []struct {
		kind  Kind
		args  string
		field string
		code  string
	}{
		{KindSearch, `{"workspace_id":"w","q":"x","mode":"fuzzy"}`, "mode", CodeEnum},
		{KindSearch, `{"workspaceId":"w","mode":"hybrid"}`, "query", CodeRequired},
		{KindExplain, `{"workspace_id":"w","file":"  "}`, "path", CodeRequired},
		{KindGround, `{"workspace_id":"w","depth":2}`, "depth", CodeUnknown},
		{KindWhyFailed, `{"workspace_id":"w","run":true}`, "run_id", CodeType},
		{KindGround, `{}`, "workspace_id", CodeRequired},
	}
	for _, tc := range cases {
		res := Normalize(specs[tc.kind], mustArgs(t, tc.args))
		if res.Err == nil || res.Err.Field != tc.field || res.Err.Code != tc.code {
			t.Errorf("%s %s: want %s on %q, got %+v", tc.kind, tc.args, tc.code, tc.field, res.Err)
		}
		if res.Err != nil && !strings.Contains(res.Err.Message, tc.field) {
			t.Errorf("message %q does not name the canonical field %q", res.Err.Message, tc.field)
		}
	}
}

// Mutating arguments of remember and verify are never rewritten: every repair
// the non-mutating path would apply is only reported (Applied=false).
func TestMutatingArgumentsAreNeverAlteredSilently(t *testing.T) {
	specs := xmSpecs()

	content := "  keep\tthis exact text  \n"
	res := Normalize(specs[KindRemember], map[string]any{
		"workspace_id": "ws",
		"text":         content,
		"paths":        "file:///repo/a.go, ./b/../c.go",
		"title":        " t ",
	})
	if res.Err != nil {
		t.Fatalf("remember should validate: %+v", res.Err)
	}
	want := map[string]any{"workspace_id": "ws", "content": content, "paths": "file:///repo/a.go, ./b/../c.go", "title": " t "}
	if !reflect.DeepEqual(res.Args, want) {
		t.Fatalf("remember values changed:\n got %#v\nwant %#v", res.Args, want)
	}
	if !hasNorm(res, OpAlias, "content", "text", true) {
		t.Fatalf("key rename must be recorded: %+v", res.Normalizations)
	}
	if !hasNorm(res, OpRepair, "paths", "", false) {
		t.Fatalf("path repair must be reported, not applied: %+v", res.Normalizations)
	}
	for _, n := range res.Normalizations {
		if n.Applied && n.Op != OpAlias && n.Op != OpDrop {
			t.Fatalf("value normalization applied to a mutating tool: %+v", n)
		}
	}

	res = Normalize(specs[KindVerify], map[string]any{"workspace_id": "ws", "id": " e1 ", "approve": "false"})
	if got := res.Args["entry_id"]; got != " e1 " {
		t.Fatalf("entry_id rewritten: %#v", got)
	}
	if got := res.Args["approve"]; got != "false" {
		t.Fatalf("approve coerced on a mutating tool: %#v", got)
	}
	if res.Err == nil || res.Err.Field != "approve" || res.Err.Code != CodeType {
		t.Fatalf("string approve must fail validation, got %+v", res.Err)
	}
	if !strings.Contains(res.Err.Message, "mutating") {
		t.Fatalf("error should explain why it was not coerced: %q", res.Err.Message)
	}
	if !hasNorm(res, OpCoerce, "approve", "", false) || !hasNorm(res, OpRepair, "entry_id", "", false) {
		t.Fatalf("coercion and trim must be reported unapplied: %+v", res.Normalizations)
	}

	// The same arguments on a non-mutating spec are repaired.
	open := specs[KindVerify]
	open.Kind = "verify_preview"
	if res := Normalize(open, map[string]any{"workspace_id": "ws", "entry_id": " e1 ", "approve": "false"}); res.Err != nil ||
		res.Args["approve"] != false || res.Args["entry_id"] != "e1" {
		t.Fatalf("non-mutating spec should repair: %+v %#v", res.Err, res.Args)
	}

	// A caller-declared mutating spec gets the same protection.
	custom := Spec{Kind: KindExplain, Mutating: true, Fields: []Field{{Name: "path", Type: TypeString, Required: true}}}
	if res := Normalize(custom, map[string]any{"path": "./a/../b"}); res.Args["path"] != "./a/../b" {
		t.Fatalf("declared mutating spec was rewritten: %#v", res.Args)
	}
}

func TestDuplicateSpellings(t *testing.T) {
	specs := xmSpecs()
	res := Normalize(specs[KindRemember], map[string]any{"workspace_id": "w", "content": "a", "text": "b"})
	if res.Err == nil || res.Err.Code != CodeAmbiguous || res.Err.Field != "content" {
		t.Fatalf("conflicting spellings on a mutating tool must be rejected, got %+v", res.Err)
	}
	res = Normalize(specs[KindRemember], map[string]any{"workspace_id": "w", "content": "a", "text": "a"})
	if res.Err != nil || res.Args["content"] != "a" || !hasNorm(res, OpDrop, "content", "text", true) {
		t.Fatalf("equal duplicate should be dropped and recorded: %+v %+v", res.Err, res.Normalizations)
	}
	res = Normalize(specs[KindRecall], map[string]any{"workspace_id": "w", "query": "a", "q": "b"})
	if res.Err != nil || res.Args["query"] != "a" || !hasNorm(res, OpDrop, "query", "q", true) {
		t.Fatalf("canonical spelling should win on a read tool: %+v %#v", res.Err, res.Args)
	}
	res = Normalize(Spec{Kind: KindShell}, map[string]any{"command": "ls", "cmd": "rm -rf /"})
	if res.Args["command"] != "ls" || !hasNorm(res, OpDrop, "command", "cmd", true) {
		t.Fatalf("canonical key must win over alias: %#v", res.Args)
	}
}

func TestNormalizeDoesNotModifyInput(t *testing.T) {
	in := mustArgs(t, `{"stringReplacements":[{"oldString":"a"}],"file_path":"./x/../y","task":{"id":"t"}}`)
	before := deepCopyMap(in)
	Normalize(Spec{Kind: KindApplyPatch}, in)
	Normalize(Spec{Kind: KindAwaitTask}, in)
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("input mutated:\n got %#v\nwant %#v", in, before)
	}
}

func TestNormalizationsCarryNoValues(t *testing.T) {
	secret := "ghp_" + strings.Repeat("A1b2", 9)
	res := Normalize(Spec{Kind: KindShell}, map[string]any{"cmd": "curl -H 'Authorization: token " + secret + "'", "workdir": " /r "})
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "curl") {
		t.Fatalf("normalization record leaked a value: %s", b)
	}
}

func TestSignatureIsStableSHA256(t *testing.T) {
	a := Signature("search", map[string]any{"b": 1, "a": "x"}, "m")
	b := Signature("search", map[string]any{"a": "x", "b": 1}, "m")
	if a != b || len(a) != 64 {
		t.Fatalf("signature must be order-independent sha256 hex: %q %q", a, b)
	}
	if a == Signature("search", map[string]any{"a": "x", "b": 1}, "other") || a == Signature("recall", map[string]any{"a": "x", "b": 1}, "m") {
		t.Fatal("signature must change with tool or message")
	}
	r1 := Normalize(xmSpecs()[KindSearch], map[string]any{"workspace_id": "w", "mode": "x", "q": "a"})
	r2 := Normalize(xmSpecs()[KindSearch], map[string]any{"q": "a", "mode": "x", "workspace_id": "w"})
	if r1.Err == nil || r1.Err.Signature != r2.Err.Signature {
		t.Fatalf("repeated failures must share a signature: %+v %+v", r1.Err, r2.Err)
	}
}

func TestNormalizeJSON(t *testing.T) {
	out, res, err := NormalizeJSON(Spec{Kind: KindShell}, json.RawMessage(`{"cmd":"ls"}`))
	if err != nil || res.Err != nil || string(out) != `{"command":"ls"}` {
		t.Fatalf("got %s %+v %v", out, res.Err, err)
	}
	if out, res, err := NormalizeJSON(Spec{Kind: KindListDir}, nil); err != nil || res.Err != nil || string(out) != `{}` {
		t.Fatalf("empty args: %s %+v %v", out, res.Err, err)
	}
	if _, _, err := NormalizeJSON(Spec{Kind: KindShell}, json.RawMessage(`["ls"]`)); err == nil {
		t.Fatal("non-object arguments must be an error")
	}
}

func TestKindForName(t *testing.T) {
	cases := map[string]Kind{
		"FetchMcpResource":          KindReadMCPResource, // ported cursor-bridge case
		"Bash":                      KindShell,
		"run_terminal_cmd":          KindShell,
		"exec_command":              KindShell,
		"Read":                      KindReadFile,
		"ReadFile":                  KindReadFile,
		"Write":                     KindWriteFile,
		"Edit":                      KindApplyPatch,
		"MultiEdit":                 KindApplyPatch,
		"NotebookEdit":              KindApplyPatch,
		"apply_patch":               KindApplyPatch,
		"Grep":                      KindGrep,
		"Glob":                      KindGlob,
		"find":                      KindGlob,
		"LS":                        KindListDir,
		"WebFetch":                  KindWebFetch,
		"Task":                      KindTask,
		"use_mcp_tool":              KindCallMCPTool,
		"codebase_search":           KindSemanticSearch,
		"recall":                    KindRecall,
		"why_failed":                KindWhyFailed,
		"mcp__xmustard__remember":   KindRemember,
		"mcp__xmustard__why_failed": KindWhyFailed,
		"xmustard_search":           KindSearch,
		"verify@xmustard":           KindVerify,
		"xmustard/ground":           KindGround,
	}
	for name, want := range cases {
		if got, ok := KindForName(name); !ok || got != want {
			t.Errorf("KindForName(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"mcp__github__search", "totally_unknown", "", "other/recall"} {
		if got, ok := KindForName(name); ok {
			t.Errorf("KindForName(%q) = %q, want no match", name, got)
		}
	}
}

func TestAliasTable(t *testing.T) {
	if n := len(argAliases); n < 70 {
		t.Fatalf("alias table has %d entries, want ~70", n)
	}
	for token, canonical := range argAliases {
		if Token(token) != token {
			t.Errorf("alias key %q is not a token", token)
		}
		if CanonicalKey(canonical) != canonical {
			t.Errorf("alias %q → %q is not idempotent (→ %q)", token, canonical, CanonicalKey(canonical))
		}
	}
	for kind, table := range kindAliases {
		for token := range table {
			if Token(token) != token {
				t.Errorf("%s alias key %q is not a token", kind, token)
			}
		}
	}
	for _, key := range []string{"file_path", "filePath", "File Path", "file-path"} {
		if got := CanonicalKey(key); got != "path" {
			t.Errorf("CanonicalKey(%q) = %q", key, got)
		}
	}
}

func TestRepairGlobAndCleanLocalPath(t *testing.T) {
	globs := map[string]string{
		".go":          "*.go",
		"**/.md":       "**/*.md",
		"/*.ts":        "*.ts",
		"/.env":        "*.env",
		"src//**/*.go": "src/**/*.go",
		"/src/*.go":    "/src/*.go",
		"*.tar.gz":     "*.tar.gz",
	}
	for in, want := range globs {
		if got := RepairGlob(in); got != want {
			t.Errorf("RepairGlob(%q) = %q, want %q", in, got, want)
		}
	}
	paths := map[string]string{
		"file:///a/b/../c.go": "/a/c.go",
		"FILE:///a%20b":       "/a b",
		"./x/./y/":            "x/y",
		"s3://bucket/key":     "",
		"   ":                 "",
	}
	for in, want := range paths {
		if got := CleanLocalPath(in); got != want {
			t.Errorf("CleanLocalPath(%q) = %q, want %q", in, got, want)
		}
	}
}
