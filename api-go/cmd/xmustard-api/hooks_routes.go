package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/hooks/transport"
	"xmustard/api-go/internal/injection"
	"xmustard/api-go/internal/workspaceops"
)

// Claude Code hook service (WS-23; PAR-HAR-01, PAR-HAR-02, PAR-ADP-06, PAR-CTX-01,
// PAR-FRESH-07, PAR-PROV-07, PAR-RT-12). Claude Code posts each hook event's JSON to
// POST /api/hooks/claude/<Event> (an http hook, or the static client xmustard-hook
// over the Unix socket for the events that only take command hooks) and reads the
// answer as hook JSON output:
//
//   - PostToolUse captures the native tool's output (redacted, reduced by its tool
//     family, retained behind a recoverable handle) and returns the shape-matched
//     updatedToolOutput; an Edit or Write feeds the dirty set and reports the syntax
//     errors it introduced; a git command that moves HEAD earns a freshness notice.
//   - PreToolUse injects, for Grep, Glob and Bash rg|grep, the index hits and the
//     verified memories for the pattern, and for Read and Edit the memories bound to
//     the path (stale ones labeled); a burst of searches earns a nudge (2-minute
//     cooldown).
//   - SessionStart and SubagentStart inject ground's spawn-free part and the core-tier
//     memories; UserPromptSubmit the memories a prompt keyword triggers.
//   - FileChanged feeds the dirty set; CwdChanged and SessionStart return watchPaths.
//   - Stop, SubagentStop, SessionEnd, PreCompact and PostCompact are recorded on a
//     queue and answered at once (SessionEnd hooks share a 1.5 s budget).
//
// Every pushed memory passes the WS-56 injection policy for its surface (hook: human
// approval, no quarantine, no instruction pattern) and arrives framed as data. The
// service fails open: an answer not ready within the hook budget (~200 ms) is an empty
// 200, and so is any error, so the client keeps its original output. No hook starts a
// process: Rust work runs only on a resident worker that is already running, and the
// repository identity is never sampled (a hook-delivered observation is
// captured_identity=unknown).
//
// Route gates: every event is its own route with a row in routeGateTable. Capture events
// need the proposer role, the others the reader role; all are served in read-only mode
// (a capture stores only the caller's own output). The workspace comes from the
// X-Xmustard-Workspace header or the registered root holding the event's cwd, and is
// then checked like a workspace path: served by this deployment and in the token's scope.

// defaultHookBudget is how long a hook answer may take; past it the route answers empty.
const defaultHookBudget = 180 * time.Millisecond

const (
	hookSearchHits   = 5   // index hits a search pre-hook injects
	hookMemoryLimit  = 5   // memories one pre-hook considers
	hookWatchPaths   = 128 // watchPaths an answer carries
	hookTriggerLimit = 16  // memories one prompt may trigger
	hookQueueDepth   = 256 // queued enqueue-mode events before new ones are dropped
)

// hookBudget is XMUSTARD_HOOK_BUDGET_MS, from 20 to 5,000 ms (default 180).
func hookBudget() time.Duration {
	if ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv("XMUSTARD_HOOK_BUDGET_MS"))); err == nil && ms >= 20 && ms <= 5000 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultHookBudget
}

// The resident-worker reads a hook makes; tests replace them.
var (
	hookSearch = workspaceops.HookSearch
	hookSyntax = workspaceops.HookSyntaxCheck
)

// hookStats counts hook answers since start (hookUsage, /api/health).
var hookStats struct {
	answered, timedOut, failOpen, enqueued, dropped atomic.Int64
}

// hookUsage is the operator's view of the hook service in /api/health: answers given,
// answers the budget cut to empty, bodies it could not read (empty answers too), and
// the queued events and those dropped because the queue was full.
func hookUsage() map[string]int64 {
	return map[string]int64{"answered": hookStats.answered.Load(), "timed_out": hookStats.timedOut.Load(),
		"fail_open": hookStats.failOpen.Load(), "enqueued": hookStats.enqueued.Load(), "dropped": hookStats.dropped.Load()}
}

type hookServer struct {
	store    *evidence.Store
	reg      *evidence.Registry
	sessions *hooks.Sessions
	queue    chan hookQueued
}

