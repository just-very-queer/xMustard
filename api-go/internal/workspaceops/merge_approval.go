package workspaceops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// Human merge-approval attestations (WS-57, folding in WS-71; PAR-REV-14). A human
// approver attests that they reviewed one change: the repository, the merge base of a
// base ref and a head, the head commit and the SHA-256 of the diff between them, and
// the review records they read. The attestation is stale as soon as the head, the merge
// base or the diff differs (a gate asked about another base sees another merge base), a
// human approver can revoke it, and it stops counting when its approver's token is
// revoked. It is an attestation
// only: xMustard never runs git merge, never changes branch protection and never posts
// to a pull request, and it is created only here, from `xmustard-ops review approve`,
// never over MCP or HTTP. `review gate` reports the state through its exit code for the
// human's own pre-push hook; there is no signed export, so it is not a CI control. The
// record is as strong as its token (see HumanApprover): advisory unless a presence-only
// token was typed at the terminal.

// MergeApprovalEnforcement is the label every attestation and every status carries.
const MergeApprovalEnforcement = "attestation only: xMustard never merges, never changes branch protection " +
	"and never posts to a pull request; enforce merges with branch protection"

// Merge approval states.
const (
	MergeApprovalNone    = "none"
	MergeApprovalCurrent = "current"
	MergeApprovalStale   = "stale"
)

// maxReviewRecords bounds the review record ids one attestation cites.
const maxReviewRecords = 32

// reviewDiffTimeout bounds each git run of a review diff.
const reviewDiffTimeout = 2 * time.Minute

// ReviewedChange is the change an attestation binds to. Repository is the workspace's
// canonical root; MergeBase is the merge base of BaseRef and Head; DiffSHA256 digests
// the hardened diff from MergeBase to Head (reviewDiffArgs), DiffBytes is its size.
type ReviewedChange struct {
	WorkspaceID string `json:"workspace_id"`
	Repository  string `json:"repository"`
	BaseRef     string `json:"base_ref"`
	MergeBase   string `json:"merge_base"`
	Head        string `json:"head"`
	DiffSHA256  string `json:"diff_sha256"`
	DiffBytes   int64  `json:"diff_bytes"`
}

// same reports whether two observations describe the same reviewed change.
func (c ReviewedChange) same(o ReviewedChange) bool {
	return c.WorkspaceID == o.WorkspaceID && c.Repository == o.Repository && c.MergeBase == o.MergeBase &&
		c.Head == o.Head && c.DiffSHA256 == o.DiffSHA256
}

// MergeApproval is one attestation as recorded, with its revocation if any.
type MergeApproval struct {
	Seq int64 `json:"seq"`
	ReviewedChange
	ReviewRecords []string         `json:"review_records"`
	Approver      string           `json:"approver"`
	ApproverOwner string           `json:"approver_owner"`
	ApproverKind  string           `json:"approver_kind"`
	Assurance     string           `json:"assurance"`
	Note          string           `json:"note,omitempty"`
	At            string           `json:"at"`
	Enforcement   string           `json:"enforcement"`
	Revoked       *MergeRevocation `json:"revoked,omitempty"`
}

