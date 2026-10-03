package toolbudget

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Page says where a result's paged list sits in the whole ranking.
type Page struct {
	// Offset is the ranking position of the paged list's first item.
	Offset int
	// Slice: the paged list holds the whole ranking, and Apply keeps
	// [Offset, Offset+Limit) of it (Limit 0: to the end) before fitting.
	Slice bool
	Limit int
	// More: the producer paged the list and items remain after it.
	More bool
	// Cursor signs a cursor that continues at a ranking position; nil issues none.
	Cursor func(offset int) string
}

// kv is one member of a JSON object, in document order.
type kv struct {
	key string
	raw json.RawMessage
}

// The scanners below read windows of a document Apply validated once (json.Valid):
// members and elements are slices of the result, never copies.

// parseObject splits a JSON object into its members, in order.
func parseObject(raw []byte) ([]kv, bool) {
	i := skipWS(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil, false
	}
	out := []kv{}
	for i = skipWS(raw, i+1); i < len(raw) && raw[i] != '}'; {
		ke := stringEnd(raw, i)
		key, ok := decodeKey(raw[i:ke])
		if !ok {
			return nil, false
		}
		vs := skipWS(raw, skipWS(raw, ke)+1) // past the ':'
		ve := valueEnd(raw, vs)
		out = append(out, kv{key, raw[vs:ve]})
		if i = skipWS(raw, ve); i < len(raw) && raw[i] == ',' {
			i = skipWS(raw, i+1)
		}
	}
	return out, true
}

// elements splits a JSON array into its elements.
func elements(raw []byte) []json.RawMessage {
	i := skipWS(raw, 0)
	if i >= len(raw) || raw[i] != '[' {
		return nil
	}
	var out []json.RawMessage
	for i = skipWS(raw, i+1); i < len(raw) && raw[i] != ']'; {
		e := valueEnd(raw, i)
		out = append(out, raw[i:e])
		if i = skipWS(raw, e); i < len(raw) && raw[i] == ',' {
			i = skipWS(raw, i+1)
		}
	}
	return out
}

func decodeKey(enc []byte) (string, bool) {
	if len(enc) < 2 {
		return "", false
	}
	if bytes.IndexByte(enc, '\\') < 0 {
		return string(enc[1 : len(enc)-1]), true
	}
	var key string
	return key, json.Unmarshal(enc, &key) == nil
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// stringEnd returns the index just past the string starting at b[i].
func stringEnd(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(b)
}

// valueEnd returns the index just past the value starting at b[i].
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				i = stringEnd(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	}
	for i < len(b) {
		switch b[i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return i
		}
		i++
	}
	return i
}

func firstByte(raw []byte) byte {
	if i := skipWS(raw, 0); i < len(raw) {
		return raw[i]
	}
	return 0
}

func isArray(raw []byte) bool  { return firstByte(raw) == '[' }
func isObject(raw []byte) bool { return firstByte(raw) == '{' }

// quote encodes a key as a JSON string.
func quote(s string) []byte {
	plain := true
	for _, c := range []byte(s) {
		if c < 0x20 || c == '"' || c == '\\' || c >= 0x7f || c == '<' || c == '>' || c == '&' {
			plain = false
			break
		}
	}
	if plain {
		return []byte(`"` + s + `"`)
	}
	b, _ := json.Marshal(s)
	return b
}

// List rendering modes: the list's items, its counts, or nothing.
const (
	modeItems = iota
	modeCounts
	modeTotal
)

// listState is where one ladder list stands.
type listState struct {
	spec  List
	leaf  string // the list's key in its object
	items []json.RawMessage
	objs  [][]kv // parsed items, filled on first use
	drop  map[string]bool
	clip  map[string]int
	each  string   // the item member list opFitEach cut, if any
	cache [][]byte // rendered items under drop and clip; nil when stale
	k     int      // items kept in modeItems
	mode  int
	state string // the last rung applied to it
	// counts caches the items' counts by GroupBy (the page never changes once loaded)
	counts map[string]int
}

func (l *listState) reset() {
	l.drop, l.clip, l.cache = map[string]bool{}, map[string]int{}, nil
	l.k, l.mode, l.state, l.each = len(l.items), modeItems, "", ""
}

