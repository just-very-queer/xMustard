package toolbudget

// Ladder is the degradation ladder of one result shape.
type Ladder struct {
	// Shape names the result shape in the documentation.
	Shape string
	// Match is a member that identifies the shape; "" matches any result.
	Match string
	// Lists are the lists the rungs reduce. "a.b" is list b inside object a.
	Lists []List
	// Signals are objects that carry failure, stale or trust signals (coverage,
	// freshness). The signal_counts rung replaces their nested lists with the lists'
	// lengths in output_budget.signals, and signal_flags their nested objects (but the
	// Keep ones) with their sizes; their scalars always stay.
	Signals []string
	// Keep names the nested signal objects signal_flags keeps ("coverage.loss_counts").
	Keep []string
	// Rungs run in order until the result fits.
	Rungs []Rung
	// More says how to get more than the budget returned without the recover handle.
	More string
}

// List is one list a ladder reduces.
type List struct {
	Member string
	// GroupBy is the item member the counts rung groups by; the list becomes
	// <list>_by_<GroupBy>, and a cut list reports the same counts.
	GroupBy string
	// Paged marks the list next_cursor continues: fewer items is lossless.
	Paged bool
}

// op is what a rung does.
type op int

const (
	opShape   op = iota // drop and clip members of every item of one list
	opOmit              // remove result members ("a" or "a.b"), named in the report
	opSignals           // lists inside signal objects become their lengths
	opFlags             // signal objects keep their scalars and Keep members
	opFit               // keep the first k items that fit (k >= 1)
	opFitEach           // keep the first k elements of an item member list, the same k for every item
	opCount             // replace the list with its counts by GroupBy
	opTotal             // remove the list; its total stays in the report
)

// opDocs describes each operation for the documentation.
var opDocs = map[op]string{
	opShape:   "detail",
	opOmit:    "members",
	opSignals: "signals",
	opFlags:   "signals",
	opFit:     "fewer items",
	opFitEach: "fewer items per group",
	opCount:   "counts",
	opTotal:   "total only",
}

// Rung is one step of a ladder.
type Rung struct {
	Name string
	Op   op
	// List is the list the rung reduces ("" for opSignals).
	List string
	// Drop removes item members; "a.b" removes b from every element of the item's
	// list (or object) a.
	Drop []string
	// Clip keeps the first N elements of an item member list.
	Clip map[string]int
	// Each is the item member list opFitEach cuts.
	Each string
	// Omit names the result members opOmit removes ("a.b": member b of object a).
	Omit []string
	Doc  string
}

// signalCounts is the shared rung that reduces signal objects' lists to lengths.
var signalCounts = Rung{Name: "signal_counts", Op: opSignals,
	Doc: "lists inside coverage and freshness (loss paths, dirty paths) become their lengths in output_budget.signals; every flag and count stays"}

// signalFlags is the shared rung that reduces signal objects to their scalars.
var signalFlags = Rung{Name: "signal_flags", Op: opFlags,
	Doc: "coverage and freshness keep every flag, count and text and coverage's loss_counts, work (the index work this read did) and source_identity; their other nested objects (per-language coverage, extraction, envelope, refresh counters) go, sized in output_budget.signals"}

var (
	envelope = []string{"coverage", "freshness"}
	// envelopeKeep are the nested envelope objects that are signals themselves: the
	// exact loss counts, the index work this read did (cache hit or miss, files
	// parsed) and whether the source identity was stable
	envelopeKeep = []string{"coverage.loss_counts", "coverage.work", "coverage.source_identity"}
)

// fallback applies when no other ladder of a tool matches: only the signals shrink.
var fallback = &Ladder{Shape: "any other result", Signals: envelope, Keep: envelopeKeep, Rungs: []Rung{signalCounts, signalFlags}}

// keeps reports whether signal_flags keeps the nested signal member name ("a.b").
func (l *Ladder) keeps(name string) bool {
	for _, k := range l.Keep {
		if k == name {
			return true
		}
	}
	return false
}