// MergeRevocation withdraws an attestation.
type MergeRevocation struct {
	Seq    int64  `json:"seq"`
	By     string `json:"by"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// MergeApprovalStatus is the human approval state of merging a head into a base:
// current when a trusted, unrevoked attestation binds exactly the change from their
// merge base to the head, stale when trusted attestations exist but none does (Approval
// is then the newest one), none when there is none. Revoked counts revoked attestations
// and Untrusted those whose approver is not, or no longer, a human approver of the
// workspace; neither is ever current.
type MergeApprovalStatus struct {
	WorkspaceID string         `json:"workspace_id"`
	Status      string         `json:"status"`
	BaseRef     string         `json:"base_ref"`
	Head        string         `json:"head"`
	Approval    *MergeApproval `json:"approval,omitempty"`
	// Current is the change as it is now, from the merge base of BaseRef and Head; it is
	// observed only when an attestation names this head.
	Current     *ReviewedChange `json:"current,omitempty"`
	Revoked     int             `json:"revoked"`
	Untrusted   int             `json:"untrusted"`
	Enforcement string          `json:"enforcement"`
}

// mergeApprovalData is the event record of an attestation.
type mergeApprovalData struct {
	ReviewedChange
	ReviewRecords []string `json:"review_records"`
	Assurance     string   `json:"assurance"`
	Enforcement   string   `json:"enforcement"`
}

// mergeRevocationData is the event record of a revocation.
type mergeRevocationData struct {
	ApprovalSeq int64 `json:"approval_seq"`
}

// DiffReviewedChange observes the change between the merge base of baseRef and headRef
// in the workspace's repository.
func DiffReviewedChange(ctx context.Context, dataDir, workspaceID, baseRef, headRef string) (ReviewedChange, error) {
	return observeChange(ctx, dataDir, workspaceID, baseRef, headRef, nil)
}

// observeChange is DiffReviewedChange that also writes the diff text to text when it is
// not nil, so what a caller reads is exactly what was digested.
func observeChange(ctx context.Context, dataDir, workspaceID, baseRef, headRef string, text io.Writer) (ReviewedChange, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return ReviewedChange{}, err
	}
	root := WorkspaceRepoScope(dataDir, workspaceID)
	if root == "" {
		return ReviewedChange{}, fmt.Errorf("workspace %s has no repository root: %w", workspaceID, os.ErrNotExist)
	}
	head, err := resolveCommit(ctx, root, headRef)
	if err != nil {
		return ReviewedChange{}, err
	}
	base, err := resolveCommit(ctx, root, baseRef)
	if err != nil {
		return ReviewedChange{}, err
	}
	return diffAt(ctx, workspaceID, root, baseRef, base, head, text)
}

// diffAt observes the change from the merge base of base (resolved from baseRef) and
// head, both resolved commits. The diff text also goes to text when it is not nil.
func diffAt(ctx context.Context, workspaceID, root, baseRef, base, head string, text io.Writer) (ReviewedChange, error) {
	var mb bytes.Buffer
	if err := reviewGit(ctx, root, &mb, "merge-base", base, head); err != nil {
		return ReviewedChange{}, fmt.Errorf("merge base of %s and %s: %w", baseRef, head, err)
	}
	c := ReviewedChange{WorkspaceID: workspaceID, Repository: root, BaseRef: baseRef, MergeBase: strings.TrimSpace(mb.String()), Head: head}
	h := sha256.New()
	var sink io.Writer = h
	if text != nil {
		sink = io.MultiWriter(h, text)
	}
	counted := &countingWriter{w: sink}
	if err := reviewGit(ctx, root, counted, reviewDiffArgs(c.MergeBase, head)...); err != nil {
		return ReviewedChange{}, fmt.Errorf("diff %s..%s: %w", c.MergeBase, head, err)
	}
	c.DiffSHA256, c.DiffBytes = hex.EncodeToString(h.Sum(nil)), counted.n
	return c, nil
}

// reviewDiffArgs is the diff an attestation digests. Every option that repository
// configuration could change is pinned on the command line (algorithm, context,
// prefixes, renames, relative paths, submodule format, order file, binary and full
// index), and no external diff or text conversion runs.
func reviewDiffArgs(from, to string) []string {
	return []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--binary", "--full-index",
		"--no-renames", "--no-relative", "--diff-algorithm=myers", "--indent-heuristic", "--inter-hunk-context=0",
		"-U3", "--src-prefix=a/", "--dst-prefix=b/", "--ignore-submodules=none", "--submodule=short",
		"-O" + os.DevNull, from, to, "--"}
}

// resolveCommit resolves ref to a full commit id. A ref shaped like an option is
// refused before git sees it.
func resolveCommit(ctx context.Context, root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") || len(ref) > 256 || strings.ContainsAny(ref, " \t\r\n\x00") {
		return "", fmt.Errorf("ref %q is not a revision: %w", ref, ErrInvalidInput)
	}
	var out bytes.Buffer
	if err := reviewGit(ctx, root, &out, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return "", fmt.Errorf("ref %q does not name a commit: %w", ref, ErrInvalidInput)
	}
	return strings.TrimSpace(out.String()), nil
}

// reviewGit runs reviewGitCommand to completion within reviewDiffTimeout, writing
// stdout to w.
func reviewGit(ctx context.Context, root string, w io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, reviewDiffTimeout)
	defer cancel()
	cmd := reviewGitCommand(ctx, root, args...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 4 << 10}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// reviewGitCommand is git in root with the system and global configuration ignored,
// hooks and the fsmonitor off, and every GIT_* and XMUSTARD_* variable removed from its
// environment (the approver's token never reaches it).
func reviewGitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.quotePath=true"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir, cmd.WaitDelay = root, time.Second
	cmd.Env = append(scrubbedGitEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	return cmd
}

func scrubbedGitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "XMUSTARD_") {
			out = append(out, kv)
		}
	}
	return out
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// limitedBuffer keeps the first max bytes written and discards the rest.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// ApproveMerge records h's attestation that they reviewed the change from the merge
// base of baseRef to headRef, citing reviews (review record ids, at most 32).
func ApproveMerge(ctx context.Context, dataDir, workspaceID string, h HumanApprover, baseRef, headRef string,
	reviews []string, note string) (*MergeApproval, error) {
	if len(reviews) > maxReviewRecords {
		return nil, fmt.Errorf("at most %d review records: %w", maxReviewRecords, ErrInvalidInput)
	}
	for _, id := range reviews {
		if err := validateSafeID("review record", id); err != nil {
			return nil, err
		}
	}
	change, err := DiffReviewedChange(ctx, dataDir, workspaceID, baseRef, headRef)
	if err != nil {
		return nil, err
	}
	if change.MergeBase == change.Head {
		return nil, fmt.Errorf("%s is already in %s; there is no change to approve: %w", headRef, baseRef, ErrInvalidInput)
	}
	var red ingestRedaction
	red.scrub(&note)
	data, err := eventObject(mergeApprovalData{ReviewedChange: change, ReviewRecords: append([]string{}, reviews...),
		Assurance: h.Assurance, Enforcement: MergeApprovalEnforcement})
	if err != nil {
		return nil, err
	}
	var ev govstore.Event
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		ev, err = tx.AppendEvent(ctx, govstore.EventInput{WorkspaceID: workspaceID, Type: govstore.EventMergeApproval,
			Note: note, Data: data}, h.storeActor(change.Head))
		return err
	})
	if err != nil {
		return nil, err
	}
	return mergeApprovalFrom(ev)
}

// storeActor is the approver as the principal of an attestation event.
func (h HumanApprover) storeActor(head string) govstore.Actor {
	p := h.Principal
	return govstore.Actor{Principal: p.ID, Owner: fallbackString(p.Owner, p.ID), Kind: p.Kind, SessionID: "xmustard-ops",
		HeadSHA: head, Approval: h.Label()}
}

// RevokeMergeApproval withdraws the attestation recorded at seq. Any human approver may
// revoke, since revoking only removes trust; the reason is required.
func RevokeMergeApproval(ctx context.Context, dataDir, workspaceID string, h HumanApprover, seq int64, reason string) (*MergeApproval, error) {
	var red ingestRedaction
	red.scrub(&reason)
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to revoke an approval: %w", ErrInvalidInput)
	}
	var out *MergeApproval
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		approvals, err := listMergeApprovals(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		var target *MergeApproval
		for i := range approvals {
			if approvals[i].Seq == seq {
				target = &approvals[i]
			}
		}
		switch {
		case target == nil:
			return fmt.Errorf("merge approval %d of workspace %s: %w", seq, workspaceID, os.ErrNotExist)
		case target.Revoked != nil:
			return Conflict(fmt.Sprintf("merge approval %d was already revoked at %s", seq, target.Revoked.At))
		}
		ev, err := tx.AppendEvent(ctx, govstore.EventInput{WorkspaceID: workspaceID, Type: govstore.EventMergeApprovalRevoked,
			Note: reason, Data: map[string]any{"approval_seq": seq}}, h.storeActor(target.Head))
		if err != nil {
			return err
		}
		target.Revoked = &MergeRevocation{Seq: ev.Seq, By: ev.Principal, Reason: reason, At: ev.At}
		out = target
		return nil
	})
	return out, err
}

// MergeApprovalState reports the human approval state of merging headRef into baseRef
// (see MergeApprovalStatus). An attestation counts only when it is unrevoked, its
// recorded approver kind is human and its approver is still a human approver scoped to
// the workspace (a revoked token distrusts what it attested), and it is current only
// when it binds the change from the merge base of baseRef and the head: the same
// repository, merge base, head and diff digest. An attestation of a narrower change (made
// against another base) is therefore stale. The change is diffed at most once, and only
// when an attestation names the resolved head.
func MergeApprovalState(ctx context.Context, dataDir, workspaceID, baseRef, headRef string) (*MergeApprovalStatus, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	var approvals []MergeApproval
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		approvals, err = listMergeApprovals(ctx, r, workspaceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	st := &MergeApprovalStatus{WorkspaceID: workspaceID, Status: MergeApprovalNone, BaseRef: baseRef,
		Enforcement: MergeApprovalEnforcement}
	root := WorkspaceRepoScope(dataDir, workspaceID)
	if root == "" {
		return nil, fmt.Errorf("workspace %s has no repository root: %w", workspaceID, os.ErrNotExist)
	}
	if st.Head, err = resolveCommit(ctx, root, headRef); err != nil {
		return nil, err
	}
	base, err := resolveCommit(ctx, root, baseRef) // an unknown base is an error, never "none"
	if err != nil {
		return nil, err
	}
	approvers := humanApproversOf(dataDir, workspaceID)
	for i := len(approvals) - 1; i >= 0; i-- {
		a := approvals[i]
		switch {
		case a.Revoked != nil:
			st.Revoked++
			continue
		case a.ApproverKind != PrincipalHuman || !approvers[a.Approver]:
			st.Untrusted++
			continue
		}
		if st.Approval == nil {
			st.Status, st.Approval = MergeApprovalStale, &approvals[i]
		}
		if a.Head != st.Head || a.Repository != root {
			continue
		}
		if st.Current == nil {
			now, err := diffAt(ctx, workspaceID, root, baseRef, base, st.Head, nil)
			if err != nil {
				return nil, err
			}
			st.Current = &now
		}
		if a.same(*st.Current) {
			st.Status, st.Approval = MergeApprovalCurrent, &approvals[i]
			return st, nil
		}
	}
	return st, nil
}

// humanApproversOf is the ids of the principals that are human approvers scoped to
// workspaceID now: of kind human, holding the human-approver role, with a token that
// was not revoked.
func humanApproversOf(dataDir, workspaceID string) map[string]bool {
	out := map[string]bool{}
	for _, p := range ListPrincipals(dataDir) {
		if IsHumanApprover(&p) && p.AllowsWorkspace(workspaceID) {
			out[p.ID] = true
		}
	}
	return out
}

// listMergeApprovals reads every attestation of the workspace, oldest first, with the
// revocations applied.
func listMergeApprovals(ctx context.Context, r govstore.Reader, workspaceID string) ([]MergeApproval, error) {
	var out []MergeApproval
	index := map[int64]int{}
	var after int64
	for {
		evs, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: workspaceID, AfterSeq: after, Limit: storeListPage,
			Types: []string{govstore.EventMergeApproval, govstore.EventMergeApprovalRevoked}})
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			if err := applyMergeEvent(ev, &out, index); err != nil {
				return nil, err
			}
		}
		if len(evs) < storeListPage {
			return out, nil
		}
		after = evs[len(evs)-1].Seq
	}
}

// applyMergeEvent folds one attestation or revocation event into approvals.
func applyMergeEvent(ev govstore.Event, approvals *[]MergeApproval, index map[int64]int) error {
	if ev.Type == govstore.EventMergeApproval {
		a, err := mergeApprovalFrom(ev)
		if err != nil {
			return err
		}
		index[a.Seq] = len(*approvals)
		*approvals = append(*approvals, *a)
		return nil
	}
	var d mergeRevocationData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("merge approval revocation %d is malformed: %w", ev.Seq, err)
	}
	if i, ok := index[d.ApprovalSeq]; ok && (*approvals)[i].Revoked == nil {
		(*approvals)[i].Revoked = &MergeRevocation{Seq: ev.Seq, By: ev.Principal, Reason: ev.Note, At: ev.At}
	}
	return nil
}

// eventObject renders v as the JSON object an event records, so the store adds the
// write's provenance beside its fields rather than nesting them.
func eventObject(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(b, &out)
}

// mergeApprovalFrom reads an attestation event. A record that does not decode is an
// error, never an approval.
func mergeApprovalFrom(ev govstore.Event) (*MergeApproval, error) {
	var d struct {
		mergeApprovalData
		Provenance struct {
			Owner string `json:"owner"`
			Kind  string `json:"kind"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, fmt.Errorf("merge approval %d is malformed: %w", ev.Seq, err)
	}
	if d.Head == "" || d.DiffSHA256 == "" {
		return nil, fmt.Errorf("merge approval %d is malformed: %w", ev.Seq, errors.New("no head or diff digest"))
	}
	return &MergeApproval{Seq: ev.Seq, ReviewedChange: d.ReviewedChange, ReviewRecords: d.ReviewRecords,
		Approver: ev.Principal, ApproverOwner: d.Provenance.Owner, ApproverKind: d.Provenance.Kind,
		Assurance: d.Assurance, Note: ev.Note, At: ev.At, Enforcement: MergeApprovalEnforcement}, nil
}
