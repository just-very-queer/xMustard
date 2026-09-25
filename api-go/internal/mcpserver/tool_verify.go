package mcpserver

import (
	"encoding/json"
	"net/url"
)

// maxVerifyNote bounds the reason stored with a vote.
const maxVerifyNote = 1000

var verifyTool = &Tool{
	Name:        "verify",
	Description: "Verify (approve/reject) a peer's proposed memory; it promotes once enough DISTINCT agents approve. Your identity is your auth token; approve defaults true.",
	Args: []Arg{
		workspaceArg,
		{Name: "entry_id", Type: typeString, Required: true, Desc: "the id of the memory entry"},
		{Name: "approve", Type: typeBoolean, Desc: "approve (default true) or reject"},
		{Name: "note", Type: typeString, MaxLen: maxVerifyNote, Desc: "reason for the verdict, stored with the vote"},
	},
	// A reject can demote a promoted memory (destructive); a repeated vote replaces the
	// caller's own prior verdict, so the call is idempotent.
	Annotations: Annotations{Title: "Verify a memory", Destructive: true, Idempotent: true},
	Output: map[string]string{
		"id": "string", "workspace_id": "string", "status": "string", "promoted": "boolean",
		"verification_mode": "string", "required_verifications": "integer",
	},
	Build: func(a map[string]string) (string, string, string) {
		approve := "true"
		if a["approve"] == "false" {
			approve = "false"
		}
		body := ""
		if a["note"] != "" {
			// the note travels in the body, like memory content (XM-NEW-018)
			b, _ := json.Marshal(map[string]string{"note": a["note"]})
			body = string(b)
		}
		return "POST", wsPath(a, "/context/"+url.PathEscape(a["entry_id"])+"/verify") + "?approve=" + approve, body
	},
}
