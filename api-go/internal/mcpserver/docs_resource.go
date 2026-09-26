package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"xmustard/api-go/internal/groundbudget"
)

// Built-in documentation resources. tools/list advertises a lean schema (a tested
// byte cap per protocol version and role); what it leaves out, the arguments tools/call
// also accepts and the output-budget contract of ground, is disclosed here on demand.
// The text is generated from the tool table and the ground section table, so it cannot
// drift from what the server validates and the API does.

// DocsURI documents the arguments tools/list does not advertise.
const DocsURI = groundbudget.DocsURI

const docsScheme = "xmustard://docs/"

var (
	toolsDocOnce sync.Once
	toolsDocText string
)

func toolsDoc() string {
	toolsDocOnce.Do(func() { toolsDocText = renderToolsDoc(Tools()) })
	return toolsDocText
}

func renderToolsDoc(tools []*Tool) string {
	var b strings.Builder
	b.WriteString("# xMustard tool arguments not listed in tools/list\n\n")
	b.WriteString("tools/list advertises a lean schema so every session's prompt prefix stays small; its size is capped by a " +
		"test per protocol version and role. tools/call also accepts the arguments below, validated exactly like listed " +
		"ones: unknown names, wrong types and out-of-range values are rejected, never clamped. A client that validates " +
		"calls against the closed inputSchema before sending them can start the MCP server with XMUSTARD_MCP_SCHEMA=full " +
		"to list them.\n")
	for _, t := range tools {
		if len(t.Advanced) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", t.Name)
		for _, a := range t.Advanced {
			fmt.Fprintf(&b, "- `%s` (%s)", a.Name, argShape(a))
			if a.Desc != "" {
				b.WriteString(": " + a.Desc)
			}
			b.WriteString("\n")
		}
		if t.Doc != "" {
			b.WriteString("\n" + t.Doc + "\n")
		}
		if t.Name == groundTool.Name {
			b.WriteString("\n" + groundbudget.Markdown())
		}
	}
	b.WriteString("\n## Hidden aliases\n\nAccepted on tools/call and never listed; each folds onto its canonical argument " +
		"and the call's _meta[\"xmustard/normalized\"] records it. Claude Code drops an argument named exactly `query`, " +
		"so the listed name is `q`.\n\n")
	for _, t := range tools {
		names := make([]string, 0, len(t.Aliases))
		for alias := range t.Aliases {
			names = append(names, alias)
		}
		sort.Strings(names)
		for _, alias := range names {
			fmt.Fprintf(&b, "- %s: `%s` for `%s`\n", t.Name, alias, t.Aliases[alias])
		}
	}
	return b.String()
}

// argShape renders an argument's type and bounds for the documentation.
func argShape(a Arg) string {
	shape := a.Type
	if a.List {
		shape = "comma-separated string or array of strings"
	}
	if a.Type == typeInteger {
		shape += fmt.Sprintf(", %d to %d", a.Min, a.Max)
	}
	if len(a.Enum) > 0 {
		each := "one of"
		if a.List {
			each = "each one of"
		}
		shape += "; " + each + ": " + strings.Join(a.Enum, ", ")
	}
	return shape
}

func docsList() []map[string]any {
	return []map[string]any{{
		"uri": DocsURI, "name": "xMustard tool arguments not in tools/list", "mimeType": "text/markdown",
		"description": "Arguments tools/call accepts beyond the listed schema (ground sections and max_chars), ground's output-budget contract, and hidden aliases.",
	}}
}

// resourcesList lists the docs resources, then the provider's.
func (s *Session) resourcesList(ctx context.Context) any {
	docs := docsList()
	r := s.srv.opts.Resources
	if r == nil {
		return map[string]any{"resources": docs}
	}
	res := r.List(ctx)
	m, ok := res.(map[string]any)
	if !ok {
		return res
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	list, _ := m["resources"].([]map[string]any)
	out["resources"] = append(docs, list...)
	return out
}

// readResource serves a docs URI itself and hands every other URI to the provider.
func (s *Session) readResource(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid params: uri is required"}
	}
	if strings.HasPrefix(p.URI, docsScheme) {
		if p.URI != DocsURI {
			return nil, &RPCError{Code: CodeResourceNotFound, Message: "resource not found: " + p.URI,
				Data: map[string]any{"uri": p.URI, "reason": "unknown_doc", "available": []string{DocsURI}}}
		}
		return map[string]any{"contents": []map[string]any{{"uri": DocsURI, "mimeType": "text/markdown", "text": toolsDoc()}}}, nil
	}
	if r := s.srv.opts.Resources; r != nil {
		return r.Read(ctx, params)
	}
	return nil, &RPCError{Code: CodeResourceNotFound, Message: "resource not found: " + p.URI,
		Data: map[string]any{"uri": p.URI, "reason": "unknown_scheme"}}
}