// hookCall is one hook request being answered.
type hookCall struct {
	r      *http.Request
	ev     hooks.Event
	in     hooks.Input
	body   []byte
	caller string // principal id; "" in open mode
	key    string // session key (hooks.Key)
	ws     workspaceops.ResolvedWorkspace
	bound  bool // ws was resolved and passed the scope checks
}

// hookResult is what a handler produced. after runs once the answer has been written
// (recording an outcome, say), so it never delays the client. It gets the request with
// a context of its own, bounded by hookAfterTimeout: the client may close the
// connection as soon as it has read the answer, which cancels the request's context.
type hookResult struct {
	context string
	updated json.RawMessage
	watch   []string
	after   func(*http.Request)
}

// hookAfterTimeout bounds the work a hook does after it has answered.
const hookAfterTimeout = 10 * time.Second

// hookHandlers answers the Respond-mode events.
var hookHandlers = map[string]func(*hookServer, context.Context, *hookCall) hookResult{
	"SessionStart":       (*hookServer).sessionStart,
	"SubagentStart":      (*hookServer).subagentStart,
	"UserPromptSubmit":   (*hookServer).userPrompt,
	"PreToolUse":         (*hookServer).preToolUse,
	"PostToolUse":        (*hookServer).postToolUse,
	"PostToolUseFailure": (*hookServer).postToolFailure,
	"PostToolBatch":      (*hookServer).postToolBatch,
	"CwdChanged":         (*hookServer).cwdChanged,
	"FileChanged":        (*hookServer).fileChanged,
	"WorktreeRemove":     (*hookServer).worktreeRemove,
}

// hookQueued is an Enqueue-mode event waiting for the consumer.
type hookQueued struct {
	event, key, agent string
}

// hookQueueEffects is what the consumer does with each Enqueue-mode event. Stop and
// PreCompact only mark the queue contract today: WS-33 adds the session ledger and
// the compaction snapshot behind them.
var hookQueueEffects = map[string]func(*hooks.Sessions, hookQueued){
	"SessionEnd":   func(s *hooks.Sessions, q hookQueued) { s.End(q.key) },
	"SubagentStop": func(s *hooks.Sessions, q hookQueued) { s.AgentStopped(q.key, q.agent) },
	"PostCompact":  func(s *hooks.Sessions, q hookQueued) { s.Compacted(q.key) },
	"PreCompact":   func(*hooks.Sessions, hookQueued) {},
	"Stop":         func(*hooks.Sessions, hookQueued) {},
}

func newHookServer(store *evidence.Store) *hookServer {
	s := &hookServer{store: store, reg: evidence.DefaultRegistry(), sessions: hooks.NewSessions(hooks.DefaultLimits, time.Now),
		queue: make(chan hookQueued, hookQueueDepth)}
	go s.drain()
	return s
}

// registerHookRoutes mounts one route per Claude Code event.
func registerHookRoutes(mux routeRegistrar, store *evidence.Store) *hookServer {
	s := newHookServer(store)
	for _, ev := range hooks.Events {
		mux.HandleFunc("POST /api/hooks/claude/"+ev.Name, s.handle(ev))
	}
	return s
}

func (s *hookServer) handle(ev hooks.Event) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// the body-limit middleware admitted and capped it; Claude Code clamps tool
		// output (Bash at 30,000 characters), so a hook body is small
		body, err := io.ReadAll(r.Body)
		var in hooks.Input
		if err == nil {
			in, err = hooks.Decode(body)
		}
		if err != nil || in.Event != "" && in.Event != ev.Name {
			hookStats.failOpen.Add(1)
			writeHookAnswer(w, nil)
			return
		}
		caller, _ := principalScope(r)
		call := &hookCall{r: r, ev: ev, in: in, body: body, caller: caller, key: hooks.Key(caller, in.SessionID)}
		if ev.Mode == hooks.Enqueue {
			s.enqueue(call)
			writeHookAnswer(w, nil)
			return
		}
		ctx, cancel := context.WithTimeout(budget.WithoutHeavyWait(r.Context()), hookBudget())
		defer cancel()
		type answered struct {
			out   *hooks.Output
			after func(*http.Request)
		}
		done := make(chan answered, 1)
		go func() {
			var a answered
			defer func() {
				if p := recover(); p != nil {
					log.Printf("hook %s: %v", ev.Name, p)
					a = answered{}
				}
				done <- a
			}()
			a.out, a.after = s.answer(ctx, call)
		}()
		var a answered
		select {
		case a = <-done:
			writeHookAnswer(w, a.out)
		case <-ctx.Done():
			hookStats.timedOut.Add(1)
			s.sessions.NoteBusy(call.key)
			writeHookAnswer(w, nil)
			// the work stops with its context; the request stays valid until it has
			a = <-done
		}
		if a.after != nil {
			actx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), hookAfterTimeout)
			defer stop()
			a.after(r.WithContext(actx))
		}
	}
}

