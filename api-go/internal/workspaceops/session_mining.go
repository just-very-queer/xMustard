package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// Candidate mining (WS-33; PAR-GOV-14): at a turn's end (Stop) and at SessionEnd the
// ledger looks for what a session completed, with deterministic templates, and proposes
// each finding once as a kind=candidate memory. A candidate is always pending: it asks
// for peer verification, so neither single-agent nor open mode promotes it on the
// miner's word, and it is never pushed into a session. Only completed changes are mined
// (a failure followed by edits and a passing run; a command that passed repeatedly),
// never an attempt. The phrasing is the template's; an agent or a helper rewrites it
// when it verifies or edits the candidate.

// minedTagPrefix marks a mined candidate with its signature, so a finding is proposed
// once whatever happens to the candidate afterwards (a rejected one is not re-proposed).
const minedTagPrefix = "mined-"

// maxMinedPerPass bounds the candidates one pass proposes.
const maxMinedPerPass = 3

// minedCandidate is one finding.
type minedCandidate struct {
	sig            string
	title, content string
	paths, handles []string
}

// candidateMiners are the templates, in order.
var candidateMiners = []func(key string, evs []govstore.SessionEvent, now time.Time) []minedCandidate{
	mineFixes,
	mineWorkflows,
}

// minedFix is a call that failed, then passed after the session edited files.
type minedFix struct {
	tool, command, failure string
	paths, handles         []string
}

func (f minedFix) fix() string { return "passed after editing " + strings.Join(f.paths, ", ") }

// callOf identifies a call across runs: its tool and argument digest (for a shell call,
// the command's).
func callOf(ev govstore.SessionEvent) string { return ev.Tool + "\x00" + ev.ArgsDigest }

