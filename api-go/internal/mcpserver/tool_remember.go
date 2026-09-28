package mcpserver

import (
	"encoding/json"
	"strconv"
)

// memoryKinds mirrors workspaceops.MemoryKinds (a test keeps them equal).
var memoryKinds = []string{"project_knowledge", "decision", "constraint", "workflow", "procedure", "gotcha",
	"measurement", "convention", "handoff", "candidate", "maintenance"}

// maxRevision bounds revision arguments; real revisions are small counters.
const maxRevision = 1 << 30

var rememberTool = &Tool{
	Name:        "remember",
	Description: "Propose a durable memory (fact/decision/gotcha); pending until enough distinct agents verify it (open mode: promoted at once as self_asserted_open_mode). Pass content; optional title, paths (comma-separated files it is about, so recall flags it stale when they change).",
	Args: []Arg{
		workspaceArg,
		{Name: "content", Type: typeString, Desc: "the memory text to propose"},
		{Name: "title", Type: typeString, Desc: "short title"},
		{Name: "paths", Type: typeString, Desc: "comma-separated repo-relative files the memory is about"},
	},
	// Lifecycle writes (WS-19A): accepted on every call, listed only in the full schema
	// profile, documented at DocsURI.
	Advanced: []Arg{
		{Name: "op", Type: typeString, Enum: []string{"propose", "supersede", "edit", "retire", "restore"},
			Desc: "default propose"},
		{Name: "entry_id", Type: typeString, Desc: "target of edit/retire/restore"},
		{Name: "base_revision", Type: typeInteger, Min: 1, Max: maxRevision, Desc: "edit: revision read (CAS)"},
		{Name: "reason", Type: typeString, MaxLen: maxVerifyNote, Desc: "required unless proposing"},
		{Name: "old_string", Type: typeString, Desc: "edit: text to replace once"},
		{Name: "new_string", Type: typeString, Desc: "edit: replacement, or appended"},
		{Name: "description", Type: typeString, Desc: "edit: new description"},
		{Name: "supersedes", Type: typeString, List: true, MaxLen: 1024, Desc: "entries this replaces"},
		{Name: "expires", Type: typeString, Desc: "last UTC date served (YYYY-MM-DD)"},
		// Provenance (WS-19B): checked before the write.
		{Name: "evidence", Type: typeString, List: true, MaxLen: 1024, Desc: "evidence handles it rests on"},
		{Name: "run_id", Type: typeString, Desc: "run it came from"},
		// Classification (WS-20): what recall's kind, topic and tags filters match.
		{Name: "kind", Type: typeString, Enum: memoryKinds},
		{Name: "topic", Type: typeString, Desc: "a/b/c"},
		{Name: "tags", Type: typeString, List: true, MaxLen: 1024},
	},
	// Proposing is additive (never destroys state) and not idempotent: each call is a
	// new proposal.
	Annotations: Annotations{Title: "Propose a memory"},
	Output: map[string]string{
		"id": "string", "workspace_id": "string", "status": "string", "promoted": "boolean",
		"verification_mode": "string", "required_verifications": "integer",
	},
	Build: func(a map[string]string) (string, string, string) {
		// content goes in the JSON BODY, not the URL, so durable memory text is not
		// exposed in access logs / error strings (XM-NEW-018).
		payload := map[string]any{}
		for _, k := range []string{"content", "title", "op", "entry_id", "reason", "old_string", "new_string", "description", "expires", "run_id", "kind", "topic"} {
			if a[k] != "" {
				payload[k] = a[k]
			}
		}
		for _, k := range []string{"paths", "supersedes", "evidence", "tags"} {
			if a[k] != "" {
				payload[k] = splitCSV(a[k])
			}
		}
		if n, err := strconv.ParseInt(a["base_revision"], 10, 64); err == nil {
			payload["base_revision"] = n
		}
		b, _ := json.Marshal(payload)
		return "POST", wsPath(a, "/context"), string(b)
	},
}
