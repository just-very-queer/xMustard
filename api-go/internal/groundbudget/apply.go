package groundbudget

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// The degradation ladder: the rungs a section is reduced through, in order.
const (
	levelFull    = iota
	levelTrimmed // lists keep their first k items, in result order
	levelCounts  // lists are removed and counted; objects keep their flags
	levelOmitted // the section is removed; its signals move to the report
)

var levelNames = [...]string{"full", "trimmed", "counts", "omitted"}

// Reasons a section was reduced.
const (
	reasonSectionCap   = "section_cap"
	reasonMaxChars     = "max_chars"
	reasonNotRequested = "not_requested"
)

// field is one member of a JSON object. Every byte slice is a window into the
// result Apply was given: parsing copies nothing, so budgeting a large result holds
// the result itself plus 4 bytes per list element (ends), never a second copy.
// Lists and object members are parsed one level deep, which is as deep as the
// ladder reduces.
type field struct {
	key    string
	keyEnc []byte // the key as it appears in the result (valid JSON)
	raw    []byte // the value as it appears in the result
	isArr  bool
	first  int32   // offset in raw of the first element
	ends   []int32 // ends[i]: offset in raw just past element i
	isObj  bool
	sub    []field // the object's members
}

// parseObject splits a JSON object into its members, in order. doc must be valid
// JSON (Apply checks it once, without allocating).
func parseObject(doc []byte, depth int) ([]field, error) {
	i := skipWS(doc, 0)
	if i >= len(doc) || doc[i] != '{' {
		return nil, errors.New("ground result is not a JSON object")
	}
	var out []field
	for i = skipWS(doc, i+1); i < len(doc) && doc[i] != '}'; {
		ke := stringEnd(doc, i)
		key, err := decodeKey(doc[i:ke])
		if err != nil {
			return nil, err
		}
		f := field{key: key, keyEnc: doc[i:ke]}
		vs := skipWS(doc, skipWS(doc, ke)+1) // past the ':'
		ve := valueEnd(doc, vs)
		f.raw = doc[vs:ve]
		switch doc[vs] {
		case '[':
			f.isArr = true
			f.first, f.ends = listSpans(f.raw)
		case '{':
			if depth > 0 {
				if f.sub, err = parseObject(f.raw, depth-1); err != nil {
					return nil, err
				}
				f.isObj = true
			}
		}
		out = append(out, f)
		if i = skipWS(doc, ve); i < len(doc) && doc[i] == ',' {
			i = skipWS(doc, i+1)
		}
	}
	return out, nil
}

// listSpans records where each element of the list a ends. The table is allocated
// once: every element but the first follows a comma.
func listSpans(a []byte) (first int32, ends []int32) {
	start := skipWS(a, 1)
	ends = make([]int32, 0, bytes.Count(a, []byte{','})+1)
	for i := start; i < len(a) && a[i] != ']'; {
		e := valueEnd(a, i)
		ends = append(ends, int32(e))
		if i = skipWS(a, e); i < len(a) && a[i] == ',' {
			i = skipWS(a, i+1)
		}
	}
	return int32(start), ends
}

