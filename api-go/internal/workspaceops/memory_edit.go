package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// adminEditor names the store principal of an admin edit made without an identity.
const adminEditor = "admin"

// UpdateContextContent amends an entry's content on behalf of editor, who must be its
// author or an admin (ErrNotEntryAuthor otherwise). Readonly entries that are already
// verified/promoted reject edits with a Conflict wrapping ErrReadonlyVerified — they can
// only be superseded by a new proposal (this is the "readonly" permission guarantee).
//
// The edit becomes a new served revision authored by the editor: the previous version
// stays in the history, votes bind to the revision they were cast on (so verification
// starts over), and the drift baseline is dropped so re-promotion captures a fresh one
// (XM-NEW-004). The edit re-gates an entry the open-mode identity wrote for the kind of
// write it is (regatedRequirement). Content identical to the served revision changes
// nothing.
func UpdateContextContent(dataDir, workspaceID, entryID, content string, editor ContextActor) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content is required: %w", ErrInvalidInput)
	}
	requireMulti, threshold := contextDefaults(dataDir)
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := memoryActor(fallbackString(strings.TrimSpace(editor.ID), adminEditor), root)
	var out ContextEntry
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if !editor.Admin && (editor.ID == "" || editor.ID != ce.Source) {
			return ErrNotEntryAuthor
		}
		if ce.Permission == "readonly" && (ce.Promoted || ce.Status == "verified") {
			return Conflict(fmt.Sprintf("entry %s is readonly and verified; propose a new entry to supersede it", entryID)).WithCause(ErrReadonlyVerified)
		}
		_, err = tx.AppendRevision(ctx, govstore.RevisionInput{
			EntryID: entryID, BaseRevision: e.HeadRevision, Op: "edit", Content: content, Activate: true,
			SearchTokens: memoryTokenList(ce.Title + " " + content),
		}, actor)
		if errors.Is(err, govstore.ErrNoChange) {
			out = ce
			return nil
		}
		if err != nil {
			return err
		}
		if err := regate(ctx, tx, ce, editor.OpenMode, requireMulti, threshold, actor); err != nil {
			return err
		}
		out, _, err = settleEntry(ctx, tx, workspaceID, entryID, threshold, root, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.Content = content
	out.ContentDigest = ""
	return &out, nil
}
