package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Process-level helpers: some findings concern the real binary boundary (signal
// handling, listener binding, startup refusal), so these tests build and exec
// xmustard-api instead of calling handlers in-process.

var (
	apiBinOnce sync.Once
	apiBinPath string
	apiBinErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if apiBinPath != "" {
		_ = os.RemoveAll(filepath.Dir(apiBinPath))
	}
	os.Exit(code)
}

func apiBinary(t *testing.T) string {
	t.Helper()
	apiBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "xmustard-api-bin-")
		if err != nil {
			apiBinErr = err
			return
		}
		apiBinPath = filepath.Join(dir, "xmustard-api")
		out, err := exec.Command("go", "build", "-o", apiBinPath, ".").CombinedOutput()
		if err != nil {
			apiBinErr = fmt.Errorf("build xmustard-api: %v\n%s", err, out)
		}
	})
	if apiBinErr != nil {
		t.Fatal(apiBinErr)
	}
	return apiBinPath
}

// testClient accepts the self-signed certificates the TLS startup cases generate.
var testClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test-only self-signed cert
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type apiProc struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
	base   string
	port   int
	exited chan struct{}
	err    error
}

// startAPIProc execs the API with env (plus the inherited non-XMUSTARD_ environment) and
// waits until /api/health answers or the process exits (e.g. a startup refusal).
func startAPIProc(t *testing.T, env map[string]string) *apiProc {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command(apiBinary(t))
	// Inherited XMUSTARD_* settings (tokens, TLS, CORE_ONLY, auth mode, …) would
	// contaminate a fixture, so only non-xMustard variables are inherited.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "XMUSTARD_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "XMUSTARD_API_PORT="+fmt.Sprint(port))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	scheme := "http"
	if env["XMUSTARD_API_TLS_CERT"] != "" {
		scheme = "https"
	}
	p := &apiProc{cmd: cmd, stderr: &syncBuffer{}, base: fmt.Sprintf("%s://127.0.0.1:%d", scheme, port), port: port, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	cmd.Stdout = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = cmd.Process.Kill()
			<-p.exited
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			return p
		default:
		}
		if resp, err := testClient.Get(p.base + "/api/health"); err == nil {
			resp.Body.Close()
			return p
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("api did not become healthy: %s", p.stderr.String())
	return nil
}

func (p *apiProc) waitExit(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-p.exited:
	case <-time.After(timeout):
		t.Fatalf("api did not exit within %v: %s", timeout, p.stderr.String())
	}
}
