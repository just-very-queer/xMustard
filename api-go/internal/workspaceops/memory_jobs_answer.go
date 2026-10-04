package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// The agent side of the consolidation queue (WS-32a, PAR-GOV-13). ground shows the queued
// jobs and remember(job_id=) is how an agent answers one: it claims the job under the store's
// compare-and-set lease, and its answer is a proposal, which still needs a distinct
// principal's verdict before anything is promoted. Nothing in this file decides about a
// memory: the queue derived a question, the agent reads the candidates and reasons, and the
// store's own promotion rules decide.

// jobLease is how long a claim is held before another agent may take the job. It is longer
// than a pass's lease because an agent reads candidates and reasons between claim and answer,
// which is not a store round trip.
const jobLease = 30 * time.Minute

// maxJobDecisionLen bounds the decision an agent records on a job, matching the verify note's
// bound: a decision is a verdict in prose, not a document.
const maxJobDecisionLen = 1000

// JobRequest is remember(job_id=...) with the agent's answer.
type JobRequest struct {
	JobID string
	// Actor is the agent answering the job. It is the author of whatever it proposes.
	Actor ContextActor
	// Decision is what the agent concluded: what it proposes, or that the candidates are
	// genuinely different and nothing should change. It is stored on the job as its result,
	// so the decision is readable afterwards whoever did not make it.
	Decision string
	// Proposal is the remember request the agent's answer amounts to. It is proposed under
	// the agent's own identity and goes through the normal proposal path, so it waits for a
	// distinct verifier exactly as any other proposal does.
	Proposal *RememberRequest
	// Skip proposes nothing and records the decision alone, for an agent that read the job
	// and found the candidates already correct.
	Skip bool
}

