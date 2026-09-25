package toolcompat

import "strings"

// Token reduces an argument key or tool name to its comparison form: lowercase
// with '_', '-', and spaces removed. "filePath", "file_path", "file-path" and
// "File Path" all share the token "filepath".
func Token(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		switch r {
		case '_', '-', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CanonicalKey maps an argument key to its canonical harness name through the
// alias table (for example "cmd" → "command", "workdir" → "cwd",
// "oldString" → "old_string"). Keys without an alias are returned unchanged.
func CanonicalKey(key string) string {
	if canonical, ok := argAliases[Token(key)]; ok {
		return canonical
	}
	return key
}

// argAliases is the harness argument alias table, keyed by Token. It is ported
// from the owner's cursor-bridge (bridge_tool_compat.go, bridgeToolArgAliases)
// and extended with the argument spellings of Claude Code, Codex, OpenCode, Pi
// and Cline tools. Values are canonical argument names. cursor-bridge's
// "message" → "question" is not ported: it served an ask_question repair that
// toolcompat does not have, and it hid Task's "message" description.
var argAliases = map[string]string{
	// ported from cursor-bridge
	"cmd":                "command",
	"script":             "command",
	"shellcommand":       "command",
	"terminalcommand":    "command",
	"filepath":           "path",
	"filename":           "path",
	"file":               "path",
	"targetpath":         "path",
	"targetfile":         "target_file",
	"targetdirectory":    "target_directory",
	"directorypath":      "directory",
	"dir":                "directory",
	"folder":             "directory",
	"globpattern":        "glob_pattern",
	"searchpattern":      "pattern",
	"searchterm":         "search_term",
	"workingdirectory":   "cwd",
	"workdir":            "cwd",
	"currentdirectory":   "cwd",
	"servername":         "server",
	"toolname":           "tool",
	"mcptoolname":        "tool",
	"mcpservername":      "server",
	"resourcename":       "uri",
	"resourceuri":        "uri",
	"promptname":         "prompt",
	"text":               "content",
	"diagram":            "content",
	"modelname":          "model",
	"requestedmodel":     "model",
	"providername":       "provider",
	"providerid":         "provider",
	"modelprovider":      "provider",
	"vendor":             "provider",
	"subagenttype":       "subagent_type",
	"agenttype":          "subagent_type",
	"taskid":             "task_id",
	"sessionid":          "session_id",
	"timeoutseconds":     "timeout_seconds",
	"timeoutms":          "timeout_ms",
	"runinbackground":    "run_in_background",
	"closestdin":         "close_stdin",
	"oldstring":          "old_string",
	"newstring":          "new_string",
	"replaceall":         "replace_all",
	"insertafter":        "insert_after",
	"insertbefore":       "insert_before",
	"cellindex":          "cell_index",
	"cellidx":            "cell_index",
	"celltype":           "cell_type",
	"celllanguage":       "cell_language",
	"cellsource":         "cell_source",
	"insertcellafter":    "insert_cell_after",
	"isnewcell":          "is_new_cell",
	"stringreplacements": "string_replacements",
	"targetnotebook":     "path",
	// harness spellings not covered by cursor-bridge
	"oldtext":      "old_string", // Pi edit
	"newtext":      "new_string", // Pi edit
	"notebookpath": "path",       // Claude Code NotebookEdit
	"newsource":    "cell_source",
	"editmode":     "edit_mode",
	"outputmode":   "output_mode",
	"headlimit":    "head_limit",
	"workspaceid":  "workspace_id",
	"entryid":      "entry_id",
	"runid":        "run_id",
	"shellid":      "session_id", // Claude Code KillShell (kind kill_shell)
	"bashid":       "session_id", // Claude Code BashOutput
	"blockuntilms": "block_until_ms",
	"absolutepath": "path",    // Cline
	"regex":        "pattern", // Cline search_files
	"filepattern":  "glob",    // Cline search_files, like Claude Code Grep's glob
}

// kindAliases holds per-tool vocabulary for xMustard's nine MCP tools, keyed by
// Token. They apply only to closed specs and only when the target is one of the
// spec's fields, so "file" can mean explain's path but recall's paths.
var kindAliases = map[Kind]map[string]string{
	KindRecall: {
		"q": "query", "text": "query", "question": "query", "search": "query",
		"searchquery": "query", "querytext": "query", "task": "query",
		"file": "paths", "files": "paths", "path": "paths", "filepath": "paths",
		"filepaths": "paths", "filelist": "paths",
	},
	KindRemember: {
		"text": "content", "body": "content", "memory": "content", "fact": "content",
		"note": "content", "message": "content",
		"name": "title", "subject": "title", "heading": "title",
		"file": "paths", "files": "paths", "path": "paths", "filepath": "paths",
		"filepaths": "paths",
	},
	KindVerify: {
		"id": "entry_id", "entry": "entry_id", "memoryid": "entry_id", "memory": "entry_id",
		"proposalid": "entry_id", "proposal": "entry_id",
		"approved": "approve", "accept": "approve", "accepted": "approve",
	},
	KindSearch: {
		"q": "query", "text": "query", "pattern": "query", "searchterm": "query",
		"searchquery": "query", "term": "query", "keyword": "query", "keywords": "query",
		"language": "lang", "searchmode": "mode", "seedsymbol": "seed", "anchor": "seed",
		"glob": "path_glob", "globpattern": "path_glob",
	},
	KindExplain: {
		"file": "path", "filepath": "path", "filename": "path", "target": "path",
		"targetpath": "path", "targetfile": "path", "dir": "path", "directory": "path",
		"uri": "path",
	},
	KindImpact: {
		"name": "symbol", "symbolname": "symbol", "target": "symbol", "function": "symbol",
		"fn": "symbol", "identifier": "symbol",
		"source": "from", "src": "from", "fromsymbol": "from",
		"dest": "to", "destination": "to", "tosymbol": "to",
	},
	KindDiagnostics: {
		"file": "path", "filepath": "path",
	},
	KindWhyFailed: {
		"id": "run_id", "run": "run_id",
	},
}

// commonAliases apply to every closed spec (xMustard's nine tools).
var commonAliases = map[string]string{
	"workspace": "workspace_id",
	"ws":        "workspace_id",
	"wsid":      "workspace_id",
}

// kindNames maps harness tool names (by Token) to kinds. It ports cursor-bridge's
// bridgeToolKindFromName (bridge_orchestrator.go) for the kinds toolcompat
// repairs, and adds the built-in file, search, shell, web, delegation and MCP
// tool names of Claude Code, Codex, Cursor, OpenCode, Pi and Cline. Tools
// outside those families (todo lists, plan mode, questions to the user) have no
// kind.
var kindNames = map[string]Kind{
	// read / write / edit
	"readfile": KindReadFile, "read": KindReadFile,
	"writefile": KindWriteFile, "write": KindWriteFile, "createfile": KindWriteFile,
	"writetofile": KindWriteFile, "deletefile": KindDeleteFile, "delete": KindDeleteFile,
	"applypatch": KindApplyPatch, "edit": KindApplyPatch, "editfile": KindApplyPatch,
	"multiedit": KindApplyPatch, "editnotebook": KindApplyPatch, "notebookedit": KindApplyPatch,
	"searchreplace": KindApplyPatch, "strreplace": KindApplyPatch, "patch": KindApplyPatch,
	"replaceinfile": KindApplyPatch,
	// listing and search
	"listdir": KindListDir, "ls": KindListDir, "list": KindListDir, "listfiles": KindListDir,
	"globfilesearch": KindGlob, "glob": KindGlob, "find": KindGlob, "filesearch": KindGlob,
	"ripgrepsearch": KindGrep, "ripgreprawsearch": KindGrep, "rg": KindGrep, "grep": KindGrep,
	"grepsearch": KindGrep, "searchfiles": KindGrep,
	"searchsymbols":  KindSearchSymbols,
	"semanticsearch": KindSemanticSearch, "codebasesearch": KindSemanticSearch,
	// shell
	"runterminalcommandv2": KindShell, "runterminalcmd": KindShell, "shell": KindShell,
	"bash": KindShell, "execcommand": KindShell, "localshell": KindShell,
	"runcommand": KindShell, "executecommand": KindShell, "terminal": KindShell,
	"writeshellstdin": KindShellStdin, "await": KindShellStdin, "awaitshell": KindShellStdin,
	"writestdin": KindShellStdin, "bashoutput": KindShellStdin,
	"killshell": KindKillShell, "killbash": KindKillShell,
	"awaittask": KindAwaitTask,
	// web
	"webfetch": KindWebFetch, "fetch": KindWebFetch,
	// delegation
	"task": KindTask, "taskv2": KindTask, "subagent": KindTask,
	// MCP family
	"callmcptool": KindCallMCPTool, "usemcptool": KindCallMCPTool,
	"getmcpserver":     KindGetMCPServer,
	"listmcptools":     KindListMCPTools,
	"listmcpresources": KindListMCPResources,
	"readmcpresource":  KindReadMCPResource, "fetchmcpresource": KindReadMCPResource,
	"accessmcpresource": KindReadMCPResource,
	"listmcpprompts":    KindListMCPPrompts,
	"getmcpprompt":      KindGetMCPPrompt,
}

// KindForName resolves a harness tool name to a kind. It accepts the built-in
// names of the supported clients ("Bash", "run_terminal_cmd", "ReadFile",
// "NotebookEdit", ...), the nine xMustard tool names, and xMustard tools
// qualified by their MCP server ("mcp__xmustard__recall", "xmustard_recall",
// "recall@xmustard", "xmustard/recall").
func KindForName(name string) (Kind, bool) {
	name = strings.TrimSpace(name)
	if server, tool, ok := SplitMCPToolName(name); ok {
		if Token(server) != "xmustard" {
			return "", false
		}
		name = tool
	}
	token := Token(name)
	if k, ok := xmustardKinds[token]; ok {
		return k, true
	}
	k, ok := kindNames[token]
	return k, ok
}

// SplitMCPToolName splits a server-qualified MCP tool name into server and tool.
// It understands "mcp__server__tool" (Claude Code, Codex), "tool@server",
// "server/tool", and — for the xmustard server only, because a bare
// "server_tool" is ambiguous — "xmustard_tool", "xmustard__tool" and
// "xmustard.tool".
func SplitMCPToolName(name string) (server, tool string, ok bool) {
	name = strings.TrimSpace(name)
	switch {
	case strings.HasPrefix(name, "mcp__"):
		rest := strings.TrimPrefix(name, "mcp__")
		if i := strings.Index(rest, "__"); i > 0 && i+2 < len(rest) {
			return rest[:i], rest[i+2:], true
		}
	case strings.Count(name, "@") == 1:
		parts := strings.SplitN(name, "@", 2)
		if parts[0] != "" && parts[1] != "" {
			return parts[1], parts[0], true
		}
	case strings.Count(name, "/") == 1:
		parts := strings.SplitN(name, "/", 2)
		if parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], true
		}
	}
	lower := strings.ToLower(name)
	for _, sep := range []string{"__", "_", "."} {
		if prefix := "xmustard" + sep; strings.HasPrefix(lower, prefix) && len(name) > len(prefix) {
			return name[:len("xmustard")], name[len(prefix):], true
		}
	}
	return "", "", false
}

var xmustardKinds = map[string]Kind{
	"ground": KindGround, "recall": KindRecall, "remember": KindRemember, "verify": KindVerify,
	"search": KindSearch, "explain": KindExplain, "impact": KindImpact,
	"diagnostics": KindDiagnostics, "whyfailed": KindWhyFailed,
}
