// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/resolver.go
// (RelocateAcrossFiles, the diffByPath lookup of ResolveLineNumbers) and the resolution
// order in internal/llmloop/loop.go:708-768. Changes by xMustard contributors: a Set
// indexes the change's files once; a file where the snippet is ambiguous counts all its
// matches toward the cross-file total, so a unique hit elsewhere is not re-filed past
// them; the LLM re-location step (internal/diff/relocation.go) is not ported.

package anchor

// Set is the files of one change, looked up by their path at head or their old path.
type Set struct {
	files  []*File
	byPath map[string]*File
}

// NewSet indexes files.
func NewSet(files []*File) *Set {
	s := &Set{files: files, byPath: make(map[string]*File, 2*len(files))}
	for _, f := range files {
		for _, p := range [...]string{f.OldPath, f.Path} {
			if p != "" {
				s.byPath[p] = f
			}
		}
	}
	return s
}

// File is the change's file at path, or nil when the change does not touch it.
func (s *Set) File(path string) *File { return s.byPath[path] }

// Place anchors a snippet filed against path. Its own file is tried first (every tier
// of Resolve). A snippet found nowhere there is re-filed to another file of the change
// only when exactly one place in all the others holds it: the same boilerplate can sit
// in several files, and picking one would trade one wrong location for another. The
// path may name a file outside the change; it is then only re-filed.
func (s *Set) Place(path string, sn Snippet) Anchor {
	if len(sn.lines) == 0 {
		return Unplaced(path, ReasonNoSnippet)
	}
	own := s.byPath[path]
	if own != nil {
		if a := own.Resolve(sn); a.Present() {
			return a
		}
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
