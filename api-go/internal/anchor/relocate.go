// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/resolver.go
// (RelocateAcrossFiles, the diffByPath lookup of ResolveLineNumbers) and the resolution
// order in internal/llmloop/loop.go:708-768. Changes by xMustard contributors: a Set
// indexes the change's files once, a path at head apart from an old path, so a renamed
// file's old name reused by a new file finds the new file; a file where the snippet is
// ambiguous counts all its matches toward the cross-file total, so a unique hit
// elsewhere is not re-filed past them; a snippet filed against a file outside the change
// is looked for in that file at head (Set.Outside, read once per path) before it is
// re-filed; the LLM re-location step (internal/diff/relocation.go) is not ported.

package anchor

// Set is the files of one change, looked up by their path at head or their old path.
type Set struct {
	files []*File
	// byPath finds a file by its path in one version: New holds each path at head, Old
	// each old path. They differ when a renamed file's old name is a new file's path.
	byPath  map[Side]map[string]*File
	outside map[string]*File // files outside the change, read through Outside

	// Outside, when not nil, returns a file outside the change at head ("" when it is
	// missing or unreadable). A snippet filed against such a file is looked for there
	// before it is re-filed, so the file a finding names is always tried first. It is
	// called at most once per path.
	Outside func(path string) string
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
		f = NewFile(path, path, "", func() string { return read(path) })
		s.outside[path] = f
	}
	return f
}

// Place anchors a snippet filed against path. Its own file is tried first: every tier
// of Resolve for a changed file, the whole file at head (Outside) for one outside the
// change. A snippet found nowhere there is re-filed to another file of the change only
// when exactly one place in all the others holds it: the same boilerplate can sit in
// several files, and picking one would trade one wrong location for another.
func (s *Set) Place(path string, sn Snippet) Anchor {
	if len(sn.lines) == 0 {
		return Unplaced(path, ReasonNoSnippet)
	}
	own, a := s.File(path), Unplaced(path, ReasonNotFound)
	switch {
	case own != nil:
		a = own.Resolve(sn)
	case s.Outside != nil:
		a = s.outsideFile(path).Resolve(sn)
	}
	if a.Present() {
		return a
	}
	return s.relocate(path, own, sn)
}

// relocate looks for the snippet in every file of the change but its own.
func (s *Set) relocate(path string, own *File, sn Snippet) Anchor {
	var hit Anchor
	n := 0
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
	}
	if n == 0 {
		return Unplaced(path, ReasonNotFound)
	}
	hit.Status, hit.RefiledFrom = Relocated, path
	return hit
}
