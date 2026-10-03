package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

// Symbol navigation (WS-36): search mode=symbol, and explain of a file, a directory
// or a symbol. The core answers from the code index (`symbolgraph find-symbol`,
// `overview` and `context`); Go validates arguments, signs cursors, composes the file
// explanation and attaches memory to a symbol's context.

// Navigation argument bounds.
const (
	// NavMaxDepth bounds depth= (children of symbols and overviews).
	NavMaxDepth = 3
	// NavMaxChainDepth bounds chain_depth= (layers past a symbol's direct edges).
	NavMaxChainDepth = 3
	// navMemories is how many memories a symbol's context attaches.
	navMemories = 5
)

// SymbolSearchRequest is search mode=symbol: a name path and its filters.
type SymbolSearchRequest struct {
	Query    string
	PathGlob string
	// Kinds includes (Function,Method) or excludes (-Variable) symbol kinds.
	Kinds                               string
	Depth                               int
	Substring, IncludeBody, IncludeInfo bool
	// Limit is the page size, and the most matches listed before the refine map.
	Limit  int
	Cursor string
}

// rankingKey names the ranking a symbol cursor pages; a hybrid search cursor never
// continues it.
func (r SymbolSearchRequest) rankingKey(workspaceID string) []byte {
	b, _ := json.Marshal([]string{workspaceID, "symbol", r.Query, r.PathGlob, r.Kinds, strconv.FormatBool(r.Substring)})
	return b
}

// CheckNamePath validates a name path as the core parses it: `/` separates non-empty
// segments, a leading `/` makes it absolute, and a trailing `[i]` is an overload index.
func CheckNamePath(namePath string) error {
	segs := strings.Split(strings.TrimPrefix(strings.TrimSpace(namePath), "/"), "/")
	for _, s := range segs {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("name path %q has an empty segment: %w", namePath, ErrInvalidInput)
		}
	}
	last := segs[len(segs)-1]
	open := strings.LastIndexByte(last, '[')
	if open < 0 || !strings.HasSuffix(last, "]") {
		return nil
	}
	if n, err := strconv.Atoi(last[open+1 : len(last)-1]); err != nil || n < 0 || strings.TrimSpace(last[:open]) == "" {
		return fmt.Errorf("name path %q needs a name and a non-negative overload index, as in name[1]: %w", namePath, ErrInvalidInput)
	}
	return nil
}

// checkRange rejects, never clamps, an integer argument outside [0, hi].
func checkRange(name string, v, hi int) error {
	if v < 0 || v > hi {
		return fmt.Errorf("%s must be between 0 and %d (got %d): %w", name, hi, v, ErrInvalidInput)
	}
	return nil
}

// coreFlags renders `--name=value` for every pair with a value, in order.
func coreFlags(kv ...string) []string {
	var out []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			out = append(out, "--"+kv[i]+"="+kv[i+1])
		}
	}
	return out
}

func nonZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func onFlag(b bool) string {
	if b {
		return "true"
	}
	return ""
}

// SymbolSearchCtx runs one page of search mode=symbol and adds its cursor.
func SymbolSearchCtx(ctx context.Context, dataDir, workspaceID string, req SymbolSearchRequest) (json.RawMessage, error) {
	if err := CheckNamePath(req.Query); err != nil {
		return nil, err
	}
	if len(req.PathGlob) > maxSearchPathGlobLen {
		return nil, fmt.Errorf("path_glob is longer than %d bytes: %w", maxSearchPathGlobLen, ErrInvalidInput)
	}
	if err := checkRange("depth", req.Depth, NavMaxDepth); err != nil {
		return nil, err
	}
	ranking := req.rankingKey(workspaceID)
	offset, err := decodeSearchCursor(req.Cursor, ranking)
	if err != nil {
		return nil, err
	}
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	limit = min(limit, searchWindow-offset)
	read := ensureCodeIndex(ctx, root)
	// flags lead the positionals: the core stops reading flags at the root
	args := []string{"find-symbol"}
	args = append(append(args, read.flags()...), coreFlags(
		"offset", nonZero(offset), "max-matches", strconv.Itoa(limit), "path-glob", req.PathGlob, "kinds", req.Kinds,
		"depth", nonZero(req.Depth), "substring", onFlag(req.Substring), "include-body", onFlag(req.IncludeBody),
		"include-info", onFlag(req.IncludeInfo),
	)...)
	args = append(args, root, workspaceID, req.Query)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	return withListCursor(read.annotate(out), "symbols", offset, ranking), nil
}

// ExplainRequest is one explain call: a path (a file or a directory), or a symbol
// (a name path or a UID).
type ExplainRequest struct {
	Path, Symbol, UID string
	// Depth lists children to this level (-1: 1 for a file, 0 for a directory).
	Depth int
	// ChainDepth adds layers past a symbol's direct edges.
	ChainDepth     int
	IncludeContent bool
	// Caller is the principal asking, for the memory a symbol's context attaches.
	Caller string
}

