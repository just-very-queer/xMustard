package workspaceops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
)

// Session ledger (WS-33; PAR-PROV-06, PAR-PROV-07): a durable log per workspace, client,
// session and principal of what a session did: tool calls (tool, arguments digest,
// evidence handle, status), files read and modified, prompts, compactions, and the
// memories it proposed and verified. The rows live in govstore's session_events, one
// session row per workspace and execution key (<client>:<session>[:agent:<agent>]):
// the row id derives from both (sessionRow), and the key is kept as the row's
// execution_key, so a session whose events reach two workspaces has a row in each.
// Raw observations, derived records (a compaction snapshot, an automatic handoff
// capsule) and verified records keep their record class.
//
// One coordinated writer per data dir appends the rows: callers queue an event
// (RecordSession) or a derived job (RunSessionJob) and return at once, so a hook never
// waits on SQLite, and Claude Code's shared 1.5 s SessionEnd budget is never spent on
// ledger work. The writer runs only while there is work: it drains the bounded queue in
// batches (one transaction each) and exits when it is empty. A full queue drops the new
// operation and counts it. A job runs after every event queued before it, so a
// snapshot built at PreCompact sees the whole session up to the compaction.

// Ledger kinds: the closed set of rows the ledger writes.
const (
	LedgerSessionStart   = "session_start"
	LedgerSessionEnd     = "session_end"
	LedgerSubagentStart  = "subagent_start"
	LedgerSubagentStop   = "subagent_stop"
	LedgerPrompt         = "prompt"
	LedgerToolCall       = "tool_call"
	LedgerFileRead       = "file_read"
	LedgerFileModified   = "file_modified"
	LedgerMemoryProposed = "memory_proposed"
	LedgerMemoryVerified = "memory_verified"
	LedgerCompaction     = "compaction"
	LedgerTurnEnd        = "turn_end"
	LedgerTranscript     = "transcript_ref"
	// LedgerSnapshot is a derived compaction snapshot (session_snapshot.go).
	LedgerSnapshot = "snapshot"
	// LedgerHandoff is a handoff capsule (handoff_capsule.go): raw when an agent wrote
	// it, derived when the ledger built it at SessionEnd.
	LedgerHandoff = "handoff"
)

// Ledger statuses of a tool call.
const (
	LedgerOK    = "ok"
	LedgerError = "error"
)

// activityKinds are the observation rows a session accumulates; the ledger bound
// counts them together.
var activityKinds = []string{LedgerSessionStart, LedgerSessionEnd, LedgerSubagentStart, LedgerSubagentStop, LedgerPrompt,
	LedgerToolCall, LedgerFileRead, LedgerFileModified, LedgerMemoryProposed, LedgerMemoryVerified, LedgerCompaction,
	LedgerTurnEnd, LedgerTranscript}

// handoffKeep is how many capsules a session keeps.
const handoffKeep = 8

// ledgerBounds bound one session's rows by kind: the newest keep of each group stay.
// Age retention (govstore's retention classes) removes the rest over time.
var ledgerBounds = []struct {
	kinds []string
	keep  int
}{
	{activityKinds, 2048},
	{[]string{LedgerSnapshot}, 4},
	{[]string{LedgerHandoff}, handoffKeep},
}

// LedgerSession is whose session a row belongs to, in client-neutral terms: the hook
// service fills it from a Claude Code event, and another client's adapter from its own.
type LedgerSession struct {
	WorkspaceID string
	// Root is the workspace root; the session's first row reads its branch and HEAD
	// from the repository files (no git process).
	Root string
	// Client, Session and Agent form the execution key (PAR-PROV-07); Agent is "" on
	// the main thread. Principal is the authenticated caller ("" in open mode). They are
	// audit metadata, never authorization.
	Client, Session, Agent, Principal string
	// Transcript is the client's own transcript file (Claude Code's transcript_path).
	// At SessionEnd a transcript_ref row keeps its path and the digest of its content
	// (transcriptJob); no other row keeps it.
	Transcript string
}

