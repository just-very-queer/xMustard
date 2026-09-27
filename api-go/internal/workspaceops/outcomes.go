package workspaceops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
	"xmustard/api-go/internal/rustcore"
)

// Run-independent failure outcomes (WS-21; PAR-HAR-06, PAR-RET-11, PAR-RT-11).
//
// why_failed works without platform runs. It runs a test, build or lint command from a
// closed table through the bounded Rust runner (outcome_commands.go: argv exec, no
// shell, the working directory and path arguments confined to the workspace root, no
// daemon secrets in its environment, one at a time, a timeout that terminates the whole
// process group), reads the last MiB of a retained evidence original, or takes a pasted
// log. The output is redacted, analyzed from its bounded tail and recorded once as a
// govstore run outcome, and ground lists the open failures. Reading an outcome never
// writes: run_fail feedback is recorded once, when an outcome is first recorded, for the
// changed files it implicates. A captured failing test, build or lint output becomes an
// outcome the same way (RecordCapturedOutcome), without a Rust or git spawn on the
// capture path. Revoking an evidence original removes the outcomes made from it.

const (
	// outcomeTailBytes is how much of an output is analyzed: its end, where runners and
	// compilers print failures and summaries.
	outcomeTailBytes = 1 << 20
	// outcomeStoredTail is the redacted excerpt an outcome keeps for display.
	outcomeStoredTail = 4 << 10
	// recentOutcomeWindow is how long an open failure stays in ground's
	// recent_failed_runs; a later outcome of its command resolves it sooner.
	recentOutcomeWindow = 24 * time.Hour
	// recentOutcomeLimit bounds the run outcomes ground lists; a longer list is
	// reported in ground's unknown, never cut silently.
	recentOutcomeLimit = 50

	// DefaultWhyFailedTimeout and MaxWhyFailedTimeout bound a why_failed command, in
	// seconds. The MCP argument stops below the clients' 60 s call timeout; the HTTP
	// route stays below the server's write timeout.
	DefaultWhyFailedTimeout = 45
	MaxWhyFailedTimeout     = 240

	maxWhyFailedArgs     = 256
	maxWhyFailedArgv     = 16 << 10
	maxMentionedPaths    = 128
	maxPathMatches       = 8192
	maxMentionedPathLen  = 512
	maxOutcomeErrorLines = 8
	maxOutcomeMemories   = 8
	maxMemoryLookupPaths = 32
)

// EvidenceTailFunc reads at most maxBytes from the end of a retained evidence original,
// authorized as the caller (evidence.Store.Tail).
type EvidenceTailFunc func(ctx context.Context, handle string, maxBytes int64) (*evidence.Tail, error)

// FailureRequest is one why_failed call without a platform run: exactly one of a
// command (Argv, or Command split like a shell would split words, without expanding
// anything), EvidenceHandle and Log is set.
type FailureRequest struct {
	Command        string
	Argv           []string
	Cwd            string // repo-relative; "" is the workspace root
	TimeoutSeconds int    // 0 means DefaultWhyFailedTimeout
	EvidenceHandle string
	Log            string
	// Evidence reads an evidence tail as the caller; required with EvidenceHandle.
	Evidence EvidenceTailFunc
	Actor    ContextActor
}

// OutcomeOutput describes the output an explanation was made from.
type OutcomeOutput struct {
	TotalBytes    int64 `json:"total_bytes"`
	AnalyzedBytes int64 `json:"analyzed_bytes"`
	// Truncated is true when only the tail was analyzed.
	Truncated bool   `json:"truncated"`
	Redacted  int    `json:"redacted,omitempty"`
	Tail      string `json:"tail,omitempty"`
}

// ImplicatedMemory is a promoted memory anchored to a file the failure names.
type ImplicatedMemory struct {
	EntryID string `json:"entry_id"`
	Path    string `json:"path"`
	Stale   bool   `json:"stale,omitempty"`
}

// outcomeAnalysis is what an outcome stores about its output (run_outcomes.analysis).
type outcomeAnalysis struct {
	Signals        []string `json:"signals"`
	ErrorLines     []string `json:"error_lines"`
	MentionedPaths []string `json:"mentioned_paths,omitempty"`
	FailingTests   []string `json:"failing_tests,omitempty"`
	Redacted       int      `json:"redacted,omitempty"`
	TimedOut       bool     `json:"timed_out,omitempty"`
}

