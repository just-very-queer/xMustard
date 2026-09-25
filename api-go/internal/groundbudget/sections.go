// Package groundbudget is the output-budget contract of the `ground` tool (WS-54,
// PAR-CTX-05 for ground). ground is the first call of every session, so its result is
// split into named sections, each with an importance rank and a per-section cap. A
// caller may select sections and a total max_chars. When the result does not fit, a
// documented degradation ladder reduces the least important section first, and a
// failure, stale or trust signal is never dropped silently: it survives as a count
// or flag, or moves to the output_budget report with a hint for getting it in full.
//
// The package is dependency-free (encoding/json only) so the MCP shim can validate
// arguments and render the contract's documentation without linking workspaceops.
package groundbudget

import (
	"fmt"
	"strconv"
	"strings"
)

// Budget bounds, in characters of the JSON result (enforced on its UTF-8 bytes,
// which are never fewer than its characters).
const (
	// DefaultMaxChars applies when max_chars is omitted: about 1.5k tokens, inside
	// the 10,000-character additionalContext cap a SessionStart hook can inject.
	DefaultMaxChars = 6000
	// MinMaxChars is the smallest budget accepted: the always-returned summary
	// section plus a report of every other section omitted fits in it.
	MinMaxChars = 2000
	// MaxMaxChars is the evidence projection target (64 KiB): a larger ground result
	// would be reduced by evidence delivery anyway.
	MaxMaxChars = 65536
	// ReportMember is the result member that reports what the budget did.
	ReportMember = "output_budget"
	// DocsURI is the MCP resource that documents this contract.
	DocsURI = "xmustard://docs/tools"
)

// Member is one top-level member of the ground result.
type Member struct {
	Name string
	// Signal marks a failure, stale or trust signal. It is reduced at most to its
	// count (a list) or its flags (an object), never removed while its section is
	// returned, and when the section is omitted its value moves to
	// output_budget.signals.
	Signal bool
	// Keep lists the keys an object member keeps at the counts stage. Nil keeps the
	// whole object (minus nested lists) for a signal and drops it otherwise.
	Keep []string
}

// Section is a named group of result members with one importance rank.
type Section struct {
	Name string
	// Pinned sections are always returned: they are never omitted, only reduced.
	Pinned bool
	// Cap is the section's own budget in characters. It keeps one large section
	// from crowding out the others; it is lifted when the section is the only one
	// requested besides the pinned summary, so a caller can page it in full.
	Cap     int
	Members []Member
	// Recover says how to get the section in full after it was reduced.
	Recover string
	Doc     string
}

// otherSection holds members no section declares. A test keeps every member of the
// ground result declared; this is the runtime safety net, ranked least important and
// reported by member name, so nothing a later change adds is dropped silently.
const otherSection = "other"

// sections are ordered most important first: the ladder reduces them in reverse.
var sections = []Section{
	{
		Name: "summary", Pinned: true, Cap: 2048,
		Members: []Member{
			{Name: "workspace_id"}, {Name: "summary"},
			{Name: "blocked_by_dirty_state", Signal: true}, {Name: "blocked_by_failing_verification", Signal: true},
			{Name: "unknown", Signal: true}, {Name: "generated_at"},
		},
		Recover: "call ground with a larger max_chars",
		Doc:     "one-line summary with every count, the blocked flags, and any fields ground could not determine",
	},
	{
		Name: "runs", Cap: 1536,
		Members: []Member{{Name: "recent_failed_runs", Signal: true}},
		Recover: "ground sections=runs max_chars=N; why_failed run_id=ID per run",
		Doc:     "failed, errored or cancelled runs, newest first",
	},
	{
		Name: "index", Cap: 3072,
		Members: []Member{
			{Name: "changed_files", Signal: true}, {Name: "dirty_symbols", Signal: true},
			{Name: "contract_breaks", Signal: true}, {Name: "broken_contracts", Signal: true},
		},
		Recover: "impact with no arguments lists every changed file and dirty symbol",
		Doc:     "working-tree changes against the indexed baseline and changed signatures",
	},
	{
		Name: "memory", Cap: 768,
		Members: []Member{
			{Name: "stale_memory", Signal: true}, {Name: "stale_memory_checked"}, {Name: "stale_memory_total"},
			{Name: "stale_memory_complete", Signal: true}, {Name: "memory_verification_modes", Signal: true},
		},
		Recover: "recall flags each stale memory; ground sections=memory",
		Doc:     "promoted memory whose files drifted, and promoted memory by verification mode",
	},
	{
		Name: "drift", Cap: 1536,
		Members: []Member{{Name: "drift", Signal: true, Keep: []string{"stale", "has_baseline", "head_changed", "content_changed"}}},
		Recover: "ground sections=drift",
		Doc:     "whether the index baseline drifted from the working tree, and why",
	},
	{
		Name: "principal", Cap: 512,
		Members: []Member{{Name: "principal", Keep: []string{"id", "role", "open_mode"}}},
		Recover: "ground sections=principal",
		Doc:     "the caller's identity and roles",
	},
}

