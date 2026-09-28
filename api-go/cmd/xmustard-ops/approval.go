package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// The human-approval surface (WS-57): a human approver votes on pending memory
// (approve, reject), lists what awaits them (queue), and attests, revokes and checks a
// merge review (review approve|revoke|gate). Every command but review gate needs a
// human approver's token: a principal of kind human with the human-approver role,
// scoped to the workspace. The token is read from --token-file, then from
// XMUSTARD_APPROVER_TOKEN, and otherwise typed at the controlling terminal with echo
// off. The assurance comes from the token (workspaceops.AuthorizeHumanApprover): a
// presence-only token is accepted only when typed and records user_presence; any other
// token records advisory wherever it is read, since the same token also works from
// places agent processes of this user can read. Nothing here merges, changes branch
// protection or posts anywhere.

// opsEnv is what the approval commands read and write, so tests can drive them.
type opsEnv struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	// prompt reads a secret typed at the controlling terminal.
	prompt func(label string) (string, error)
}

func defaultOpsEnv() opsEnv {
	return opsEnv{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, prompt: readTerminalSecret}
}

// approvalCommands are the subcommands this file adds to xmustard-ops.
var approvalCommands = map[string]func(opsEnv, []string) int{
	"approve": func(e opsEnv, args []string) int { return runHumanVerdict(e, workspaceops.OutcomeApprove, args) },
	"reject":  func(e opsEnv, args []string) int { return runHumanVerdict(e, workspaceops.OutcomeReject, args) },
	"queue":   runApprovalQueue,
	"review":  runReview,
}

// Exit codes: 0 done (or a current approval), 1 an error, 2 a usage error, and the
// gate's 3 (no approval) and 4 (a stale one).
const (
	exitError = 1
	exitUsage = 2
)

var gateExit = map[string]int{
	workspaceops.MergeApprovalCurrent: 0,
	workspaceops.MergeApprovalNone:    3,
	workspaceops.MergeApprovalStale:   4,
}

const maxTokenFileBytes = 4 << 10

// approverFlags are the flags every token-bound command takes.
type approverFlags struct {
	dataDir, tokenFile *string
}

func (e opsEnv) approverFlagSet(name string) (*flag.FlagSet, approverFlags) {
	fs := flag.NewFlagSet("xmustard-ops "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs, approverFlags{
		dataDir:   fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory"),
		tokenFile: fs.String("token-file", "", "file holding the human approver's token (advisory: agent processes can read it; a presence-only token is refused)"),
	}
}

// approver authenticates the human approver for workspaceID (fail closed; see
// workspaceops.AuthorizeHumanApprover).
func (e opsEnv) approver(f approverFlags, workspaceID string) (workspaceops.HumanApprover, error) {
	raw, source, err := e.approverToken(*f.tokenFile)
	if err != nil {
		return workspaceops.HumanApprover{}, err
	}
	return workspaceops.AuthorizeHumanApprover(*f.dataDir, workspaceID, raw, source)
}

// approverToken reads the token and where it came from: a file, the environment, or
// typed at the terminal.
func (e opsEnv) approverToken(tokenFile string) (raw, source string, err error) {
	switch {
	case tokenFile != "":
		f, err := os.Open(tokenFile)
		if err != nil {
			return "", "", err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes))
		return strings.TrimSpace(string(b)), workspaceops.TokenFile, err
	case strings.TrimSpace(e.getenv("XMUSTARD_APPROVER_TOKEN")) != "":
		return strings.TrimSpace(e.getenv("XMUSTARD_APPROVER_TOKEN")), workspaceops.TokenEnviron, nil
	}
	raw, err = e.prompt("xmustard human approver token: ")
	return strings.TrimSpace(raw), workspaceops.TokenTyped, err
}

// runHumanVerdict is `approve|reject <workspace_id> <entry_id> [--revision N] [--note TEXT]`.
func runHumanVerdict(e opsEnv, verb string, args []string) int {
	usage := "usage: xmustard-ops " + verb + " <workspace_id> <entry_id> [--revision N] [--note TEXT] [--token-file PATH] [--data-dir DIR]"
	if len(args) < 2 {
		return e.usage(usage)
	}
	fs, af := e.approverFlagSet(verb)
	revision := fs.Int64("revision", 0, "pending edit revision to vote on (default: the served revision)")
	note := fs.String("note", "", "reason stored with the verdict")
	if fs.Parse(args[2:]) != nil || fs.NArg() > 0 {
		return e.usage(usage)
	}
	h, err := e.approver(af, args[0])
	if err != nil {
		return e.fail(err)
	}
	entry, err := workspaceops.HumanVerdict(*af.dataDir, args[0], args[1], h, verb, *revision, *note)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"approver": h.Principal.ID, "approval": h.Label(), "verdict": verb, "entry": entry})
}