// collectedOutput is what to record about one source's redacted, analyzed output.
type collectedOutput struct {
	in       govstore.RunOutcomeInput
	analysis outcomeAnalysis
}

// RecordFailureOutcome runs or reads req's source, records its outcome and explains
// it. A log or evidence original already recorded returns its first outcome
// (Created false) and feeds nothing back again.
func RecordFailureOutcome(ctx context.Context, dataDir, workspaceID string, req FailureRequest) (*FailureExplanation, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := collectFailureOutput(ctx, root, req)
	if err != nil {
		return nil, err
	}
	out.in.WorkspaceID = workspaceID
	rec, created, err := storeRunOutcome(ctx, dataDir, workspaceID, out, req.Actor.storeActor(root))
	if err != nil {
		return nil, err
	}
	changed, changedErr := workingChangedFiles(ctx, dataDir, workspaceID)
	exp := explainOutcome(ctx, dataDir, workspaceID, rec, changed, changedErr)
	exp.Created = &created
	// Feedback is an outcome event: recorded once per outcome, for the paths it
	// implicates, never on a read (PAR-RET-11).
	if created && rec.Failed && len(exp.ImplicatedPaths) > 0 {
		if err := RecordFeedback(dataDir, workspaceID, "run_fail", exp.ImplicatedPaths); err != nil {
			log.Printf("feedback: run_fail for workspace %s outcome %s failed: %v", workspaceID, rec.ID, err)
		}
	}
	return exp, nil
}

// ExplainRunOutcome explains a recorded outcome. It only reads.
func ExplainRunOutcome(ctx context.Context, dataDir, workspaceID, outcomeID string) (*FailureExplanation, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("run", outcomeID); err != nil {
		return nil, err
	}
	var rec govstore.RunOutcome
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		rec, err = r.GetRunOutcome(ctx, workspaceID, outcomeID)
		return err
	}); err != nil {
		return nil, err
	}
	changed, changedErr := workingChangedFiles(ctx, dataDir, workspaceID)
	return explainOutcome(ctx, dataDir, workspaceID, rec, changed, changedErr), nil
}

// IsRunOutcomeID reports whether id names a run outcome rather than a platform run.
func IsRunOutcomeID(id string) bool { return strings.HasPrefix(id, govstore.RunOutcomePrefix) }

// RunOutcomeSummary is one row of the outcome list: the outcome without its analysis.
type RunOutcomeSummary struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Status      string `json:"status"`
	Failed      bool   `json:"failed"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Command     string `json:"command,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Principal   string `json:"principal"`
	HeadSHA     string `json:"head_sha,omitempty"`
	RecordedAt  string `json:"recorded_at"`
	ResolvedBy  string `json:"resolved_by,omitempty"`
	ResolvedAt  string `json:"resolved_at,omitempty"`
	OutputBytes int64  `json:"output_bytes"`
}

// ListRunOutcomes lists a workspace's recorded outcomes, newest first; open selects the
// failures no later outcome of their command resolved.
func ListRunOutcomes(ctx context.Context, dataDir, workspaceID string, open bool, limit int) ([]RunOutcomeSummary, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	var recs []govstore.RunOutcome
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		recs, err = r.ListRunOutcomes(ctx, govstore.RunOutcomeFilter{WorkspaceID: workspaceID, Open: open, Limit: limit})
		return err
	}); err != nil {
		return nil, err
	}
	out := make([]RunOutcomeSummary, 0, len(recs))
	for _, o := range recs {
		out = append(out, RunOutcomeSummary{ID: o.ID, Source: o.Source, Status: o.Status, Failed: o.Failed,
			ExitCode: o.ExitCode, Command: o.Command, Tool: o.Tool, Principal: o.Principal, HeadSHA: o.HeadSHA,
			RecordedAt: o.CreatedAt, ResolvedBy: o.ResolvedBy, ResolvedAt: o.ResolvedAt, OutputBytes: o.OutputBytes})
	}
	return out, nil
}

// DeleteRunOutcome removes one recorded outcome: the admin's answer to a secret the
// redactor missed in an outcome's command or tail.
func DeleteRunOutcome(ctx context.Context, dataDir, workspaceID, outcomeID string) error {
	if err := validateSafeID("run", outcomeID); err != nil || !IsRunOutcomeID(outcomeID) {
		return Invalid("not a run outcome id")
	}
	return memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		return tx.DeleteRunOutcome(ctx, workspaceID, outcomeID)
	})
}

