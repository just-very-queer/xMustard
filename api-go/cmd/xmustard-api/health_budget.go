package main

import (
	"log"

	"xmustard/api-go/internal/budget"
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
