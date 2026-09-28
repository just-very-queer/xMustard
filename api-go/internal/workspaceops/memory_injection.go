package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// Injection admission (WS-56). Memory that xMustard pushes into an agent's context
// without being asked, before a native tool call (a hook, WS-23) or into every session
// (the core tier in initialize.instructions and SessionStart, WS-31), passes the
// injection policy first: it must be served, approved on its served revision by a human
// approver, not quarantined, and free of instruction patterns. What passes is framed as
// data. Recall and fetch by id are pulled surfaces instead: they serve what the caller's
// role may read, labeled with quarantine and injection_flags.

// maxInjectionCandidates bounds one admission.
const maxInjectionCandidates = 64

// InjectedMemory is one memory a pushed surface admitted: the served revision whose
// approval admitted it, and that revision's text.
type InjectedMemory struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Title    string `json:"title"`
	Basis    string `json:"basis"`
	Content  string `json:"content"`
}

// Block is the memory as the data frame a pushed surface carries (injection.Frame),
// labeled with the revision it pins. A surface that budgets its text frames the
// admitted memories one at a time.
func (m InjectedMemory) Block() injection.Block {
	return injection.Block{Kind: "memory", ID: m.ID, Rev: strconv.FormatInt(m.Revision, 10), Trust: m.Basis,
		Text: m.Title + "\n" + m.Content}
}

// WithheldMemory is a candidate the surface refused, and why: an injection reason
// (quarantined, needs_human_approved, instruction_pattern) or one of withheldNotFound,
// withheldNotServed and withheldContentChanged.
type WithheldMemory struct {
	ID     string   `json:"id"`
	Reason string   `json:"reason"`
	Flags  []string `json:"injection_flags,omitempty"`
}

// MemoryInjection is what one push may carry.
type MemoryInjection struct {
	Surface  string           `json:"surface"`
	Admitted []InjectedMemory `json:"admitted"`
	Withheld []WithheldMemory `json:"withheld"`
	// Text is the admitted memory framed as data after the injection notice, in
	// candidate order; empty when nothing was admitted.
	Text string `json:"text"`
}

// Why AdmitMemory withholds a candidate before the policy sees it.
const (
	withheldNotFound       = "not_found"       // no such entry in this workspace
	withheldNotServed      = "not_served"      // pending, rejected, expired or no longer active
	withheldContentChanged = "content_changed" // the served content does not match its digest
)

