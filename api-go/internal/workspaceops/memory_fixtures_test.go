package workspaceops

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// dedupeFixtures is eval/tasks/memory_lifecycle/dedupe_conflicts.json (PAR-EVAL-02).
type dedupeFixtures struct {
	Cases []struct {
		ID   string `json:"id"`
		Seed []struct {
			Key     string        `json:"key"`
			Content string        `json:"content"`
			Paths   []string      `json:"paths"`
			Claims  []MemoryClaim `json:"claims"`
			Served  bool          `json:"served"`
		} `json:"seed"`
		Propose struct {
			Content     string        `json:"content"`
			Paths       []string      `json:"paths"`
			Claims      []MemoryClaim `json:"claims"`
			Quarantined bool          `json:"quarantined"`
		} `json:"propose"`
		Expect struct {
			Outcome             string            `json:"outcome"`
			DuplicateOf         string            `json:"duplicate_of"`
			Duplicates          map[string]string `json:"duplicates"`
			Conflicts           map[string]string `json:"conflicts"`
			SuggestedSupersedes []string          `json:"suggested_supersedes"`
		} `json:"expect"`
		MergeInto string `json:"merge_into"`
	} `json:"cases"`
}

// The lifecycle suite's duplicate and contradiction fixtures pass through remember:
// every case's outcome, duplicates, conflicts and suggested supersessions are exactly
// the expected ones, and a verified duplicate_of merges.
func TestEval02DedupeConflictFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "eval", "tasks", "memory_lifecycle", "dedupe_conflicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx dedupeFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 10 {
		t.Fatalf("%d fixture cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.ID, func(t *testing.T) {
			dir, ws := multiAgentDir(t), "ws"
			keys := map[string]string{} // entry id -> seed key
			for _, s := range c.Seed {
				opts := func(r *RememberRequest) { r.Paths, r.Claims = s.Paths, s.Claims }
				var e *ContextEntry
				if s.Served {
					e = promoted(t, dir, ws, "author", s.Content, opts)
				} else {
					e = rememberAs(t, dir, ws, ContextActor{ID: "author"}, s.Content, opts)
				}
				keys[e.ID] = s.Key
			}
			who := ContextActor{ID: "agent-b"}
			if c.Propose.Quarantined {
				who.Quarantine = "untrusted_capture:webfetch"
			}
			got := rememberAs(t, dir, ws, who, c.Propose.Content, func(r *RememberRequest) {
				r.Paths, r.Claims = c.Propose.Paths, c.Propose.Claims
			})
			if got.Outcome != c.Expect.Outcome || keys[got.DuplicateOf] != c.Expect.DuplicateOf {
				t.Fatalf("outcome %q duplicate_of %q (%s), want %q %q", got.Outcome, got.DuplicateOf, keys[got.DuplicateOf],
					c.Expect.Outcome, c.Expect.DuplicateOf)
			}
			dups := map[string]string{}
			for _, d := range got.PossibleDuplicates {
				dups[keys[d.ID]] = d.Match
			}
			conflicts := map[string]string{}
			for _, cf := range got.PossibleConflicts {
				for _, id := range cf.EntryIDs {
					if id != got.ID {
						conflicts[keys[id]] = cf.Kind
					}
				}
			}
			var suggested []string
			for _, id := range got.SuggestedSupersedes {
				suggested = append(suggested, keys[id])
			}
			if !maps.Equal(dups, orEmpty(c.Expect.Duplicates)) || !maps.Equal(conflicts, orEmpty(c.Expect.Conflicts)) ||
				!slices.Equal(suggested, c.Expect.SuggestedSupersedes) {
				t.Fatalf("duplicates %v conflicts %v suggested %v; want %v %v %v (result %+v)", dups, conflicts, suggested,
					c.Expect.Duplicates, c.Expect.Conflicts, c.Expect.SuggestedSupersedes, got)
			}
			if c.MergeInto != "" {
				mergeFixture(t, dir, ws, got.ID, keyID(keys, c.MergeInto))
			}
		})
	}
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func keyID(keys map[string]string, key string) string {
	for id, k := range keys {
		if k == key {
			return id
		}
	}
	return ""
}

// mergeFixture has two peers verify dup duplicate_of target, then checks the merge: dup
// is merged and leaves the verification queue, and recall serves target.
func mergeFixture(t *testing.T, dir, ws, dup, target string) {
	t.Helper()
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, dup, ContextActor{ID: peer, Verifier: true},
			VerifyRequest{Outcome: OutcomeDuplicateOf, Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	if view, _ := entryView(t, dir, ws, dup, false); view.Lifecycle != govstore.LifecycleMerged {
		t.Fatalf("not merged: %+v", view)
	}
	res, err := RecallWith(context.Background(), dir, ws, RecallRequest{IncludePending: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range res["entries"].([]ContextEntry) {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []string{target}) {
		t.Fatalf("recall after the merge serves %v, want only %s", ids, target)
	}
}
