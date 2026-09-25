package rustcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// TestMain lets the test binary stand in for xmustard-core: with
// XMUSTARD_FAKE_WORKER set it runs fakeCore instead of the tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("XMUSTARD_FAKE_WORKER"); mode != "" {
		os.Exit(fakeCore(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeCore is a scripted xmustard-core. `serve` speaks the worker protocol with test
// methods; any other subcommand is a one-shot call printing {"oneshot":"<sub>"}.
// Starts, one-shot calls, cancels and concurrency are appended to
// XMUSTARD_FAKE_WORKER_LOG.
func fakeCore(mode string, args []string) int {
	record := func(line string) {
		f, err := os.OpenFile(os.Getenv("XMUSTARD_FAKE_WORKER_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, line)
			f.Close()
		}
	}
	if len(args) == 0 || args[0] != "serve" {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		record("oneshot " + sub)
		fmt.Printf("{\"oneshot\":%q}\n", sub)
		return 0
	}
	record(fmt.Sprintf("serve %d", os.Getpid()))
	switch mode {
	case "garbage":
		os.Stdout.WriteString("this is not a frame\n")
		return 0
	case "exit":
		return 1
	case "nohandshake":
		time.Sleep(time.Hour)
		return 0
	}
	in := bufio.NewReaderSize(os.Stdin, 64<<10)
	handshaken := false
	var wmu sync.Mutex
	send := func(id int64, body string) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = writeWorkerFrame(os.Stdout, id, true, []byte(body))
	}
	var cancels sync.Map
	var running atomic.Int64
	for {
		n, _, _, err := readWorkerHeader(in)
		if err != nil {
			return 0
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(in, body); err != nil {
			return 0
		}
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Args []string `json:"args"`
				ID   int64    `json:"id"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return 2
		}
		if req.Method == "$/cancelRequest" {
			record(fmt.Sprintf("cancel %d", req.Params.ID))
			if ch, ok := cancels.LoadAndDelete(req.Params.ID); ok {
				close(ch.(chan struct{}))
			}
			continue
		}
		id := *req.ID
		if mode == "deaf" && handshaken {
			// Answer this request, then live on without the request pipe, as a worker
			// that died before its exit was noticed: the supervisor's next write fails.
			// stdin closes before the answer is sent, so no later request can be
			// written into a pipe that nobody reads.
			b, _ := json.Marshal(req.Params.Args)
			os.Stdin.Close()
			send(id, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, b))
			time.Sleep(time.Hour)
		}
		handshaken = handshaken || req.Method == "initialize"
		cancelled := make(chan struct{})
		cancels.Store(id, cancelled)
		go func() {
			result := func(v string) {
				send(id, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, v))
			}
			fail := func(code int, msg, data string) {
				send(id, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":%q,"data":%s}}`, id, code, msg, data))
			}
			arg := func(i int) string {
				if i < len(req.Params.Args) {
					return req.Params.Args[i]
				}
				return ""
			}
			switch req.Method {
			case "initialize":
				proto := 1
				if mode == "badproto" {
					proto = 99
				}
				result(fmt.Sprintf(`{"protocol":%d,"pid":%d,"methods":["echo","sleep","hang","crash","big","fail","conc","notresident","family","env"],"one_shot_subcommands":{"family":["heavy"]}}`, proto, os.Getpid()))
			case "echo", "family":
				b, _ := json.Marshal(req.Params.Args)
				result(string(b))
			case "env":
				v, ok := os.LookupEnv(arg(0))
				if !ok {
					v = "unset"
				}
				b, _ := json.Marshal(v)
				result(string(b))
			case "sleep":
				ms, _ := strconv.Atoi(arg(0))
				select {
				case <-time.After(time.Duration(ms) * time.Millisecond):
					result(`"slept"`)
				case <-cancelled:
					fail(rpcRequestCancelled, "request cancelled", "null")
				}
			case "hang":
				// ignores cancellation, like a handler that cannot be interrupted.
				time.Sleep(time.Hour)
			case "crash":
				os.Exit(3)
			case "big":
				n, _ := strconv.Atoi(arg(0))
				result(`"` + strings.Repeat("a", n) + `"`)
			case "fail":
				fail(rpcCommandFailed, "explain failed: /home/user/.ssh/id_rsa unreadable", `{"exit_code":1}`)
			case "conc":
				record(fmt.Sprintf("conc %d", running.Add(1)))
				ms, _ := strconv.Atoi(arg(0))
				time.Sleep(time.Duration(ms) * time.Millisecond)
				running.Add(-1)
				result(`"done"`)
			case "notresident":
				fail(rpcMethodNotFound, "notresident runs one-shot only", `{"reason":"not_resident"}`)
			default:
				fail(rpcMethodNotFound, "unknown command", "null")
			}
		}()
	}
}

