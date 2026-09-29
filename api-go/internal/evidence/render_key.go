package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Cache-stable rendering and key derivation (PAR-CTX-15, folded into WS-42 from WS-74).
//
// The repeat guard (ledger.go) and the result cache (result_cache.go) both need to know
// when two tool results say the same thing. A result's bytes are not enough: search,
// ground, impact, explain and recall carry generated_at, and search reports how long
// the call took and how it used the graph cache, so two identical calls never produce
// the same bytes. Render splits a JSON result into
//   - Text: the canonical stable rendering (sorted keys, no volatile member), a pure
//     function of the tool, its arguments and the revisions the result depends on;
//   - Live: the live-session sections (who is asking, what this session was shown),
//     keyed separately so a change there never hides behind the stable digest;
//   - Meta: every volatile member removed from Text, by path, for a caller's _meta.
// Freshness stays visible in Text as a coarse bucket (identity_age: under_5s ...), the
// exact age moves to Meta.
//
// Key is the identity of a call: (tool, canonical arguments, repository revision,
// memory-store revision, parser version, source revision, renderer version). A key is
// derivable only when the repository identity is complete and every revision the
// tool's policy needs is present; otherwise nothing is deduplicated or cached for the
// call (fail closed).

// RendererVersion names the stabilization rules below. Changing a rule changes every key
// and digest derived with it, so a rendering made under old rules never matches.
const RendererVersion = "xm-render/1"

// keyVersion prefixes every key digest.
const keyVersion = "xm-key/1"

// Errors a key derivation reports; each makes the call exempt from the ledger and cache.
var (
	ErrIdentityIncomplete = errors.New("repository identity incomplete")
	ErrRevisionMissing    = errors.New("a revision the tool depends on is missing")
	ErrArgsInvalid        = errors.New("tool arguments are not a JSON object")
)

// Revisions are the states a tool result depends on besides its arguments.
type Revisions struct {
	// Repo is the repo-key identity sampled for the call; only a complete identity keys.
	Repo Identity
	// Memory is the memory-store revision (opaque to this package: the caller's
	// monotonic store revision, e.g. the last governance event id).
	Memory string
	// Parser is the version of the analyzer that produced the result (the core's
	// parser_version or build identity), so an upgraded core never serves old output.
	Parser string
	// Source is any other state the tool reads (diagnostics: the diagnostic run it
	// reports), for tools whose policy needs it.
	Source string
}

// needs is a set of revisions a tool's key must carry.
type needs uint8

const (
	needMemory needs = 1 << iota
	needParser
	needSource
)

// revisionFields is the order and the presence rule of the revisions in a key.
var revisionFields = [...]struct {
	name string
	need needs
	get  func(Revisions) string
}{
	{"memory", needMemory, func(r Revisions) string { return r.Memory }},
	{"parser", needParser, func(r Revisions) string { return r.Parser }},
	{"source", needSource, func(r Revisions) string { return r.Source }},
}

// ToolPolicy is how the ledger, the cache and the renderer treat one tool.
type ToolPolicy struct {
	// SideEffecting tools are never deduplicated, annotated or cached.
	SideEffecting bool
	// Exploratory tools (reads, searches) get the looser coarse repeat limit.
	Exploratory bool
	// Cacheable tools' successful results may be served from the result cache.
	Cacheable bool
	// needs are the revisions (besides a complete repository identity) a key must carry.
	needs needs
	// volatile are member paths (dotted, array indices left out) moved to Meta; live are
	// the top-level live-session members moved to Live.
	volatile []string
	live     []string
}

// indexWork are the members of an index-backed result that say how the answer was
// computed, not what it is: they differ between a cold and a warm call with the same
// answer (files parsed, graph cache hit, reparsed and re-resolved counts, which index
// snapshot served it).
var indexWork = []string{"coverage.work", "freshness.refresh", "freshness.snapshot_generation"}

// readTool is the policy of a read-only exploratory tool.
var readTool = ToolPolicy{Exploratory: true}

// sideEffecting is the policy of a tool that may change state.
var sideEffecting = ToolPolicy{SideEffecting: true}

// toolPolicies are xMustard's nine tools. search, explain and impact read the code
// index (parser); every tool that may quote memory needs the memory revision;
// diagnostics reports a stored diagnostic run, which the repository identity does not
// cover, so it needs that run's revision (source). ground and recall carry per-caller
// sections (principal, pending_for_you, already_shown), which are live.
var toolPolicies = map[string]ToolPolicy{
	"search":      {Exploratory: true, Cacheable: true, needs: needMemory | needParser, volatile: indexWork},
	"explain":     {Exploratory: true, Cacheable: true, needs: needMemory | needParser, volatile: indexWork},
	"impact":      {Exploratory: true, Cacheable: true, needs: needMemory | needParser, volatile: indexWork},
	"diagnostics": {Cacheable: true, needs: needSource},
	"ground": {needs: needMemory,
		live: []string{"principal", "pending_for_you", "summary"}},
	"recall": {Exploratory: true, needs: needMemory,
		volatile: []string{"consistency_attempts"}, live: []string{"already_shown", "principal"}},
	"why_failed": {needs: needSource},
	"remember":   sideEffecting,
	"verify":     sideEffecting,
}

