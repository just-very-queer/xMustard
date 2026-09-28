package workspaceops

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// Tiered conflict candidates (WS-27, PAR-GOV-11). Deterministic and advisory: remember
// and recall report them, nothing is invalidated. A pair of memories is labeled with the
// first tier that fires:
//
//   - T1 claim: their structured claims on one subject exclude each other.
//   - T2 contradiction: they share an anchor and most of their vocabulary, but state
//     different numbers, versions or commands for the same thing, or only one of them
//     negates or deprecates.
//   - T3 path overlap: they reference the same file (the pre-WS-27 conflict, kept).

// MemoryConflict is one conflict candidate. Path and Titles are set for a path overlap.
type MemoryConflict struct {
	Tier     int      `json:"tier"`
	Kind     string   `json:"kind"`
	EntryIDs []string `json:"entry_ids"`
	Reason   string   `json:"reason"`
	Path     string   `json:"path,omitempty"`
	Titles   []string `json:"titles,omitempty"`
}

// Conflict kinds.
const (
	ConflictClaim         = "claim"
	ConflictContradiction = "contradiction"
	ConflictPathOverlap   = "path_overlap"
)

// conflictSide is one memory as conflict detection reads it.
type conflictSide struct {
	id, title string
	claims    []govstore.Claim
	paths     []string // declared paths
	anchors   map[govstore.AnchorRef]bool
	facts     textFacts
}

func newConflictSide(e ContextEntry, claims []govstore.Claim, anchors []govstore.AnchorRef) *conflictSide {
	s := &conflictSide{id: e.ID, title: fallbackString(e.Title, e.ID), claims: claims, paths: e.Paths,
		anchors: map[govstore.AnchorRef]bool{}, facts: readFacts(e.Title + "\n" + e.Content)}
	for _, p := range e.Paths {
		s.anchors[govstore.AnchorRef{Kind: govstore.AnchorPath, Value: p}] = true
	}
	for _, a := range anchors {
		s.anchors[govstore.AnchorRef{Kind: sameAnchorKinds(a.Kind)[0], Value: a.Value}] = true
	}
	return s
}

// conflictTier detects one tier for a pair; reason says why it fired.
type conflictTier struct {
	tier int
	kind string
	find func(a, b *conflictSide) (reason, path string, ok bool)
}

var conflictTiers = []conflictTier{
	{1, ConflictClaim, claimConflict},
	{2, ConflictContradiction, contradiction},
	{3, ConflictPathOverlap, pathOverlap},
}

// pairConflict is the highest tier a and b conflict at, among tiers.
func pairConflict(a, b *conflictSide, tiers []conflictTier) (MemoryConflict, bool) {
	for _, t := range tiers {
		if reason, path, ok := t.find(a, b); ok {
			c := MemoryConflict{Tier: t.tier, Kind: t.kind, EntryIDs: []string{a.id, b.id}, Reason: reason, Path: path}
			if path != "" {
				c.Titles = []string{a.title, b.title}
			}
			return c, true
		}
	}
	return MemoryConflict{}, false
}

// Claim rules (T1): a functional predicate takes one object per subject, and requires
// and forbids exclude each other on the same object. requires and forbids are
// multi-valued: a subject may require several things.
var (
	functionalPredicates = setOf(govstore.PredicateConfiguredAs, govstore.PredicatePinnedTo, govstore.PredicateDeprecatedBy,
		govstore.PredicateOwnedBy, govstore.PredicateReplaces, govstore.PredicateRunsWith)
	opposedPredicates = map[string]string{govstore.PredicateRequires: govstore.PredicateForbids,
		govstore.PredicateForbids: govstore.PredicateRequires}
)

// claimsContradict reports whether two claims exclude each other, and how.
func claimsContradict(x, y govstore.Claim) (string, bool) {
	sameObject := strings.EqualFold(x.Object, y.Object)
	switch {
	case !strings.EqualFold(x.Subject, y.Subject):
		return "", false
	case x.Predicate == y.Predicate && functionalPredicates[x.Predicate] && !sameObject:
		return fmt.Sprintf("%s %s %q vs %q", x.Subject, x.Predicate, x.Object, y.Object), true
	case opposedPredicates[x.Predicate] == y.Predicate && sameObject:
		return fmt.Sprintf("%s both %s and %s %q", x.Subject, x.Predicate, y.Predicate, x.Object), true
	}
	return "", false
}

func claimConflict(a, b *conflictSide) (string, string, bool) {
	for _, x := range a.claims {
		for _, y := range b.claims {
			if why, ok := claimsContradict(x, y); ok {
				return why, "", true
			}
		}
	}
	return "", "", false
}

