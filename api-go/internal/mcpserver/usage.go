package mcpserver

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Per-tool usage accounting (PAR-EVAL-04): calls, failures, argument and response
// bytes, an estimate of the response tokens, latency and argument normalizations,
// per tool and per client profile, for this process. The API reports them in
// /api/health ("mcp_usage") for its Streamable HTTP endpoint.

// ClientProfiles are the client names ?client= (and the relay's --client) accept.
var ClientProfiles = []string{"claude-code", "codex", "cursor", "opencode", "pi", "letta"}

// ParseClientProfile validates a client profile; blank is "unspecified".
func ParseClientProfile(v string) (string, error) {
	c := strings.ToLower(strings.TrimSpace(v))
	switch {
	case c == "":
		return "unspecified", nil
	case slices.Contains(ClientProfiles, c):
		return c, nil
	}
	return "", fmt.Errorf("unknown client %q; use one of %s", v, strings.Join(ClientProfiles, ", "))
}

// Modes a connection runs in: every tool, or only the read-only ones.
const (
	ModeFull     = "full"
	ModeReadOnly = "readonly"
)

// ParseMode reads ?mode= (and the relay's --mode); blank is full.
func ParseMode(v string) (readOnly bool, err error) {
	switch m := strings.ToLower(strings.TrimSpace(v)); m {
	case "", ModeFull:
		return false, nil
	case ModeReadOnly, "read-only", "read_only":
		return true, nil
	}
	return false, fmt.Errorf("unknown mode %q; use %s or %s", v, ModeFull, ModeReadOnly)
}

// modeFilter narrows a tools/list allow set (nil: every tool) to the read-only tools
// when the connection is read-only.
func (s *Session) modeFilter(allowed map[string]bool) map[string]bool {
	if !s.srv.opts.ReadOnly {
		return allowed
	}
	out := map[string]bool{}
	for _, t := range Tools() {
		if t.Annotations.ReadOnly && (allowed == nil || allowed[t.Name]) {
			out[t.Name] = true
		}
	}
	return out
}

// ToolUsage is one tool's accumulated usage.
type ToolUsage struct {
	Calls         int64 `json:"calls"`
	Errors        int64 `json:"errors"`
	ArgBytes      int64 `json:"arg_bytes"`
	ResponseBytes int64 `json:"response_bytes"`
	TokensEst     int64 `json:"response_tokens_est"`
	LatencyMicros int64 `json:"latency_us"`
	Normalized    int64 `json:"normalized_args"`
}

func (u *ToolUsage) add(o ToolUsage) {
	u.Calls += o.Calls
	u.Errors += o.Errors
	u.ArgBytes += o.ArgBytes
	u.ResponseBytes += o.ResponseBytes
	u.TokensEst += o.TokensEst
	u.LatencyMicros += o.LatencyMicros
	u.Normalized += o.Normalized
}

// UsageSnapshot is the process's MCP usage by tool and by client profile.
type UsageSnapshot struct {
	Tools   map[string]ToolUsage `json:"tools"`
	Clients map[string]ToolUsage `json:"clients"`
}

var usage = struct {
	sync.Mutex
	tools, clients map[string]*ToolUsage
}{tools: map[string]*ToolUsage{}, clients: map[string]*ToolUsage{}}

// Usage returns a copy of the usage counters.
func Usage() UsageSnapshot {
	usage.Lock()
	defer usage.Unlock()
	snap := UsageSnapshot{Tools: map[string]ToolUsage{}, Clients: map[string]ToolUsage{}}
	for k, v := range usage.tools {
		snap.Tools[k] = *v
	}
	for k, v := range usage.clients {
		snap.Clients[k] = *v
	}
	return snap
}

// recordUsage accounts one tools/call. res is the result (nil on a protocol error).
func (s *Session) recordUsage(tool string, argBytes int, res map[string]any, rerr *RPCError, start time.Time, normalized int) {
	u := ToolUsage{Calls: 1, ArgBytes: int64(argBytes), LatencyMicros: time.Since(start).Microseconds(), Normalized: int64(normalized)}
	if isErr, _ := res["isError"].(bool); isErr || rerr != nil {
		u.Errors = 1
	}
	content, _ := res["content"].([]map[string]any)
	for _, c := range content {
		text, _ := c["text"].(string)
		u.ResponseBytes += int64(len(text))
	}
	u.TokensEst = (u.ResponseBytes + 3) / 4
	client := s.srv.opts.Client
	if client == "" {
		client = "unspecified"
	}
	usage.Lock()
	defer usage.Unlock()
	bump(usage.tools, tool, u)
	bump(usage.clients, client, u)
}

func bump(m map[string]*ToolUsage, key string, u ToolUsage) {
	if m[key] == nil {
		m[key] = &ToolUsage{}
	}
	m[key].add(u)
}
