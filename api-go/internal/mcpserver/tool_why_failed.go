package mcpserver

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// whyFailedWriteArgs are the sources that run a command or record an outcome; without
// them why_failed reads a run or an outcome by id.
var whyFailedWriteArgs = []string{"command", "evidence_handle", "log"}

// maxWhyFailedTimeout stays below the MCP clients' 60 s call timeout (the HTTP route
// accepts up to workspaceops.MaxWhyFailedTimeout).
const maxWhyFailedTimeout = 50

var whyFailedTool = &Tool{
	Name:        "why_failed",
	Description: "Why a run_id, log or evidence_handle failed: error lines, implicated files.",
	Args: []Arg{
		workspaceArg,
		{Name: "run_id", Type: typeString, Desc: "run/outcome id"},
	},
	// Run-independent sources (WS-21): listed only in the full schema profile.
	Advanced: []Arg{
		{Name: "command", Type: typeString, MaxLen: 4096, Desc: "admins, if the operator enabled commands: test/build/lint argv to run"},
		{Name: "cwd", Type: typeString, MaxLen: 1024, Desc: "repo-relative directory to run command in"},
		{Name: "timeout_seconds", Type: typeInteger, Min: 1, Max: maxWhyFailedTimeout, Desc: "seconds before command's process group is killed (default 45)"},
		{Name: "evidence_handle", Type: typeString, MaxLen: 256, Desc: "retained tool output to explain from its last MiB; records the outcome"},
		{Name: "log", Type: typeString, MaxLen: 1 << 20, Desc: "pasted log to explain from its last MiB; records the outcome"},
	},
	Doc: "### Command mode\n\n`command` is off unless the server's operator started it with XMUSTARD_WHY_FAILED_COMMANDS=1, " +
		"and then runs only for an authenticated admin; every other caller gets 403. Enabled, it is trusted execution of " +
		"the repository's code on the server host by admin credentials: the closed table of test, build and lint programs " +
		"is defense in depth, not a sandbox. `evidence_handle` and `log` never run anything and need the proposer role; " +
		"a call naming more than one source is refused.\n",
	WriteArgs: whyFailedWriteArgs,
	PathArg:   "cwd",
	// A command can change the working tree (a build writes outputs, make clean deletes
	// them), so the tool is not read-only; a call without a write argument only reads.
	Annotations: Annotations{Title: "Explain a failure", Destructive: true},
	Output: map[string]string{
		"run_id": "string", "failed": "boolean", "status": "string", "summary": "string", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		if !writesAny(a, whyFailedWriteArgs) && a["run_id"] != "" {
			return "GET", wsPath(a, "/runs/"+url.PathEscape(a["run_id"])+"/why-failed"), ""
		}
		// Anything else is a recording call; the API rejects a missing or mixed source
		// (run_id travels along so a run_id with a source is refused, not ignored).
		payload := map[string]any{}
		for _, k := range []string{"command", "cwd", "evidence_handle", "log", "run_id"} {
			if a[k] != "" {
				payload[k] = a[k]
			}
		}
		if n, err := strconv.Atoi(a["timeout_seconds"]); err == nil {
			payload["timeout_seconds"] = n
		}
		body, _ := json.Marshal(payload)
		return "POST", wsPath(a, "/why-failed"), string(body)
	},
}
