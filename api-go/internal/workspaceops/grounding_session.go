package workspaceops

import (
	"fmt"
	"time"
)

// groundingSession is the session section of `ground`: the one-line summary an
// agent reads first and when the grounding was generated.
type groundingSession struct {
	Summary     string `json:"summary"`
	GeneratedAt string `json:"generated_at"`
}

func (s *groundingSession) stampGenerated() {
	s.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
}

// GroundingUnknown names a `ground` field that could not be determined and why.
// Such a field is null (or, for the stale-memory counts, incomplete), never a
// silent 0.
type GroundingUnknown struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// summarize writes the summary from the other sections; "?" marks an unknown count.
func (g *SessionGrounding) summarize() {
	count := func(n *int) string {
		if n == nil {
			return "?"
		}
		return fmt.Sprint(*n)
	}
	runs := fmt.Sprint(len(g.RecentFailedRuns))
	if g.BlockedByFailingVerification == nil {
		runs = "?"
	}
	g.Summary = fmt.Sprintf("%s changed file(s), %s dirty symbol(s), %s contract break(s), %s failed run(s), %s stale memory.",
		count(g.ChangedFiles), count(g.DirtySymbols), count(g.ContractBreaks), runs, count(g.StaleMemory))
	if n := g.MemoryVerificationModes[VerificationSelfAssertedOpen]; n > 0 {
		g.Summary += fmt.Sprintf(" %d memory self-asserted in open mode (not peer-verified).", n)
	}
	if g.PendingForYou != nil && *g.PendingForYou > 0 {
		g.Summary += fmt.Sprintf(" %d pending memory await your verification (recall status=awaiting_me).", *g.PendingForYou)
	}
	if len(g.Unknown) > 0 {
		g.Summary += fmt.Sprintf(" %d field(s) unknown (see unknown).", len(g.Unknown))
	}
}

// isUnknown reports whether field is listed unknown.
func (g *SessionGrounding) isUnknown(field string) bool {
	for _, u := range g.Unknown {
		if u.Field == field {
			return true
		}
	}
	return false
}
