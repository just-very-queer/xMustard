package workspaceops

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Global (not workspace-scoped) audit trail of authentication/authorization events:
// token mint/revoke/rotate, workspace registrations and denied requests. Distinct
// from the per-workspace governance audit log (audit_log.go). Append-only with a
// bounded tail so a flood of denied requests can't fill the disk — the oldest events
// roll off, token administration last (trimAuthAudit).

const authAuditMax = 5000

// authFieldMax caps each attacker-influenced string field, so the count cap
// (authAuditMax) actually bounds the file size — without it, a long
// RemoteAddr/Path/Detail per denied request amplifies into an unbounded file.
const authFieldMax = 256

// minDeniedInterval throttles persisting "denied" events. An unauthenticated flood
// would otherwise trigger an O(n) full-file rewrite per request AND roll the
// genuine mint/revoke history out of the capped log. We persist at most one denied
// event per interval, carrying a suppressed-count so the flood is still visible.
const minDeniedInterval = time.Second

// authAuditMu serializes the read-modify-write on the shared auth-audit file so
// concurrent denials don't clobber each other's appends.
var authAuditMu sync.Mutex

// auditSeq makes EventIDs unique regardless of same-millisecond bursts or the
// length cap (which would otherwise pin the count suffix to a constant).
var auditSeq atomic.Uint64

// denied-event throttle state (guarded by deniedThrottleMu).
var (
	deniedThrottleMu sync.Mutex
	lastDeniedWrite  time.Time
	deniedSuppressed int
)

func clipField(s string) string {
	if len(s) > authFieldMax {
		return s[:authFieldMax] + "…"
	}
	return s
}

type AuthAuditEvent struct {
	EventID    string `json:"event_id"`
	Action     string `json:"action"` // mint | revoke | rotate | denied | register
	Actor      string `json:"actor"`  // principal id performing the action (or "anonymous")
	TokenID    string `json:"token_id,omitempty"`
	Role       string `json:"role,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func authAuditPath(dataDir string) string {
	return filepath.Join(dataDir, "auth_audit.json")
}

func loadAuthAudit(dataDir string) []AuthAuditEvent {
	var events []AuthAuditEvent
	if err := readJSON(authAuditPath(dataDir), &events); err != nil {
		return []AuthAuditEvent{}
	}
	return events
}

// RecordAuthAudit appends an auth event (best-effort: auditing must never break the
// request path). Attacker-influenced fields are length-clipped; "denied" events are
// rate-throttled (carrying a suppressed count) so a flood can't rewrite the file per
// request or evict the genuine mint/revoke/rotate history. Capped at authAuditMax.
func RecordAuthAudit(dataDir string, ev AuthAuditEvent) {
	if strings.TrimSpace(ev.Action) == "" {
		return
	}
	// Throttle denied-event floods: keep at most one persisted per interval.
	if ev.Action == "denied" {
		deniedThrottleMu.Lock()
		now := time.Now()
		if !lastDeniedWrite.IsZero() && now.Sub(lastDeniedWrite) < minDeniedInterval {
			deniedSuppressed++
			deniedThrottleMu.Unlock()
			return
		}
		suppressed := deniedSuppressed
		deniedSuppressed = 0
		lastDeniedWrite = now
		deniedThrottleMu.Unlock()
		if suppressed > 0 {
			ev.Detail = strings.TrimSpace(ev.Detail + " (+" + itoa(suppressed) + " suppressed since last)")
		}
	}

	ev.Actor = clipField(fallbackStr(strings.TrimSpace(ev.Actor), "anonymous"))
	ev.TokenID = clipField(ev.TokenID)
	ev.Detail = clipField(ev.Detail)
	ev.Method = clipField(ev.Method)
	ev.Path = clipField(ev.Path)
	ev.RemoteAddr = clipField(ev.RemoteAddr)

	authAuditMu.Lock()
	defer authAuditMu.Unlock()
	events := loadAuthAudit(dataDir)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ev.CreatedAt = now
	ev.EventID = "authaudit_" + compactTimestamp(now) + "_" + padCount(int(auditSeq.Add(1)))
	events = append(events, ev)
	_ = writeJSON(authAuditPath(dataDir), trimAuthAudit(events, authAuditMax))
}

// tokenAdministration reports whether action changed which tokens exist: an admin's
// mint, revoke or rotate.
func tokenAdministration(action string) bool {
	return action == "mint" || action == "revoke" || action == "rotate"
}

// trimAuthAudit keeps the newest max events, but rolls token administration off
// last: other events (denied requests, and registrations, which a non-admin token
// can cause) go first, oldest first. So neither a flood nor an agent's
// registrations can evict the mint/revoke/rotate history an admin audits.
func trimAuthAudit(events []AuthAuditEvent, max int) []AuthAuditEvent {
	excess := len(events) - max
	if excess <= 0 {
		return events
	}
	kept := events[:0]
	for _, ev := range events {
		if excess > 0 && !tokenAdministration(ev.Action) {
			excess--
			continue
		}
		kept = append(kept, ev)
	}
	if len(kept) > max {
		kept = kept[len(kept)-max:]
	}
	return kept
}

// ListAuthAudit returns auth events newest-first (capped by limit; 0 = all).
func ListAuthAudit(dataDir string, limit int) []AuthAuditEvent {
	events := loadAuthAudit(dataDir)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events
}