// ExplainCtx answers explain: a file's explanation with its outline and cluster, a
// directory's outline, or a symbol's context with the memory on its file.
func ExplainCtx(ctx context.Context, dataDir, workspaceID string, req ExplainRequest) (json.RawMessage, error) {
	targets := 0
	for _, t := range []string{req.Path, req.Symbol, req.UID} {
		if strings.TrimSpace(t) != "" {
			targets++
		}
	}
	if targets != 1 {
		return nil, fmt.Errorf("explain takes one of path=, symbol= or uid=: %w", ErrInvalidInput)
	}
	if err := checkRange("chain_depth", req.ChainDepth, NavMaxChainDepth); err != nil {
		return nil, err
	}
	if req.Depth != -1 {
		if err := checkRange("depth", req.Depth, NavMaxDepth); err != nil {
			return nil, err
		}
	}
	if req.Path == "" {
		return explainSymbol(ctx, dataDir, workspaceID, req)
	}
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	rel, err := ConfineWorkspacePath(root, req.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return overviewOf(ctx, root, workspaceID, rel, depthOr(req.Depth, 0))
	}
	return explainFile(ctx, dataDir, workspaceID, req.Caller, root, rel, depthOr(req.Depth, 1))
}

func depthOr(depth, fallback int) int {
	if depth < 0 {
		return fallback
	}
	return depth
}

// overviewOf is the core's outline of a file or directory.
func overviewOf(ctx context.Context, root, workspaceID, rel string, depth int) (json.RawMessage, error) {
	read := ensureCodeIndex(ctx, root)
	args := []string{"overview"}
	args = append(append(args, read.flags()...), "--depth="+strconv.Itoa(depth), root, workspaceID, rel)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	return read.annotate(out), nil
}

// overviewMembers are the members of a file's outline an explanation carries.
var overviewMembers = []string{"symbols", "symbols_total", "symbols_by_kind", "symbols_truncated", "depth", "degradations"}

// explainFile is the file's explanation, its cluster (its functional neighbourhood)
// and the freshness and coverage of the graph behind it, plus its outline.
func explainFile(ctx context.Context, dataDir, workspaceID, caller, root, rel string, depth int) (json.RawMessage, error) {
	explanation, err := ExplainPathCtx(ctx, dataDir, workspaceID, rel)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	// the cluster and the graph envelope are best-effort, as before the outline
	if graph, gerr := PathGraphCtx(ctx, dataDir, workspaceID, rel); gerr == nil {
		if graph.Cluster != nil {
			out["explanation"], out["cluster"] = explanation, graph.Cluster
			out["freshness"], out["coverage"] = graph.Freshness, graph.Coverage
		} else {
			explanation.Freshness, explanation.Coverage = graph.Freshness, graph.Coverage
		}
	}
	if len(out) == 0 {
		raw, err := json.Marshal(explanation)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	// the memories anchored to the file, with their trust and stale labels (WS-28)
	key, memories := anchoredMemories(ctx, dataDir, workspaceID, caller, "", []string{rel})
	out[key] = memories
	// the outline is best-effort too: a core failure is named, not fatal
	var members map[string]json.RawMessage
	outline, err := overviewOf(ctx, root, workspaceID, rel, depth)
	if err == nil {
		err = json.Unmarshal(outline, &members)
	}
	if err != nil {
		out["degradations"] = []string{"outline unavailable: " + err.Error()}
		return json.Marshal(out)
	}
	for _, k := range overviewMembers {
		if v, ok := members[k]; ok {
			out[k] = v
		}
	}
	return json.Marshal(out)
}

// explainSymbol is the core's context of a symbol, with the served memory on the
// symbol's file that its name ranks.
func explainSymbol(ctx context.Context, dataDir, workspaceID string, req ExplainRequest) (json.RawMessage, error) {
	target := req.UID
	if target == "" {
		if err := CheckNamePath(req.Symbol); err != nil {
			return nil, err
		}
		target = req.Symbol
	}
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"context"}
	args = append(append(args, read.flags()...), coreFlags("chain-depth", nonZero(req.ChainDepth), "include-content", onFlag(req.IncludeContent))...)
	args = append(args, root, workspaceID, target)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	return withSymbolMemories(ctx, dataDir, workspaceID, req.Caller, read.annotate(out)), nil
}

