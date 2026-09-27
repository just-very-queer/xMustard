package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
)

// The human-approval surface (WS-57, PAR-SEC-02, PAR-GOV-07). A human approver is a
// principal of kind human that holds the human-approver role. It acts where the human
// is present: `xmustard-ops approve|reject|queue|review`, with the token typed at the
// terminal or read from a file or the environment, and over MCP only through a client
// that asks the human to confirm each write (elicitation). Every write it makes carries
// an approval label, "<surface>/<assurance>". The assurance is user_presence only when
// the token was typed at the terminal, so no agent process could read it; anything else
// is advisory, because an agent process of the same user could have used the token.
// Neither is an enforcement: the store is a file the user's processes can write, so an
// approval records a human's decision, and enforcement stays with the branch
// protection or policy that consumes it.

// Where a human approver's write was made.
const (
	ApprovalSurfaceOps  = "ops"             // xmustard-ops approve|reject|review
	ApprovalSurfaceMCP  = "mcp_elicitation" // an MCP client confirmed it through elicitation
	ApprovalSurfaceHTTP = "http"            // a direct API call
)

// Whether an agent process could have read the token that made the write.
const (
	AssuranceUserPresence = "user_presence" // typed at the terminal and never stored
	AssuranceAdvisory     = "advisory"      // from a file, the environment or a client config
)

var assurances = map[string]bool{AssuranceUserPresence: true, AssuranceAdvisory: true}

// HumanApprovalLabel is the label a human approver's write records under
// provenance.approval.
func HumanApprovalLabel(surface, assurance string) string { return surface + "/" + assurance }

// IsHumanApprover reports whether p is a human approver: of kind human and holding the
// human-approver role (admin holds it too).
func IsHumanApprover(p *Principal) bool {
	return p != nil && p.Kind == PrincipalHuman && p.Has(RoleHumanApprover)
}

// ErrHumanApproverRequired: the surface needs a human approver's token.
var ErrHumanApproverRequired = errors.New("a human approver's token is required (kind human, human-approver role)")

// ErrSelfApproval: a human approver voted on memory they wrote.
var ErrSelfApproval = errors.New("a human approver cannot approve or reject memory they wrote")

// HumanApprover is an authenticated human approver and the assurance of its token.
type HumanApprover struct {
	Principal Principal
	Assurance string
}

// AuthorizeHumanApprover resolves raw to a human approver acting on workspaceID. The
// checks run in order and fail closed: a token is given, it resolves (known, not
// revoked, not expired), it is not the open-mode identity, it is of kind human, it holds
// the human-approver role, and it is scoped to the workspace. A refusal is audited.
func AuthorizeHumanApprover(dataDir, workspaceID, raw, assurance string) (HumanApprover, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return HumanApprover{}, err
	}
	if !assurances[assurance] {
		return HumanApprover{}, fmt.Errorf("assurance %q is not user_presence or advisory: %w", assurance, ErrInvalidInput)
	}
	deny := func(actor, why string) (HumanApprover, error) {
		RecordAuthAudit(dataDir, AuthAuditEvent{Action: "denied", Actor: actor, Detail: "human approval: " + why,
			Method: "CLI", Path: "xmustard-ops"})
		return HumanApprover{}, fmt.Errorf("%w: %s", ErrHumanApproverRequired, why)
	}
	p := ResolveToken(dataDir, raw)
	switch {
	case strings.TrimSpace(raw) == "":
		return deny("anonymous", "no token was given")
	case p == nil:
		return deny("anonymous", "the token is unknown, revoked or expired")
	case IsOpenModeIdentity(p.ID):
		return deny(p.ID, "the principal id is reserved for open mode")
	case p.Kind != PrincipalHuman:
		return deny(p.ID, fmt.Sprintf("principal %q is of kind %s, not human", p.ID, p.Kind))
	case !p.Has(RoleHumanApprover):
		return deny(p.ID, fmt.Sprintf("principal %q lacks the human-approver role (has %s)", p.ID, strings.Join(p.RoleSet(), ", ")))
	case !p.AllowsWorkspace(workspaceID):
		return deny(p.ID, fmt.Sprintf("principal %q is not scoped to workspace %s", p.ID, workspaceID))
	}
	return HumanApprover{Principal: *p, Assurance: assurance}, nil
}