// answer runs the event's handler and builds its output; nil when the budget ran out.
func (s *hookServer) answer(ctx context.Context, c *hookCall) (*hooks.Output, func(*http.Request)) {
	c.bound = s.resolve(c)
	res := hookHandlers[c.ev.Name](s, ctx, c)
	if ctx.Err() != nil {
		return nil, res.after
	}
	if c.ev.Context {
		res.context = hooks.Compose(res.context, s.sessions.BusyNote(c.key))
	}
	hookStats.answered.Add(1)
	return hooks.Answer(c.ev, res.context, res.updated, res.watch), res.after
}

// writeHookAnswer writes an answer: 200 with an empty body when there is nothing to say.
func writeHookAnswer(w http.ResponseWriter, out *hooks.Output) {
	var body []byte
	if out != nil {
		body, _ = json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// resolve binds the call to its workspace: the one the X-Xmustard-Workspace header
// names, else the registered root that holds the event's cwd. The checks every
// workspace route makes follow, in order, failing closed: a safe id, served by this
// deployment, in the caller's token scope.
func (s *hookServer) resolve(c *hookCall) bool {
	var ws workspaceops.ResolvedWorkspace
	var err error
	found := false
	if id := strings.TrimSpace(c.r.Header.Get(transport.WorkspaceHeader)); id != "" {
		if !workspaceops.IsSafeID(id) {
			return false
		}
		ws, err = workspaceops.LookupWorkspace(dataDir(), id)
		found = err == nil
	} else {
		ws, found, err = workspaceops.WorkspaceForPath(dataDir(), c.in.Cwd)
	}
	if err != nil || !found {
		return false
	}
	if !postureFrom(c.r).allowsWorkspace(ws.WorkspaceID) {
		return false
	}
	if p := principalFromContext(c.r.Context()); p != nil && !p.AllowsWorkspace(ws.WorkspaceID) {
		workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
			Action: "denied", Actor: p.ID, Detail: "workspace " + ws.WorkspaceID + " not in token scope (hook " + c.ev.Name + ")",
			Method: c.r.Method, Path: c.r.URL.Path, RemoteAddr: c.r.RemoteAddr,
		})
		return false
	}
	c.ws = ws
	return true
}

// --- session events ---

func (s *hookServer) sessionStart(ctx context.Context, c *hookCall) hookResult {
	if c.in.Source == "clear" || c.in.Source == "compact" {
		s.sessions.Compacted(c.key) // what was pushed is gone from the context
	}
	return s.grounding(ctx, c, true)
}

func (s *hookServer) subagentStart(ctx context.Context, c *hookCall) hookResult {
	s.sessions.AgentStarted(c.key, c.in.AgentID, c.in.AgentType)
	return s.grounding(ctx, c, false)
}

// grounding is SessionStart's and SubagentStart's context: ground's spawn-free part
// and the core-tier memories, and (SessionStart) the files memory is anchored to as
// watchPaths, so FileChanged reports their edits.
func (s *hookServer) grounding(ctx context.Context, c *hookCall, watch bool) hookResult {
	if !c.bound {
		return hookResult{}
	}
	g := workspaceops.HookGrounding(ctx, dataDir(), c.ws.WorkspaceID, c.caller)
	parts := []string{groundLine(c.ws, g)}
	var res hookResult
	if idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID); err == nil {
		parts = append(parts, s.push(ctx, c, injection.SurfaceCore, idx.Core, nil, "recall")...)
		if watch {
			res.watch = watchPaths(c.ws, idx.Anchors)
		}
	}
	res.context = hooks.Compose(parts...)
	return res
}

