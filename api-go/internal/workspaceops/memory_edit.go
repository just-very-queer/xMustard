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
	var red ingestRedaction
	red.scrub(&content)
	requireMulti, threshold := contextDefaults(dataDir)
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := editor.storeActor(root)
	var out ContextEntry
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if err := authorizeEdit(editor, ce); err != nil {
			return err
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
	red.annotate(&out)
	return &out, nil
}

// authorizeEdit lets an entry's author or an admin change it. A readonly entry that is
// verified is never edited: it can only be superseded by a new proposal.
func authorizeEdit(editor ContextActor, ce ContextEntry) error {
	if !editor.Admin && (editor.ID == "" || editor.ID != ce.Source) {
		return ErrNotEntryAuthor
	}
	if ce.Permission == "readonly" && (ce.Promoted || ce.Status == "verified") {
		return Conflict(fmt.Sprintf("entry %s is readonly and verified; propose a new entry to supersede it", ce.ID)).WithCause(ErrReadonlyVerified)
	}
	return nil
}

// EditRequest is a focused edit (PAR-GOV-05). It proposes a pending revision on top of
// BaseRevision, which must be the entry's newest revision (compare-and-set); the served
// revision stays served until verification accepts the edit. Exactly one change is
// read, in this order: OldString (replace its one occurrence with NewString), NewString
// alone (append it), Content (replace everything), Description. Expires sets the expiry
// without a new revision.
type EditRequest struct {
	BaseRevision int64
	Reason       string
	OldString    string
	NewString    string
	Content      string
	Description  *string
	Expires      string
}

// revisionEdit derives the revision op and the full new content from the base revision.
// ok is false when the request changes no content or description.
func (req EditRequest) revisionEdit(base govstore.Revision) (op, content string, ok bool, err error) {
	switch {
	case req.OldString != "":
		if n := strings.Count(base.Content, req.OldString); n != 1 {
			return "", "", false, fmt.Errorf("old_string occurs %d times in revision %d; it must occur exactly once: %w",
				n, base.Revision, ErrInvalidInput)
		}
		return "str_replace", strings.Replace(base.Content, req.OldString, req.NewString, 1), true, nil
	case req.NewString != "":
		return "insert", strings.TrimRight(base.Content, "\n") + "\n" + req.NewString, true, nil
	case strings.TrimSpace(req.Content) != "":
		return "edit", req.Content, true, nil
	case req.Description != nil:
		return "update_description", base.Content, true, nil
	}
	return "", "", false, nil
}

// EditContext applies a focused edit on behalf of editor, who must be the entry's author
// or an admin. A reason and the base revision are required; a stale base is a conflict
// that names the current revision and digest, and an edit that changes nothing is
// rejected. The new revision is pending: peers accept it with verify(revision=N), under
// the rule that promotes entries, and an edit in single-agent or open mode is accepted on
// its author's own word, as a proposal is.
func EditContext(dataDir, workspaceID, entryID string, req EditRequest, editor ContextActor) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrInvalidInput)
	}
	if req.BaseRevision <= 0 {
		return nil, fmt.Errorf("base_revision is required (the revision the edit is based on): %w", ErrInvalidInput)
	}
	expiresAt, expiresOK := parseExpiry(req.Expires)
	requireMulti, threshold := contextDefaults(dataDir)
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	actor := editor.storeActor(root)
	var out ContextEntry
	var red ingestRedaction
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		red = ingestRedaction{}
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if err := authorizeEdit(editor, ce); err != nil {
			return err
		}
		// One pending edit at a time: an edit stacked on an undecided one would build on
		// unverified content and leave the older revision impossible to accept.
		if req.BaseRevision != e.HeadRevision || e.HeadRevision != e.Revision {
			return &govstore.ConflictError{EntryID: entryID, BaseRevision: req.BaseRevision, CurrentRevision: e.Revision,
				CurrentDigest: e.ContentDigest, HeadRevision: e.HeadRevision, Reason: editConflictReason(e)}
		}
		base, err := tx.GetRevision(ctx, entryID, e.HeadRevision)
		if err != nil {
			return err
		}
		op, content, changed, err := req.revisionEdit(base)
		if err != nil {
			return err
		}
		// Redact the text that is stored, not only each input: an edit can splice a
		// secret together from stored text and new_string (PAR-SEC-04).
		red.scrub(&content)
		if !changed && expiresAt == "" {
			return fmt.Errorf("the edit changes nothing (give old_string/new_string, content, description or expires): %w", ErrInvalidInput)
		}
		if expiresAt != "" {
			if err := editor.authorizeExpiry(ce); err != nil {
				return err
			}
			if _, err := tx.SetExpiry(ctx, entryID, expiresAt, actor); err != nil {
				return err
			}
		}
		if !changed {
			out, err = entryAfter(ctx, tx, workspaceID, entryID)
			return err
		}
		rv, err := tx.AppendRevision(ctx, govstore.RevisionInput{
			EntryID: entryID, BaseRevision: req.BaseRevision, Op: op, Content: content, Description: req.Description,
			Reason: req.Reason, SearchTokens: memoryTokenList(base.Title + " " + content),
		}, actor)
		if errors.Is(err, govstore.ErrNoChange) {
			return fmt.Errorf("no-op edit: revision %d already has this content: %w", base.Revision, ErrInvalidInput)
		}
		if err != nil {
			return err
		}
		if note := selfAssertion(editor.OpenMode, ce.RequireVerification, requireMulti); note != "" {
			if _, err := tx.RecordVote(ctx, govstore.VoteInput{EntryID: entryID, Revision: rv.Revision,
				Verdict: govstore.VerdictApprove, Note: note}, actor); err != nil {
				return err
			}
		}
		if err := regate(ctx, tx, ce, editor.OpenMode, requireMulti, threshold, actor); err != nil {
			return err
		}
		out, _, err = settleRevision(ctx, tx, workspaceID, entryID, rv.Revision, threshold, root, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !expiresOK {
		out.Warnings = append(out.Warnings, fmt.Sprintf("expires %q is not a date or RFC 3339 time; no expiry set", req.Expires))
	}
	red.annotate(&out)
	return &out, nil
}

