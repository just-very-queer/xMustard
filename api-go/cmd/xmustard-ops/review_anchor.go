//go:build review

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/review"
	"xmustard/api-go/internal/workspaceops"
)

// `review anchor` (WS-65), built only with the review build tag: it anchors a batch of
// review findings, from a findings file or a captured evidence original, against the
// change from the merge base of --base to --head and prints the anchors and checks. It
// reads only and stores nothing.

func init() { reviewCommands["anchor"] = runReviewAnchor }

func runReviewAnchor(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops review anchor <workspace_id> --base REF [--head REF] (--findings FILE | --evidence HANDLE [--token-file PATH]) [--data-dir DIR]"
	fs := flag.NewFlagSet("xmustard-ops review anchor", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dataDir := fs.String("data-dir", envOr(e.getenv, "XMUSTARD_DATA_DIR", "../backend/data"), "xMustard data directory")
	base := fs.String("base", "", "the ref the change merges into (required)")
	head := fs.String("head", "HEAD", "the head of the reviewed change")
	file := fs.String("findings", "", "findings JSON: an array, {\"findings\": [...]}, or open-code-review's --format json --output file")
	handle := fs.String("evidence", "", "evidence handle of a captured findings JSON")
	tokenFile := fs.String("token-file", "", "token to read the evidence as, when auth is configured (default: XMUSTARD_API_TOKEN)")
	if fs.Parse(args) != nil || fs.NArg() > 0 || strings.TrimSpace(*base) == "" || (*file == "") == (*handle == "") {
		return e.usage(usage)
	}
	batch, err := e.findingsFrom(*dataDir, workspaceID, *file, *handle, *tokenFile)
	if err != nil {
		return e.fail(err)
	}
	res, err := workspaceops.AnchorReviewFindings(context.Background(), *dataDir, workspaceID, *base, *head, batch)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(res)
}

// findingsFrom decodes the findings file, or the evidence original handle names.
func (e opsEnv) findingsFrom(dataDir, workspaceID, file, handle, tokenFile string) (*review.Batch, error) {
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := review.Read(f)
		if err != nil {
			return nil, err
		}
		b.Source.Kind, b.Source.Ref = "findings_file", file
		return b, nil
	}
	actor, enforced, err := e.evidenceReader(dataDir, workspaceID, tokenFile)
	if err != nil {
		return nil, err
	}
	store := evidence.NewStore(dataDir, evidence.DefaultLimits())
	t, err := store.Tail(context.Background(), evidence.ReadRequest{WorkspaceID: workspaceID, Handle: handle, Actor: actor, AuthEnforced: enforced}, review.MaxFindingsBytes)
	if err != nil {
		return nil, fmt.Errorf("evidence %s: %w", handle, err)
	}
	if t.Offset > 0 {
		return nil, fmt.Errorf("%w: evidence %s holds %d bytes, over %d", review.ErrInvalid, handle, t.TotalBytes, review.MaxFindingsBytes)
	}
	b, err := review.Decode(t.Data)
	if err != nil {
		return nil, err
	}
	b.Source.Kind, b.Source.Ref = "evidence_handle", handle
	return b, nil
}

// evidenceReader is who reads the evidence, as the evidence store checks it. With no
// token store (open mode) that is workspace scope; otherwise a valid token is required
// and the read is that principal's, which the store allows only for originals captured
// by the same principal. Fail closed, in this order.
func (e opsEnv) evidenceReader(dataDir, workspaceID, tokenFile string) (actor string, enforced bool, err error) {
	raw := strings.TrimSpace(e.getenv("XMUSTARD_API_TOKEN"))
	if tokenFile != "" {
		f, err := os.Open(tokenFile)
		if err != nil {
			return "", false, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes))
		if err != nil {
			return "", false, err
		}
		raw = strings.TrimSpace(string(b))
	}
	p, configured := workspaceops.ResolveAuth(dataDir, raw)
	switch {
	case !configured:
		return "", false, nil
	case p == nil:
		return "", false, errors.New("reading evidence needs a valid token when auth is configured (--token-file or XMUSTARD_API_TOKEN)")
	case p.PresenceOnly:
		return "", false, errors.New("a presence-only token is accepted only at the approval prompt")
	case !p.AllowsWorkspace(workspaceID):
		return "", false, fmt.Errorf("the token is not scoped to workspace %s", workspaceID)
	}
	return p.ID, true, nil
}
