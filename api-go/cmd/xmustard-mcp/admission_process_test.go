package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildShim(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "xmustard-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	return bin
}

// Audit Go #5 (MCP calls): the stdio reader started one goroutine per request with no
// admission limit. Beyond XMUSTARD_MCP_MAX_INFLIGHT outstanding calls, the shim must
// answer at once with a protocol-correct JSON-RPC overload error.
func TestShimRefusesCallsBeyondInflightLimit(t *testing.T) {
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	defer close(release)

	cmd := exec.Command(buildShim(t))
	cmd.Env = append(os.Environ(), "XMUSTARD_API_BASE="+api.URL, "XMUSTARD_MCP_MAX_INFLIGHT=2")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	for id := 1; id <= 3; id++ {
		fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"ground","arguments":{"workspace_id":"ws"}}}`+"\n", id)
	}
	lines := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	select {
	case line := <-lines:
		var resp struct {
			ID    json.RawMessage `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		if resp.Error == nil || resp.Error.Code != -32000 || string(resp.ID) != "3" {
			t.Fatalf("want overload error -32000 for id 3, got %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("third concurrent call was neither answered nor refused within 3s (no admission limit)")
	}
}

// Fable runtime review #1: the shim read, copied and decoded request frames before any
// admission, so a 1 MiB pool still forwarded 6×7 MiB frames (shim RSS 183 MB). A frame
// that cannot be reserved must be refused with -32000 before it is buffered/decoded or
// forwarded, and a small frame must still work.
func TestShimIngressFramesAreAdmitted(t *testing.T) {
	var mu sync.Mutex
	forwarded := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		forwarded++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	cmd := exec.Command(buildShim(t))
	cmd.Env = append(os.Environ(), "XMUSTARD_API_BASE="+api.URL, "XMUSTARD_TRANSIENT_BYTE_BUDGET=1048576")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	go func() {
		big := strings.Repeat("m", 7<<20)
		for id := 1; id <= 6; id++ {
			fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"remember","arguments":{"workspace_id":"ws","content":"%s"}}}`+"\n", id, big)
		}
		fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ground","arguments":{"workspace_id":"ws"}}}`+"\n")
	}()
	refused, smallOK := 0, false
	deadline := time.After(20 * time.Second)
	for refused+btoi(smallOK) < 7 {
		select {
		case line := <-lines:
			var resp struct {
				ID    json.RawMessage `json:"id"`
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(line), &resp)
			switch {
			case string(resp.ID) == "7" && resp.Error == nil:
				smallOK = true
			case resp.Error != nil && resp.Error.Code == -32000:
				refused++
			default:
				t.Fatalf("unexpected reply %.200s", line)
			}
		case <-deadline:
			t.Fatalf("replies: refused=%d smallOK=%v", refused, smallOK)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if forwarded != 1 {
		t.Fatalf("frames over the pool must not be forwarded: api saw %d requests", forwarded)
	}
	if rss := processRSSKiB(cmd.Process.Pid); rss > 64<<10 {
		t.Fatalf("shim RSS %d KiB after refusing oversized frames", rss)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func processRSSKiB(pid int) int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}
