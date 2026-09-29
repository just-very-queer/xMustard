package mcpserver

import (
	"strconv"

	"xmustard/api-go/internal/groundbudget"
)

var groundTool = &Tool{
	Name:        "ground",
	Description: "Orient before acting: what changed, is stale, broken or blocked since the baseline, with index drift and contract breaks (changed signatures).",
	Args:        []Arg{workspaceArg},
	// The output budget (WS-54): accepted on every call, listed only in the full
	// schema profile, documented at DocsURI. The API applies it (groundbudget) when a
	// call sends either argument, and Build always sends max_chars.
	Advanced: []Arg{
		{Name: "sections", Type: typeString, List: true, Enum: groundbudget.SectionNames(), MaxLen: 256,
			Desc: "sections to return (summary always; default all)"},
		{Name: "max_chars", Type: typeInteger, Min: groundbudget.MinMaxChars, Max: groundbudget.MaxMaxChars,
			Desc: "result budget in characters (default 6000)"},
		// WS-31: what changed in served memory after a memory_revision ground returned.
		{Name: "since_revision", Type: typeInteger, Min: 0, Max: maxMemoryRevision, Desc: "memory_delta after this memory_revision"},
		// The caller's session (WS-33): its snapshot is restored after a compaction.
		{Name: "session_id", Type: typeString, MaxLen: maxSessionKey},
		// WS-34: the task, to bootstrap a new session with relevant past sessions.
		{Name: "query", Type: typeString, MaxLen: 512, Desc: "the task: adds relevant_sessions"},
	},
	Doc: groundbudget.Markdown() + "\n### Sessions\n\n`session_handoff` hands the prior session's state (its compaction " +
		"snapshot of at most 2 KB, unexpired evidence handles, proposals awaiting a distinct verifier, drift_stale) to " +
		"a new session in any client. `session_id` names your own session (an execution key such as `claude:<session>`): " +
		"when it has a ledger, `session_handoff` restores its snapshot instead (`restore: true`), which a client without " +
		"a compaction hook calls after compacting. `continuity` lists what recent sessions left open, `handoffs` the " +
		"latest handoff capsules with the paths changed since, and `last_sessions` the last sessions. `query` (the task, at most 512 bytes) adds `relevant_sessions`: " +
		"past sessions ranked against it. All of it is " +
		"derived from the session ledger: lightweight context, not verified memory.\n",
	Annotations: Annotations{Title: "Ground the session", ReadOnly: true},
	// Only members that are never null are declared. A count or blocked flag ground
	// cannot determine is null (and listed under "unknown"), and the portable schema
	// subset has no null type; the schema is open, so those members still arrive.
	Output:         map[string]string{"workspace_id": "string", "summary": "string", "generated_at": "string"},
	MaxResultChars: boundedResultChars,
	// Over plain HTTP the budget is opt-in, so clients that cannot send these arguments
	// (Pi's closed mirror, the UI) keep the unbudgeted result; the MCP tool always opts
	// in, with max_chars defaulting to groundbudget.DefaultMaxChars.
	Build: func(a map[string]string) (string, string, string) {
		maxChars := a["max_chars"]
		if maxChars == "" {
			maxChars = strconv.Itoa(groundbudget.DefaultMaxChars)
		}
		return "GET", query(wsPath(a, "/session-grounding"), "sections", a["sections"], "max_chars", maxChars,
			"since_revision", a["since_revision"], "session_id", a["session_id"], "query", a["query"]), ""
	},
}

// maxMemoryRevision bounds since_revision: the largest integer a JSON number carries
// exactly.
const maxMemoryRevision = 1<<53 - 1
