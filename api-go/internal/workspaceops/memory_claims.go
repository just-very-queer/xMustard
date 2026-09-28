package workspaceops

import (
	"context"
	"fmt"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// Structured claims and typed relations (WS-27, PAR-GOV-10). A proposal may state
// claims {subject, predicate, object} with a predicate from govstore's fixed coding
// vocabulary, and typed edges to other memories. Claims feed the T1 conflict tier and
// recall's subject and predicate filters; relations are recorded as the author states
// them. Both attach to the new entry in the transaction that creates it.

// MemoryClaim is one structured claim of a proposal.
type MemoryClaim struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// MemoryRelation is a typed edge from a proposal to an existing memory.
type MemoryRelation struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// Claim and relation bounds per proposal.
const (
	maxClaims       = 8
	maxRelations    = 8
	maxClaimPartLen = 256
)

// proposalRelationKinds are the edges a proposal may state. supersedes goes through
// the proposal's supersedes list and merged_into through verify's duplicate_of.
var proposalRelationKinds = setOf(govstore.RelationRefines, govstore.RelationContradicts, govstore.RelationDuplicates,
	govstore.RelationReferences)

// cleanClaims trims and validates claims; the predicate must be in the vocabulary.
func cleanClaims(in []MemoryClaim) ([]MemoryClaim, error) {
	if len(in) > maxClaims {
		return nil, fmt.Errorf("at most %d claims: %w", maxClaims, ErrInvalidInput)
	}
	out := make([]MemoryClaim, 0, len(in))
	for _, c := range in {
		c = MemoryClaim{Subject: claimSubject(c.Subject), Predicate: strings.ToLower(strings.TrimSpace(c.Predicate)),
			Object: strings.TrimSpace(c.Object)}
		if !govstore.ValidPredicate(c.Predicate) {
			return nil, fmt.Errorf("claim predicate %q is not one of %s: %w", c.Predicate,
				strings.Join(govstore.Predicates, ", "), ErrInvalidInput)
		}
		for _, part := range []string{c.Subject, c.Object} {
			if part == "" || len(part) > maxClaimPartLen || strings.ContainsRune(part, 0) {
				return nil, fmt.Errorf("claim subject and object are 1-%d bytes: %w", maxClaimPartLen, ErrInvalidInput)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// claimSubject is a subject as claims store and match it: trimmed and lower-cased, so
// "Go" and "go" are one subject.
func claimSubject(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// cleanRelations validates relations: a proposal-level kind and a well-formed target.
func cleanRelations(in []MemoryRelation) ([]MemoryRelation, error) {
	if len(in) > maxRelations {
		return nil, fmt.Errorf("at most %d relations: %w", maxRelations, ErrInvalidInput)
	}
	out := make([]MemoryRelation, 0, len(in))
	for _, r := range in {
		r = MemoryRelation{Kind: strings.ToLower(strings.TrimSpace(r.Kind)), Target: strings.TrimSpace(r.Target)}
		if !proposalRelationKinds[r.Kind] {
			return nil, fmt.Errorf("relation kind %q is not refines, contradicts, duplicates or references: %w", r.Kind, ErrInvalidInput)
		}
		if err := validateSafeID("relation target", r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// subjectKind is the anchor kind a claim subject reads as, or "" when it is prose.
func subjectKind(subject string) string {
	anchors, _ := extractAnchors("", subject)
	for _, a := range anchors {
		if a.Value == subject {
			return a.Kind
		}
	}
	return ""
}

// recordClaims attaches claims to a new entry's served revision.
func recordClaims(ctx context.Context, tx govstore.Tx, entryID string, claims []MemoryClaim, actor govstore.Actor) ([]govstore.Claim, error) {
	out := make([]govstore.Claim, 0, len(claims))
	for _, c := range claims {
		stored, err := tx.AddClaim(ctx, govstore.ClaimInput{EntryID: entryID, Subject: c.Subject,
			SubjectKind: subjectKind(c.Subject), Predicate: c.Predicate, Object: c.Object}, actor)
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	return out, nil
}

// recordRelations records a new entry's edges; a target must be another entry of the
// workspace.
func recordRelations(ctx context.Context, tx govstore.Tx, workspaceID, entryID string, rels []MemoryRelation, actor govstore.Actor) error {
	for _, r := range rels {
		if _, _, err := loadAnyEntryTx(ctx, tx, workspaceID, r.Target); err != nil {
			return err
		}
		if err := tx.AddRelation(ctx, entryID, r.Target, r.Kind, actor); err != nil {
			return err
		}
	}
	return nil
}

// claimRivals are the active entries holding a claim on a subject the proposal claims,
// the T1 candidates beyond its anchor neighbours.
func claimRivals(ctx context.Context, r govstore.Reader, workspaceID string, claims []MemoryClaim) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, c := range claims {
		found, err := r.ListClaims(ctx, govstore.ClaimFilter{WorkspaceID: workspaceID, Subject: c.Subject, ActiveOnly: true,
			Limit: maxConflictSides})
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			if !seen[f.EntryID] {
				seen[f.EntryID] = true
				ids = append(ids, f.EntryID)
			}
		}
	}
	return ids, nil
}

// maxClaimedEntries bounds the claims recall's subject filter reads (the store's cap).
const maxClaimedEntries = 1000

// claimedEntries are the active entries with a claim on subject (and predicate, when
// given), for recall's subject filter.
func claimedEntries(ctx context.Context, r govstore.Reader, workspaceID, subject, predicate string) (map[string]bool, error) {
	claims, err := r.ListClaims(ctx, govstore.ClaimFilter{WorkspaceID: workspaceID, Subject: subject, Predicate: predicate,
		ActiveOnly: true, Limit: maxClaimedEntries})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(claims))
	for _, c := range claims {
		out[c.EntryID] = true
	}
	return out, nil
}
