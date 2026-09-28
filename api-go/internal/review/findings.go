// Package review turns review findings into anchored, checked evidence (WS-65,
// PAR-REV-04/05). Findings arrive as a findings file or a captured evidence original,
// never as nested MCP arrays; Decode reads them under a closed item schema and fixed
// bounds, and Anchor places each one with the anchor package and runs the deterministic
// checks. A finding is evidence, never a verdict: nothing here approves a change.
package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/anchor"
)

// Input bounds (PAR-REV-04, critic §13.6).
const (
	MaxFindingsBytes = 4 << 20 // the whole input; larger imports would need the heavy slot
	MaxFindings      = 50
	MaxContentRunes  = 2000
	maxPathBytes     = 4096
)

// ErrInvalid marks input that is refused as a whole.
var ErrInvalid = errors.New("invalid findings")

// Categories and severities (OCR's closed sets). An unknown value becomes the fallback
// and the change is recorded.
var (
	categories = enum{fallback: "other", values: set("bug", "security", "performance", "maintainability", "test", "style", "documentation", "other")}
	severities = enum{fallback: "low", values: set("critical", "high", "medium", "low")}
)

type enum struct {
	fallback string
	values   map[string]bool
}

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// normalize folds case and maps an unknown value to the fallback, with a note naming the
// field when it changed anything but case.
func (e enum) normalize(field, v string) (string, string) {
	low := strings.ToLower(strings.TrimSpace(v))
	switch {
	case e.values[low]:
		return low, ""
	case low == "":
		return e.fallback, fmt.Sprintf("%s missing; recorded as %s", field, e.fallback)
	}
	return e.fallback, fmt.Sprintf("%s %q is not a known value; recorded as %s", field, v, e.fallback)
}

// Finding is one reviewer finding after normalization.
type Finding struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExistingCode   string `json:"existing_code,omitempty"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`

	// claimed is the producer's own line range (OCR emits one); the server anchors, and
	// a disagreement is recorded.
	claimed [2]int
	// snippet is ExistingCode normalized, or why it cannot anchor.
	snippet    anchor.Snippet
	snippetErr error
	notes      []string
}

