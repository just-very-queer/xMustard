package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// scriptedRoots answers roots/list from a script, one step per request; the last
// step repeats. A step may run a hook first (to interleave a notification).
type scriptedRoots struct {
	mu    sync.Mutex
	steps []func() (json.RawMessage, error)
	calls int
}

func (c *scriptedRoots) Request(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method != "roots/list" {
		return nil, fmt.Errorf("unexpected %s", method)
	}
	c.mu.Lock()
	i := min(c.calls, len(c.steps)-1)
	c.calls++
	step := c.steps[i]
	c.mu.Unlock()
	return step()
}

func (c *scriptedRoots) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func rootsAnswer(paths ...string) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) {
		return (&rootsClient{roots: paths}).Request(context.Background(), "roots/list", nil)
	}
}

func fail(err error) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return nil, err }
}

func wsOf(t *testing.T, res map[string]any) Workspace {
	t.Helper()
	meta, _ := res["_meta"].(map[string]any)
	ws, ok := meta["xmustard/workspace"].(Workspace)
	if !ok {
		t.Fatalf("no workspace in result: %v", res)
	}
	return ws
}

// A failed roots/list is not the client's answer: it is asked again on the next call,
// and meanwhile the working directory never registers a repository the client's
// roots may not name.
func TestFailedRootsAreAskedAgainAndNeverRegisterTheCwd(t *testing.T) {
	project, other := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){
		fail(errors.New("client error -32603: busy")),
		rootsAnswer(project),
	}}
	s := newSession(t, api, Options{Cwd: other, AutoRegister: true}, client, "2025-06-18")
	res, rerr := call(t, s, "remember", map[string]any{"content": "c"})
	if rerr != nil || res["isError"] != true {
		t.Fatalf("first call: want a tool error, got %v %v", rerr, res)
	}
	for _, want := range []string{"roots/list failed: client error -32603: busy", "not auto-registered while the client's roots are unavailable"} {
		if !strings.Contains(text(res), want) {
			t.Fatalf("error %q lacks %q", text(res), want)
		}
	}
	if n := api.count("POST", "/api/workspaces/load"); n != 0 {
		t.Fatalf("the cwd repository was registered while the roots were unavailable (%d loads)", n)
	}
	for i := 0; i < 3; i++ {
		res, rerr = call(t, s, "remember", map[string]any{"content": "c"})
		if rerr != nil || res["isError"] != false {
			t.Fatalf("call %d: %v %v", i+2, rerr, res)
		}
		if ws := wsOf(t, res); ws.ID != "project-ws" || ws.Source != SourceRoots || ws.Root != project {
			t.Fatalf("call %d resolved %+v, want the client's root", i+2, ws)
		}
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times: want once more after the failure, then cached", n)
	}
}

// A registered working directory still answers while the roots are unavailable, and
// the next call returns to the client's root.
func TestFailedRootsFallBackToARegisteredCwdForOneCall(t *testing.T) {
	project, other := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}, {ID: "other-ws", Root: other}}}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){fail(errors.New("transport closed")), rootsAnswer(project)}}
	s := newSession(t, api, Options{Cwd: other, AutoRegister: true}, client, "2025-06-18")
	first, _ := call(t, s, "ground", map[string]any{})
	second, _ := call(t, s, "ground", map[string]any{})
	if a, b := wsOf(t, first), wsOf(t, second); a.ID != "other-ws" || a.Source != SourceCWD || b.ID != "project-ws" || b.Source != SourceRoots {
		t.Fatalf("resolved %+v then %+v", a, b)
	}
}

// A timed-out roots/list is not asked again on every call (each would wait
// rootsTimeout), but it is asked again once the back-off expires.
func TestTimedOutRootsBackOffThenRetry(t *testing.T) {
	project := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){
		fail(fmt.Errorf("waiting for roots/list: %w", context.DeadlineExceeded)),
		rootsAnswer(project),
	}}
	s := newSession(t, api, Options{}, client, "2025-06-18")
	for i := 0; i < 3; i++ {
		res, _ := call(t, s, "ground", map[string]any{})
		if res["isError"] != true || !strings.Contains(text(res), "asked again after 15s") {
			t.Fatalf("call %d during the back-off: %v", i+1, text(res))
		}
	}
	if n := client.count(); n != 1 {
		t.Fatalf("roots/list asked %d times during the back-off", n)
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(-time.Second) // the back-off expires
	s.mu.Unlock()
	res, _ := call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 2 {
		t.Fatalf("after the back-off: %+v (roots/list asked %d times)", ws, client.count())
	}
}

