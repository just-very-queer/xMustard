package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/anchor"
	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/rustcore"
)

// Code anchors (WS-27, PAR-RCL-02). At propose time a memory's text is read for what it
// is about, deterministically: URLs, commands, error codes, env keys, repository paths,
// config keys and identifiers, and quoted code blocks. Identifiers are resolved against
// the code index through the core worker; one that names a single declaration becomes
// a symbol anchor. Quoted code is placed on the lines of one of the memory's files by
// WS-65's anchor package (locateQuote). Anchors live in the store's anchor table, the
// inverted index anchor -> memories that recall boosts through (anchorBoost), damped by
// how many memories share the anchor. Extracted anchors are not the entry's declared
// paths: drift baselines stay on what the author declared.

// Extraction bounds: anchors kept per memory, the text read for them and the
// filesystem checks its path candidates may cost, names sent for resolution, quoted
// blocks and the files each is looked for in, and how long resolution may take.
const (
	maxExtractedAnchors = 32
	maxAnchorValue      = 256
	maxAnchorText       = 64 << 10
	maxPathProbes       = 64
	maxResolveNames     = 16
	maxQuotedBlocks     = 4
	maxQuoteFiles       = 4
	resolveTimeout      = 3 * time.Second
)

// Quote placement bounds: a file quoted code is looked for in has at most
// maxQuoteFileBytes and maxQuoteFileLines. The placement holds one file at a time with
// the line index anchor.Locate builds over it (lineIndexBytes a line: a number and a
// string header), and reserves that much in the transient pool before reading any.
const (
	maxQuoteFileBytes   = 1 << 20
	maxQuoteFileLines   = 32 << 10
	lineIndexBytes      = 24
	quotePlacementBytes = maxQuoteFileBytes + maxQuoteFileLines*lineIndexBytes
)

// MemoryAnchor is one anchor as remember reports it. SymbolUID is "path#name" for a
// symbol resolved to one declaration; Fanout is how many memories of the workspace that
// the proposer may read share the anchor (readableAnchors), the new one included.
type MemoryAnchor struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	SymbolUID string `json:"symbol_uid,omitempty"`
	Declared  bool   `json:"declared,omitempty"`
	Fanout    int    `json:"fanout,omitempty"`
}

// anchorExtractor finds one kind of anchor. The table runs in order and every span an
// extractor claims is blanked before the next runs, so a URL is never also a path and
// an error code never also an env key. keep normalizes a match or rejects it; consume
// blanks a rejected match too, so a slashed token that names no file is not read again
// as an identifier.
type anchorExtractor struct {
	kind    string
	find    *regexp.Regexp
	keep    func(x *anchorExtraction, match string) (string, bool)
	consume bool
}

// commandLeaders are the programs a backticked command may start with.
var commandLeaders = setOf("go", "make", "cargo", "npm", "npx", "pnpm", "yarn", "bun", "deno", "node", "git", "gh",
	"python", "python3", "pip", "uv", "pytest", "ruff", "docker", "kubectl", "helm", "terraform", "bash", "sh", "just",
	"rustup", "rustc", "gofmt", "rustfmt", "curl", "xmustard-ops", "xmustard-core", "xmustard-api", "sqlite3")

// errnoNames are the POSIX error names read as error codes.
var errnoNames = setOf("ENOENT", "EACCES", "EPERM", "EEXIST", "EADDRINUSE", "ECONNREFUSED", "ECONNRESET", "ETIMEDOUT",
	"EPIPE", "EAGAIN", "EMFILE", "ENOSPC", "EISDIR", "ENOTDIR", "EBADF", "EINVAL", "ENOTEMPTY", "EXDEV")