func decodeKey(enc []byte) (string, error) {
	if bytes.IndexByte(enc, '\\') < 0 {
		return string(enc[1 : len(enc)-1]), nil
	}
	var key string
	err := json.Unmarshal(enc, &key)
	return key, err
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// stringEnd returns the index just past the string starting at b[i]: the first
// quote after it that an even run of backslashes precedes.
func stringEnd(b []byte, i int) int {
	for j := i + 1; ; {
		q := bytes.IndexByte(b[j:], '"')
		if q < 0 {
			return len(b)
		}
		q += j
		esc := 0
		for k := q - 1; k > i && b[k] == '\\'; k-- {
			esc++
		}
		if esc%2 == 0 {
			return q + 1
		}
		j = q + 1
	}
}

// valueEnd returns the index just past the value starting at b[i].
func valueEnd(b []byte, i int) int {
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

// sectionState is where a section stands on the ladder.
type sectionState struct {
	requested bool
	present   bool // the result has a member of this section
	level     int
	k         int // items each list keeps at levelTrimmed
	reason    string
}

// tally records what a level removed, for the report.
type tally struct {
	kept, total map[string]int
}

func (t *tally) cut(path string, kept, total int) {
	if t == nil {
		return
	}
	if t.total == nil {
		t.kept, t.total = map[string]int{}, map[string]int{}
	}
	if kept > 0 {
		t.kept[path] = kept
	}
	t.total[path] = total
}

type budgeter struct {
	req    Request
	fields []field
	secOf  []int    // the section index of each field; len(sections) is otherSection
	spec   []Member // the declaration of each field
	st     []sectionState
	full   []int // each section's chars before any reduction
	// recovery, once set, is named in the report whenever anything was reduced.
	recovery *Recovery
}

func (b *budgeter) nsec() int { return len(sections) + 1 }

func (b *budgeter) section(s int) Section {
	if s < len(sections) {
		return sections[s]
	}
	return Section{Name: otherSection, Cap: otherCap, Recover: otherRecover}
}

// Recovery is how a caller gets the unbudgeted result after anything it asked for
// was reduced. On a delivered call (the MCP server's) the API retains the result as
// evidence: URI reads it in pages or searches it with resources/read, and Handle is
// the same original for clients that expand by handle (Pi's xmustard_expand).
// Unavailable says why no handle was issued, and what to do instead.
type Recovery struct {
	Handle      string
	URI         string
	Unavailable string
}

// Apply fits a ground result (a JSON object) to req and appends the output_budget
// report. Each requested section is first held to its own cap (lifted for the one
// section requested). Then, while the result exceeds req.MaxChars, the least
// important section is reduced one rung (trimmed, counts, omitted) before a more
// important one is touched. The pinned summary section is never omitted. Signals
// of an omitted or unrequested section move to output_budget.signals. Sizes are
// computed without rendering; only the final result is written.
func Apply(result []byte, req Request) ([]byte, error) {
	return ApplyRecoverable(result, req, nil)
}

// ApplyRecoverable is Apply for a caller that can keep the unbudgeted result. When
// the fitted result leaves out anything the caller asked for (a section reduced, or
// members no section declares), retain is called once and the Recovery it returns
// is named in output_budget, the result refitted with it. retain is never called for
// a result returned in full.
func ApplyRecoverable(result []byte, req Request, retain func() Recovery) ([]byte, error) {
	if req.MaxChars == 0 {
		req.MaxChars = DefaultMaxChars
	}
	if len(result) > math.MaxInt32 {
		return nil, errors.New("ground result too large to budget")
	}
	if !json.Valid(result) {
		return nil, errors.New("ground result is not valid JSON")
	}
	fields, err := parseObject(result, 1)
	if err != nil {
		return nil, err
	}
	b := &budgeter{req: req}
	b.st = make([]sectionState, b.nsec())
	for _, f := range fields {
		if f.key == ReportMember {
			continue // a stale report is replaced, never budgeted as content
		}
		// A member no section declares is treated as a signal: it may be a failure
		// a later change added, so it is counted, never dropped without a trace.
		s, m := len(sections), Member{Name: f.key, Signal: true}
		for i, sec := range sections {
			for _, decl := range sec.Members {
				if decl.Name == f.key {
					s, m = i, decl
				}
			}
		}
		b.fields = append(b.fields, f)
		b.secOf = append(b.secOf, s)
		b.spec = append(b.spec, m)
		b.st[s].present = true
	}
	b.full = make([]int, b.nsec())
	for s := range b.st {
		b.full[s] = b.sectionChars(s, levelFull, 0)
	}
	b.fit()
	if retain != nil && b.reduced() {
		rec := retain()
		b.recovery = &rec
		b.fit() // the report grew: fit again with it
	}
	return b.render(), nil
}

// WorkingSet bounds the bytes a budgeted call holds for result until its response
// is written: the result itself, the element tables (4 bytes per list element; every
// element but a list's first follows a comma), the rendered output (max_chars, or the
// pinned summary at counts when that alone exceeds it, which is far smaller than the
// slack), and the ladder's report encodings (a few KB for each of at most a few dozen
// steps). A caller admits it before Apply.
func WorkingSet(result []byte, req Request) int64 {
	elems := bytes.Count(result, []byte{','}) + bytes.Count(result, []byte{'['})
	return int64(len(result)) + 4*int64(elems) + int64(max(req.MaxChars, DefaultMaxChars)) + ladderSlack
}

// ladderSlack bounds what fitting allocates besides the tables and the output: the
// report measured at every step (TestApplyDoesNotCopyTheResult checks it).
const ladderSlack = 256 << 10

// fit runs the ladder from the unreduced state.
func (b *budgeter) fit() {
	req := b.req
	alone := 0 // requested sections other than the pinned ones
	for s := range b.st {
		sec := b.section(s)
		b.st[s] = sectionState{present: b.st[s].present,
			requested: sec.Pinned || (req.Sections == nil) || (s < len(sections) && req.Sections[sec.Name])}
		if !b.st[s].requested {
			b.st[s].level, b.st[s].reason = levelOmitted, reasonNotRequested
		} else if !sec.Pinned && b.st[s].present {
			alone++
		}
	}
	// 1. per-section caps: never below counts. The cap is lifted for the one section
	// requested, so `sections=NAME` returns it in full up to max_chars; for the
	// pinned summary that is `sections=summary` alone.
	for s := range b.st {
		sec := b.section(s)
		lifted := req.Sections != nil && ((!sec.Pinned && alone == 1) || (sec.Pinned && len(req.Sections) == 1 && req.Sections[sec.Name]))
		if !b.st[s].requested || lifted {
			continue
		}
		for b.sectionChars(s, b.st[s].level, b.st[s].k) > sec.Cap && b.step(s, sec.Cap, levelCounts, reasonSectionCap) {
		}
	}
	// 2. max_chars: least important first
	total, _ := b.measure()
	for s := b.nsec() - 1; s >= 0 && total > req.MaxChars; s-- {
		if !b.st[s].requested {
			continue
		}
		floor := levelOmitted
		if b.section(s).Pinned {
			floor = levelCounts
		}
		for total > req.MaxChars {
			target := b.sectionChars(s, b.st[s].level, b.st[s].k) - (total - req.MaxChars)
			if !b.step(s, target, floor, reasonMaxChars) {
				break
			}
			total, _ = b.measure()
		}
	}
}

// reduced reports whether the fitted result leaves out anything the caller asked
// for: a requested section below full, or undeclared members (which no sections=
// can name) left out.
func (b *budgeter) reduced() bool {
	for s, st := range b.st {
		if st.present && st.level != levelFull && (st.requested || s == len(sections)) {
			return true
		}
	}
	return false
}

// step moves section s one rung down the ladder, toward target chars, but no lower
// than floor. A trimmable section keeps the most list items that fit target before
// it drops to counts. It reports false when the section cannot be reduced further.
func (b *budgeter) step(s, target, floor int, reason string) bool {
	st := &b.st[s]
	if st.level >= floor {
		return false
	}
	if st.level <= levelTrimmed {
		hi := b.longestList(s) - 1 // the largest k that still trims something
		if st.level == levelTrimmed {
			hi = min(hi, st.k-1)
		}
		if hi >= 1 {
			lo, best := 1, 1
			for lo <= hi {
				mid := (lo + hi) / 2
				if b.sectionChars(s, levelTrimmed, mid) <= target {
					best, lo = mid, mid+1
				} else {
					hi = mid - 1
				}
			}
			st.level, st.k, st.reason = levelTrimmed, best, reason
			return true
		}
	}
	if st.level < levelCounts {
		st.level = levelCounts
	} else {
		st.level = levelOmitted
	}
	st.k, st.reason = 0, reason
	return true
}

// longestList is the length of the longest list in section s, nested one level.
func (b *budgeter) longestList(s int) int {
	n := 0
	for i := range b.fields {
		if b.secOf[i] != s {
			continue
		}
		f := &b.fields[i]
		n = max(n, len(f.ends))
		for j := range f.sub {
			n = max(n, len(f.sub[j].ends))
		}
	}
	return n
}

// sectionChars is the size of section s's members at level with k items per list.
func (b *budgeter) sectionChars(s, level, k int) int {
	n := 0
	for i := range b.fields {
		if b.secOf[i] != s {
			continue
		}
		if size, ok := emit(nil, &b.fields[i], b.spec[i], level, k, nil); ok {
			n += len(b.fields[i].keyEnc) + 1 + size + 1
		}
	}
	return n
}

// emit sizes a member's value at a ladder level and, when w is set, writes it. t
// records what the level removed. ok is false when the member is not returned.
func emit(w *bytes.Buffer, f *field, m Member, level, k int, t *tally) (size int, ok bool) {
	switch level {
	case levelFull:
		return write(w, f.raw), true
	case levelOmitted:
		return 0, false
	case levelTrimmed:
		switch {
		case f.isArr:
			return emitList(w, f.key, f, k, t), true
		case f.isObj:
			return emitObject(w, f, func(w *bytes.Buffer, sub *field) (int, bool) {
				if sub.isArr {
					return emitList(w, f.key+"."+sub.key, sub, k, t), true
				}
				return write(w, sub.raw), true
			}), true
		}
		return write(w, f.raw), true
	}
	// levelCounts
	switch {
	case f.isArr:
		t.cut(f.key, 0, len(f.ends))
		return 0, false
	case f.isObj:
		if m.Keep == nil && !m.Signal {
			return 0, false
		}
		return emitObject(w, f, func(w *bytes.Buffer, sub *field) (int, bool) {
			if sub.isArr { // counted even when the key is not kept
				t.cut(f.key+"."+sub.key, 0, len(sub.ends))
				return 0, false
			}
			if !keeps(m, sub) {
				return 0, false
			}
			return write(w, sub.raw), true
		}), true
	}
	return write(w, f.raw), true
}

// keeps reports whether an object member keeps the scalar sub at the counts stage:
// a key it names in Keep, every key when it has no Keep list, and every flag
// (boolean) of a signal, so a flag added later is never dropped for lack of a Keep
// entry.
func keeps(m Member, sub *field) bool {
	return m.Keep == nil || contains(m.Keep, sub.key) || (m.Signal && isBool(sub.raw))
}

func isBool(raw []byte) bool { return string(raw) == "true" || string(raw) == "false" }

func write(w *bytes.Buffer, p []byte) int {
	if w != nil {
		w.Write(p)
	}
	return len(p)
}

// emitList sizes (and writes) a list keeping its first k elements. The kept
// elements are one window of the result, separators included.
func emitList(w *bytes.Buffer, path string, f *field, k int, t *tally) int {
	if len(f.ends) <= k {
		return write(w, f.raw)
	}
	t.cut(path, k, len(f.ends))
	var kept []byte
	if k > 0 {
		kept = f.raw[f.first:f.ends[k-1]]
	}
	if w != nil {
		w.WriteByte('[')
		w.Write(kept)
		w.WriteByte(']')
	}
	return 2 + len(kept)
}

// emitObject sizes (and writes) an object; each decides whether and how a member is
// returned.
func emitObject(w *bytes.Buffer, f *field, each func(*bytes.Buffer, *field) (int, bool)) int {
	n := 2
	if w != nil {
		w.WriteByte('{')
	}
	first := true
	for i := range f.sub {
		sub := &f.sub[i]
		mark := 0
		if w != nil {
			mark = w.Len()
			if !first {
				w.WriteByte(',')
			}
			w.Write(sub.keyEnc)
			w.WriteByte(':')
		}
		size, ok := each(w, sub)
		if !ok {
			if w != nil {
				w.Truncate(mark)
			}
			continue
		}
		if !first {
			n++
		}
		first = false
		n += len(sub.keyEnc) + 1 + size
	}
	if w != nil {
		w.WriteByte('}')
	}
	return n
}

// signalsOf adds a signal member's value to into: a list's length; an object's
// nested list lengths and the scalars it keeps at the counts stage (its flags and
// Keep keys); or the scalar itself.
func signalsOf(f *field, m Member, into map[string]any) {
	if !m.Signal {
		return
	}
	switch {
	case f.isArr:
		into[f.key] = len(f.ends)
	case f.isObj:
		for i := range f.sub {
			sub := &f.sub[i]
			switch {
			case sub.isArr:
				into[f.key+"."+sub.key] = len(sub.ends)
			case sub.raw[0] != '{' && keeps(m, sub):
				into[f.key+"."+sub.key] = json.RawMessage(sub.raw)
			}
		}
	default:
		into[f.key] = json.RawMessage(f.raw)
	}
}

// sectionReport describes a section that was not returned in full.
type sectionReport struct {
	State     string         `json:"state"`
	Reason    string         `json:"reason"`
	Cap       int            `json:"cap,omitempty"`
	FullChars int            `json:"full_chars"`
	Kept      map[string]int `json:"kept,omitempty"`
	Total     map[string]int `json:"total,omitempty"`
	Members   []string       `json:"members,omitempty"`
	Recover   string         `json:"recover"`
}

// report is the output_budget member.
type report struct {
	MaxChars      int                      `json:"max_chars"`
	UsedChars     int                      `json:"used_chars"`
	DegradedStage string                   `json:"degraded_stage"`
	OverBudget    bool                     `json:"over_budget,omitempty"`
	Sections      map[string]sectionReport `json:"sections,omitempty"`
	NotRequested  []string                 `json:"not_requested,omitempty"`
	Signals       map[string]any           `json:"signals,omitempty"`
	// RecoverURI / RecoverHandle name the retained unbudgeted result (see Recovery);
	// RecoverUnavailable says why there is none.
	RecoverURI         string `json:"recover_uri,omitempty"`
	RecoverHandle      string `json:"recover_handle,omitempty"`
	RecoverUnavailable string `json:"recover_unavailable,omitempty"`
	Docs               string `json:"docs,omitempty"`
}

var reportKey = []byte(`"` + ReportMember + `":`)

// measure sizes the result at the current ladder state, its report included, and
// returns the size and the settled report encoding.
func (b *budgeter) measure() (int, []byte) {
	tallies := make([]tally, b.nsec())
	body := 1 // '{'
	for i := range b.fields {
		s := b.secOf[i]
		if size, ok := emit(nil, &b.fields[i], b.spec[i], b.st[s].level, b.st[s].k, &tallies[s]); ok {
			body += len(b.fields[i].keyEnc) + 1 + size + 1
		}
	}
	body += len(reportKey)
	rep := b.report(tallies)
	var enc []byte
	for i := 0; i < 4; i++ { // used_chars counts itself; settle its digits
		enc, _ = json.Marshal(rep)
		used := body + len(enc) + 1
		if used == rep.UsedChars {
			break
		}
		rep.UsedChars = used
		rep.OverBudget = used > b.req.MaxChars
	}
	return rep.UsedChars, enc
}

// render writes the result at the current ladder state with its report.
func (b *budgeter) render() []byte {
	total, enc := b.measure()
	var w bytes.Buffer
	w.Grow(total)
	w.WriteByte('{')
	for i := range b.fields {
		s := b.secOf[i]
		mark := w.Len()
		w.Write(b.fields[i].keyEnc)
		w.WriteByte(':')
		if _, ok := emit(&w, &b.fields[i], b.spec[i], b.st[s].level, b.st[s].k, nil); !ok {
			w.Truncate(mark)
			continue
		}
		w.WriteByte(',')
	}
	w.Write(reportKey)
	w.Write(enc)
	w.WriteByte('}')
	return w.Bytes()
}

func (b *budgeter) report(tallies []tally) report {
	rep := report{MaxChars: b.req.MaxChars, DegradedStage: levelNames[levelFull]}
	deepest := levelFull
	signals := map[string]any{}
	for s, st := range b.st {
		if !st.present {
			continue
		}
		sec := b.section(s)
		undeclared := s == len(sections)
		if st.level == levelOmitted {
			for i := range b.fields {
				if b.secOf[i] == s {
					signalsOf(&b.fields[i], b.spec[i], signals)
				}
			}
		}
		// Undeclared members cannot be requested by name, so a narrow request that
		// leaves them out reports them like a reduction: by name, with how to get them.
		if !st.requested && !undeclared {
			rep.NotRequested = append(rep.NotRequested, sec.Name)
			continue
		}
		if st.level == levelFull {
			continue
		}
		if st.requested {
			deepest = max(deepest, st.level)
		}
		sr := sectionReport{State: levelNames[st.level], Reason: st.reason, FullChars: b.full[s], Recover: sec.Recover,
			Kept: tallies[s].kept, Total: tallies[s].total}
		if len(sr.Kept) == 0 {
			sr.Kept = nil
		}
		if st.reason == reasonSectionCap {
			sr.Cap = sec.Cap
		}
		if undeclared {
			for i := range b.fields {
				if b.secOf[i] == s {
					sr.Members = append(sr.Members, b.fields[i].key)
				}
			}
		}
		if rep.Sections == nil {
			rep.Sections = map[string]sectionReport{}
		}
		rep.Sections[sec.Name] = sr
	}
	rep.DegradedStage = levelNames[deepest]
	if len(signals) > 0 {
		rep.Signals = signals
	}
	if rep.Sections != nil {
		rep.Docs = DocsURI
		if r := b.recovery; r != nil {
			rep.RecoverURI, rep.RecoverHandle, rep.RecoverUnavailable = r.URI, r.Handle, r.Unavailable
		}
	}
	return rep
}