// A roots/list that keeps failing backs off: one request inside the window, and a
// retry once it expires. Today only a deadline sets retryAt, so a client that always
// answers -32603 is re-asked on every call (FU:10 measured 5 calls, 5 requests).
// A first fast failure is still asked again on the next call, so a client that fails
// once recovers immediately.
func TestRepeatedRootsFailureBacksOffUntilTheWindowExpires(t *testing.T) {
	project, other := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	busy := fail(fmt.Errorf("client error -32603: %w", errors.New("no roots configured")))
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){busy, busy, rootsAnswer(project)}}
	s := newSession(t, api, Options{Cwd: other}, client, "2025-06-18")

	// The first failure is asked again immediately: one-shot recovery is preserved.
	// other is an unregistered git repository, so resolution falls through to it and
	// fails there: the working directory never registers while the roots are unknown.
	res, _ := call(t, s, "ground", map[string]any{})
	if !res["isError"].(bool) || strings.Contains(text(res), "asked again after") {
		t.Fatalf("a single fast failure must not back off: %v", text(res))
	}
	if n := api.count("POST", "/api/workspaces/load"); n != 0 {
		t.Fatalf("the working directory was registered while the roots were unavailable (%d loads)", n)
	}
	if n := client.count(); n != 1 {
		t.Fatalf("roots/list asked %d times on the first call", n)
	}

	// The second failure opens the back-off window, and calls inside it keep failing
	// from the cached answer without sending another request.
	res, _ = call(t, s, "ground", map[string]any{})
	if !res["isError"].(bool) || !strings.Contains(text(res), "asked again after "+rootsRetryAfterFailure.String()) {
		t.Fatalf("the second failure must open the back-off window: %v", text(res))
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(time.Hour) // the window cannot expire mid-test
	s.mu.Unlock()
	for i := 0; i < 3; i++ {
		res, _ = call(t, s, "ground", map[string]any{})
		if !res["isError"].(bool) {
			t.Fatalf("call %d inside the window should still fail: %v", i+3, text(res))
		}
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times: want 2 (the first failure and the back-off that followed it)", n)
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(-time.Second) // the back-off expires
	s.mu.Unlock()
	res, _ = call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 3 {
		t.Fatalf("after the back-off: %+v (roots/list asked %d times)", ws, client.count())
	}
}

// A registered working directory still answers every call while the client's roots
// are unknown, and still registers nothing. The window is pinned far into the future
// before the calls that must land inside it, so a slow host cannot expire it and the
// request count becomes a deterministic assertion.
func TestRegisteredCwdAnswersInsideTheBackOffWindow(t *testing.T) {
	other := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "other-ws", Root: other}}}
	busy := fail(fmt.Errorf("client error -32603: %w", errors.New("no roots configured")))
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){busy, busy, busy}}
	s := newSession(t, api, Options{Cwd: other}, client, "2025-06-18")

	// the first two failures open the window
	for i := 0; i < 2; i++ {
		if _, _ = call(t, s, "ground", map[string]any{}); client.count() != i+1 {
			t.Fatalf("after %d calls roots/list was asked %d times", i+1, client.count())
		}
	}
	s.mu.Lock()
	windowed := s.roots != nil && s.roots.transient && !s.roots.retryAt.IsZero()
	if windowed {
		s.roots.retryAt = time.Now().Add(time.Hour) // the window cannot expire mid-test
	}
	s.mu.Unlock()
	if !windowed {
		t.Fatal("two failures must leave the answer cached with a window")
	}

	for i := 0; i < 4; i++ {
		res, _ := call(t, s, "ground", map[string]any{})
		if ws := wsOf(t, res); ws.ID != "other-ws" || ws.Source != SourceCWD || ws.Registered {
			t.Fatalf("call %d: a registered working directory must answer: %+v", i+3, ws)
		}
	}
	if n := api.count("POST", "/api/workspaces/load"); n != 0 {
		t.Fatalf("the working directory was registered while the roots were unavailable (%d loads)", n)
	}
	if asked := client.count(); asked != 2 {
		t.Fatalf("roots/list asked %d times inside the window; the calls above must be served from cache", asked)
	}
}

