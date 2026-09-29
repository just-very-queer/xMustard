package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Memory governance commands (WS-31): the write policy (policy show|set), tier changes
// (memory tier) and the core-memory token report (memory tokens). policy set needs an
// admin's human token (an admin who is a human approver: WS-09 roles, WS-57
// AuthorizeHumanApprover); memory tier needs a human approver's token; show and tokens
// read only and take none.

// governanceCommands are the top-level subcommands this file adds to xmustard-ops.
var governanceCommands = map[string]func(opsEnv, []string) int{
	"policy":   func(e opsEnv, args []string) int { return e.dispatch("policy", policyCommands, args) },
	"memory":   func(e opsEnv, args []string) int { return e.dispatch("memory", memoryCommands, args) },
	"guidance": runGuidance,
}

var policyCommands = map[string]func(opsEnv, string, []string) int{
	"show": runPolicyShow,
	"set":  runPolicySet,
}

var memoryCommands = map[string]func(opsEnv, string, []string) int{
	"tier":   runMemoryTier,
	"tokens": runMemoryTokens,
}

// dispatch runs `<group> <subcommand> <workspace_id> [flags]`.
func (e opsEnv) dispatch(group string, commands map[string]func(opsEnv, string, []string) int, args []string) int {
	if len(args) >= 2 {
		if run, ok := commands[args[0]]; ok {
			return run(e, args[1], args[2:])
		}
	}
	return e.usage("usage: xmustard-ops " + group + " <" + strings.Join(slices.Sorted(maps.Keys(commands)), "|") + "> <workspace_id> [flags]")
}

// dataDirFlagSet is a flag set with only --data-dir, for the commands that read.
func (e opsEnv) dataDirFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("xmustard-ops "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs, fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
}

// runPolicyShow prints the workspace's memory policy (the default when it has none).
func runPolicyShow(e opsEnv, workspaceID string, args []string) int {
	fs, dataDir := e.dataDirFlagSet("policy show")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage("usage: xmustard-ops policy show <workspace_id> [--data-dir DIR]")
	}
	p, err := workspaceops.LoadMemoryPolicy(*dataDir, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"workspace_id": workspaceID, "policy": p, "digest": p.Digest()})
}

// maxPolicyFileBytes bounds the policy file read.
const maxPolicyFileBytes = 64 << 10

// runPolicySet replaces the policy with the JSON in --file ("-" reads stdin). The
// token must be an admin's who is a human approver; the change is recorded with its
// approval label.
func runPolicySet(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops policy set <workspace_id> --file PATH|- [--token-file PATH] [--data-dir DIR]"
	fs, af := e.approverFlagSet("policy set")
	file := fs.String("file", "", "the policy JSON (see policy show); - reads stdin")
	if fs.Parse(args) != nil || fs.NArg() > 0 || *file == "" {
		return e.usage(usage)
	}
	p, err := readPolicyFile(*file)
	if err != nil {
		return e.fail(err)
	}
	h, err := e.approver(af, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	change, err := workspaceops.SetMemoryPolicy(*af.dataDir, workspaceID, p, h)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(change)
}

// readPolicyFile decodes a policy, refusing unknown fields and trailing data.
func readPolicyFile(path string) (workspaceops.MemoryPolicy, error) {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return workspaceops.MemoryPolicy{}, err
		}
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(io.LimitReader(r, maxPolicyFileBytes))
	dec.DisallowUnknownFields()
	var p workspaceops.MemoryPolicy
	if err := dec.Decode(&p); err != nil {
		return workspaceops.MemoryPolicy{}, err
	}
	if dec.More() {
		return workspaceops.MemoryPolicy{}, errTrailingPolicy
	}
	return p, nil
}

var errTrailingPolicy = errors.New("the policy file holds more than one JSON value")

// runMemoryTier moves an entry into or out of the core tier as a human approver.
func runMemoryTier(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops memory tier <workspace_id> <entry_id> <core|deferred> --reason TEXT [--token-file PATH] [--data-dir DIR]"
	if len(args) < 2 {
		return e.usage(usage)
	}
	fs, af := e.approverFlagSet("memory tier")
	reason := fs.String("reason", "", "why the tier changes (required)")
	if fs.Parse(args[2:]) != nil || fs.NArg() > 0 || strings.TrimSpace(*reason) == "" {
		return e.usage(usage)
	}
	h, err := e.approver(af, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	entry, err := workspaceops.SetMemoryTier(*af.dataDir, workspaceID, args[0], args[1], *reason, h.TierActor())
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"approver": h.Principal.ID, "approval": h.Label(), "tier": args[1], "entry": entry})
}

// runMemoryTokens prints the core-memory token report: what the standing-memory
// projection pushes at --max-chars and what each core entry costs.
func runMemoryTokens(e opsEnv, workspaceID string, args []string) int {
	fs, dataDir := e.dataDirFlagSet("memory tokens")
	maxChars := fs.Int("max-chars", workspaceops.ProjectionDefaultChars, "projection budget in characters")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage("usage: xmustard-ops memory tokens <workspace_id> [--max-chars N] [--data-dir DIR]")
	}
	report, err := workspaceops.MemoryTokens(context.Background(), *dataDir, workspaceID, *maxChars)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(report)
}