// resetWorker ends the current worker and clears the supervisor between tests.
func resetWorker() {
	s := coreWorker
	s.mu.Lock()
	p := s.proc
	s.proc = nil
	s.retryKey, s.retryAt, s.startFails, s.crashes = "", time.Time{}, 0, 0
	s.knownKey, s.known = "", workerResidency{}
	s.recycles, s.lastRecycled = nil, nil
	if p != nil {
		p.retiring = true
	}
	s.mu.Unlock()
	if p != nil {
		p.kill()
		<-p.done
	}
}

// useFakeWorker points the bridge at fakeCore in mode with the worker on, and
// returns the fake's log path.
func useFakeWorker(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("XMUSTARD_CORE_BIN", exe)
	t.Setenv("XMUSTARD_FAKE_WORKER", mode)
	t.Setenv("XMUSTARD_FAKE_WORKER_LOG", logPath)
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	// The fake worker is this test binary, whose memory says nothing about a real
	// worker's lines; the line recycles are tested on their own.
	prevPeak, prevRunaway := workerPeakLine, workerRunawayLine
	workerPeakLine, workerRunawayLine = 1<<40, 1<<40
	t.Cleanup(func() { workerPeakLine, workerRunawayLine = prevPeak, prevRunaway })
	resetWorker()
	t.Cleanup(resetWorker)
	return logPath
}

func fakeLog(t *testing.T, path string, prefix string) []string {
	t.Helper()
	raw, _ := os.ReadFile(path)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	return out
}

func withChildren(t *testing.T, n int, wait time.Duration) *budget.ChildLimit {
	t.Helper()
	prev := budget.Children
	l := budget.NewChildLimit(n, wait)
	budget.Children = l
	t.Cleanup(func() { budget.Children = prev })
	return l
}

func processGone(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) != nil
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustEcho(t *testing.T, arg string) {
	t.Helper()
	out, err := runCoreCtx(context.Background(), "echo", arg)
	if err != nil {
		t.Fatalf("echo %q: %v", arg, err)
	}
	if want := `["` + arg + `"]`; string(out) != want {
		t.Fatalf("echo %q returned %s, want %s", arg, out, want)
	}
}

// ---- framing ----

func TestWorkerFrameRoundTripAndCaps(t *testing.T) {
	var buf bytes.Buffer
	if err := writeWorkerFrame(&buf, 7, true, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "Content-Length: 7\r\nXmustard-Id: 7\r\n\r\n{\"a\":1}" {
		t.Fatalf("frame bytes %q", got)
	}
	r := bufio.NewReaderSize(&buf, 64<<10)
	n, id, hasID, err := readWorkerHeader(r)
	if err != nil || n != 7 || id != 7 || !hasID {
		t.Fatalf("header = %d %d %v %v", n, id, hasID, err)
	}
	_, _ = r.Discard(n)
	if _, _, _, err := readWorkerHeader(r); !errors.Is(err, io.EOF) {
		t.Fatalf("clean end of input: want io.EOF, got %v", err)
	}
	bad := map[string]string{
		"line cap":      "X-Pad: " + strings.Repeat("a", maxWorkerHeaderLen) + "\r\nContent-Length: 0\r\n\r\n",
		"flood":         strings.Repeat("z", 1<<20),
		"header count":  strings.Repeat("X: 1\r\n", maxWorkerHeaders+1) + "Content-Length: 0\r\n\r\n",
		"no length":     "Xmustard-Id: 1\r\n\r\n",
		"bad length":    "Content-Length: nope\r\n\r\n",
		"negative":      "Content-Length: -4\r\n\r\n",
		"no colon":      "garbage line\r\n\r\n",
		"frame limit":   fmt.Sprintf("Content-Length: %d\r\n\r\n", maxWorkerFrame+1),
		"bad id":        "Content-Length: 1\r\nXmustard-Id: x\r\n\r\n",
		"truncated hdr": "Content-Length: 1\r\n",
	}
	for name, raw := range bad {
		_, _, _, err := readWorkerHeader(bufio.NewReaderSize(strings.NewReader(raw), 64<<10))
		if err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%s: want a protocol error, got %v", name, err)
		}
	}
	okAtLimit := "X-Pad: " + strings.Repeat("a", maxWorkerHeaderLen-7) + "\r\nContent-Length: 0\r\n\r\n"
	if _, _, _, err := readWorkerHeader(bufio.NewReaderSize(strings.NewReader(okAtLimit), 64<<10)); err != nil {
		t.Fatalf("a header line at the cap is valid: %v", err)
	}
	limit := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxWorkerFrame)
	if n, _, _, err := readWorkerHeader(bufio.NewReaderSize(strings.NewReader(limit), 64<<10)); err != nil || n != maxWorkerFrame {
		t.Fatalf("a %d-byte frame is within the limit: %d %v", maxWorkerFrame, n, err)
	}
}

