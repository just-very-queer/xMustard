package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Route gate table (PAR-SEC-02/03, PAR-RT-07, PAR-FRESH-06). Every HTTP route is
// classified here, by its exact ServeMux pattern, at registration time:
//
//   - Core: served in the core profile (the default). Everything else is platform
//     and answers 404 unless the API runs with XMUSTARD_PROFILE=platform.
//   - Role: the role the caller's token must hold (admin holds every role).
//   - Mutating: refused in read-only mode (XMUSTARD_READ_ONLY=1). Every non-GET
//     route mutates unless the entry says otherwise.
//   - Tool: the MCP tool the route serves, so a deployment can disable it
//     (XMUSTARD_DISABLED_TOOLS) and tools/list can hide what the caller cannot use.
//
// gatedMux panics when a route is registered without a row here, so a new route
// (core or platform) can never be served unclassified or silently dropped from the
// core profile. TestRouteGateTableMatchesRegisteredRoutes also fails on a row whose
// route no longer exists. docs/SECURITY.md renders this table.

type routeGate struct {
	Core bool
	Role string
	Tool string
	// ReadSafe marks a non-GET route read-only mode still serves: it changes no
	// shared state (capture or revocation of the caller's own tool output, policy
	// evaluation), or it only withdraws access (token revoke and rotate, so a leaked
	// token can be cut off without leaving read-only mode). A route that deletes
	// shared data is never ReadSafe.
	ReadSafe bool
	Note     string
}

const (
	roleReader   = workspaceops.RoleReader
	roleProposer = workspaceops.RoleProposer
	roleVerifier = workspaceops.RoleVerifier
	roleApprover = workspaceops.RoleHumanApprover
	roleIndexer  = workspaceops.RoleIndexer
	roleAdmin    = workspaceops.RoleAdmin
)

func coreGate(role, tool, note string) routeGate {
	return routeGate{Core: true, Role: role, Tool: tool, Note: note}
}

func platformGate(role, note string) routeGate { return routeGate{Role: role, Note: note} }

// hookGate is a hook route's row: core, and served in read-only mode, because a hook
// changes no shared state beyond the caller's own capture (the outcome a capture would
// record is skipped in read-only mode). A hook runs on behalf of an agent session, so
// every hook route needs the proposer role: a reader-only token drives no hook, and no
// non-GET route grants reader (TestNoWriteRouteGrantsReader).
func hookGate(note string) routeGate {
	return routeGate{Core: true, Role: roleProposer, ReadSafe: true, Note: "hook: " + note}
}

