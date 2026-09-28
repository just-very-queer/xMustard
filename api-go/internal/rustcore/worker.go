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
//     response is drained without buffering it. As on the one-shot path, output over
//     the call's pool-derived cap (coreStdoutCap), or that the pool could never hold,
//     is the permanent "output too large" error, not a retryable overload.
//   - Budget hooks. The worker gets the one-shot core's environment (coreChildEnv,
//     the Linux allocator cap), and its start is counted as one core spawn
//     (noteCoreSpawn); calls it answers start no process and count none.
//
// Cancellation: a caller that gives up (context cancelled or timed out) gets its error
// at once, and the worker is sent $/cancelRequest. A queued request is dropped at once;
// a running handler cannot be interrupted. The call's ChildLimit slot stays held until
// the worker has ended the request (answered it, or exited), so admission counts
// abandoned work that is still running. If the request is still unanswered
// workerKillGrace after it was abandoned, the worker is retired and replaced: new
// calls start a fresh worker, calls already running on the old one finish under their
// own deadlines, and the old worker exits once none is left (closing its stdin ends it
// and the abandoned handler with it). Unrelated calls are therefore not failed by one
// slow request. The one-shot path kills its child at once instead.
//
// Fallback: when the worker cannot be started (no built binary, a binary without
// `serve`, a failed handshake), or the request could not be sent to it, calls take the
// one-shot path and a restart is tried after a backoff. Subcommands the worker does
// not run in-process (live LSP, verification runners, goals, whole-repository builds)
// always take the one-shot path, and do not start a worker.

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
	// maxWorkerNotice bounds a frame that carries no request id: a watcher notification
	// (refresh.go).
	maxWorkerNotice    = 64 << 10
	maxWorkerHeaderLen = 1024
	maxWorkerHeaders   = 16
	// workerKillGrace is how long an abandoned request may keep running before its
	// worker is retired and replaced, and how long a retired worker may take to exit
	// before it is killed. It matches the one-shot WaitDelay.
	workerKillGrace = 2 * time.Second

	// An idle worker keeps most of its heap: it drops its resident graph snapshots
	// after 30 s, but the allocator returns little of the freed memory (measured on
	// pi-mono: 0-3.5 MiB of 19-23 MiB). So it exits after two minutes without calls:
	// long enough to stay warm across an agent's turns, short enough that an idle API
	// does not hold it. A call after a longer gap starts a new worker (one exec).
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
	// errWorkerUnsent means the request never reached the worker (it had exited or its
	// stdin was gone), so nothing ran and the one-shot path may take the call.
	errWorkerUnsent = errors.New("rust-core worker request not sent")
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
	s := workerSettings{
		idle:  envDuration("XMUSTARD_CORE_WORKER_IDLE_MS", defaultWorkerIdle),
		trim:  envDuration("XMUSTARD_CORE_WORKER_TRIM_MS", defaultWorkerTrim),
		start: envDuration("XMUSTARD_CORE_WORKER_START_MS", defaultWorkerStart),
	}
	if s.start <= 0 {
		s.start = defaultWorkerStart
	}
	return s
}

// workerKey identifies what a worker was started as: its command line, working
// directory, environment and the API's working directory. A one-shot child sees the
// environment of the moment it is started, so a worker started under another
// environment is replaced rather than reused.
type workerKey struct {
	name string
	args []string
	dir  string
	// env is the worker's environment: nil (inherit) or coreChildEnv's answer.
	env []string
	id  string
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
	// serve runs no external program (lsp-* and run-* stay one-shot), so it gets the
	// same environment as a one-shot core: the Linux allocator cap included.
	env := coreChildEnv("serve")
	seen := env
	if seen == nil {
		seen = os.Environ()
	}
	h := fnv.New64a()
	for _, kv := range seen {
		h.Write([]byte(kv))
		h.Write([]byte{0})
	}
	cwd, _ := os.Getwd()
	id := strings.Join(append([]string{name, dir, cwd, strconv.FormatUint(h.Sum64(), 16)}, full...), "\x00")
	return workerKey{name: name, args: full, dir: dir, env: env, id: id}, true
}

