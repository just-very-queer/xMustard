package groundbudget

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// The degradation ladder: the rungs a section is reduced through, in order.
const (
	levelFull    = iota
	levelTrimmed // lists keep their first (newest) k items
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

// field is one member of a JSON object. Lists and object members are parsed one
// level deep, which is as deep as the ladder reduces.
type field struct {
	key    string
	keyEnc []byte // the key as JSON
	raw    json.RawMessage
	isArr  bool
	elems  []json.RawMessage // the list's elements
	prefix []int             // prefix[i]: bytes of elems[:i]
	isObj  bool
	sub    []field // the object's members
}

// parseObject splits a JSON object into its members, in order.
func parseObject(raw []byte, depth int) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("ground result is not a JSON object")
	}
	var out []field
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v", tok)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		enc, _ := json.Marshal(key)
		f := field{key: key, keyEnc: enc, raw: v}
		switch firstByte(v) {
		case '[':
			if err := json.Unmarshal(v, &f.elems); err != nil {
				return nil, err
			}
			f.isArr = true
			f.prefix = make([]int, len(f.elems)+1)
			for i, e := range f.elems {
				f.prefix[i+1] = f.prefix[i] + len(e)
			}
		case '{':
			if depth > 0 {
				if f.sub, err = parseObject(v, depth-1); err != nil {
					return nil, err
				}
				f.isObj = true
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func firstByte(b []byte) byte {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return c
		}
	}
	return 0
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
}

func (b *budgeter) nsec() int { return len(sections) + 1 }

func (b *budgeter) section(s int) Section {
	if s < len(sections) {
		return sections[s]
	}
	return Section{Name: otherSection, Cap: otherCap, Recover: "call ground without sections"}
}

// Apply fits a ground result (a JSON object) to req and appends the output_budget
// report. Each requested section is first held to its own cap (lifted for a section
// requested alone besides summary). Then, while the result exceeds req.MaxChars, the
// least important section is reduced one rung (trimmed, counts, omitted) before a
// more important one is touched. The pinned summary section is never omitted.
// Signals of an omitted or unrequested section move to output_budget.signals. Sizes
// are computed without rendering; only the final result is written.
func Apply(result []byte, req Request) ([]byte, error) {
	if req.MaxChars == 0 {
		req.MaxChars = DefaultMaxChars
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
		s, m := len(sections), Member{Name: f.key}
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
	alone := 0 // requested sections other than the pinned ones
	for s := range b.st {
		sec := b.section(s)
		b.st[s].requested = sec.Pinned || (req.Sections == nil) || (s < len(sections) && req.Sections[sec.Name])
		if !b.st[s].requested {
			b.st[s].level, b.st[s].reason = levelOmitted, reasonNotRequested
		} else if !sec.Pinned && b.st[s].present {
			alone++
		}
	}
	b.full = make([]int, b.nsec())
	for s := range b.st {
		b.full[s] = b.sectionChars(s, levelFull, 0)
	}
	// 1. per-section caps: never below counts
	for s := range b.st {
		sec := b.section(s)
		if !b.st[s].requested || (alone == 1 && req.Sections != nil && !sec.Pinned) {
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
	return b.render(), nil
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
		n = max(n, len(f.elems))
		for j := range f.sub {
			n = max(n, len(f.sub[j].elems))
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
		t.cut(f.key, 0, len(f.elems))
		return 0, false
	case f.isObj:
		if m.Keep == nil && !m.Signal {
			return 0, false
		}
		return emitObject(w, f, func(w *bytes.Buffer, sub *field) (int, bool) {
			if sub.isArr { // counted even when the key is not kept
				t.cut(f.key+"."+sub.key, 0, len(sub.elems))
				return 0, false
			}
			if m.Keep != nil && !contains(m.Keep, sub.key) {
				return 0, false
			}
			return write(w, sub.raw), true
		}), true
	}
	return write(w, f.raw), true
}

func write(w *bytes.Buffer, p []byte) int {
	if w != nil {
		w.Write(p)
	}
	return len(p)
}

// emitList sizes (and writes) a list keeping its first k elements.
func emitList(w *bytes.Buffer, path string, f *field, k int, t *tally) int {
	if len(f.elems) <= k {
		return write(w, f.raw)
	}
	t.cut(path, k, len(f.elems))
	if w != nil {
		w.WriteByte('[')
		for i, e := range f.elems[:k] {
			if i > 0 {
				w.WriteByte(',')
			}
			w.Write(e)
		}
		w.WriteByte(']')
	}
	return 2 + f.prefix[k] + max(k-1, 0)
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

// signalsOf adds a signal member's value to into: a list's length, an object's
// (kept) scalar keys, or the scalar itself.
func signalsOf(f *field, m Member, into map[string]any) {
	if !m.Signal {
		return
	}
	switch {
	case f.isArr:
		into[f.key] = len(f.elems)
	case f.isObj:
		for i := range f.sub {
			sub := &f.sub[i]
			if m.Keep != nil && !contains(m.Keep, sub.key) {
				continue
			}
			switch {
			case sub.isArr:
				into[f.key+"."+sub.key] = len(sub.elems)
			case firstByte(sub.raw) != '{':
				into[f.key+"."+sub.key] = sub.raw
			}
		}
	default:
		into[f.key] = f.raw
	}
}

// sectionReport describes a requested section that was not returned in full.
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
	Docs          string                   `json:"docs,omitempty"`
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
		if st.level == levelOmitted {
			for i := range b.fields {
				if b.secOf[i] == s {
					signalsOf(&b.fields[i], b.spec[i], signals)
				}
			}
		}
		if !st.requested {
			rep.NotRequested = append(rep.NotRequested, sec.Name)
			continue
		}
		if st.level == levelFull {
			continue
		}
		deepest = max(deepest, st.level)
		sr := sectionReport{State: levelNames[st.level], Reason: st.reason, FullChars: b.full[s], Recover: sec.Recover,
			Kept: tallies[s].kept, Total: tallies[s].total}
		if len(sr.Kept) == 0 {
			sr.Kept = nil
		}
		if st.reason == reasonSectionCap {
			sr.Cap = sec.Cap
		}
		if s == len(sections) {
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
	}
	return rep
}