var routeGateTable = map[string]routeGate{
	// --- core: liveness, identity and token administration ---
	"/api/health":                               coreGate(roleReader, "", "public liveness and limits; the budget block needs an operator token while auth is enforced"),
	"GET /api/auth/whoami":                      coreGate(roleReader, "", "caller principal, roles and usable tools"),
	"GET /api/auth/principals":                  coreGate(roleAdmin, "", ""),
	"POST /api/auth/tokens":                     coreGate(roleAdmin, "", "mint"),
	"POST /api/auth/tokens/{id}/rotate":         {Core: true, Role: roleAdmin, ReadSafe: true, Note: "replaces the secret; the old one stops working"},
	"DELETE /api/auth/tokens/{id}":              {Core: true, Role: roleAdmin, ReadSafe: true, Note: "revoke; served in read-only mode to cut off a leaked token"},
	"GET /api/auth/audit":                       coreGate(roleAdmin, "", ""),
	"GET /api/workspaces":                       coreGate(roleReader, "", "filtered by token scope and workspace allowlist"),
	"POST /api/workspaces/load":                 coreGate(roleProposer, "", "workspace registration; below admin only a git work tree top level under XMUSTARD_REGISTER_ROOTS; id checked against the allowlist and token scope"),
	"POST /api/workspaces/{workspace_id}/index": coreGate(roleIndexer, "", "rebaseline the index; agents cannot reset it"),

	// --- core: the nine MCP tools ---
	"GET /api/workspaces/{workspace_id}/session-grounding":           coreGate(roleReader, "ground", ""),
	"GET /api/workspaces/{workspace_id}/context/active":              coreGate(roleReader, "recall", "scope=all needs admin; unverified text and history need verifier or human-approver"),
	"POST /api/workspaces/{workspace_id}/context":                    coreGate(roleProposer, "remember", "author is the principal"),
	"POST /api/workspaces/{workspace_id}/context/{entry_id}/verify":  coreGate(roleVerifier, "verify", "verifier is the principal"),
	"GET /api/workspaces/{workspace_id}/search":                      coreGate(roleReader, "search", ""),
	"GET /api/workspaces/{workspace_id}/explain-path":                coreGate(roleReader, "explain", ""),
	"GET /api/workspaces/{workspace_id}/changes/since-index":         coreGate(roleReader, "impact", ""),
	"GET /api/workspaces/{workspace_id}/diagnostics":                 coreGate(roleReader, "diagnostics", ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/why-failed":    coreGate(roleReader, "why_failed", ""),
	"PUT /api/workspaces/{workspace_id}/context/{entry_id}":          coreGate(roleProposer, "", "memory edit; author or admin only"),
	"DELETE /api/workspaces/{workspace_id}/context/{entry_id}":       coreGate(roleApprover, "", "retract; purge=true deletes the text and keeps a digest tombstone"),
	"POST /api/workspaces/{workspace_id}/context/{entry_id}/restore": coreGate(roleApprover, "", "restore a retired, retracted, superseded or expired entry"),
	"GET /api/workspaces/{workspace_id}/context":                     coreGate(roleAdmin, "", "full memory history"),

	// --- core: run-independent outcomes (outcome_routes.go, WS-21) ---
	"POST /api/workspaces/{workspace_id}/why-failed":              coreGate(roleProposer, "why_failed", "reads an evidence tail or a log and records the outcome; a command runs only with XMUSTARD_WHY_FAILED_COMMANDS=1 and an authenticated admin (host-code execution; the closed program table is not a sandbox)"),
	"GET /api/workspaces/{workspace_id}/outcomes":                 coreGate(roleReader, "", "run-independent outcomes, newest first; reads only"),
	"DELETE /api/workspaces/{workspace_id}/outcomes/{outcome_id}": coreGate(roleAdmin, "", "removes one outcome (a secret the redactor missed in its command or tail)"),

	// --- core: MCP over Streamable HTTP (mcp_routes.go) ---
	"POST /mcp":   {Core: true, Role: roleReader, ReadSafe: true, Note: "MCP messages; each tool call re-enters the API through its own route gate as the caller"},
	"GET /mcp":    coreGate(roleReader, "", "no server-initiated stream: 405"),
	"DELETE /mcp": {Core: true, Role: roleReader, ReadSafe: true, Note: "ends the caller's own MCP session"},

	// --- core: evidence delivery and recovery ---
	"POST /api/workspaces/{workspace_id}/evidence":            {Core: true, Role: roleProposer, ReadSafe: true, Note: "projection of the caller's own tool result"},
	"GET /api/workspaces/{workspace_id}/evidence/{handle}":    coreGate(roleReader, "", "issuer-bound expansion"),
	"DELETE /api/workspaces/{workspace_id}/evidence/{handle}": {Core: true, Role: roleProposer, ReadSafe: true, Note: "issuer revokes its own original"},
	"DELETE /api/workspaces/{workspace_id}/evidence":          coreGate(roleAdmin, "", "workspace-wide purge of every principal's originals"),
	"POST /api/workspaces/{workspace_id}/evidence/capture":    {Core: true, Role: roleProposer, ReadSafe: true, Note: "any tool's output (raw or a client hook body), the caller's own"},
	"GET /api/workspaces/{workspace_id}/evidence/search":      coreGate(roleReader, "", "issuer-bound search in an original"),

	// --- core: Claude Code hook service (hooks_routes.go, WS-23) ---
	"POST /api/hooks/claude/SessionStart":       hookGate("ground's spawn-free part and core-tier memories as context; watchPaths"),
	"POST /api/hooks/claude/SubagentStart":      hookGate("the same context for a subagent; records its id for attribution"),
	"POST /api/hooks/claude/UserPromptSubmit":   hookGate("memories a prompt keyword triggers"),
	"POST /api/hooks/claude/PreToolUse":         hookGate("index hits and memories for a search pattern or a file"),
	"POST /api/hooks/claude/PostToolUse":        hookGate("captures the caller's own tool output; shape-matched updatedToolOutput"),
	"POST /api/hooks/claude/PostToolUseFailure": hookGate("captures the caller's own failed tool output for the run outcome"),
	"POST /api/hooks/claude/PostToolBatch":      hookGate("batch search nudge"),
	"POST /api/hooks/claude/CwdChanged":         hookGate("watchPaths of the new directory's workspace"),
	"POST /api/hooks/claude/FileChanged":        hookGate("feeds the watcher's pending batch; drops the cached repository identity"),
	"POST /api/hooks/claude/WorktreeRemove":     hookGate("forgets the worktree's cached identity"),
	"POST /api/hooks/claude/PreCompact":         hookGate("queued; answered at once"),
	"POST /api/hooks/claude/PostCompact":        hookGate("queued; answered at once"),
	"POST /api/hooks/claude/Stop":               hookGate("queued; answered at once"),
	"POST /api/hooks/claude/SubagentStop":       hookGate("queued; answered at once"),
	"POST /api/hooks/claude/SessionEnd":         hookGate("queued; answered at once (1.5 s SessionEnd budget)"),

	// --- platform: operator configuration ---
	"GET /api/runtimes":                platformGate(roleReader, ""),
	"GET /api/settings":                platformGate(roleReader, ""),
	"POST /api/settings":               platformGate(roleAdmin, "includes memory verification policy"),
	"GET /api/providers":               platformGate(roleReader, ""),
	"POST /api/providers":              platformGate(roleAdmin, ""),
	"DELETE /api/providers/{name}":     platformGate(roleAdmin, ""),
	"GET /api/providers/{name}/models": platformGate(roleReader, ""),
	"POST /api/providers/{name}/probe": platformGate(roleProposer, ""),
	"POST /api/providers/{name}/chat":  platformGate(roleProposer, ""),
	"POST /api/route":                  platformGate(roleProposer, ""),
	"POST /api/route/chat":             platformGate(roleProposer, ""),
	"GET /api/routes":                  platformGate(roleReader, ""),
	"POST /api/routes":                 platformGate(roleAdmin, ""),
	"GET /api/postgres/plan":           platformGate(roleReader, ""),
	"GET /api/postgres/render":         platformGate(roleReader, ""),
	"POST /api/postgres/bootstrap":     platformGate(roleAdmin, "schema DDL"),
	"GET /api/agent/capabilities":      platformGate(roleReader, ""),
	"POST /api/integrations/test":      platformGate(roleAdmin, "uses supplied credentials"),

	// --- platform: remote execution (terminals) ---
	"POST /api/terminal/open":                 platformGate(roleAdmin, "remote shell"),
	"POST /api/terminal/{terminal_id}/write":  platformGate(roleAdmin, "remote shell"),
	"POST /api/terminal/{terminal_id}/resize": platformGate(roleAdmin, "remote shell"),
	"GET /api/terminal/{terminal_id}/read":    platformGate(roleAdmin, "remote shell output"),
	"DELETE /api/terminal/{terminal_id}":      platformGate(roleAdmin, "remote shell"),

	// --- platform: workspace reads ---
	"GET /api/workspaces/{workspace_id}/snapshot":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/worktree":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/repo-state":                                 platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/ingestion-plan":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/run-targets":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verify-targets":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/activity":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/activity/overview":                          platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/signals":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/fixes":                                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verifications":                              platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/review-queue":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/metrics":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/costs":                                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/sources":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/drift":                                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/tree":                                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/guidance":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/impact":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/diagnostics/status":                         platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/diagnostics/live":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/repo-context":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/project-info":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verification-outcomes":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/retrieval-search":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/path-symbols":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/document-symbols":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/go-to-definition":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/references":                                 platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/workspace-symbols":                          platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/lsp/workspace-symbols":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/lsp/document-symbols":                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/clusters":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/semantic-search":                            platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/repo-map":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/export":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runbooks":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/goals":                                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/goals/{goal_id}":                            platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/goals/{goal_id}/ledger":                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/goals/{goal_id}/context":                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verification-profiles":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verification-profile-history":               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verification-profile-reports":               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/coverage":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/policy":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/audit-log":                                  platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/security/dispositions":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition": platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/security/review-packet":                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/security/acceptance-criteria":               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/security/acceptance-evaluation":             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/dashboard":                                  platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/fingerprint":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/changes/drift":                              platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/changes":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/symbol-graph":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issue-symbol-edges":                         platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/hotspots":                                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/blast-radius":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/wiki":                                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/pg/search":                                  platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/pg/runs":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/pg/run-plans":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/pg/issues/search":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/pg/verifications":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/subsystems":                                 platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/owners":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/lineage":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/eval-timeline":                              platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/verifier-telemetry":                         platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/agents":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/agents/{agent_id}":                          platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/repo-config":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/repo-config/health":                         platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/eval-scenarios":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/eval-report":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/integrations":                               platformGate(roleReader, ""),

	// --- platform: issues, runs and their reads ---
	"GET /api/workspaces/{workspace_id}/issues":                                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/drift":                               platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/fix-draft":                           platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/work":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps":                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays":                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays/{replay_id}/compare": platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models":                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/coverage-delta":                      platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/quality":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/duplicates":                          platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions":                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/review-packet":                       platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/owner-suggestions":                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ownership-history":                   platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/ingested-ticket":                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/issues/{issue_id}/handoff":                             platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/views":                                                 platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs":                                                  platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}":                                         platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/log":                                     platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/plan":                                    platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/insights":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/metrics":                                 platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/critique":                                platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/improvements":                            platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/confidence":                              platformGate(roleReader, ""),
	"GET /api/workspaces/{workspace_id}/runs/{run_id}/brief":                                   platformGate(roleReader, ""),

	// --- platform: writes ---
	"POST /api/workspaces/{workspace_id}/agent/probe":                                              platformGate(roleProposer, "executes the configured runtime"),
	"POST /api/workspaces/{workspace_id}/agent/query":                                              platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/scan":                                                     platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues":                                                   platformGate(roleProposer, ""),
	"PATCH /api/workspaces/{workspace_id}/issues/{issue_id}":                                       platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/runs":                                   platformGate(roleProposer, "launches an agent run"),
	"POST /api/workspaces/{workspace_id}/views":                                                    platformGate(roleProposer, ""),
	"PUT /api/workspaces/{workspace_id}/views/{view_id}":                                           platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/views/{view_id}":                                        platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/review":                                     platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/accept":                                     platformGate(roleApprover, "approval"),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/cancel":                                     platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/retry":                                      platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan":                                       platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/approve":                               platformGate(roleApprover, "approval; never a merge authorization"),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/reject":                                platformGate(roleApprover, "approval"),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/critique":                                   platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/runs/{run_id}/improvements/{suggestion_id}/dismiss":       platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/fixes":                                  platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/diagnostics/run":                                          platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/path-symbols/materialize":                                 platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/semantic-index/materialize":                               platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/semantic-search/materialize":                              platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/verification-profiles/{profile_id}/run": platformGate(roleProposer, "runs a saved profile"),
	"POST /api/workspaces/{workspace_id}/runbooks":                                                 platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/runbooks/{runbook_id}":                                  platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/goals":                                                    platformGate(roleProposer, ""),
	"PATCH /api/workspaces/{workspace_id}/goals/{goal_id}/status":                                  platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/goals/{goal_id}/iterations":                               platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/verification-profiles":                                    platformGate(roleAdmin, "defines commands a run executes"),
	"DELETE /api/workspaces/{workspace_id}/verification-profiles/{profile_id}":                     platformGate(roleAdmin, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context":                         platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context/{context_id}":          platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps":                          platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps/{dump_id}":              platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays":                        platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models":                          platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models/{threat_model_id}":      platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/coverage/parse":                                           platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/quality":                                platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/quality/score-all":                                        platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/triage":                                 platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/triage/all":                                               platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions":                       platformGate(roleProposer, ""),
	"PUT /api/workspaces/{workspace_id}/policy":                                                    platformGate(roleApprover, "policy change"),
	"POST /api/workspaces/{workspace_id}/policy/evaluate":                                          {Role: roleProposer, ReadSafe: true, Note: "evaluation only"},
	"POST /api/workspaces/{workspace_id}/audit-log":                                                platformGate(roleAdmin, "audit integrity"),
	"PUT /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition":                platformGate(roleApprover, "risk acceptance"),
	"PUT /api/workspaces/{workspace_id}/security/acceptance-criteria":                              platformGate(roleApprover, "policy change"),
	"POST /api/workspaces/{workspace_id}/pg/materialize":                                           platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/pg/ops/materialize":                                       platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/pg/verifications/materialize":                             platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/incorporate":                                              platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/ingest-ticket":                          platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/guidance/customize":                                       platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/guidance/starters":                                        platformGate(roleProposer, "writes files into the repository"),
	"POST /api/workspaces/{workspace_id}/agents/sync":                                              platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/eval-scenarios":                                           platformGate(roleProposer, ""),
	"DELETE /api/workspaces/{workspace_id}/eval-scenarios/{scenario_id}":                           platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/issues/{issue_id}/eval-scenarios/replay":                  platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/integrations":                                             platformGate(roleAdmin, "stores integration credentials"),
	"POST /api/workspaces/{workspace_id}/integrations/github/import":                               platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/integrations/github/pr":                                   platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/integrations/slack/notify":                                platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}":                      platformGate(roleProposer, ""),
	"POST /api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}":                        platformGate(roleProposer, ""),
}