// WorkerStats reports the resident worker's lifecycle counters.
type WorkerStats struct {
	Enabled       bool  `json:"enabled"`
	PID           int   `json:"pid,omitempty"`
	Starts        int64 `json:"starts"`
	StartFailures int64 `json:"start_failures"`
	Crashes       int64 `json:"crashes"`
	IdleExits     int64 `json:"idle_exits"`
	// WedgedRetires counts workers retired because an abandoned request kept running
	// past workerKillGrace.
	WedgedRetires int64 `json:"wedged_retires"`
	Calls         int64 `json:"calls"`
	Fallbacks     int64 `json:"fallbacks"`
	Cancels       int64 `json:"cancels"`
	// Recycles counts workers retired to give their memory back, by reason: the
	// governor's heavy_admission and over_soft_ceiling, the idle policy's runaway,
	// idle_tight and idle_over_line, and requested (RecycleCoreWorker). See
	// worker_governor.go.
	Recycles map[string]int64 `json:"recycles"`
	// RecyclePending is set while a busy worker is marked to retire when its calls end.
	RecyclePending bool `json:"recycle_pending"`
}

type workerCounters struct {
	starts, startFailures, crashes, idleExits, wedgedRetires atomic.Int64
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
	// what the last handshake for knownKey said runs in-process, so a call that
	// can only run one-shot does not start (or wait for) a worker.
	knownKey string
	known    workerResidency
	counters workerCounters
	// recycles counts recycles by reason; lastRecycled is the last recycled worker, so
	// a reclaim request can wait for its exit (worker_governor.go).
	recycles     map[string]int64
	lastRecycled *workerProc
	// pressureCheck is set while an idle-time pressure check runs.
	pressureCheck atomic.Bool
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
		WedgedRetires: s.counters.wedgedRetires.Load(),
		Calls:         s.counters.calls.Load(),
		Fallbacks:     s.counters.fallbacks.Load(),
		Cancels:       s.counters.cancels.Load(),
	}
	s.mu.Lock()
	if s.proc != nil {
		st.PID = s.proc.pid
		st.RecyclePending = s.proc.recycleAtIdle
	}
	st.Recycles = make(map[string]int64, len(s.recycles))
	for k, v := range s.recycles {
		st.Recycles[k] = v
	}
	s.mu.Unlock()
	return st
}

// RecycleCoreWorker retires the resident worker once its current calls finish, which
// returns its retained heap to the OS; the next call starts a fresh worker. It reports
// whether a worker was running. The budget governor recycles through
// reclaimCoreWorker instead (worker_governor.go), which does not start a second worker
// beside a busy one.
func RecycleCoreWorker() bool {
	s := coreWorker
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil {
		return false
	}
	if s.retireProc(p) {
		s.noteRecycle(p, reasonRequested)
	}
	return true
}

// retireProc detaches p, so the next call starts a fresh worker, and ends p once its
// current callers are done (release retires it when the last one leaves). It reports
// whether p was not already retiring.
func (s *workerSupervisor) retireProc(p *workerProc) bool {
	s.mu.Lock()
	if s.proc == p {
		s.proc = nil
	}
	first := !p.retiring
	p.retiring = true
	idle := p.active == 0
	s.mu.Unlock()
	if idle {
		p.retire()
	}
	return first
}

// mayRunResident reports whether sub with args may run on the worker, before one is
// started: from the last handshake for this key when there was one, otherwise from
// the one-shot hints. The handshake of the worker that takes the call decides.
func (s *workerSupervisor) mayRunResident(key workerKey, sub string, args []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.knownKey == key.id {
		return s.known.residentFor(sub, args)
	}
	return !oneShotHint(sub, args)
}