// withSymbolMemories attaches the served memory whose paths overlap the symbol's file,
// ranked by the symbol's name, as compact lines. It never fails the explanation: a
// recall that fails is named under memories_unavailable.
func withSymbolMemories(ctx context.Context, dataDir, workspaceID, caller string, raw json.RawMessage) json.RawMessage {
	var found struct {
		Found  bool `json:"found"`
		Symbol struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"symbol"`
	}
	if json.Unmarshal(raw, &found) != nil || !found.Found {
		return raw
	}
	return withMemories(ctx, dataDir, workspaceID, caller, raw, found.Symbol.Name, []string{found.Symbol.Path})
}

// impactMemoryPaths bounds the direct dependents' files impact looks for memory on.
const impactMemoryPaths = 10

// WithImpactMemories attaches to an impact result the served memory anchored to the
// files the target is defined in and its direct dependents' (depth 1), as compact lines
// with trust and stale labels (WS-28). paths adds files the request named; a result that
// is not a graph answer is returned as it is.
func WithImpactMemories(ctx context.Context, dataDir, workspaceID, caller, query string, paths []string, raw json.RawMessage) json.RawMessage {
	var res struct {
		DefinedIn []string `json:"defined_in"`
		ByDepth   []struct {
			Depth int `json:"depth"`
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
		} `json:"by_depth"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return raw
	}
	files := append(slices.Clone(paths), res.DefinedIn...)
	var dependents []string
	for _, tier := range res.ByDepth {
		if tier.Depth == 1 {
			for _, it := range tier.Items {
				dependents = append(dependents, it.Path)
			}
		}
	}
	files = append(files, dependents[:min(len(dependents), impactMemoryPaths)]...)
	return withMemories(ctx, dataDir, workspaceID, caller, raw, query, cleanPaths(files))
}

// withMemories attaches memories to a JSON object result, or returns it unchanged when
// it is not one.
func withMemories(ctx context.Context, dataDir, workspaceID, caller string, raw json.RawMessage, query string, paths []string) json.RawMessage {
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil || res == nil {
		return raw
	}
	key, v := anchoredMemories(ctx, dataDir, workspaceID, caller, query, paths)
	res[key], _ = json.Marshal(v)
	out, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return out
}

// anchoredMemories is the member an explanation or impact result carries: the served
// memory recall ranks for query and paths (compact lines: trust, state and stale
// labels) under "memories", or the reason it could not under "memories_unavailable".
// It never fails the answer it decorates.
func anchoredMemories(ctx context.Context, dataDir, workspaceID, caller, query string, paths []string) (string, any) {
	rec, err := RecallWith(ctx, dataDir, workspaceID, RecallRequest{
		Query: query, Paths: paths, Limit: navMemories, Render: "compact", Caller: caller,
	})
	if err != nil {
		return "memories_unavailable", err.Error()
	}
	return "memories", rec["entries"]
}

// explainCursors page the list an explain result returns (a file's symbols, a
// directory's files): the offset of the next page.
var explainCursors = cursorCodec{kind: "e1", fields: 1, maxLen: maxSearchCursorLen}

// explainRanking names the list an explain cursor pages: the workspace and every
// argument that selects or shapes it.
func explainRanking(workspaceID string, q url.Values) []byte {
	b, _ := json.Marshal([]string{workspaceID, q.Get("path"), q.Get("symbol"), q.Get("uid"), q.Get("depth")})
	return b
}

// ExplainCursor signs the cursors of one explain list.
func ExplainCursor(workspaceID string, q url.Values) func(offset int) string {
	ranking := explainRanking(workspaceID, q)
	return func(offset int) string { return explainCursors.encode(ranking, offset) }
}

// DecodeExplainCursor returns the offset an explain cursor continues from; "" is 0. A
// cursor that is over-long, malformed, edited or from another list is rejected.
func DecodeExplainCursor(workspaceID string, q url.Values, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	nums, ok := explainCursors.decode(cursor, explainRanking(workspaceID, q))
	if !ok || nums[0] <= 0 {
		return 0, fmt.Errorf("cursor is not a next_cursor of this explain call: %w", ErrInvalidInput)
	}
	return nums[0], nil
}

// impactRanking names the tier page an impact cursor pages: the workspace and every
// argument that selects or shapes it. A blast tier and a diff-scope tier page
// differently, so the form's own target is part of the key, and every parameter
// impactFlags accepts for either form is in it -- scope= also bounds its walk with
// symbols=, so leaving that one out let a cursor from one ranking page another
// (WS-59e-r5-1). The names are the query's own, not the tool's: the MCP and Pi
// builders both send max_depth as depth (tool_impact.go's Build, and the Pi mirror's
// k === "max_depth" ? "depth" : k), and ImpactFlags reads params["depth"], so a key
// reading max_depth would be empty on the wire and depth would never be bound.
func impactRanking(workspaceID string, q url.Values) []byte {
	b, _ := json.Marshal([]string{
		workspaceID, q.Get("symbol"), q.Get("path"), q.Get("from"), q.Get("to"), q.Get("scope"),
		q.Get("base"), q.Get("head"), q.Get("worktree"), q.Get("direction"), q.Get("depth"),
		q.Get("relation_types"), q.Get("min_confidence"), q.Get("include_tests"), q.Get("limit"),
		q.Get("timeout_ms"), q.Get("summary_only"), q.Get("symbols"),
	})
	return b
}