// patternMethod returns the method of a ServeMux pattern ("" for any method).
func patternMethod(pattern string) string {
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		return pattern[:i]
	}
	return ""
}

// patternPath returns the path of a ServeMux pattern.
func patternPath(pattern string) string {
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		return pattern[i+1:]
	}
	return pattern
}

// mutating reports whether read-only mode refuses the route.
func (g routeGate) mutating(pattern string) bool {
	switch patternMethod(pattern) {
	case "", http.MethodGet, http.MethodHead:
		return false
	}
	return !g.ReadSafe
}

// routeRegistrar is the part of *http.ServeMux the route files use, so they register
// onto a plain mux in isolated tests and onto a gatedMux in the server.
type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// gatedMux registers routes on a ServeMux and records each route's gate. A route
// without a row in routeGateTable is a programming error caught at startup. The
// ServeMux is not embedded, so no promoted Handle or HandleFunc can register a
// route around the table; routeGateMiddleware also refuses a matched pattern that
// has no gate.
type gatedMux struct {
	mux   *http.ServeMux
	gates map[string]routeGate
}

func newGatedMux() *gatedMux {
	return &gatedMux{mux: http.NewServeMux(), gates: map[string]routeGate{}}
}

// gate returns the route's row, panicking when it is missing or invalid.
func (m *gatedMux) gate(pattern string) routeGate {
	g, ok := routeGateTable[pattern]
	if !ok {
		panic(fmt.Sprintf("route %q has no row in routeGateTable (route_gates.go): classify it core or platform and give it a role", pattern))
	}
	if g.Role == "" || !validGateRole(g.Role) {
		panic(fmt.Sprintf("route %q has an invalid gate role %q", pattern, g.Role))
	}
	return g
}