var searchLadders = []*Ladder{
	{
		Shape: "hybrid (hits)", Match: "hits",
		Lists:   []List{{Member: "hits", GroupBy: "path", Paged: true}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			{Name: "one_snippet", List: "hits", Drop: []string{"reason"}, Clip: map[string]int{"snippet": 1},
				Doc: "each hit keeps its best snippet window; reason goes (reasons lists the same text)"},
			signalCounts,
			signalFlags,
			{Name: "brief", List: "hits", Drop: []string{"scores", "reasons", "lanes_matched"},
				Doc: "hits lose scores, reasons and lanes_matched; kind, name, path, line, lines, uid, score and the snippet stay"},
			{Name: "no_snippets", List: "hits", Drop: []string{"snippet"}, Doc: "hits lose their snippet; lines says where to read"},
			{Name: "trimmed", Op: opFit, List: "hits", Doc: "fewer hits, best first; next_cursor continues at the first hit not returned"},
			{Name: "counts", Op: opCount, List: "hits", Doc: "hits become hits_by_path"},
			{Name: "total", Op: opTotal, List: "hits", Doc: "only total and omitted remain"},
		},
		More: "next_cursor continues the ranking; a smaller limit returns fewer hits in full; max_chars up to 65536",
	},
	{
		Shape: "pattern (matches)", Match: "matches",
		Lists:   []List{{Member: "matches", GroupBy: "path"}, {Member: "match_rows"}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			{Name: "no_rows", Op: opTotal, List: "match_rows", Doc: "match_rows (materialization rows repeating matches) go"},
			signalCounts,
			signalFlags,
			{Name: "no_context", List: "matches", Drop: []string{"context_lines", "meta_variables"}, Doc: "matches lose context_lines and meta_variables"},
			{Name: "trimmed", Op: opFit, List: "matches", Doc: "fewer matches, in result order"},
			{Name: "counts", Op: opCount, List: "matches", Doc: "matches become matches_by_path"},
			{Name: "total", Op: opTotal, List: "matches", Doc: "only match_count remains"},
		},
		More: "a narrower path_glob, or max_chars up to 65536",
	},
	{
		Shape: "symbol (name path)", Match: "symbols",
		Lists:   []List{{Member: "symbols", GroupBy: "path", Paged: true}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "no_body", List: "symbols", Drop: []string{"body"}, Doc: "symbols lose body (include_body); lines says where to read"},
			{Name: "no_info", List: "symbols", Drop: []string{"info"}, Doc: "symbols lose info (signature and doc comment)"},
			{Name: "depth0", List: "symbols", Drop: []string{"children"}, Doc: "symbols lose their children (depth)"},
			{Name: "no_refine", Op: opOmit, Omit: []string{"refine"}, Doc: "the refine map goes; total_candidates keeps the count"},
			{Name: "trimmed", Op: opFit, List: "symbols", Doc: "fewer symbols, in rank order; next_cursor continues"},
			{Name: "counts", Op: opCount, List: "symbols", Doc: "symbols become symbols_by_path"},
			{Name: "total", Op: opTotal, List: "symbols", Doc: "only total remains"},
		},
		More: "next_cursor continues; a longer name path (Container/name) or path_glob narrows the match",
	},
	{
		// `mode=exact|regex` (WS-59c): the scan ranks nothing, so its list is occurrences
		// and there is no snippet or reason to trim. The first rung drops the two members a
		// caller can rebuild from `byte` and `len`; the rest reduce the list the same way
		// the hybrid's does.
		Shape:   "exact/regex (spans)",
		Match:   "spans",
		Lists:   []List{{Member: "spans", GroupBy: "path", Paged: true}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			{Name: "no_positions", List: "spans", Drop: []string{"column", "len"},
				Doc: "each span keeps path, line and byte; the column within its line and the match length go"},
			signalCounts,
			signalFlags,
			{Name: "trimmed", Op: opFit, List: "spans", Doc: "fewer spans, in file order; next_cursor continues at the first span not returned"},
			{Name: "counts", Op: opCount, List: "spans", Doc: "spans become spans_by_path"},
			{Name: "total", Op: opTotal, List: "spans", Doc: "only total, omitted and bytes_scanned remain"},
		},
		More: "next_cursor continues; a narrower path_glob or a smaller max_bytes scans less",
	},
	fallback,
}