func (l *listState) obj(i int) []kv {
	if l.objs == nil {
		l.objs = make([][]kv, len(l.items))
	}
	if l.objs[i] == nil {
		o, ok := parseObject(l.items[i])
		if !ok {
			o = []kv{}
		}
		l.objs[i] = o
	}
	return l.objs[i]
}

// item is item i rendered under the list's drops and clips.
func (l *listState) item(i int) []byte {
	if len(l.drop) == 0 && len(l.clip) == 0 {
		return l.items[i]
	}
	if l.cache == nil {
		l.cache = make([][]byte, len(l.items))
	}
	if l.cache[i] == nil {
		l.cache[i] = l.shape(i)
	}
	return l.cache[i]
}

func (l *listState) shape(i int) []byte {
	o := l.obj(i)
	if len(o) == 0 {
		return l.items[i]
	}
	var w bytes.Buffer
	w.WriteByte('{')
	n := 0
	for _, m := range o {
		if l.drop[m.key] {
			continue
		}
		if n > 0 {
			w.WriteByte(',')
		}
		n++
		w.Write(quote(m.key))
		w.WriteByte(':')
		sub := subDrops(l.drop, m.key)
		switch c, clipped := l.clip[m.key]; {
		case clipped && isArray(m.raw):
			writeList(&w, elements(m.raw), c, nil)
		case len(sub) > 0:
			writeWithout(&w, m.raw, sub)
		default:
			w.Write(m.raw)
		}
	}
	w.WriteByte('}')
	return w.Bytes()
}

// subDrops are the drops "key.x" reaching into an item's member key.
func subDrops(drop map[string]bool, key string) map[string]bool {
	var out map[string]bool
	for d := range drop {
		if rest, ok := strings.CutPrefix(d, key+"."); ok {
			if out == nil {
				out = map[string]bool{}
			}
			out[rest] = true
		}
	}
	return out
}

// writeWithout writes an object, or a list of objects, without the members in drop.
func writeWithout(w *bytes.Buffer, raw []byte, drop map[string]bool) {
	one := func(el []byte) {
		o, ok := parseObject(el)
		if !ok {
			w.Write(el)
			return
		}
		writeObject(w, o, func(m kv) bool { return !drop[m.key] }, nil)
	}
	if !isArray(raw) {
		one(raw)
		return
	}
	w.WriteByte('[')
	for i, el := range elements(raw) {
		if i > 0 {
			w.WriteByte(',')
		}
		one(el)
	}
	w.WriteByte(']')
}

// writeObject writes the members keep admits, each through val when set.
func writeObject(w *bytes.Buffer, o []kv, keep func(kv) bool, val func(*bytes.Buffer, kv)) {
	w.WriteByte('{')
	n := 0
	for _, m := range o {
		if !keep(m) {
			continue
		}
		if n > 0 {
			w.WriteByte(',')
		}
		n++
		w.Write(quote(m.key))
		w.WriteByte(':')
		if val != nil {
			val(w, m)
		} else {
			w.Write(m.raw)
		}
	}
	w.WriteByte('}')
}

// writeList writes the first k elements, each through el when set.
func writeList(w *bytes.Buffer, items []json.RawMessage, k int, el func(int) []byte) {
	w.WriteByte('[')
	for i := 0; i < k && i < len(items); i++ {
		if i > 0 {
			w.WriteByte(',')
		}
		if el != nil {
			w.Write(el(i))
		} else {
			w.Write(items[i])
		}
	}
	w.WriteByte(']')
}

// groups counts the list's items by its GroupBy member.
func (l *listState) groups() map[string]int {
	if l.counts != nil {
		return l.counts
	}
	out := map[string]int{}
	for i := range l.items {
		key := "(none)"
		for _, m := range l.obj(i) {
			if m.key != l.spec.GroupBy {
				continue
			}
			var s string
			if json.Unmarshal(m.raw, &s) == nil {
				key = s
			} else {
				key = string(m.raw)
			}
		}
		out[key]++
	}
	l.counts = out
	return out
}

// countsKey is the member a list becomes at the counts rung.
func (l *listState) countsKey() string { return l.leaf + "_by_" + l.spec.GroupBy }

// cut reports whether the list returns fewer items than its page holds.
func (l *listState) cut() bool { return l.mode != modeItems || l.k < len(l.items) }

func (l *listState) kept() int {
	if l.mode != modeItems {
		return 0
	}
	return l.k
}

