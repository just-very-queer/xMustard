package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
)

// Provenance binding, evidence-bound votes, principal distinctness and ingest redaction
// (WS-19B: PAR-PROV-04, PAR-PROV-05, PAR-SEC-04, D-16). Every governed write records
// who made it (principal, owner, kind), from where (session, tool call, run) and against
// what (HEAD and branch, the evidence handles it cites); the store appends that to each
// event it writes, and the entry keeps the repository state at propose and at promote.

// maxEvidenceHandles bounds how many evidence handles one write cites.
const maxEvidenceHandles = 8

// maxProvenanceLabel bounds a session or tool-call id taken from the transport.
const maxProvenanceLabel = 128

// ErrSameOwner: under the owner-distinct policy a principal may not verify memory whose
// author (or the author of the revision voted on) has the same owner.
var ErrSameOwner = errors.New("the owner-distinct policy forbids verifying memory written under the same owner")

// storeActor is the store actor for a governance write by a: its principal, owner and
// kind, its provenance, and the workspace HEAD, so every event records the repository
// state it was made against. An actor with no identity is an admin edit.
func (a ContextActor) storeActor(root string) govstore.Actor {
	sha, branch := gitHead(root)
	id := fallbackString(strings.TrimSpace(a.ID), adminEditor)
	return govstore.Actor{
		Principal: id, Owner: fallbackString(a.Owner, id), Kind: a.Kind, SessionID: a.SessionID,
		CallID: a.CallID, RunID: a.RunID, Evidence: a.Evidence, HeadSHA: sha, Branch: branch,
	}
}

// CheckProvenanceLabel validates a session or tool-call id a transport supplies: at most
// maxProvenanceLabel bytes of printable text. JSON-RPC ids keep their quotes, so the
// check is on shape, not on an id alphabet.
func CheckProvenanceLabel(kind, v string) error {
	if len(v) > maxProvenanceLabel || strings.IndexFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return fmt.Errorf("%s is not printable text of at most %d bytes: %w", kind, maxProvenanceLabel, ErrInvalidInput)
	}
	return nil
}

// bindProvenance checks the evidence handles and run a write cites and binds them to
// the actor. Fail closed: a handle must be a retained, unrevoked, unexpired original of
// this workspace that the actor may read, and a run must exist in the workspace.
func bindProvenance(dataDir, workspaceID string, a ContextActor, handles []string, runID string) (ContextActor, error) {
	handles = cleanPaths(handles)
	if len(handles) > maxEvidenceHandles {
		return a, fmt.Errorf("at most %d evidence handles: %w", maxEvidenceHandles, ErrInvalidInput)
	}
	if runID = strings.TrimSpace(runID); runID != "" {
		if _, err := loadRun(dataDir, workspaceID, runID); err != nil {
			return a, fmt.Errorf("run %q is not a run of workspace %s: %w", runID, workspaceID, ErrInvalidInput)
		}
	}
	for _, h := range handles {
		if err := checkEvidence(dataDir, workspaceID, h, a); err != nil {
			return a, err
		}
	}
	a.Evidence, a.RunID = handles, runID
	return a, nil
}

// checkEvidence authorizes one evidence handle for a at this moment by reading the
// first byte of its original under the evidence store's own rules: the workspace, the
// principal binding, revocation, expiry and the stored length are all checked.
func checkEvidence(dataDir, workspaceID, handle string, a ContextActor) error {
	req := evidence.ReadRequest{WorkspaceID: workspaceID, Handle: handle, Length: 1}
	if !a.OpenMode {
		req.Actor, req.AuthEnforced = a.ID, true
	}
	if _, err := evidence.NewStore(dataDir, evidence.DefaultLimits()).Read(context.Background(), req); err != nil {
		return fmt.Errorf("evidence handle %q: %v: %w", handle, err, ErrInvalidInput)
	}
	return nil
}

// Principal distinctness policies (D-16). token counts every token as a distinct
// verifier (the default); owner counts owners, so two tokens of one operator cannot
// verify each other's memory.
const (
	DistinctToken = "token"
	DistinctOwner = "owner"
)

// principalDistinctness is the operator's policy. An unreadable settings file or an
// unknown value takes the stricter owner policy.
func principalDistinctness(dataDir string) string {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return DistinctOwner
	}
	switch strings.ToLower(strings.TrimSpace(settings.PrincipalDistinctness)) {
	case "", DistinctToken:
		return DistinctToken
	}
	return DistinctOwner
}

// ownerBoundVerdicts are the verdicts that decide verification, so the owner-distinct
// policy applies to them.
var ownerBoundVerdicts = map[string]bool{govstore.VerdictApprove: true, govstore.VerdictReject: true}