// fixes finds each failure followed by file edits and a passing run of the same call. A
// failure again after the edits starts over: those edits did not fix it.
func fixes(evs []govstore.SessionEvent) []minedFix {
	type open struct {
		fail   govstore.SessionEvent
		edited []string
	}
	pending := map[string]*open{}
	var order []string
	var out []minedFix
	for _, ev := range evs {
		switch {
		case ev.Kind == LedgerFileModified && ev.Path != "":
			for _, call := range order {
				if o := pending[call]; o != nil && !slices.Contains(o.edited, ev.Path) && len(o.edited) < 8 {
					o.edited = append(o.edited, ev.Path)
				}
			}
		case ev.Kind == LedgerToolCall && ev.Status == LedgerError:
			if _, ok := pending[callOf(ev)]; !ok {
				order = append(order, callOf(ev))
			}
			pending[callOf(ev)] = &open{fail: ev}
		case ev.Kind == LedgerToolCall && ev.Status == LedgerOK:
			if o := pending[callOf(ev)]; o != nil && len(o.edited) > 0 {
				f := rowText(o.fail)
				out = append(out, minedFix{tool: ev.Tool, command: rowText(ev).Text, failure: fallbackString(f.Signature, f.Text),
					paths: o.edited, handles: nonEmpty(o.fail.Handle, ev.Handle)})
			}
			delete(pending, callOf(ev))
		}
	}
	return out
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// mineFixes: "`<command>` failed (<signature>); after editing <files> it passed".
func mineFixes(_ string, evs []govstore.SessionEvent, _ time.Time) []minedCandidate {
	var out []minedCandidate
	for _, f := range fixes(evs) {
		what := fallbackString(f.command, f.tool)
		paths := slices.Clone(f.paths)
		slices.Sort(paths)
		out = append(out, minedCandidate{
			sig:   "fix\x00" + what + "\x00" + f.failure + "\x00" + strings.Join(paths, ","),
			title: cutBytes("Fix: `"+what+"` failure", snapTitleMax),
			content: fmt.Sprintf("`%s` failed (%s); after editing %s it passed.", what,
				fallbackString(f.failure, "non-zero exit"), strings.Join(paths, ", ")),
			paths: paths, handles: f.handles,
		})
	}
	return out
}

// workflowRunners are the command prefixes of build, test and lint workflows.
var workflowRunners = []string{"go test", "go build", "go vet", "cargo test", "cargo build", "cargo clippy", "cargo check",
	"npm test", "npm run", "pnpm test", "pnpm run", "yarn test", "yarn run", "bun test", "node --test", "deno test",
	"make", "just", "pytest", "python -m pytest", "tox", "gradle", "./gradlew", "mvn", "ruff", "golangci-lint"}

// workflowRepeats is how often a command must pass in one session to be a workflow.
const workflowRepeats = 2

// mineWorkflows: a build, test or lint command that passed repeatedly in the session
// ("new workflow command observed").
func mineWorkflows(key string, evs []govstore.SessionEvent, _ time.Time) []minedCandidate {
	passed := map[string]int{}
	var order []string
	for _, ev := range evs {
		cmd := rowText(ev).Text
		if ev.Kind != LedgerToolCall || ev.Status != LedgerOK || !isWorkflowCommand(cmd) {
			continue
		}
		if passed[cmd]++; passed[cmd] == workflowRepeats {
			order = append(order, cmd)
		}
	}
	out := make([]minedCandidate, 0, len(order))
	for _, cmd := range order {
		out = append(out, minedCandidate{sig: "workflow\x00" + cmd, title: cutBytes("Workflow: `"+cmd+"`", snapTitleMax),
			content: fmt.Sprintf("`%s` is a workflow command of this repository: it passed %d times in session %s.", cmd, passed[cmd], key)})
	}
	return out
}

func isWorkflowCommand(cmd string) bool {
	return slices.ContainsFunc(workflowRunners, func(p string) bool { return cmd == p || strings.HasPrefix(cmd, p+" ") })
}

// minedTag is a finding's tag.
func minedTag(sig string) string {
	sum := sha256.Sum256([]byte(sig))
	return minedTagPrefix + hex.EncodeToString(sum[:6])
}

// mineSession runs the templates over a session's ledger and proposes each new finding
// as a pending candidate authored by the session's principal, recording the proposal in
// the ledger. It returns how many it proposed.
func mineSession(ctx context.Context, dataDir string, ev LedgerEvent) (int, error) {
	workspaceID, key, principal := ev.WorkspaceID, ev.key(), ev.Principal
	var found []minedCandidate
	seen := map[string]bool{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		_, evs, err := sessionTail(ctx, r, workspaceID, key)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, miner := range candidateMiners {
			found = append(found, miner(key, evs, now)...)
		}
		if len(found) == 0 {
			return nil
		}
		return minedTags(ctx, r, workspaceID, seen)
	})
	if err != nil {
		return 0, err
	}
	proposed := 0
	for _, c := range found {
		tag := minedTag(c.sig)
		if seen[tag] || proposed == maxMinedPerPass {
			continue
		}
		seen[tag] = true
		e, err := proposeCandidate(dataDir, workspaceID, key, principal, c, tag)
		if err != nil {
			ledgerStats.failed.Add(1)
			continue
		}
		proposed++
		row := ev
		row.Kind, row.Tool, row.EntryID, row.Status, row.Text = LedgerMemoryProposed, "xmustard-miner", e.ID, "", ""
		RecordSession(dataDir, row)
	}
	return proposed, nil
}

// minedTags collects the tags of the workspace's candidates in every state.
func minedTags(ctx context.Context, r govstore.Reader, workspaceID string, into map[string]bool) error {
	f := govstore.EntryFilter{WorkspaceID: workspaceID, Kind: "candidate", Lifecycles: []string{"*"}, Limit: storeListPage}
	for {
		page, err := r.ListEntries(ctx, f)
		if err != nil {
			return err
		}
		for _, e := range page {
			for _, t := range e.Tags {
				if strings.HasPrefix(t, minedTagPrefix) {
					into[t] = true
				}
			}
		}
		if len(page) < storeListPage {
			return nil
		}
		f.AfterCursor = page[len(page)-1].Cursor
	}
}

// proposeCandidate proposes one finding through remember, as the session's principal,
// asking for peer verification so that it stays pending. Evidence handles that can no
// longer be cited (expired, revoked) are dropped rather than failing the proposal.
func proposeCandidate(dataDir, workspaceID, key, principal string, c minedCandidate, tag string) (*ContextEntry, error) {
	peers := true
	actor := ContextActor{ID: ledgerPrincipal(principal), OpenMode: strings.TrimSpace(principal) == "", Kind: "agent", SessionID: key}
	req := RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: c.title, Content: c.content,
		Permission: "readwrite", Paths: c.paths, RequireVerification: &peers, Kind: "candidate",
		Tags: []string{"mined", tag}}, Evidence: c.handles}
	e, err := Remember(dataDir, workspaceID, req, actor)
	if err != nil && len(req.Evidence) > 0 {
		req.Evidence = nil
		e, err = Remember(dataDir, workspaceID, req, actor)
	}
	return e, err
}