// member is one top-level member of the result, with what the ladder reaches in it.
type member struct {
	kv
	list   *listState            // the member is a ladder list
	sub    []kv                  // parsed members, when the ladder reaches into this object
	nested map[string]*listState // ladder lists inside this object, by key
	signal bool
	lens   map[string]int // a signal object's list lengths, by member
	keys   map[string]int // a signal object's nested object sizes (keys), by member
}

// dropsSignal reports whether a signal object's member key goes at signal level sig:
// its lists at sigCounts, its nested objects (but the ladder's Keep ones) at sigFlags.
func (m *member) dropsSignal(key string, sig int) bool {
	_, list := m.lens[key]
	_, obj := m.keys[key]
	return sig >= sigCounts && list || sig >= sigFlags && obj
}

type budgeter struct {
	req     Request
	page    Page
	ladder  *Ladder
	members []member
	lists   map[string]*listState // by List.Member
	paged   *listState
	// origCursor is the result's own next_cursor (a producer-paged list).
	origCursor string
	more       bool

	// sig is how far signal objects are reduced: sigFull, sigCounts or sigFlags.
	sig      int
	omitted  map[string]bool
	applied  []string
	recovery *Recovery
	buf      bytes.Buffer

	// ranked is the result's own ranked total; a trimmed paged list rewrites the
	// omitted member against it (WS-FIX-18).
	ranked int
}

// Apply fits a tool result (a JSON object) to req and appends the output_budget
// report. The ladder is the first of the tool's ladders whose Match member the result
// has. With page.Slice, the paged list is first cut to its page. Rungs run in order
// until the result fits; a paged list that returns fewer items than its page holds
// sets has_more and next_cursor at the first item not returned. When anything was
// reduced, retain (when set) is called once and its Recovery named in the report.
// A result that is not a JSON object is returned unchanged.
func (s *Spec) Apply(result []byte, req Request, page Page, retain func() Recovery) ([]byte, error) {
	if !json.Valid(result) {
		return nil, errors.New(s.Tool + " result is not valid JSON")
	}
	top, ok := parseObject(result)
	if !ok {
		return result, nil
	}
	b := &budgeter{req: req, page: page, lists: map[string]*listState{}}
	b.ladder = s.ladderFor(top)
	b.load(top)
	b.reset()
	if req.MaxChars <= 0 {
		return b.render(nil), nil
	}
	b.fit()
	if retain != nil && len(b.applied) > 0 {
		rec := retain()
		b.recovery = &rec
		b.reset()
		b.fit()
	}
	return b.render(b.report), nil
}

// Settle re-fits a result that a producer changed after Apply. Apply settles
// output_budget.used_chars against the members it wrote, so a member added after it
// returned leaves the report naming a length the result no longer has. A producer that
// adds its own members (impact's has_more and next_cursor, WS-59e) settles the result
// through here so the report describes what is actually sent.
//
// Settling fits, so a result that no longer fits is trimmed again rather than reported
// over budget; the ladder that trims it is named in stages as usual. It never adds
// items, and an unbudgeted result (MaxChars 0) carries no report to settle and is
// returned unchanged.
func (s *Spec) Settle(result []byte, req Request, page Page) ([]byte, error) {
	if req.MaxChars <= 0 {
		return result, nil
	}
	top, ok := parseObject(result)
	if !ok {
		return result, nil
	}
	b := &budgeter{req: req, page: page, lists: map[string]*listState{}}
	b.ladder = s.ladderFor(top)
	b.load(top)
	b.reset()
	b.fit()
	return b.render(b.report), nil
}

func (s *Spec) ladderFor(top []kv) *Ladder {
	for _, l := range s.Ladders {
		if l.Match == "" {
			return l
		}
		for _, m := range top {
			if m.key == l.Match {
				return l
			}
		}
	}
	return fallback
}