// otherCap bounds the undeclared members together.
const otherCap = 1024

// Sections returns the declared sections, most important first.
func Sections() []Section {
	out := make([]Section, len(sections))
	copy(out, sections)
	return out
}

// SectionNames lists the names sections= accepts, most important first.
func SectionNames() []string {
	out := make([]string, len(sections))
	for i, s := range sections {
		out[i] = s.Name
	}
	return out
}

// SectionOf names the section that declares member ("" when none does).
func SectionOf(member string) string {
	for _, s := range sections {
		for _, m := range s.Members {
			if m.Name == member {
				return s.Name
			}
		}
	}
	return ""
}

// Request is a validated budget request.
type Request struct {
	// Sections selects sections by name; nil selects every section, including
	// members no section declares. The pinned summary is always selected.
	Sections map[string]bool
	MaxChars int
}

// ArgError rejects an argument; it names the argument so a caller can point at it.
type ArgError struct {
	Argument string
	Reason   string
	Extra    map[string]any
}

func (e *ArgError) Error() string { return fmt.Sprintf("argument %q %s", e.Argument, e.Reason) }

// ParseSections validates a comma-separated section list. Blank selects every
// section (nil). Names are trimmed and deduplicated; an unknown name is rejected.
func ParseSections(list string) (map[string]bool, *ArgError) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	valid := SectionNames()
	out := map[string]bool{}
	for _, part := range strings.Split(list, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !contains(valid, name) {
			return nil, &ArgError{Argument: "sections", Reason: fmt.Sprintf("names unknown section %q; use: %s", name, strings.Join(valid, ", ")),
				Extra: map[string]any{"enum": valid}}
		}
		out[name] = true
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ParseMaxChars validates max_chars; blank is the default. Out-of-range values are
// rejected, never clamped.
func ParseMaxChars(v string) (int, *ArgError) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultMaxChars, nil
	}
	n, err := strconv.Atoi(v)
	bounds := map[string]any{"minimum": MinMaxChars, "maximum": MaxMaxChars}
	if err != nil {
		return 0, &ArgError{Argument: "max_chars", Reason: "must be an integer", Extra: bounds}
	}
	if n < MinMaxChars || n > MaxMaxChars {
		return 0, &ArgError{Argument: "max_chars", Reason: fmt.Sprintf("must be between %d and %d (got %d); out-of-range values are rejected, not clamped",
			MinMaxChars, MaxMaxChars, n), Extra: bounds}
	}
	return n, nil
}

// ParseRequest validates the sections and max_chars arguments.
func ParseRequest(sectionList, maxChars string) (Request, *ArgError) {
	secs, aerr := ParseSections(sectionList)
	if aerr != nil {
		return Request{}, aerr
	}
	n, aerr := ParseMaxChars(maxChars)
	if aerr != nil {
		return Request{}, aerr
	}
	return Request{Sections: secs, MaxChars: n}, nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
