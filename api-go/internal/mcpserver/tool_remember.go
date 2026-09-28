package mcpserver

import (
	"encoding/json"
	"strconv"
	"strings"
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
		{Name: "op", Type: typeString, Enum: []string{"propose", "supersede", "edit", "retire", "restore", "tier"},
			Desc: "default propose"},
		{Name: "entry_id", Type: typeString, Desc: "target of edit/retire/restore/tier"},
		{Name: "base_revision", Type: typeInteger, Min: 1, Max: maxRevision, Desc: "edit: revision read (CAS)"},
		{Name: "reason", Type: typeString, MaxLen: maxVerifyNote, Desc: "required unless proposing"},
		{Name: "old_string", Type: typeString, Desc: "edit: text to replace once"},
		{Name: "new_string", Type: typeString, Desc: "edit: replacement, or appended"},
		{Name: "description", Type: typeString, Desc: "one-line description (deferred index)"},
		{Name: "supersedes", Type: typeString, List: true, MaxLen: 1024, Desc: "entries this replaces"},
		{Name: "expires", Type: typeString, Desc: "last UTC date served (YYYY-MM-DD)"},
		// Provenance (WS-19B): checked before the write.
		{Name: "evidence", Type: typeString, List: true, MaxLen: 1024, Desc: "evidence handles it rests on"},
		{Name: "run_id", Type: typeString, Desc: "run it came from"},
		// Classification (WS-20): what recall's kind, topic and tags filters match.
		{Name: "kind", Type: typeString, Enum: memoryKinds},
		{Name: "topic", Type: typeString, Desc: "a/b/c"},
		{Name: "tags", Type: typeString, List: true, MaxLen: 1024},
		// Structured claims and typed relations (WS-27).
		{Name: "claims", Type: typeString, MaxLen: 4096},
		{Name: "relations", Type: typeString, List: true, MaxLen: 1024},
		// Tiers (WS-31): core is pushed into every session; a protected field.
		{Name: "tier", Type: typeString, Enum: []string{"core", "deferred"}, Desc: "core needs a human approver"},
	},
	Doc: "A proposal is checked against the workspace before it is written. An exact restatement (after " +
		"lower-casing and collapsing whitespace) of a served memory is recorded as a corroboration of it: " +
		"`outcome` is `corroborated`, `duplicate_of` names it and `corroborations` counts the principals that " +
		"restated it; nothing new is created. It is created instead, with a warning saying why, when it names " +
		"`supersedes`, `claims` or `relations`, asks for another kind, topic, tags or expiry, declares a path " +
		"the memory does not have, or cites an untrusted capture the memory is clean of (dedupe never launders " +
		"quarantine). Otherwise `outcome` is `created`. Either way `possible_duplicates` lists active memories " +
		"it restates: " +
		"`exact`, `near` (Jaccard of character 3-grams >= 0.9) or `similar` (>= 0.5), with `similarity`; short " +
		"or low-entropy text only matches exactly. `possible_conflicts` are advisory: tier 1 `claim` (claims " +
		"on one subject exclude each other), tier 2 `contradiction` (a shared anchor and most words in common, " +
		"but different numbers, versions or commands, or only one negates or deprecates), tier 3 " +
		"`path_overlap`. `suggested_supersedes` names served memories it may replace (near duplicates and tier " +
		"1-2 conflicts): pass them as `supersedes`, or verify the other `duplicate_of` the served one (the " +
		"target must be served), which merges it once its gate's number of principals agree. `anchors` are " +
		"what the text is about: paths, symbols (identifiers resolved against the code index), identifiers, " +
		"commands, env and config keys, error " +
		"codes, URLs and quoted code placed on file lines, each with `fanout`, the memories you may read that " +
		"share it (served ones, and pending ones for the verifier and human-approver roles). Secrets are " +
		"redacted from claims as from the text. " +
		"`claims` states `subject|predicate|object` statements separated by `;`, the predicate one of " +
		"requires, forbids, runs_with, configured_as, pinned_to, deprecated_by, owned_by, replaces; subjects " +
		"compare case-insensitively, and recall filters on them with `subject` and `predicate`. `relations` " +
		"are `kind:entry_id` edges, the kind one of refines, contradicts, duplicates, references. A principal " +
		"may hold at most XMUSTARD_MEMORY_PENDING_QUOTA (500) pending proposals per workspace, and a " +
		"workspace at most XMUSTARD_MEMORY_ACTIVE_ENVELOPE (100000) active memories. " +
		"`tier`: core memory is pushed into every session (initialize instructions, SessionStart) once a human approver " +
		"approved its served revision; deferred memory (the default) appears there only as a title in the deferred index. " +
		"Choosing a tier, at proposal or with op=tier (entry_id, tier, reason), needs a human approver, and core must fit the " +
		"workspace's core budget. The workspace's write policy checks every write server-side: a refusal lists its " +
		"violations as {field, rule, limit, actual}.",
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
		for _, k := range []string{"content", "title", "op", "entry_id", "reason", "old_string", "new_string", "description", "expires", "run_id", "kind", "topic", "tier"} {
			if a[k] != "" {
				payload[k] = a[k]
			}
		}
		for _, k := range []string{"paths", "supersedes", "evidence", "tags"} {
			if a[k] != "" {
				payload[k] = splitCSV(a[k])
			}
		}
		if claims := parseClaims(a["claims"]); len(claims) > 0 {
			payload["claims"] = claims
		}
		if rels := parseRelations(a["relations"]); len(rels) > 0 {
			payload["relations"] = rels
		}
		if n, err := strconv.ParseInt(a["base_revision"], 10, 64); err == nil {
			payload["base_revision"] = n
		}
		b, _ := json.Marshal(payload)
		return "POST", wsPath(a, "/context"), string(b)
	},
}

// parseClaims reads "subject|predicate|object" claims separated by ";". A claim with
// another shape keeps its text as the subject, so the API refuses it by name.
func parseClaims(s string) []map[string]string {
	var out []map[string]string
	for _, c := range strings.Split(s, ";") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		parts := strings.SplitN(c, "|", 3)
		if len(parts) != 3 {
			parts = []string{c, "", ""}
		}
		out = append(out, map[string]string{"subject": strings.TrimSpace(parts[0]),
			"predicate": strings.TrimSpace(parts[1]), "object": strings.TrimSpace(parts[2])})
	}
	return out
}

// parseRelations reads "kind:entry_id" relations.
func parseRelations(s string) []map[string]string {
	var out []map[string]string
	for _, r := range splitCSV(s) {
		kind, target, _ := strings.Cut(r, ":")
		out = append(out, map[string]string{"kind": strings.TrimSpace(kind), "target": strings.TrimSpace(target)})
	}
	return out
}
