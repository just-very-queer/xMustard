package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// Review: the id of a refused frame must come from a real top-level parse of the
// bounded prefix, never a regex that can pick a nested or escaped "id".
func TestProbeIDUsesOnlyTopLevelID(t *testing.T) {
	cases := []struct {
		frame string
		want  string
	}{
		{`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{}}`, `5`},
		{`{"jsonrpc":"2.0","id":"a\"b","method":"x"}`, `"a\"b"`},
		{`{"method":"x\"id\":7","id":3}`, `3`},
		{`{"params":{"id":99,"arguments":{"id":98}},"id":4}`, `4`},
		// nested id first and the top-level id beyond the bounded prefix: unknown
		{`{"params":{"id":99,"content":"` + strings.Repeat("x", 4000) + `"},"id":6}`, ``},
		{`{"params":{"id":99}`, ``},
		{`not json "id":1`, ``},
	}
	for _, c := range cases {
		got := probeID([]byte(c.frame[:min(len(c.frame), idProbeBytes)]))
		if string(got) != c.want {
			t.Fatalf("probeID(%.60s…) = %q, want %q", c.frame, got, c.want)
		}
	}
}

// Review: a saturated pool must not stop a client from cancelling the calls that hold
// it. End-to-end smoke only: the pool here is not provably full (see the in-process
// saturation test below for the proof).
func TestCancellationServiceableUnderPoolSaturation(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer api.Close()
	cmd := exec.Command(buildShim(t))
	cmd.Env = append(os.Environ(), "XMUSTARD_API_BASE="+api.URL, "XMUSTARD_TRANSIENT_BYTE_BUDGET=204800")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	content := strings.Repeat("c", 40<<10)
	fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"workspace_id":"ws","content":"%s"}}}`+"\n", content)
	time.Sleep(300 * time.Millisecond)
	// the pool is now held by call 1: a further large frame is refused...
	fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remember","arguments":{"workspace_id":"ws","content":"%s"}}}`+"\n", content)
	// ...but the cancellation for call 1 still gets through
	fmt.Fprintf(stdin, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"user"}}`+"\n")
	got := map[string]string{}
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case line := <-lines:
			var resp struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal([]byte(line), &resp)
			got[string(resp.ID)] = line
		case <-deadline:
			t.Fatalf("replies so far: %v (call 1 was not cancelled under saturation)", got)
		}
	}
	if !strings.Contains(got["2"], "-32000") {
		t.Fatalf("frame 2 should be refused under saturation: %s", got["2"])
	}
	if _, ok := got["1"]; !ok {
		t.Fatalf("call 1 not answered after cancellation: %v", got)
	}
}

// With the pool provably exhausted, a cancellation frame is still read (fixed control
// headroom) while a large frame is refused without being buffered.
func TestControlFramesReadUnderExhaustedPool(t *testing.T) {
	pool := budget.NewByteBudget(64)
	if !pool.Acquire(64) {
		t.Fatal("fill pool")
	}
	cancelFrame := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}` + "\n"
	big := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remember","arguments":{"content":"` + strings.Repeat("x", 64<<10) + `"}}}` + "\n"
	r := bufio.NewReaderSize(bytes.NewReader([]byte(cancelFrame+big)), 64<<10)
	f := readAdmittedLine(r, budget.NewScope(pool))
	if !f.headroom || f.refused || !strings.Contains(string(f.line), "notifications/cancelled") {
		t.Fatalf("cancellation frame must be read from headroom: %+v", f.headroom)
	}
	f = readAdmittedLine(r, budget.NewScope(pool))
	if !f.refused || f.line != nil || string(probeID(f.probe)) != "2" {
		t.Fatalf("large frame under an exhausted pool must be refused unbuffered with its id: refused=%v", f.refused)
	}
	if pool.InUse() != 64 {
		t.Fatalf("headroom must not draw on the pool: %d", pool.InUse())
	}
}
