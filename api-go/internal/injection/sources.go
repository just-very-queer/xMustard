package injection

import (
	"cmp"
	"regexp"
	"strings"
)

// Quarantine marks content whose origin xMustard cannot vouch for: web pages and search
// results, another MCP server's output, and memory imported from outside the
// workspace's own store. Content derived from such a source stays quarantined: it is
// served on pulled surfaces with its quarantine label and never pushed (Decide), and the
// store refuses it the core tier.

// QuarantineForeignImport is the quarantine of memory an importer brought in from
// outside the workspace's own store (another harness's sessions, foreign rule files).
const QuarantineForeignImport = "foreign_import"

// quarantineCapturePrefix starts the quarantine of content derived from a capture of an
// untrusted tool; the tool name follows.
const quarantineCapturePrefix = "untrusted_capture:"

// workspaceTools are the native tools whose output comes from the workspace itself: the
// file, search, list, diff and shell tools of the supported clients (the names the
// evidence registry knows, lowercased). With xmustardTools they are the trusted capture
// sources; output of any other tool is untrusted: WebFetch, WebSearch, a browser, and
// every other MCP server.
var workspaceTools = setOf(
	// file reads, edits and notebook reads
	"read", "read_file", "readfile", "view", "open_file", "view_file", "cat", "edit", "multiedit", "write",
	"apply_patch", "notebookread", "notebookedit",
	// search, glob and list
	"grep", "rg", "ripgrep", "grep_search", "search_files", "search_file_content", "codebase_search",
	"glob", "find", "find_files", "file_search", "ls", "list", "list_dir", "list_directory", "listdir", "list_files",
	// diffs and shells
	"git_diff", "diff", "bash", "shell", "sh", "exec", "exec_command", "run_terminal_cmd", "run_shell_command",
	"terminal", "execute_command", "local_shell", "powershell", "command", "run_command", "bashoutput",
)

// xmustardTools are xMustard's nine tools, whose output is the workspace's code and
// memory.
var xmustardTools = setOf("ground", "recall", "search", "explain", "impact", "diagnostics", "why_failed", "remember", "verify")

// xmustardToolPrefixes are how clients name xMustard's own MCP tools.
var xmustardToolPrefixes = []string{"mcp__xmustard__", "mcp__xmustard-mcp__"}

// maxQuarantineTool bounds the tool name a quarantine records.
const maxQuarantineTool = 96

func setOf(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// CaptureQuarantine is the quarantine of content derived from a capture of tool, or ""
// when the tool reads the workspace. The name is the one the capturing client recorded.
// Fail closed: an unknown or empty name is untrusted, and an MCP tool is trusted only
// when it is xMustard's own.
func CaptureQuarantine(tool string) string {
	t := strings.ToLower(strings.TrimSpace(tool))
	if workspaceTools[t] || OwnTool(t) {
		return ""
	}
	return quarantineCapturePrefix + attrValue(cutUTF8(cmp.Or(t, "unknown"), maxQuarantineTool))
}

// OwnTool reports whether tool is one of xMustard's nine tools, by its bare name or as
// an MCP tool of the xmustard server.
func OwnTool(tool string) bool {
	t := strings.ToLower(strings.TrimSpace(tool))
	for _, p := range xmustardToolPrefixes {
		if rest, ok := strings.CutPrefix(t, p); ok {
			return xmustardTools[rest]
		}
	}
	return xmustardTools[t]
}

// quarantineMember is how xMustard's tools render a quarantined memory: a JSON member
// "quarantine" whose value is the reason (a quarantine name keeps attrValue's
// characters). Inside a JSON string a quote is escaped, so tool-written text a result
// quotes (a code snippet, a memory's content) never forms the member.
var quarantineMember = regexp.MustCompile(`"quarantine"[ \t\r\n]{0,8}:[ \t\r\n]{0,8}"([A-Za-z0-9_.,:/-]{1,128})"`)

// MaxQuarantineMember bounds the bytes of one quarantine member, so a caller reading a
// result in chunks finds a member that spans two of them by carrying this many bytes.
const MaxQuarantineMember = 160

// CarriedQuarantine is the reason of the first quarantined memory an xMustard tool
// result carries (recall, a memory fetched by id, a remember or verify reply), or "".
// Content derived from such a result stays quarantined like the memory it quotes.
func CarriedQuarantine(result []byte) string {
	if m := quarantineMember.FindSubmatch(result); m != nil {
		return string(m[1])
	}
	return ""
}
