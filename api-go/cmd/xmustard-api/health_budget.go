package main

import (
	"log"
	"net/http"
	"strings"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

// healthBudget is the /api/health "budget" block (PAR-OPS-01): per-component reserved
// and used memory, the heavy slot's owner and queue, the RSS watchdog, the byte pool and
// child limit, Go GC and heap stats, and the data-movement counters (PAR-EVAL-04), plus
// the API's own body admission.
type healthBudget struct {
	budget.Snapshot
	RequestBodyCapBytes int64      `json:"request_body_cap_bytes"`
	InFlightBodies      slotStatus `json:"in_flight_bodies"`
}

type slotStatus struct {
	Cap   int `json:"cap"`
	InUse int `json:"in_use"`
}

// publicBudget is what an unauthenticated caller sees while authentication is enforced:
// the static gate and soft ceiling, and nothing about activity.
type publicBudget struct {
	Version          int    `json:"version"`
	GateBytes        int64  `json:"gate_bytes"`
	SoftCeilingBytes int64  `json:"soft_ceiling_bytes"`
	Detail           string `json:"detail"`
}

// healthBudgetFor is the budget block for one /api/health request. Health stays public
// for liveness probes, but while authentication is enforced (XMUSTARD_AUTH=required, or
// auto with credentials minted, as on every non-loopback bind) the block needs a valid
// bearer token: it shows activity (captures, hashed bytes, spawns, live external
// processes, heavy-slot owner and queue) and each uncached call samples the process
// tree. Without a token only the gate and the soft ceiling are shown and nothing is
// sampled.
func healthBudgetFor(r *http.Request) any {
	if !healthBudgetVisible(r) {
		return publicBudget{Version: 1, GateBytes: budget.GateBytes, SoftCeilingBytes: budget.Gov.SoftCeiling(),
			Detail: "authentication is enforced: send a bearer token for the full budget block"}
	}
	return healthBudgetBlock()
}

// healthBudgetVisible applies the auth middleware's rule (enforce when mode is required
// or credentials exist) to a route that middleware does not guard.
func healthBudgetVisible(r *http.Request) bool {
	mode := strings.ToLower(strings.TrimSpace(envDefault("XMUSTARD_AUTH", "auto")))
	if mode == "off" {
		return true
	}
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	principal, configured := workspaceops.ResolveAuth(dataDir(), token)
	return principal != nil || (mode != "required" && !configured)
}

func healthBudgetBlock() healthBudget {
	return healthBudget{
		Snapshot:            budget.Status(),
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