// ForgetEvidenceOutcomes removes the outcomes made from the evidence original handle,
// or, with handle "", from any original of the workspace. Revoking or purging an
// original calls it, so the tail, error lines and failing tests an outcome copied from
// the original go with it.
func ForgetEvidenceOutcomes(ctx context.Context, dataDir, workspaceID, handle string) (int, error) {
	var n int
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		var err error
		n, err = tx.DeleteEvidenceOutcomes(ctx, workspaceID, handle)
		return err
	})
	return n, err
}

// --- sources -------------------------------------------------------------------------

func collectFailureOutput(ctx context.Context, root string, req FailureRequest) (*collectedOutput, error) {
	if req.Command != "" {
		if len(req.Argv) > 0 {
			return nil, Invalid("pass command or argv, not both")
		}
		argv, err := splitShellArgs(req.Command)
		if err != nil {
			return nil, Invalid("command: " + err.Error())
		}
		if len(argv) == 0 {
			return nil, Invalid("command has no program")
		}
		req.Argv = argv
	}
	sources := 0
	for _, set := range []bool{len(req.Argv) > 0, req.EvidenceHandle != "", req.Log != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return nil, Invalid("why_failed needs exactly one of command, evidence_handle or log (a run_id is read with GET)")
	}
	switch {
	case len(req.Argv) > 0:
		return runFailureCommand(ctx, root, req)
	case req.EvidenceHandle != "":
		return readEvidenceFailure(ctx, req)
	}
	if err := admitAnalysis(ctx, int64(len(req.Log))); err != nil {
		return nil, err
	}
	return pastedLogOutput(req.Log), nil
}

// whyFailedFamilies are the command families whose captured outputs become outcomes.
var whyFailedFamilies = map[evidence.Family]bool{evidence.FamilyTest: true, evidence.FamilyBuild: true, evidence.FamilyLint: true}

// CapturesOutcome reports whether a captured output of family can become an outcome.
func CapturesOutcome(family evidence.Family) bool { return whyFailedFamilies[family] }

// runFailureCommand checks the command (outcome_commands.go), runs it through the
// bounded runner when no other why_failed command is running, and analyzes its output.
func runFailureCommand(ctx context.Context, root string, req FailureRequest) (*collectedOutput, error) {
	cmd, err := prepareCommand(root, req)
	if err != nil {
		return nil, err
	}
	release, err := acquireCommandSlot()
	if err != nil {
		return nil, err
	}
	res, err := cmd.run(ctx)
	release()
	if err != nil {
		return nil, err
	}
	raw := combineCommandOutput(res.StdoutExcerpt, res.StderrExcerpt)
	if err := admitAnalysis(ctx, int64(len(raw))); err != nil {
		return nil, err
	}
	nonce, err := outcomeNonce()
	if err != nil {
		return nil, err
	}
	status := commandStatus(res)
	out := analyzedOutput(raw, streamBytes(res.StdoutExcerpt)+streamBytes(res.StderrExcerpt))
	out.analysis.TimedOut = res.TimedOut
	out.analysis.Signals = append(statusSignals(status, res.ExitCode, cmd.timeout), out.analysis.Signals...)
	out.in.Source, out.in.SourceKey = govstore.RunSourceCommand, "command:"+nonce
	out.in.SubjectKey = commandSubject(cmd.rel, req.Argv)
	out.in.Status, out.in.ExitCode = status, res.ExitCode
	out.in.Command, out.in.Cwd = redactedLine(strings.Join(req.Argv, " ")), cmd.rel
	return out, nil
}

// droppedMarker is the bounded runner's note in an excerpt whose middle it dropped
// (rust-core verification.rs BoundedCapture): it names the stream's true size.
var droppedMarker = regexp.MustCompile(`\.\.\.\[dropped \d+ bytes of (\d+) total\]`)

// streamBytes is the size of the stream an excerpt came from.
func streamBytes(excerpt string) int64 {
	if m := droppedMarker.FindStringSubmatch(excerpt); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return n
		}
	}
	return int64(len(excerpt))
}

// commandStatus maps the runner's result onto the outcome statuses.
func commandStatus(res *rustcore.ManagedCommandResult) string {
	switch {
	case res.TimedOut:
		return govstore.RunTimedOut
	case res.Success:
		return govstore.RunPassed
	}
	return govstore.RunFailed
}