// LedgerEvent is one observation for the ledger.
type LedgerEvent struct {
	LedgerSession
	Kind string
	Tool string
	// Args is the digest of the call's arguments, never the arguments.
	Args    string
	Handle  string
	Status  string
	Path    string
	EntryID string
	// Text is a short excerpt kept with the row, redacted and cut to ledgerTextMax: a
	// prompt, a shell command, a compaction trigger or an end reason.
	Text string
	// Signature names a failure: the failing tests, or the first line of the error.
	Signature string
	// ExpiresAt is when Handle's original expires.
	ExpiresAt string
}

// key is the session's execution key.
func (ls LedgerSession) key() string { return ExecutionKey(ls.Client, ls.Session, ls.Agent) }

// sessionRow is the govstore id of a session's row in a workspace: a digest of the
// workspace and the execution key, so one client session seen in two workspaces (a cd
// into another repository, nested roots) keeps a row, and its rows, in each. It is
// always a valid id, whatever the lengths of its parts.
func sessionRow(workspaceID, key string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + key))
	return "s-" + hex.EncodeToString(sum[:16])
}

// ledgerData is the JSON a row keeps besides its columns.
type ledgerData struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// ledgerTextMax bounds the excerpt a row keeps, in bytes.
const ledgerTextMax = 240

// ExecutionKey is the session row id of a client session and, inside it, a subagent:
// <client>:<session> or <client>:<session>:agent:<agent> (PAR-PROV-07). A component
// that is not a plain id (a colon, a path, "..", over 64 bytes) is replaced by a digest
// of it, so the key is always a valid, collision-resistant id.
func ExecutionKey(client, session, agent string) string {
	key := keyPart(client) + ":" + keyPart(session)
	if agent != "" {
		key += ":agent:" + keyPart(agent)
	}
	return key
}

var keyPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// executionKeyPattern matches an execution key as ExecutionKey writes it.
var executionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}:[A-Za-z0-9_.@-]{1,64}(:agent:[A-Za-z0-9_.@-]{1,64})?$`)

func keyPart(s string) string {
	if keyPartPattern.MatchString(s) && !strings.Contains(s, "..") {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "h-" + hex.EncodeToString(sum[:8])
}

// ledgerPrincipal is the principal a row is written as: the open-mode identity when
// the API runs without credentials.
func ledgerPrincipal(p string) string { return cmp.Or(strings.TrimSpace(p), OpenModeIdentity) }

// LedgerExcerpt is text as a row keeps it: secrets redacted, then cut to ledgerTextMax
// bytes on a character boundary, whitespace runs folded.
func LedgerExcerpt(s string) string {
	s, _ = redact.Default().String(strings.Join(strings.Fields(s), " "))
	return cutBytes(s, ledgerTextMax)
}

// cutBytes cuts s to at most n bytes on a character boundary, marking the cut.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// --- the writer ---

const (
	ledgerQueueDepth = 1024 // queued operations before new ones are dropped
	ledgerBatch      = 64   // events one transaction appends
	ledgerTrimEvery  = 128  // appends to a session between bound checks
	ledgerJobTimeout = 10 * time.Second
)

type ledgerOp struct {
	ev  *LedgerEvent
	job func(context.Context)
}

type ledgerWriter struct {
	dataDir string
	mu      sync.Mutex
	idle    *sync.Cond
	queue   []ledgerOp
	running bool
	// appended counts appends per session row since its last bound check; a row leaves
	// it when it is checked, so it holds only sessions active since the last idle.
	appended map[string]int // by session row
}

// ledgerStats counts ledger work since start (the hook service's operator view).
var ledgerStats struct {
	recorded, dropped, failed, trimmed, jobs atomic.Int64
}

// LedgerUsage is the ledger's counters: rows written, operations dropped because the
// queue was full, rows that failed, rows removed by the bounds, and jobs run.
func LedgerUsage() map[string]int64 {
	return map[string]int64{"recorded": ledgerStats.recorded.Load(), "dropped": ledgerStats.dropped.Load(),
		"failed": ledgerStats.failed.Load(), "trimmed": ledgerStats.trimmed.Load(), "jobs": ledgerStats.jobs.Load()}
}

var ledgerWriters = struct {
	sync.Mutex
	m map[string]*ledgerWriter
}{m: map[string]*ledgerWriter{}}

// sessionLedger is the data dir's writer.
func sessionLedger(dataDir string) *ledgerWriter {
	key, err := filepath.Abs(dataDir)
	if err != nil {
		key = dataDir
	}
	ledgerWriters.Lock()
	defer ledgerWriters.Unlock()
	w := ledgerWriters.m[key]
	if w == nil {
		w = &ledgerWriter{dataDir: dataDir, appended: map[string]int{}}
		w.idle = sync.NewCond(&w.mu)
		ledgerWriters.m[key] = w
	}
	return w
}

// RecordSession queues one ledger event and reports whether it was queued; a full
// queue drops it (counted).
func RecordSession(dataDir string, ev LedgerEvent) bool {
	return sessionLedger(dataDir).push(ledgerOp{ev: &ev})
}

// RunSessionJob queues derived ledger work (a snapshot, a capsule, mining). It runs on
// the writer after every event queued before it, with a bounded context.
func RunSessionJob(dataDir string, job func(context.Context)) bool {
	return sessionLedger(dataDir).push(ledgerOp{job: job})
}

// FlushSessionLedger waits until everything queued before the call has been written,
// or timeout passes; it reports whether the writer went idle.
func FlushSessionLedger(dataDir string, timeout time.Duration) bool {
	w := sessionLedger(dataDir)
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		w.mu.Lock()
		w.idle.Broadcast()
		w.mu.Unlock()
	})
	defer timer.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.running && time.Now().Before(deadline) {
		w.idle.Wait()
	}
	return !w.running
}

func (w *ledgerWriter) push(op ledgerOp) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) >= ledgerQueueDepth {
		ledgerStats.dropped.Add(1)
		return false
	}
	w.queue = append(w.queue, op)
	if !w.running {
		w.running = true
		go w.run()
	}
	return true
}

// run drains the queue: a run of events is appended in one transaction, a job runs on
// its own. It trims the sessions it touched once the queue is empty, and exits.
func (w *ledgerWriter) run() {
	for {
		w.mu.Lock()
		if len(w.queue) == 0 {
			w.mu.Unlock()
			w.trimTouched(true)
			w.mu.Lock()
			if len(w.queue) == 0 {
				w.running = false
				w.idle.Broadcast()
				w.mu.Unlock()
				return
			}
		}
		n := 1
		if w.queue[0].job == nil {
			for n < len(w.queue) && n < ledgerBatch && w.queue[n].job == nil {
				n++
			}
		}
		batch := append([]ledgerOp(nil), w.queue[:n]...)
		w.queue = append(w.queue[:0], w.queue[n:]...)
		w.mu.Unlock()
		w.process(batch)
		w.trimTouched(false)
	}
}

func (w *ledgerWriter) process(batch []ledgerOp) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("session ledger: %v", p)
			ledgerStats.failed.Add(int64(len(batch)))
		}
	}()
	if job := batch[0].job; job != nil {
		ctx, cancel := context.WithTimeout(context.Background(), ledgerJobTimeout)
		defer cancel()
		job(ctx)
		ledgerStats.jobs.Add(1)
		return
	}
	evs := make([]*LedgerEvent, len(batch))
	for i, op := range batch {
		evs[i] = op.ev
	}
	for _, group := range byWorkspace(evs) {
		if w.append(group) != nil && len(group) > 1 {
			for _, ev := range group { // one bad row must not lose its batch
				_ = w.append([]*LedgerEvent{ev})
			}
		}
	}
}

// byWorkspace splits events into runs of one workspace, in order.
func byWorkspace(evs []*LedgerEvent) [][]*LedgerEvent {
	var out [][]*LedgerEvent
	for i, ev := range evs {
		if i == 0 || ev.WorkspaceID != evs[i-1].WorkspaceID {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], ev)
	}
	return out
}

// append writes events of one workspace in one transaction, starting or touching each
// session row first (its branch and HEAD may have moved); a failed group is counted
// only when it is a single row (process retries a failed batch row by row).
func (w *ledgerWriter) append(evs []*LedgerEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), ledgerJobTimeout)
	defer cancel()
	counts := map[string]int{}
	err := memoryUpdate(ctx, w.dataDir, evs[0].WorkspaceID, func(tx govstore.Tx) error {
		clear(counts)
		for _, ev := range evs {
			key, row := ev.key(), sessionRow(ev.WorkspaceID, ev.key())
			actor := govstore.Actor{Principal: ledgerPrincipal(ev.Principal), SessionID: key, AgentID: ev.Agent}
			if counts[row] == 0 {
				if err := upsertLedgerSession(ctx, tx, ev, actor); err != nil {
					return err
				}
			}
			counts[row]++
			if _, err := tx.AppendSessionEvent(ctx, ledgerRow(ev, row), actor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if len(evs) == 1 {
			ledgerStats.failed.Add(1)
		}
		return err
	}
	ledgerStats.recorded.Add(int64(len(evs)))
	w.mu.Lock()
	defer w.mu.Unlock()
	for row, n := range counts {
		w.appended[row] += n
	}
	return nil
}

// recordFromJob appends one row at once. A job runs on the writer, so a row it queued
// with RecordSession would land behind the jobs queued after it: a snapshot taken
// next would then look older than the session.
func recordFromJob(dataDir string, ev LedgerEvent) error {
	return sessionLedger(dataDir).append([]*LedgerEvent{&ev})
}

// touched marks a session row a job appended to, so the writer's next idle check
// bounds it.
func touched(dataDir, row string) {
	w := sessionLedger(dataDir)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appended[row]++
}

// upsertLedgerSession starts the session row, or touches it, with its branch and HEAD
// read from the repository files. A subagent's row names its parent session's row; a
// session on a branch joins the branch's thread.
func upsertLedgerSession(ctx context.Context, tx govstore.Tx, ev *LedgerEvent, actor govstore.Actor) error {
	sha, branch := gitHead(ev.Root)
	key := ev.key()
	in := govstore.SessionInput{ID: sessionRow(ev.WorkspaceID, key), WorkspaceID: ev.WorkspaceID, Client: ev.Client,
		AgentID: ev.Agent, ExecutionKey: key, Branch: branch, HeadSHA: sha, Worktree: ev.Root}
	if ev.Agent != "" {
		in.ParentID = sessionRow(ev.WorkspaceID, ExecutionKey(ev.Client, ev.Session, ""))
	}
	if branch != "" {
		in.ThreadID = "branch:" + branch
	}
	_, err := tx.UpsertSession(ctx, in, actor)
	return err
}

// ledgerRow is the row an event appends to the session row.
func ledgerRow(ev *LedgerEvent, row string) govstore.SessionEventInput {
	in := govstore.SessionEventInput{SessionID: row, Kind: ev.Kind, Tool: ev.Tool, ArgsDigest: ev.Args,
		Handle: ev.Handle, Status: ev.Status, Path: ev.Path, EntryID: ev.EntryID}
	if d := (ledgerData{Text: LedgerExcerpt(ev.Text), Signature: LedgerExcerpt(ev.Signature), ExpiresAt: ev.ExpiresAt}); d != (ledgerData{}) {
		in.Data = d
	}
	return in
}

// trimTouched applies the ledger bounds to the sessions appended to since their last
// check: those with ledgerTrimEvery appends, or all of them once the queue is empty, so
// a session overshoots its bound by less than ledgerTrimEvery rows while it is busy.
func (w *ledgerWriter) trimTouched(all bool) {
	w.mu.Lock()
	var rows []string
	for row, n := range w.appended {
		if all || n >= ledgerTrimEvery {
			rows = append(rows, row)
			delete(w.appended, row)
		}
	}
	w.mu.Unlock()
	for _, row := range rows {
		if err := trimLedger(w.dataDir, row); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("session ledger: trim %s: %v", row, err)
		}
	}
}

// trimLedger applies ledgerBounds to one session row (the bounds are per session row,
// so no workspace is needed).
func trimLedger(dataDir, row string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ledgerJobTimeout)
	defer cancel()
	s, release, err := acquireMemoryStore(ctx, dataDir)
	if err != nil {
		return err
	}
	defer release()
	return s.Update(ctx, func(tx govstore.Tx) error {
		for _, b := range ledgerBounds {
			n, err := tx.TrimSessionEvents(ctx, row, b.kinds, b.keep)
			if err != nil {
				return err
			}
			ledgerStats.trimmed.Add(n)
		}
		return nil
	})
}

// --- reading ---

// ledgerTail is how many of a session's newest rows a snapshot, a capsule or the
// miner reads.
const ledgerTail = 512

// sessionTail reads a session (by execution key) of a workspace and its newest rows in
// order (oldest first).
func sessionTail(ctx context.Context, r govstore.Reader, workspaceID, key string) (govstore.Session, []govstore.SessionEvent, error) {
	s, err := workspaceSession(ctx, r, workspaceID, key)
	if err != nil {
		return govstore.Session{}, nil, err
	}
	evs, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: s.ID,
		Kinds: activityKinds, Limit: ledgerTail})
	if err != nil {
		return govstore.Session{}, nil, err
	}
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	return s, evs, nil
}

// workspaceSession reads a workspace's session row by execution key.
func workspaceSession(ctx context.Context, r govstore.Reader, workspaceID, key string) (govstore.Session, error) {
	s, err := r.GetSession(ctx, sessionRow(workspaceID, key))
	if err == nil && s.WorkspaceID != workspaceID { // a digest collision: never another workspace's
		err = fmt.Errorf("session %s: %w", key, govstore.ErrNotFound)
	}
	return s, err
}

// rowText is a row's kept excerpt.
func rowText(ev govstore.SessionEvent) ledgerData {
	var d ledgerData
	if len(ev.Data) > 0 {
		_ = json.Unmarshal(ev.Data, &d)
	}
	return d
}

// --- boundaries ---

// Session boundaries: the client events that close a stretch of a session. Each records
// its row and queues its derived work behind it, and returns at once: a hook answers
// without waiting (Claude Code gives SessionEnd hooks a shared 1.5 s budget), and the
// snapshot, capsule and mining run on the writer, after every row the session queued
// before the boundary.
const (
	BoundaryPreCompact   = "pre_compact"
	BoundaryPostCompact  = "post_compact"
	BoundaryTurnEnd      = "turn_end"
	BoundarySubagentStop = "subagent_stop"
	BoundarySessionEnd   = "session_end"
)

// ledgerJob is derived work a boundary queues, for the boundary's event.
type ledgerJob func(ctx context.Context, dataDir string, ev LedgerEvent) error

// ledgerBoundaries: the row each boundary appends and the jobs it queues, in order. A
// subagent's boundary needs its agent id: without one it would end the main thread.
// The jobs that append rows (mining, the transcript reference) run before the end row's
// summary and the snapshot, so the stored snapshot covers the whole session and is the
// one a later start restores (latestSnapshot), not rebuilt.
var ledgerBoundaries = map[string]struct {
	kind, status string
	needsAgent   bool
	jobs         []ledgerJob
}{
	BoundaryPreCompact:   {kind: LedgerCompaction, status: "pre", jobs: []ledgerJob{snapshotJob}},
	BoundaryPostCompact:  {kind: LedgerCompaction, status: "post"},
	BoundaryTurnEnd:      {kind: LedgerTurnEnd, jobs: []ledgerJob{mineJob}},
	BoundarySubagentStop: {kind: LedgerSubagentStop, needsAgent: true, jobs: []ledgerJob{endRowJob}},
	BoundarySessionEnd: {kind: LedgerSessionEnd,
		jobs: []ledgerJob{mineJob, transcriptJob, endRowJob, autoCapsuleJob, snapshotJob}},
}

// RecordBoundary records a boundary (ev.Text: the compaction trigger or end reason) and
// queues its work. It reports whether the row was queued; an unknown boundary, or a
// subagent's without its agent id, records nothing.
func RecordBoundary(dataDir, boundary string, ev LedgerEvent) bool {
	b, ok := ledgerBoundaries[boundary]
	if !ok || b.needsAgent && ev.Agent == "" {
		return false
	}
	ev.Kind, ev.Status = b.kind, b.status
	queued := RecordSession(dataDir, ev)
	for _, job := range b.jobs {
		RunSessionJob(dataDir, func(ctx context.Context) {
			if err := job(ctx, dataDir, ev); err != nil && !isNotExist(err) {
				ledgerStats.failed.Add(1)
				log.Printf("session ledger: %s job for %s: %v", boundary, ev.key(), err)
			}
		})
	}
	return queued
}

func snapshotJob(ctx context.Context, dataDir string, ev LedgerEvent) error {
	return storeSnapshot(ctx, dataDir, ev.WorkspaceID, ev.key(), ev.Principal)
}

func mineJob(ctx context.Context, dataDir string, ev LedgerEvent) error {
	_, err := mineSession(ctx, dataDir, ev)
	return err
}

func autoCapsuleJob(ctx context.Context, dataDir string, ev LedgerEvent) error {
	return storeAutoCapsule(ctx, dataDir, ev.WorkspaceID, ev.key(), ev.Principal, ev.Text)
}

// endRowJob ends the session row with its deterministic summary; a row already ended
// is left as it is.
func endRowJob(ctx context.Context, dataDir string, ev LedgerEvent) error {
	key := ev.key()
	return memoryUpdate(ctx, dataDir, ev.WorkspaceID, func(tx govstore.Tx) error {
		sess, evs, err := sessionTail(ctx, tx, ev.WorkspaceID, key)
		if err != nil || sess.Status != govstore.SessionActive {
			return err
		}
		_, err = tx.EndSession(ctx, sess.ID, govstore.SessionEnded, sessionSummary(evs, time.Now().UTC()),
			govstore.Actor{Principal: ledgerPrincipal(ev.Principal), SessionID: key})
		return err
	})
}

// sessionSummary is a session's deterministic one-line summary.
func sessionSummary(evs []govstore.SessionEvent, now time.Time) string {
	n := map[string]int{}
	for _, ev := range evs {
		n[ev.Kind]++
		if ev.Kind == LedgerToolCall && ev.Status == LedgerError {
			n["failed"]++
		}
	}
	modified := 0
	for _, f := range activeFiles(evs) {
		if f.Modified {
			modified++
		}
	}
	return fmt.Sprintf("%d prompt(s), %d tool call(s) (%d failed), %d file(s) modified, %d memory proposal(s), %d open failure(s)",
		n[LedgerPrompt], n[LedgerToolCall], n["failed"], modified, n[LedgerMemoryProposed], len(openFailures(evs, now)))
}

// transcriptJob records the client's transcript as an upstream reference (PAR-HAR-05):
// a transcript_ref row keeps its path and the digest of its content.
func transcriptJob(_ context.Context, dataDir string, ev LedgerEvent) error {
	if ev.Transcript == "" {
		return nil
	}
	digest, hashed, truncated, err := transcriptRef(ev.Client, ev.Transcript)
	if err != nil {
		return nil // not a transcript this client keeps: nothing to reference
	}
	row := ev
	row.Kind, row.Status, row.Path, row.Args, row.Transcript = LedgerTranscript, "", ev.Transcript, "sha256:"+digest, ""
	row.Text = fmt.Sprintf("%d bytes hashed", hashed)
	if truncated {
		row.Text += fmt.Sprintf(" (the first %d; the file is larger)", transcriptHashCap)
	}
	return recordFromJob(dataDir, row)
}

// transcriptRoots are where a client keeps its transcripts; a transcript is hashed only
// under one of them. Claude Code is the one client whose hooks name a transcript.
func transcriptRoots(client string, getenv func(string) string) []string {
	if client != "claude" {
		return nil
	}
	var roots []string
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		roots = append(roots, filepath.Join(d, "projects"))
	}
	if h := getenv("HOME"); h != "" {
		roots = append(roots, filepath.Join(h, ".claude", "projects"))
	}
	return roots
}

// transcriptHashCap bounds the bytes of a transcript that are hashed.
const transcriptHashCap = 64 << 20

var errNotTranscript = errors.New("not a transcript of this client")

// transcriptRef hashes a client's transcript, streamed and capped at transcriptHashCap
// bytes. The checks run in order and fail closed: an absolute .jsonl path, resolved
// (symlinks followed) inside one of the client's transcript roots, opened as the same
// regular file that was resolved.
func transcriptRef(client, path string) (digest string, hashed int64, truncated bool, err error) {
	roots := transcriptRoots(client, os.Getenv)
	if len(roots) == 0 || !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return "", 0, false, errNotTranscript
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", 0, false, errNotTranscript
	}
	inRoot := false
	for _, root := range roots {
		if r, err := filepath.EvalSymlinks(root); err == nil && pathWithin(resolved, r) {
			inRoot = true
			break
		}
	}
	if !inRoot {
		return "", 0, false, errNotTranscript
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", 0, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", 0, false, err
	}
	if lst, err := os.Lstat(resolved); err != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, lst) {
		return "", 0, false, errNotTranscript
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, transcriptHashCap))
	if err != nil {
		return "", 0, false, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, opened.Size() > n, nil
}
