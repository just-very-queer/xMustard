package rustcore

// The resident Rust worker (WS-02, PAR-RT-01). One long-lived `xmustard-core serve`
// process answers the bridge's calls instead of a new process per call. It is off by
// default. XMUSTARD_CORE_WORKER=1 turns it on, and it stays opt-in until the
// parity-scale budget gate passes.
//
// The client follows the cursor-bridge MCPClient/CodexClient pattern
// (research/cursor-bridge/bridge_mcp.go and main.go): a pending map keyed by request
// id, one read loop, failAllPending when the process exits, lazy (re)start with
// backoff, and a cancel message when a caller gives up. The existing boundaries still
// hold:
//   - Kill boundary. The worker runs in its own process group and is tracked for
//     shutdown (TrackChild / KillActiveChildren). When it misbehaves, it is killed
//     with its descendants (KillProcessTree).
//   - ChildLimit admission. Every call holds a budget.Children slot while it runs, so
//     concurrent Rust work stays bounded as before. serve gets the same limit through
//     --max-inflight.
//   - Byte admission. A response's size is known from its frame header. Its bytes are
//     reserved against the caller's budget scope before they are read. A refused
//     response is drained without buffering it.
//
// Deadlines: a caller that gives up (context cancelled or timed out) gets its error at
// once and releases its slot, and the worker is sent $/cancelRequest. The worker
// cannot interrupt a running handler. If an abandoned request still has no answer
// workerKillGrace after its deadline, the worker is killed and restarted, as the
// one-shot path kills a child at its timeout.
//
// Fallback: when the worker cannot be started (no built binary, a binary without
// `serve`, a failed handshake), calls take the one-shot path and a restart is tried
// after a backoff. Subcommands the worker does not run in-process (live LSP,
// verification runners, goals) always take the one-shot path.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xmustard/api-go/internal/budget"
)

const (
	workerProtocol = 1
	// maxWorkerFrame is the largest response body: the one-shot stdout cap.
	maxWorkerFrame = maxCoreStdout
	// maxWorkerNotice bounds a frame that carries no request id (never sent today).
	maxWorkerNotice    = 64 << 10
	maxWorkerHeaderLen = 1024
	maxWorkerHeaders   = 16
	// workerKillGrace is how long an abandoned request may outlive its deadline before
	// the worker is killed. It matches the one-shot WaitDelay.
	workerKillGrace = 2 * time.Second

	// An idle worker keeps its heap (the allocator does not return it), so it exits
	// after two minutes without calls: long enough to stay warm across an agent's
	// turns, short enough that an idle API does not hold it. It drops its resident
	// graph snapshots after 30 s.
	defaultWorkerIdle  = 2 * time.Minute
	defaultWorkerTrim  = 30 * time.Second
	defaultWorkerStart = 10 * time.Second

	startBackoffMin = time.Second
	startBackoffMax = time.Minute
	crashBackoffMin = 250 * time.Millisecond
	crashBackoffMax = 30 * time.Second
)

// JSON-RPC error codes the worker sends (rust-core/src/serve.rs).
const (
	rpcMethodNotFound   = -32601
	rpcCommandFailed    = -32001
	rpcOutputTooLarge   = -32002
	rpcRequestCancelled = -32800
)

var (
	errWorkerExited   = errors.New("rust-core worker exited")
	errWorkerBackoff  = errors.New("rust-core worker restart is backing off")
	errWorkerProtocol = errors.New("rust-core worker protocol error")
)

// workerEnabled reports whether XMUSTARD_CORE_WORKER selects the resident worker.
func workerEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XMUSTARD_CORE_WORKER"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

// workerSettings are read per call, so tests and operators can change them. A change
// that alters the worker's command line restarts it (see workerKey).
type workerSettings struct {
	// idle is how long the worker lives without calls before its stdin is closed
	// (XMUSTARD_CORE_WORKER_IDLE_MS; zero keeps it until shutdown).
	idle time.Duration
	// trim is when the worker drops its resident graph snapshots after its last
	// request (XMUSTARD_CORE_WORKER_TRIM_MS, passed as --trim-idle-ms).
	trim time.Duration
	// start bounds the initialize handshake (XMUSTARD_CORE_WORKER_START_MS).
	start time.Duration
}

