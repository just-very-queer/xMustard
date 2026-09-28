package toolbudget

import (
	"fmt"
	"strings"
)

// Markdown documents a tool's contract for agents: the default and bounds, paging,
// every ladder as a table and the report. It is generated from the ladder tables, so
// the documentation cannot drift from what Apply does.
func (s *Spec) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Output budget\n\nThe MCP %s tool always budgets its result: `max_chars` defaults to %d (%d to %d; "+
		"out-of-range values are rejected, not clamped). Over plain HTTP the budget applies only when `max_chars` is given. "+
		"While the result exceeds `max_chars`, the ladder of its shape runs one rung at a time, in order, and stops as soon "+
		"as it fits.", s.Tool, s.DefaultMaxChars, MinMaxChars, MaxMaxChars)
	if s.Paged {
		fmt.Fprintf(&b, " `limit` (1 to %d) pages the list the tool returns and `cursor` continues it; every page says "+
			"`has_more`, `next_cursor` and `offset`.", MaxLimit)
	}
	b.WriteString("\n")
	for _, l := range s.Ladders {
		fmt.Fprintf(&b, "\n%s:\n\n| rung | reduces | what changes |\n|---|---|---|\n", l.Shape)
		for _, r := range l.Rungs {
			target := r.List
			if r.Op == opSignals || r.Op == opFlags {
				target = strings.Join(l.Signals, ", ")
			}
			if r.Op == opOmit {
				target = strings.Join(r.Omit, ", ")
			}
			fmt.Fprintf(&b, "| `%s` | %s: %s | %s |\n", r.Name, target, opDocs[r.Op], r.Doc)
		}
		if l.More != "" {
			fmt.Fprintf(&b, "\nFor more: %s.\n", l.More)
		}
	}
	b.WriteString("\nA list that returns fewer items than its page holds sets `has_more`; a paged list's `next_cursor` continues " +
		"at the first item not returned, so fewer items loses nothing. Signals are never dropped silently: `signal_counts` " +
		"and `signal_flags` keep every flag, count and short text of coverage and freshness (and coverage.loss_counts) and " +
		"move the lengths of the lists and the sizes of the nested objects they remove to `output_budget.signals`; any " +
		"member no rung names is returned as is. When anything was reduced on an MCP call, the unbudgeted result is " +
		"retained as evidence: `recover_uri` pages or searches it with resources/read and `recover_handle` names it for " +
		"clients that expand by handle; `recover_unavailable` says why there is none.\n\n")
	fmt.Fprintf(&b, "A budgeted result ends with `%s`: `max_chars`, `used_chars` (the result's own size), `degraded_stage` (the "+
		"last rung applied, `full` when none), `stages`, `over_budget` (only when true), `lists` (per reduced list: `state`, "+
		"`kept`, `total`, `each_kept` for tiers, `by` group counts), `omitted`, `signals`, `more`, `recover_uri`, `recover_handle`, "+
		"`recover_unavailable` and `docs`.\n", ReportMember)
	return b.String()
}
