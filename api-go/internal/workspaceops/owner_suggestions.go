package workspaceops

import (
	"sort"
	"strings"
)

// Owner suggestions (FRONTIER Lane 2): suggest likely owners for an issue from
// inspectable signals — who verified this issue before, and who verified
// similar (duplicate-candidate) issues. No CODEOWNERS guessing; only recorded
// human signals, each with a reason.

type OwnerSuggestion struct {
	Owner   string   `json:"owner"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

func suggestOwners(issue issueRecord, all []issueRecord) []OwnerSuggestion {
	scores := map[string]int{}
	reasons := map[string][]string{}
	add := func(owner string, score int, reason string) {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			return
		}
		scores[owner] += score
		reasons[owner] = append(reasons[owner], reason)
	}

	if issue.VerifiedBy != nil {
		add(*issue.VerifiedBy, 5, "previously verified this issue")
	}

	byID := map[string]issueRecord{}
	for _, o := range all {
		byID[o.BugID] = o
	}
	for _, match := range duplicateMatches(issue, all) {
		target, ok := byID[match.TargetID]
		if !ok || target.VerifiedBy == nil {
			continue
		}
		score := 2
		if match.Similarity >= 0.99 {
			score = 4
		}
		add(*target.VerifiedBy, score, "verified a similar issue ("+match.TargetID+")")
	}

	out := make([]OwnerSuggestion, 0, len(scores))
	for owner, score := range scores {
		out = append(out, OwnerSuggestion{Owner: owner, Score: score, Reasons: reasons[owner]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}

// SuggestIssueOwners returns ranked owner suggestions for an issue.
func SuggestIssueOwners(dataDir, workspaceID, issueID string) ([]OwnerSuggestion, error) {
	issue, all, err := findIssueRecord(dataDir, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	return suggestOwners(issue, all), nil
}