// checkOwnerDistinct refuses a verification verdict on a peer-gated entry when the
// owner-distinct policy is on and the voter's owner is the owner of the entry's author
// or of the author of the revision voted on. Single-assertion gates are the operator
// opting out of peers, so they are left alone.
func checkOwnerDistinct(ctx context.Context, r govstore.Reader, dataDir string, e govstore.Entry, in govstore.VoteInput,
	voter ContextActor) error {
	if !ownerBoundVerdicts[in.Verdict] || e.RequiredVerifications <= 1 || principalDistinctness(dataDir) != DistinctOwner {
		return nil
	}
	rev := in.Revision
	if rev == 0 {
		rev = e.Revision
	}
	rv, err := r.GetRevision(ctx, e.ID, rev)
	if err != nil {
		return err
	}
	mine := fallbackString(voter.Owner, voter.ID)
	for _, author := range []string{fallbackString(e.SourceOwner, PrincipalOwner(dataDir, e.Source)), PrincipalOwner(dataDir, rv.Author)} {
		if strings.EqualFold(strings.TrimSpace(author), strings.TrimSpace(mine)) {
			return fmt.Errorf("%w: %s is owned by %s, like the author of %s revision %d", ErrSameOwner, voter.ID, mine, e.ID, rev)
		}
	}
	return nil
}

// ingestRedaction removes secrets from memory text before it is stored (PAR-SEC-04):
// each secret becomes a marker naming its rule, and the report travels back with the
// write so the caller learns what was replaced.
type ingestRedaction struct{ rep redact.Report }

func (ir *ingestRedaction) scrub(fields ...*string) {
	for _, f := range fields {
		out, rep := redact.Default().String(*f)
		*f = out
		ir.rep.Merge(rep)
	}
}

// scrubOptional scrubs an optional field in place of its caller's copy.
func (ir *ingestRedaction) scrubOptional(f *string) *string {
	if f == nil {
		return nil
	}
	v := *f
	ir.scrub(&v)
	return &v
}

// annotate reports the redaction on the written entry.
func (ir *ingestRedaction) annotate(e *ContextEntry) {
	if e == nil || !ir.rep.Redacted {
		return
	}
	rules := make([]string, 0, len(ir.rep.Rules))
	for rule := range ir.rep.Rules {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	rep := ir.rep
	e.Redactions = &rep
	e.Warnings = append(e.Warnings, fmt.Sprintf("redacted %d secret value(s) (%s) before storing", rep.Count, strings.Join(rules, ", ")))
}

// entryProvenance is how an entry was derived (PAR-PROV-04): the repository state at
// propose and at promote, the proposing session, and the tool call, run and evidence
// its propose event recorded.
func entryProvenance(ctx context.Context, r govstore.Reader, e govstore.Entry) (map[string]any, error) {
	out := map[string]any{
		"source_owner": e.SourceOwner,
		"propose":      repoState(e.ProposeHead, e.ProposeBranch, e.ProposeDirty, e.CreatedAt),
	}
	if e.SessionID != "" {
		out["session_id"] = e.SessionID
	}
	if e.PromotedAt != "" {
		out["promote"] = repoState(e.PromoteHead, e.PromoteBranch, e.PromoteDirty, e.PromotedAt)
	}
	events, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: e.WorkspaceID, EntryID: e.ID,
		Types: []string{govstore.EventPropose}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		if p := eventProvenance(events[0]); p != nil {
			out["derived_from"] = p
		}
	}
	return out, nil
}

func repoState(head, branch string, dirty *bool, at string) map[string]any {
	s := map[string]any{"head": head, "branch": branch, "at": at}
	if dirty != nil {
		s["dirty"] = *dirty
	}
	return s
}

// eventProvenance is the provenance an event recorded, nil when it has none.
func eventProvenance(ev govstore.Event) any {
	var data struct {
		Provenance any `json:"provenance"`
	}
	if len(ev.Data) == 0 || json.Unmarshal(ev.Data, &data) != nil {
		return nil
	}
	return data.Provenance
}

// verificationBasis is what each counting verdict on the served revision rests on
// (PAR-PROV-05): who cast it, their owner and kind, and the evidence it cited.
func verificationBasis(ctx context.Context, r govstore.Reader, e govstore.Entry) ([]map[string]any, error) {
	votes, err := r.ListVotes(ctx, e.ID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(votes))
	for _, v := range votes {
		b := map[string]any{"principal": v.Principal, "owner": fallbackString(v.PrincipalOwner, v.Principal),
			"verdict": v.Verdict, "revision": v.Revision, "at": v.At}
		for k, s := range map[string]string{"kind": v.PrincipalKind, "evidence_handle": v.EvidenceHandle,
			"target": v.Target, "session_id": v.SessionID} {
			if s != "" {
				b[k] = s
			}
		}
		out = append(out, b)
	}
	return out, nil
}