func TestWorkerResponseParsing(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":12,"result":{"hits":[1,2]}}`)
	r, err := parseWorkerResponse(body, 12)
	if err != nil || string(r.result) != `{"hits":[1,2]}` {
		t.Fatalf("fast path: %s %v", r.result, err)
	}
	if &r.result[0] != &body[len(`{"jsonrpc":"2.0","id":12,"result":`)] {
		t.Fatal("fast path must slice the frame, not copy it")
	}
	r, err = parseWorkerResponse([]byte(`{"id":3,"jsonrpc":"2.0","result":[true]}`), 3)
	if err != nil || string(r.result) != `[true]` {
		t.Fatalf("decoded path: %s %v", r.result, err)
	}
	r, err = parseWorkerResponse([]byte(`{"jsonrpc":"2.0","id":4,"error":{"code":-32001,"message":"m","data":{"exit_code":2}}}`), 4)
	if err != nil || r.rpcErr == nil || r.rpcErr.Code != rpcCommandFailed {
		t.Fatalf("error response: %+v %v", r, err)
	}
	if _, err := parseWorkerResponse([]byte(`{"jsonrpc":"2.0","id":5,"result":1}`), 6); !errors.Is(err, errWorkerProtocol) {
		t.Fatalf("mismatched id: want protocol error, got %v", err)
	}
	if _, err := parseWorkerResponse([]byte(`not json`), 1); !errors.Is(err, errWorkerProtocol) {
		t.Fatalf("garbage: want protocol error, got %v", err)
	}
}

// ---- lifecycle against the fake worker ----

func TestWorkerServesCallsFromOneProcess(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	before := CoreWorkerStats()
	for i := 0; i < 25; i++ {
		mustEcho(t, "call-"+strconv.Itoa(i))
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 {
		t.Fatalf("25 calls started %d workers, want 1", n)
	}
	if n := len(fakeLog(t, logPath, "oneshot ")); n != 0 {
		t.Fatalf("25 calls made %d one-shot execs, want 0", n)
	}
	after := CoreWorkerStats()
	if after.Calls-before.Calls != 25 || after.Starts-before.Starts != 1 || after.PID == 0 {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
}

func TestWorkerCrashMidRequestFailsTheCallAndTheNextCallRestarts(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	_, err := runCoreCtx(context.Background(), "crash")
	if err == nil || err.Error() != "rust-core crash failed" {
		t.Fatalf("crash mid-request: want a sanitized failure, got %v", err)
	}
	waitFor(t, "the crashed worker to be reaped", 3*time.Second, func() bool { return processGone(first) })
	mustEcho(t, "after-crash")
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want a restart after the crash: %d starts", n)
	}
	after := CoreWorkerStats()
	if after.Crashes-before.Crashes != 1 || after.PID == first {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
}

func TestWorkerCancelSendsCancelAndFreesTheSlotWhenTheWorkEnds(t *testing.T) {
	slots := withChildren(t, 1, 200*time.Millisecond)
	logPath := useFakeWorker(t, "ok")
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	cancelled := func(method string) time.Time {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := runCoreCtx(ctx, method, "60000")
			done <- err
		}()
		waitFor(t, "the call to hold the slot", 3*time.Second, func() bool { return slots.InUse() == 1 })
		at := time.Now()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: cancelled call returned %v", method, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: a cancelled caller must return at once", method)
		}
		return at
	}

	// A handler that stops on the cancel message ends at once, and so does its slot.
	cancelled("sleep")
	waitFor(t, "the cancelled request to end and free its slot", time.Second, func() bool { return slots.InUse() == 0 })
	waitFor(t, "the cancel message", 3*time.Second, func() bool { return len(fakeLog(t, logPath, "cancel ")) == 1 })
	mustEcho(t, "after-sleep")

	// A handler that ignores it keeps running, and keeps its slot: with a limit of one,
	// no other Rust work is admitted until the worker has been retired and replaced.
	at := cancelled("hang")
	waitFor(t, "the cancel message", 3*time.Second, func() bool { return len(fakeLog(t, logPath, "cancel ")) == 2 })
	if slots.InUse() != 1 {
		t.Fatal("an abandoned request that is still running must keep its slot")
	}
	if _, err := runCoreCtx(context.Background(), "echo", "blocked"); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("a call beyond the limit while abandoned work runs: want ErrOverloaded, got %v", err)
	}
	waitFor(t, "the wedged worker to exit and free the slot", workerKillGrace+3*time.Second, func() bool {
		return slots.InUse() == 0 && processGone(first)
	})
	if waited := time.Since(at); waited < workerKillGrace {
		t.Fatalf("the worker was retired %v after the cancel, before the %v grace", waited, workerKillGrace)
	}
	mustEcho(t, "after-hang")
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want one replacement worker: %d starts", n)
	}
	after := CoreWorkerStats()
	if after.WedgedRetires-before.WedgedRetires != 1 || after.Crashes != before.Crashes || after.PID == first {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
	if slots.Peak() > 1 {
		t.Fatalf("admission let %d calls run at once with a limit of 1", slots.Peak())
	}
}