// statusSignals names what the status says about a command.
func statusSignals(status string, exitCode *int, timeout int) []string {
	switch {
	case status == govstore.RunTimedOut:
		return []string{fmt.Sprintf("timed out after %ds; the command's process group was terminated", timeout)}
	case exitCode != nil && *exitCode != 0:
		return []string{fmt.Sprintf("exited with code %d", *exitCode)}
	case status == govstore.RunFailed:
		return []string{"exited abnormally (no exit code)"}
	}
	return nil
}

// readEvidenceFailure reads the tail of a retained original the caller may read.
func readEvidenceFailure(ctx context.Context, req FailureRequest) (*collectedOutput, error) {
	if req.Evidence == nil {
		return nil, Unavailable("evidence is not available on this server")
	}
	if err := admitAnalysis(ctx, outcomeTailBytes); err != nil {
		return nil, err
	}
	tail, err := req.Evidence(ctx, req.EvidenceHandle, outcomeTailBytes)
	switch {
	case errors.Is(err, evidence.ErrInvalidHandle):
		return nil, Invalid("evidence_handle is not an xMustard evidence handle")
	case errors.Is(err, evidence.ErrCorrupt):
		return nil, err
	case err != nil:
		return nil, NotFoundErr("evidence_handle is not a retained original this caller can read (missing, expired, revoked or issued to another principal)").WithCause(err)
	}
	out := evidenceOutput(tail)
	out.in.Source = govstore.RunSourceEvidence
	return out, nil
}

// evidenceOutput analyzes an evidence tail. The source key is the handle's digest, so
// a capture and a later why_failed of the same original share one outcome.
func evidenceOutput(tail *evidence.Tail) *collectedOutput {
	out := analyzedOutput(string(tail.Data), tail.TotalBytes)
	failed := tail.IsError || (tail.ExitCode != nil && *tail.ExitCode != 0) || len(tail.FailingTests) > 0
	status := govstore.RunPassed
	if failed {
		status = govstore.RunFailed
		out.analysis.Signals = append(evidenceSignals(tail), out.analysis.Signals...)
	}
	out.analysis.FailingTests = boundedStrings(tail.FailingTests, maxOutcomeErrorLines)
	out.in.SourceKey = "evidence:" + digestHex(tail.Handle)
	out.in.Status, out.in.ExitCode, out.in.Tool = status, tail.ExitCode, tail.Tool
	out.in.EvidenceHandle, out.in.OutputSHA256 = tail.Handle, tail.RawSHA256
	return out
}

func evidenceSignals(tail *evidence.Tail) []string {
	var s []string
	if tail.ExitCode != nil && *tail.ExitCode != 0 {
		s = append(s, fmt.Sprintf("exited with code %d", *tail.ExitCode))
	}
	if tail.IsError {
		s = append(s, "the captured "+tail.Tool+" call reported an error")
	}
	if n := len(tail.FailingTests); n > 0 {
		s = append(s, fmt.Sprintf("%d failing test(s)", n))
	}
	return s
}

// pastedLogOutput analyzes a pasted log's tail. A log is a failure report: it records
// as failed, keyed by its digest.
func pastedLogOutput(text string) *collectedOutput {
	sum := sha256.Sum256([]byte(text))
	out := analyzedOutput(text, int64(len(text)))
	out.in.Source, out.in.SourceKey = govstore.RunSourceLog, "log:"+hex.EncodeToString(sum[:])
	out.in.Status, out.in.OutputSHA256 = govstore.RunFailed, hex.EncodeToString(sum[:])
	return out
}

// --- captured outputs (the evidence capture hook) -------------------------------------

// CapturedOutcome is a captured test, build or lint output (POST .../evidence/capture).
type CapturedOutcome struct {
	Family  evidence.Family
	Command string // the shell command line the client reported, if any
	Failed  bool
	// Handle names the retained original, read through Evidence only when the outcome
	// is recorded; Text is the whole output when the capture kept no original (it was
	// below its projection target).
	Handle   string
	Evidence EvidenceTailFunc
	Text     string
	Tool     string
	// ExitCode and FailingTests are what the family reducer found in the output.
	ExitCode     *int
	FailingTests []string
	// Session and Call identify the client's tool call, to key an unretained capture.
	Session, Call string
	Actor         ContextActor
}