// rootsBackoff is the table behind that: a timeout backs off immediately, a prompt
// failure backs off once it has followed a cached failure, and a first prompt failure
// does not back off at all.
func TestRootsBackoffTable(t *testing.T) {
	cases := []struct {
		name     string
		cached   *rootsState
		timedOut bool
		want     time.Duration
	}{
		{"timeout after nothing", nil, true, rootsRetryAfter},
		{"timeout after a cached failure", &rootsState{transient: true}, true, rootsRetryAfter},
		{"prompt failure after a timeout", &rootsState{transient: true}, false, rootsRetryAfterFailure},
		{"prompt failure after a prompt failure", &rootsState{transient: true}, false, rootsRetryAfterFailure},
		{"prompt failure after nothing", nil, false, 0},
		{"prompt failure after a good answer", &rootsState{}, false, 0},
	}
	for _, c := range cases {
		if got := rootsBackoff(c.cached, c.timedOut); got != c.want {
			t.Errorf("%s: rootsBackoff = %s, want %s", c.name, got, c.want)
		}
	}
}

// A roots/list answer that cannot be parsed is a client failure, not a definitive
// "the client has no usable roots": it is asked again once it has failed twice, the
// same as any other failure.
func TestMalformedRootsAnswerIsAskedAgain(t *testing.T) {
	project, other := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	garbage := func() (json.RawMessage, error) { return json.RawMessage(`{"roots": [`), nil }
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){garbage, garbage, rootsAnswer(project)}}
	s := newSession(t, api, Options{Cwd: other}, client, "2025-06-18")

	res, _ := call(t, s, "ground", map[string]any{})
	if !strings.Contains(text(res), "roots/list answer is malformed") {
		t.Fatalf("a malformed answer must be reported as one: %v", text(res))
	}
	res, _ = call(t, s, "ground", map[string]any{})
	if !strings.Contains(text(res), "asked again after "+rootsRetryAfterFailure.String()) {
		t.Fatalf("a repeated malformed answer must back off: %v", text(res))
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times: want 2, then none inside the window", n)
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(time.Hour) // the window cannot expire mid-test
	s.mu.Unlock()
	// calls inside the window are served from the cached failure, so no further
	// request is sent even when the answer would be malformed again
	for i := 0; i < 3; i++ {
		res, _ = call(t, s, "ground", map[string]any{})
		if !strings.Contains(text(res), "roots/list answer is malformed") {
			t.Fatalf("call %d inside the window should still report the malformed answer: %v", i+3, text(res))
		}
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times inside the window", n)
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	res, _ = call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 3 {
		t.Fatalf("after the back-off a malformed answer must be asked again: %+v (asked %d)", ws, client.count())
	}
}

// A failure is cached for the session, so its text is held for the session's
// lifetime. The client's message can be as large as one transport message, so the
// cached text is bounded and the reported error still names the cause.
func TestCachedRootsFailureTextIsBounded(t *testing.T) {
	other := tempRepo(t)
	huge := strings.Repeat("x", 64*1024)
	api := &fakeAPI{}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){
		fail(fmt.Errorf("client error -32603: %w", errors.New(huge))),
		fail(fmt.Errorf("client error -32603: %w", errors.New(huge))),
	}}
	s := newSession(t, api, Options{Cwd: other}, client, "2025-06-18")

	res, _ := call(t, s, "ground", map[string]any{})
	if len(text(res)) > rootsErrClip+512 {
		t.Fatalf("the reported error is %d bytes, over the clip", len(text(res)))
	}
	if !strings.Contains(text(res), "client error -32603") {
		t.Fatalf("the clipped error must still name the cause: %v", text(res))
	}
	s.mu.Lock()
	cached := len(s.roots.err)
	s.mu.Unlock()
	if cached > rootsErrClip+128 {
		t.Fatalf("the cached failure text is %d bytes; a failure is held for the whole session", cached)
	}
}

