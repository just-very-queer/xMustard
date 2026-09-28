package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/hooks/transport"
)

// serveUnix serves h on a Unix socket in a fresh directory and returns its path.
func serveUnix(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "xmh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "hook.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return path
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// closedPort is a TCP address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

func TestPostsTheEventAndPrintsTheAnswer(t *testing.T) {
	const answer = `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"hello"}}`
	var got struct {
		path, auth, ws, ctype, body string
	}
	sock := serveUnix(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.path, got.auth, got.ws, got.ctype, got.body = r.URL.Path, r.Header.Get("Authorization"),
			r.Header.Get(transport.WorkspaceHeader), r.Header.Get("Content-Type"), string(b)
		w.Header().Set("Content-Length", strconv.Itoa(len(answer)))
		io.WriteString(w, answer)
	})
	var out bytes.Buffer
	in := `{"session_id":"s","hook_event_name":"SessionStart","source":"startup","cwd":"/r"}`
	run([]string{"claude", "SessionStart"}, strings.NewReader(in), &out, env(map[string]string{
		transport.SocketEnv: sock, "XMUSTARD_API_TOKEN": "tok-1", "XMUSTARD_WORKSPACE_ID": "ws1", "XMUSTARD_API_BASE": closedPort(t)}))
	if out.String() != answer {
		t.Fatalf("stdout %q", out.String())
	}
	if got.path != "/api/hooks/claude/SessionStart" || got.auth != "Bearer tok-1" || got.ws != "ws1" || got.ctype != "application/json" || got.body != in {
		t.Fatalf("request %+v", got)
	}
}

func TestFallsBackToTCPWhenNoSocketListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	var out bytes.Buffer
	run([]string{"claude", "WorktreeRemove"}, strings.NewReader(`{"worktree_path":"/w"}`), &out,
		env(map[string]string{transport.SocketEnv: filepath.Join(t.TempDir(), "none.sock"), "XMUSTARD_API_BASE": "http://" + ln.Addr().String()}))
	if out.String() != `{"ok":true}` {
		t.Fatalf("stdout %q", out.String())
	}
}

// Every failure is silent: no daemon, a slow daemon, a non-200 or non-JSON answer,
// bad arguments, an empty or endless stdin.
func TestFailsOpenWithinTheBudget(t *testing.T) {
	slow := serveUnix(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		io.WriteString(w, `{"late":true}`)
	})
	denied := serveUnix(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"authentication required"}`)
	})
	plain := serveUnix(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not json") })
	endless, _ := io.Pipe()
	nowhere := closedPort(t)
	cases := []struct {
		name  string
		args  []string
		stdin io.Reader
		sock  string
	}{
		{"no daemon", []string{"claude", "SessionStart"}, strings.NewReader(`{}`), filepath.Join(t.TempDir(), "x.sock")},
		{"slow daemon", []string{"claude", "SessionStart"}, strings.NewReader(`{}`), slow},
		{"refused", []string{"claude", "SessionStart"}, strings.NewReader(`{}`), denied},
		{"not json", []string{"claude", "SessionStart"}, strings.NewReader(`{}`), plain},
		{"bad event", []string{"claude", "../../api/auth/tokens"}, strings.NewReader(`{}`), plain},
		{"one argument", []string{"SessionStart"}, strings.NewReader(`{}`), plain},
		{"empty stdin", []string{"claude", "SessionStart"}, strings.NewReader(" \n"), plain},
		{"endless stdin", []string{"claude", "SessionStart"}, endless, plain},
	}
	for _, c := range cases {
		var out bytes.Buffer
		start := time.Now()
		run(c.args, c.stdin, &out, env(map[string]string{transport.SocketEnv: c.sock, "XMUSTARD_API_BASE": nowhere, "XMUSTARD_HOOK_TIMEOUT_MS": "150"}))
		if took := time.Since(start); out.Len() != 0 || took > 600*time.Millisecond {
			t.Errorf("%s: printed %q after %v", c.name, out.String(), took)
		}
	}
}

func TestHeaderValuesCannotSplitTheRequest(t *testing.T) {
	for _, v := range []string{"a\r\nX-Evil: 1", "a\nb", "a\x00b"} {
		if headerValue(v) != "" {
			t.Fatalf("header value %q kept", v)
		}
	}
	if headerValue(" tok ") != "tok" {
		t.Fatal("trim")
	}
}

func TestSocketPath(t *testing.T) {
	if p := transport.SocketPath(env(map[string]string{transport.SocketEnv: "off"})); p != "" {
		t.Fatalf("off: %q", p)
	}
	if p := transport.SocketPath(env(map[string]string{transport.SocketEnv: "/x/h.sock"})); p != "/x/h.sock" {
		t.Fatalf("explicit: %q", p)
	}
	if p := transport.SocketPath(env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/7"})); p != "/run/user/7/xmustard/hook.sock" {
		t.Fatalf("xdg: %q", p)
	}
	if p := transport.SocketPath(env(nil)); !strings.HasSuffix(p, "/hook.sock") || !strings.Contains(p, "xmustard-") {
		t.Fatalf("default: %q", p)
	}
}

// The client must stay a small program that starts no process: it may import only
// these packages (no os/exec, no net/http).
func TestClientImportsStaySmall(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"bufio": true, "bytes": true, "errors": true, "io": true, "net": true, "net/url": true,
		"os": true, "strconv": true, "strings": true, "time": true, "xmustard/api-go/internal/hooks/transport": true}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); !allowed[p] {
			t.Errorf("xmustard-hook imports %s", p)
		}
	}
}