// RecordCapturedOutcome turns a captured failing test, build or lint output into a run
// outcome, and a passing one into the outcome that resolves its command's open failure
// (only when one is open, so a passing capture costs one read otherwise). It runs no
// Rust or git process: the analysis is the text's, and the changed files are read when
// the outcome is explained. Other families are ignored.
func RecordCapturedOutcome(ctx context.Context, dataDir, workspaceID string, c CapturedOutcome) error {
	if !whyFailedFamilies[c.Family] {
		return nil
	}
	subject := ""
	if argv, err := splitShellArgs(c.Command); err == nil && len(argv) > 0 {
		subject = commandSubject("", argv)
	}
	if !c.Failed && (subject == "" || !openFailureFor(ctx, dataDir, workspaceID, subject)) {
		return nil
	}
	out, err := capturedOutput(ctx, c)
	if err != nil {
		return err
	}
	out.in.Source, out.in.WorkspaceID, out.in.SubjectKey = govstore.RunSourceCapture, workspaceID, subject
	out.in.Status, out.in.Tool, out.in.Command = govstore.RunPassed, c.Tool, redactedLine(c.Command)
	if c.Failed {
		out.in.Status = govstore.RunFailed
	}
	_, _, err = storeRunOutcome(ctx, dataDir, workspaceID, out, c.Actor.storeActor(contextRoot(dataDir, workspaceID)))
	return err
}

// capturedOutput analyzes a capture: the retained original's tail, keyed by its handle
// like why_failed(evidence_handle), or the unretained text, keyed by the tool call.
func capturedOutput(ctx context.Context, c CapturedOutcome) (*collectedOutput, error) {
	if c.Handle == "" {
		if err := admitAnalysis(ctx, int64(len(c.Text))); err != nil {
			return nil, err
		}
		out := analyzedOutput(c.Text, int64(len(c.Text)))
		sum := sha256.Sum256([]byte(c.Text))
		out.in.SourceKey = "capture:" + digestHex(c.Session+"\x00"+c.Call+"\x00"+hex.EncodeToString(sum[:]))
		out.in.OutputSHA256, out.in.ExitCode = hex.EncodeToString(sum[:]), c.ExitCode
		out.analysis.FailingTests = boundedStrings(c.FailingTests, maxOutcomeErrorLines)
		return out, nil
	}
	if c.Evidence == nil {
		return nil, Unavailable("evidence is not available on this server")
	}
	if err := admitAnalysis(ctx, outcomeTailBytes); err != nil {
		return nil, err
	}
	tail, err := c.Evidence(ctx, c.Handle, outcomeTailBytes)
	if err != nil {
		return nil, err
	}
	return evidenceOutput(tail), nil
}

func openFailureFor(ctx context.Context, dataDir, workspaceID, subject string) bool {
	var open []govstore.RunOutcome
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		open, err = r.ListRunOutcomes(ctx, govstore.RunOutcomeFilter{WorkspaceID: workspaceID, Open: true, SubjectKey: subject, Limit: 1})
		return err
	})
	return err == nil && len(open) > 0
}

// --- analysis --------------------------------------------------------------------------

// outputRedactor removes secrets, including the values of secret-named variables in
// this process's environment (a command runs without them, but its code can still find
// and print one), before anything is analyzed or stored.
var outputRedactor = sync.OnceValue(func() *redact.Redactor {
	return redact.New(redact.WithEnv(redact.SecretEnv(os.Environ())...))
})

// analysisCopies is how many copies of an analyzed tail one analysis holds: as read, as
// text and redacted (the analysis itself works line by line).
const analysisCopies = 3

// admitAnalysis reserves, in the request's transient budget, the memory analyzing n
// bytes of output holds (their last outcomeTailBytes). A command's output is admitted
// after the command ends, so the window is not held while it runs. Outside a request
// (no scope) nothing is reserved.
func admitAnalysis(ctx context.Context, n int64) error {
	scope, owned := budget.ScopeFor(ctx)
	if owned {
		scope.Close()
		return nil
	}
	err := scope.Acquire(analysisCopies * min(n, outcomeTailBytes))
	if errors.Is(err, budget.ErrTooLarge) { // retrying cannot help: the pool is smaller
		return Unavailable("this server's transient budget (XMUSTARD_TRANSIENT_BYTE_BUDGET) is too small to analyze the output").WithCause(err)
	}
	return err
}