// A malformed answer must contribute no roots at all. Go's decoder fills array
// elements before it reports a later type error, so an answer whose first root is
// usable and whose second is a number parses partially; the usable one is still
// discarded, because the answer was rejected as a whole.
func TestMalformedRootsAnswerContributesNoRoots(t *testing.T) {
	partial := tempRepo(t)
	other := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "other-ws", Root: other}}}
	halfValid := func() (json.RawMessage, error) {
		return json.RawMessage(`{"roots": [{"uri": "file://` + partial + `"}, {"uri": 7}]}`), nil
	}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){halfValid}}
	s := newSession(t, api, Options{Cwd: other, AutoRegister: true}, client, "2025-06-18")

	res, _ := call(t, s, "ground", map[string]any{})
	// A rejected answer supplies no root, so resolution falls through to the
	// registered working directory rather than to the answer's usable first root.
	if ws := wsOf(t, res); ws.ID != "other-ws" || ws.Source != SourceCWD {
		t.Fatalf("a rejected answer must not supply a root: %+v", ws)
	}
	if n := api.count("POST", "/api/workspaces/load"); n != 0 {
		t.Fatalf("a root from a rejected answer was registered (%d loads)", n)
	}
	s.mu.Lock()
	paths, st := len(s.roots.paths), s.roots.transient
	s.mu.Unlock()
	if paths != 0 {
		t.Fatalf("%d roots kept from a rejected answer", paths)
	}
	if !st {
		t.Fatal("a rejected answer must stay transient")
	}
}

// A roots/list request that was never delivered says nothing about the client, so it
// is not cached and cannot open a window. On a Streamable HTTP session a plain JSON
// exchange has no event stream to ask on; if that counted as a client failure, two
// streamless calls would suppress the next stream-capable call and it would resolve
// from the working directory instead of the client's root.
func TestUnsentRootsRequestIsNotCached(t *testing.T) {
	project, other := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}, {ID: "other-ws", Root: other}}}
	// every request before the last never reaches the client
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){
		fail(errNoStream), fail(errNoStream), fail(errNoStream), rootsAnswer(project),
	}}
	s := newSession(t, api, Options{Cwd: other}, client, "2026-06-18")

	for i := 0; i < 3; i++ {
		res, _ := call(t, s, "ground", map[string]any{})
		// the working directory answers while the client's roots are unknown
		if ws := wsOf(t, res); ws.ID != "other-ws" || ws.Source != SourceCWD {
			t.Fatalf("call %d: %+v", i+1, ws)
		}
	}
	// nothing is kept: an undelivered request says nothing about the client
	s.mu.Lock()
	cached := s.roots
	s.mu.Unlock()
	if cached != nil {
		t.Fatalf("an undelivered request was cached: %+v", cached)
	}
	// the next request is still made, and the client's root is used
	res, _ := call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || ws.Source != SourceRoots || client.count() != 4 {
		t.Fatalf("the client's root must be used after unsent requests: %+v (asked %d)", ws, client.count())
	}
}

// An answer that carries no roots field is not a statement about the client's roots,
// so it is asked again; one that carries an empty roots array is.
func TestRootsAnswerWithoutARootsFieldIsAskedAgain(t *testing.T) {
	project := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	noField := func() (json.RawMessage, error) { return json.RawMessage(`{"capabilities":{}}`), nil }
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){noField, rootsAnswer(project)}}
	s := newSession(t, api, Options{}, client, "2026-06-18")

	res, _ := call(t, s, "ground", map[string]any{})
	if !strings.Contains(text(res), "carries no roots array") {
		t.Fatalf("an answer with no roots field must say so: %v", text(res))
	}
	res, _ = call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 2 {
		t.Fatalf("the client must be asked again: %+v (asked %d)", ws, client.count())
	}
}