func readWorkerSettings() workerSettings {
	return workerSettings{
		idle:  envDuration("XMUSTARD_CORE_WORKER_IDLE_MS", defaultWorkerIdle),
		trim:  envDuration("XMUSTARD_CORE_WORKER_TRIM_MS", defaultWorkerTrim),
		start: envDuration("XMUSTARD_CORE_WORKER_START_MS", defaultWorkerStart),
	}
}

// workerKey identifies what a worker was started as: its command line, working
// directory, environment and the API's working directory. A one-shot child sees the
// environment of the moment it is started, so a worker started under another
// environment is replaced rather than reused.
type workerKey struct {
	name string
	args []string
	dir  string
	id   string
}

func workerKeyFor(s workerSettings) (workerKey, bool) {
	name, full, dir := coreInvocation("serve",
		"--max-inflight="+strconv.Itoa(budget.Children.Cap()),
		"--trim-idle-ms="+strconv.FormatInt(s.trim.Milliseconds(), 10),
		"--max-snapshots=1",
	)
	if name == "cargo" {
		// No built binary: `cargo run` may compile for minutes, so there is nothing to
		// keep resident. The one-shot path handles it.
		return workerKey{}, false
	}
	h := fnv.New64a()
	for _, kv := range os.Environ() {
		h.Write([]byte(kv))
		h.Write([]byte{0})
	}
	cwd, _ := os.Getwd()
	id := strings.Join(append([]string{name, dir, cwd, strconv.FormatUint(h.Sum64(), 16)}, full...), "\x00")
	return workerKey{name: name, args: full, dir: dir, id: id}, true
}

// WorkerStats reports the resident worker's lifecycle counters.
type WorkerStats struct {
	Enabled       bool  `json:"enabled"`
	PID           int   `json:"pid,omitempty"`
	Starts        int64 `json:"starts"`
	StartFailures int64 `json:"start_failures"`
	Crashes       int64 `json:"crashes"`
	IdleExits     int64 `json:"idle_exits"`
	DeadlineKills int64 `json:"deadline_kills"`
	Calls         int64 `json:"calls"`
	Fallbacks     int64 `json:"fallbacks"`
	Cancels       int64 `json:"cancels"`
}

type workerCounters struct {
	starts, startFailures, crashes, idleExits, deadlineKills atomic.Int64
	calls, fallbacks, cancels                                atomic.Int64
}

type workerSupervisor struct {
	mu       sync.Mutex
	proc     *workerProc
	starting chan struct{} // closed when the start in progress ends
	// restart backoff, for one key
	retryKey   string
	retryAt    time.Time
	startFails int
	crashes    int
	counters   workerCounters
}

// coreWorker is the process-wide resident worker supervisor.
var coreWorker = &workerSupervisor{}

// CoreWorkerStats snapshots the resident worker's counters.
func CoreWorkerStats() WorkerStats {
	s := coreWorker
	st := WorkerStats{
		Enabled:       workerEnabled(),
		Starts:        s.counters.starts.Load(),
		StartFailures: s.counters.startFailures.Load(),
		Crashes:       s.counters.crashes.Load(),
		IdleExits:     s.counters.idleExits.Load(),
		DeadlineKills: s.counters.deadlineKills.Load(),
		Calls:         s.counters.calls.Load(),
		Fallbacks:     s.counters.fallbacks.Load(),
		Cancels:       s.counters.cancels.Load(),
	}
	s.mu.Lock()
	if s.proc != nil {
		st.PID = s.proc.pid
	}
	s.mu.Unlock()
	return st
}

// RecycleCoreWorker retires the resident worker once its current calls finish, which
// returns its retained heap to the OS; the next call starts a fresh worker. It is the
// lever for a memory governor (WS-06) under pressure, and reports whether a worker
// was running.
func RecycleCoreWorker() bool {
	s := coreWorker
	s.mu.Lock()
	p := s.proc
	if p == nil {
		s.mu.Unlock()
		return false
	}
	s.proc = nil
	p.retiring = true
	idle := p.active == 0
	s.mu.Unlock()
	if idle {
		p.retire()
	}
	return true
}