var explainLadders = []*Ladder{
	{
		Shape: "directory (files)", Match: "files",
		Lists:   []List{{Member: "files", GroupBy: "lang", Paged: true}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "depth0", List: "files", Drop: []string{"symbols.children"}, Doc: "file symbols lose their children"},
			{Name: "names_only", List: "files", Drop: []string{"symbols"}, Doc: "files keep symbols_by_kind counts instead of their symbols"},
			{Name: "trimmed", Op: opFit, List: "files", Doc: "fewer files, in path order; next_cursor continues"},
			{Name: "counts", Op: opCount, List: "files", Doc: "files become files_by_lang"},
			{Name: "total", Op: opTotal, List: "files", Doc: "only files_total and symbols_by_kind remain"},
		},
		More: "next_cursor continues; explain a subdirectory or one file for more detail",
	},
	{
		Shape: "symbol context (incoming, outgoing)", Match: "incoming",
		Lists: []List{
			{Member: "memories"}, {Member: "outgoing", GroupBy: "relation"}, {Member: "incoming", GroupBy: "relation"},
		},
		Signals: append([]string{"epistemic"}, envelope...),
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "no_snippets", List: "incoming", Drop: []string{"snippet"}, Doc: "incoming references lose their snippet"},
			{Name: "memories_trimmed", Op: opFit, List: "memories", Doc: "fewer attached memories, best first"},
			{Name: "outgoing_trimmed", Op: opFit, List: "outgoing", Doc: "fewer outgoing edges, nearest layer first"},
			{Name: "no_content", Op: opOmit, Omit: []string{"symbol.content"}, Doc: "the symbol loses content (include_content)"},
			{Name: "incoming_trimmed", Op: opFit, List: "incoming", Doc: "fewer incoming edges, nearest layer first"},
			{Name: "counts", Op: opCount, List: "outgoing", Doc: "outgoing becomes outgoing_by_relation"},
			{Name: "incoming_counts", Op: opCount, List: "incoming", Doc: "incoming becomes incoming_by_relation"},
		},
		More: "chain_depth=0 keeps the direct layer; max_chars up to 65536",
	},
	{
		Shape: "file (symbols)", Match: "symbols",
		Lists:   []List{{Member: "memories"}, {Member: "cluster.files"}, {Member: "symbols", GroupBy: "kind", Paged: true}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "memories_trimmed", Op: opFit, List: "memories", Doc: "fewer attached memories, best first"},
			{Name: "cluster_trimmed", Op: opFit, List: "cluster.files", Doc: "the cluster lists fewer member files (size keeps the count)"},
			{Name: "depth0", List: "symbols", Drop: []string{"children"}, Doc: "symbols lose their children (depth 0)"},
			{Name: "trimmed", Op: opFit, List: "symbols", Doc: "fewer symbols, in line order; next_cursor continues"},
			{Name: "counts", Op: opTotal, List: "symbols", Doc: "only symbols_by_kind counts remain"},
		},
		More: "next_cursor continues; depth=0 lists top-level symbols only",
	},
	fallback,
}

