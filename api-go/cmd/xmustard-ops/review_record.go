//go:build review

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"xmustard/api-go/internal/review"
	"xmustard/api-go/internal/workspaceops"
)

// `review record|show|triage` (WS-66), built only with the review build tag. record
// stores one review of the change from the merge base of --base to --head: its findings,
// anchored against that change (WS-65) and deduplicated within the record's lineage, and
// its coverage over every file the change touches. show prints a record with its
// findings as they stand; triage records a verdict on a finding. record and triage run as
// the token in --token-file or XMUSTARD_API_TOKEN (proposer to record; verifier or
// human-approver to triage), or as the open-mode identity when no credentials are
// configured. A record is evidence for the human's merge decision and never approves a
// change.

func init() {
	reviewCommands["record"] = runReviewRecord
	reviewCommands["show"] = runReviewShow
	reviewCommands["triage"] = runReviewTriage
}

// reviewFlags are the flags record and triage share.
type reviewFlags struct {
	dataDir, tokenFile *string
}

func (e opsEnv) reviewFlagSet(name string) (*flag.FlagSet, reviewFlags) {
	fs := flag.NewFlagSet("xmustard-ops review "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs, reviewFlags{
		dataDir:   fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory"),
		tokenFile: fs.String("token-file", "", "token to act as when auth is configured (default: XMUSTARD_API_TOKEN)"),
	}
}

// reviewActor resolves who the command runs as (fail closed; see workspaceops.ReviewActor).
func (e opsEnv) reviewActor(f reviewFlags, workspaceID string, roles ...string) (workspaceops.ContextActor, error) {
	raw := strings.TrimSpace(e.getenv("XMUSTARD_API_TOKEN"))
	if *f.tokenFile != "" {
		b, err := readBounded(*f.tokenFile, maxTokenFileBytes)
		if err != nil {
			return workspaceops.ContextActor{}, err
		}
		raw = strings.TrimSpace(string(b))
	}
	return workspaceops.ReviewActor(*f.dataDir, workspaceID, raw, roles...)
}

// readBounded reads at most limit bytes of path, refusing a longer file.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is over %d bytes", path, limit)
	}
	return b, err
}

// runReviewRecord records a review. --findings and --evidence may repeat; each input
// holds at most 50 findings, and a record at most 500.
func runReviewRecord(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops review record <workspace_id> --base REF [--head REF] [--findings FILE]... [--evidence HANDLE]... " +
		"[--coverage FILE] [--lineage NAME] [--producer agent|ocr|human] [--token-file PATH] [--data-dir DIR]"
	fs, rf := e.reviewFlagSet("record")
	base := fs.String("base", "", "the ref the change merges into (required)")
	head := fs.String("head", "HEAD", "the head of the reviewed change")
	coverage := fs.String("coverage", "", "coverage JSON: [{path, status: reviewed|not_reviewed, reason}]")
	lineage := fs.String("lineage", "", "the change's lineage (default: <base>@<merge base>:<head branch>; "+
		"required when --head is not a branch)")
	producer := fs.String("producer", "agent", "who produced the findings: agent, ocr or human (human needs a token of kind human)")
	var files, handles stringSliceFlag
	fs.Var(&files, "findings", "findings JSON: an array, {\"findings\": [...]} or open-code-review's --format json output; may repeat")
	fs.Var(&handles, "evidence", "evidence handle of a captured findings JSON; may repeat")
	if fs.Parse(args) != nil || fs.NArg() > 0 || strings.TrimSpace(*base) == "" {
		return e.usage(usage)
	}
	actor, err := e.reviewActor(rf, workspaceID, workspaceops.RoleProposer)
	if err != nil {
		return e.fail(err)
	}
	sub := workspaceops.ReviewSubmission{BaseRef: *base, HeadRef: *head, Lineage: *lineage, Producer: *producer}
	// findingsFrom reads a findings file, or an evidence handle as the token's principal.
	inputs := make([][2]string, 0, len(files)+len(handles))
	for _, f := range files {
		inputs = append(inputs, [2]string{f, ""})
	}
	for _, h := range handles {
		inputs = append(inputs, [2]string{"", h})
	}
	for _, in := range inputs {
		b, err := e.findingsFrom(*rf.dataDir, workspaceID, in[0], in[1], *rf.tokenFile)
		if err != nil {
			return e.fail(fmt.Errorf("%s: %w", in[0]+in[1], err))
		}
		sub.Batches = append(sub.Batches, b)
	}
	if *coverage != "" {
		data, err := readBounded(*coverage, review.MaxFindingsBytes)
		if err != nil {
			return e.fail(err)
		}
		if sub.Coverage, err = review.DecodeCoverage(data); err != nil {
			return e.fail(fmt.Errorf("%s: %w", *coverage, err))
		}
	}
	res, err := workspaceops.RecordReview(context.Background(), *rf.dataDir, workspaceID, actor, sub)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(res)
}

// runReviewShow prints a record with its findings. It reads only and takes no token.
func runReviewShow(e opsEnv, workspaceID string, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops review show", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dataDir := fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	record := fs.String("record", "", "the review record id (required)")
	if fs.Parse(args) != nil || fs.NArg() > 0 || *record == "" {
		return e.usage("usage: xmustard-ops review show <workspace_id> --record ID [--data-dir DIR]")
	}
	res, err := workspaceops.ShowReviewRecord(context.Background(), *dataDir, workspaceID, *record)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(res)
}

// runReviewTriage records a verdict on a finding.
func runReviewTriage(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops review triage <workspace_id> --finding ID --verdict confirm|dismiss|fixed|wont_fix " +
		"[--note TEXT] [--token-file PATH] [--data-dir DIR]"
	fs, rf := e.reviewFlagSet("triage")
	finding := fs.String("finding", "", "the finding id (required)")
	verdict := fs.String("verdict", "", "confirm, dismiss, fixed or wont_fix (required)")
	note := fs.String("note", "", "note stored with the verdict")
	if fs.Parse(args) != nil || fs.NArg() > 0 || *finding == "" || *verdict == "" {
		return e.usage(usage)
	}
	actor, err := e.reviewActor(rf, workspaceID, workspaceops.RoleVerifier, workspaceops.RoleHumanApprover)
	if err != nil {
		return e.fail(err)
	}
	f, err := workspaceops.TriageReviewFinding(context.Background(), *rf.dataDir, workspaceID, actor, *finding, *verdict, *note)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"finding": f, "label": workspaceops.ReviewRecordLabel})
}