// load indexes the result's members and the ladder's lists and signals in them. A
// stale report is dropped; a paged list's has_more and next_cursor are rewritten.
func (b *budgeter) load(top []kv) {
	signal := map[string]bool{}
	for _, s := range b.ladder.Signals {
		signal[s] = true
	}
	for _, m := range top {
		if m.key == ReportMember {
			continue
		}
		b.members = append(b.members, member{kv: m, signal: signal[m.key] && isObject(m.raw)})
	}
	for _, spec := range b.ladder.Lists {
		parent, leaf, nested := strings.Cut(spec.Member, ".")
		if !nested {
			parent, leaf = "", spec.Member
		}
		for i := range b.members {
			m := &b.members[i]
			var raw json.RawMessage
			switch {
			case !nested && m.key == leaf && isArray(m.raw):
				raw = m.raw
			case nested && m.key == parent && isObject(m.raw):
				b.parse(m)
				for _, s := range m.sub {
					if s.key == leaf && isArray(s.raw) {
						raw = s.raw
					}
				}
			}
			if raw == nil {
				continue
			}
			l := &listState{spec: spec, leaf: leaf, items: elements(raw)}
			b.lists[spec.Member] = l
			if nested {
				if m.nested == nil {
					m.nested = map[string]*listState{}
				}
				m.nested[leaf] = l
			} else {
				m.list = l
			}
			if spec.Paged {
				b.paged = l
			}
		}
	}
	reached := map[string]bool{}
	for _, r := range b.ladder.Rungs {
		for _, name := range r.Omit {
			if top, _, nested := strings.Cut(name, "."); nested {
				reached[top] = true
			}
		}
	}
	for i := range b.members {
		m := &b.members[i]
		if m.signal || reached[m.key] && isObject(m.raw) {
			b.parse(m)
		}
		if !m.signal {
			continue
		}
		m.lens, m.keys = map[string]int{}, map[string]int{}
		for _, s := range m.sub {
			switch {
			case isArray(s.raw):
				m.lens[s.key] = len(elements(s.raw))
			case isObject(s.raw) && !b.ladder.keeps(m.key+"."+s.key):
				o, _ := parseObject(s.raw)
				m.keys[s.key] = len(o)
			}
		}
	}
	if b.paged == nil {
		return
	}
	// the paged list's cursor members are rewritten from the page
	kept := b.members[:0]
	for _, m := range b.members {
		switch m.key {
		case "next_cursor":
			_ = json.Unmarshal(m.raw, &b.origCursor)
		case "has_more":
		case "offset":
			if !b.page.Slice {
				kept = append(kept, m)
			}
		case "total":
			if err := json.Unmarshal(m.raw, &b.ranked); err != nil {
				b.ranked = 0
			}
			kept = append(kept, m)
		default:
			kept = append(kept, m)
		}
	}
	b.members = kept
	b.more = b.page.More || b.origCursor != ""
	if b.page.Slice {
		l := b.paged
		start := min(max(b.page.Offset, 0), len(l.items))
		end := len(l.items)
		if b.page.Limit > 0 {
			end = min(end, start+b.page.Limit)
		}
		b.more = end < len(l.items)
		l.items = l.items[start:end]
	}
}

func (b *budgeter) parse(m *member) {
	if m.sub == nil {
		if o, ok := parseObject(m.raw); ok {
			m.sub = o
		}
	}
}

func (b *budgeter) reset() {
	for _, l := range b.lists {
		l.reset()
	}
	b.sig, b.omitted, b.applied = sigFull, map[string]bool{}, nil
}

// fit runs the ladder's rungs in order until the result fits max_chars. A rung is
// named in the report (stages, its list's state) while it is tried, so a rung that
// searches for the most items that fit measures the report it will carry.
func (b *budgeter) fit() {
	for _, r := range b.ladder.Rungs {
		if b.size() <= b.req.MaxChars {
			return
		}
		b.applied = append(b.applied, r.Name)
		if !b.step(r) {
			b.applied = b.applied[:len(b.applied)-1]
		}
	}
}

// step applies one rung; it reports whether the rung changed the result.
func (b *budgeter) step(r Rung) bool {
	switch r.Op {
	case opSignals:
		return b.signals(sigCounts)
	case opFlags:
		return b.signals(sigFlags)
	case opOmit:
		return b.changes(func() { b.setOmit(r.Omit, true) }, func() { b.setOmit(r.Omit, false) })
	}
	l := b.lists[r.List]
	if l == nil || l.mode == modeTotal {
		return false
	}
	prev := l.state
	l.state = r.Name
	if !b.stepList(l, r) {
		l.state = prev
		return false
	}
	return true
}