// Label is the approval label of the approver's writes from the ops CLI.
func (h HumanApprover) Label() string { return HumanApprovalLabel(ApprovalSurfaceOps, h.Assurance) }

// actor is the approver as a memory writer. It casts verdicts only: the lifecycle
// shortcuts an admin or approver has over HTTP (retract, restore, purge) are not
// reached from here.
func (h HumanApprover) actor() ContextActor {
	p := h.Principal
	return ContextActor{ID: p.ID, Owner: p.Owner, Kind: p.Kind, SessionID: "xmustard-ops", Approval: h.Label()}
}

// HumanVerdict casts the approver's outcome, approve or reject, on an entry's served
// revision (revision 0) or its pending edit. It is an ordinary verdict of one more distinct
// principal: it counts toward the entry's quorum like any peer's, the owner-distinct
// policy applies (VerifyContextOutcome), and it is recorded with the principal's kind
// and the approval label. It never promotes on its own when the gate needs more peers.
// A human approver never votes on memory they wrote (ErrSelfApproval, whatever the
// quorum mode). Revision 0 is pinned to the served revision checked here, so a revision
// served after the check is refused rather than voted on unchecked.
func HumanVerdict(dataDir, workspaceID, entryID string, h HumanApprover, outcome string, revision int64, note string) (*ContextEntry, error) {
	if outcome != OutcomeApprove && outcome != OutcomeReject {
		return nil, fmt.Errorf("a human approver's outcome is approve or reject, not %q: %w", outcome, ErrInvalidInput)
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	if revision < 0 {
		return nil, fmt.Errorf("revision must be positive: %w", ErrInvalidInput)
	}
	ctx := context.Background()
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		e, _, err := loadEntryTx(ctx, r, workspaceID, entryID)
		if err != nil {
			return err
		}
		if revision == 0 {
			revision = e.Revision
		}
		rv, err := r.GetRevision(ctx, e.ID, revision)
		if err != nil {
			return err
		}
		if sameOwner(e.Source, h.Principal.ID) || sameOwner(rv.Author, h.Principal.ID) {
			return fmt.Errorf("%w: %s wrote %s revision %d", ErrSelfApproval, h.Principal.ID, e.ID, revision)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return VerifyContextOutcome(dataDir, workspaceID, entryID, h.actor(), VerifyRequest{Outcome: outcome, Revision: revision, Note: note})
}

// --- the approval queue ---------------------------------------------------------------

// maxQueueContent bounds the text one queue item shows of a proposal.
const maxQueueContent = 2000

// ApprovalItem is one memory write awaiting the approver's verdict: a pending proposal
// (the served revision of an unpromoted entry) or a pending edit (the entry's head
// revision). votes_needed counts the approvals still missing, from peers when the gate
// needs more than one.
type ApprovalItem struct {
	EntryID          string   `json:"entry_id"`
	Revision         int64    `json:"revision"`
	Kind             string   `json:"kind"` // proposal | edit
	Title            string   `json:"title"`
	Author           string   `json:"author"`
	Reason           string   `json:"reason,omitempty"`
	Content          string   `json:"content,omitempty"`
	ContentTruncated bool     `json:"content_truncated,omitempty"`
	ContentWithheld  string   `json:"content_withheld,omitempty"`
	Diff             string   `json:"diff,omitempty"`
	Approvals        int      `json:"approvals"`
	Rejections       int      `json:"rejections"`
	VotesNeeded      int      `json:"votes_needed"`
	HumanApprovals   []string `json:"human_approvals"`
	CreatedAt        string   `json:"created_at"`
}

// IndexBaselineRecord is one index_baseline event (WS-22) as the queue lists it: who
// rebuilt the change-tracking baseline, why, and at which HEAD. It needs no approval;
// it is listed so a human sees every reset, automatic or not.
type IndexBaselineRecord struct {
	Seq       int64  `json:"seq"`
	At        string `json:"at"`
	Principal string `json:"principal"`
	Reason    string `json:"reason"`
	Auto      bool   `json:"auto"`
	Head      string `json:"head"`
	Replaced  bool   `json:"replaced"`
	Previous  string `json:"previous_head,omitempty"`
}

// ApprovalQueue is what awaits one human approver in a workspace: the memory writes
// the approver may vote on (awaiting_me: not written by them, not already voted on by
// them, and not blocked by the owner-distinct policy; Skipped counts the rest), oldest
// first, at most limit of them with Total counting all, and the recent index baseline
// builds.
type ApprovalQueue struct {
	WorkspaceID    string                `json:"workspace_id"`
	Approver       string                `json:"approver"`
	Items          []ApprovalItem        `json:"items"`
	Total          int                   `json:"total"`
	Skipped        map[string]int        `json:"skipped"`
	IndexBaselines []IndexBaselineRecord `json:"index_baselines"`
}

// Why a pending write is not in the approver's queue.
const (
	skipOwn       = "own"
	skipVoted     = "voted"
	skipSameOwner = "same_owner"
)

// queueCandidate is one pending revision of an entry.
type queueCandidate struct {
	entry    govstore.Entry
	revision int64
	kind     string
}

// HumanApprovalQueue lists what awaits h in workspaceID (see ApprovalQueue). limit
// bounds the items rendered (default 50) and baselines the index baseline builds listed
// (default 10), newest last.
func HumanApprovalQueue(dataDir, workspaceID string, h HumanApprover, limit, baselines int) (*ApprovalQueue, error) {
	limit, baselines = positiveOr(limit, 50), positiveOr(baselines, 10)
	ctx := context.Background()
	q := &ApprovalQueue{WorkspaceID: workspaceID, Approver: h.Principal.ID, Items: []ApprovalItem{},
		Skipped: map[string]int{skipOwn: 0, skipVoted: 0, skipSameOwner: 0}, IndexBaselines: []IndexBaselineRecord{}}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		f := govstore.EntryFilter{WorkspaceID: workspaceID, Limit: storeListPage}
		for {
			page, err := r.ListEntries(ctx, f)
			if err != nil {
				return err
			}
			for _, e := range page {
				for _, c := range pendingRevisions(e) {
					if err := q.consider(ctx, r, dataDir, h, c, limit); err != nil {
						return err
					}
				}
			}
			if len(page) < storeListPage {
				break
			}
			f.AfterCursor = page[len(page)-1].Cursor
		}
		var err error
		q.IndexBaselines, err = recentIndexBaselines(ctx, r, workspaceID, baselines)
		return err
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

// pendingRevisions are the revisions of e that await verdicts: its served revision
// while it is an unpromoted proposal, and a pending edit.
func pendingRevisions(e govstore.Entry) []queueCandidate {
	var out []queueCandidate
	if e.Status == govstore.StatusPending {
		out = append(out, queueCandidate{entry: e, revision: e.Revision, kind: "proposal"})
	}
	if e.HeadRevision > e.Revision {
		out = append(out, queueCandidate{entry: e, revision: e.HeadRevision, kind: "edit"})
	}
	return out
}

// consider adds c to the queue when h may vote on it, or counts why not.
func (q *ApprovalQueue) consider(ctx context.Context, r govstore.Reader, dataDir string, h HumanApprover, c queueCandidate, limit int) error {
	e, me := c.entry, h.Principal.ID
	rv, err := r.GetRevision(ctx, e.ID, c.revision)
	if err != nil {
		return err
	}
	votes, err := r.ListVotes(ctx, e.ID, c.revision)
	if err != nil {
		return err
	}
	skip := ""
	switch {
	case sameOwner(e.Source, me) || sameOwner(rv.Author, me):
		skip = skipOwn
	case votedBy(votes, me):
		skip = skipVoted
	default:
		err := checkOwnerDistinct(ctx, r, dataDir, e, govstore.VoteInput{EntryID: e.ID, Revision: c.revision,
			Verdict: govstore.VerdictApprove}, h.actor())
		if errors.Is(err, ErrSameOwner) {
			skip = skipSameOwner
		} else if err != nil {
			return err
		}
	}
	if skip != "" {
		q.Skipped[skip]++
		return nil
	}
	q.Total++
	if len(q.Items) >= limit {
		return nil
	}
	item, err := queueItem(ctx, r, c, rv, votes)
	if err != nil {
		return err
	}
	q.Items = append(q.Items, item)
	return nil
}

// votedBy reports whether principal has a counting verdict among votes.
func votedBy(votes []govstore.Vote, principal string) bool {
	for _, v := range votes {
		if sameOwner(v.Principal, principal) {
			return true
		}
	}
	return false
}

// queueItem renders one candidate: a proposal with its text (bound to its digest), an
// edit with its reason and diff, and the verdicts cast so far.
func queueItem(ctx context.Context, r govstore.Reader, c queueCandidate, rv govstore.Revision, votes []govstore.Vote) (ApprovalItem, error) {
	e := c.entry
	tally, err := r.Tally(ctx, e.ID, c.revision)
	if err != nil {
		return ApprovalItem{}, err
	}
	need := max(e.RequiredVerifications, 1)
	have := tally.Approvals
	if need > 1 {
		have = tally.PeerApprovals
	}
	item := ApprovalItem{EntryID: e.ID, Revision: c.revision, Kind: c.kind, Title: fallbackString(rv.Title, e.Title),
		Author: rv.Author, Reason: rv.Reason, Approvals: tally.Approvals, Rejections: tally.Rejections,
		VotesNeeded: max(need-have, 0), HumanApprovals: []string{}, CreatedAt: rv.CreatedAt}
	for _, v := range votes {
		if v.Verdict == govstore.VerdictApprove && v.PrincipalKind == PrincipalHuman {
			item.HumanApprovals = append(item.HumanApprovals, v.Principal)
		}
	}
	if c.kind == "edit" {
		item.Diff, err = revisionDiff(ctx, r, e.ID, e.Revision, c.revision)
		return item, err
	}
	switch {
	case rv.ContentDropped:
		item.ContentWithheld = "content was purged"
	case contentDigest(rv.Content) != rv.ContentDigest:
		item.ContentWithheld = "content does not match its digest"
	default:
		item.Content, item.ContentTruncated = clipRunes(rv.Content, maxQueueContent)
	}
	return item, nil
}

// clipRunes cuts s to at most n bytes on a rune boundary.
func clipRunes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// recentIndexBaselines lists the newest n index_baseline events of the workspace,
// oldest first.
func recentIndexBaselines(ctx context.Context, r govstore.Reader, workspaceID string, n int) ([]IndexBaselineRecord, error) {
	ring := make([]IndexBaselineRecord, 0, n)
	var after int64
	for {
		evs, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: workspaceID,
			Types: []string{govstore.EventIndexBaseline}, AfterSeq: after, Limit: storeListPage})
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			if len(ring) == n {
				ring = ring[1:]
			}
			ring = append(ring, indexBaselineRecord(ev))
		}
		if len(evs) < storeListPage {
			return ring, nil
		}
		after = evs[len(evs)-1].Seq
	}
}

// indexBaselineRecord reads the fields WS-22 records in an index_baseline event.
func indexBaselineRecord(ev govstore.Event) IndexBaselineRecord {
	var d struct {
		Reason       string `json:"reason"`
		Auto         bool   `json:"auto"`
		Head         string `json:"head"`
		Replaced     bool   `json:"replaced"`
		PreviousHead string `json:"previous_head"`
	}
	_ = json.Unmarshal(ev.Data, &d) // a malformed record still lists its seq, time and principal
	return IndexBaselineRecord{Seq: ev.Seq, At: ev.At, Principal: ev.Principal, Reason: d.Reason, Auto: d.Auto,
		Head: fallbackString(d.Head, ev.HeadSHA), Replaced: d.Replaced, Previous: d.PreviousHead}
}

func positiveOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}
