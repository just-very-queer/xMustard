package workspaceops

import (
	"context"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/groundbudget"
)

// ground shows the queued jobs, because a queue nothing surfaces is a queue nothing answers:
// the agent has to be able to learn there is work waiting (PAR-GOV-13).
func TestGroundShowsTheConsolidationQueue(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsGroundJobs"
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	if got := runConsolidation(t, dir, ws, "worker-1"); len(got.Jobs) == 0 {
		t.Fatal("the pass queued nothing, so there is nothing for ground to show")
	}

	jobs, total, err := readGroundingConsolidationJobs(dir, ws)
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	if total == nil || *total < 1 {
		t.Fatalf("ground reports no queued jobs: total=%v", total)
	}
	if len(jobs) == 0 {
		t.Fatal("ground shows no job although the queue holds one")
	}
	j := jobs[0]
	if j.JobID == "" || j.Kind == "" || len(j.CandidateIDs) == 0 {
		t.Fatalf("ground's job is not actionable: %+v", j)
	}
	// The instructions travel with the job: the agent is the one who acts on them.
	if !strings.Contains(j.Instructions, "decide") {
		t.Fatalf("ground's job carries no instructions for the agent: %+v", j)
	}
}

// The queue's block is a signal: a job that is never noticed is work never done, so ground
// must not drop the member silently under its budget.
func TestConsolidationSectionIsASignal(t *testing.T) {
	if s := groundbudget.SectionOf("consolidation_jobs"); s != "consolidation" {
		t.Fatalf("consolidation_jobs belongs to section %q, want consolidation", s)
	}
}

// Answering a job proposes, and the proposal still waits for a distinct verifier: the agent's
// own answer never promotes what it concludes (PAR-GOV-02, PAR-GOV-13).
func TestAnsweringAJobProposesAndStillNeedsAVerifier(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsAnswerJob"
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	res := runConsolidation(t, dir, ws, "worker-1")
	if len(res.Jobs) == 0 {
		t.Fatal("the pass queued nothing to answer")
	}
	job := res.Jobs[0]

	out, err := AnswerJob(context.Background(), dir, ws, JobRequest{JobID: job.ID,
		Actor:    ContextActor{ID: "agent-1"},
		Decision: "these two restate each other; keeping the first",
		Proposal: &RememberRequest{ProposeContextRequest: ProposeContextRequest{
			Title: "the parser caches tokens", Content: "the parser caches tokenized results in src/parser.go",
			Permission: "readwrite", Supersedes: job.CandidateIDs}}})
	if err != nil {
		t.Fatalf("answer the job: %v", err)
	}
	if out.Proposed == nil {
		t.Fatal("answering with a proposal created nothing")
	}
	if out.Proposed.Promoted {
		t.Fatalf("the agent's own proposal was promoted: %+v", out.Proposed)
	}
	if !out.RequiresVerification {
		t.Fatal("the outcome does not say the proposal needs a distinct verifier")
	}
	if out.State != govstore.JobDone {
		t.Fatalf("the job is %s after an answer, want %s", out.State, govstore.JobDone)
	}
	// The decision is stored on the job, so what was concluded is readable afterwards.
	done, err := ReadConsolidationJob(context.Background(), dir, ws, job.ID)
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	decision, verification, err := JobDecision(done)
	if err != nil {
		t.Fatalf("read the decision: %v", err)
	}
	if !strings.Contains(decision, "restate each other") {
		t.Fatalf("the stored decision does not name what was concluded: %q", decision)
	}
	if !verification {
		t.Fatal("the stored result does not say the answer needs verification")
	}
}

// An agent that read the job and found the candidates already correct records the decision
// and proposes nothing: answering is not obliged to write memory.
func TestAnsweringAJobCanConcludeWithoutProposing(t *testing.T) {
	dir := singleAgentDir(t)
	ws := "wsAnswerNoop"
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	job := runConsolidation(t, dir, ws, "worker-1").Jobs[0]

	out, err := AnswerJob(context.Background(), dir, ws, JobRequest{JobID: job.ID,
		Actor: ContextActor{ID: "agent-1"}, Decision: "different subjects, nothing to merge", Skip: true})
	if err != nil {
		t.Fatalf("answer without proposing: %v", err)
	}
	if out.Proposed != nil {
		t.Fatalf("an answer with Skip proposed %+v", out.Proposed)
	}
	if out.RequiresVerification {
		t.Fatal("an answer that proposed nothing should not claim it needs verification")
	}
}

// Two agents cannot both answer one job: the claim is the store's compare-and-set lease, so
// the second is refused with the job's state rather than overwriting the first's answer.
func TestTwoAgentsCannotBothAnswerOneJob(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	ws := "wsAnswerRace"
	proposeOn(t, dir, ws, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, ws, "b", "two", "two things", "src/shared.go")
	job := runConsolidation(t, dir, ws, "worker-1").Jobs[0]

	if _, err := AnswerJob(ctx, dir, ws, JobRequest{JobID: job.ID, Actor: ContextActor{ID: "agent-1"},
		Decision: "first answer", Skip: true}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	_, err := AnswerJob(ctx, dir, ws, JobRequest{JobID: job.ID, Actor: ContextActor{ID: "agent-2"},
		Decision: "second answer", Skip: true})
	if err == nil {
		t.Fatal("a second agent answered a job that was already answered")
	}
	if !strings.Contains(err.Error(), "done") {
		t.Fatalf("the refusal does not name the job's state: %v", err)
	}
}

// A job id that names nothing, and one from another workspace, are both refused: the queue is
// a workspace's own and an agent must not answer another one's job.
func TestAnsweringAJobValidatesItsID(t *testing.T) {
	ctx := context.Background()
	dir := singleAgentDir(t)
	if _, err := AnswerJob(ctx, dir, "wsA", JobRequest{Actor: ContextActor{ID: "agent-1"}, Decision: "x", Skip: true}); err == nil {
		t.Fatal("an answer with no job_id was accepted")
	}
	other := singleAgentDir(t)
	wsA, wsB := "wsOne", "wsTwo"
	proposeOn(t, dir, wsA, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, dir, wsA, "b", "two", "two things", "src/shared.go")
	proposeOn(t, other, wsB, "a", "one", "one thing", "src/shared.go")
	proposeOn(t, other, wsB, "b", "two", "two things", "src/shared.go")
	jobA := runConsolidation(t, dir, wsA, "worker-1").Jobs[0]
	if _, err := AnswerJob(ctx, other, wsB, JobRequest{JobID: jobA.ID, Actor: ContextActor{ID: "agent-1"},
		Decision: "x", Skip: true}); err == nil {
		t.Fatal("a job from another workspace was answered")
	}
}
