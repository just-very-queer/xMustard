package workspaceops

import (
	"encoding/json"
	"sort"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

// Issue↔symbol edges complete the typed semantic graph: the code-level edges
// (imports/calls/inherits/tests/references) live in rust-core's symbol graph;
// the issue↔symbol layer lives here because issues are a Go-side data store.
// An edge links an issue to a defined symbol either because the issue text
// mentions the symbol name ("mentions") or because the issue's evidence points
// at the file that defines it ("evidence").

type IssueSymbolEdge struct {
	IssueID string `json:"issue_id"`
	Title   string `json:"title"`
	Symbol  string `json:"symbol"`
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "mentions" | "evidence"
}

type graphSymbolLite struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type graphLite struct {
	Symbols []graphSymbolLite `json:"symbols"`
}

const (
	minIssueSymbolLen   = 4
	maxIssueSymbolEdges = 500
	maxEdgesPerIssue    = 24
)

// names too generic to anchor an issue↔symbol edge.
var issueSymbolStopwords = map[string]struct{}{
	"main": {}, "test": {}, "tests": {}, "init": {}, "build": {}, "error": {},
	"result": {}, "value": {}, "data": {}, "name": {}, "path": {}, "type": {},
	"node": {}, "item": {}, "list": {}, "status": {}, "title": {}, "issue": {},
}

// IssueSymbolEdges joins the workspace's issues to the symbol graph.
func IssueSymbolEdges(dataDir, workspaceID string) ([]IssueSymbolEdge, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	raw, err := rustcore.RunSymbolgraph("build", root, workspaceID)
	if err != nil {
		return nil, err
	}
	var g graphLite
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}

	// symbol name -> distinct defining paths (only distinctive names).
	defs := map[string]map[string]struct{}{}
	for _, s := range g.Symbols {
		name := strings.TrimSpace(s.Name)
		if len(name) < minIssueSymbolLen {
			continue
		}
		if _, stop := issueSymbolStopwords[strings.ToLower(name)]; stop {
			continue
		}
		if defs[name] == nil {
			defs[name] = map[string]struct{}{}
		}
		defs[name][s.Path] = struct{}{}
	}

	issues, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	edges := make([]IssueSymbolEdge, 0, 64)
	seen := map[string]struct{}{} // issue|symbol|path|kind dedupe
	add := func(e IssueSymbolEdge) bool {
		key := e.IssueID + "|" + e.Symbol + "|" + e.Path + "|" + e.Kind
		if _, dup := seen[key]; dup {
			return true
		}
		seen[key] = struct{}{}
		edges = append(edges, e)
		return len(edges) < maxIssueSymbolEdges
	}

	for i := range issues {
		issue := &issues[i]
		perIssue := 0
		// 1) evidence paths -> symbols defined in that file
		evidencePaths := map[string]struct{}{}
		for _, ev := range issue.Evidence {
			if ev.Path != "" {
				clean := strings.TrimPrefix(strings.ReplaceAll(ev.Path, "\\", "/"), "./")
				evidencePaths[clean] = struct{}{}
			}
		}
		// 2) text mentions
		blob := issueText(issue)
		tokens := tokenizeIssueBlob(blob)
		for name, paths := range defs {
			if perIssue >= maxEdgesPerIssue {
				break
			}
			kind := ""
			if _, mentioned := tokens[name]; mentioned {
				kind = "mentions"
			}
			// evidence edges take precedence in labeling
			for p := range paths {
				edgeKind := kind
				if _, ok := evidencePaths[p]; ok {
					edgeKind = "evidence"
				}
				if edgeKind == "" {
					continue
				}
				if !add(IssueSymbolEdge{
					IssueID: issue.BugID,
					Title:   issue.Title,
					Symbol:  name,
					Path:    p,
					Kind:    edgeKind,
				}) {
					sortIssueSymbolEdges(edges)
					return edges, nil
				}
				perIssue++
				if perIssue >= maxEdgesPerIssue {
					break
				}
			}
		}
	}
	sortIssueSymbolEdges(edges)
	return edges, nil
}

func issueText(issue *issueRecord) string {
	var b strings.Builder
	b.WriteString(issue.Title)
	b.WriteByte(' ')
	if issue.Summary != nil {
		b.WriteString(*issue.Summary)
		b.WriteByte(' ')
	}
	if issue.Impact != nil {
		b.WriteString(*issue.Impact)
		b.WriteByte(' ')
	}
	if issue.Notes != nil {
		b.WriteString(*issue.Notes)
	}
	return b.String()
}

func tokenizeIssueBlob(blob string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tok := range strings.FieldsFunc(blob, func(r rune) bool {
		return !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) {
		if len(tok) >= minIssueSymbolLen {
			out[tok] = struct{}{}
		}
	}
	return out
}

func sortIssueSymbolEdges(edges []IssueSymbolEdge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].IssueID != edges[j].IssueID {
			return edges[i].IssueID < edges[j].IssueID
		}
		if edges[i].Kind != edges[j].Kind {
			return edges[i].Kind < edges[j].Kind
		}
		return edges[i].Symbol < edges[j].Symbol
	})
}