func setOf(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

var (
	fencedCode = regexp.MustCompile("(?s)```[^\\n]*\\n(.*?)```")
	backticked = regexp.MustCompile("`([^`\\n]{1,256})`")
	// qualifiedName is PAR-RCL-02's identifier pattern.
	qualifiedName = regexp.MustCompile(`[A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*)+`)
	sourceFile    = regexp.MustCompile(`^[\w.@-]+\.(?:go|rs|ts|tsx|js|jsx|mjs|cjs|py|md|json|ya?ml|toml|sql|sh|mod|sum|lock|txt|proto|java|kt|rb|c|h|cc|cpp|hpp|cs|swift|php|vue|svelte|css|scss|html|ini|cfg|conf|env)$`)
	plainName     = regexp.MustCompile(`^[A-Za-z_][\w:#.-]*$`)
)

var anchorExtractors = []anchorExtractor{
	{kind: govstore.AnchorURL, find: regexp.MustCompile(`https?://[^\s<>()` + "`" + `"']+`), keep: keepTrimmed},
	{kind: govstore.AnchorCommand, find: backticked, keep: keepCommand},
	{kind: govstore.AnchorCommand, find: regexp.MustCompile(`(?m)^\s*\$ ([^\n]{1,256})`), keep: keepCommand},
	{kind: govstore.AnchorErrorCode, find: regexp.MustCompile(`\b(?:[A-Z]{1,2}\d{4}|ERR_[A-Z0-9_]+)\b`), keep: keepTrimmed},
	{kind: govstore.AnchorErrorCode, find: regexp.MustCompile(`\bE[A-Z]{3,11}\b`), keep: keepErrno},
	{kind: govstore.AnchorEnv, find: regexp.MustCompile(`\$\{?[A-Z][A-Z0-9_]{2,}\}?|\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`), keep: keepEnv},
	{kind: govstore.AnchorPath, find: regexp.MustCompile(`[\w.@-]*(?:/[\w.@-]+)+`), keep: keepPath, consume: true},
	{kind: govstore.AnchorPath, find: regexp.MustCompile(`[\w.@-]+\.[A-Za-z]{1,5}\b`), keep: keepPath},
	{kind: govstore.AnchorConfig, find: regexp.MustCompile(`\b[A-Za-z][\w-]*(?:\.[\w-]+)+\s*[:=]`), keep: keepConfig},
	{kind: govstore.AnchorIdentifier, find: backticked, keep: keepIdentifier},
	{kind: govstore.AnchorIdentifier, find: qualifiedName, keep: keepIdentifier},
}

// anchorExtraction is one memory's extraction: the files it may name are checked
// against the workspace root, at most maxPathProbes times (probes counts them).
type anchorExtraction struct {
	root    string
	anchors []govstore.AnchorInput
	seen    map[govstore.AnchorRef]bool
	probes  int
}

// extractAnchors reads text for anchors, in anchorExtractors order, and returns them
// with the quoted code blocks it found. root, when known, confirms path anchors.
func extractAnchors(root, text string) ([]govstore.AnchorInput, []string) {
	x := &anchorExtraction{root: root, seen: map[govstore.AnchorRef]bool{}}
	quotes := x.run(text)
	return x.anchors, quotes
}

// run reads the first maxAnchorText bytes of text for anchors and returns its quoted
// code blocks. Once the extraction is full no match is looked at again, so a long text
// costs no filesystem checks past its first 32 anchors.
func (x *anchorExtraction) run(text string) []string {
	var quotes []string
	masked := fencedCode.ReplaceAllStringFunc(anchorText(text), func(block string) string {
		if m := fencedCode.FindStringSubmatch(block); len(quotes) < maxQuotedBlocks && strings.TrimSpace(m[1]) != "" {
			quotes = append(quotes, m[1])
		}
		return blank(block)
	})
	for _, ex := range anchorExtractors {
		if x.full() {
			break
		}
		masked = ex.find.ReplaceAllStringFunc(masked, func(match string) string {
			if x.full() {
				return match
			}
			sub := match
			if m := ex.find.FindStringSubmatch(match); len(m) > 1 {
				sub = m[1]
			}
			if v, ok := ex.keep(x, sub); (ok && x.add(ex.kind, v)) || ex.consume {
				return blank(match)
			}
			return match
		})
	}
	return quotes
}

// anchorText is the part of text anchors are read from: its first maxAnchorText bytes,
// cut back to the last line or word break so no token is read in part.
func anchorText(text string) string {
	if len(text) <= maxAnchorText {
		return text
	}
	text = text[:maxAnchorText]
	if i := strings.LastIndexAny(text, " \t\n"); i > 0 {
		return text[:i]
	}
	return text
}

func (x *anchorExtraction) full() bool { return len(x.anchors) >= maxExtractedAnchors }

// add records an anchor once and reports whether its span is claimed: false when the
// value is not storable or the extraction is full.
func (x *anchorExtraction) add(kind, value string) bool {
	ref := govstore.AnchorRef{Kind: kind, Value: value}
	switch {
	case value == "" || len(value) > maxAnchorValue:
		return false
	case x.seen[ref]:
		return true
	case x.full():
		return false
	}
	x.seen[ref] = true
	x.anchors = append(x.anchors, govstore.AnchorInput{Kind: kind, Value: value, Extracted: true})
	return true
}

// blank replaces a claimed span with spaces of the same length.
func blank(s string) string { return strings.Repeat(" ", len(s)) }

func keepTrimmed(_ *anchorExtraction, m string) (string, bool) {
	v := strings.TrimRight(m, ".,;:!?")
	return v, v != ""
}

func keepCommand(_ *anchorExtraction, m string) (string, bool) {
	v := strings.Join(strings.Fields(m), " ")
	leader, _, _ := strings.Cut(v, " ")
	return v, commandLeaders[leader] || strings.HasPrefix(leader, "./")
}

func keepErrno(_ *anchorExtraction, m string) (string, bool) { return m, errnoNames[m] }

// keepEnv keeps "$NAME" and "${NAME}", and bare names with an underscore.
func keepEnv(_ *anchorExtraction, m string) (string, bool) {
	v := strings.Trim(m, "${}")
	return v, strings.HasPrefix(m, "$") || (len(v) >= 4 && strings.Contains(v, "_"))
}

// keepPath keeps a repository path: confined to the workspace root and naming an
// existing file or directory there, so without a root no path is kept. A trailing
// ":line" is dropped. Each candidate costs a filesystem check, and past maxPathProbes
// of them none is kept.
func keepPath(x *anchorExtraction, m string) (string, bool) {
	v := strings.TrimRight(m, ".,;:!?")
	if x.root == "" || x.probes >= maxPathProbes || (!strings.Contains(v, "/") && !sourceFile.MatchString(v)) {
		return "", false
	}
	x.probes++
	// lexical confinement, then the symlink-resolving one that returns the target
	clean, err := ConfineWorkspacePath("", v)
	if err != nil {
		return "", false
	}
	resolved, err := resolveWorkspacePath(x.root, filepath.FromSlash(clean))
	if err != nil {
		return "", false
	}
	_, err = os.Lstat(resolved)
	return clean, err == nil
}

func keepConfig(_ *anchorExtraction, m string) (string, bool) {
	return strings.TrimSpace(strings.TrimRight(m, ":= \t")), true
}

// keepIdentifier keeps a code identifier; a dotted one needs every segment to be two
// characters or more, so abbreviations such as "e.g" stay prose.
func keepIdentifier(_ *anchorExtraction, m string) (string, bool) {
	v := strings.TrimSpace(m)
	if !plainName.MatchString(v) {
		return "", false
	}
	for _, seg := range strings.Split(v, ".") {
		if len(seg) < 2 {
			return "", false
		}
	}
	return v, true
}

// symbolMatch is one identifier resolved against the code index: how many
// declarations it names, and the first few.
type symbolMatch struct {
	Name        string             `json:"name"`
	Count       int                `json:"count"`
	Definitions []symbolDefinition `json:"definitions"`
}

type symbolDefinition struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// resolveSymbols resolves identifiers against the code index; tests replace it.
var resolveSymbols = resolveSymbolsWithCore

// resolveSymbolsWithCore resolves names through the core worker (`symbolgraph
// resolve`), from the code index at the workspace's current identity or the graph of
// the tree as it is. It never starts an index refresh.
func resolveSymbolsWithCore(ctx context.Context, root, workspaceID string, names []string) ([]symbolMatch, error) {
	read := codeIndexRead{key: codeIndexIdentity(ctx, root).Key}
	args := []string{"resolve"}
	args = append(append(append(args, read.flags()...), root, workspaceID), names...)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	var matches []symbolMatch
	return matches, json.Unmarshal(out, &matches)
}

// resolveAnchors turns identifiers that name exactly one declaration into symbol
// anchors. Resolution is best effort: a failure keeps the identifiers and says why.
func resolveAnchors(ctx context.Context, root, workspaceID string, anchors []govstore.AnchorInput) ([]govstore.AnchorInput, string) {
	var names []string
	for _, a := range anchors {
		if a.Kind == govstore.AnchorIdentifier && len(names) < maxResolveNames {
			names = append(names, a.Value)
		}
	}
	if root == "" || len(names) == 0 {
		return anchors, ""
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	matches, err := resolveSymbols(ctx, root, workspaceID, names)
	if err != nil {
		return anchors, fmt.Sprintf("identifiers were not resolved against the code index: %v", err)
	}
	unique := map[string]string{}
	for _, m := range matches {
		if m.Count == 1 && len(m.Definitions) == 1 {
			unique[m.Name] = m.Definitions[0].Path + "#" + lastSegment(m.Name)
		}
	}
	out := slices.Clone(anchors)
	for i, a := range out {
		if uid, ok := unique[a.Value]; ok && a.Kind == govstore.AnchorIdentifier {
			out[i].Kind, out[i].SymbolUID = govstore.AnchorSymbol, uid
		}
	}
	return out, ""
}

// sameAnchorKinds are the kinds an anchor of kind may be stored under: an identifier and
// the symbol it resolved to name the same thing. The first is the family compared.
func sameAnchorKinds(kind string) []string {
	if kind == govstore.AnchorIdentifier || kind == govstore.AnchorSymbol {
		return []string{govstore.AnchorIdentifier, govstore.AnchorSymbol}
	}
	return []string{kind}
}

func lastSegment(name string) string {
	return name[strings.LastIndexAny(name, ".:#")+1:]
}

// locateQuote places quoted code in a file's content through WS-65's anchor package,
// the one seam between memory anchors and it. ok is false when the code is absent or
// ambiguous there.
func locateQuote(path, content, code string) (start, end int, ok bool) {
	snippet, err := anchor.NewSnippet(code)
	if err != nil {
		return 0, 0, false
	}
	a := anchor.Locate(path, content, snippet)
	return a.StartLine, a.EndLine, a.Status != anchor.Unanchored
}

// quoteAnchors places each quoted block in the first of files that holds it exactly
// once, as a code anchor "path#Lstart-Lend". Files are read one at a time, in order,
// only while a block is unplaced, and only within the quote placement bounds; the
// placement's bytes are reserved in the transient pool first, and a refusal skips it
// with a warning. Each placed anchor carries snippetID as its symbol_uid: WS-28B
// re-anchors a code anchor by the snippet it placed, and the line range in the value is
// exactly what such a move rewrites.
func quoteAnchors(root string, quotes, files []string) ([]govstore.AnchorInput, string) {
	if root == "" || len(quotes) == 0 || len(files) == 0 {
		return nil, ""
	}
	scope := budget.NewScope(nil)
	defer scope.Close()
	if err := scope.Acquire(quotePlacementBytes); err != nil {
		return nil, fmt.Sprintf("quoted code was not placed on file lines: %v", err)
	}
	placed := make([]string, len(quotes))
	unplaced := len(quotes)
	for _, f := range files[:min(len(files), maxQuoteFiles)] {
		if unplaced == 0 {
			break
		}
		content, ok := readWorkspaceText(root, f, maxQuoteFileBytes)
		if !ok || strings.Count(content, "\n") >= maxQuoteFileLines {
			continue
		}
		for i, q := range quotes {
			if placed[i] != "" {
				continue
			}
			if start, end, ok := locateQuote(f, content, q); ok {
				placed[i], unplaced = fmt.Sprintf("%s#L%d-L%d", f, start, end), unplaced-1
			}
		}
	}
	var out []govstore.AnchorInput
	for i, v := range placed {
		if v != "" {
			out = append(out, govstore.AnchorInput{Kind: govstore.AnchorCode, Value: v, Extracted: true, SymbolUID: snippetID(quotes[i])})
		}
	}
	return out, ""
}

// proposalAnchors extracts, resolves and places the anchors of a proposal. declared
// are the author's confined paths; the quoted code is looked for in them first, then in
// the paths the text names.
func proposalAnchors(ctx context.Context, root, workspaceID, text string, declared []string) ([]govstore.AnchorInput, []string) {
	extracted, quotes := extractAnchors(root, text)
	extracted = slices.DeleteFunc(extracted, func(a govstore.AnchorInput) bool {
		return a.Kind == govstore.AnchorPath && slices.Contains(declared, a.Value)
	})
	var warnings []string
	extracted, warn := resolveAnchors(ctx, root, workspaceID, extracted)
	if warn != "" {
		warnings = append(warnings, warn)
	}
	files := slices.Clone(declared)
	for _, a := range extracted {
		if a.Kind == govstore.AnchorPath {
			files = append(files, a.Value)
		}
	}
	placed, warn := quoteAnchors(root, quotes, files)
	if warn != "" {
		warnings = append(warnings, warn)
	}
	return append(extracted, placed...), warnings
}

// Recall's anchor signal (PAR-RCL-02): a memory sharing an anchor with the query gains
// sim·0.5/(1+0.001(n−1)²), where n is how many memories share that anchor, so an
// anchor half the workspace mentions adds almost nothing.
const (
	anchorBoostWeight  = 0.5
	anchorFanoutDamper = 0.001
)

func anchorBoost(sim float64, fanout int) float64 {
	n := float64(max(fanout, 1) - 1)
	return sim * anchorBoostWeight / (1 + anchorFanoutDamper*n*n)
}

// maxQueryAnchorHits bounds the index rows one recall reads per anchor kind.
const maxQueryAnchorHits = 500

// readableAnchors scopes an anchor lookup to the memories a principal may read: the
// active ones, pending included, for a reviewer (the verifier or human-approver role),
// and the served ones otherwise. remember's fan-out, the review's neighbours and
// recall's damping all count through it, so they count the same memories and none
// reveals pending memory to a principal that may not read it.
func readableAnchors(workspaceID string, reviewer bool) govstore.AnchorQuery {
	return govstore.AnchorQuery{WorkspaceID: workspaceID, ActiveOnly: true, ServedOnly: !reviewer}
}

// anchorsWithFanout renders a new entry's anchors with how many memories its author may
// read (readableAnchors) share each. The author always reads its own entry, so it is
// counted where the scope leaves it out.
func anchorsWithFanout(ctx context.Context, r govstore.Reader, e govstore.Entry, anchors []govstore.Anchor,
	reviewer bool) ([]MemoryAnchor, error) {
	scope := readableAnchors(e.WorkspaceID, reviewer)
	self := 0
	if scope.ServedOnly && !e.Served(time.Now()) {
		self = 1
	}
	byKind := map[string][]string{}
	for _, a := range anchors {
		byKind[a.Kind] = append(byKind[a.Kind], a.Value)
	}
	fanout := map[govstore.AnchorRef]int{}
	for _, kind := range slices.Sorted(maps.Keys(byKind)) {
		q := scope
		q.Kind, q.Values = kind, byKind[kind]
		counts, err := r.AnchorFanout(ctx, q)
		if err != nil {
			return nil, err
		}
		for v, n := range counts {
			fanout[govstore.AnchorRef{Kind: kind, Value: v}] = n
		}
	}
	out := make([]MemoryAnchor, len(anchors))
	for i, a := range anchors {
		out[i] = MemoryAnchor{Kind: a.Kind, Value: a.Value, SymbolUID: a.SymbolUID, Declared: a.Declared,
			Fanout: fanout[govstore.AnchorRef{Kind: a.Kind, Value: a.Value}] + self}
	}
	return out, nil
}

// queryAnchorBoosts reads the inverted index, within scope (readableAnchors), for the
// anchors a recall names (in its query text, and its focus paths) and returns each
// memory's damped boost with the anchors behind it. A memory's own declared paths
// already score as paths, so a focus path boosts only the memories that merely mention
// it.
func queryAnchorBoosts(ctx context.Context, r govstore.Reader, scope govstore.AnchorQuery, query string, focus []string) (map[string]float64, map[string][]string, error) {
	extracted, _ := extractAnchors("", query)
	byKind := map[string][]string{}
	for _, a := range extracted {
		for _, kind := range sameAnchorKinds(a.Kind) {
			if kind != govstore.AnchorPath {
				byKind[kind] = append(byKind[kind], a.Value)
			}
		}
	}
	for _, p := range focus {
		byKind[govstore.AnchorPath] = append(byKind[govstore.AnchorPath], p)
	}
	boosts, why := map[string]float64{}, map[string][]string{}
	for _, kind := range slices.Sorted(maps.Keys(byKind)) {
		values := byKind[kind]
		if len(values) == 0 {
			continue
		}
		q := scope
		q.Kind, q.Values, q.Limit = kind, values[:min(len(values), maxResolveNames)], maxQueryAnchorHits
		hits, err := r.EntriesByAnchor(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		for _, h := range hits {
			if h.Kind == govstore.AnchorPath && h.Declared {
				continue
			}
			boosts[h.EntryID] += anchorBoost(1, h.Fanout)
			why[h.EntryID] = append(why[h.EntryID], fmt.Sprintf("%s %s (shared by %d)", h.Kind, h.Value, h.Fanout))
		}
	}
	for id, b := range boosts {
		boosts[id] = math.Round(b*1000) / 1000
	}
	return boosts, why, nil
}
