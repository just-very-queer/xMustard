package groundbudget

import (
	"fmt"
	"strings"
)

// Markdown documents the contract for agents: the sections in rank order with their
// caps, the ladder and the report. It is generated from the section
// table, so the documentation cannot drift from what Apply does.
func Markdown() string {
	var b strings.Builder
	names := SectionNames()
	fmt.Fprintf(&b, "### Output budget\n\n")
	fmt.Fprintf(&b, "The MCP ground tool always budgets its result; `max_chars` defaults to %d. Over plain HTTP the budget "+
		"applies only when `sections` or `max_chars` is given: without either, `GET .../session-grounding` returns the "+
		"unbudgeted result with no `%s` (the Pi adapter and the UI read it that way). `%s` is always returned; `sections` "+
		"selects the others (default: all). `max_chars` bounds the UTF-8 bytes of the JSON result, which are never fewer "+
		"than its characters.\n\n", DefaultMaxChars, ReportMember, names[0])
	b.WriteString("Sections, most important first:\n\n| section | members | cap | contents | get it in full |\n|---|---|---|---|---|\n")
	for _, s := range sections {
		var members []string
		for _, m := range s.Members {
			name := "`" + m.Name + "`"
			if m.Signal {
				name += "*"
			}
			members = append(members, name)
		}
		label := s.Name
		if s.Pinned {
			label += " (always)"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", label, strings.Join(members, ", "), s.Cap, s.Doc, s.Recover)
	}
	b.WriteString("\n`*` marks a failure, stale or trust signal. A signal is never dropped silently: it stays as a count or flag " +
		"while its section is returned, and moves to `output_budget.signals` when its section is omitted or not requested " +
		"(a list as its length; an object as its flags, its kept keys and its nested lists' lengths).\n\n")
	fmt.Fprintf(&b, "Degradation ladder. First, each section is held to its cap; the cap is lifted for the one section "+
		"requested (besides `%s`, or `%s` alone), so `ground sections=NAME max_chars=N` returns that section in full up "+
		"to N. Then, while the result exceeds max_chars, the least important section is reduced one rung at a time; a more "+
		"important section is reduced for max_chars only after every less important one is omitted:\n\n", names[0], names[0])
	b.WriteString("1. `trimmed`: lists keep their first items in result order (failed runs are newest first); `kept` and `total` count them.\n")
	b.WriteString("2. `counts`: lists are removed and counted in `total`; objects keep only their flags (every boolean) and named keys.\n")
	fmt.Fprintf(&b, "3. `omitted`: the section is removed. `%s` is never omitted, only reduced to counts; if even "+
		"that exceeds max_chars, `over_budget` is true.\n\n", names[0])
	fmt.Fprintf(&b, "Members no section declares form the `%s` section: treated as signals, reduced first, and reported by "+
		"name under `sections.%s` (with `members`), also when a narrow `sections` leaves them out, since they cannot be "+
		"requested by name.\n\n", otherSection, otherSection)
	fmt.Fprintf(&b, "Recovery. When anything requested was reduced (or undeclared members were left out) on a delivered call, "+
		"which is every MCP call, the unbudgeted result is retained as evidence: `recover_uri` reads it with resources/read "+
		"in pages of at most 64 KiB, or searches it (`&pattern=RE2`, `&lines=A-B`), and `recover_handle` names the same "+
		"original for clients that expand by handle (Pi: xmustard_expand). This is the way to anything a section cannot "+
		"return within %d characters. `recover_unavailable` says why no handle was issued instead (an evidence quota, or "+
		"a plain HTTP call, which gets the unbudgeted result by omitting sections and max_chars).\n\n", MaxMaxChars)
	fmt.Fprintf(&b, "Every budgeted result ends with `%s`: `max_chars`, `used_chars` (the result's own size), `degraded_stage` "+
		"(the deepest rung any requested section reached), `over_budget` (only when true), `sections` "+
		"(each section not returned in full: `state`, `reason` section_cap, max_chars or not_requested, `cap`, "+
		"`full_chars`, `kept`, `total`, `members`, `recover`), `not_requested`, `signals`, `recover_uri`, "+
		"`recover_handle`, `recover_unavailable`, and `docs` (this resource) when anything was reduced.\n", ReportMember)
	return b.String()
}