// wireFinding is the closed item schema: xMustard's fields plus the keys OCR's
// `--format json --output` comments carry (start_line, end_line, thinking).
type wireFinding struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExistingCode   string `json:"existing_code"`
	SuggestionCode string `json:"suggestion_code"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Thinking       string `json:"thinking"`
}

// itemKeys are the member names of the item schema, from wireFinding's tags.
var itemKeys = func() []string {
	t := reflect.TypeFor[wireFinding]()
	keys := make([]string, t.NumField())
	for i := range keys {
		keys[i] = t.Field(i).Tag.Get("json")
	}
	return keys
}()

// envelopeKeys are the members an envelope object is read for.
var envelopeKeys = []string{"findings", "comments"}

// Source says where a batch came from: its kind (findings_file or evidence_handle), the
// file or handle, and the size and SHA-256 of the bytes read.
type Source struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Batch is one decoded input.
type Batch struct {
	Source   Source
	Findings []Finding
	// Normalized records what was changed on the way in, input-wide.
	Normalized []string
}

// Read decodes at most MaxFindingsBytes from r.
func Read(r io.Reader) (*Batch, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFindingsBytes+1))
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Decode reads findings from a JSON array, an object with "findings" (xMustard) or with
// "comments" (OCR's `--format json --output`; the envelope's other members are ignored).
// A findings array sent as a JSON string is decoded once and the repair recorded. The
// input is refused whole when it is over MaxFindingsBytes, holds more than MaxFindings
// findings, or a finding has an unknown member, a wrong type or an unusable path. Member
// names are read exactly (exactKeys), so any JSON reader sees the findings that
// Source.SHA256 names.
func Decode(data []byte) (*Batch, error) {
	if len(data) > MaxFindingsBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrInvalid, MaxFindingsBytes)
	}
	sum := sha256.Sum256(data)
	b := &Batch{Source: Source{Bytes: len(data), SHA256: hex.EncodeToString(sum[:])}}
	items, err := b.items(bytes.TrimSpace(data))
	if err != nil {
		return nil, err
	}
	if len(items) > MaxFindings {
		return nil, fmt.Errorf("%w: %d findings, at most %d per input", ErrInvalid, len(items), MaxFindings)
	}
	thinking := 0
	for i, raw := range items {
		if err := exactKeys(raw, itemKeys, true); err != nil {
			return nil, fmt.Errorf("%w: finding %d: %v", ErrInvalid, i, err)
		}
		var w wireFinding
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&w); err != nil {
			return nil, fmt.Errorf("%w: finding %d: %v", ErrInvalid, i, err)
		}
		f, err := normalizeFinding(w)
		if err != nil {
			return nil, fmt.Errorf("%w: finding %d: %v", ErrInvalid, i, err)
		}
		if w.Thinking != "" {
			thinking++
		}
		b.Findings = append(b.Findings, f)
	}
	if thinking > 0 {
		b.Normalized = append(b.Normalized, fmt.Sprintf("thinking dropped from %d findings", thinking))
	}
	return b, nil
}

// items finds the findings array in the accepted shapes.
func (b *Batch) items(data []byte) ([]json.RawMessage, error) {
	if len(data) > 0 && data[0] == '"' { // the whole input is a JSON string
		inner, err := unquoteArray(data)
		if err != nil {
			return nil, err
		}
		b.Normalized = append(b.Normalized, "the findings arrived as a JSON string and were decoded")
		return b.items(inner)
	}
	if len(data) > 0 && data[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return items, nil
	}
	var env struct {
		Findings json.RawMessage `json:"findings"`
		Comments json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: expected a JSON array or an object with findings or comments: %v", ErrInvalid, err)
	}
	if err := exactKeys(data, envelopeKeys, false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var list json.RawMessage
	switch {
	case len(env.Findings) > 0 && len(env.Comments) > 0:
		return nil, fmt.Errorf("%w: both findings and comments are present", ErrInvalid)
	case len(env.Findings) > 0:
		list = env.Findings
	default:
		list = env.Comments
	}
	list = bytes.TrimSpace(list)
	if len(list) == 0 || bytes.Equal(list, []byte("null")) {
		return nil, fmt.Errorf("%w: no findings or comments array", ErrInvalid)
	}
	if list[0] != '[' && list[0] != '"' {
		return nil, fmt.Errorf("%w: findings must be an array", ErrInvalid)
	}
	return b.items(list)
}

// exactKeys refuses an object whose member names one of names in another case, or names
// one twice: encoding/json would accept either, matching a name in any case and letting
// the last repeat win, so another reader could see different values in the same bytes.
// closed also refuses a member that is not in names.
func exactKeys(object []byte, names []string, closed bool) error {
	dec := json.NewDecoder(bytes.NewReader(object))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string) // an object's member names are strings, or Token failed
		i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, key) })
		switch {
		case i < 0 && closed:
			return fmt.Errorf("unknown member %q", key)
		case i >= 0 && names[i] != key:
			return fmt.Errorf("member %q must be written %q", key, names[i])
		case i >= 0 && seen[key]:
			return fmt.Errorf("member %q appears twice", key)
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return nil
}

// unquoteArray decodes a JSON string that holds an array; a string of anything else is
// refused, so a repair never recurses past one level.
func unquoteArray(data []byte) ([]byte, error) {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	inner := bytes.TrimSpace([]byte(s))
	if len(inner) == 0 || inner[0] != '[' {
		return nil, fmt.Errorf("%w: a findings string must hold a JSON array", ErrInvalid)
	}
	return inner, nil
}

// normalizeFinding checks and bounds one finding. Refusals are for input no reviewer
// could mean (no path, a path outside the repository, no content); everything else is
// kept and its normalization recorded.
func normalizeFinding(w wireFinding) (Finding, error) {
	p, err := cleanPath(w.Path)
	if err != nil {
		return Finding{}, err
	}
	content := strings.TrimSpace(w.Content)
	if content == "" {
		return Finding{}, errors.New("content is required")
	}
	f := Finding{Path: p, Content: content, ExistingCode: w.ExistingCode, SuggestionCode: w.SuggestionCode,
		claimed: [2]int{w.StartLine, w.EndLine}}
	if n := utf8.RuneCountInString(content); n > MaxContentRunes {
		f.Content = truncateRunes(content, MaxContentRunes)
		f.note(fmt.Sprintf("content cut from %d to %d characters", n, MaxContentRunes))
	}
	var note string
	f.Category, note = categories.normalize("category", w.Category)
	f.note(note)
	f.Severity, note = severities.normalize("severity", w.Severity)
	f.note(note)
	f.snippet, f.snippetErr = anchor.NewSnippet(w.ExistingCode)
	if errors.Is(f.snippetErr, anchor.ErrSnippetTooLarge) {
		f.ExistingCode = ""
		f.note(fmt.Sprintf("existing_code dropped: %d bytes, %d lines, over the %d-line and %d-byte bound",
			len(w.ExistingCode), strings.Count(w.ExistingCode, "\n")+1, anchor.MaxSnippetLines, anchor.MaxSnippetBytes))
	}
	if anchor.Oversized(w.SuggestionCode) {
		f.SuggestionCode = ""
		f.note("suggestion_code dropped: over the same bound as existing_code")
	}
	return f, nil
}

func (f *Finding) note(n string) {
	if n != "" {
		f.notes = append(f.notes, n)
	}
}

// cleanPath is a repository-relative slash path: no NUL, not absolute, not escaping.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", errors.New("path is required")
	case len(p) > maxPathBytes || strings.ContainsRune(p, 0):
		return "", errors.New("path is not a repository path")
	case path.IsAbs(p):
		return "", fmt.Errorf("path %q is absolute; findings name repository-relative paths", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") || c == "." {
		return "", fmt.Errorf("path %q is outside the repository", p)
	}
	return c, nil
}

func truncateRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
