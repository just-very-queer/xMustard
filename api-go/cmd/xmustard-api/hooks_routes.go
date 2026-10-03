package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
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

// Hook service (WS-23, WS-40a; PAR-HAR-01, PAR-HAR-02, PAR-ADP-06, PAR-ADP-07,
// PAR-CTX-01, PAR-FRESH-07, PAR-PROV-07, PAR-RT-12). A client posts each hook event's
// JSON to POST /api/hooks/<client>/<Event> and reads the answer as hook JSON output in
// its own schema (hooks.Client): Claude Code through http hooks, or through the static
// client xmustard-hook over the Unix socket for the events that only take command
// hooks; Codex, which has no http handler, through xmustard-hook for every event. The
// handlers are shared by event name:
//
//   - PostToolUse captures the native tool's output (redacted, reduced by its tool
//     family, retained behind a recoverable handle) and returns the shape-matched
//     replacement (Claude Code's updatedToolOutput, Codex's block reason); an Edit,
//     Write or apply_patch feeds the dirty set and reports the syntax errors it
//     introduced; a git command that moves HEAD earns a freshness notice.
//   - PreToolUse injects, for Grep, Glob and Bash rg|grep, the index hits and the
//     verified memories for the pattern, and for Read, Edit and apply_patch the
//     memories bound to the paths (stale ones labeled); a burst of searches earns a
//     nudge (2-minute cooldown).
//   - SessionStart and SubagentStart inject ground's spawn-free part and the core-tier
//     memories; UserPromptSubmit the memories a prompt keyword triggers.
//   - FileChanged feeds the dirty set (the resident watcher's pending batch, WS-15);
//     SessionStart returns watchPaths, and CwdChanged always does ([] clears the list).
//   - Stop, SubagentStop, SessionEnd, PreCompact and PostCompact are recorded on a
//     queue and answered at once (SessionEnd hooks share a 1.5 s budget).
//
// Every pushed memory passes the WS-56 injection policy for its surface (hook: human
// approval, no quarantine, no instruction pattern) and arrives framed as data. Steering
// state (what was pushed, nudges, edit baselines) is per context: a subagent's hooks
// carry the parent's session_id and their own agent_id, and the subagent starts with a
// fresh context, so it has state of its own (hooks.SessionKey). A memory counts as
// pushed into a context only once the answer that carries it has been written.
//
// The service fails open: an answer not ready within the hook budget (~200 ms) is an
// empty 200, and so is a body it cannot read or a workspace out of scope, so the client
// keeps its original output. Authentication stays fail-closed: a missing, expired or
// reader-only token gets the middleware's 401 or 403, which Claude Code shows as a
// non-blocking hook error (the static client prints nothing). The service starts no
// process for a hook (the client starts xmustard-hook for command hooks): Rust work
// runs only on a resident worker that is already running, and the repository identity
// is never sampled (a hook-delivered observation is captured_identity=unknown).
//
// Route gates: every event is its own route with a row in routeGateTable (hookGate): core,
// the proposer role (a hook runs for an agent session; no non-GET route grants reader),
// served in read-only mode (a capture stores only the caller's own output; the session
// ledger records nothing, so no boundary work runs: no snapshot, capsule or mined
// candidate). The workspace comes from the X-Xmustard-Workspace header or the
// registered root holding the event's cwd, and is then checked like a workspace path:
// served by this deployment and in the token's scope.

// defaultHookBudget is how long a hook answer may take; past it the route answers empty.
const defaultHookBudget = 180 * time.Millisecond

const (
	hookSearchHits   = 5   // index hits a search pre-hook injects
	hookMemoryLimit  = 5   // memories one pre-hook considers
	hookWatchPaths   = 128 // watchPaths an answer carries
	hookTriggerLimit = 16  // memories one prompt may trigger
	hookQueueDepth   = 256 // queued enqueue-mode events before new ones are dropped
	// hookMaxCandidates is AdmitMemory's bound on one admission; later candidates wait
	// for a later push.
	hookMaxCandidates = 64
	// hookProjectionChars budgets SessionStart's standing memory: a fixed budget under
	// the additionalContext cap (the ground line takes a few hundred characters), so the
	// projection cached at it serves every session until memory changes.
	hookProjectionChars = 9000
)

// hookBudget is XMUSTARD_HOOK_BUDGET_MS, from 20 to 5,000 ms (default 180).
func hookBudget() time.Duration {
	if ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv("XMUSTARD_HOOK_BUDGET_MS"))); err == nil && ms >= 20 && ms <= 5000 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultHookBudget
}

