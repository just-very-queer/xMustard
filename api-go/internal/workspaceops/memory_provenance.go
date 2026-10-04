package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
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
		CallID: a.CallID, RunID: a.RunID, Evidence: a.Evidence, HeadSHA: sha, Branch: branch, Approval: a.Approval,
	}
}

// ProvenanceLabel is the session or tool-call id a transport supplies, as it is
// recorded: printable text (ErrInvalidInput otherwise) of at most maxProvenanceLabel
// bytes. A longer id is still recorded, not refused, so a client with long JSON-RPC ids
// can write memory: its prefix is kept and the rest becomes a digest, which keeps
// distinct ids distinct. JSON-RPC ids keep their quotes, so the check is on shape, not
// on an id alphabet.
func ProvenanceLabel(kind, v string) (string, error) {
	if strings.IndexFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return "", fmt.Errorf("%s is not printable text: %w", kind, ErrInvalidInput)
	}
	if len(v) <= maxProvenanceLabel {
		return v, nil
	}
	sum := sha256.Sum256([]byte(v))
	tail := "~sha256:" + hex.EncodeToString(sum[:8])
	cut := maxProvenanceLabel - len(tail)
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + tail, nil
}

// bindProvenance checks the evidence handles and run a write cites and binds them to
// the actor. Fail closed: a handle must be a retained, unrevoked, unexpired original of
// this workspace that the actor may read, and a run must exist in the workspace. A
// handle that captured an untrusted tool's output (WebFetch, another MCP server), or an
// xMustard result that carried quarantined memory, sets the actor's quarantine, so what
// it writes is quarantined.
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
		quarantine, err := checkEvidence(dataDir, workspaceID, h, a)
		if err != nil {
			return a, err
		}
		// content derived from an untrusted capture is quarantined (WS-56)
		a.Quarantine = fallbackString(a.Quarantine, quarantine)
	}
	a.Evidence, a.RunID = handles, runID
	return a, nil
}

// checkEvidence authorizes one evidence handle for a at this moment by reading the
// first byte of its original under the evidence store's own rules: the workspace, the
// principal binding, revocation, expiry and the stored length are all checked. It
// returns the quarantine of content derived from the capture: the one recorded when it
// was captured, else (a capture from before captures recorded it) its tool's.
func checkEvidence(dataDir, workspaceID, handle string, a ContextActor) (string, error) {
	req := evidence.ReadRequest{WorkspaceID: workspaceID, Handle: handle, Length: 1}
	if !a.OpenMode {
		req.Actor, req.AuthEnforced = a.ID, true
	}
	page, err := evidence.NewStore(dataDir, evidence.DefaultLimits()).Read(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("evidence handle %q: %v: %w", handle, err, ErrInvalidInput)
	}
	return fallbackString(page.Quarantine, injection.CaptureQuarantine(page.Tool)), nil
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
// owner-distinct policy is on and the voter's owner already has a voice on the revision
// voted on: it owns the entry's author or the revision's author, or another of its
// principals has a counting verdict there. So the quorum counts distinct owners and two
// tokens of one operator can neither verify each other's memory nor stack their votes;
// the voter's own earlier verdict is replaced as usual. Owners are the ones recorded at
// write time, falling back to the token store only for writes that recorded none.
// Single-assertion gates are the operator opting out of peers, so they are left alone.
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
	revOwner, err := revisionOwner(ctx, r, dataDir, e, rv)
	if err != nil {
		return err
	}
	mine := fallbackString(voter.Owner, voter.ID)
	refuse := func(whom string) error {
		return fmt.Errorf("%w: %s is owned by %s, like %s of %s revision %d", ErrSameOwner, voter.ID, mine, whom, e.ID, rev)
	}
	if sameOwner(recordedOwner(dataDir, e.SourceOwner, e.Source), mine) {
		return refuse("the entry's author")
	}
	if sameOwner(revOwner, mine) {
		return refuse("the revision's author")
	}
	votes, err := r.ListVotes(ctx, e.ID, rev)
	if err != nil {
		return err
	}
	for _, v := range votes {
		if ownerBoundVerdicts[v.Verdict] && !sameOwner(v.Principal, voter.ID) &&
			sameOwner(recordedOwner(dataDir, v.PrincipalOwner, v.Principal), mine) {
			return refuse("a principal (" + v.Principal + ") that already voted " + v.Verdict)
		}
	}
	return nil
}

// batchVerdict names the verdict a batched guard run answers for. It defaults to
// VerdictApprove, the verdict the approval queue asks about; the other tests pass a
// verdict that does not decide verification to reach the guard's early return.
type batchVerdict string