func TestWorkerRequestPastItsDeadlineRetiresAndReplacesTheWorker(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runCoreCtx(ctx, "hang")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's deadline error, got %v", err)
	}
	waitFor(t, "the wedged worker to exit", workerKillGrace+3*time.Second, func() bool { return processGone(first) })
	after := CoreWorkerStats()
	if after.WedgedRetires-before.WedgedRetires != 1 || after.Crashes != before.Crashes {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
	mustEcho(t, "after-retire")
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want a new worker after the retire: %d starts", n)
	}
}

func TestWorkerRetiredForAnAbandonedRequestLetsOtherCallsFinish(t *testing.T) {
	withChildren(t, 4, time.Second)
	logPath := useFakeWorker(t, "ok")
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	other := make(chan error, 1)
	go func() {
		out, err := runCoreCtx(context.Background(), "sleep", "5000")
		if err == nil && string(out) != `"slept"` {
			err = fmt.Errorf("unexpected output %s", out)
		}
		other <- err
	}()
	waitFor(t, "the unrelated call to start", 3*time.Second, func() bool {
		coreWorker.mu.Lock()
		defer coreWorker.mu.Unlock()
		return coreWorker.proc != nil && coreWorker.proc.active == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runCoreCtx(ctx, "hang"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's deadline error, got %v", err)
	}
	waitFor(t, "the worker to be retired", workerKillGrace+2*time.Second, func() bool {
		return CoreWorkerStats().WedgedRetires-before.WedgedRetires == 1
	})
	// new calls go to a fresh worker while the retired one finishes its running call.
	mustEcho(t, "fresh")
	if pid := CoreWorkerStats().PID; pid == first || pid == 0 {
		t.Fatalf("a call after the retire ran on worker %d (retired: %d)", pid, first)
	}
	if processGone(first) {
		t.Fatal("the retired worker must stay until its running call has finished")
	}
	if err := <-other; err != nil {
		t.Fatalf("an unrelated call on the retired worker failed: %v", err)
	}
	waitFor(t, "the retired worker to exit", 3*time.Second, func() bool { return processGone(first) })
	if after := CoreWorkerStats(); after.Crashes != before.Crashes {
		t.Fatalf("a retire is not a crash: before %+v after %+v", before, after)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want one replacement worker: %d starts", n)
	}
}

func TestWorkerThatCannotBeReachedSendsTheCallOneShot(t *testing.T) {
	logPath := useFakeWorker(t, "deaf")
	mustEcho(t, "warm")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	start := time.Now()
	out, err := runCoreCtx(context.Background(), "echo", "x")
	if err != nil || strings.TrimSpace(string(out)) != `{"oneshot":"echo"}` {
		t.Fatalf("a request the worker never received must run one-shot: %s %v", out, err)
	}
	// it returns as soon as the write fails (plus one one-shot exec), not at its
	// deadline after waiting for the read loop.
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the unsent call waited %v", d)
	}
	waitFor(t, "the unreachable worker to be ended", 3*time.Second, func() bool { return processGone(first) })
	after := CoreWorkerStats()
	if after.Fallbacks-before.Fallbacks != 1 {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
	// the next call starts a new worker (which is deaf too, after its handshake).
	if _, err := runCoreCtx(context.Background(), "echo", "y"); err != nil {
		t.Fatal(err)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want a restart after the unreachable worker: %d starts", n)
	}
}

func TestWorkerIsNotStartedForOneShotOnlyCalls(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_WORKER_START_MS", "1500")
	logPath := useFakeWorker(t, "nohandshake")
	before := CoreWorkerStats()
	for _, call := range [][]string{
		{"lsp-hover", "x"}, {"goal", "list"}, {"symbolgraph", "build", "/r", "ws"},
		{"symbolgraph", "blast-radius", "/r", "ws", "X"}, {"changetrack", "index", "/d", "/r", "ws"},
	} {
		out, err := runCoreCtx(context.Background(), call[0], call[1:]...)
		if err != nil || strings.TrimSpace(string(out)) != fmt.Sprintf(`{"oneshot":%q}`, call[0]) {
			t.Fatalf("%v: want the one-shot result, got %s %v", call, out, err)
		}
	}
	// a start attempt would have waited out the 1.5 s handshake bound and counted a
	// start failure and a fallback.
	if n := len(fakeLog(t, logPath, "serve ")); n != 0 {
		t.Fatalf("one-shot-only calls started %d workers", n)
	}
	after := CoreWorkerStats()
	if after.StartFailures != before.StartFailures || after.Fallbacks != before.Fallbacks {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
}

func TestWorkerExitsWhenIdleAndRestartsLazily(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_WORKER_IDLE_MS", "150")
	logPath := useFakeWorker(t, "ok")
	before := CoreWorkerStats()
	mustEcho(t, "one")
	first := CoreWorkerStats().PID
	waitFor(t, "the idle worker to exit", 3*time.Second, func() bool { return processGone(first) })
	if CoreWorkerStats().PID != 0 {
		t.Fatal("an idle-exited worker is still registered")
	}
	mustEcho(t, "two")
	after := CoreWorkerStats()
	if after.IdleExits-before.IdleExits != 1 || after.Crashes != before.Crashes {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want a lazy restart after idle exit: %d starts", n)
	}
}

func TestWorkerConcurrencyIsBoundedByChildLimit(t *testing.T) {
	withChildren(t, 2, 10*time.Second)
	logPath := useFakeWorker(t, "ok")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runCoreCtx(context.Background(), "conc", "150")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	peak := 0
	for _, line := range fakeLog(t, logPath, "conc ") {
		if n, _ := strconv.Atoi(strings.TrimPrefix(line, "conc ")); n > peak {
			peak = n
		}
	}
	if peak < 1 || peak > 2 {
		t.Fatalf("worker ran %d requests at once with a child limit of 2", peak)
	}
}

// With 1 MiB of a 5 MiB pool free, a 4 MiB response (which would fit an idle pool) is
// refused as a retryable overload before it is read, as the one-shot capture is.
func TestWorkerAdmitsResponseBytesBeforeReadingThem(t *testing.T) {
	pool := busyPool(t, busyMax, busyHeld)
	logPath := useFakeWorker(t, "ok")
	_, err := runCoreCtx(context.Background(), "big", strconv.Itoa(4<<20))
	if !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("4 MiB response with 1 MiB of the pool free: want ErrOverloaded, got %v", err)
	}
	if pool.Peak() > pool.Max() || pool.InUse() != busyHeld {
		t.Fatalf("pool peak %d max %d in use %d (held elsewhere %d)", pool.Peak(), pool.Max(), pool.InUse(), busyHeld)
	}
	// the refused frame was drained, so the stream is still in sync.
	mustEcho(t, "in-sync")
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 {
		t.Fatalf("a refused response must not restart the worker: %d starts", n)
	}
}

// WS-06 on the worker path: output the pool could never hold is the permanent "output
// too large" error, with the same pool-derived caps as the one-shot path
// (TestCoreOutputThePoolCanNeverHoldIsPermanent), and the drained frame leaves the
// stream in sync.
func TestWorkerOutputThePoolCanNeverHoldIsPermanent(t *testing.T) {
	pool := withPool(t, budget.DefaultTransientBudgetBytes)
	logPath := useFakeWorker(t, "ok")
	request := func() (context.Context, func()) {
		s := budget.NewScope(nil)
		return budget.WithScope(context.Background(), s), s.Close
	}
	for _, tc := range []struct {
		name   string
		bytes  int
		scoped bool
		ok     bool
	}{
		{"5 MiB in a request", 5 << 20, true, true},
		{"10 MiB in a request", 10 << 20, true, false},
		{"10 MiB owned", 10 << 20, false, true},
		{"30 MiB owned", 30 << 20, false, false},
	} {
		ctx, done := context.Background(), func() {}
		if tc.scoped {
			ctx, done = request()
		}
		out, err := runCoreCtx(ctx, "big", strconv.Itoa(tc.bytes))
		done()
		if tc.ok && (err != nil || len(out) != tc.bytes+2) {
			t.Fatalf("%s: %d bytes, %v", tc.name, len(out), err)
		}
		if !tc.ok && (err == nil || errors.Is(err, budget.ErrOverloaded) || !strings.Contains(err.Error(), "output too large")) {
			t.Fatalf("%s: want the permanent output-too-large error, got %v", tc.name, err)
		}
		if pool.InUse() != 0 {
			t.Fatalf("%s: %d bytes leaked", tc.name, pool.InUse())
		}
		mustEcho(t, "in-sync")
	}
	// under an idle 1 MiB pool a 4 MiB answer can never be held
	withPool(t, 1<<20)
	if _, err := runCoreCtx(context.Background(), "big", strconv.Itoa(4<<20)); err == nil || errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("4 MiB under an idle 1 MiB pool: want the permanent output-too-large error, got %v", err)
	}
	mustEcho(t, "in-sync")
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 {
		t.Fatalf("a dropped response must not restart the worker: %d starts", n)
	}
}

// WS-06 on the worker path: the worker is started with the one-shot core's environment
// (the Linux allocator cap, stood in here on any platform), and its start is the one
// core spawn; the calls it answers start no process.
func TestWorkerGetsTheCoreEnvironmentAndCountsOneSpawn(t *testing.T) {
	t.Setenv("MALLOC_ARENA_MAX", "")
	os.Unsetenv("MALLOC_ARENA_MAX")
	prev := childEnv
	childEnv = func() []string { return append(os.Environ(), "MALLOC_ARENA_MAX=2") }
	t.Cleanup(func() { childEnv = prev })
	logPath := useFakeWorker(t, "ok")
	before := budget.Counters()
	for i := 0; i < 5; i++ {
		out, err := runCoreCtx(context.Background(), "env", "MALLOC_ARENA_MAX")
		if err != nil || string(out) != `"2"` {
			t.Fatalf("the worker must get the core's allocator cap: %s %v", out, err)
		}
	}
	after := budget.Counters()
	if got := after.Spawns["core"] - before.Spawns["core"]; got != 1 {
		t.Fatalf("five worker calls counted %d core spawns, want 1 (the worker start)", got)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 1 {
		t.Fatalf("want one worker: %d starts", n)
	}
}

func TestWorkerCommandErrorsAreSanitized(t *testing.T) {
	useFakeWorker(t, "ok")
	_, err := runCoreCtx(context.Background(), "fail")
	if err == nil || err.Error() != "rust-core fail failed" {
		t.Fatalf("want a sanitized failure, got %v", err)
	}
	mustEcho(t, "still-up")
}

func TestWorkerFallsBackToOneShotWhenItCannotStart(t *testing.T) {
	for _, mode := range []string{"garbage", "exit", "badproto"} {
		t.Run(mode, func(t *testing.T) {
			logPath := useFakeWorker(t, mode)
			before := CoreWorkerStats()
			set := readWorkerSettings()
			key, _ := workerKeyFor(set)
			if _, err := coreWorker.acquire(context.Background(), key, set); err == nil || errors.Is(err, errWorkerBackoff) {
				t.Fatalf("want a failed start, got %v", err)
			}
			// a failed start is retried only after a backoff; meanwhile calls take the
			// one-shot path.
			if _, err := coreWorker.acquire(context.Background(), key, set); !errors.Is(err, errWorkerBackoff) {
				t.Fatalf("an immediate retry must back off, got %v", err)
			}
			out, err := runCoreCtx(context.Background(), "echo", "x")
			if err != nil || strings.TrimSpace(string(out)) != `{"oneshot":"echo"}` {
				t.Fatalf("want the one-shot result, got %s %v", out, err)
			}
			starts := fakeLog(t, logPath, "serve ")
			if len(starts) != 1 {
				t.Fatalf("want one start attempt, got %d", len(starts))
			}
			pid, _ := strconv.Atoi(strings.TrimPrefix(starts[0], "serve "))
			waitFor(t, "the failed worker to be gone", 3*time.Second, func() bool { return processGone(pid) })
			after := CoreWorkerStats()
			if after.StartFailures-before.StartFailures != 1 || after.Fallbacks-before.Fallbacks != 1 ||
				after.Crashes != before.Crashes {
				t.Fatalf("stats before %+v after %+v", before, after)
			}
		})
	}
}

func TestWorkerLeavesOneShotOnlyCommandsToTheOneShotPath(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	// not advertised, refused as not_resident, and a family's one-shot subcommand.
	for _, call := range [][]string{{"lsp-hover"}, {"notresident"}, {"family", "heavy"}} {
		out, err := runCoreCtx(context.Background(), call[0], call[1:]...)
		if err != nil || strings.TrimSpace(string(out)) != fmt.Sprintf(`{"oneshot":%q}`, call[0]) {
			t.Fatalf("%v: want the one-shot result, got %s %v", call, out, err)
		}
	}
	if n := len(fakeLog(t, logPath, "oneshot ")); n != 3 {
		t.Fatalf("want 3 one-shot execs, got %d", n)
	}
	// the family's other subcommands stay resident.
	out, err := runCoreCtx(context.Background(), "family", "light")
	if err != nil || string(out) != `["light"]` {
		t.Fatalf("resident family call: %s %v", out, err)
	}
	if n := len(fakeLog(t, logPath, "oneshot ")); n != 3 {
		t.Fatalf("a resident family call must not exec: %d one-shot execs", n)
	}
}

func TestWorkerCancelledDuringStartIsKilled(t *testing.T) {
	logPath := useFakeWorker(t, "nohandshake")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runCoreCtx(ctx, "echo", "x")
		done <- err
	}()
	waitFor(t, "the worker to start", 5*time.Second, func() bool { return len(fakeLog(t, logPath, "serve ")) == 1 })
	pid, _ := strconv.Atoi(strings.TrimPrefix(fakeLog(t, logPath, "serve ")[0], "serve "))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled start did not return")
	}
	waitFor(t, "the half-started worker to be killed", 3*time.Second, func() bool { return processGone(pid) })
}