// AdmitMemory decides which of the candidate entries a pushed surface may carry and
// frames them. The caller picks the candidates (the memories bound to a path a hook
// sees, or the core tier) and budgets the text. Fail closed: a surface that is not
// pushed is refused, and an unreadable token store, memory policy or memory store is an
// error, so nothing is pushed. Each candidate is checked in order: it exists in the
// workspace, it is served, its content matches its digest, and then injection.Decide
// applies the surface's policy, whose basis counts a human approval only as
// humanApproved defines it, at the strength the workspace's memory policy asks of the
// surface (MemoryPolicy.PushApproval).
func AdmitMemory(ctx context.Context, dataDir, workspaceID string, surface injection.Surface, ids []string) (*MemoryInjection, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if !injection.Pushed(surface) {
		return nil, fmt.Errorf("surface %q is not a pushed surface: %w", surface, ErrInvalidInput)
	}
	ids = cleanPaths(ids)
	if len(ids) > maxInjectionCandidates {
		return nil, fmt.Errorf("at most %d candidates per admission: %w", maxInjectionCandidates, ErrInvalidInput)
	}
	for _, id := range ids {
		if err := validateSafeID("entry", id); err != nil {
			return nil, err
		}
	}
	gate, err := loadApprovalGate(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	var out *MemoryInjection
	err = memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		out, err = admitIn(ctx, r, workspaceID, surface, ids, gate)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// approvalGate is what a human approval is judged against: the workspace's human
// approvers now and the approval strength each pushed surface needs.
type approvalGate struct {
	approvers map[string]bool
	policy    MemoryPolicy
}

// loadApprovalGate reads the human approvers and the memory policy; either one
// unreadable is an error (fail closed).
func loadApprovalGate(dataDir, workspaceID string) (approvalGate, error) {
	approvers, err := humanApprovers(dataDir, workspaceID)
	if err != nil {
		return approvalGate{}, fmt.Errorf("human approvers: %w", err)
	}
	policy, err := LoadMemoryPolicy(dataDir, workspaceID)
	if err != nil {
		return approvalGate{}, err
	}
	return approvalGate{approvers: approvers, policy: policy}, nil
}

// digest identifies what the gate admits: the approvers and the policy.
func (g approvalGate) digest() string {
	return strings.Join(slices.Sorted(maps.Keys(g.approvers)), ",") + "|" + g.policy.Digest()
}

// admitIn is AdmitMemory inside one read snapshot r.
func admitIn(ctx context.Context, r govstore.Reader, workspaceID string, surface injection.Surface, ids []string, g approvalGate) (*MemoryInjection, error) {
	out := &MemoryInjection{Surface: string(surface), Admitted: []InjectedMemory{}, Withheld: []WithheldMemory{}}
	served, err := servedCandidates(ctx, r, workspaceID, ids, out)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(served))
	for i, e := range served {
		keys[i] = e.ID
	}
	votes, err := r.ServedVotes(ctx, keys)
	if err != nil {
		return nil, err
	}
	contents, err := r.EntryContents(ctx, keys)
	if err != nil {
		return nil, err
	}
	level := g.policy.pushLevel(surface)
	var blocks []injection.Block
	for _, e := range served {
		c := contents[e.ID]
		if c.Withheld != "" || c.Digest != e.ContentDigest || contentDigest(c.Content) != e.ContentDigest {
			out.Withheld = append(out.Withheld, WithheldMemory{ID: e.ID, Reason: withheldContentChanged})
			continue
		}
		rv, err := r.GetRevision(ctx, e.ID, e.Revision)
		if err != nil {
			return nil, err
		}
		approved, err := humanApprovedAt(ctx, r, e, rv.Author, votes[e.ID], g.approvers, level)
		if err != nil {
			return nil, err
		}
		basis := injection.BasisOf(e.Promoted, e.VerificationMode, approved)
		d := injection.Decide(surface, injection.Candidate{Basis: basis, Quarantine: e.Metadata[govstore.MetaQuarantine],
			Scan: injection.Scan(e.Title, e.Description, c.Content)})
		if !d.Admit {
			out.Withheld = append(out.Withheld, WithheldMemory{ID: e.ID, Reason: d.Reason, Flags: d.Flags})
			continue
		}
		m := InjectedMemory{ID: e.ID, Revision: e.Revision, Title: e.Title, Basis: basis.String(), Content: c.Content}
		out.Admitted = append(out.Admitted, m)
		blocks = append(blocks, m.Block())
	}
	out.Text = injection.FrameAll(blocks)
	return out, nil
}

// servedCandidates reads the candidates and returns the served ones, in order; the
// others are recorded withheld on out. An entry of another workspace is reported not
// found, never read further.
func servedCandidates(ctx context.Context, r govstore.Reader, workspaceID string, ids []string, out *MemoryInjection) ([]govstore.Entry, error) {
	now := time.Now()
	served := make([]govstore.Entry, 0, len(ids))
	for _, id := range ids {
		e, err := r.GetEntry(ctx, id)
		switch {
		case errors.Is(err, govstore.ErrNotFound) || err == nil && e.WorkspaceID != workspaceID:
			out.Withheld = append(out.Withheld, WithheldMemory{ID: id, Reason: withheldNotFound})
		case err != nil:
			return nil, err
		case !e.Served(now):
			out.Withheld = append(out.Withheld, WithheldMemory{ID: id, Reason: withheldNotServed})
		default:
			served = append(served, e)
		}
	}
	return served, nil
}

// humanApprovals are the principals whose approval makes e human-approved: a counting
// approve verdict (on the served revision, in the current vote epoch) cast as kind human
// by a principal that is a human approver of the workspace now (humanApprovers), and
// that wrote neither the entry nor the served revision.
func humanApprovals(e govstore.Entry, revisionAuthor string, votes []govstore.Vote, approvers map[string]bool) []string {
	var out []string
	for _, v := range votes {
		if v.Verdict == govstore.VerdictApprove && v.PrincipalKind == PrincipalHuman && approvers[v.Principal] &&
			!sameOwner(v.Principal, e.Source) && !sameOwner(v.Principal, revisionAuthor) {
			out = append(out, v.Principal)
		}
	}
	return out
}

// userPresenceLabel is the approval label of a vote cast with a presence-only token
// typed at the ops terminal (WS-57): the strongest assurance xMustard records. It is
// still not proof a human was present: a process that learns a presence-only token can
// type it through a pseudo-terminal.
var userPresenceLabel = HumanApprovalLabel(ApprovalSurfaceOps, AssuranceUserPresence)

// humanApprovedAt is humanApproved at the policy's level: human_approver takes any human
// approval; user_presence takes only one whose vote was recorded with the
// ops/user_presence label, read from the vote's own event (the verdict a principal
// cast last on the revision is its newest vote event there). An unknown level admits
// nothing.
func humanApprovedAt(ctx context.Context, r govstore.Reader, e govstore.Entry, revisionAuthor string, votes []govstore.Vote,
	approvers map[string]bool, level string) (bool, error) {
	principals := humanApprovals(e, revisionAuthor, votes, approvers)
	switch {
	case len(principals) == 0:
		return false, nil
	case level == PushApprovalHumanApprover:
		return true, nil
	case level != PushApprovalUserPresence:
		return false, nil
	}
	events, err := r.ListEvents(ctx, govstore.EventFilter{EntryID: e.ID, Revision: e.Revision,
		Types: []string{govstore.EventVote, govstore.EventReject}, Limit: 1000})
	if err != nil {
		return false, err
	}
	label := map[string]string{} // principal -> approval label of its newest vote event
	for _, ev := range events {
		var data struct {
			Provenance struct {
				Approval string `json:"approval"`
			} `json:"provenance"`
		}
		_ = json.Unmarshal(ev.Data, &data) // a record that does not decode carries no label
		label[strings.ToLower(strings.TrimSpace(ev.Principal))] = data.Provenance.Approval
	}
	return slices.ContainsFunc(principals, func(p string) bool {
		return label[strings.ToLower(strings.TrimSpace(p))] == userPresenceLabel
	}), nil
}

// humanApprovers are the principals that are human approvers of workspaceID now (WS-09
// roles, WS-19B kinds, WS-57 IsHumanApprover): a file-backed token (environment tokens
// are agents) of kind human that holds the human-approver role (admin holds it too), is
// scoped to the workspace, has not expired and is not the open-mode identity. A revoked token is gone
// from the store, so its approvals stop counting. An unreadable token store is an
// error: nothing counts as approved.
func humanApprovers(dataDir, workspaceID string) (map[string]bool, error) {
	store, err := cachedTokenStore(dataDir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range store.recs {
		p := r.principal()
		if tokenExpired(r.ExpiresAt) || IsOpenModeIdentity(p.ID) || !IsHumanApprover(&p) || !p.AllowsWorkspace(workspaceID) {
			continue
		}
		out[p.ID] = true
	}
	return out, nil
}
