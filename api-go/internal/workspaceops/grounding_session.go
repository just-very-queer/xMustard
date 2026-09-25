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

// summarize writes the summary from the other sections.
func (g *SessionGrounding) summarize() {
	g.Summary = fmt.Sprintf("%d changed file(s), %d dirty symbol(s), %d contract break(s), %d failed run(s), %d stale memory.",
		g.ChangedFiles, g.DirtySymbols, g.ContractBreaks, len(g.RecentFailedRuns), g.StaleMemory)
	if n := g.MemoryVerificationModes[VerificationSelfAssertedOpen]; n > 0 {
		g.Summary += fmt.Sprintf(" %d memory self-asserted in open mode (not peer-verified).", n)
	}
}