func TestWorkerIsReplacedWhenItsEnvironmentChanges(t *testing.T) {
	useFakeWorker(t, "ok")
	mustEcho(t, "a")
	first := CoreWorkerStats().PID
	t.Setenv("XMUSTARD_INDEX_TRUST_SCOPE", "changed-"+strconv.Itoa(first))
	mustEcho(t, "b")
	second := CoreWorkerStats().PID
	if second == first || second == 0 {
		t.Fatalf("worker %d was reused under a new environment (now %d)", first, second)
	}
	waitFor(t, "the old worker to retire", 3*time.Second, func() bool { return processGone(first) })
}

func TestRecycleCoreWorkerRetiresAfterCurrentCalls(t *testing.T) {
	logPath := useFakeWorker(t, "ok")
	if RecycleCoreWorker() {
		t.Fatal("no worker is running yet")
	}
	mustEcho(t, "a")
	first := CoreWorkerStats().PID
	before := CoreWorkerStats()
	// a call in flight finishes on the old worker; the recycle waits for it.
	done := make(chan error, 1)
	go func() {
		out, err := runCoreCtx(context.Background(), "sleep", "300")
		if err == nil && string(out) != `"slept"` {
			err = fmt.Errorf("unexpected output %s", out)
		}
		done <- err
	}()
	waitFor(t, "the call to start", 3*time.Second, func() bool {
		coreWorker.mu.Lock()
		defer coreWorker.mu.Unlock()
		return coreWorker.proc != nil && coreWorker.proc.active == 1
	})
	if !RecycleCoreWorker() {
		t.Fatal("a running worker must be recycled")
	}
	if err := <-done; err != nil {
		t.Fatalf("the in-flight call must complete on the recycled worker: %v", err)
	}
	waitFor(t, "the recycled worker to exit", 3*time.Second, func() bool { return processGone(first) })
	mustEcho(t, "b")
	after := CoreWorkerStats()
	if after.PID == first || after.Crashes != before.Crashes || after.IdleExits != before.IdleExits {
		t.Fatalf("stats before %+v after %+v", before, after)
	}
	if n := len(fakeLog(t, logPath, "serve ")); n != 2 {
		t.Fatalf("want a fresh worker after the recycle: %d starts", n)
	}
}