// {"roots":null} carries the field but no array, so it is not a statement about the
// client's roots either: it is asked again rather than cached until roots/list_changed.
func TestNullRootsFieldIsAskedAgain(t *testing.T) {
	project := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
	null := func() (json.RawMessage, error) { return json.RawMessage(`{"roots":null}`), nil }
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){null, rootsAnswer(project)}}
	s := newSession(t, api, Options{}, client, "2026-06-18")

	res, _ := call(t, s, "ground", map[string]any{})
	if !strings.Contains(text(res), "carries no roots array") {
		t.Fatalf("a null roots field must be reported as carrying no array: %v", text(res))
	}
	res, _ = call(t, s, "ground", map[string]any{})
	if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 2 {
		t.Fatalf("the client must be asked again: %+v (asked %d)", ws, client.count())
	}
}

// An answer that keeps omitting the roots array is a repeated failure like any other:
// the second in a row opens the back-off window, so a persistently broken client is
// not asked on every call.
func TestRepeatedRootsAnswerWithoutAnArrayBacksOff(t *testing.T) {
	noField := func() (json.RawMessage, error) { return json.RawMessage(`{"capabilities":{}}`), nil }
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){noField, noField, noField}}
	s := newSession(t, &fakeAPI{}, Options{}, client, "2026-06-18")

	call(t, s, "ground", map[string]any{})
	res, _ := call(t, s, "ground", map[string]any{})
	if !strings.Contains(text(res), "asked again after "+rootsRetryAfterFailure.String()) {
		t.Fatalf("a repeated answer without a roots array must back off: %v", text(res))
	}
	s.mu.Lock()
	s.roots.retryAt = time.Now().Add(time.Hour) // the window cannot expire mid-test
	s.mu.Unlock()
	for i := 0; i < 3; i++ {
		call(t, s, "ground", map[string]any{})
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times: want 2, then none inside the window", n)
	}
}

// A stream write that fails never delivered roots/list either, so it is classed with
// errNoStream: not a client failure, never cached.
func TestHTTPClientStreamWriteFailureIsUnsent(t *testing.T) {
	c := &httpClient{pending: map[string]chan rpcMessage{}}
	ctx := withStream(context.Background(), &eventStream{w: failingWriter{httptest.NewRecorder()}})
	_, err := c.Request(ctx, "roots/list", map[string]any{})
	if err == nil || !unsentRootCause(err) {
		t.Fatalf("a failed stream write must count as undelivered: %v", err)
	}
	if !unsentRootCause(errNoStream) || unsentRootCause(errors.New("client error -32603")) {
		t.Fatal("errNoStream is undelivered; a client error is not")
	}
}

type failingWriter struct{ http.ResponseWriter }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// A roots array whose entries carry no uri is malformed, not a definitive "no roots".
func TestRootsEntriesWithoutAURIAreAskedAgain(t *testing.T) {
	for _, body := range []string{`{"roots":[null]}`, `{"roots":[{}]}`} {
		project := tempRepo(t)
		api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
		bad := func() (json.RawMessage, error) { return json.RawMessage(body), nil }
		client := &scriptedRoots{steps: []func() (json.RawMessage, error){bad, rootsAnswer(project)}}
		s := newSession(t, api, Options{}, client, "2026-06-18")
		call(t, s, "ground", map[string]any{})
		res, _ := call(t, s, "ground", map[string]any{})
		if ws := wsOf(t, res); ws.ID != "project-ws" || client.count() != 2 {
			t.Fatalf("%s: the client must be asked again: %+v (asked %d)", body, ws, client.count())
		}
	}
}

// clip never splits a UTF-8 sequence, so a cached error stays valid text.
func TestClipKeepsValidUTF8(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5} {
		if got := clip("aé€😀", n); !utf8.ValidString(got) {
			t.Fatalf("clip(_, %d) = %q is not valid UTF-8", n, got)
		}
	}
}