func (m *gatedMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(handler))
}

func (m *gatedMux) Handle(pattern string, handler http.Handler) {
	g := m.gate(pattern)
	m.gates[pattern] = g
	m.mux.Handle(pattern, handler)
}

// Handler reports the handler and pattern the ServeMux would use for r.
func (m *gatedMux) Handler(r *http.Request) (http.Handler, string) { return m.mux.Handler(r) }

func (m *gatedMux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }

func validGateRole(role string) bool {
	for _, r := range workspaceops.Roles() {
		if r == role {
			return true
		}
	}
	return false
}

// toolGates maps each MCP tool to the pattern of its primary route: the one whose role
// makes the tool usable. A tool served by more than one route (why_failed reads with
// GET and records with POST) is usable through its read route; its other routes carry
// the tool name so that disabling the tool refuses them too.
func toolGates() map[string]string {
	out := map[string]string{}
	for pattern, g := range routeGateTable {
		if g.Tool == "" {
			continue
		}
		if prev, ok := out[g.Tool]; !ok || routeGateTable[prev].mutating(prev) && !g.mutating(pattern) {
			out[g.Tool] = pattern
		}
	}
	return out
}

// sortedGatePatterns lists the table's patterns: core first, then by path and method.
func sortedGatePatterns() []string {
	out := make([]string, 0, len(routeGateTable))
	for p := range routeGateTable {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		gi, gj := routeGateTable[out[i]], routeGateTable[out[j]]
		if gi.Core != gj.Core {
			return gi.Core
		}
		if pi, pj := patternPath(out[i]), patternPath(out[j]); pi != pj {
			return pi < pj
		}
		return out[i] < out[j]
	})
	return out
}