func TestWorkerIsTrackedForShutdown(t *testing.T) {
	useFakeWorker(t, "ok")
	mustEcho(t, "a")
	pid := CoreWorkerStats().PID
	if n := KillActiveChildren(); n < 1 {
		t.Fatalf("the resident worker is not tracked: killed %d", n)
	}
	waitFor(t, "shutdown to end the worker", 3*time.Second, func() bool { return processGone(pid) })
}

// ---- the real xmustard-core ----

// realCore returns a built xmustard-core that supports `serve`, or skips.
func realCore(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("XMUSTARD_CORE_BIN"), filepath.Join(rustCoreDir(), "target", "release", "xmustard-core")}
	for _, bin := range candidates {
		if bin == "" {
			continue
		}
		// serve's own usage error proves the binary has the worker; an older binary
		// answers "unknown command: serve".
		out, _ := exec.Command(bin, "serve", "--bogus=1").CombinedOutput()
		if strings.HasPrefix(string(out), "usage: xmustard-core serve") {
			return bin
		}
	}
	t.Skip("no xmustard-core with `serve` built (cd rust-core && cargo build --release)")
	return ""
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"src/engine.go":  "package engine\n\nfunc ComputeTotal(a int) int { return helperValue(a) }\n\nfunc helperValue(a int) int { return a + 1 }\n",
		"src/handler.go": "package engine\n\nfunc HandleRequest() int { return ComputeTotal(2) }\n",
		"web/view.ts":    "export function renderWidget(): number { return 1 }\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-qm", "c"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