func backoff(min, max time.Duration, failures int) time.Duration {
	d := min
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// acquire returns a live worker for key, starting one if needed, and counts the
// caller as active on it (release must follow). It fails with errWorkerBackoff while
// a restart for key is backing off.
func (s *workerSupervisor) acquire(ctx context.Context, key workerKey, set workerSettings) (*workerProc, error) {
	for {
		s.mu.Lock()
		if p := s.proc; p != nil {
			if p.isDead() {
				// it exited; wait until its exit is accounted (onExit), which also
				// sets the crash backoff, then decide again.
				s.mu.Unlock()
				<-p.done
				s.mu.Lock()
				if s.proc == p {
					s.proc = nil
				}
				s.mu.Unlock()
				continue
			}
			if p.key == key.id {
				p.active++
				s.mu.Unlock()
				return p, nil
			}
			// the configuration changed: retire it once its callers are done.
			s.proc = nil
			p.retiring = true
			idle := p.active == 0
			s.mu.Unlock()
			if idle {
				p.retire()
			}
			continue
		}
		if wait := s.starting; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if s.retryKey == key.id && time.Now().Before(s.retryAt) {
			s.mu.Unlock()
			return nil, errWorkerBackoff
		}
		done := make(chan struct{})
		s.starting = done
		s.mu.Unlock()

		p, err := s.startWorker(ctx, key, set)

		s.mu.Lock()
		s.starting = nil
		close(done)
		if err != nil {
			if ctx.Err() == nil {
				s.startFails++
				s.retryKey = key.id
				s.retryAt = time.Now().Add(backoff(startBackoffMin, startBackoffMax, s.startFails))
				s.counters.startFailures.Add(1)
			}
			s.mu.Unlock()
			return nil, err
		}
		s.startFails = 0
		s.proc = p
		p.active++
		s.counters.starts.Add(1)
		s.mu.Unlock()
		return p, nil
	}
}

// release ends a caller's use of p. ok reports a completed call, which clears the
// crash backoff.
func (s *workerSupervisor) release(p *workerProc, ok bool) {
	s.mu.Lock()
	p.active--
	p.lastUsed = time.Now()
	if ok {
		s.crashes = 0
	}
	retire := p.active == 0 && p.retiring
	if p.active == 0 && s.proc == p && p.idleAfter > 0 {
		if p.idleTimer != nil {
			p.idleTimer.Stop()
		}
		p.idleTimer = time.AfterFunc(p.idleAfter, func() { s.idleCheck(p) })
	}
	s.mu.Unlock()
	if retire {
		p.retire()
	}
}

// idleCheck retires p when it has had no caller for its idle period.
func (s *workerSupervisor) idleCheck(p *workerProc) {
	s.mu.Lock()
	if s.proc != p || p.active != 0 || time.Since(p.lastUsed) < p.idleAfter {
		s.mu.Unlock()
		return
	}
	s.proc = nil
	p.retiring = true
	s.mu.Unlock()
	s.counters.idleExits.Add(1)
	p.retire()
}

// onExit runs once when a worker process has exited and its calls have failed, before
// its done channel closes.
func (s *workerSupervisor) onExit(p *workerProc, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	if s.proc == p {
		s.proc = nil
	}
	if p.retiring || !p.started.Load() {
		return
	}
	// An unexpected exit: the next call restarts at once; repeated crashes back off.
	s.crashes++
	s.counters.crashes.Add(1)
	s.retryKey = p.key
	s.retryAt = time.Now()
	if s.crashes > 1 {
		s.retryAt = s.retryAt.Add(backoff(crashBackoffMin, crashBackoffMax, s.crashes-1))
	}
	log.Printf("rust-core worker %d exited unexpectedly: %v", p.pid, err)
}

// workerProc is one running `xmustard-core serve` process.
type workerProc struct {
	key     string
	pid     int
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	untrack func()
	methods map[string]bool
	// oneShot lists, per resident family, the first arguments it runs one-shot.
	oneShot   map[string]map[string]bool
	counters  *workerCounters
	idleAfter time.Duration
	onExit    func(*workerProc, error)

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]*workerCall
	dead    bool
	reaped  bool
	exitErr error
	done    chan struct{}

	// guarded by the supervisor's mu
	active    int
	retiring  bool
	lastUsed  time.Time
	idleTimer *time.Timer

	// started is set once the handshake succeeded; an exit before that is a failed
	// start (handled by acquire), not a crash.
	started atomic.Bool
	retired atomic.Bool
}

