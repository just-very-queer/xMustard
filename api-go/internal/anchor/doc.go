// SPDX-License-Identifier: MIT
// Copyright 2026 xMustard contributors

// Package anchor places quoted code on exact lines, deterministically and without a
// model (WS-65, PAR-REV-04). It is a port of open-code-review's snippet resolver:
// hunk.go, resolve.go and relocate.go are Apache-2.0 translations that keep upstream's
// SPDX and copyright lines and state their changes (see NOTICE at the repository root);
// the other files are xMustard's own, under MIT.
//
// A Snippet is normalized once: lines trimmed, one leading + and - marker stripped,
// blank lines dropped, and at most MaxSnippetLines lines and MaxSnippetBytes bytes.
// A File is one changed file of a unified diff. File.Resolve slides the snippet over
// the hunks' new side, then their old side, then the whole file at head; the first tier
// with a match decides, and several matches anchor nothing (ambiguous). A head the
// caller did not read leaves a snippet no hunk holds head_unread: unknown, not absent.
// Set.PlaceAll adds the cross-file re-filing: a snippet absent from its own file moves
// to another file of the change only when exactly one of them holds it, and no file's
// head is left unread.
//
// Locate (a memory's quoted-code anchor, WS-27) and Reanchor (refs_stale re-anchoring,
// WS-28) work on file content alone. Set.Touches says whether an anchored range holds a
// changed line. Nothing here reads a file or runs git; callers supply the diff text and
// the head content.
package anchor