// The resident-worker reads a hook makes, and the identity drop of a removed worktree;
// tests replace them.
var (
	hookSearch     = workspaceops.HookSearch
	hookSyntax     = workspaceops.HookSyntaxCheck
	hookForgetRoot = workspaceops.ForgetRoot
)

// hookStats counts hook answers since start (hookUsage, /api/health); drained counts
// the queued events the consumer has handled.
var hookStats struct {
	answered, timedOut, failOpen, enqueued, dropped, drained atomic.Int64
}

// hookUsage is the operator's view of the hook service in /api/health: answers given,
// answers the budget cut to empty, bodies it could not read (empty answers too), and
// the queued events and those dropped because the queue was full.
func hookUsage() map[string]int64 {
	usage := map[string]int64{"answered": hookStats.answered.Load(), "timed_out": hookStats.timedOut.Load(),
		"fail_open": hookStats.failOpen.Load(), "enqueued": hookStats.enqueued.Load(), "dropped": hookStats.dropped.Load()}
	for k, v := range workspaceops.LedgerUsage() {
		usage["ledger_"+k] = v
	}
	return usage
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
	client hooks.Client
	ev     hooks.Event
	in     hooks.Input
	body   []byte
	caller string           // principal id; "" in open mode
	key    hooks.SessionKey // the context's steering state
	ws     workspaceops.ResolvedWorkspace
	bound  bool // ws was resolved and passed the scope checks
	// recallRefused: this caller may not use recall on this deployment, so no memory
	// reaches this answer (grounding's projection and every push). A hook route names
	// no Tool in the gate table, so the middleware's tool_disabled branch never fires
	// for one and this field is where a deployment's disabled-tool switch is applied to
	// a hook's memory. It is the predicate whoami and tools/list use, so a hook can
	// never reach memory a recall call would be refused. The hook still answers: the
	// refusal is the memory section, not the route, because watchPaths, the restored
	// snapshot and the prior session's handoff are the caller's own session state.
	recallRefused bool
}

