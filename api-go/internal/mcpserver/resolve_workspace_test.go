package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	_, rerr := call(t, s, "explain", map[string]any{"path": "/" + strings.Repeat("a", maxPathArg)})
	if rerr == nil || rerr.Code != CodeInvalidParams {
		t.Fatalf("an explain path over %d characters must be rejected: %v", maxPathArg, rerr)
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
