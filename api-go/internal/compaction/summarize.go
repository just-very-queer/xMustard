package compaction

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/injection"
)

// Summaries by kind. A replaced tool result is described in one line from its call's
// arguments and its content: what was read, searched or run and what came of it. The
// line is xMustard's; any text it quotes from the result (a first error line, an MCP
// server name) is a short single-line snippet that passes the injection scan (WS-56)
// and never comes from an untrusted tool. Otherwise it is withheld, and the stub says
// the original is data.
//
// The planner summaries, the task and MCP-call summaries and the todo delta are ported
// from cursor-bridge bridge_compaction.go (SHA-256
// b94bfa747732e588572db6dc63c8e5ae7910a752fa5e22ee4cfb7b4eb2f00394). Changes from
// upstream: they read structured call inputs and results instead of a flat transcript;
// the todo delta is computed against the previous planner state in the history when the
// result carries no original list; tool names are matched through one table.

// exchange is one tool call and its result.
type exchange struct {
	call   call
	result result
	text   string // the result content's text
	images int
}

// maxSnippet bounds a quoted snippet, in runes.
const maxSnippet = 120

// summarizer describes one exchange.
type summarizer func(x *exchange) string

// toolSummarizers describe tools by their normalized name, before the family table.
var toolSummarizers = map[string]summarizer{
	"task_v2": taskSummary, "subagent": taskSummary, "await_task": taskSummary,
	"call_mcp_tool": mcpCallSummary, "read_mcp_resource": mcpReadSummary,
	"list_mcp_resources": mcpListSummary, "list_mcp_tools": mcpListSummary, "list_mcp_prompts": mcpListSummary,
}

// familySummarizers describe tools by their evidence family (registry.go).
var familySummarizers = map[evidence.Family]summarizer{
	evidence.FamilyRead: readSummary, evidence.FamilyGrep: grepSummary,
	evidence.FamilyGlob: listSummary, evidence.FamilyList: listSummary, evidence.FamilyDiff: diffSummary,
	evidence.FamilyShell: commandSummary, evidence.FamilyTest: commandSummary, evidence.FamilyBuild: commandSummary,
	evidence.FamilyLint: commandSummary, evidence.FamilyLog: commandSummary, evidence.FamilyGit: commandSummary,
	evidence.FamilyStructured: structuredSummary,
}

// argKeys are the input members that name each concept, in preference order.
var argKeys = map[string][]string{
	"path":    {"file_path", "path", "filePath", "target_file", "file", "notebook_path", "uri"},
	"pattern": {"pattern", "query", "q", "regex", "glob", "search"},
	"command": {"command", "cmd"},
}

// commandFamilies run a command: their summary reports the exit status and the first
// failure line, for a failed call too.
var commandFamilies = map[evidence.Family]bool{
	evidence.FamilyShell: true, evidence.FamilyTest: true, evidence.FamilyBuild: true, evidence.FamilyLint: true,
	evidence.FamilyLog: true, evidence.FamilyGit: true,
}

// summarize describes x in one line (without the stub's prefix). The first summarizer
// that has something to say wins: a named tool's, xMustard's own (JSON), a failure's,
// then the tool family's; the size of the content is the fallback.
func summarize(x *exchange) string {
	name := toolName(x)
	fam, _ := evidence.SelectFamily(evidence.Selector{Tool: name, Command: arg(x, "command")})
	var chain []summarizer
	if s, ok := toolSummarizers[normalizeTool(name)]; ok {
		chain = append(chain, s)
	}
	switch {
	case injection.OwnTool(name):
		chain = append(chain, structuredSummary)
	case x.result.isError && !commandFamilies[fam]:
		chain = append(chain, errorSummary)
	}
	chain = append(chain, familySummarizers[fam])
	for _, s := range chain {
		if s == nil {
			continue
		}
		if line := s(x); line != "" {
			return line
		}
	}
	return fmt.Sprintf("%s returned %s", name, size(x))
}

// toolName is the call's tool name (the result's for a Pi result without its call).
func toolName(x *exchange) string {
	if x.call.name != "" {
		return x.call.name
	}
	return x.result.name
}

// normalizeTool lowercases a tool name and drops a client's MCP namespace.
func normalizeTool(name string) string {
	t := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(t, "__"); i >= 0 {
		t = t[i+2:]
	}
	return t
}

