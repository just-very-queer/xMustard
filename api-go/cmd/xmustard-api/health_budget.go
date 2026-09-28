package main

import (
	"log"
	"net/http"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/rustcore"
	"xmustard/api-go/internal/workspaceops"
)

// healthBudget is the /api/health "budget" block (PAR-OPS-01): per-component reserved
// and used memory, the heavy slot's owner and queue, the RSS watchdog and reclaim
// requests, the byte pool and child limit, Go GC and heap stats, the data-movement
// counters (PAR-EVAL-04), the resident Rust worker (WS-06B), plus the API's own body
// admission.
type healthBudget struct {
	budget.Snapshot
	CoreWorker          rustcore.WorkerHealth `json:"core_worker"`
	RequestBodyCapBytes int64                 `json:"request_body_cap_bytes"`
	InFlightBodies      slotStatus            `json:"in_flight_bodies"`
}

type slotStatus struct {
	Cap   int `json:"cap"`
	InUse int `json:"in_use"`
}

// publicBudget is what a caller without the full view sees: the static gate and soft
// ceiling, and nothing about activity.
type publicBudget struct {
	Version          int    `json:"version"`
	GateBytes        int64  `json:"gate_bytes"`
	SoftCeilingBytes int64  `json:"soft_ceiling_bytes"`
	Detail           string `json:"detail"`
}

// healthResponse is the /api/health body. Health stays public for liveness probes. The
// full view (the budget block, the live pool and child counters, and mcp_usage) shows host-wide
// activity: captures, hashed bytes, spawns, the owned tree and live external processes,
// the stdio shims on the host, the heavy-slot owner and queue, and the worker's pid and
// memory. Each uncached call also samples the process tree. So while authentication is
// enforced (XMUSTARD_AUTH=required, or auto with credentials minted, as on every
// non-loopback bind) only an operator sees it: an admin or other non-reader token with
// no workspace scope. Everyone else (no token, a reader-only token, or a
// workspace-scoped token of any role) sees the static limits, and nothing is sampled.
func healthResponse(r *http.Request) map[string]any {
	full, detail := healthBudgetView(r)
	body := map[string]any{"status": "ok", "service": "api-go"}
	// the governance store's startup check (WS-58), once the daemon ran it
	store := workspaceops.MemoryStoreHealth(dataDir())
	if !full {
		body["transient_pool"] = map[string]any{"max": budget.TransientBytes.Max()}
		body["children"] = map[string]any{"cap": budget.Children.Cap()}
		body["budget"] = publicBudget{Version: 1, GateBytes: budget.GateBytes, SoftCeilingBytes: budget.Gov.SoftCeiling(), Detail: detail}
		if store.Status != "" {
			body["store"] = store.Public()
		}
		return body
	}
	if store.Status != "" {
		body["store"] = store
	}
	// admission counters (bench/diagnostics): bytes xMustard reserved, not RSS
	body["transient_pool"] = map[string]any{"max": budget.TransientBytes.Max(), "in_use": budget.TransientBytes.InUse(), "peak": budget.TransientBytes.Peak()}
	body["children"] = map[string]any{"cap": budget.Children.Cap(), "in_use": budget.Children.InUse(), "peak": budget.Children.Peak()}
	body["budget"] = healthBudgetBlock()
	// the /mcp endpoint's per-tool and per-client counters are activity too
	body["mcp_usage"] = mcpserver.Usage()
	return body
}

// healthBudgetView applies the auth middleware's rule (enforce when mode is required or
// credentials exist) to a route that middleware does not guard, then the operator rule
// above. detail says why a caller gets only the public view.
func healthBudgetView(r *http.Request) (full bool, detail string) {
	mode := strings.ToLower(strings.TrimSpace(envDefault("XMUSTARD_AUTH", "auto")))
	if mode == "off" {
		return true, ""
	}
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	principal, configured := workspaceops.ResolveAuth(dataDir(), token)
	switch {
	case principal == nil && (mode == "required" || configured):
		return false, "authentication is enforced: send an operator bearer token (admin, or another non-reader role with no workspace scope) for the full budget block"
	case principal == nil:
		return true, "" // auto mode with no credentials: the open loopback default
	case principal.PresenceOnly:
		return false, "a presence-only token is accepted only at the xmustard-ops prompt, never as a bearer token"
	case len(principal.Workspaces) > 0:
		return false, "the full budget block shows host-wide activity, so a workspace-scoped token sees only the limits"
	case principal.ReadOnly():
		return false, "the full budget block shows host-wide activity, so a reader-only token sees only the limits"
	}
	return true, ""
}

func healthBudgetBlock() healthBudget {
	return healthBudget{
		Snapshot:            budget.Status(),
		CoreWorker:          rustcore.CoreWorkerHealth(),
		RequestBodyCapBytes: budget.CapToPool(maxRequestBodyBytesConfigured()),
		InFlightBodies:      slotStatus{Cap: cap(bodyInFlight), InUse: len(bodyInFlight)},
	}
}

// applyRuntimeHygiene sets the Go soft memory limit to the daemon's line, with the GOGC
// floor, unless GOMEMLIMIT was given (PAR-RT-05).
func applyRuntimeHygiene() {
	source, limit := budget.ApplyMemoryLimit()
	s := budget.Status()
	log.Printf("memory: Go soft limit %d bytes (%s, GOGC floor %d%%); transient pool %d bytes; heavy slot wait %d ms; tree soft ceiling %d bytes (%s)",
		limit, source, s.Runtime.GOGCFloorPercent, s.TransientPool.Max, s.HeavySlot.WaitBoundMS, s.SoftCeilingBytes, s.Watchdog.Last.Basis)
}
