package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Health is the part of /api/health the lifecycle reads.
type Health struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	// Store is the governance store's state after the daemon's startup check
	// (migration and quick_check), passed through as the daemon reports it.
	Store json.RawMessage `json:"store,omitempty"`
}

// healthClient probes the loopback daemon directly, never through a proxy.
var healthClient = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}

const probeInterval = 200 * time.Millisecond

// Probe asks base once whether the xMustard API answers there. Something else
// listening on the port is an error, not a healthy daemon.
func Probe(ctx context.Context, base string) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	var h Health
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&h)
	switch {
	case resp.StatusCode != http.StatusOK:
		return h, fmt.Errorf("%s answered HTTP %d", base, resp.StatusCode)
	case decodeErr != nil:
		return h, fmt.Errorf("%s answered something other than the xMustard health document: %v", base, decodeErr)
	case h.Service != "api-go" || h.Status != "ok":
		return h, fmt.Errorf("%s answered, but not as the xMustard API (service %q, status %q)", base, h.Service, h.Status)
	}
	return h, nil
}

// WaitHealthy probes base until the daemon answers or wait passes, and returns the
// last failure when it does not.
func WaitHealthy(ctx context.Context, probe func(context.Context, string) (Health, error), base string, wait time.Duration) (Health, error) {
	deadline := time.Now().Add(wait)
	for {
		h, err := probe(ctx, base)
		if err == nil || !time.Now().Before(deadline) {
			return h, err
		}
		select {
		case <-ctx.Done():
			return h, ctx.Err()
		case <-time.After(probeInterval):
		}
	}
}

// unloadWait bounds how long Stop waits for launchd to let a booted-out agent go; it
// covers the agent's ExitTimeOut.
const unloadWait = 35 * time.Second

// waitFor polls done until it holds or d passes.
func waitFor(ctx context.Context, d time.Duration, failure string, done func() bool) error {
	deadline := time.Now().Add(d)
	for !done() {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s after %s", failure, d)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(probeInterval):
		}
	}
	return nil
}
