package mcpserver

import (
	"encoding/json"
	"net/url"
	"strconv"
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
	// Lifecycle outcomes (WS-19A): listed only in the full schema profile.
	Advanced: []Arg{
		{Name: "outcome", Type: typeString, Enum: []string{"approve", "reject", "retract"}, Desc: "overrides approve"},
		{Name: "revision", Type: typeInteger, Min: 1, Max: maxRevision, Desc: "pending edit revision to vote on"},
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
		// the note travels in the body, like memory content (XM-NEW-018)
		payload := map[string]any{}
		for _, k := range []string{"note", "outcome"} {
			if a[k] != "" {
				payload[k] = a[k]
			}
		}
		if n, err := strconv.ParseInt(a["revision"], 10, 64); err == nil {
			payload["revision"] = n
		}
		body := ""
		if len(payload) > 0 {
			b, _ := json.Marshal(payload)
			body = string(b)
		}
		return "POST", wsPath(a, "/context/"+url.PathEscape(a["entry_id"])+"/verify") + "?approve=" + approve, body
	},
}
