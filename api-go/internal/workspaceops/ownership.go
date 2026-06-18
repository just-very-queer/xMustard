package workspaceops

import (
	"sort"
	"strings"
)

// Agent identity / ownership history: the trail of which agents (runtime/model)
// and humans touched an issue, assembled from its runs, its verifier, and the
// audit log. Answers "who/what worked on this, and when".

type OwnershipEntry struct {
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type OwnershipHistory struct {
	IssueID        string           `json:"issue_id"`
	WorkspaceID    string           `json:"workspace_id"`
	Entries        []OwnershipEntry `json:"entries"`
	DistinctAgents []string         `json:"distinct_agents"`
	DistinctActors []string         `json:"distinct_actors"`
	GeneratedAt    string           `json:"generated_at"`
}

// BuildOwnershipHistory assembles the ownership trail for an issue.
func BuildOwnershipHistory(dataDir, workspaceID, issueID string) (*OwnershipHistory, error) {
	issue, _, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}

	entries := []OwnershipEntry{}
	agents := map[string]struct{}{}
	actors := map[string]struct{}{}

	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.IssueID != issueID {
			continue
		}
		agent := strings.TrimSpace(run.Runtime + "/" + run.Model)
		if agent != "/" {
			agents[agent] = struct{}{}
		}
		entries = append(entries, OwnershipEntry{
			At:      run.CreatedAt,
			Actor:   fallbackStr(run.Runtime, "agent"),
			Action:  "run." + fallbackStr(run.Status, "launched"),
			Runtime: run.Runtime,
			Model:   run.Model,
			Detail:  run.Title,
		})
	}

	if issue.VerifiedBy != nil && strings.TrimSpace(*issue.VerifiedBy) != "" {
		actors[*issue.VerifiedBy] = struct{}{}
		at := ""
		if issue.VerifiedAt != nil {
			at = *issue.VerifiedAt
		}
		entries = append(entries, OwnershipEntry{
			At:     at,
			Actor:  *issue.VerifiedBy,
			Action: "issue.verified",
		})
	}

	events, err := loadAuditEvents(dataDir, workspaceID)
	if err == nil {
		for _, e := range events {
			if e.TargetID != issueID {
				continue
			}
			if strings.TrimSpace(e.Actor) != "" {
				actors[e.Actor] = struct{}{}
			}
			entries = append(entries, OwnershipEntry{
				At:     e.CreatedAt,
				Actor:  e.Actor,
				Action: e.Action,
				Detail: e.Details,
			})
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At > entries[j].At })

	return &OwnershipHistory{
		IssueID:        issueID,
		WorkspaceID:    workspaceID,
		Entries:        entries,
		DistinctAgents: sortedKeys(agents),
		DistinctActors: sortedKeys(actors),
		GeneratedAt:    nowUTC(),
	}, nil
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
