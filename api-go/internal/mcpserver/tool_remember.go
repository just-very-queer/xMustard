package mcpserver

import (
	"encoding/json"
	"strconv"
)

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
			Desc: "default propose; edit/retire/restore act on entry_id"},
		{Name: "entry_id", Type: typeString, Desc: "entry to edit, retire or restore"},
		{Name: "base_revision", Type: typeInteger, Min: 1, Max: maxRevision, Desc: "edit: revision read (compare-and-set)"},
		{Name: "reason", Type: typeString, MaxLen: maxVerifyNote, Desc: "required by edit, retire, restore"},
		{Name: "old_string", Type: typeString, Desc: "edit: exact text to replace (once)"},
		{Name: "new_string", Type: typeString, Desc: "edit: replacement; alone, appended"},
		{Name: "description", Type: typeString, Desc: "edit: new description"},
		{Name: "supersedes", Type: typeString, List: true, MaxLen: 1024, Desc: "entries replaced once this is promoted"},
		{Name: "expires", Type: typeString, Desc: "hide after this UTC date (YYYY-MM-DD, inclusive)"},
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
		for _, k := range []string{"content", "title", "op", "entry_id", "reason", "old_string", "new_string", "description", "expires"} {
			if a[k] != "" {
				payload[k] = a[k]
			}
		}
		for _, k := range []string{"paths", "supersedes"} {
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
