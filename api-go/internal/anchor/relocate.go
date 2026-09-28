// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/resolver.go
// (RelocateAcrossFiles, the diffByPath lookup of ResolveLineNumbers) and the resolution
// order in internal/llmloop/loop.go:708-768. Changes by xMustard contributors:
//   - a Set indexes the change's files once, a path at head apart from an old path, so
//     a renamed file's old name reused by a new file finds the new file;
//   - each other file places the snippet with Resolve, as if it had been filed there, and
//     one where the deciding tier holds it several times counts all of them toward the
//     cross-file total, so a unique hit elsewhere is not re-filed past them;
//   - a file whose head was not read could hold the snippet, so while one is left no hit
//     is unique (head_unread);
//   - a snippet filed against a file outside the change is looked for in that file at
//     head (Set.Outside, read once per path) before it is re-filed;
//   - PlaceAll anchors a batch in two passes, every snippet's own file and then
//     re-filing, so the files the snippets name are read before re-filing reads the rest;
//   - the LLM re-location step (internal/diff/relocation.go) is not ported.

package anchor

// Set is the files of one change, looked up by their path at head or their old path.
type Set struct {
	files []*File
	// byPath finds a file by its path in one version: New holds each path at head, Old
	// each old path. They differ when a renamed file's old name is a new file's path.
	byPath  map[Side]map[string]*File
	outside map[string]*File // files outside the change, read through Outside

	// Outside, when not nil, returns a file outside the change at head, and false when it
	// was not read (a file head does not hold is "" and true: it holds nothing). A snippet
	// filed against such a file is looked for there before it is re-filed, so the file a
	// finding names is always tried first. It is called at most once per path.
	Outside func(path string) (string, bool)
}

// NewSet indexes files.
func NewSet(files []*File) *Set {
	s := &Set{files: files, byPath: map[Side]map[string]*File{New: {}, Old: {}}, outside: map[string]*File{}}
	for _, f := range files {
		if !f.deleted {
			s.byPath[New][f.Path] = f
		}
		if f.OldPath != "" {
			s.byPath[Old][f.OldPath] = f
		}
	}
	return s
}

// File is the change's file at path, or nil when the change does not touch it. A path
// at head wins over another file's old path.
func (s *Set) File(path string) *File {
	if f := s.byPath[New][path]; f != nil {
		return f
	}
	return s.byPath[Old][path]
}

// outsideFile is a file the change does not touch: no hunks, so only the whole file at
// head is searched, and its content is read once.
func (s *Set) outsideFile(path string) *File {
	f := s.outside[path]
	if f == nil {
		read := s.Outside
		f = NewFile(path, path, "", func() (string, bool) { return read(path) })
		s.outside[path] = f
	}
	return f
}

// Query is a snippet filed against a path.
type Query struct {
	Path    string
	Snippet Snippet
}

// Place anchors one snippet filed against path, as PlaceAll does.
func (s *Set) Place(path string, sn Snippet) Anchor { return s.PlaceAll([]Query{{path, sn}})[0] }

// PlaceAll anchors each query. Its own file is tried first: every tier of Resolve for a
// changed file, the whole file at head (Outside) for one outside the change. A snippet
// that file does not hold (not_found) is re-filed to another file of the change only when
// exactly one of them holds it, placed there by Resolve as if it had been filed there:
// the same boilerplate can sit in several files, and picking one would trade one wrong
// location for another.
//
// Every query's own file is tried before any is re-filed, and re-filing first reads each
// changed file's head in diff order. A caller that reads heads under a shared budget
// thus spends it on the files the queries name first, and re-filing sees the same heads
// whatever order the queries come in.
func (s *Set) PlaceAll(qs []Query) []Anchor {
	out := make([]Anchor, len(qs))
	var refile []int
	for i, q := range qs {
		if out[i] = s.own(q.Path, q.Snippet); out[i].Reason == ReasonNotFound {
			refile = append(refile, i)
		}
	}
	if len(refile) > 0 {
		for _, f := range s.files {
			f.headSegments()
		}
	}
	for _, i := range refile {
		out[i] = s.relocate(qs[i].Path, qs[i].Snippet)
	}
	return out
}

// own anchors a snippet in the file it is filed against.
func (s *Set) own(path string, sn Snippet) Anchor {
	switch f := s.File(path); {
	case len(sn.lines) == 0:
		return Unplaced(path, ReasonNoSnippet)
	case f != nil:
		return f.Resolve(sn)
	case s.Outside != nil:
		return s.outsideFile(path).Resolve(sn)
	}
	return Unplaced(path, ReasonNotFound)
}

// relocate looks for the snippet in every file of the change but its own. A file whose
// head was not read could hold it, so while one is left a single hit is not unique.
func (s *Set) relocate(path string, sn Snippet) Anchor {
	own := s.File(path)
	var hit Anchor
	n, unread := 0, false
	for _, f := range s.files {
		if f == own {
			continue
		}
		a := f.Resolve(sn)
		if a.Present() {
			n, hit = n+max(a.Candidates, 1), a
		}
		if n > 1 {
			// Ambiguous already; looking further cannot make it unique.
			return Anchor{Path: path, Status: Unanchored, Reason: ReasonAmbiguousAcrossFiles, Candidates: n}
		}
		unread = unread || a.Unchecked()
	}
	switch {
	case unread:
		return Unplaced(path, ReasonHeadUnread)
	case n == 0:
		return Unplaced(path, ReasonNotFound)
	}
	hit.Status, hit.RefiledFrom = Relocated, path
	return hit
}