// groundLine is ground's summary as far as a hook computes it.
func groundLine(ws workspaceops.ResolvedWorkspace, g *workspaceops.SessionGrounding) string {
	count := func(n *int) string {
		if n == nil {
			return "?"
		}
		return strconv.Itoa(*n)
	}
	runs := "?"
	if g.BlockedByFailingVerification != nil {
		runs = strconv.Itoa(len(g.RecentFailedRuns))
		if n := len(g.RecentFailedRuns); n > 0 {
			runs += " (" + strings.Join(g.RecentFailedRuns[:min(n, 5)], ", ") + ")"
		}
	}
	return fmt.Sprintf("[xmustard ground] workspace %s at %s: %s failed run(s), %s stale memory, %s pending memory await your verification. "+
		"Changes, drift and index coverage are not sampled by hooks; the xmustard ground tool reports them.",
		ws.WorkspaceID, ws.Root, runs, count(g.StaleMemory), count(g.PendingForYou))
}

// userPrompt pushes the memories a keyword of the prompt triggers (tags
// "trigger:<keyword>").
func (s *hookServer) userPrompt(ctx context.Context, c *hookCall) hookResult {
	if !c.bound || strings.TrimSpace(c.in.Prompt) == "" {
		return hookResult{}
	}
	idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID)
	if err != nil || len(idx.Triggers) == 0 {
		return hookResult{}
	}
	keywords, ids := triggered(idx.Triggers, c.in.Prompt)
	pushed := s.push(ctx, c, injection.SurfaceHook, ids, nil, `recall(tags=["trigger:<keyword>"])`)
	if strings.TrimSpace(strings.Join(pushed, "")) == "" {
		return hookResult{}
	}
	head := "[xmustard] memory triggered by keywords in the prompt: " + strings.Join(keywords, ", ")
	return hookResult{context: hooks.Compose(append([]string{head}, pushed...)...)}
}