// batchOwnerVoices answers checkOwnerDistinct for many pending candidates at once,
// with one govstore query for all of them instead of three per row (WS-FIX-22b). It
// re-derives the same rule from the same rows, so
// TestTheBatchedOwnerGuardAnswersExactlyWhatThePerRowGuardAnswers pins the two
// together; read that test as part of this function's contract.
//
// The result is one entry per candidate, in order: ErrSameOwner for the ones the
// per-row guard refuses, nil for the ones it admits. An error from the batched read
// is returned for every candidate, so a caller that must not skip a guard fails closed
// on the whole batch rather than admitting rows it could not check.
func batchOwnerVoices(ctx context.Context, r govstore.Reader, dataDir string, candidates []queueCandidate,
	h HumanApprover, verdict ...batchVerdict) ([]error, error) {
	out := make([]error, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}
	// the same early return checkOwnerDistinct makes, as one clause: the approval
	// queue asks about approve, so an absent verdict is approve
	v := govstore.VerdictApprove
	if len(verdict) > 0 && verdict[0] != "" {
		v = string(verdict[0])
	}
	if !ownerBoundVerdicts[v] || principalDistinctness(dataDir) != DistinctOwner {
		return out, nil
	}
	mine := fallbackString(h.Principal.Owner, h.Principal.ID)

	// only the rows the guard would read: a single-assertion gate is the operator
	// opting out of peers, so such an entry is admitted whatever its authors are.
	keys := make([]govstore.PendingKey, 0, len(candidates))
	guarded := make([]int, 0, len(candidates))
	for i, c := range candidates {
		if c.entry.RequiredVerifications <= 1 {
			continue
		}
		rev := c.revision
		if rev == 0 {
			rev = c.entry.Revision
		}
		keys = append(keys, govstore.PendingKey{EntryID: c.entry.ID, Revision: rev})
		guarded = append(guarded, i)
	}
	if len(keys) == 0 {
		return out, nil
	}

	revs, err := r.RevisionOwners(ctx, keys)
	if err != nil {
		return nil, err
	}
	voices, err := r.PendingOwnerVoices(ctx, keys)
	if err != nil {
		return nil, err
	}
	for n, i := range guarded {
		c := candidates[i]
		key := keys[n]
		rev := c.revision
		if rev == 0 {
			rev = c.entry.Revision
		}
		refuse := func(whom string) error {
			return fmt.Errorf("%w: %s is owned by %s, like %s of %s revision %d", ErrSameOwner, h.Principal.ID, mine, whom, c.entry.ID, rev)
		}
		if sameOwner(recordedOwner(dataDir, c.entry.SourceOwner, c.entry.Source), mine) {
			out[i] = refuse("the entry's author")
			continue
		}
		ro, ok := revs[key]
		if !ok {
			out[i] = fmt.Errorf("revision %s@%d: %w", key.EntryID, key.Revision, govstore.ErrNotFound)
			continue
		}
		// the same three lines revisionOwner runs, on the same rows: the proposal that
		// started the entry takes the owner the entry recorded (which survives the token
		// being revoked), and a later revision takes the owner its edit event recorded
		revOwner := ro.Owner
		if ro.Revision == 1 && sameOwner(ro.Author, ro.Source) {
			revOwner = ro.SourceOwner
		}
		revOwner = recordedOwner(dataDir, revOwner, ro.Author)
		if sameOwner(revOwner, mine) {
			out[i] = refuse("the revision's author")
			continue
		}
		for _, v := range voices[key] {
			if !sameOwner(v.Principal, h.Principal.ID) &&
				sameOwner(recordedOwner(dataDir, v.PrincipalOwner, v.Principal), mine) {
				out[i] = refuse("a principal (" + v.Principal + ") that already voted " + v.Verdict)
				break
			}
		}
	}
	return out, nil
}

// revisionOwner is the owner recorded when rv was written: the entry's source owner for
// the proposal, the owner its edit event carries for a later revision.
func revisionOwner(ctx context.Context, r govstore.Reader, dataDir string, e govstore.Entry, rv govstore.Revision) (string, error) {
	if rv.Revision == 1 && sameOwner(rv.Author, e.Source) {
		return recordedOwner(dataDir, e.SourceOwner, e.Source), nil
	}
	events, err := r.ListEvents(ctx, govstore.EventFilter{WorkspaceID: e.WorkspaceID, EntryID: e.ID,
		Types: []string{govstore.EventEdit}, Revision: rv.Revision, Limit: 1})
	if err != nil {
		return "", err
	}
	var recorded string
	if len(events) > 0 {
		var data struct {
			Provenance struct {
				Owner string `json:"owner"`
			} `json:"provenance"`
		}
		if json.Unmarshal(events[0].Data, &data) == nil {
			recorded = data.Provenance.Owner
		}
	}
	return recordedOwner(dataDir, recorded, rv.Author), nil
}

// recordedOwner is the owner a write recorded for principal, or the token store's
// answer for a write that recorded none.
func recordedOwner(dataDir, recorded, principal string) string {
	return fallbackString(strings.TrimSpace(recorded), PrincipalOwner(dataDir, principal))
}

func sameOwner(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
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

// annotate reports the redaction on the written entry, merged with any redaction an
// inner write already reported there.
func (ir *ingestRedaction) annotate(e *ContextEntry) {
	if e == nil || !ir.rep.Redacted {
		return
	}
	rules := make([]string, 0, len(ir.rep.Rules))
	for rule := range ir.rep.Rules {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	var rep redact.Report
	rep.Merge(ir.rep)
	if e.Redactions != nil {
		rep.Merge(*e.Redactions)
	}
	e.Redactions = &rep
	e.Warnings = append(e.Warnings, fmt.Sprintf("redacted %d secret value(s) (%s) before storing", ir.rep.Count, strings.Join(rules, ", ")))
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
