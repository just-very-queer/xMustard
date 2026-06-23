package workspaceops

import "sort"

// Verifier telemetry: per-agent reliability + pairwise agreement over the governed-memory
// verification history. This is the EVIDENCE a weighted quorum would later use — gather
// and surface it FIRST, before changing the gate, so any weighting is grounded in
// observed behavior, not guessed. The gate itself is unchanged (still an unweighted
// distinct-agent threshold).

type VerifierAgentStats struct {
	Agent              string  `json:"agent"`
	Votes              int     `json:"votes"`
	Approvals          int     `json:"approvals"`
	Rejections         int     `json:"rejections"`
	DecidedVotes       int     `json:"decided_votes"`        // votes on entries with a terminal outcome
	AlignedWithOutcome int     `json:"aligned_with_outcome"` // voted the same way the entry resolved
	AlignmentRate      float64 `json:"alignment_rate"`       // aligned / decided (reliability signal)
}

type VerifierPairAgreement struct {
	AgentA        string  `json:"agent_a"`
	AgentB        string  `json:"agent_b"`
	SharedEntries int     `json:"shared_entries"`
	Agreements    int     `json:"agreements"`
	AgreementRate float64 `json:"agreement_rate"` // correlation: high => the two rarely add independent signal
}

type VerifierTelemetry struct {
	WorkspaceID string                  `json:"workspace_id"`
	Agents      []VerifierAgentStats    `json:"agents"`
	Pairs       []VerifierPairAgreement `json:"pairs"`
	GeneratedAt string                  `json:"generated_at"`
}

// latestVotePerAgent collapses an entry's verification log to one vote per agent (the
// most recent by timestamp), so re-votes don't double-count.
func latestVotePerAgent(verifications []ContextVerification) map[string]bool {
	at := map[string]string{}
	vote := map[string]bool{}
	for _, v := range verifications {
		if v.Agent == "" {
			continue
		}
		if prev, ok := at[v.Agent]; !ok || v.At >= prev {
			at[v.Agent] = v.At
			vote[v.Agent] = v.Approve
		}
	}
	return vote
}

// ComputeVerifierTelemetry aggregates the verification history into per-agent reliability
// (how often an agent's vote matched the entry's eventual promoted/rejected outcome) and
// pairwise agreement (how often two agents voted the same on shared entries).
func ComputeVerifierTelemetry(dataDir, workspaceID string) (*VerifierTelemetry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	entries, err := ListContextEntries(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}

	type agg struct {
		votes, approvals, rejections, decided, aligned int
	}
	stats := map[string]*agg{}
	type pairKey struct{ a, b string }
	pairs := map[pairKey]*VerifierPairAgreement{}

	for i := range entries {
		e := &entries[i]
		votes := latestVotePerAgent(e.Verifications)

		// terminal outcome direction: promoted => approve resolved; rejected => reject
		// resolved; otherwise pending (no alignment signal).
		var outcome *bool
		if e.Promoted {
			t := true
			outcome = &t
		} else if e.Status == "rejected" {
			f := false
			outcome = &f
		}

		for agent, approve := range votes {
			s := stats[agent]
			if s == nil {
				s = &agg{}
				stats[agent] = s
			}
			s.votes++
			if approve {
				s.approvals++
			} else {
				s.rejections++
			}
			if outcome != nil {
				s.decided++
				if approve == *outcome {
					s.aligned++
				}
			}
		}

		// pairwise agreement over agents who both voted on this entry.
		agents := make([]string, 0, len(votes))
		for a := range votes {
			agents = append(agents, a)
		}
		sort.Strings(agents)
		for x := 0; x < len(agents); x++ {
			for y := x + 1; y < len(agents); y++ {
				k := pairKey{agents[x], agents[y]}
				p := pairs[k]
				if p == nil {
					p = &VerifierPairAgreement{AgentA: k.a, AgentB: k.b}
					pairs[k] = p
				}
				p.SharedEntries++
				if votes[agents[x]] == votes[agents[y]] {
					p.Agreements++
				}
			}
		}
	}

	out := &VerifierTelemetry{WorkspaceID: workspaceID, GeneratedAt: nowUTC()}
	for agent, s := range stats {
		rate := 0.0
		if s.decided > 0 {
			rate = float64(s.aligned) / float64(s.decided)
		}
		out.Agents = append(out.Agents, VerifierAgentStats{
			Agent: agent, Votes: s.votes, Approvals: s.approvals, Rejections: s.rejections,
			DecidedVotes: s.decided, AlignedWithOutcome: s.aligned, AlignmentRate: rate,
		})
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Agent < out.Agents[j].Agent })
	for _, p := range pairs {
		if p.SharedEntries > 0 {
			p.AgreementRate = float64(p.Agreements) / float64(p.SharedEntries)
		}
		out.Pairs = append(out.Pairs, *p)
	}
	sort.Slice(out.Pairs, func(i, j int) bool {
		if out.Pairs[i].AgentA != out.Pairs[j].AgentA {
			return out.Pairs[i].AgentA < out.Pairs[j].AgentA
		}
		return out.Pairs[i].AgentB < out.Pairs[j].AgentB
	})
	return out, nil
}