var impactLadders = []*Ladder{
	{
		// impact op= (WS-59d, PAR-IMP-03): the symbol-level occurrences. Staged as the
		// requirement names it: snippets go, then the lines, then fewer rows, then
		// per-file counts, then the total. The list is not Paged: impact's own
		// limit/cursor page it (WS-59e generalises that cursor), so the budget layer
		// must not claim to continue it, and search is the only tool whose list it does.
		Shape: "occurrences (op)", Match: "occurrences",
		Lists:   []List{{Member: "occurrences", GroupBy: "path"}},
		Signals: append([]string{"epistemic"}, envelope...),
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "no_snippets", List: "occurrences", Drop: []string{"snippet"},
				Doc: "occurrences lose their snippet; line says where to read"},
			{Name: "no_lines", List: "occurrences", Drop: []string{"line", "snippet"},
				Doc: "occurrences lose their line and snippet; path and name say what to read"},
			{Name: "trimmed", Op: opFit, List: "occurrences",
				Doc: "fewer occurrences, in index order; next_cursor continues at the first occurrence not returned"},
			{Name: "counts", Op: opCount, List: "occurrences",
				Doc: "occurrences become occurrences_by_path"},
			{Name: "total", Op: opTotal, List: "occurrences", Doc: "only total remains"},
		},
		More: "next_cursor continues the list; a smaller max_chars keeps fewer rows, each in full; max_chars up to 65536",
	},
	{
		// impact v2 (WS-35) scope=: the symbols a diff touches and their dependents
		Shape: "diff scope (changed_symbols)", Match: "changed_symbols",
		Lists: []List{
			{Member: "changed_files", GroupBy: "change"}, {Member: "unmapped_files"}, {Member: "affected.affected_modules"}, {Member: "affected.by_depth"},
			{Member: "changed_symbols", GroupBy: "path"},
		},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "files_trimmed", Op: opFit, List: "changed_files", Doc: "fewer changed files (changed_files_total keeps the count)"},
			{Name: "unmapped_trimmed", Op: opFit, List: "unmapped_files", Doc: "fewer changed source files without mapped symbols (unmapped_files_total keeps the count)"},
			{Name: "unmapped_total", Op: opTotal, List: "unmapped_files", Doc: "only unmapped_files_total remains"},
			{Name: "modules_trimmed", Op: opFit, List: "affected.affected_modules", Doc: "fewer affected modules"},
			{Name: "tiers_trimmed", Op: opFitEach, List: "affected.by_depth", Each: "items", Doc: "each dependent tier keeps its first items (total keeps the count)"},
			{Name: "files_counts", Op: opCount, List: "changed_files", Doc: "changed files become changed_files_by_change"},
			{Name: "tiers_total", Op: opTotal, List: "affected.by_depth", Doc: "only affected.counts remain"},
			{Name: "symbols_trimmed", Op: opFit, List: "changed_symbols", Doc: "fewer changed symbols, in the order the diff lists them"},
			{Name: "symbols_counts", Op: opCount, List: "changed_symbols", Doc: "changed symbols become changed_symbols_by_path"},
		},
		More: "limit and offset page a dependent tier; a narrower scope or max_chars up to 65536",
	},
	{
		// impact v2 (WS-35) from=&to=: the path with per-hop edges
		Shape: "trace (hops)", Match: "hops",
		Lists:   []List{{Member: "hops"}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "hops_trimmed", Op: opFit, List: "hops", Doc: "fewer per-hop edges, from the start (path keeps every file)"},
			{Name: "hops_total", Op: opTotal, List: "hops", Doc: "only path and length remain"},
		},
		More: "a smaller max_depth, or max_chars up to 65536",
	},
	{
		Shape: "changes (no arguments)", Match: "dirty_symbols",
		Lists:   []List{{Member: "changed_files", GroupBy: "change"}, {Member: "dirty_symbols", GroupBy: "path"}},
		Signals: []string{"truncation", "drift_checked"},
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "files_trimmed", Op: opFit, List: "changed_files", Doc: "fewer changed files (changed_files_total keeps the count)"},
			{Name: "files_counts", Op: opCount, List: "changed_files", Doc: "changed files become changed_files_by_change"},
			{Name: "symbols_trimmed", Op: opFit, List: "dirty_symbols", Doc: "fewer dirty symbols; contract breaks are listed first"},
			{Name: "symbols_counts", Op: opCount, List: "dirty_symbols", Doc: "dirty symbols become dirty_symbols_by_path"},
		},
		More: "ground sections=index lists contract breaks; max_chars up to 65536",
	},
	{
		// impact v2 (WS-35) groups dependents by depth tier; its limit and offset page a tier
		Shape: "by depth tier (by_depth)", Match: "by_depth",
		Lists:   []List{{Member: "memories"}, {Member: "affected_modules"}, {Member: "by_depth"}},
		Signals: append([]string{"epistemic", "counts"}, envelope...),
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "memories_trimmed", Op: opFit, List: "memories", Doc: "fewer attached memories, best first"},
			{Name: "modules_trimmed", Op: opFit, List: "affected_modules", Doc: "fewer affected modules, largest first"},
			{Name: "tiers_trimmed", Op: opFitEach, List: "by_depth", Each: "items", Doc: "each depth tier keeps its first items (total keeps the count)"},
			{Name: "tiers_total", Op: opTotal, List: "by_depth", Doc: "only counts remain"},
		},
		More: "limit and offset page a depth tier; summary_only returns the counts",
	},
	{
		Shape: "blast radius (impacted)", Match: "impacted",
		Lists:   []List{{Member: "memories"}, {Member: "defined_in"}, {Member: "impacted", GroupBy: "distance"}},
		Signals: envelope,
		Keep:    envelopeKeep,
		Rungs: []Rung{
			signalCounts,
			signalFlags,
			{Name: "memories_trimmed", Op: opFit, List: "memories", Doc: "fewer attached memories, best first"},
			{Name: "defined_trimmed", Op: opFit, List: "defined_in", Doc: "fewer defining files"},
			{Name: "trimmed", Op: opFit, List: "impacted", Doc: "fewer impacted files, nearest first"},
			{Name: "counts", Op: opCount, List: "impacted", Doc: "impacted files become impacted_by_distance"},
			{Name: "total", Op: opTotal, List: "impacted", Doc: "only impacted_count remains"},
		},
		More: "a smaller max_depth keeps the nearest files; max_chars up to 65536",
	},
	fallback,
}