// contradictionOverlap is T2's lexical gate: the shared content words over the smaller
// vocabulary, with at least contradictionShared words shared.
const (
	contradictionOverlap = 0.5
	contradictionShared  = 2
)

// contradictionRules are T2's signals, the first that differs is reported.
var contradictionRules = []struct {
	name   string
	differ func(a, b textFacts) (string, bool)
}{
	{"number", differentValues(func(f textFacts) map[string][]string { return f.numbers })},
	{"version", differentValues(func(f textFacts) map[string][]string { return f.versions })},
	{"command", differentCommands},
	{"negation", markerAsymmetry(func(f textFacts) []string { return f.negations })},
	{"deprecation", markerAsymmetry(func(f textFacts) []string { return f.deprecations })},
}

func contradiction(a, b *conflictSide) (string, string, bool) {
	if !sharesAnchor(a, b) || !lexicallyClose(a.facts.words, b.facts.words) {
		return "", "", false
	}
	for _, r := range contradictionRules {
		if why, ok := r.differ(a.facts, b.facts); ok {
			return r.name + ": " + why, "", true
		}
	}
	return "", "", false
}

func pathOverlap(a, b *conflictSide) (string, string, bool) {
	for _, p := range a.paths {
		if slices.Contains(b.paths, p) {
			return "both reference " + p, p, true
		}
	}
	return "", "", false
}

func sharesAnchor(a, b *conflictSide) bool {
	for ref := range a.anchors {
		if b.anchors[ref] {
			return true
		}
	}
	return false
}

func lexicallyClose(a, b map[string]bool) bool {
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	smaller := min(len(a), len(b))
	return shared >= contradictionShared && float64(shared) >= contradictionOverlap*float64(smaller)
}

// differentValues fires when both texts give a key values and no value is common.
func differentValues(pick func(textFacts) map[string][]string) func(a, b textFacts) (string, bool) {
	return func(a, b textFacts) (string, bool) {
		va, vb := pick(a), pick(b)
		for _, k := range slices.Sorted(maps.Keys(va)) {
			if other, ok := vb[k]; ok && !slices.ContainsFunc(va[k], func(v string) bool { return slices.Contains(other, v) }) {
				return fmt.Sprintf("%s %s vs %s", k, strings.Join(va[k], "/"), strings.Join(other, "/")), true
			}
		}
		return "", false
	}
}

// differentCommands fires when both texts give a command of the same program and share
// none.
func differentCommands(a, b textFacts) (string, bool) {
	for _, x := range a.commands {
		if slices.Contains(b.commands, x) {
			return "", false
		}
	}
	for _, x := range a.commands {
		for _, y := range b.commands {
			if leader(x) == leader(y) {
				return fmt.Sprintf("`%s` vs `%s`", x, y), true
			}
		}
	}
	return "", false
}

func leader(cmd string) string { l, _, _ := strings.Cut(cmd, " "); return l }

// markerAsymmetry fires when exactly one text carries markers of the kind.
func markerAsymmetry(pick func(textFacts) []string) func(a, b textFacts) (string, bool) {
	return func(a, b textFacts) (string, bool) {
		ma, mb := pick(a), pick(b)
		if (len(ma) == 0) == (len(mb) == 0) {
			return "", false
		}
		return "only one says " + strings.Join(append(ma, mb...), "/"), true
	}
}

// textFacts is what T2 compares in one text: its content words, the numbers and
// versions stated for a key (the nearest content word before them in the clause), its
// commands, and its negation and deprecation markers.
type textFacts struct {
	words                   map[string]bool
	numbers, versions       map[string][]string
	commands                []string
	negations, deprecations []string
}

var (
	factToken = regexp.MustCompile(`v?\d+(?:\.\d+)+|\d{4}-\d{2}(?:-\d{2})?|\d+|[a-z_][a-z0-9_]*(?:'[a-z]+)?|[;,()!?\n]|\.(?:\s|$)`)
	isoDate   = regexp.MustCompile(`^\d{4}-\d{2}`)
	stopWords = setOf("a", "an", "the", "is", "are", "was", "were", "be", "been", "to", "of", "on", "in", "at", "for",
		"by", "with", "and", "or", "as", "it", "its", "this", "that", "these", "from", "should", "must", "will", "shall",
		"can", "may", "set", "sets", "use", "uses", "using", "via", "than", "then", "because", "so", "into", "over",
		"under", "up", "out", "we", "our", "you", "your", "they", "their", "has", "have", "had", "do", "does", "did", "now",
		"also", "only", "always", "when", "while", "if", "else", "all", "any", "each", "per", "about", "which", "who",
		"what", "where", "there", "here", "same")
	negationWords    = setOf("not", "never", "no", "don't", "doesn't", "didn't", "cannot", "can't", "won't", "shouldn't", "mustn't", "avoid")
	deprecationWords = setOf("deprecated", "deprecate", "obsolete", "removed", "legacy", "superseded")
	// factStems strip one suffix, leaving at least three letters.
	factStems = []string{"ing", "ed", "es", "s"}
)

