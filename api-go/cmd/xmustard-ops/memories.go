package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/workspaceops"
)

// The consolidation queue's operator surface (WS-32a, PAR-GOV-13). `memory jobs-plan` shows
// the candidates a pass would derive without queueing them, `memory jobs-run` queues them,
// and `memory jobs-list` reads what the queue holds. Nothing here decides anything about a
// memory: the queue is work for an agent, and claiming and completing a job is the agent's
// through the store's own compare-and-set lease.
//
// These join the memoryCommands table in policy.go as three entries beside tier and tokens,
// so `memory <tier|tokens|jobs-run|jobs-list|jobs-plan>` is one closed set and each command is
// one line, which is the shape the rest of the ops surface uses.

func init() {
	memoryCommands["jobs-plan"] = runMemoryJobsPlan
	memoryCommands["jobs-run"] = runMemoryJobsRun
	memoryCommands["jobs-list"] = runMemoryJobsList
}

// memoryJobsOutput is what every action prints: the pass's own figures and the jobs it left
// queued. Skipped is the work the pass's bound left out, which a later pass takes.
type memoryJobsOutput struct {
	Action   string              `json:"action"`
	Jobs     []govstore.Job      `json:"jobs,omitempty"`
	Prepared []govstore.JobInput `json:"prepared,omitempty"`
	Seen     int                 `json:"seen"`
	Cursor   int64               `json:"cursor"`
	Complete bool                `json:"complete"`
	Skipped  int                 `json:"skipped"`
}

// jobsFlagSet is the flags the three actions share.
func jobsFlagSet(e opsEnv, name string) (*flag.FlagSet, *string, *string, *string) {
	fs := flag.NewFlagSet("xmustard-ops memory "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs,
		fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory"),
		fs.String("source", "entries", "watermark source the pass reflects over"),
		fs.String("principal", "xmustard-ops", "principal recorded as the pass's actor")
}

const jobsUsage = "usage: xmustard-ops memory jobs-plan <workspace_id> [--source NAME] [--data-dir DIR]"

// The other two actions take the same flags; their own usage names them.

// runMemoryJobsPlan derives the candidates and prints them without queueing any, so an
// operator can see the work before it is offered to an agent.
func runMemoryJobsPlan(e opsEnv, workspaceID string, args []string) int {
	fs, dataDir, _, _ := jobsFlagSet(e, "jobs-plan")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage(jobsUsage)
	}
	prepared, err := workspaceops.PrepareConsolidationJobs(context.Background(), *dataDir, workspaceID)
	if err != nil {
		return e.fail(fmt.Errorf("memory jobs plan: %w", err))
	}
	return e.emit(memoryJobsOutput{Action: "plan", Prepared: prepared})
}

// runMemoryJobsRun queues the candidates and advances the source watermark on success.
func runMemoryJobsRun(e opsEnv, workspaceID string, args []string) int {
	fs, dataDir, source, principal := jobsFlagSet(e, "jobs-run")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage(jobsUsage)
	}
	res, err := workspaceops.RunConsolidationPass(context.Background(), *dataDir, workspaceID,
		workspaceops.ConsolidationPassInput{Source: strings.TrimSpace(*source),
			Actor: govstore.Actor{Principal: strings.TrimSpace(*principal), Kind: workspaceops.PrincipalHuman}})
	if err != nil {
		return e.fail(fmt.Errorf("memory jobs run: %w", err))
	}
	return e.emit(memoryJobsOutput{Action: "run", Jobs: res.Jobs, Seen: res.Seen, Cursor: res.Cursor,
		Complete: res.Complete, Skipped: res.Skipped})
}

// runMemoryJobsList reads the queue. It writes nothing and takes no principal.
func runMemoryJobsList(e opsEnv, workspaceID string, args []string) int {
	fs, dataDir, _, _ := jobsFlagSet(e, "jobs-list")
	limit := fs.Int("limit", 100, "how many jobs to list, oldest first")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage(jobsUsage)
	}
	jobs, err := workspaceops.ListConsolidationJobs(context.Background(), *dataDir, workspaceID, *limit)
	if err != nil {
		return e.fail(fmt.Errorf("memory jobs list: %w", err))
	}
	return e.emit(memoryJobsOutput{Action: "list", Jobs: jobs})
}
