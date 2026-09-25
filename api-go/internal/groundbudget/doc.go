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
	fmt.Fprintf(&b, "`%s` is always returned; `sections` selects the others (default: all). `max_chars` (default %d) "+
		"bounds the UTF-8 bytes of the JSON result, which are never fewer than its characters.\n\n", names[0], DefaultMaxChars)
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
		"while its section is returned, and moves to `output_budget.signals` when its section is omitted or not requested.\n\n")
	fmt.Fprintf(&b, "Degradation ladder. First, each section is held to its cap; the cap is lifted for a section requested alone "+
		"(besides `%s`), so `ground sections=NAME max_chars=N` returns that section in full up to N. Then, while the result "+
		"exceeds max_chars, the least important section is reduced one rung at a time; a more important section is "+
		"reduced for max_chars only after every less important one is omitted:\n\n", names[0])
	b.WriteString("1. `trimmed`: lists keep their first (newest) items; `kept` and `total` count them.\n")
	b.WriteString("2. `counts`: lists are removed and counted in `total`; objects keep only their flags.\n")
	fmt.Fprintf(&b, "3. `omitted`: the section is removed. `%s` is never omitted, only reduced to counts; if even "+
		"that exceeds max_chars, `over_budget` is true.\n\n", names[0])
	fmt.Fprintf(&b, "Members no section declares form the `%s` section, reduced first and reported by name.\n\n", otherSection)
	fmt.Fprintf(&b, "Every result ends with `%s`: `max_chars`, `used_chars` (the result's own size), `degraded_stage` "+
		"(the deepest rung any requested section reached), `over_budget` (only when true), `sections` "+
		"(each section not returned in full: `state`, `reason` section_cap or max_chars, `cap`, `full_chars`, `kept`, "+
		"`total`, `recover`), `not_requested`, `signals`, and `docs` (this resource) when anything was reduced.\n", ReportMember)
	return b.String()
}