// normalizeCoreJSON drops fields that differ between two runs of one query: clock
// readings, per-call ids and per-call cache accounting.
func normalizeCoreJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %.200s: %v", raw, err)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if strings.HasSuffix(k, "_at") || k == "elapsed_ms" || k == "work" || k == "result_id" {
					delete(x, k)
					continue
				}
				x[k] = walk(child)
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	return walk(v)
}

func TestWorkerMatchesOneShotWithTheRealCoreAndSpawnsOnce(t *testing.T) {
	core := realCore(t)
	resetWorker()
	t.Cleanup(resetWorker)
	dir := t.TempDir()
	execLog := filepath.Join(dir, "execs.log")
	wrapper := filepath.Join(dir, "xmustard-core")
	script := "#!/bin/sh\necho \"$1\" >> '" + execLog + "'\nexec '" + core + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", wrapper)
	root := gitFixture(t)
	data := t.TempDir()
	calls := [][]string{
		{"repo-key", root},
		{"search", root, "ws", "ComputeTotal", "5"},
		{"symbolgraph", "impact", root, "ws", "ComputeTotal", "3"},
		{"symbolgraph", "trace", root, "ws", "HandleRequest", "helperValue"},
		{"symbolgraph", "clusters", root, "ws"},
		{"symbolgraph", "hotspots", root, "ws", "5"},
		{"ownership", "subsystems", root, "ws"},
		{"explain-path", "ws", root, "src/engine.go"},
		{"path-symbols", "ws", root, "web/view.ts"},
		{"build-repo-map", "ws", root},
		{"changetrack", "working-changes", data, root, "ws"},
	}
	execs := func() []string {
		raw, _ := os.ReadFile(execLog)
		return strings.Fields(string(raw))
	}
	t.Setenv("XMUSTARD_CORE_WORKER", "0")
	oneShot := make([][]byte, len(calls))
	for i, c := range calls {
		out, err := runCoreCtx(context.Background(), c[0], c[1:]...)
		if err != nil {
			t.Fatalf("one-shot %v: %v", c, err)
		}
		oneShot[i] = out
	}
	if n := len(execs()); n != len(calls) {
		t.Fatalf("one-shot path: %d execs for %d calls", n, len(calls))
	}
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	for pass := 0; pass < 2; pass++ {
		for i, c := range calls {
			out, err := runCoreCtx(context.Background(), c[0], c[1:]...)
			if err != nil {
				t.Fatalf("worker %v: %v", c, err)
			}
			got, want := normalizeCoreJSON(t, out), normalizeCoreJSON(t, oneShot[i])
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			if !bytes.Equal(gb, wb) {
				t.Fatalf("%v pass %d: worker output differs from one-shot\nworker:   %.400s\none-shot: %.400s", c, pass, gb, wb)
			}
		}
	}
	all := execs()
	if extra := all[len(calls):]; len(extra) != 1 || extra[0] != "serve" {
		t.Fatalf("worker path must exec only `serve` once; execs after the one-shot phase: %v", extra)
	}
	// a failing command carries the one-shot error through the worker.
	_, err := runCoreCtx(context.Background(), "explain-path", "ws", root, "missing.go")
	if err == nil || err.Error() != "rust-core explain-path failed" {
		t.Fatalf("missing path through the worker: %v", err)
	}
}