// An empty roots array is a real answer: the client has no file roots, and that is
// kept until roots/list_changed.
func TestEmptyRootsArrayIsDefinitive(t *testing.T) {
	empty := func() (json.RawMessage, error) { return json.RawMessage(`{"roots":[]}`), nil }
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){empty}}
	s := newSession(t, &fakeAPI{}, Options{}, client, "2026-06-18")
	for i := 0; i < 3; i++ {
		call(t, s, "ground", map[string]any{})
	}
	if n := client.count(); n != 1 {
		t.Fatalf("roots/list asked %d times; an empty roots array is definitive", n)
	}
}

// A client that does not implement roots/list says so once; that answer is kept.
func TestRootsMethodNotFoundIsKept(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "cwd-ws", Root: repo}}}
	client := &scriptedRoots{steps: []func() (json.RawMessage, error){
		fail(fmt.Errorf("client error -32601: %w", &RPCError{Code: CodeMethodNotFound, Message: "method not found"})),
	}}
	s := newSession(t, api, Options{Cwd: repo}, client, "2025-06-18")
	for i := 0; i < 3; i++ {
		res, _ := call(t, s, "ground", map[string]any{})
		if ws := wsOf(t, res); ws.ID != "cwd-ws" || ws.Source != SourceCWD {
			t.Fatalf("call %d: %+v", i+1, ws)
		}
	}
	if n := client.count(); n != 1 {
		t.Fatalf("roots/list asked %d times after method-not-found", n)
	}
}

// roots/list_changed arriving while a roots/list answer is being processed: the
// answer (the old roots) serves the call that asked, but is not kept.
func TestRootsAnswerOvertakenByListChangedIsNotKept(t *testing.T) {
	oldRoot, newRoot := tempRepo(t), tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "old-ws", Root: oldRoot}, {ID: "new-ws", Root: newRoot}}}
	var s *Session
	client := &scriptedRoots{}
	client.steps = []func() (json.RawMessage, error){
		func() (json.RawMessage, error) {
			// the client answers with its old roots, then reports the change; the
			// notification is processed before the waiting call stores the answer
			s.Notify("notifications/roots/list_changed", nil)
			return rootsAnswer(oldRoot)()
		},
		rootsAnswer(newRoot),
	}
	s = newSession(t, api, Options{}, client, "2025-06-18")
	first, _ := call(t, s, "ground", map[string]any{})
	second, _ := call(t, s, "ground", map[string]any{})
	if a, b := wsOf(t, first), wsOf(t, second); a.ID != "old-ws" || b.ID != "new-ws" {
		t.Fatalf("resolved %s then %s: the pre-change roots were kept", a.ID, b.ID)
	}
	if n := client.count(); n != 2 {
		t.Fatalf("roots/list asked %d times", n)
	}
}

// Model-supplied path arguments select among registered workspaces without growing
// the session: they are not cached, and an explain path is capped at PATH_MAX.
func TestPathArgumentsDoNotGrowTheSession(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "repo-ws", Root: repo}}}
	s := newSession(t, api, Options{}, nil, "2025-06-18")
	for i := 0; i < 40; i++ {
		p := filepath.Join(repo, fmt.Sprintf("d%02d", i), strings.Repeat("a", 200))
		res, rerr := call(t, s, "explain", map[string]any{"path": p})
		if rerr != nil || res["isError"] != false || wsOf(t, res).ID != "repo-ws" {
			t.Fatalf("call %d: %v %v", i, rerr, res)
		}
	}
	s.mu.Lock()
	cached := len(s.byPath)
	s.mu.Unlock()
	if cached != 0 {
		t.Fatalf("path arguments grew the session cache to %d entries", cached)
	}
	if n := api.count("GET", "/api/workspaces"); n != 1 {
		t.Fatalf("each path argument re-read the listing (%d reads)", n)
	}
	if _, d := argErr(t, s, "explain", map[string]any{"path": "/" + strings.Repeat("a", maxPathArg)}); d["maxLength"] != maxPathArg {
		t.Fatalf("an explain path over %d characters must be rejected: %v", maxPathArg, d)
	}
}