type workerCall struct {
	id        int64
	scope     *budget.Scope
	resp      chan workerResult
	deadline  time.Time
	abandoned bool
	timer     *time.Timer
}

type workerRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type workerResult struct {
	// result is the JSON-RPC result, sliced from the frame buffer (no copy).
	result []byte
	rpcErr *workerRPCError
	err    error
}

// residentFor reports whether the worker runs sub with args in-process.
func (p *workerProc) residentFor(sub string, args []string) bool {
	if !p.methods[sub] {
		return false
	}
	return len(args) == 0 || !p.oneShot[sub][args[0]]
}

func (p *workerProc) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// startWorker starts `xmustard-core serve` for key and completes the initialize
// handshake within ctx and the start bound.
func (s *workerSupervisor) startWorker(ctx context.Context, key workerKey, set workerSettings) (*workerProc, error) {
	cmd := exec.Command(key.name, key.args...)
	cmd.Dir = key.dir
	isolateProcessGroup(cmd)
	cmd.WaitDelay = workerKillGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &workerProc{
		key:       key.id,
		cmd:       cmd,
		stdin:     stdin,
		pending:   map[int64]*workerCall{},
		done:      make(chan struct{}),
		counters:  &s.counters,
		idleAfter: set.idle,
		onExit:    s.onExit,
	}
	cmd.Stderr = &workerLog{proc: p}
	untrack, err := startTracked(cmd)
	if err != nil {
		return nil, err
	}
	p.untrack = untrack
	p.pid = cmd.Process.Pid
	go p.readLoop(bufio.NewReaderSize(stdout, 64<<10))

	hctx, cancel := context.WithTimeout(ctx, set.start)
	defer cancel()
	scope := budget.NewScope(nil)
	defer scope.Close()
	r := p.call(hctx, scope, "initialize", nil)
	var init struct {
		Protocol int                 `json:"protocol"`
		Methods  []string            `json:"methods"`
		OneShot  map[string][]string `json:"one_shot_subcommands"`
	}
	switch {
	case r.err != nil:
		err = r.err
	case r.rpcErr != nil:
		err = fmt.Errorf("initialize refused: %s", r.rpcErr.Message)
	default:
		if derr := json.Unmarshal(r.result, &init); derr != nil {
			err = fmt.Errorf("initialize: %w", derr)
		} else if init.Protocol != workerProtocol {
			err = fmt.Errorf("initialize: protocol %d, want %d", init.Protocol, workerProtocol)
		}
	}
	if err != nil {
		p.kill()
		<-p.done
		log.Printf("rust-core worker %d did not start: %v", p.pid, err)
		return nil, fmt.Errorf("start rust-core worker: %w", err)
	}
	p.methods = make(map[string]bool, len(init.Methods))
	for _, m := range init.Methods {
		p.methods[m] = true
	}
	p.oneShot = make(map[string]map[string]bool, len(init.OneShot))
	for family, subs := range init.OneShot {
		p.oneShot[family] = make(map[string]bool, len(subs))
		for _, sub := range subs {
			p.oneShot[family][sub] = true
		}
	}
	p.started.Store(true)
	return p, nil
}

// call sends one request and waits for its answer or for ctx to end. A caller that
// gives up abandons the request (see abandon); its answer is discarded unread.
func (p *workerProc) call(ctx context.Context, scope *budget.Scope, method string, args []string) workerResult {
	c := &workerCall{scope: scope, resp: make(chan workerResult, 1)}
	c.deadline, _ = ctx.Deadline()
	p.mu.Lock()
	if p.dead {
		err := p.exitErr
		p.mu.Unlock()
		return workerResult{err: err}
	}
	p.nextID++
	c.id = p.nextID
	p.pending[c.id] = c
	p.mu.Unlock()

	if args == nil {
		args = []string{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": c.id, "method": method,
		"params": map[string]any{"args": args},
	})
	if err == nil {
		err = p.write(c.id, body)
	}
	if err != nil {
		// a failed write means the process is gone; the read loop fails the call.
		p.kill()
		return <-c.resp
	}
	select {
	case r := <-c.resp:
		return r
	case <-ctx.Done():
		p.abandon(c)
		return workerResult{err: ctx.Err()}
	}
}

func (p *workerProc) write(id int64, body []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return writeWorkerFrame(p.stdin, id, true, body)
}