// The one-shot hints must never send a call the core runs in-process to the
// one-shot path, and must name every one-shot subcommand of a resident family.
func TestWorkerOneShotHintsMatchTheCore(t *testing.T) {
	core := realCore(t)
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	resetWorker()
	t.Cleanup(resetWorker)
	set := readWorkerSettings()
	key, ok := workerKeyFor(set)
	if !ok {
		t.Fatal("no worker key for the real core")
	}
	p, err := coreWorker.acquire(context.Background(), key, set)
	if err != nil {
		t.Fatal(err)
	}
	coreWorker.release(p, true)
	for name := range oneShotOnly {
		if p.residency.methods[name] {
			t.Errorf("hint sends %s one-shot, but the core runs it in-process", name)
		}
	}
	for family, subs := range oneShotFamilies {
		if !p.residency.methods[family] {
			t.Errorf("family %s is not resident in the core", family)
		}
		for sub := range subs {
			if !p.residency.oneShot[family][sub] {
				t.Errorf("hint sends %s %s one-shot, but the core runs it in-process", family, sub)
			}
		}
	}
	for family, subs := range p.residency.oneShot {
		for sub := range subs {
			if !oneShotFamilies[family][sub] {
				t.Errorf("the core runs %s %s one-shot; add it to oneShotFamilies", family, sub)
			}
		}
	}
	if !mustKnow(t, key).residentFor("search", nil) {
		t.Fatal("the supervisor did not keep the handshake's residency")
	}
}

func mustKnow(t *testing.T, key workerKey) workerResidency {
	t.Helper()
	coreWorker.mu.Lock()
	defer coreWorker.mu.Unlock()
	if coreWorker.knownKey != key.id {
		t.Fatal("no handshake recorded for this key")
	}
	return coreWorker.known
}

// When the real worker dies mid-request, the call fails at once and the next call
// starts a new worker, even while a process a git child left behind is still
// running: the worker's protocol stream is close-on-exec, so no descendant holds it
// open and the supervisor sees the exit.
func TestWorkerCrashWithTheRealCoreIsSeenAtOnce(t *testing.T) {
	core := realCore(t)
	root := gitFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	pids := filepath.Join(bin, "pids")
	script := "#!/bin/sh\nsleep 30 </dev/null >/dev/null 2>&1 &\necho $! >> '" + pids + "'\nsleep 0.3\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, _ := os.ReadFile(pids)
		for _, f := range strings.Fields(string(raw)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "1")
	resetWorker()
	t.Cleanup(resetWorker)

	if _, err := runCoreCtx(context.Background(), "repo-key", root); err != nil {
		t.Fatalf("warm call: %v", err)
	}
	victim := CoreWorkerStats().PID
	before := CoreWorkerStats()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runCoreCtx(ctx, "repo-key", root)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(victim, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	killed := time.Now()
	select {
	case err := <-done:
		if err == nil || err.Error() != "rust-core repo-key failed" {
			t.Fatalf("a call in flight on a killed worker: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call in flight on a killed worker did not fail within 5 s")
	}
	t.Logf("in-flight call failed %v after the kill", time.Since(killed).Round(time.Millisecond))
	waitFor(t, "the crash to be accounted", 3*time.Second, func() bool {
		return CoreWorkerStats().Crashes-before.Crashes == 1
	})
	if _, err := runCoreCtx(context.Background(), "repo-key", root); err != nil {
		t.Fatalf("the call after the crash must restart the worker: %v", err)
	}
	if pid := CoreWorkerStats().PID; pid == victim || pid == 0 {
		t.Fatalf("no new worker after the crash (pid %d)", pid)
	}
	if raw, _ := os.ReadFile(pids); len(strings.Fields(string(raw))) == 0 {
		t.Fatal("no git child left a process behind, so this test proves nothing")
	}
}