func (b *budgeter) stepList(l *listState, r Rung) bool {
	switch r.Op {
	case opShape:
		drop, clip := l.drop, l.clip
		return l.mode == modeItems && b.changes(func() {
			l.drop, l.clip, l.cache = withKeys(drop, r.Drop), withClips(clip, r.Clip), nil
		}, func() { l.drop, l.clip, l.cache = drop, clip, nil })
	case opFit:
		return l.mode == modeItems && b.fitItems(l)
	case opFitEach:
		l.each = r.Each
		return l.mode == modeItems && b.fitEach(l, r.Each)
	case opCount:
		// a nested list cannot be renamed inside its object: it goes to the total
		if l.spec.GroupBy == "" || strings.Contains(l.spec.Member, ".") {
			return b.toMode(l, modeTotal)
		}
		return b.toMode(l, modeCounts)
	case opTotal:
		return b.toMode(l, modeTotal)
	}
	return false
}

func (b *budgeter) toMode(l *listState, mode int) bool {
	if l.mode >= mode {
		return false
	}
	l.mode = mode
	return true
}

// Signal reduction levels (the signal_counts and signal_flags rungs).
const (
	sigFull   = iota
	sigCounts // lists inside signal objects become their lengths
	sigFlags  // signal objects keep their scalars and Keep members
)

// signals reduces signal objects to level when that shrinks the result.
func (b *budgeter) signals(level int) bool {
	if b.sig >= level {
		return false
	}
	prev := b.sig
	return b.changes(func() { b.sig = level }, func() { b.sig = prev })
}

// changes applies do and keeps it when the result without its report got smaller;
// otherwise undo.
func (b *budgeter) changes(do, undo func()) bool {
	before := len(b.render(nil))
	do()
	if len(b.render(nil)) < before {
		return true
	}
	undo()
	return false
}

func (b *budgeter) setOmit(names []string, on bool) {
	for _, n := range names {
		b.omitted[n] = on
	}
}