// abandon marks c's caller gone, asks the worker to cancel it, and arms the deadline
// watchdog: a request still unanswered workerKillGrace after its deadline kills the
// worker.
func (p *workerProc) abandon(c *workerCall) {
	p.mu.Lock()
	if p.pending[c.id] != c {
		p.mu.Unlock()
		return
	}
	c.abandoned = true
	wait := workerKillGrace
	if !c.deadline.IsZero() {
		if until := time.Until(c.deadline) + workerKillGrace; until > wait {
			wait = until
		}
	}
	c.timer = time.AfterFunc(wait, func() { p.deadlineExpired(c) })
	p.mu.Unlock()
	p.counters.cancels.Add(1)
	go func() {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]any{"id": c.id},
		})
		p.writeMu.Lock()
		defer p.writeMu.Unlock()
		_ = writeWorkerFrame(p.stdin, 0, false, body)
	}()
}

func (p *workerProc) deadlineExpired(c *workerCall) {
	p.mu.Lock()
	stillRunning := p.pending[c.id] == c && !p.dead
	p.mu.Unlock()
	if !stillRunning {
		return
	}
	p.counters.deadlineKills.Add(1)
	log.Printf("rust-core worker %d: request %d outlived its deadline; killing the worker", p.pid, c.id)
	p.kill()
}

// deliver completes c with r unless it was already completed.
func (p *workerProc) deliver(c *workerCall, r workerResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[c.id] != c {
		return
	}
	delete(p.pending, c.id)
	if c.timer != nil {
		c.timer.Stop()
	}
	c.resp <- r
}

// failAllPending completes every outstanding call with err and marks p dead.
func (p *workerProc) failAllPending(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	p.exitErr = err
	for id, c := range p.pending {
		if c.timer != nil {
			c.timer.Stop()
		}
		c.resp <- workerResult{err: err}
		delete(p.pending, id)
	}
}

// retire ends p gracefully: closing stdin makes the worker exit; it is killed if it
// has not exited after workerKillGrace.
func (p *workerProc) retire() {
	if !p.retired.CompareAndSwap(false, true) {
		return
	}
	p.writeMu.Lock()
	_ = p.stdin.Close()
	p.writeMu.Unlock()
	go func() {
		select {
		case <-p.done:
		case <-time.After(workerKillGrace):
			p.kill()
		}
	}()
}

// kill ends the worker and its process group now. After the process is reaped its
// process group id may be reused, so a reaped worker is left alone.
func (p *workerProc) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		KillProcessTree(p.cmd)
	}
}

// readLoop reads frames until the worker's stdout ends, then reaps the process and
// fails whatever is still pending.
func (p *workerProc) readLoop(r *bufio.Reader) {
	err := p.readFrames(r)
	if !errors.Is(err, io.EOF) {
		log.Printf("rust-core worker %d: %v", p.pid, err)
	}
	if !errors.Is(err, io.EOF) || !p.retired.Load() {
		// a protocol error, or output that ended without a retire: the worker is not
		// usable, so it does not get to linger. A retired worker exits on its own
		// (retire kills it after workerKillGrace).
		p.kill()
	}
	waitErr := p.cmd.Wait()
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
	cause := fmt.Errorf("%w (%v)", errWorkerExited, waitErr)
	if waitErr == nil {
		cause = errWorkerExited
	}
	p.failAllPending(cause)
	if p.untrack != nil {
		p.untrack()
	}
	if p.onExit != nil {
		p.onExit(p, cause)
	}
	close(p.done)
}