// familyPolicies are the other tools, by evidence family (registry.go). Reads,
// searches, listings and diffs are read-only; a shell, test, build, lint, log or git
// command may change state, and an unknown tool (another MCP server's) is treated as
// side-effecting unless its caller declares it read-only (Delivered.ReadOnly).
var familyPolicies = map[Family]ToolPolicy{
	FamilyRead: readTool, FamilyGrep: readTool, FamilyGlob: readTool, FamilyList: readTool, FamilyDiff: readTool,
}

// volatileNames are members that are volatile wherever they appear: when a result was
// made and how long making it took.
var volatileNames = map[string]bool{
	"generated_at": true, "elapsed_ms": true, "duration_ms": true, "lock_wait_ms": true,
	"lock_contended": true, "current_key_cached": true,
}

// bucketRule replaces a volatile age (milliseconds) with a coarse, stable label.
type bucketRule struct {
	as    string
	edges []ageEdge
}

type ageEdge struct {
	below time.Duration
	label string
}

// ageEdges are the freshness buckets. The identity cache reuses an identity for 5 s,
// so almost every current answer is under_5s.
var ageEdges = []ageEdge{
	{5 * time.Second, "under_5s"}, {time.Minute, "under_1m"}, {10 * time.Minute, "under_10m"}, {time.Hour, "under_1h"},
}

// bucketNames are the ages kept in Text as a bucket.
var bucketNames = map[string]bucketRule{
	"identity_age_ms":    {as: "identity_age", edges: ageEdges},
	"current_key_age_ms": {as: "current_key_age", edges: ageEdges},
	"age_ms":             {as: "age", edges: ageEdges},
}

// ToolPolicyFor returns tool's policy. xMustard's own tools are looked up by their bare
// name or a client's namespaced name (mcp__xmustard__search); another MCP server's tool is
// side-effecting whatever its name says (mcp__github__search); a native tool is looked up
// by its family.
func ToolPolicyFor(tool string) ToolPolicy {
	if p, ok := toolPolicies[ownToolName(tool)]; ok {
		return p
	}
	if foreignTool(tool) {
		return sideEffecting
	}
	fam, _ := SelectFamily(Selector{Tool: tool})
	if p, ok := familyPolicies[fam]; ok {
		return p
	}
	return sideEffecting
}

// foreignTool reports another MCP server's tool: namespaced, and not one of xMustard's.
func foreignTool(tool string) bool {
	_, own := toolPolicies[ownToolName(tool)]
	return !own && NamespacedTool(tool)
}

// ownToolName strips the prefixes clients put before xMustard's tool names.
func ownToolName(tool string) string {
	t := strings.ToLower(strings.TrimSpace(tool))
	for _, prefix := range []string{"mcp__xmustard__", "mcp__xmustard-mcp__", "xmustard_", "xmustard."} {
		if rest, ok := strings.CutPrefix(t, prefix); ok {
			return rest
		}
	}
	return t
}

// Key identifies one call: the tool, its canonical arguments and the revisions its
// result depends on, under one renderer version.
type Key struct {
	Tool     string
	Args     string // canonical JSON object
	Rev      Revisions
	Renderer string
	digest   [32]byte
}

// NewKey derives the key of a call. It fails when args is not a JSON object (or empty),
// when the repository identity is incomplete, or when a revision the tool's policy needs
// is empty.
func NewKey(tool string, args []byte, rev Revisions) (Key, error) {
	if !rev.Repo.Complete || rev.Repo.Key == "" {
		return Key{}, ErrIdentityIncomplete
	}
	pol := ToolPolicyFor(tool)
	for _, f := range revisionFields {
		if pol.needs&f.need != 0 && f.get(rev) == "" {
			return Key{}, fmt.Errorf("%w: %s", ErrRevisionMissing, f.name)
		}
	}
	canon, err := CanonicalArgs(args)
	if err != nil {
		return Key{}, err
	}
	k := Key{Tool: ownToolName(tool), Args: canon, Rev: rev, Renderer: RendererVersion}
	h := sha256.New()
	fields := []string{keyVersion, k.Tool, k.Args, rev.Repo.Key}
	for _, f := range revisionFields {
		fields = append(fields, f.get(rev))
	}
	writeFields(h, append(fields, k.Renderer)...)
	h.Sum(k.digest[:0])
	return k, nil
}

// Digest is the key's SHA-256.
func (k Key) Digest() [32]byte { return k.digest }