// The roots and working-directory cache is bounded even for a client that keeps
// changing its roots.
func TestPathCacheIsBounded(t *testing.T) {
	api := &fakeAPI{}
	var repos []string
	for i := 0; i < maxPathCache+8; i++ {
		repo := tempRepo(t)
		repos = append(repos, repo)
		api.workspaces = append(api.workspaces, registeredWorkspace{ID: fmt.Sprintf("ws-%d", i), Root: repo})
	}
	next := 0
	client := &scriptedRoots{}
	client.steps = []func() (json.RawMessage, error){func() (json.RawMessage, error) {
		next++
		return rootsAnswer(repos[next-1])()
	}}
	s := newSession(t, api, Options{}, client, "2025-06-18")
	for range repos {
		if res, _ := call(t, s, "ground", map[string]any{}); res["isError"] != false {
			t.Fatalf("%v", text(res))
		}
		s.Notify("notifications/roots/list_changed", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byPath) > maxPathCache {
		t.Fatalf("path cache holds %d entries, over %d", len(s.byPath), maxPathCache)
	}
}

// A refused registration reports the API's message, not its JSON envelope, so the
// rule that refused it (and XMUSTARD_REGISTER_ROOTS) survives the clip.
func TestRefusedRegistrationReportsTheAPIMessage(t *testing.T) {
	repo := tempRepo(t)
	msg := "non-admin registration is off: the operator has configured no registration roots (XMUSTARD_REGISTER_ROOTS), so an admin must register " + repo
	body, _ := json.Marshal(map[string]any{"error": msg, "reason": "registration_not_allowed", "refusal": "no_register_roots"})
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		if r.Path == "/api/workspaces/load" {
			return &APIResponse{Status: 403, Body: string(body)}
		}
		return nil
	}}
	s := newSession(t, api, Options{Cwd: filepath.Join(repo, "src"), AutoRegister: true}, nil, "2025-06-18")
	res, _ := call(t, s, "ground", map[string]any{})
	out := text(res)
	if res["isError"] != true || !strings.Contains(out, "(403 Forbidden: non-admin registration is off") || !strings.Contains(out, "XMUSTARD_REGISTER_ROOTS") || strings.Contains(out, `"reason"`) {
		t.Fatalf("refusal must carry the API's message: %s", out)
	}
}

// rootsUnknownClient answers roots/list with a failure, so the client's roots are
// unknown rather than absent on every call.
func rootsUnknownClient(err error) *scriptedRoots {
	return &scriptedRoots{steps: []func() (json.RawMessage, error){fail(err)}}
}

// The default is the merged tip's rule: an unknown roots/list never registers the
// working directory, and the refusal names that rule.
func TestCwdIsNotRegisteredWhileRootsAreUnknownByDefault(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{}
	// RegisterCwdWhenRootsUnknown unset is the zero value the shims pass when the
	// variable is unset or "0"; they read it with the same `!= "0"` rule
	// XMUSTARD_MCP_AUTO_REGISTER uses.
	s := newSession(t, api, Options{Cwd: filepath.Join(repo, "src"), AutoRegister: true}, rootsUnknownClient(errors.New("client error -32603: busy")), "2025-06-18")
	res, _ := call(t, s, "ground", map[string]any{})
	out := text(res)
	if res["isError"] != true || !strings.Contains(out, "not auto-registered while the client's roots are unavailable") {
		t.Fatalf("%s", out)
	}
	if n := api.count("POST", "/api/workspaces/load"); n != 0 {
		t.Fatalf("the cwd was registered (%d loads)", n)
	}
}

// The opt-in registers exactly the working directory's own repository, once, and the
// answer reports it.
func TestTheOptInRegistersTheCwdWhileRootsAreUnknown(t *testing.T) {
	repo := tempRepo(t)
	api := &fakeAPI{}
	opts := Options{Cwd: filepath.Join(repo, "src", "pkg"), AutoRegister: true, RegisterCwdWhenRootsUnknown: true}
	s := newSession(t, api, opts, rootsUnknownClient(errors.New("client error -32603: busy")), "2025-06-18")
	res, rerr := call(t, s, "ground", map[string]any{})
	if rerr != nil || res["isError"] != false {
		t.Fatalf("the opt-in must register the cwd: %v %v", rerr, text(res))
	}
	ws := wsOf(t, res)
	if ws.Source != SourceCWD || ws.Root != repo || !ws.Registered || ws.ID != WorkspaceIDForPath(repo) {
		t.Fatalf("resolved %+v, want the cwd's own repository registered", ws)
	}
	// The registration is cached like any other resolved path, so the next call neither
	// registers again nor asks roots/list again.
	if _, _ = call(t, s, "ground", map[string]any{}); api.count("POST", "/api/workspaces/load") != 1 {
		t.Fatalf("registered %d times", api.count("POST", "/api/workspaces/load"))
	}
}