// analyzedOutput redacts the last outcomeTailBytes of raw (total bytes long) and
// analyzes it.
func analyzedOutput(raw string, total int64) *collectedOutput {
	if len(raw) > outcomeTailBytes {
		raw = raw[len(raw)-outcomeTailBytes:]
	}
	analyzed := int64(len(raw))
	text, rep := outputRedactor().String(raw)
	a := outcomeAnalysis{
		Signals:        outputSignals(text),
		ErrorLines:     salientErrorLines(text, maxOutcomeErrorLines),
		MentionedPaths: mentionedPaths(text),
		Redacted:       rep.Count,
	}
	stored := text
	if len(stored) > outcomeStoredTail {
		stored = strings.ToValidUTF8(stored[len(stored)-outcomeStoredTail:], "")
	}
	return &collectedOutput{
		in:       govstore.RunOutcomeInput{OutputBytes: max(total, analyzed), AnalyzedBytes: analyzed, Tail: stored},
		analysis: a,
	}
}

// failureMarkers are output markers named as a signal, matched case-insensitively
// against the analyzed tail, in this order.
var failureMarkers = []struct{ needle, signal string }{
	{"panic:", "Go panic"},
	{"--- fail:", "Go test failure"},
	{"traceback (most recent call last)", "Python traceback"},
	{"assertionerror", "assertion failed"},
	{"error[e", "Rust compile error"},
	{"test result: failed", "Rust test failure"},
	{"npm err!", "npm error"},
	{"segmentation fault", "segmentation fault"},
	{"undefined reference", "link error"},
	{"cannot find module", "missing module"},
	{"out of memory", "out of memory"},
}

// outputSignals scans line by line, so no lower-cased copy of the whole tail is held.
func outputSignals(text string) []string {
	found := make([]bool, len(failureMarkers))
	for line := range strings.Lines(text) {
		lower := strings.ToLower(line)
		for i, m := range failureMarkers {
			found[i] = found[i] || strings.Contains(lower, m.needle)
		}
	}
	var out []string
	for i, m := range failureMarkers {
		if found[i] {
			out = append(out, m.signal)
		}
	}
	return out
}

// mentionedPaths collects the distinct path-like tokens of text, bounded: one scan of
// the whole text (twice as fast as a scan per line), at most maxPathMatches matches.
func mentionedPaths(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range pathLikePattern.FindAllString(text, maxPathMatches) {
		m = strings.TrimPrefix(m, "./")
		if len(m) > maxMentionedPathLen || seen[m] || strings.HasPrefix(m, "//") { // "//" is a URL's host part
			continue
		}
		seen[m] = true
		if out = append(out, m); len(out) >= maxMentionedPaths {
			break
		}
	}
	return out
}

// implicatedBy returns the changed files a mentioned path names: by the whole
// repo-relative path, a trailing part of it, or (for an absolute path in the log) the
// path relative to the workspace root.
func implicatedBy(mentioned []string, root string, changed []string) []string {
	if len(mentioned) == 0 || len(changed) == 0 {
		return nil
	}
	set := make(map[string]bool, len(mentioned))
	for _, m := range mentioned {
		if root != "" {
			m = strings.TrimPrefix(m, strings.TrimSuffix(root, "/")+"/")
		}
		set[m] = true
	}
	var hits []string
	for _, c := range changed {
		for s := c; ; {
			if set[s] {
				hits = append(hits, c)
				break
			}
			i := strings.IndexByte(s, '/')
			if i < 0 {
				break
			}
			s = s[i+1:]
		}
	}
	sort.Strings(hits)
	return hits
}

// --- explanation ---------------------------------------------------------------------