// oneShotOnly and oneShotFamilies mirror the one-shot entries of the core's
// subcommand table (rust-core/src/bin/xmustard-core.rs). They are hints for the time
// before the first handshake; TestWorkerOneShotHintsMatchTheCore keeps them from
// sending a resident call to the one-shot path.
var (
	oneShotOnly = map[string]bool{
		"lsp-document-symbols": true, "lsp-hover": true, "run-verification-command": true,
		"run-managed-command": true, "run-verification-profile": true, "goal": true,
		"semantic-search": true, "index": true,
	}
	oneShotFamilies = map[string]map[string]bool{
		"changetrack": {"index": true},
		"symbolgraph": {"build": true, "blast-radius": true},
	}
)

func oneShotHint(sub string, args []string) bool {
	if oneShotOnly[sub] {
		return true
	}
	return len(args) > 0 && oneShotFamilies[sub][args[0]]
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
				select {
				case <-p.done:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
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
			s.mu.Unlock()
			s.retireProc(p)
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
		s.knownKey, s.known = key.id, p.residency
		p.active++
		s.counters.starts.Add(1)
		s.mu.Unlock()
		return p, nil
	}
}

// release ends a caller's use of p. ok reports a completed call, which clears the
// crash backoff. When p goes idle it is retired if the governor asked for its memory
// while it was busy, or if it is above the runaway line; otherwise its idle exit and
// idle pressure check are armed, and the governor samples the tree
// (worker_governor.go).
func (s *workerSupervisor) release(p *workerProc, ok bool) {
	gov := budget.Gov // read on the caller's goroutine; the timers and checks keep it
	s.mu.Lock()
	p.active--
	p.lastUsed = time.Now()
	if ok {
		s.crashes = 0
	}
	retire := p.active == 0 && p.retiring
	idle := p.active == 0 && s.proc == p
	reason := ""
	if idle && p.recycleAtIdle {
		s.proc, p.retiring = nil, true
		retire, idle, reason = true, false, p.recycleReason
	}
	if idle && p.idleAfter > 0 {
		if p.idleTimer != nil {
			p.idleTimer.Stop()
		}
		p.idleTimer = time.AfterFunc(p.idleAfter, func() { s.idleCheck(p) })
	}
	if idle {
		s.armIdlePressureCheck(p, gov)
	}
	s.mu.Unlock()
	if reason != "" {
		s.noteRecycle(p, reason)
	}
	if retire {
		p.retire()
		return
	}
	if idle {
		s.checkRunaway(p)
		s.checkPressureAtIdle(gov)
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
	if p.pressureTimer != nil {
		p.pressureTimer.Stop()
	}
	if s.proc == p {
		s.proc = nil
	}
	if s.lastRecycled == p {
		s.lastRecycled = nil
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

// workerResidency is what a worker's handshake said it runs in-process.
type workerResidency struct {
	methods map[string]bool
	// oneShot lists, per resident family, the first arguments it runs one-shot.
	oneShot map[string]map[string]bool
}

// residentFor reports whether the worker runs sub with args in-process.
func (r workerResidency) residentFor(sub string, args []string) bool {
	if !r.methods[sub] {
		return false
	}
	return len(args) == 0 || !r.oneShot[sub][args[0]]
}

// workerProc is one running `xmustard-core serve` process.
type workerProc struct {
	key       string
	pid       int
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	untrack   func()
	residency workerResidency
	counters  *workerCounters
	idleAfter time.Duration
	// trimAfter is the worker's idle trim period, when the idle pressure check runs.
	trimAfter time.Duration
	sup       *workerSupervisor

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
	// pressureTimer runs the idle pressure check; recycleAtIdle marks a busy worker
	// the governor asked for memory, retired when its last call ends (recycleReason).
	pressureTimer *time.Timer
	recycleAtIdle bool
	recycleReason string

	// started is set once the handshake succeeded; an exit before that is a failed
	// start (handled by acquire), not a crash.
	started atomic.Bool
	retired atomic.Bool
}

type workerCall struct {
	id    int64
	scope *budget.Scope
	// limit is the largest result the caller takes (coreStdoutCap); 0 means only the
	// frame cap applies.
	limit     int
	resp      chan workerResult
	abandoned bool
	timer     *time.Timer
	// slot releases the call's ChildLimit slot. It runs once, when the worker has
	// ended the request: answered it, or exited. Guarded by the proc's mu.
	slot func()
}

// endSlot releases c's ChildLimit slot once. p.mu must be held.
func (c *workerCall) endSlot() {
	if c.slot != nil {
		c.slot()
		c.slot = nil
	}
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
	cmd.Env = key.env
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
		trimAfter: set.trim,
		sup:       s,
	}
	cmd.Stderr = &workerLog{proc: p}
	untrack, err := startTracked(cmd)
	if err != nil {
		return nil, err
	}
	noteCoreSpawn(cmd)
	p.untrack = untrack
	p.pid = cmd.Process.Pid
	go p.readLoop(bufio.NewReaderSize(stdout, 64<<10))

	hctx, cancel := context.WithTimeout(ctx, set.start)
	defer cancel()
	scope := budget.NewScope(nil)
	defer scope.Close()
	r := p.call(hctx, scope, nil, 0, "initialize", nil)
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
	p.residency.methods = make(map[string]bool, len(init.Methods))
	for _, m := range init.Methods {
		p.residency.methods[m] = true
	}
	p.residency.oneShot = make(map[string]map[string]bool, len(init.OneShot))
	for family, subs := range init.OneShot {
		p.residency.oneShot[family] = make(map[string]bool, len(subs))
		for _, sub := range subs {
			p.residency.oneShot[family][sub] = true
		}
	}
	p.started.Store(true)
	return p, nil
}

// call sends one request and waits for its answer or for ctx to end. It takes over
// slot, the caller's ChildLimit release (nil for none), and runs it once the worker
// has ended the request. An answer whose result is over limit (0: no limit but the
// frame cap) is drained unread and fails with an error wrapping budget.ErrTooLarge. A
// caller that gives up abandons the request (see abandon); its answer is discarded
// unread. A request that could not be sent fails with errWorkerUnsent.
func (p *workerProc) call(ctx context.Context, scope *budget.Scope, slot func(), limit int, method string, args []string) workerResult {
	c := &workerCall{scope: scope, limit: limit, resp: make(chan workerResult, 1), slot: slot}
	p.mu.Lock()
	if p.dead {
		err := p.exitErr
		c.endSlot()
		p.mu.Unlock()
		return workerResult{err: fmt.Errorf("%w: %v", errWorkerUnsent, err)}
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
		// The worker's stdin is gone, so the process is dead or dying. Fail this call
		// now, without waiting for the read loop to see the exit, and mark the process
		// unusable so later calls do not try it.
		p.mu.Lock()
		if p.pending[c.id] == c {
			delete(p.pending, c.id)
		}
		c.endSlot()
		if !p.dead {
			p.dead = true
			p.exitErr = fmt.Errorf("write request: %v", err)
		}
		p.mu.Unlock()
		p.kill()
		return workerResult{err: fmt.Errorf("%w: %v", errWorkerUnsent, err)}
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

// abandon marks c's caller gone, asks the worker to cancel it, and arms the watchdog:
// a request still unanswered workerKillGrace after it was abandoned retires the
// worker (see abandonedTooLong). c keeps its ChildLimit slot until the worker ends it.
func (p *workerProc) abandon(c *workerCall) {
	p.mu.Lock()
	if p.pending[c.id] != c {
		p.mu.Unlock()
		return
	}
	c.abandoned = true
	c.timer = time.AfterFunc(workerKillGrace, func() { p.abandonedTooLong(c) })
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

// abandonedTooLong runs workerKillGrace after c was abandoned. A handler that is
// still running cannot be interrupted, so the worker is retired and replaced: new
// calls go to a fresh worker, calls still running on this one finish, and this one
// exits (abandoned handler included) when the last of them is done.
func (p *workerProc) abandonedTooLong(c *workerCall) {
	p.mu.Lock()
	stillRunning := p.pending[c.id] == c && !p.dead
	p.mu.Unlock()
	if !stillRunning || p.sup == nil {
		return
	}
	if p.sup.retireProc(p) {
		p.counters.wedgedRetires.Add(1)
		log.Printf("rust-core worker %d: abandoned request %d is still running; retiring the worker", p.pid, c.id)
	}
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
	c.endSlot()
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
		c.endSlot()
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
	if p.sup != nil {
		p.sup.onExit(p, cause)
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
			// a notification: the watcher's `$/refresh.due` and `$/watch.state`
			if n > maxWorkerNotice {
				return fmt.Errorf("%w: %d-byte frame without a request id", errWorkerProtocol, n)
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(r, body); err != nil {
				return err
			}
			p.workerNotice(body)
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
			c.endSlot()
			c = nil
		}
		p.mu.Unlock()
		if c == nil {
			if _, err := r.Discard(n); err != nil {
				return err
			}
			continue
		}
		// A result over the caller's cap is the permanent error, as a one-shot stdout
		// over coreStdoutCap is; the frame is drained, so the stream stays in sync.
		if c.limit > 0 && n-resultEnvelope(id) > c.limit {
			if _, derr := r.Discard(n); derr != nil {
				return derr
			}
			p.deliver(c, workerResult{err: fmt.Errorf("%w: %d-byte response over the %d-byte output cap", budget.ErrTooLarge, n, c.limit)})
			continue
		}
		// Admission before allocation: reserve the body against the caller's scope. A
		// body the pool could never hold (budget.ErrNeverFits) stays permanent; any
		// other refusal is the retryable overload.
		if err := c.scope.Acquire(int64(n)); err != nil {
			if _, derr := r.Discard(n); derr != nil {
				return derr
			}
			if !errors.Is(err, budget.ErrTooLarge) {
				err = budget.ErrOverloaded
			}
			p.deliver(c, workerResult{err: err})
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

// resultEnvelope is the size of a result frame's JSON-RPC wrapping for id, so a frame's
// result can be held to the one-shot output cap before it is read.
func resultEnvelope(id int64) int {
	return len(`{"jsonrpc":"2.0","id":`) + len(strconv.FormatInt(id, 10)) + len(`,"result":`) + len(`}`)
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
	if !s.mayRunResident(key, sub, args) {
		return nil, false, nil
	}
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
	if !p.residency.residentFor(sub, args) {
		return nil, false, nil
	}
	// The slot is handed to the call, which releases it when the worker has ended the
	// request: at once for an answered call, later for an abandoned one that is still
	// running, so admission counts all Rust work in flight.
	slot, err := budget.Children.Acquire(parent)
	if err != nil {
		return nil, true, fmt.Errorf("rust-core %s: %w", sub, err)
	}
	scope, owned := budget.ScopeFor(parent)
	if owned {
		defer scope.Close()
	}
	s.counters.calls.Add(1)
	r := p.call(ctx, scope, slot, coreStdoutCap(scope, !owned), sub, args)
	switch {
	case r.err != nil:
		if cerr := callerError(parent, ctx, sub); cerr != nil {
			return nil, true, cerr
		}
		if errors.Is(r.err, errWorkerUnsent) {
			// nothing ran: the one-shot path takes the call; the next call restarts
			// the worker.
			log.Printf("rust-core %s: worker %d unavailable (%v); running one-shot", sub, p.pid, r.err)
			s.counters.fallbacks.Add(1)
			return nil, false, nil
		}
		if errors.Is(r.err, budget.ErrTooLarge) {
			log.Printf("rust-core %s: worker output dropped: %v", sub, r.err)
			return nil, true, fmt.Errorf("rust-core %s: output too large", sub)
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
			if errors.Is(err, budget.ErrTooLarge) { // this request can never hold it
				return nil, true, fmt.Errorf("rust-core %s: output too large", sub)
			}
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
