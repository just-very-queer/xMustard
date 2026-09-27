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
	// DefaultMaxChars applies when max_chars is omitted from a budgeted call (the MCP
	// ground tool always budgets): about 1.5k tokens, inside the 10,000-character
	// additionalContext cap a SessionStart hook can inject.
	DefaultMaxChars = 6000
	// MinMaxChars is the smallest budget accepted: the always-returned summary
	// section plus a report of every other section omitted fits in it.
	MinMaxChars = 2000
	// MaxMaxChars is the evidence projection target (64 KiB): a larger ground result
	// would be reduced by evidence delivery anyway. Beyond it the retained
	// unbudgeted result (Recovery) is the way to the rest.
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
	// count (a list) or its flags (an object: every boolean plus its Keep keys),
	// never removed while its section is returned, and when the section is omitted
	// its value moves to output_budget.signals (a list or nested list as its length).
	Signal bool
	// Keep lists the keys an object member keeps at the counts stage, besides a
	// signal's booleans. Nil keeps the whole object (minus nested lists) for a
	// signal and drops it otherwise.
	Keep []string
}

// Section is a named group of result members with one importance rank.
type Section struct {
	Name string
	// Pinned sections are always returned: they are never omitted, only reduced.
	Pinned bool
	// Cap is the section's own budget in characters. It keeps one large section
	// from crowding out the others; it is lifted when the section is the only one
	// requested (besides the pinned summary, or the summary alone), so a caller can
	// get it in full.
	Cap     int
	Members []Member
	// Recover says how to get the section in full after it was reduced.
	Recover string
	Doc     string
}

// otherSection holds members no section declares. A test keeps every member of the
// ground result declared; this is the runtime safety net, ranked least important,
// treated as signals and reported by member name, so nothing a later change adds is
// dropped silently. It cannot be requested by name: only the retained unbudgeted
// result returns it in full once it was reduced.
const otherSection = "other"

// otherRecover is the recover hint of otherSection.
const otherRecover = "recover_uri (undeclared members cannot be requested by section)"

// sections are ordered most important first: the ladder reduces them in reverse.
var sections = []Section{
	{
		Name: "summary", Pinned: true, Cap: 2048,
		Members: []Member{
			{Name: "workspace_id"}, {Name: "summary"},
			{Name: "blocked_by_dirty_state", Signal: true}, {Name: "blocked_by_failing_verification", Signal: true},
			{Name: "unknown", Signal: true}, {Name: "generated_at"},
		},
		Recover: "ground sections=summary lifts its cap",
		Doc:     "one-line summary with every count, the blocked flags, and any fields ground could not determine",
	},
	{
		Name: "runs", Cap: 1536,
		Members: []Member{{Name: "recent_failed_runs", Signal: true}},
		Recover: "ground sections=runs max_chars=N; past 65536 chars, recover_uri; why_failed run_id=ID",
		Doc:     "failed, errored or cancelled runs, newest first",
	},
	{
		Name: "index", Cap: 3072,
		Members: []Member{
			{Name: "changed_files", Signal: true}, {Name: "dirty_symbols", Signal: true},
			{Name: "contract_breaks", Signal: true}, {Name: "broken_contracts", Signal: true},
			{Name: "coverage", Signal: true},
		},
		Recover: "ground sections=index max_chars=N; past 65536 chars, recover_uri",
		Doc:     "working-tree changes against the indexed baseline, changed signatures in the order the index lists them, and per-language symbol coverage",
	},
	{
		Name: "memory", Cap: 768,
		Members: []Member{
			{Name: "stale_memory", Signal: true}, {Name: "stale_memory_checked"}, {Name: "stale_memory_total"},
			{Name: "stale_memory_complete", Signal: true}, {Name: "memory_verification_modes", Signal: true},
		},
		Recover: "ground sections=memory; recall flags each stale memory",
		Doc:     "promoted memory whose files drifted, and promoted memory by verification mode",
	},
	{
		Name: "drift", Cap: 1536,
		// Keep names Rust's DriftReport flags; a signal keeps every boolean anyway
		Members: []Member{
			{Name: "drift", Signal: true, Keep: []string{"stale", "has_baseline", "head_changed", "content_changed", "sibling_clone"}},
			// which baseline ground compared against, and whether it was reset automatically
			{Name: "baseline", Signal: true, Keep: []string{"head", "indexed_at", "auto"}},
		},
		Recover: "ground sections=drift",
		Doc:     "whether the index baseline drifted from the working tree, and why; the baseline's HEAD, when and why it was built",
	},
	{
		Name: "principal", Cap: 512,
		// a trust signal: in open mode, memory this caller writes is
		// self_asserted_open_mode, never peer-verified
		Members: []Member{{Name: "principal", Signal: true, Keep: []string{"id", "role", "open_mode"}}},
		Recover: "ground sections=principal",
		Doc:     "the caller's identity and roles, and whether auth is off (open_mode)",
	},
}

// otherCap bounds the undeclared members together.
const otherCap = 1024

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

// parseSections validates a comma-separated section list. Blank selects every
// section (nil). Names are trimmed and deduplicated; an unknown name is rejected.
func parseSections(list string) (map[string]bool, *ArgError) {
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

// parseMaxChars validates max_chars; blank is the default. Out-of-range values are
// rejected, never clamped.
func parseMaxChars(v string) (int, *ArgError) {
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
	secs, aerr := parseSections(sectionList)
	if aerr != nil {
		return Request{}, aerr
	}
	n, aerr := parseMaxChars(maxChars)
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