// factKind classifies a token: a clause boundary, a value (a version has a dot), or a
// word; a date is ignored ("").
func factKind(tok string) string {
	switch {
	case tok == "" || (len(tok) == 1 && strings.ContainsAny(tok, ";,()!?.")):
		return "boundary"
	case isoDate.MatchString(tok):
		return ""
	case strings.Contains(tok, "."):
		return "version"
	case tok[0] >= '0' && tok[0] <= '9':
		return "number"
	}
	return "word"
}

func readFacts(text string) textFacts {
	f := textFacts{words: map[string]bool{}, numbers: map[string][]string{}, versions: map[string][]string{},
		commands: commandsOf(text)}
	values := map[string]map[string][]string{"number": f.numbers, "version": f.versions}
	key := ""
	for _, tok := range factToken.FindAllString(strings.ToLower(text), -1) {
		switch kind := factKind(strings.TrimSpace(tok)); kind {
		case "boundary":
			key = ""
		case "number", "version":
			if key != "" && !slices.Contains(values[kind][key], tok) {
				values[kind][key] = append(values[kind][key], tok)
			}
		case "word":
			f.noteWord(tok, &key)
		}
	}
	return f
}

// commandsOf is the commands a text names, read by the extraction table's command rows.
func commandsOf(text string) []string {
	var out []string
	for _, ex := range anchorExtractors {
		if ex.kind != govstore.AnchorCommand {
			continue
		}
		for _, m := range ex.find.FindAllStringSubmatch(text, -1) {
			if v, ok := ex.keep(nil, m[1]); ok && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// noteWord records a word: a marker, a stop word, or a content word that keys the
// values after it.
func (f *textFacts) noteWord(tok string, key *string) {
	switch {
	case negationWords[tok]:
		f.negations = appendNew(f.negations, tok)
	case deprecationWords[tok]:
		f.deprecations = appendNew(f.deprecations, tok)
	case stopWords[tok]:
	default:
		*key = stem(tok)
		f.words[*key] = true
	}
}

func appendNew(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func stem(w string) string {
	for _, suf := range factStems {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 && !strings.HasSuffix(w, "ss") {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// pairConflicts labels the T1 and T2 conflicts among recalled entries, pairwise;
// claims holds the entries' claims.
func pairConflicts(entries []ContextEntry, claims map[string][]govstore.Claim) []MemoryConflict {
	sides := make([]*conflictSide, len(entries))
	for i, e := range entries {
		sides[i] = newConflictSide(e, claims[e.ID], nil)
	}
	var out []MemoryConflict
	for i := range sides {
		for j := i + 1; j < len(sides); j++ {
			if c, ok := pairConflict(sides[i], sides[j], conflictTiers[:2]); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

// keptConflicts is recall's conflicts block for the entries it returns: the pairs both
// of whose entries are kept, then T3 grouped by shared path as before WS-27.
func keptConflicts(pairs []MemoryConflict, kept []ContextEntry) []MemoryConflict {
	in := map[string]bool{}
	for _, e := range kept {
		in[e.ID] = true
	}
	out := []MemoryConflict{}
	for _, c := range pairs {
		if in[c.EntryIDs[0]] && in[c.EntryIDs[1]] {
			out = append(out, c)
		}
	}
	return append(out, overlappingMemory(kept)...)
}

// overlappingMemory is T3: each file two or more of entries reference.
func overlappingMemory(entries []ContextEntry) []MemoryConflict {
	byPath := map[string][]ContextEntry{}
	for _, e := range entries {
		for _, p := range e.Paths {
			byPath[p] = append(byPath[p], e)
		}
	}
	out := []MemoryConflict{}
	for _, p := range slices.Sorted(maps.Keys(byPath)) {
		es := byPath[p]
		if len(es) < 2 {
			continue
		}
		c := MemoryConflict{Tier: 3, Kind: ConflictPathOverlap, Path: p, Reason: "each references " + p}
		for _, e := range es {
			c.EntryIDs = append(c.EntryIDs, e.ID)
			c.Titles = append(c.Titles, fallbackString(e.Title, e.ID))
		}
		out = append(out, c)
	}
	return out
}