// triggered returns the trigger keywords a prompt names, in the order it names them,
// and the memories they trigger (each once, at most hookTriggerLimit).
func triggered(triggers map[string][]string, prompt string) (keywords, ids []string) {
	seen := map[string]bool{}
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-.", r))
	})
	for _, w := range words {
		w = strings.Trim(w, ".-")
		if ids0, ok := triggers[w]; ok && !seen["kw\x00"+w] {
			seen["kw\x00"+w] = true
			keywords = append(keywords, w)
			for _, id := range ids0 {
				if !seen[id] && len(ids) < hookTriggerLimit {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	return keywords, ids
}

// --- tool events ---

func (s *hookServer) preToolUse(ctx context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	switch hooks.ClassOf(c.in.ToolName) {
	case hooks.ToolSearch, hooks.ToolShell:
		return s.searchContext(ctx, c)
	case hooks.ToolRead, hooks.ToolEdit:
		return s.fileContext(ctx, c)
	}
	return hookResult{}
}

// searchContext is a search call's context: the memories and index hits for its
// pattern, and the burst nudge when it is due.
func (s *hookServer) searchContext(ctx context.Context, c *hookCall) hookResult {
	q := hooks.SearchQuery(c.in.ToolName, c.in.ToolInput)
	if q == "" {
		return hookResult{}
	}
	nudge := ""
	if s.sessions.NoteSearch(c.key) {
		nudge = hooks.Nudge
	}
	var hits json.RawMessage
	var wg sync.WaitGroup
	wg.Go(func() { hits, _ = hookSearch(ctx, c.ws, q, hookSearchHits) })
	parts := s.memoryContext(ctx, c, q, nil)
	wg.Wait()
	return hookResult{context: hooks.Compose(append(parts, hooks.RenderHits(q, hits, hookSearchHits), nudge)...)}
}

// fileContext is a Read or Edit's context: the memories bound to the file. Before an
// edit it also keeps the file's syntax errors as the baseline of the post-edit delta.
func (s *hookServer) fileContext(ctx context.Context, c *hookCall) hookResult {
	rel := workspaceops.RelativeToRoot(c.ws, c.in.ToolInput.TargetPath())
	if rel == "" {
		return hookResult{}
	}
	var wg sync.WaitGroup
	if hooks.ClassOf(c.in.ToolName) == hooks.ToolEdit {
		wg.Go(func() { s.baseline(ctx, c, rel) })
	}
	parts := s.memoryContext(ctx, c, "", []string{rel})
	wg.Wait()
	return hookResult{context: hooks.Compose(parts...)}
}

// memoryContext pushes the served memories recall ranks for a pattern or a path.
func (s *hookServer) memoryContext(ctx context.Context, c *hookCall, query string, paths []string) []string {
	cands, err := workspaceops.HookMemories(ctx, dataDir(), c.ws.WorkspaceID, c.caller, query, paths, hookMemoryLimit)
	if err != nil || len(cands) == 0 {
		return nil
	}
	ids := make([]string, len(cands))
	stale := map[string][]string{}
	for i, m := range cands {
		ids[i] = m.ID
		if m.Stale {
			stale[m.ID] = m.StalePaths
		}
	}
	recall := fmt.Sprintf("recall(query=%q)", query)
	if len(paths) > 0 {
		recall = fmt.Sprintf("recall(paths=[%q])", paths[0])
	}
	return s.push(ctx, c, injection.SurfaceHook, ids, stale, recall)
}

// push admits candidate memories through the injection policy of a pushed surface
// (workspaceops.AdmitMemory) and returns what the context carries: the admitted
// memories framed as data, the stale labels of the admitted ones, and a note on the
// withheld ones. A memory is considered once per session (until compaction); an
// admission error pushes nothing (fail closed).
func (s *hookServer) push(ctx context.Context, c *hookCall, surface injection.Surface, ids []string, stale map[string][]string, recall string) []string {
	ids = s.sessions.Unseen(c.key, ids)
	if len(ids) == 0 {
		return nil
	}
	m, err := workspaceops.AdmitMemory(ctx, dataDir(), c.ws.WorkspaceID, surface, ids[:min(len(ids), 64)])
	if err != nil {
		return nil
	}
	s.sessions.MarkInjected(c.key, ids)
	staleAdmitted := map[string][]string{}
	for _, a := range m.Admitted {
		if p, ok := stale[a.ID]; ok {
			staleAdmitted[a.ID] = p
		}
	}
	reasons := make([]string, len(m.Withheld))
	for i, w := range m.Withheld {
		reasons[i] = w.Reason
	}
	return []string{m.Text, hooks.StaleNote(staleAdmitted), hooks.WithheldNote(reasons, recall)}
}

// baseline keeps a file's syntax errors before an edit. A file that does not exist
// yet has an empty baseline: every error of the new file is new.
func (s *hookServer) baseline(ctx context.Context, c *hookCall, rel string) {
	raw, err := hookSyntax(ctx, c.ws, []string{rel})
	files, perr := hooks.ParseSyntax(raw)
	if err != nil || perr != nil || len(files) != 1 {
		return
	}
	switch files[0].Status {
	case "checked":
		s.sessions.SetBaseline(c.key, rel, files[0].Keys())
	case "unreadable":
		s.sessions.SetBaseline(c.key, rel, nil)
	}
}

func (s *hookServer) postToolUse(ctx context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	switch tool := c.in.ToolName; {
	case hooks.ClassOf(tool) == hooks.ToolEdit:
		return s.afterEdit(ctx, c)
	case hooks.Captured(tool):
		return s.afterCapture(ctx, c)
	}
	return hookResult{}
}

// afterEdit feeds the dirty set and reports the syntax errors the edit introduced.
func (s *hookServer) afterEdit(ctx context.Context, c *hookCall) hookResult {
	rel := workspaceops.RelativeToRoot(c.ws, c.in.ToolInput.TargetPath())
	if rel == "" {
		return hookResult{}
	}
	workspaceops.NoteDirtyPaths(c.ws.Scope, []string{rel})
	before, had := s.sessions.TakeBaseline(c.key, rel)
	raw, err := hookSyntax(ctx, c.ws, []string{rel})
	files, perr := hooks.ParseSyntax(raw)
	if err != nil || perr != nil || len(files) != 1 || files[0].Status != "checked" {
		return hookResult{}
	}
	f := files[0]
	return hookResult{context: hooks.SyntaxNote(rel, hooks.NewErrors(before, f.Errors), had, f.Truncated)}
}

// afterCapture replaces a native tool's output with its reduction when it was reduced
// and the shape matched; otherwise the client keeps its output, with the shape notice
// as context. A shell call also feeds the dirty set and may earn the post-git notice.
func (s *hookServer) afterCapture(ctx context.Context, c *hookCall) hookResult {
	var res hookResult
	var parts []string
	if obs := s.capture(ctx, c); obs != nil {
		switch obs.Shape.Mode {
		case evidence.ShapeReplace:
			res.updated = obs.Shape.Payload
		case evidence.ShapeFallback, evidence.ShapeSizeError:
			parts = append(parts, obs.Shape.Notice)
		}
		res.after = func(r *http.Request) { recordCaptureOutcome(r, s.store, c.ws.WorkspaceID, false, obs) }
	}
	if hooks.ClassOf(c.in.ToolName) == hooks.ToolShell {
		parts = append(parts, s.afterShell(c))
	}
	res.context = hooks.Compose(parts...)
	return res
}

// afterShell feeds the dirty set from the command's changed files (Claude Code's
// bashEditDiff) and gives the post-git notice when the command moved HEAD.
func (s *hookServer) afterShell(c *hookCall) string {
	var diff struct {
		ToolResponse struct {
			BashEditDiff struct {
				ChangedFiles []string `json:"changedFiles"`
			} `json:"bashEditDiff"`
		} `json:"tool_response"`
	}
	if json.Unmarshal(c.body, &diff) == nil {
		var rels []string
		for _, p := range diff.ToolResponse.BashEditDiff.ChangedFiles {
			if rel := workspaceops.RelativeToRoot(c.ws, p); rel != "" {
				rels = append(rels, rel)
			}
		}
		workspaceops.NoteDirtyPaths(c.ws.Scope, rels)
	}
	verb := hooks.GitHeadMove(c.in.ToolInput.Command)
	if verb == "" {
		return ""
	}
	workspaceops.InvalidateRepoIdentity(c.ws.Scope)
	if !s.sessions.GitNoticeDue(c.key) {
		return ""
	}
	return hooks.GitNotice(verb)
}

// capture stream-decodes the hook body into the spool through the capture redactor,
// reduces it by tool family and shapes it for Claude Code. nil when capture is not
// possible now (no redactor, memory refused, an error): the client keeps its output.
func (s *hookServer) capture(ctx context.Context, c *hookCall) *evidence.ObservationResult {
	redact := captureRedactor
	if redact == nil {
		return nil
	}
	if scope, owned := budget.ScopeFor(ctx); !owned {
		if scope.Acquire(captureWindowBytes) != nil {
			s.sessions.NoteBusy(c.key)
			return nil
		}
	} else {
		scope.Close()
	}
	actor, enforced := principalScope(c.r)
	res, err := s.store.Observe(ctx, s.reg, evidence.ObservationInput{
		WorkspaceID: c.ws.WorkspaceID, RepoScope: c.ws.Scope, Actor: actor, AuthEnforced: enforced,
		Format: evidence.FormatClaude, Body: bytes.NewReader(c.body), Meta: evidence.CaptureMeta{Client: hooks.Client},
		Redact: redact,
	})
	if err != nil {
		if errors.Is(err, budget.ErrOverloaded) {
			s.sessions.NoteBusy(c.key)
		}
		return nil
	}
	return res
}

// postToolFailure captures a failed tool's error text for the run outcome (a failing
// test, build or lint command) and names the failing tests it parsed.
func (s *hookServer) postToolFailure(ctx context.Context, c *hookCall) hookResult {
	if !c.bound || !hooks.Captured(c.in.ToolName) {
		return hookResult{}
	}
	obs := s.capture(ctx, c)
	if obs == nil {
		return hookResult{}
	}
	res := hookResult{after: func(r *http.Request) { recordCaptureOutcome(r, s.store, c.ws.WorkspaceID, false, obs) }}
	if tests := obs.Facts.FailingTests; len(tests) > 0 {
		res.context = fmt.Sprintf("[xmustard] failing tests in this output: %s. The xmustard why_failed tool explains the failure from the recorded outcome.",
			strings.Join(tests[:min(len(tests), 8)], ", "))
	}
	return res
}

// postToolBatch nudges when one parallel batch ran several searches.
func (s *hookServer) postToolBatch(_ context.Context, c *hookCall) hookResult {
	n := 0
	for _, call := range c.in.ToolCalls {
		if hooks.SearchQuery(call.ToolName, call.ToolInput) != "" {
			n++
		}
	}
	if s.sessions.NoteBatch(c.key, n) {
		return hookResult{context: hooks.Nudge}
	}
	return hookResult{}
}

// --- change events ---

// cwdChanged returns the watch list of the workspace the new directory is in.
func (s *hookServer) cwdChanged(ctx context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID)
	if err != nil {
		return hookResult{}
	}
	return hookResult{watch: watchPaths(c.ws, idx.Anchors)}
}

// fileChanged feeds the dirty set.
func (s *hookServer) fileChanged(_ context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	if rel := workspaceops.RelativeToRoot(c.ws, c.in.FilePath); rel != "" {
		workspaceops.NoteDirtyPaths(c.ws.Scope, []string{rel})
	}
	return hookResult{}
}

// worktreeRemove forgets a removed worktree's cached identity and dirty set.
func (s *hookServer) worktreeRemove(_ context.Context, c *hookCall) hookResult {
	if p := c.in.WorktreePath; filepath.IsAbs(p) {
		workspaceops.ForgetRoot(filepath.Clean(p))
	}
	return hookResult{}
}

// watchPaths are the absolute paths of the regular files memory is anchored to, under
// the root, at most hookWatchPaths.
func watchPaths(ws workspaceops.ResolvedWorkspace, anchors []string) []string {
	var out []string
	for _, a := range anchors {
		if len(out) == hookWatchPaths {
			break
		}
		abs := filepath.Join(ws.Root, filepath.FromSlash(a))
		if workspaceops.RelativeToRoot(ws, abs) == "" {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			out = append(out, abs)
		}
	}
	return out
}

// --- enqueue-mode events ---

func (s *hookServer) enqueue(c *hookCall) {
	select {
	case s.queue <- hookQueued{event: c.ev.Name, key: c.key, agent: c.in.AgentID}:
		hookStats.enqueued.Add(1)
	default:
		hookStats.dropped.Add(1)
	}
}

func (s *hookServer) drain() {
	for q := range s.queue {
		if f := hookQueueEffects[q.event]; f != nil {
			f(s.sessions, q)
		}
	}
}

// --- the static client's Unix socket ---

// serveHookSocket serves the hook routes on the Unix socket the static client
// xmustard-hook dials (transport.SocketPath), through the full middleware stack h (auth,
// body limits, route gates). Only /api/hooks/ is served there. A failure to listen is
// logged, not fatal: the client then falls back to the TCP address.
func serveHookSocket(h http.Handler) {
	path := transport.SocketPath(os.Getenv)
	if path == "" {
		return
	}
	ln, err := listenHookSocket(path)
	if err != nil {
		log.Printf("hooks: unix socket %s not served: %v", path, err)
		return
	}
	log.Printf("hooks: serving %s on unix socket %s", "/api/hooks/", path)
	srv := &http.Server{Handler: onlyHookRoutes(h), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("hooks: unix socket %s: %v", path, err)
	}
}

// listenHookSocket listens on path in a directory only this user can enter. A socket
// another live daemon serves is left alone; a stale one is replaced.
func listenHookSocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("directory %s must be a directory only its owner can use (mode 0700)", dir)
	}
	if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil, errors.New("another daemon serves it")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// onlyHookRoutes answers 404 for anything but the hook routes.
func onlyHookRoutes(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/hooks/") || slices.Contains(strings.Split(r.URL.Path, "/"), "..") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