// explainOutcome renders a recorded outcome as a why_failed answer: its stored analysis,
// the changed files it implicates now, and the promoted memories on them.
func explainOutcome(ctx context.Context, dataDir, workspaceID string, rec govstore.RunOutcome, changed []string, changedErr error) *FailureExplanation {
	var a outcomeAnalysis
	if err := json.Unmarshal(rec.Analysis, &a); err != nil {
		log.Printf("why_failed: outcome %s has an unreadable analysis: %v", rec.ID, err)
	}
	root := contextRoot(dataDir, workspaceID)
	exp := &FailureExplanation{
		RunID: rec.ID, Failed: rec.Failed, Status: rec.Status, ExitCode: rec.ExitCode,
		Signals: nonNil(a.Signals), ErrorLines: nonNil(a.ErrorLines), ChangedFiles: nonNil(changed),
		GeneratedAt: nowUTC(), Source: rec.Source, Command: rec.Command, Cwd: rec.Cwd, TimedOut: a.TimedOut,
		FailingTests: a.FailingTests, EvidenceHandle: rec.EvidenceHandle, RecordedAt: rec.CreatedAt,
		HeadSHA: rec.HeadSHA, ResolvedBy: rec.ResolvedBy,
		Output: &OutcomeOutput{TotalBytes: rec.OutputBytes, AnalyzedBytes: rec.AnalyzedBytes,
			Truncated: rec.AnalyzedBytes < rec.OutputBytes, Redacted: a.Redacted, Tail: rec.Tail},
	}
	if changedErr != nil {
		exp.Unknown = append(exp.Unknown, GroundingUnknown{Field: "changed_files", Reason: changedErr.Error()})
	}
	exp.ImplicatedPaths = nonNil(implicatedBy(a.MentionedPaths, root, changed))
	exp.Memories, exp.Unknown = linkMemories(ctx, dataDir, workspaceID, exp.ImplicatedPaths, a.MentionedPaths, exp.Unknown)
	exp.Summary = summarizeFailure(exp)
	return exp
}

// linkMemories finds promoted memories anchored to the implicated files, or to the
// files the output names when none is implicated. A store failure is reported in
// unknown, not as "no memories".
func linkMemories(ctx context.Context, dataDir, workspaceID string, implicated, mentioned []string, unknown []GroundingUnknown) ([]ImplicatedMemory, []GroundingUnknown) {
	paths := implicated
	if len(paths) == 0 {
		paths = mentioned[:min(len(mentioned), maxMemoryLookupPaths)]
	}
	if len(paths) == 0 {
		return nil, unknown
	}
	var hits []govstore.AnchorHit
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		hits, err = r.EntriesByAnchor(ctx, govstore.AnchorQuery{WorkspaceID: workspaceID, Kind: govstore.AnchorPath,
			Values: paths, ServedOnly: true, Limit: maxOutcomeMemories})
		return err
	})
	if err != nil {
		return nil, append(unknown, GroundingUnknown{Field: "memories", Reason: "memory store unreadable: " + err.Error()})
	}
	out := make([]ImplicatedMemory, 0, len(hits))
	for _, h := range hits {
		out = append(out, ImplicatedMemory{EntryID: h.EntryID, Path: h.Value, Stale: h.StaleSince != ""})
	}
	return out, unknown
}

// --- ground ----------------------------------------------------------------------------

// recentRunOutcomes returns the open failures recorded in the last recentOutcomeWindow,
// newest first, and whether more than recentOutcomeLimit exist.
func recentRunOutcomes(ctx context.Context, dataDir, workspaceID string) ([]govstore.RunOutcome, bool, error) {
	var recs []govstore.RunOutcome
	since := time.Now().Add(-recentOutcomeWindow).UTC().Format(time.RFC3339Nano)
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		recs, err = r.ListRunOutcomes(ctx, govstore.RunOutcomeFilter{WorkspaceID: workspaceID, Open: true, Since: since,
			Limit: recentOutcomeLimit + 1})
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(recs) > recentOutcomeLimit {
		return recs[:recentOutcomeLimit], true, nil
	}
	return recs, false, nil
}

// --- storage and helpers -----------------------------------------------------------------

// storeRunOutcome records out with its analysis, which it keeps typed until here.
func storeRunOutcome(ctx context.Context, dataDir, workspaceID string, out *collectedOutput, actor govstore.Actor) (govstore.RunOutcome, bool, error) {
	analysis, err := json.Marshal(out.analysis)
	if err != nil {
		return govstore.RunOutcome{}, false, err
	}
	in := out.in
	in.Analysis = analysis
	var rec govstore.RunOutcome
	var created bool
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		var err error
		rec, created, err = tx.RecordRunOutcome(ctx, in, actor)
		return err
	})
	return rec, created, err
}

// commandSubject identifies what a command verifies: its working directory and argv.
func commandSubject(cwd string, argv []string) string {
	return "cmd:" + digestHex(cwd+"\x00"+strings.Join(argv, "\x00"))
}

func digestHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func outcomeNonce() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("outcome nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// redactedLine redacts a command line and bounds it for storage.
func redactedLine(s string) string {
	out, _ := outputRedactor().String(s)
	if len(out) > 1024 {
		out = strings.ToValidUTF8(out[:1024], "") + "…"
	}
	return out
}

func boundedStrings(in []string, n int) []string {
	if len(in) > n {
		return in[:n]
	}
	return in
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
