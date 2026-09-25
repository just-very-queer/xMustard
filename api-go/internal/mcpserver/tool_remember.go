package mcpserver

import "encoding/json"

var rememberTool = &Tool{
	Name:        "remember",
	Description: "Propose a durable memory (fact/decision/gotcha); pending until enough distinct agents verify it (open mode: promoted at once as self_asserted_open_mode). Pass content; optional title, paths (comma-separated files it is about, so recall flags it stale when they change).",
	Args: []Arg{
		workspaceArg,
		{Name: "content", Type: typeString, Required: true, Desc: "the memory text to propose"},
		{Name: "title", Type: typeString, Desc: "short title"},
		{Name: "paths", Type: typeString, Desc: "comma-separated repo-relative files the memory is about"},
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
		payload := map[string]any{"content": a["content"]}
		if a["title"] != "" {
			payload["title"] = a["title"]
		}
		if a["paths"] != "" {
			payload["paths"] = splitCSV(a["paths"])
		}
		b, _ := json.Marshal(payload)
		return "POST", wsPath(a, "/context"), string(b)
	},
}
