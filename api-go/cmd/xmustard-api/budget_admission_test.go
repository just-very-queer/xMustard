package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// The default pool is 24 MiB (PAR-RT-04), so the plan's five concurrent 16 MiB
// captures now admit exactly one and refuse the other four with 503; in-use bytes never
// exceed the pool. (TestFiveConcurrent16MiBCapturesAgainst64MiBPool keeps the old pool.)
func TestFiveConcurrent16MiBCapturesAgainstDefaultPool(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(budget.DefaultTransientBudgetBytes)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	f := newEvidenceFixture(t, true)
	const size = 16<<20 - 1024
	payload := bytes.Repeat([]byte("line of tool output ok\n"), size/23)
	release := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr, pw := io.Pipe()
			go func() {
				_, _ = pw.Write(payload[:1<<20])
				<-release
				_, _ = pw.Write(payload[1<<20:])
				pw.Close()
			}()
			req, _ := http.NewRequestWithContext(context.Background(), "POST", f.srv.URL+"/api/workspaces/"+f.ws+"/evidence?tool=search", pr)
			req.ContentLength = int64(len(payload))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes[i] = -1
				once.Do(func() { close(release) })
				return
			}
			resp.Body.Close()
			codes[i] = resp.StatusCode
			once.Do(func() { close(release) })
		}(i)
	}
	go func() { time.Sleep(10 * time.Second); once.Do(func() { close(release) }) }()
	wg.Wait()
	refused, ok := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusServiceUnavailable:
			refused++
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("every capture attempt must be admitted (200) or refused (503), got %v", codes)
		}
	}
	if ok != 1 || refused != 4 {
		t.Fatalf("a 24 MiB pool admits one 16 MiB capture at a time: codes=%v", codes)
	}
	if pool.Peak() > pool.Max() || pool.InUse() != 0 {
		t.Fatalf("pool peak %d (max %d), in use after %d", pool.Peak(), pool.Max(), pool.InUse())
	}
}

// Lowering the pool below the request body cap must not turn permanently inadmissible
// bodies into retryable overloads: a body larger than the whole pool is 413 without
// Retry-After, declared or chunked.
func TestBodyLargerThanPoolIs413NotRetryable(t *testing.T) {
	prev := budget.TransientBytes
	budget.TransientBytes = budget.NewByteBudget(1 << 20)
	defer func() { budget.TransientBytes = prev }()
	ran := false
	h := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	for _, declared := range []bool{true, false} {
		ran = false
		req, _ := http.NewRequest("POST", "/api/workspaces/ws/context", strings.NewReader(strings.Repeat("a", 2<<20)))
		if !declared {
			req.ContentLength = -1
		}
		rec := newRecorder()
		h.ServeHTTP(rec, req)
		if ran || rec.code != http.StatusRequestEntityTooLarge || rec.header.Get("Retry-After") != "" {
			t.Fatalf("2 MiB body under a 1 MiB pool (declared=%v): want 413 without Retry-After, got %d ran=%v", declared, rec.code, ran)
		}
	}
	req, _ := http.NewRequest("POST", "/api/workspaces/ws/context", strings.NewReader(strings.Repeat("a", 512<<10)))
	rec := newRecorder()
	h.ServeHTTP(rec, req)
	if !ran || rec.code != http.StatusOK {
		t.Fatalf("a body that fits the pool must pass: %d", rec.code)
	}
}

type recorder struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func newRecorder() *recorder                    { return &recorder{header: http.Header{}} }
func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(p []byte) (int, error) { return r.buf.Write(p) }
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

// Audit Go #5 (request decode): every body, including one under the 1 MiB in-flight
// threshold, is charged against the pool before a handler reads it. A 300 KiB body
// fits a 512 KiB pool on its own, so with 300 KiB of it held by another request it is
// refused with the retryable 503 + Retry-After (not the permanent 413), and admitted
// once that request ends.
func TestSubThresholdBodyIsChargedAgainstThePool(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(512 << 10)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()
	ran := false
	h := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	other := budget.NewScope(pool)
	if err := other.Acquire(300 << 10); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("a", 300<<10)
	for _, declared := range []bool{true, false} {
		ran = false
		req, _ := http.NewRequest("POST", "/api/workspaces/ws/context", strings.NewReader(body))
		if !declared {
			req.ContentLength = -1
		}
		rec := newRecorder()
		h.ServeHTTP(rec, req)
		if ran || rec.code != http.StatusServiceUnavailable || rec.header.Get("Retry-After") == "" {
			t.Fatalf("300 KiB body with 212 KiB of the pool free (declared=%v): want 503 + Retry-After, got %d ran=%v", declared, rec.code, ran)
		}
	}
	if pool.InUse() != 300<<10 {
		t.Fatalf("a refused body must reserve nothing: %d in use", pool.InUse())
	}
	other.Close()
	req, _ := http.NewRequest("POST", "/api/workspaces/ws/context", strings.NewReader(body))
	rec := newRecorder()
	h.ServeHTTP(rec, req)
	if !ran || rec.code != http.StatusOK || pool.InUse() != 0 {
		t.Fatalf("the same body on an idle pool: %d ran=%v in use %d", rec.code, ran, pool.InUse())
	}
}

// Audit Go #5 (Go→Rust capture), retryable path: core output that fits an idle pool is
// refused with 503 + Retry-After while other requests hold the pool, and served once
// they finish; no reservation outlives its request.
func TestRustCaptureRefusedWhileThePoolIsBusy(t *testing.T) {
	url, pool := inProcessAPI(t, `printf '{"hits":[],"pad":"'; head -c 4194304 /dev/zero | tr '\0' 'a'; printf '"}'`)
	get := func() (int, string, []byte) {
		resp, err := testClient.Get(url + "/api/workspaces/ws/search?q=x")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Retry-After"), b
	}
	other := budget.NewScope(pool)
	if err := other.Acquire(21 << 20); err != nil {
		t.Fatal(err)
	}
	if code, retry, b := get(); code != http.StatusServiceUnavailable || retry == "" || !strings.Contains(string(b), "overload") {
		t.Fatalf("4 MiB of output with 3 MiB of the pool free: want 503 + Retry-After, got %d %q %.200s", code, retry, b)
	}
	other.Close()
	if code, _, b := get(); code != http.StatusOK || len(b) < 4<<20 {
		t.Fatalf("the same call on an idle pool: %d, %d bytes", code, len(b))
	}
	if pool.InUse() != 0 || pool.Peak() > pool.Max() {
		t.Fatalf("in use %d, peak %d of %d", pool.InUse(), pool.Peak(), pool.Max())
	}
}