// ImpactCursor signs the cursors of one impact tier page. An offset at or below the
// first page signs nothing: a cursor names a position after the last item returned.
// One past the last position the forms accept signs nothing either, so the answer
// never hands back a cursor its own validator would refuse on the next call
// (WS-59e-r5-2).
func ImpactCursor(workspaceID string, q url.Values) func(offset int) string {
	ranking := impactRanking(workspaceID, q)
	return func(offset int) string {
		if offset <= 0 || offset > MaxImpactOffset {
			return ""
		}
		return impactCursors.encode(ranking, offset)
	}
}

// DecodeImpactCursor returns the offset an impact cursor continues from; "" is 0. A
// cursor that is over-long, malformed, edited or from another ranking is rejected, and
// so is a zero offset, which no page hands out.
func DecodeImpactCursor(workspaceID string, q url.Values, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	nums, ok := impactCursors.decode(cursor, impactRanking(workspaceID, q))
	if !ok || nums[0] <= 0 {
		return 0, fmt.Errorf("cursor is not a next_cursor of this impact call: %w", ErrInvalidInput)
	}
	return nums[0], nil
}

// SearchCursor signs the cursors of one search ranking, by mode: a symbol cursor and a
// hybrid one never continue each other. `lang` and `mode` are part of the ranking key, so
// they must be read here too: a budget-truncated search continues through this function,
// and a cursor signed without them would be refused by the request that holds them.
func SearchCursor(workspaceID string, q url.Values) func(offset int) string {
	ranking := SearchRequest{
		Query: q.Get("q"), Seed: q.Get("seed"), PathGlob: q.Get("path_glob"),
		Lang: q.Get("lang"), Mode: q.Get("mode"),
	}.rankingKey(workspaceID)
	if q.Get("mode") == "symbol" {
		ranking = symbolSearchRequest(q).rankingKey(workspaceID)
	}
	return func(offset int) string {
		if offset <= 0 || offset >= searchWindow {
			return ""
		}
		return encodeSearchCursor(offset, ranking)
	}
}

// symbolSearchRequest reads search mode=symbol's arguments from a query. Substring
// parses the same way ParseSymbolSearch does (strconv.ParseBool), so a cursor signed
// here matches the ranking key the handler builds from a validated request.
func symbolSearchRequest(q url.Values) SymbolSearchRequest {
	substring, _ := boolParam(q, "substring")
	return SymbolSearchRequest{Query: q.Get("q"), PathGlob: q.Get("path_glob"), Kinds: q.Get("kinds"), Substring: substring}
}

// ParseSymbolSearch reads search mode=symbol's arguments, rejecting malformed ones.
func ParseSymbolSearch(q url.Values, limit int) (SymbolSearchRequest, error) {
	req := symbolSearchRequest(q)
	req.Limit, req.Cursor = limit, q.Get("cursor")
	var err error
	if req.Depth, err = intParam(q, "depth"); err != nil {
		return req, err
	}
	for _, b := range []struct {
		name string
		dst  *bool
	}{{"include_body", &req.IncludeBody}, {"include_info", &req.IncludeInfo}, {"substring", &req.Substring}} {
		if *b.dst, err = boolParam(q, b.name); err != nil {
			return req, err
		}
	}
	return req, nil
}

// ParseExplain reads explain's arguments, rejecting malformed ones.
func ParseExplain(q url.Values, caller string) (ExplainRequest, error) {
	req := ExplainRequest{Path: q.Get("path"), Symbol: q.Get("symbol"), UID: q.Get("uid"), Depth: -1, Caller: caller}
	var err error
	if q.Has("depth") {
		if req.Depth, err = intParam(q, "depth"); err != nil {
			return req, err
		}
	}
	if req.ChainDepth, err = intParam(q, "chain_depth"); err != nil {
		return req, err
	}
	req.IncludeContent, err = boolParam(q, "include_content")
	return req, err
}

// intParam reads an optional non-negative integer; absent is 0.
func intParam(q url.Values, name string) (int, error) {
	v := strings.TrimSpace(q.Get(name))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, ErrInvalidInput)
	}
	return n, nil
}

// boolParam reads an optional boolean; absent is false.
func boolParam(q url.Values, name string) (bool, error) {
	v := strings.TrimSpace(q.Get(name))
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", name, ErrInvalidInput)
	}
	return b, nil
}