func (p *workerProc) readFrames(r *bufio.Reader) error {
	for {
		n, id, hasID, err := readWorkerHeader(r)
		if err != nil {
			return err
		}
		if !hasID {
			if n > maxWorkerNotice {
				return fmt.Errorf("%w: %d-byte frame without a request id", errWorkerProtocol, n)
			}
			if _, err := r.Discard(n); err != nil {
				return err
			}
			continue
		}
		p.mu.Lock()
		c := p.pending[id]
		if c != nil && c.abandoned {
			// the caller is gone: this answer ends the request; nobody reads it.
			delete(p.pending, id)
			if c.timer != nil {
				c.timer.Stop()
			}
			c = nil
		}
		p.mu.Unlock()
		if c == nil {
			if _, err := r.Discard(n); err != nil {
				return err
			}
			continue
		}
		// Admission before allocation: reserve the body against the caller's scope.
		if err := c.scope.Acquire(int64(n)); err != nil {
			if _, derr := r.Discard(n); derr != nil {
				return derr
			}
			p.deliver(c, workerResult{err: budget.ErrOverloaded})
			continue
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		res, perr := parseWorkerResponse(body, id)
		if perr != nil {
			return perr
		}
		p.deliver(c, res)
	}
}

// parseWorkerResponse extracts a response's result or error. The worker writes a
// result as `{"jsonrpc":"2.0","id":N,"result":<output>}`, so the fast path slices the
// output out of body without decoding or copying it.
func parseWorkerResponse(body []byte, id int64) (workerResult, error) {
	prefix := `{"jsonrpc":"2.0","id":` + strconv.FormatInt(id, 10) + `,"result":`
	if len(body) > len(prefix) && bytes.HasPrefix(body, []byte(prefix)) && body[len(body)-1] == '}' {
		return workerResult{result: body[len(prefix) : len(body)-1]}, nil
	}
	var msg struct {
		ID     *int64          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *workerRPCError `json:"error"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return workerResult{}, fmt.Errorf("%w: undecodable response: %v", errWorkerProtocol, err)
	}
	if msg.ID == nil || *msg.ID != id {
		return workerResult{}, fmt.Errorf("%w: response id does not match frame id %d", errWorkerProtocol, id)
	}
	if msg.Error != nil {
		return workerResult{rpcErr: msg.Error}, nil
	}
	return workerResult{result: msg.Result}, nil
}

// writeWorkerFrame writes one message: a Content-Length header (plus Xmustard-Id when
// hasID), a blank line, and body.
func writeWorkerFrame(w io.Writer, id int64, hasID bool, body []byte) error {
	var b bytes.Buffer
	b.Grow(64 + len(body))
	b.WriteString("Content-Length: ")
	b.WriteString(strconv.Itoa(len(body)))
	b.WriteString("\r\n")
	if hasID {
		b.WriteString("Xmustard-Id: ")
		b.WriteString(strconv.FormatInt(id, 10))
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.Write(body)
	_, err := w.Write(b.Bytes())
	return err
}

// readWorkerHeader reads one header block: at most maxWorkerHeaders lines of at most
// maxWorkerHeaderLen bytes. The body length may not exceed maxWorkerFrame. It returns
// io.EOF only at a clean end of input between messages.
func readWorkerHeader(r *bufio.Reader) (n int, id int64, hasID bool, err error) {
	lines := 0
	haveLen := false
	for {
		line, rerr := r.ReadSlice('\n')
		if rerr != nil {
			if errors.Is(rerr, bufio.ErrBufferFull) {
				return 0, 0, false, fmt.Errorf("%w: header line longer than %d bytes", errWorkerProtocol, maxWorkerHeaderLen)
			}
			if errors.Is(rerr, io.EOF) && len(line) == 0 && lines == 0 {
				return 0, 0, false, io.EOF
			}
			return 0, 0, false, fmt.Errorf("%w: %v", errWorkerProtocol, rerr)
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > maxWorkerHeaderLen {
			return 0, 0, false, fmt.Errorf("%w: header line longer than %d bytes", errWorkerProtocol, maxWorkerHeaderLen)
		}
		if len(line) == 0 {
			if lines == 0 {
				continue
			}
			break
		}
		lines++
		if lines > maxWorkerHeaders {
			return 0, 0, false, fmt.Errorf("%w: more than %d header lines", errWorkerProtocol, maxWorkerHeaders)
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			return 0, 0, false, fmt.Errorf("%w: malformed header line", errWorkerProtocol)
		}
		value = bytes.TrimSpace(value)
		switch {
		case strings.EqualFold(string(name), "Content-Length"):
			v, perr := strconv.Atoi(string(value))
			if perr != nil || v < 0 {
				return 0, 0, false, fmt.Errorf("%w: bad Content-Length", errWorkerProtocol)
			}
			n, haveLen = v, true
		case strings.EqualFold(string(name), "Xmustard-Id"):
			v, perr := strconv.ParseInt(string(value), 10, 64)
			if perr != nil {
				return 0, 0, false, fmt.Errorf("%w: bad Xmustard-Id", errWorkerProtocol)
			}
			id, hasID = v, true
		}
	}
	if !haveLen {
		return 0, 0, false, fmt.Errorf("%w: header block without Content-Length", errWorkerProtocol)
	}
	if n > maxWorkerFrame {
		return 0, 0, false, fmt.Errorf("%w: %d-byte frame exceeds %d", errWorkerProtocol, n, maxWorkerFrame)
	}
	return n, id, hasID, nil
}

// workerLog forwards the worker's stderr to the server log, one bounded line at a
// time. Worker stderr is never returned to callers.
type workerLog struct {
	proc *workerProc
	line []byte
}

const maxWorkerLogLine = 4 << 10

func (l *workerLog) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if room := maxWorkerLogLine - len(l.line); room > 0 {
			if len(chunk) > room {
				l.line = append(l.line, chunk[:room]...)
			} else {
				l.line = append(l.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		log.Printf("rust-core worker %d: %s", l.proc.pid, l.line)
		l.line = l.line[:0]
		b = b[i+1:]
	}
	return n, nil
}

// runViaWorker runs sub on the resident worker. handled is false when the caller must
// take the one-shot path instead: the worker is disabled, sub is one-shot only, or
// the worker cannot be started right now.
func runViaWorker(parent context.Context, sub string, args []string) (out []byte, handled bool, err error) {
	if !workerEnabled() {
		return nil, false, nil
	}
	settings := readWorkerSettings()
	key, ok := workerKeyFor(settings)
	if !ok {
		return nil, false, nil
	}
	s := coreWorker
	ctx, cancel := context.WithTimeout(parent, coreCallTimeout)
	defer cancel()
	p, err := s.acquire(ctx, key, settings)
	if err != nil {
		if cerr := callerError(parent, ctx, sub); cerr != nil {
			return nil, true, cerr
		}
		s.counters.fallbacks.Add(1)
		return nil, false, nil
	}
	completed := false
	defer func() { s.release(p, completed) }()
	if !p.residentFor(sub, args) {
		return nil, false, nil
	}
	release, err := budget.Children.Acquire(parent)
	if err != nil {
		return nil, true, fmt.Errorf("rust-core %s: %w", sub, err)
	}
	defer release()
	scope, owned := budget.ScopeFor(parent)
	if owned {
		defer scope.Close()
	}
	s.counters.calls.Add(1)
	r := p.call(ctx, scope, sub, args)
	switch {
	case r.err != nil:
		if cerr := callerError(parent, ctx, sub); cerr != nil {
			return nil, true, cerr
		}
		if errors.Is(r.err, budget.ErrOverloaded) {
			log.Printf("rust-core %s: worker output refused by transient budget", sub)
			return nil, true, fmt.Errorf("rust-core %s: %w", sub, budget.ErrOverloaded)
		}
		log.Printf("rust-core %s failed in worker %d: %v", sub, p.pid, r.err)
		return nil, true, fmt.Errorf("rust-core %s failed", sub)
	case r.rpcErr != nil:
		switch r.rpcErr.Code {
		case rpcMethodNotFound:
			// not resident for these arguments: the one-shot path owns it.
			return nil, false, nil
		case rpcOutputTooLarge:
			log.Printf("rust-core %s: %s", sub, r.rpcErr.Message)
			return nil, true, fmt.Errorf("rust-core %s: output too large", sub)
		case rpcRequestCancelled:
			if cerr := callerError(parent, ctx, sub); cerr != nil {
				return nil, true, cerr
			}
		}
		log.Printf("rust-core %s failed in worker %d: %s %s", sub, p.pid, r.rpcErr.Message, r.rpcErr.Data)
		completed = true
		return nil, true, fmt.Errorf("rust-core %s failed", sub)
	}
	completed = true
	// As on the one-shot path: without a request scope the reservation ends here, so
	// the caller's decode and response construction are admitted now.
	if !owned {
		if err := scope.Acquire(2 * int64(len(r.result))); err != nil {
			return nil, true, fmt.Errorf("rust-core %s: %w", sub, budget.ErrOverloaded)
		}
	}
	return r.result, true, nil
}

// callerError maps a finished caller context onto the one-shot path's errors.
func callerError(parent, ctx context.Context, sub string) error {
	if parent.Err() != nil {
		return fmt.Errorf("rust-core %s: %w", sub, parent.Err())
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("rust-core %s timed out", sub)
	}
	return nil
}