// JobOutcome is what remember(job_id=) returns.
type JobOutcome struct {
	JobID string `json:"job_id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	// Proposed is the entry the answer created, when it proposed one.
	Proposed *ContextEntry `json:"proposed,omitempty"`
	// Decision is what the agent concluded, recorded on the job.
	Decision string `json:"decision,omitempty"`
	// RequiresVerification is true for a proposal: the agent's own answer never promotes it.
	RequiresVerification bool `json:"requires_verification"`
}

// AnswerJob claims one consolidation job for the agent and records its answer.
//
// The claim is the store's compare-and-set lease, so two agents answering the same job
// cannot both win: the second finds the lease held and is refused with the job's state. The
// proposal is created before the job is completed, so an answer that cannot be stored leaves
// the job claimed rather than marked done without a result.
func AnswerJob(ctx context.Context, dataDir, workspaceID string, in JobRequest) (*JobOutcome, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	jobID := strings.TrimSpace(in.JobID)
	if jobID == "" {
		return nil, fmt.Errorf("remember(op=job) needs job_id: %w", ErrInvalidInput)
	}
	decision := strings.TrimSpace(in.Decision)
	if decision == "" {
		return nil, fmt.Errorf("remember(op=job) needs decision, what you concluded about the candidates: %w", ErrInvalidInput)
	}
	if len(decision) > maxJobDecisionLen {
		return nil, fmt.Errorf("remember(op=job) decision is %d bytes, at most %d: %w", len(decision), maxJobDecisionLen, ErrInvalidInput)
	}

	var job govstore.Job
	var lease govstore.Lease
	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		j, err := tx.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if j.WorkspaceID != workspaceID {
			return fmt.Errorf("job %s belongs to another workspace: %w", jobID, NotFoundErr("no such job"))
		}
		switch j.State {
		case govstore.JobQueued:
		case govstore.JobLeased:
			return fmt.Errorf("job %s is leased by %s until %s: %w", jobID, j.LeaseOwner, j.LeaseExpiresAt, Conflict("job leased"))
		default:
			return fmt.Errorf("job %s is %s and takes no answer: %w", jobID, j.State, Conflict("job not open"))
		}
		claimed, ok, err := tx.ClaimJob(ctx, govstore.ClaimRequest{WorkspaceID: workspaceID,
			Kinds: []string{j.Kind}, Lease: jobLease}, jobActor(in.Actor))
		if err != nil {
			return err
		}
		if !ok || claimed.ID != j.ID {
			return fmt.Errorf("job %s was taken by another claim: %w", jobID, Conflict("job claimed"))
		}
		job, lease = claimed, govstore.Lease{JobID: claimed.ID, Owner: claimed.LeaseOwner, Version: claimed.LeaseVersion}
		return nil
	}); err != nil {
		return nil, err
	}

	out := &JobOutcome{JobID: job.ID, Kind: job.Kind, Decision: decision, RequiresVerification: !in.Skip}

	// The proposal goes through the ordinary path, so it is attributed to the agent, redacted,
	// and left pending for a distinct verifier.
	if !in.Skip && in.Proposal != nil {
		proposal := *in.Proposal
		// A consolidation answer always waits for a distinct principal. The store would
		// otherwise promote it on the agent's own word in single-agent mode, and the whole
		// point of the queue is that the reasoning is the agent's while the decision to
		// believe it is not (PAR-GOV-02, PAR-GOV-13).
		required := true
		proposal.RequireVerification = &required
		if strings.TrimSpace(proposal.Title) == "" || strings.TrimSpace(proposal.Content) == "" {
			// The job is claimed and the answer is refused: returning the lease would let
			// another agent take a job whose answer never landed.
			return nil, fmt.Errorf("remember(op=job) with a proposal needs title and content: %w", ErrInvalidInput)
		}
		entry, err := Remember(dataDir, workspaceID, proposal, in.Actor)
		if err != nil {
			return nil, err
		}
		out.Proposed = entry
	}

	if err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		_, err := tx.CompleteJob(ctx, lease, map[string]any{
			"decision":     decision,
			"proposed_id":  proposedID(out.Proposed),
			"verification": !in.Skip,
		}, jobActor(in.Actor))
		return err
	}); err != nil {
		return nil, err
	}
	done, err := ReadConsolidationJob(ctx, dataDir, workspaceID, jobID)
	if err != nil {
		return nil, err
	}
	out.State = done.State
	return out, nil
}

// ReadConsolidationJob is one job with its stored result, for an agent checking what a
// previous answer decided.
func ReadConsolidationJob(ctx context.Context, dataDir, workspaceID string, jobID string) (govstore.Job, error) {
	var job govstore.Job
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		j, err := r.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if j.WorkspaceID != workspaceID {
			return fmt.Errorf("job %s belongs to another workspace: %w", jobID, NotFoundErr("no such job"))
		}
		job = j
		return nil
	}); err != nil {
		return govstore.Job{}, err
	}
	return job, nil
}

// JobDecision reads the decision an agent recorded on a completed job. It is what the store
// keeps of an answer that proposed nothing.
func JobDecision(job govstore.Job) (string, bool, error) {
	if len(job.Result) == 0 {
		return "", false, nil
	}
	var res struct {
		Decision     string `json:"decision"`
		Verification bool   `json:"verification"`
	}
	if err := json.Unmarshal(job.Result, &res); err != nil {
		return "", false, fmt.Errorf("job %s result is undecodable: %w", job.ID, err)
	}
	return res.Decision, res.Verification, nil
}

// jobActor is the store actor an answering agent writes under: its own principal, its kind
// from the token, and the session it was in.
func jobActor(a ContextActor) govstore.Actor {
	kind := a.Kind
	if kind == "" {
		kind = PrincipalAgent
	}
	return govstore.Actor{Principal: a.ID, Kind: kind, Owner: a.Owner, SessionID: a.SessionID, CallID: a.CallID}
}

func proposedID(e *ContextEntry) string {
	if e == nil {
		return ""
	}
	return e.ID
}