// hookResult is what a handler produced. offered maps each memory a push considered to
// the text whose presence in the answer marks it seen in this context (push). after
// runs once the answer has been written (recording an outcome, say), so it never
// delays the client. It gets the request with a context of its own, bounded by
// hookAfterTimeout: the client may close the connection as soon as it has read the
// answer, which cancels the request's context.
type hookResult struct {
	context string
	updated json.RawMessage
	watch   []string
	offered map[string]string
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

// hookQueued is an Enqueue-mode event waiting for the consumer. ledger is its session in
// the ledger's terms and text its trigger or reason; ledger is nil when the event names
// no session or no workspace in scope, and in read-only mode (ledgerBase).
type hookQueued struct {
	event, text string
	key         hooks.SessionKey
	ledger      *workspaceops.LedgerSession
}

// hookEnqueued is what the consumer does with each Enqueue-mode event: its effect on the
// steering state (SessionEnd drops the session's contexts, SubagentStop the
// subagent's, PostCompact forgets what was pushed into the compacted context), and the
// session-ledger boundary it records (WS-33: PreCompact takes the compaction snapshot,
// Stop mines candidates, SessionEnd ends the session with its mining, transcript
// reference, automatic capsule and snapshot). The ledger writer does the boundary's
// work after the answer (Claude Code gives SessionEnd hooks a shared 1.5 s budget).
var hookEnqueued = map[string]struct {
	effect   func(*hooks.Sessions, hooks.SessionKey)
	boundary string
}{
	"PreCompact":   {boundary: workspaceops.BoundaryPreCompact},
	"PostCompact":  {(*hooks.Sessions).Compacted, workspaceops.BoundaryPostCompact},
	"Stop":         {boundary: workspaceops.BoundaryTurnEnd},
	"SubagentStop": {endSubagent, workspaceops.BoundarySubagentStop},
	"SessionEnd":   {(*hooks.Sessions).End, workspaceops.BoundarySessionEnd},
}

// endSubagent drops a subagent's context. Claude Code also fires SubagentStop for its
// own internal agents; one without an agent id must not end the main thread's state.
func endSubagent(s *hooks.Sessions, key hooks.SessionKey) {
	if key.Agent == "" {
		return
	}
	s.End(key)
}

func newHookServer(store *evidence.Store) *hookServer {
	s := &hookServer{store: store, reg: evidence.DefaultRegistry(), sessions: hooks.NewSessions(hooks.DefaultLimits, time.Now),
		queue: make(chan hookQueued, hookQueueDepth)}
	go s.drain()
	return s
}

// registerHookRoutes mounts one route per client event.
func registerHookRoutes(mux routeRegistrar, store *evidence.Store) *hookServer {
	s := newHookServer(store)
	for _, client := range hooks.Clients {
		for _, ev := range client.Events {
			mux.HandleFunc("POST /api/hooks/"+client.Name+"/"+ev.Name, s.handle(client, ev))
		}
	}
	return s
}

func (s *hookServer) handle(client hooks.Client, ev hooks.Event) http.HandlerFunc {
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
		call := &hookCall{r: r, client: client, ev: ev, in: in, body: body, caller: caller, key: hooks.Key(caller, in.SessionID, in.AgentID),
			recallRefused: toolDisabledFor(postureFrom(r), callerOf(r), "recall")}
		if ev.Mode == hooks.Enqueue {
			s.enqueue(call)
			writeHookAnswer(w, nil)
			return
		}
		ctx, cancel := context.WithTimeout(budget.WithoutHeavyWait(r.Context()), hookBudget())
		defer cancel()
		type answered struct {
			out       *hooks.Reply
			delivered []string
			after     func(*http.Request)
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
			a.out, a.delivered, a.after = s.answer(ctx, call)
		}()
		var a answered
		select {
		case a = <-done:
			if writeHookAnswer(w, a.out) == nil {
				s.sessions.MarkInjected(call.key, a.delivered)
			}
		case <-ctx.Done(): // nothing is marked pushed: the answer is empty
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

// answer runs the event's handler and writes its answer in the client's schema (nil
// when the budget ran out or there is nothing to say) and the memory ids it delivers.
func (s *hookServer) answer(ctx context.Context, c *hookCall) (*hooks.Reply, []string, func(*http.Request)) {
	c.bound = s.resolve(c)
	res := hookHandlers[c.ev.Name](s, ctx, c)
	if ctx.Err() != nil {
		return nil, nil, res.after
	}
	if c.ev.Context {
		res.context = hooks.Compose(res.context, s.sessions.BusyNote(c.key))
	}
	hookStats.answered.Add(1)
	out := c.client.Reply(c.ev, hooks.Result{Context: res.context, Updated: res.updated, Watch: res.watch})
	return out, delivered(out, res.offered), res.after
}

// delivered lists the offered memory ids whose text the answer carries: a frame or a
// note that was composed out, or cut by the context cap, delivers nothing.
func delivered(out *hooks.Reply, offered map[string]string) []string {
	if out == nil {
		return nil
	}
	var ids []string
	for id, text := range offered {
		if text != "" && strings.Contains(out.Context, text) {
			ids = append(ids, id)
		}
	}
	return ids
}

// writeHookAnswer writes an answer: 200 with an empty body when there is nothing to say.
func writeHookAnswer(w http.ResponseWriter, out *hooks.Reply) error {
	var body []byte
	if out != nil {
		body = out.Body
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return err
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
	s.record(c, workspaceops.LedgerEvent{Kind: workspaceops.LedgerSessionStart, Text: c.in.Source})
	return s.grounding(ctx, c, c.ev.WatchPaths, s.sessionContext(ctx, c)...)
}

// subagentStart grounds a subagent: its hooks carry its agent_id, so it gets a
// context of its own (hooks.SessionKey) and the core tier is pushed into it too.
func (s *hookServer) subagentStart(ctx context.Context, c *hookCall) hookResult {
	s.record(c, workspaceops.LedgerEvent{Kind: workspaceops.LedgerSubagentStart, Text: c.in.AgentType})
	return s.grounding(ctx, c, false)
}

// grounding is SessionStart's and SubagentStart's context: ground's spawn-free part,
// the session parts (SessionStart: the snapshot or handoff, sessionContext), and the
// standing-memory projection (WS-31: the human-approved core tier, each entry
// pinned to its approved served revision, and the deferred index, stamped with the
// memory revision), and (SessionStart) the files memory is anchored to as watchPaths,
// so FileChanged reports their edits. The projection is pushed once per context: when
// every core entry it carries was already offered here (a resume), only the ground line
// is sent. A projection that cannot be compiled pushes no memory (fail closed).
// When the deployment serves this caller no recall (hookCall.recallRefused) the
// projection is left out and the watch list is kept: watchPaths names the files memory is
// anchored to, which the grounding index answers from the store's anchors alone.
func (s *hookServer) grounding(ctx context.Context, c *hookCall, watch bool, session ...string) hookResult {
	if !c.bound {
		return hookResult{}
	}
	var g *workspaceops.SessionGrounding
	if c.recallRefused {
		// ground's spawn-free part without its memory section: the runs it reports are
		// run outcomes, not memory, so a deployment that withholds recall still gets
		// them and the ground line stays whole.
		g = workspaceops.HookGroundingNoMemory(ctx, dataDir(), c.ws.WorkspaceID)
	} else {
		g = workspaceops.HookGrounding(ctx, dataDir(), c.ws.WorkspaceID, c.caller)
	}
	res := hookResult{context: hooks.Compose(append([]string{groundLine(c.ws, g)}, session...)...)}
	if !c.recallRefused {
		budget := min(hooks.Room(res.context), hookProjectionChars)
		if p, err := workspaceops.CoreProjection(ctx, dataDir(), c.ws.WorkspaceID, budget); err == nil && p.Text != "" {
			offered := p.ProjectionOffered()
			if ids := slices.Collect(maps.Keys(offered)); len(ids) == 0 || len(s.sessions.Unseen(c.key, ids)) > 0 {
				res.context, res.offered = oneNotice(hooks.Compose(res.context, p.Text)), offered
			}
		}
	}
	if idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID); err == nil && watch {
		res.watch = watchPaths(c.ws, idx.Anchors)
	}
	return res
}

// hookHandoffs are the session states SessionStart restores, by source: after a
// compaction the session's own snapshot (PAR-HAR-04), at a new session the prior
// session's handoff, from any client (PAR-HAR-05). resume and clear restore nothing:
// a resumed session has its history, and clear asked for a fresh context.
var hookHandoffs = map[string]struct {
	own, prior bool
	header     string
}{
	"compact": {own: true, header: "[xmustard] restored after compaction: this session's state at the compaction boundary"},
	"startup": {prior: true, header: "[xmustard] handoff from the prior session"},
}

// sessionContext is SessionStart's session parts: the line naming this session's
// execution key, and the restored snapshot or the prior session's handoff with its
// capsule, each scanned for instruction patterns and framed as data (WS-56). A flagged
// part is withheld with a note naming the ground call that returns it labeled.
func (s *hookServer) sessionContext(ctx context.Context, c *hookCall) []string {
	if !c.bound || c.in.SessionID == "" || c.in.AgentID != "" {
		return nil
	}
	key := workspaceops.ExecutionKey(c.client.Name, c.in.SessionID, "")
	parts := []string{fmt.Sprintf("[xmustard session] this session is %s. Before it ends, remember(kind=\"handoff\", session_id=%q, "+
		"content=<capsule JSON>, paths=...) leaves a handoff for the next session; ground session_id=%s restores its state.", key, key, key)}
	mode, ok := hookHandoffs[c.in.Source]
	if !ok {
		return parts
	}
	h, err := workspaceops.HookSessionHandoff(ctx, dataDir(), c.ws.WorkspaceID, c.caller, key, mode.own, mode.prior)
	if err != nil || h == nil {
		return parts
	}
	caveat := ""
	if h.Caveat != "" {
		caveat = "; " + h.Caveat
	}
	header := fmt.Sprintf("%s (%s from %s, %s, last seen %s%s): derived from the session ledger, not verified memory.",
		mode.header, h.FromSession, cmp.Or(h.Client, "an unknown client"), h.Status, h.LastSeenAt, caveat)
	parts = append(parts, framedPart(header, "snapshot", h.FromSession, "derived", h.SnapshotText()))
	if c := h.Capsule; c != nil {
		parts = append(parts, framedPart("[xmustard] its handoff capsule "+c.ID+":", "handoff", c.ID, c.Trust(), c.Gist()))
	}
	return parts
}

// framedPart frames text as data under a header, or withholds it when the scan flags it
// (the ground call that returns it labeled is session_handoff's).
func framedPart(header, kind, id, trust, text string) string {
	if rep := injection.Scan(text); !rep.Clean() {
		return fmt.Sprintf("%s withheld: it holds instruction-like text (%s); ground returns it labeled.",
			strings.TrimSuffix(header, ":"), strings.Join(rep.Flags, ", "))
	}
	return header + "\n" + injection.Notice + "\n" + injection.Frame(injection.Block{Kind: kind, ID: id, Trust: trust, Text: text})
}

// oneNotice keeps the first injection notice of a composed context: each framed part
// carries one so that it stands alone, and a context needs it once.
func oneNotice(s string) string {
	first := strings.Index(s, injection.Notice)
	if first < 0 {
		return s
	}
	end := first + len(injection.Notice)
	rest := strings.ReplaceAll(s[end:], "\n\n"+injection.Notice+"\n\n", "\n\n")
	return s[:end] + strings.ReplaceAll(rest, "\n"+injection.Notice+"\n", "\n")
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
// "trigger-<keyword>", workspaceops.TriggerTagPrefix). A caller this deployment serves no
// recall to records the prompt in the ledger and stops there: the trigger index is read
// from the memory store, so reading it is the memory work the refusal is for, and the head
// line names only keywords a trigger would have matched.
func (s *hookServer) userPrompt(ctx context.Context, c *hookCall) hookResult {
	if !c.bound || strings.TrimSpace(c.in.Prompt) == "" {
		return hookResult{}
	}
	s.record(c, workspaceops.LedgerEvent{Kind: workspaceops.LedgerPrompt, Text: c.in.Prompt})
	if c.recallRefused {
		return hookResult{}
	}
	idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID)
	if err != nil || len(idx.Triggers) == 0 {
		return hookResult{}
	}
	keywords, ids := triggered(idx.Triggers, c.in.Prompt)
	head := "[xmustard] memory triggered by keywords in the prompt: " + strings.Join(keywords, ", ")
	memory, offered := s.push(ctx, c, injection.SurfaceHook, ids, nil, triggerRecall(keywords), hooks.Room(head))
	if memory == "" {
		return hookResult{}
	}
	return hookResult{context: hooks.Compose(head, memory), offered: offered}
}

// triggerRecall is the recall call that lists the memories these keywords trigger
// (recall's tags match any of the tags given).
func triggerRecall(keywords []string) string {
	tags := make([]string, len(keywords))
	for i, k := range keywords {
		tags[i] = strconv.Quote(workspaceops.TriggerTagPrefix + k)
	}
	return "recall(tags=[" + strings.Join(tags, ", ") + "])"
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
	memory, offered := s.memoryContext(ctx, c, q, nil)
	wg.Wait()
	return hookResult{context: hooks.Compose(memory, hooks.RenderHits(q, hits, hookSearchHits), nudge), offered: offered}
}

// fileContext is a Read, Edit or apply_patch's context: the memories bound to its
// files. Before an edit it also keeps the files' syntax errors as the baseline of the
// post-edit delta.
func (s *hookServer) fileContext(ctx context.Context, c *hookCall) hookResult {
	rels := c.targets()
	if len(rels) == 0 {
		return hookResult{}
	}
	var wg sync.WaitGroup
	if hooks.ClassOf(c.in.ToolName) == hooks.ToolEdit {
		wg.Go(func() { s.baseline(ctx, c, rels) })
	}
	memory, offered := s.memoryContext(ctx, c, "", rels)
	wg.Wait()
	return hookResult{context: memory, offered: offered}
}

// targets are the call's files inside the workspace root, relative to it.
func (c *hookCall) targets() []string {
	var rels []string
	for _, p := range c.in.Targets() {
		if rel := workspaceops.RelativeToRoot(c.ws, p); rel != "" {
			rels = append(rels, rel)
		}
	}
	return rels
}

// moves maps each file the call renames a file to onto that file, both inside the
// workspace root and relative to it.
func (c *hookCall) moves() map[string]string {
	out := map[string]string{}
	for to, from := range c.in.Moves() {
		if rt, rf := workspaceops.RelativeToRoot(c.ws, to), workspaceops.RelativeToRoot(c.ws, from); rt != "" && rf != "" {
			out[rt] = rf
		}
	}
	return out
}

// memoryContext pushes the served memories recall ranks for a pattern or a path; they
// come first in a pre-hook's context. A caller this deployment serves no recall to ranks
// nothing: HookMemories reads the memory store, so the refusal comes before it.
func (s *hookServer) memoryContext(ctx context.Context, c *hookCall, query string, paths []string) (string, map[string]string) {
	if c.recallRefused {
		return "", nil
	}
	cands, err := workspaceops.HookMemories(ctx, dataDir(), c.ws.WorkspaceID, c.caller, query, paths, hookMemoryLimit)
	if err != nil || len(cands) == 0 {
		return "", nil
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
		quoted := make([]string, len(paths))
		for i, p := range paths {
			quoted[i] = strconv.Quote(p)
		}
		recall = "recall(paths=[" + strings.Join(quoted, ", ") + "])"
	}
	return s.push(ctx, c, injection.SurfaceHook, ids, stale, recall, hooks.MaxContextChars)
}

// hookNoteRoom is the room a push keeps for its withheld note.
const hookNoteRoom = 512

// hookTooLarge is the withheld reason of an admitted memory that did not fit in the
// context left in its answer.
const hookTooLarge = "too_large"

// push admits candidate memories through the injection policy of a pushed surface
// (workspaceops.AdmitMemory) and renders, within room characters, the injection notice
// and each admitted memory framed as data (a stale one with its label), then one note
// counting the withheld ones, by policy reason or too_large, with the recall call that
// shows them. A candidate is considered once per context (until compaction): offered
// maps each one to the text whose presence in the answer written marks it seen, its
// frame or the note, so a memory whose frame or note never reached the client is
// considered again. An admission error pushes nothing (fail closed). A caller this
// deployment serves no recall to pushes nothing either: the refused push is empty, so the
// answer carries no memory and no note counting what was withheld.
func (s *hookServer) push(ctx context.Context, c *hookCall, surface injection.Surface, ids []string, stale map[string][]string, recall string, room int) (string, map[string]string) {
	if c.recallRefused {
		return "", nil
	}
	ids = s.sessions.Unseen(c.key, ids)
	ids = ids[:min(len(ids), hookMaxCandidates)]
	if len(ids) == 0 {
		return "", nil
	}
	m, err := workspaceops.AdmitMemory(ctx, dataDir(), c.ws.WorkspaceID, surface, ids)
	if err != nil {
		return "", nil
	}
	offered := map[string]string{}
	var frames, reasons, noted []string
	left := room - hookNoteRoom - hooks.Size(injection.Notice)
	for _, a := range m.Admitted {
		frame := injection.Frame(a.Block())
		if paths, ok := stale[a.ID]; ok {
			frame += "\n" + hooks.StaleNote(map[string][]string{a.ID: paths})
		}
		if size := hooks.Size(frame); size <= left {
			left -= size
			frames = append(frames, frame)
			offered[a.ID] = frame
			continue
		}
		reasons, noted = append(reasons, hookTooLarge), append(noted, a.ID)
	}
	for _, w := range m.Withheld {
		reasons, noted = append(reasons, w.Reason), append(noted, w.ID)
	}
	note := hooks.WithheldNote(reasons, recall)
	for _, id := range noted {
		offered[id] = note
	}
	if len(frames) > 0 {
		frames = append([]string{injection.Notice}, frames...)
	}
	return hooks.ComposeWithin(room, append(frames, note)...), offered
}

// baseline keeps the files' syntax errors before an edit (one worker read for all of
// them). A file that does not exist yet has an empty baseline: every error of the new
// file is new. A file an apply_patch renames keeps its errors: the destination, which
// does not exist yet, takes its source's baseline.
func (s *hookServer) baseline(ctx context.Context, c *hookCall, rels []string) {
	raw, err := hookSyntax(ctx, c.ws, rels)
	files, perr := hooks.ParseSyntax(raw)
	if err != nil || perr != nil {
		return
	}
	before := map[string][]string{}
	for _, f := range files {
		switch f.Status {
		case "checked":
			before[f.Path] = f.Keys()
		case "unreadable":
			before[f.Path] = nil
		}
	}
	for to, from := range c.moves() {
		if errs, ok := before[from]; ok {
			before[to] = errs
		}
	}
	for rel, errs := range before {
		s.sessions.SetBaseline(c.key, rel, errs)
	}
}

func (s *hookServer) postToolUse(ctx context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	switch tool := c.in.ToolName; {
	case hooks.ClassOf(tool) == hooks.ToolEdit:
		s.recordCall(c, workspaceops.LedgerOK, nil)
		return s.afterEdit(ctx, c)
	case hooks.Captured(tool):
		return s.afterCapture(ctx, c)
	}
	s.recordCall(c, workspaceops.LedgerOK, nil)
	return hookResult{}
}

// --- the session ledger ---

// ledgerFileKinds are the rows of the file tools' calls that succeeded (WS-33); a failed
// one, like any other call, is a tool_call with its status (a failed Edit changed
// nothing), and an xMustard remember or verify call is its memory action
// (hooks.OwnMemoryCall).
var ledgerFileKinds = map[hooks.ToolClass]string{
	hooks.ToolRead: workspaceops.LedgerFileRead, hooks.ToolEdit: workspaceops.LedgerFileModified,
}

// hookMaxChangedRows bounds the file_modified rows one shell call appends.
const hookMaxChangedRows = 16

// record queues one ledger row for the call's session (never waiting). A call with no
// session id, or not bound to a workspace in scope, records nothing.
func (s *hookServer) record(c *hookCall, ev workspaceops.LedgerEvent) {
	if base := ledgerBase(c); base != nil {
		ev.LedgerSession = *base
		workspaceops.RecordSession(dataDir(), ev)
	}
}

// ledgerBase is the call's session in the ledger's terms: nil when it has none, and in
// read-only mode, where hooks change no shared state: no ledger row, and no boundary
// work (snapshot, capsule, mined candidate).
func ledgerBase(c *hookCall) *workspaceops.LedgerSession {
	if !c.bound || c.in.SessionID == "" || postureFrom(c.r).readOnly() {
		return nil
	}
	return &workspaceops.LedgerSession{WorkspaceID: c.ws.WorkspaceID, Root: c.ws.Root, Client: c.client.Name,
		Session: c.in.SessionID, Agent: c.in.AgentID, Principal: c.caller, Transcript: c.in.TranscriptPath}
}

// recordCall records a tool call: its kind (ledgerFileKinds), its arguments' digest,
// the file it read or changed, the handle of its captured output, and for a shell call
// the command (redacted) and a failure's signature.
func (s *hookServer) recordCall(c *hookCall, status string, obs *evidence.ObservationResult) {
	ev := workspaceops.LedgerEvent{Tool: c.in.ToolName, Args: hooks.ArgsDigest(c.in, c.body), Status: status,
		Path: workspaceops.RelativeToRoot(c.ws, c.in.ToolInput.TargetPath())}
	if obs != nil && obs.Delivery != nil {
		ev.Handle, ev.ExpiresAt = obs.Handle, obs.ExpiresAt
		if code := obs.Facts.ExitCode; code != nil && *code != 0 {
			ev.Status = workspaceops.LedgerError
		}
	}
	ev.Kind = workspaceops.LedgerToolCall
	if kind, ok := ledgerFileKinds[hooks.ClassOf(c.in.ToolName)]; ok && ev.Status == workspaceops.LedgerOK {
		ev.Kind = kind
	}
	if kind, id, ok := hooks.OwnMemoryCall(c.in.ToolName, c.body); ok {
		ev.Kind, ev.EntryID = kind, id
	}
	if hooks.ClassOf(c.in.ToolName) == hooks.ToolShell {
		ev.Text = c.in.ToolInput.Command
	}
	if ev.Status == workspaceops.LedgerError {
		ev.Signature = failureSignature(c.in.Error, obs)
	}
	s.record(c, ev)
}

// failureSignature names a failure: the failing tests the capture parsed, else the
// first line of the error text.
func failureSignature(errText string, obs *evidence.ObservationResult) string {
	if obs != nil && len(obs.Facts.FailingTests) > 0 {
		tests := obs.Facts.FailingTests
		return strings.Join(tests[:min(len(tests), 3)], ", ")
	}
	for _, line := range strings.Split(errText, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "Exit code") {
			return line
		}
	}
	return strings.TrimSpace(errText)
}

// afterEdit feeds the dirty set and reports the syntax errors the edit introduced, per
// file (an apply_patch may change several).
func (s *hookServer) afterEdit(ctx context.Context, c *hookCall) hookResult {
	rels := c.targets()
	if len(rels) == 0 {
		return hookResult{}
	}
	workspaceops.NoteChangedPaths(ctx, c.ws.Scope, rels)
	type baseline struct {
		errs []string
		had  bool
	}
	before := make(map[string]baseline, len(rels)) // taken even if the check fails: a baseline serves one edit
	for _, rel := range rels {
		errs, had := s.sessions.TakeBaseline(c.key, rel)
		before[rel] = baseline{errs, had}
	}
	raw, err := hookSyntax(ctx, c.ws, rels)
	files, perr := hooks.ParseSyntax(raw)
	if err != nil || perr != nil {
		return hookResult{}
	}
	var notes []string
	for _, f := range files {
		if b := before[f.Path]; f.Status == "checked" {
			notes = append(notes, hooks.SyntaxNote(f.Path, hooks.NewErrors(b.errs, f.Errors), b.had, f.Truncated))
		}
	}
	return hookResult{context: hooks.Compose(notes...)}
}

// afterCapture replaces a native tool's output with its reduction when it was reduced
// and the shape matched; otherwise the client keeps its output, with the shape notice
// as context. A shell call also feeds the dirty set and may earn the post-git notice.
func (s *hookServer) afterCapture(ctx context.Context, c *hookCall) hookResult {
	var res hookResult
	var parts []string
	obs := s.capture(ctx, c)
	s.recordCall(c, workspaceops.LedgerOK, obs)
	if obs != nil {
		switch obs.Shape.Mode {
		case evidence.ShapeReplace:
			res.updated = obs.Shape.Payload
		case evidence.ShapeFallback, evidence.ShapeSizeError:
			parts = append(parts, obs.Shape.Notice)
		}
		res.after = func(r *http.Request) { recordCaptureOutcome(r, s.store, c.ws.WorkspaceID, false, obs) }
	}
	if hooks.ClassOf(c.in.ToolName) == hooks.ToolShell {
		parts = append(parts, s.afterShell(ctx, c))
	}
	res.context = hooks.Compose(parts...)
	return res
}

// afterShell feeds the dirty set from the command's changed files (Claude Code's
// bashEditDiff) and gives the post-git notice when the command moved HEAD.
func (s *hookServer) afterShell(ctx context.Context, c *hookCall) string {
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
		workspaceops.NoteChangedPaths(ctx, c.ws.Scope, rels)
		for _, rel := range rels[:min(len(rels), hookMaxChangedRows)] {
			s.record(c, workspaceops.LedgerEvent{Kind: workspaceops.LedgerFileModified, Tool: c.in.ToolName, Path: rel})
		}
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

// capture stream-decodes the hook body into the spool through the capture redactor
// (fail-closed as on the capture route: a redactor failure retains nothing), reduces
// it by tool family and shapes it for the client. nil when capture is not possible
// now (no redactor, memory refused, the output of a secret path, a redactor failure,
// another error): the client keeps its output. Only a refusal for memory counts as a
// busy skip; a secret path or a redactor failure is not the daemon being busy.
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
		Format: evidence.HookFormat(c.client.Format), Body: bytes.NewReader(c.body), Meta: evidence.CaptureMeta{Client: c.client.Name},
		Redact: func(w io.Writer) evidence.StreamRedactor { return &failClosed{next: redact(w)} },
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
	if !c.bound {
		return hookResult{}
	}
	var obs *evidence.ObservationResult
	if hooks.Captured(c.in.ToolName) {
		obs = s.capture(ctx, c)
	}
	s.recordCall(c, workspaceops.LedgerError, obs)
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

// cwdChanged replaces Claude Code's dynamic watch list with the files memory is
// anchored to in the new directory's workspace. The list is always sent: an empty one
// clears it, so a directory outside any workspace the caller may use (or an unreadable
// memory store) leaves nothing of the previous workspace watched.
func (s *hookServer) cwdChanged(ctx context.Context, c *hookCall) hookResult {
	res := hookResult{watch: []string{}}
	if !c.bound {
		return res
	}
	if idx, err := workspaceops.ReadHookMemoryIndex(ctx, dataDir(), c.ws.WorkspaceID); err == nil {
		res.watch = append(res.watch, watchPaths(c.ws, idx.Anchors)...)
	}
	return res
}

// fileChanged feeds the dirty set: the watcher's pending batch (NoteChangedPaths).
func (s *hookServer) fileChanged(ctx context.Context, c *hookCall) hookResult {
	if !c.bound {
		return hookResult{}
	}
	if rel := workspaceops.RelativeToRoot(c.ws, c.in.FilePath); rel != "" {
		workspaceops.NoteChangedPaths(ctx, c.ws.Scope, []string{rel})
	}
	return hookResult{}
}

// worktreeRemove forgets a removed worktree's cached identity, when the worktree is
// the caller's workspace root or lies under it (Claude Code's worktrees live under
// .claude/worktrees/ of the repository): like every handler, it acts only inside the
// workspace that passed the scope checks.
func (s *hookServer) worktreeRemove(_ context.Context, c *hookCall) hookResult {
	p := filepath.Clean(c.in.WorktreePath)
	if !c.bound || !filepath.IsAbs(p) {
		return hookResult{}
	}
	if p == filepath.Clean(c.ws.Root) || workspaceops.RelativeToRoot(c.ws, p) != "" {
		hookForgetRoot(p)
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

// enqueue queues an Enqueue-mode event with its ledger view. Binding the workspace is
// in-memory (the registry and the scope checks), so the answer is not delayed.
func (s *hookServer) enqueue(c *hookCall) {
	c.bound = s.resolve(c)
	q := hookQueued{event: c.ev.Name, text: cmp.Or(c.in.Trigger, c.in.Reason), key: c.key, ledger: ledgerBase(c)}
	select {
	case s.queue <- q:
		hookStats.enqueued.Add(1)
	default:
		hookStats.dropped.Add(1)
	}
}

// drain applies each queued event's steering effect and records its ledger boundary;
// the boundary's work runs on the ledger writer, not here.
func (s *hookServer) drain() {
	for q := range s.queue {
		e := hookEnqueued[q.event]
		if e.effect != nil {
			e.effect(s.sessions, q.key)
		}
		if q.ledger != nil {
			workspaceops.RecordBoundary(dataDir(), e.boundary, workspaceops.LedgerEvent{LedgerSession: *q.ledger, Text: q.text})
		}
		hookStats.drained.Add(1)
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

// listenHookSocket listens on path in a directory only this user owns and can enter
// (transport.CheckDir, the check the client makes before it dials). Whatever already
// sits at path is replaced only when it is a stale socket this user owns: a socket
// another live daemon serves, another user's file and a file that is not a socket are
// left alone, and the socket is not served.
func listenHookSocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := transport.CheckDir(dir); err != nil {
		return nil, err
	}
	switch _, err := os.Lstat(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := transport.CheckSocket(path); err != nil {
			return nil, fmt.Errorf("not replacing %s: %w", path, err)
		}
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, errors.New("another daemon serves it")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
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