// The opt-in changes the cwd signal and nothing else: it is not a wider
// auto-registration, and auto-registration itself still wins.
func TestTheOptInRefusesNothingElse(t *testing.T) {
	t.Run("auto-registration off still refuses", func(t *testing.T) {
		repo := tempRepo(t)
		api := &fakeAPI{}
		s := newSession(t, api, Options{Cwd: repo, RegisterCwdWhenRootsUnknown: true}, rootsUnknownClient(errors.New("client error -32603: busy")), "2025-06-18")
		res, _ := call(t, s, "ground", map[string]any{})
		out := text(res)
		if res["isError"] != true || !strings.Contains(out, "auto-registration is off") || strings.Contains(out, "roots are unavailable") {
			t.Fatalf("XMUSTARD_MCP_AUTO_REGISTER=0 must win: %s", out)
		}
		if n := api.count("POST", "/api/workspaces/load"); n != 0 {
			t.Fatalf("registered %d times with auto-registration off", n)
		}
	})
	t.Run("answering roots still win over the cwd", func(t *testing.T) {
		project, other := tempRepo(t), tempRepo(t)
		api := &fakeAPI{workspaces: []registeredWorkspace{{ID: "project-ws", Root: project}}}
		s := newSession(t, api, Options{Cwd: other, AutoRegister: true, RegisterCwdWhenRootsUnknown: true}, &rootsClient{roots: []string{project}}, "2025-06-18")
		res, _ := call(t, s, "ground", map[string]any{})
		ws := wsOf(t, res)
		if ws.Source != SourceRoots || ws.ID != "project-ws" || ws.Registered {
			t.Fatalf("the opt-in must not apply once the client's roots are known: %+v", ws)
		}
		if n := api.count("POST", "/api/workspaces/load"); n != 0 {
			t.Fatalf("registered %d times with a registered root", n)
		}
	})
	t.Run("a path argument still never registers", func(t *testing.T) {
		repo := tempRepo(t)
		// the working directory is outside every git repository, so the path argument
		// is the only signal that could register anything
		outside := t.TempDir()
		api := &fakeAPI{}
		s := newSession(t, api, Options{Cwd: outside, AutoRegister: true, RegisterCwdWhenRootsUnknown: true}, rootsUnknownClient(errors.New("client error -32603: busy")), "2025-06-18")
		res, _ := call(t, s, "explain", map[string]any{"path": filepath.Join(repo, "src", "a.go")})
		if res["isError"] != true || !strings.Contains(text(res), "a path argument never registers a repository") {
			t.Fatalf("a model-supplied path stays unregistering: %s", text(res))
		}
		if n := api.count("POST", "/api/workspaces/load"); n != 0 {
			t.Fatalf("registered %d times for a path argument", n)
		}
	})
	t.Run("a timed-out roots/list is the same unknown case", func(t *testing.T) {
		repo := tempRepo(t)
		api := &fakeAPI{}
		s := newSession(t, api, Options{Cwd: repo, AutoRegister: true, RegisterCwdWhenRootsUnknown: true}, rootsUnknownClient(fmt.Errorf("waiting for roots/list: %w", context.DeadlineExceeded)), "2025-06-18")
		res, _ := call(t, s, "ground", map[string]any{})
		if res["isError"] != false {
			t.Fatalf("the opt-in covers every unknown-roots failure, not one error string: %s", text(res))
		}
		if ws := wsOf(t, res); ws.Source != SourceCWD || !ws.Registered {
			t.Fatalf("resolved %+v", ws)
		}
	})
}
