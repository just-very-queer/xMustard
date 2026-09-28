// Command xmustard-hook is the static hook client (WS-23, PAR-HAR-01). Claude Code
// runs it as a command hook for the events that take no http hook (SessionStart) and
// for those whose failure must stay silent (WorktreeRemove: a failing hook there
// blocks the removal). It reads the event's JSON on stdin, posts it to the daemon's
// /api/hooks/<client>/<Event> over the Unix socket (transport.SocketPath) or, when no
// daemon listens there or the socket is not private to this user, over TCP
// (XMUSTARD_API_BASE, default http://127.0.0.1:8042), and writes the daemon's JSON
// answer to stdout.
//
// It fails open: it always exits 0, and prints nothing when anything fails or the
// budget runs out (XMUSTARD_HOOK_TIMEOUT_MS, default 200). It speaks just enough
// HTTP/1.1 to post one request (no net/http, so the binary and its start stay small)
// and starts no process.
//
//	usage: xmustard-hook <client> <Event>      e.g. xmustard-hook claude SessionStart
//
// XMUSTARD_API_TOKEN is sent as the bearer token and XMUSTARD_WORKSPACE_ID as the
// X-Xmustard-Workspace header, when set.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"xmustard/api-go/internal/hooks/transport"
)

const (
	defaultTimeout = 200 * time.Millisecond
	// maxBody bounds the event read from stdin; a larger event is not posted.
	maxBody = 8 << 20
	// maxAnswer bounds the answer read back (Claude Code caps each context string at
	// 10,000 characters; an updatedToolOutput replaces an output of at most ~30,000).
	maxAnswer = 1 << 20
	// defaultBase is the daemon's TCP address when XMUSTARD_API_BASE is unset.
	defaultBase = "http://127.0.0.1:8042"
)

func main() {
	run(os.Args[1:], os.Stdin, os.Stdout, os.Getenv)
	os.Exit(0)
}

// run posts one event and writes the answer; it never fails.
func run(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	if len(args) != 2 || !segment(args[0]) || !segment(args[1]) {
		return
	}
	deadline := time.Now().Add(timeout(getenv))
	body, ok := readStdin(stdin, deadline)
	if !ok {
		return
	}
	req := request{path: "/api/hooks/" + args[0] + "/" + args[1], body: body,
		token: headerValue(getenv("XMUSTARD_API_TOKEN")), workspace: headerValue(getenv("XMUSTARD_WORKSPACE_ID"))}
	for _, t := range targets(getenv) {
		conn, err := t.dial(deadline)
		if err != nil {
			continue // no daemon there, or a socket this user does not own: try the next address
		}
		answer, err := req.send(conn, t.host, deadline)
		_ = conn.Close()
		if err == nil && isObject(answer) {
			_, _ = stdout.Write(answer)
		}
		return // the daemon was reached: its answer, or none, is final
	}
}

// segment accepts a path segment of letters, digits, '_' and '-'.
func segment(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
			return false
		}
	}
	return true
}

func timeout(getenv func(string) string) time.Duration {
	if ms, err := strconv.Atoi(strings.TrimSpace(getenv("XMUSTARD_HOOK_TIMEOUT_MS"))); err == nil && ms > 0 && ms <= 60000 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTimeout
}

// readStdin reads the event before the deadline; a client that never closes stdin
// costs at most the budget.
func readStdin(r io.Reader, deadline time.Time) ([]byte, bool) {
	type read struct {
		b   []byte
		err error
	}
	done := make(chan read, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
		done <- read{b, err}
	}()
	select {
	case got := <-done:
		return got.b, got.err == nil && len(got.b) <= maxBody && len(bytes.TrimSpace(got.b)) > 0
	case <-time.After(time.Until(deadline)):
		return nil, false
	}
}

// headerValue drops a value that could split the request's header.
func headerValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, "\r\n\x00") {
		return ""
	}
	return v
}

// target is one address the daemon may listen on.
type target struct {
	host string
	dial func(deadline time.Time) (net.Conn, error)
}

// targets are the Unix socket, then the TCP address. The socket is dialed only when
// it passes transport.CheckSocket: a socket in a directory another user owns or can
// enter is skipped, so the token and the event never reach it.
func targets(getenv func(string) string) []target {
	var out []target
	if path := transport.SocketPath(getenv); path != "" {
		out = append(out, target{host: "localhost", dial: func(d time.Time) (net.Conn, error) {
			if err := transport.CheckSocket(path); err != nil {
				return nil, err
			}
			return (&net.Dialer{Deadline: d}).Dial("unix", path)
		}})
	}
	base := strings.TrimSpace(getenv("XMUSTARD_API_BASE"))
	if base == "" {
		base = defaultBase
	}
	if u, err := url.Parse(base); err == nil && u.Scheme == "http" && u.Host != "" {
		host := u.Host
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
		out = append(out, target{host: u.Host, dial: func(d time.Time) (net.Conn, error) {
			return (&net.Dialer{Deadline: d}).Dial("tcp", host)
		}})
	}
	return out
}

// request is one hook event to post.
type request struct {
	path, token, workspace string
	body                   []byte
}

var errAnswer = errors.New("no usable answer")

// send writes the request and reads a 200 answer with a Content-Length body.
func (q request) send(conn net.Conn, host string, deadline time.Time) ([]byte, error) {
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	var h strings.Builder
	h.WriteString("POST " + q.path + " HTTP/1.1\r\nHost: " + host + "\r\nContent-Type: application/json\r\n")
	h.WriteString("Content-Length: " + strconv.Itoa(len(q.body)) + "\r\nConnection: close\r\n")
	if q.token != "" {
		h.WriteString("Authorization: Bearer " + q.token + "\r\n")
	}
	if q.workspace != "" {
		h.WriteString(transport.WorkspaceHeader + ": " + q.workspace + "\r\n")
	}
	h.WriteString("\r\n")
	if _, err := io.WriteString(conn, h.String()); err != nil {
		return nil, err
	}
	if _, err := conn.Write(q.body); err != nil {
		return nil, err
	}
	return readAnswer(bufio.NewReader(conn))
}

// readAnswer reads a response: only a 200 with a Content-Length body is an answer.
func readAnswer(br *bufio.Reader) ([]byte, error) {
	status, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if f := strings.Fields(status); len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/1.") || f[1] != "200" {
		return nil, errAnswer
	}
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, _ := strings.Cut(line, ":")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "content-length":
			if length, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
				return nil, errAnswer
			}
		case "transfer-encoding":
			return nil, errAnswer // the daemon always sends a length
		}
	}
	if length < 0 || length > maxAnswer {
		return nil, errAnswer
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// isObject reports whether an answer is a JSON object Claude Code reads as hook output.
func isObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) >= 2 && b[0] == '{' && b[len(b)-1] == '}'
}