// runApprovalQueue is `queue <workspace_id> [--limit N] [--baselines N]`.
func runApprovalQueue(e opsEnv, args []string) int {
	usage := "usage: xmustard-ops queue <workspace_id> [--limit N] [--baselines N] [--token-file PATH] [--data-dir DIR]"
	if len(args) < 1 {
		return e.usage(usage)
	}
	fs, af := e.approverFlagSet("queue")
	limit := fs.Int("limit", 50, "most memory writes listed")
	baselines := fs.Int("baselines", 10, "most recent index baseline builds listed")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		return e.usage(usage)
	}
	h, err := e.approver(af, args[0])
	if err != nil {
		return e.fail(err)
	}
	q, err := workspaceops.HumanApprovalQueue(*af.dataDir, args[0], h, *limit, *baselines)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(q)
}

// reviewCommands are the `review` subcommands. The review build tag adds anchor
// (review_anchor.go).
var reviewCommands = map[string]func(opsEnv, string, []string) int{
	"approve": runReviewApprove,
	"revoke":  runReviewRevoke,
	"gate":    runReviewGate,
}

// runReview is `review <subcommand> <workspace_id> [flags]`.
func runReview(e opsEnv, args []string) int {
	if len(args) >= 2 {
		if run, ok := reviewCommands[args[0]]; ok {
			return run(e, args[1], args[2:])
		}
	}
	return e.usage("usage: xmustard-ops review <" + strings.Join(slices.Sorted(maps.Keys(reviewCommands)), "|") + "> <workspace_id> [flags]")
}

// runReviewApprove attests the change from the merge base of --base to --head.
func runReviewApprove(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops review approve <workspace_id> --base REF [--head REF] [--review ID]... [--note TEXT] [--token-file PATH] [--data-dir DIR]"
	fs, af := e.approverFlagSet("review approve")
	base := fs.String("base", "", "the ref the change merges into (required)")
	head := fs.String("head", "HEAD", "the head of the reviewed change")
	note := fs.String("note", "", "note stored with the attestation")
	var reviews stringSliceFlag
	fs.Var(&reviews, "review", "review record id the approval rests on; may be repeated")
	if fs.Parse(args) != nil || fs.NArg() > 0 || strings.TrimSpace(*base) == "" {
		return e.usage(usage)
	}
	h, err := e.approver(af, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	a, err := workspaceops.ApproveMerge(context.Background(), *af.dataDir, workspaceID, h, *base, *head, []string(reviews), *note)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(a)
}

// runReviewRevoke withdraws the attestation recorded at --approval.
func runReviewRevoke(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops review revoke <workspace_id> --approval SEQ --reason TEXT [--token-file PATH] [--data-dir DIR]"
	fs, af := e.approverFlagSet("review revoke")
	seq := fs.Int64("approval", 0, "seq of the attestation to revoke (required)")
	reason := fs.String("reason", "", "why it is revoked (required)")
	if fs.Parse(args) != nil || fs.NArg() > 0 || *seq < 1 || strings.TrimSpace(*reason) == "" {
		return e.usage(usage)
	}
	h, err := e.approver(af, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	a, err := workspaceops.RevokeMergeApproval(context.Background(), *af.dataDir, workspaceID, h, *seq, *reason)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(a)
}

// runReviewGate prints the approval state of merging --head into --base and exits 0
// only when a current attestation binds that change (3: none, 4: stale). --base is
// required: an attestation made against another base covers another diff. It reads
// only, so it takes no token.
func runReviewGate(e opsEnv, workspaceID string, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops review gate", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dataDir := fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	base := fs.String("base", "", "the ref the head merges into, such as the remote branch a push updates (required)")
	head := fs.String("head", "HEAD", "the head to check")
	if fs.Parse(args) != nil || fs.NArg() > 0 || strings.TrimSpace(*base) == "" {
		return e.usage("usage: xmustard-ops review gate <workspace_id> --base REF [--head REF] [--data-dir DIR]")
	}
	st, err := workspaceops.MergeApprovalState(context.Background(), *dataDir, workspaceID, *base, *head)
	if err != nil {
		return e.fail(err)
	}
	code, known := gateExit[st.Status]
	if !known { // fail closed on a state this build does not know
		return e.fail(fmt.Errorf("unknown merge approval state %q", st.Status))
	}
	if failed := e.emit(st); failed != 0 {
		return failed
	}
	return code
}

func (e opsEnv) emit(payload any) int {
	b, err := json.Marshal(payload)
	if err != nil {
		return e.fail(err)
	}
	_, _ = e.stdout.Write(append(b, '\n'))
	return 0
}

func (e opsEnv) fail(err error) int {
	fmt.Fprintln(e.stderr, err)
	return exitError
}

func (e opsEnv) usage(msg string) int {
	fmt.Fprintln(e.stderr, msg)
	return exitUsage
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}