func withKeys(m map[string]bool, keys []string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func withClips(m map[string]int, add map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// fitItems keeps the most leading items that fit, at least one. It reports whether
// the list got shorter.
func (b *budgeter) fitItems(l *listState) bool {
	n := len(l.items)
	if n <= 1 {
		return false
	}
	lo, hi, best := 1, n-1, 1
	for lo <= hi {
		mid := (lo + hi) / 2
		l.k = mid
		if b.size() <= b.req.MaxChars {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	l.k = best
	return true
}

// fitEach keeps the same leading number of elements (at least one) of every item's
// member list each, the most that fit.
func (b *budgeter) fitEach(l *listState, each string) bool {
	longest := 0
	for i := range l.items {
		for _, m := range l.obj(i) {
			if m.key == each && isArray(m.raw) {
				longest = max(longest, len(elements(m.raw)))
			}
		}
	}
	if longest <= 1 {
		return false
	}
	clip := l.clip
	set := func(k int) { l.clip, l.cache = withClips(clip, map[string]int{each: k}), nil }
	lo, hi, best := 1, longest-1, 1
	for lo <= hi {
		mid := (lo + hi) / 2
		set(mid)
		if b.size() <= b.req.MaxChars {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	set(best)
	return true
}

// size is the rendered result's length, report included.
func (b *budgeter) size() int {
	return len(b.render(b.report))
}

// render writes the result at the current state; report, when set, renders the
// output_budget member for a given total size. The returned slice is b.buf's.
func (b *budgeter) render(report func(int) []byte) []byte {
	b.buf.Reset()
	w := &b.buf
	w.WriteByte('{')
	n := 0
	sep := func() {
		if n > 0 {
			w.WriteByte(',')
		}
		n++
	}
	for i := range b.members {
		m := &b.members[i]
		if b.omitted[m.key] {
			continue
		}
		if m.list != nil {
			if key, ok := b.listKey(m.list); ok {
				sep()
				w.Write(quote(key))
				w.WriteByte(':')
				b.writeListValue(w, m.list)
			}
			continue
		}
		sep()
		w.Write(quote(m.key))
		w.WriteByte(':')
		if m.key == "omitted" && b.recountsOmitted() {
			b.writeOmitted(w)
			continue
		}
		if m.sub == nil {
			w.Write(m.raw)
			continue
		}
		b.writeMember(w, m)
	}
	if b.paged != nil {
		more := b.more || b.paged.cut()
		sep()
		w.WriteString(`"has_more":` + strconv.FormatBool(more))
		if c := b.nextCursor(more); c != "" {
			sep()
			w.WriteString(`"next_cursor":`)
			w.Write(quote(c))
		}
		if b.page.Slice {
			sep()
			w.WriteString(`"offset":` + strconv.Itoa(b.page.Offset))
		}
	}
	if report == nil {
		w.WriteByte('}')
		return w.Bytes()
	}
	// used_chars counts itself: settle its digits
	fixed := w.Len() + len(`"`+ReportMember+`":`) + 1
	if n > 0 {
		fixed++
	}
	used := 0
	var enc []byte
	for range 5 {
		enc = report(used)
		if fixed+len(enc) == used {
			break
		}
		used = fixed + len(enc)
	}
	sep()
	w.WriteString(`"` + ReportMember + `":`)
	w.Write(enc)
	w.WriteByte('}')
	return w.Bytes()
}

// writeMember writes an object member the ladder reaches into: nested lists at their
// state, signal lists as lengths once compacted, omitted members left out.
func (b *budgeter) writeMember(w *bytes.Buffer, m *member) {
	keep := func(s kv) bool {
		if b.omitted[m.key+"."+s.key] {
			return false
		}
		if m.dropsSignal(s.key, b.sig) {
			return false
		}
		if l := m.nested[s.key]; l != nil {
			_, ok := b.listKey(l)
			return ok
		}
		return true
	}
	writeObject(w, m.sub, keep, func(w *bytes.Buffer, s kv) {
		// a nested list keeps its key (a counts rename is not possible inside the
		// parent's object, so nested lists only trim or go)
		if l := m.nested[s.key]; l != nil {
			b.writeListValue(w, l)
			return
		}
		w.Write(s.raw)
	})
}

// listKey is the member name a list renders under, false when it renders nothing.
func (b *budgeter) listKey(l *listState) (string, bool) {
	switch l.mode {
	case modeCounts:
		return l.countsKey(), true
	case modeTotal:
		return "", false
	}
	return l.leaf, true
}

func (b *budgeter) writeListValue(w *bytes.Buffer, l *listState) {
	if l.mode == modeCounts {
		enc, _ := json.Marshal(l.groups())
		w.Write(enc)
		return
	}
	writeList(w, l.items, l.k, l.item)
}

// recountsOmitted reports whether the budget is the one that made this result's
// omitted count untrue, so writeOmitted may replace the producer's value. It
// did when the budget cut the paged hybrid hits list: the producer counted the
// ranked hits after its own page, and the delivered answer carries fewer. Two
// guards keep the arithmetic to the one list it describes. `hits` is the only
// list in the three ladder files whose top-level total and offset describe the
// ranking it is a page of, so any other result's omitted keeps its own value.
// And a list the budget did not cut still carries the producer's count: a
// producer need not describe its page as total - offset - delivered, since
// WorkspaceSearchReranked sets total to the hits it returned while keeping the
// core's omitted for the hits it never fetched (WS-FIX-18).
func (b *budgeter) recountsOmitted() bool {
	return b.paged != nil && b.paged.spec.Member == "hits" && b.paged.cut()
}

// writeOmitted writes a hybrid result's omitted member after the budget cut its
// hits: the ranked hits that come after this page, total - the offset the page
// starts at - the hits the paged list kept (WS-FIX-18).
func (b *budgeter) writeOmitted(w *bytes.Buffer) {
	w.WriteString(strconv.Itoa(max(b.ranked-b.page.Offset-b.paged.kept(), 0)))
}

// nextCursor continues the paged list at its first item not returned. When the budget
// cut nothing, the producer's own cursor stands, or a sliced page continues after its
// last item. A cut list with no cursor signer issues none: has_more and the report
// say so, and recover_uri holds the rest.
func (b *budgeter) nextCursor(more bool) string {
	switch {
	case !more:
		return ""
	case !b.paged.cut() && b.origCursor != "":
		return b.origCursor
	case b.page.Cursor == nil:
		return ""
	case !b.paged.cut():
		return b.page.Cursor(b.page.Offset + len(b.paged.items))
	case b.page.Offset+b.paged.kept() <= 0:
		// the list was reduced below its items (e.g. to counts/total) at the very
		// first page: no item was ever returned, so there is no valid next offset
		// to sign. has_more and recover_uri still report the cut.
		return ""
	}
	return b.page.Cursor(b.page.Offset + b.paged.kept())
}

// maxReportGroups bounds a cut list's group counts in the report; the rest are summed
// under otherGroups.
const (
	maxReportGroups = 20
	otherGroups     = "(other)"
)

// largest keeps the n largest groups (ties by key) and sums the rest.
func largest(groups map[string]int, n int) map[string]int {
	if len(groups) <= n {
		return groups
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if groups[keys[i]] != groups[keys[j]] {
			return groups[keys[i]] > groups[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := map[string]int{}
	for i, k := range keys {
		if i < n {
			out[k] = groups[k]
		} else {
			out[otherGroups] += groups[k]
		}
	}
	return out
}

// listReport describes a list the ladder reduced.
type listReport struct {
	State string `json:"state"`
	Kept  int    `json:"kept"`
	Total int    `json:"total"`
	// EachKept is how many elements of each item's own list stayed (tiers_trimmed).
	EachKept int `json:"each_kept,omitempty"`
	// By counts the page's items by the list's GroupBy member when items were cut.
	By map[string]int `json:"by,omitempty"`
}

// report is the output_budget member.
type report struct {
	MaxChars      int                   `json:"max_chars"`
	UsedChars     int                   `json:"used_chars"`
	DegradedStage string                `json:"degraded_stage"`
	Stages        []string              `json:"stages,omitempty"`
	OverBudget    bool                  `json:"over_budget,omitempty"`
	Lists         map[string]listReport `json:"lists,omitempty"`
	Omitted       []string              `json:"omitted,omitempty"`
	Signals       map[string]int        `json:"signals,omitempty"`
	More          string                `json:"more,omitempty"`
	// RecoverURI / RecoverHandle name the retained unbudgeted result;
	// RecoverUnavailable says why there is none.
	RecoverURI         string `json:"recover_uri,omitempty"`
	RecoverHandle      string `json:"recover_handle,omitempty"`
	RecoverUnavailable string `json:"recover_unavailable,omitempty"`
	Docs               string `json:"docs,omitempty"`
}

// report encodes output_budget for a result of used bytes.
func (b *budgeter) report(used int) []byte {
	rep := report{MaxChars: b.req.MaxChars, UsedChars: used, DegradedStage: "full", Stages: b.applied,
		OverBudget: used > b.req.MaxChars}
	if len(b.applied) > 0 {
		rep.DegradedStage = b.applied[len(b.applied)-1]
		rep.More, rep.Docs = b.ladder.More, DocsURI
		if r := b.recovery; r != nil {
			rep.RecoverURI, rep.RecoverHandle, rep.RecoverUnavailable = r.URI, r.Handle, r.Unavailable
		}
	}
	for name, l := range b.lists {
		if l.state == "" {
			continue
		}
		lr := listReport{State: l.state, Kept: l.kept(), Total: len(l.items), EachKept: l.clip[l.each]}
		if l.spec.GroupBy != "" && l.mode == modeItems && l.cut() {
			lr.By = largest(l.groups(), maxReportGroups)
		}
		if rep.Lists == nil {
			rep.Lists = map[string]listReport{}
		}
		rep.Lists[name] = lr
	}
	for name, on := range b.omitted {
		if on && b.present(name) {
			rep.Omitted = append(rep.Omitted, name)
		}
	}
	sort.Strings(rep.Omitted)
	for _, m := range b.members {
		for _, sizes := range []struct {
			on bool
			of map[string]int
		}{{b.sig >= sigCounts, m.lens}, {b.sig >= sigFlags, m.keys}} {
			for key, n := range sizes.of {
				if !sizes.on {
					continue
				}
				if rep.Signals == nil {
					rep.Signals = map[string]int{}
				}
				rep.Signals[m.key+"."+key] = n
			}
		}
	}
	enc, _ := json.Marshal(rep)
	return enc
}

// present reports whether the result has member name ("a" or "a.b").
func (b *budgeter) present(name string) bool {
	top, sub, nested := strings.Cut(name, ".")
	for _, m := range b.members {
		if m.key != top {
			continue
		}
		if !nested {
			return true
		}
		o, ok := parseObject(m.raw)
		if !ok {
			return false
		}
		for _, s := range o {
			if s.key == sub {
				return true
			}
		}
	}
	return false
}