// editConflictReason says why an edit's base is refused: a pending edit must be decided
// first, otherwise the base is stale.
func editConflictReason(e govstore.Entry) string {
	if e.HeadRevision != e.Revision {
		return fmt.Sprintf("revision %d is pending verification; it must be accepted or rejected before another edit", e.HeadRevision)
	}
	return "base revision is not the newest revision"
}

// authorizeExpiry lets an admin or a human approver change an entry's expiry, and its
// author while the entry is unpromoted or rests on a single assertion. On memory that
// peers verified, the expiry is part of what they approved, and setting a past one
// would retire it without the retract quorum (PAR-GOV-04).
func (a ContextActor) authorizeExpiry(ce ContextEntry) error {
	switch {
	case a.hasLifecycleAuthority():
		return nil
	case a.ID == "" || a.ID != ce.Source:
		return ErrNotEntryAuthor
	case ce.Promoted && ce.RequiredVerifications > 1:
		return fmt.Errorf("entry %s is peer-verified, so its expiry is governed: %w", ce.ID, ErrApproverRequired)
	}
	return nil
}

// settleRevision decides a pending revision from the votes cast on it, by the rule that
// promotes entries (reconcileEntry, with the revision's author excluded like the entry's):
// enough approvals accept it, so it becomes the served revision and the entry settles on
// those same votes; enough rejections reject it; otherwise it stays pending.
func settleRevision(ctx context.Context, tx govstore.Tx, workspaceID, entryID string, rev int64, threshold int,
	root string, actor govstore.Actor) (ContextEntry, bool, error) {
	e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
	if err != nil {
		return ContextEntry{}, false, err
	}
	pending, err := tx.GetRevision(ctx, entryID, rev)
	if err != nil {
		return ContextEntry{}, false, err
	}
	votes, err := tx.ListVotes(ctx, entryID, rev)
	if err != nil {
		return ContextEntry{}, false, err
	}
	probe := contextEntryFrom(e, votes, nil)
	reconcileEntry(&probe, pending.Author)
	switch probe.Status {
	case govstore.StatusVerified:
		if _, err := tx.AcceptRevision(ctx, govstore.AcceptInput{EntryID: entryID, Revision: rev,
			SearchTokens: memoryTokenList(pending.Title + " " + pending.Content)}, actor); err != nil {
			return ContextEntry{}, false, err
		}
	case govstore.StatusRejected:
		if _, err := tx.RejectRevision(ctx, entryID, rev, "rejected by verification", actor); err != nil {
			return ContextEntry{}, false, err
		}
	default:
		ce.ContentDigest = ""
		return ce, false, nil
	}
	return settleEntry(ctx, tx, workspaceID, entryID, threshold, root, actor)
}

// RetireContext takes an entry out of recall (PAR-GOV-04). An admin or a human
// approver, or the author of an entry that was never promoted, archives it at once:
// nothing verified is withdrawn and it stays restorable. Retiring promoted memory is
// governed: the call casts the caller's retract verdict, which needs the verifier role
// (ErrVerifierRequired), and the entry is retracted once as many distinct principals
// retract it as its gate requires.
func RetireContext(dataDir, workspaceID, entryID, reason string, actor ContextActor) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrInvalidInput)
	}
	ctx := context.Background()
	root := contextRoot(dataDir, workspaceID)
	by := actor.storeActor(root)
	var out ContextEntry
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		if actor.hasLifecycleAuthority() || (actor.ID != "" && actor.ID == ce.Source && !e.Promoted) {
			if _, err := tx.Transition(ctx, entryID, govstore.TransitionInput{To: govstore.LifecycleArchived, Reason: reason}, by); err != nil {
				return err
			}
			out, err = entryAfter(ctx, tx, workspaceID, entryID)
			return err
		}
		if !actor.Verifier {
			return ErrVerifierRequired
		}
		out, _, err = castVerdict(ctx, tx, dataDir, workspaceID, root, e, ce, actor, by,
			govstore.VoteInput{EntryID: entryID, Verdict: govstore.VerdictRetract, Note: "retire: " + reason})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