// arg returns the first string input member naming concept.
func arg(x *exchange, concept string) string {
	for _, k := range argKeys[concept] {
		switch v := x.call.input[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			if parts := stringsOf(v); len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

func stringsOf(v []any) []string {
	var out []string
	for _, e := range v {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// size is the content's size: lines and bytes, plus images.
func size(x *exchange) string {
	s := fmt.Sprintf("%d lines, %d bytes", lineCount(x.text), len(x.result.content))
	if x.images > 0 {
		s += fmt.Sprintf(", %d images", x.images)
	}
	return s
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

func errorSummary(x *exchange) string {
	name := toolName(x)
	line := safeSnippet(x, firstLine(strings.TrimPrefix(strings.TrimSpace(x.text), "ERROR:")))
	if line == "" {
		return fmt.Sprintf("%s failed (%s)", name, size(x))
	}
	return fmt.Sprintf("%s failed: %s", name, line)
}

func readSummary(x *exchange) string {
	return fmt.Sprintf("%s %s: %s", toolName(x), quoteArg(x, "path"), size(x))
}

// grepHit is a path:line: prefix of a search result line.
var grepHit = regexp.MustCompile(`^([^\s:]+):\d+[:-]`)

func grepSummary(x *exchange) string {
	files := map[string]bool{}
	hits := 0
	for _, l := range strings.Split(x.text, "\n") {
		if m := grepHit.FindStringSubmatch(l); m != nil {
			files[m[1]] = true
			hits++
		}
	}
	if hits == 0 {
		return fmt.Sprintf("%s %s: %s", toolName(x), quoteArg(x, "pattern"), size(x))
	}
	return fmt.Sprintf("%s %s: %d matching lines in %d files (%s)", toolName(x), quoteArg(x, "pattern"), hits, len(files), size(x))
}

func listSummary(x *exchange) string {
	target := quoteArg(x, "pattern")
	if target == "" {
		target = quoteArg(x, "path")
	}
	return fmt.Sprintf("%s %s: %d entries", toolName(x), target, lineCount(strings.TrimSpace(x.text)))
}

func diffSummary(x *exchange) string {
	var add, del, files int
	for _, l := range strings.Split(x.text, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git"):
			files++
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			add++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			del++
		}
	}
	return fmt.Sprintf("%s: %d files, +%d -%d (%s)", toolName(x), files, add, del, size(x))
}

// exitPatterns find a command's exit status in its output, in the forms the clients
// print it.
var exitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^Exit code:? (-?\d+)`),
	regexp.MustCompile(`Process exited with code (-?\d+)`),
	regexp.MustCompile(`"(?:exit_code|exitCode|exit)"\s*:\s*(-?\d+)`),
	regexp.MustCompile(`exit status (-?\d+)`),
}

// failureLine finds the first line that reports a failure.
var failureLine = regexp.MustCompile(`(?i)\b(errors?|fail|failed|failures?|failing|panic|fatal|exception)\b`)

func commandSummary(x *exchange) string {
	head := fmt.Sprintf("%s %s", toolName(x), quoteArg(x, "command"))
	if x.result.isError {
		head += " failed"
	}
	parts := []string{head}
	for _, re := range exitPatterns {
		if m := re.FindStringSubmatch(x.text); m != nil {
			parts = append(parts, "exit "+m[1])
			break
		}
	}
	parts = append(parts, size(x))
	for _, l := range strings.Split(x.text, "\n") {
		if failureLine.MatchString(l) {
			if s := safeSnippet(x, l); s != "" {
				parts = append(parts, "first failure: "+s)
			}
			break
		}
	}
	return strings.Join(parts, "; ")
}

// structuredSummary describes a JSON result by its top-level numbers, booleans and
// array lengths (never its strings), sorted by name; a member whose name is not an
// identifier is left out.
func structuredSummary(x *exchange) string {
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(x.text))
	dec.UseNumber()
	if dec.Decode(&payload) != nil || len(payload) == 0 {
		return ""
	}
	names := make([]string, 0, len(payload))
	for k := range payload {
		names = append(names, k)
	}
	sort.Strings(names)
	var facts []string
	for _, k := range names {
		if idText(k) == "" {
			continue
		}
		switch v := payload[k].(type) {
		case json.Number:
			facts = append(facts, k+"="+v.String())
		case bool:
			facts = append(facts, k+"="+strconv.FormatBool(v))
		case []any:
			facts = append(facts, fmt.Sprintf("%s=[%d]", k, len(v)))
		}
		if len(facts) == 8 {
			break
		}
	}
	return fmt.Sprintf("%s returned JSON (%s): %s", toolName(x), size(x), strings.Join(facts, ", "))
}

// quoteArg is a concept's argument as a quoted snippet ("" when absent or withheld).
func quoteArg(x *exchange, concept string) string {
	if s := safeSnippet(x, arg(x, concept)); s != "" {
		return strconv.Quote(s)
	}
	return ""
}

// firstLine is the first non-blank line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// safeSnippet is cleanText of a result's text, withheld ("") when the exchange's tool is
// untrusted (injection.CaptureQuarantine): nothing an untrusted tool wrote is quoted.
func safeSnippet(x *exchange, s string) string {
	if injection.CaptureQuarantine(toolName(x)) != "" {
		return ""
	}
	return cleanText(s, maxSnippet)
}

// idPattern is an identifier: a task id, a status, a server or tool name, a URI. It
// holds no space, so it can name a thing but not carry a sentence.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/@?=&%#+-]{1,96}$`)

// idText is s when it is an identifier, else "" (withheld). Identifiers are quoted from
// any tool's result, trusted or not.
func idText(s string) string {
	if idPattern.MatchString(s) {
		return s
	}
	return ""
}

// cleanText makes s quotable in a summary: one line, whitespace collapsed, at most n
// runes, and no "<" that could open or close an xMustard data frame. It returns ""
// (withheld) when the text matches an instruction pattern.
func cleanText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || !injection.Scan(s).Clean() {
		return ""
	}
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return strings.ReplaceAll(s, "<", "‹")
}

// payloadOf decodes a JSON object result.
func payloadOf(x *exchange) map[string]any {
	var p map[string]any
	if json.Unmarshal([]byte(x.text), &p) != nil {
		return nil
	}
	return p
}

func taskSummary(x *exchange) string {
	p := payloadOf(x)
	if p == nil {
		return ""
	}
	name := toolName(x)
	id := idText(firstNonEmpty(anyString(p["task_id"]), nested(p["task"], "id")))
	status := idText(firstNonEmpty(anyString(p["status"]), nested(p["task"], "status")))
	switch {
	case id == "":
		return ""
	case truthy(p["task_started"]):
		return fmt.Sprintf("%s started %s", name, id)
	case truthy(p["task_reused"]):
		return fmt.Sprintf("%s reused %s (%s)", name, id, firstNonEmpty(status, "existing"))
	case status != "":
		return fmt.Sprintf("%s observed %s as %s", name, id, status)
	}
	return ""
}

func mcpCallSummary(x *exchange) string {
	p := payloadOf(x)
	server, tool := idText(anyString(p["server"])), idText(anyString(p["tool"]))
	switch {
	case server != "" && tool != "":
		return fmt.Sprintf("%s called %s on %s", toolName(x), tool, server)
	case server != "":
		return fmt.Sprintf("%s used MCP server %s", toolName(x), server)
	}
	return ""
}

func mcpReadSummary(x *exchange) string {
	p := payloadOf(x)
	server, uri := idText(anyString(p["server"])), idText(anyString(p["uri"]))
	if server == "" || uri == "" {
		return ""
	}
	return fmt.Sprintf("%s read %s from %s", toolName(x), uri, server)
}

func mcpListSummary(x *exchange) string {
	if server := idText(anyString(payloadOf(x)["server"])); server != "" {
		return fmt.Sprintf("%s listed data from %s", toolName(x), server)
	}
	return ""
}

func anyString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func nested(v any, key string) string {
	if m, ok := v.(map[string]any); ok {
		return anyString(m[key])
	}
	return ""
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Guidance: repeated harness guidance in user messages. A text block is guidance when
// it starts with one of these markers; real user prompts never are.
var guidanceMarkers = []string{"<system-reminder>", "BRIDGE GUIDANCE:", "[xmustard "}

func isGuidance(text string) bool {
	t := strings.TrimSpace(text)
	for _, m := range guidanceMarkers {
		if strings.HasPrefix(t, m) {
			return true
		}
	}
	return false
}