// RevisionKey is a short digest of the revisions alone (the `revision_key` a cached
// result reports): two calls with the same revision key saw the same states.
func (k Key) RevisionKey() string {
	h := sha256.New()
	fields := []string{keyVersion, k.Rev.Repo.Key}
	for _, f := range revisionFields {
		fields = append(fields, f.get(k.Rev))
	}
	writeFields(h, append(fields, k.Renderer)...)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// writeFields writes each field length-prefixed, so no two field lists hash alike.
func writeFields(h io.Writer, fields ...string) {
	var n [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
}

// CanonicalArgs renders tool arguments as canonical JSON: members sorted by name,
// numbers kept as written, no insignificant whitespace. Empty or null arguments are
// "{}"; anything but a JSON object is refused.
func CanonicalArgs(args []byte) (string, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		return "{}", nil
	}
	v, err := decodeJSON(args)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrArgsInvalid, err)
	}
	switch v.(type) {
	case nil:
		return "{}", nil
	case map[string]any:
	default:
		return "", ErrArgsInvalid
	}
	b, err := canonicalJSON(v)
	return string(b), err
}

// decodeJSON decodes exactly one JSON value, keeping numbers as written.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

// canonicalJSON encodes v with sorted object members and no HTML escaping.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Rendered is a result split into its stable text, its live-session sections and its
// volatile members.
type Rendered struct {
	// Text is the canonical stable rendering (the whole body, unchanged, when it is not
	// JSON).
	Text []byte
	// Live is the canonical JSON object of the live-session sections; nil when none.
	Live []byte
	// Meta maps each removed volatile member's path to its value; nil when none.
	Meta map[string]any
	// JSON reports whether the body was a JSON document (only then was it stabilized).
	JSON bool
}

// Render stabilizes a tool result (PAR-CTX-15). A body that is not one JSON document is
// returned whole as Text: a text result's stability is its tool's.
func Render(tool string, body []byte) Rendered {
	v, err := decodeJSON(body)
	if err != nil {
		return Rendered{Text: body}
	}
	pol := ToolPolicyFor(tool)
	st := &stabilizer{pol: pol}
	if m, ok := v.(map[string]any); ok {
		st.takeLive(m)
	}
	v = st.walk(v, "", "")
	text, err := canonicalJSON(v)
	if err != nil {
		return Rendered{Text: body}
	}
	r := Rendered{Text: text, Meta: st.meta, JSON: true}
	if len(st.live) > 0 {
		r.Live, _ = canonicalJSON(st.live)
	}
	return r
}

// StableDigest is the SHA-256 of the stable text alone: the key of the block a consumer
// may cache or place before the live block (a provider prompt-cache prefix).
func (r Rendered) StableDigest() [32]byte {
	return sha256.Sum256(append([]byte(RendererVersion+"\x00"), r.Text...))
}

// ContentDigest is the SHA-256 of the stable text and the live sections together: what
// the repeat guard compares, so a change in either is a change.
func (r Rendered) ContentDigest() [32]byte {
	h := sha256.New()
	writeFields(h, RendererVersion, string(r.Text), string(r.Live))
	var d [32]byte
	h.Sum(d[:0])
	return d
}

// ContentDigestOf renders body as tool's result and returns its ContentDigest.
func ContentDigestOf(tool string, body []byte) [32]byte {
	return Render(tool, body).ContentDigest()
}

// stabilizer removes the volatile and live members of one decoded result.
type stabilizer struct {
	pol  ToolPolicy
	meta map[string]any
	live map[string]any
}

// takeLive moves the policy's top-level live-session members into the live block.
func (st *stabilizer) takeLive(m map[string]any) {
	for _, name := range st.pol.live {
		if v, ok := m[name]; ok {
			if st.live == nil {
				st.live = map[string]any{}
			}
			st.live[name] = v
			delete(m, name)
		}
	}
}

// walk stabilizes v in place. match is the member path without array indices (what
// policy paths name); full includes them (what Meta records).
func (st *stabilizer) walk(v any, match, full string) any {
	switch t := v.(type) {
	case map[string]any:
		for name, child := range t {
			m, f := joinPath(match, name), joinPath(full, name)
			switch {
			case volatileNames[name] || slices.Contains(st.pol.volatile, m):
				st.note(f, child)
				delete(t, name)
			case bucketNames[name].as != "":
				rule := bucketNames[name]
				st.note(f, child)
				delete(t, name)
				t[rule.as] = rule.label(child)
			default:
				t[name] = st.walk(child, m, f)
			}
		}
	case []any:
		for i, child := range t {
			t[i] = st.walk(child, match, full+"["+strconv.Itoa(i)+"]")
		}
	}
	return v
}

func (st *stabilizer) note(path string, v any) {
	if st.meta == nil {
		st.meta = map[string]any{}
	}
	st.meta[path] = v
}

// label is the bucket of an age in milliseconds; a value that is not a number is
// "unknown".
func (r bucketRule) label(v any) string {
	n, ok := v.(json.Number)
	if !ok {
		return "unknown"
	}
	ms, err := n.Float64()
	if err != nil || ms < 0 {
		return "unknown"
	}
	age := time.Duration(ms * float64(time.Millisecond))
	for _, e := range r.edges {
		if age < e.below {
			return e.label
		}
	}
	return "over_" + strings.TrimPrefix(r.edges[len(r.edges)-1].label, "under_")
}
