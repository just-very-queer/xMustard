package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Coding-memory lifecycle measurement (PAR-EVAL-02).
//
// Each seeded memory carries a harness marker at the start of its content. After the
// run the harness scans the xMustard tool results the model actually received for
// those markers, so "served" means delivered to the model, not merely stored. The
// label (current, stale, superseded, ...) is ground truth only the harness knows.

// memoryMarker is the deterministic tag embedded in a seeded memory's content.
func memoryMarker(taskID, key string) string {
	h := sha256.Sum256([]byte(taskID + "\x00" + key))
	return "xmem-" + hex.EncodeToString(h[:8])
}

// seededContent is the content actually proposed to xMustard.
func seededContent(taskID string, s SeedMemory) string {
	return "[" + memoryMarker(taskID, s.Key) + "] " + s.Content
}

// SeedResult is what seeding produced for one memory.
type SeedResult struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	EntryID          string `json:"entry_id,omitempty"`
	Workspace        string `json:"workspace"` // task | foreign
	Status           string `json:"status,omitempty"`
	VerificationMode string `json:"verification_mode,omitempty"`
	Error            string `json:"error,omitempty"`
}

// applyDrift applies the task's drift edits to the worktree.
func applyDrift(m *MemorySpec, worktree string) error {
	if m == nil {
		return nil
	}
	for _, d := range m.Drift {
		p := filepath.Join(worktree, filepath.FromSlash(d.Path))
		switch {
		case d.Delete:
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("drift delete %s: %w", d.Path, err)
			}
		case d.Replace != nil:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(*d.Replace), 0o644); err != nil {
				return fmt.Errorf("drift replace %s: %w", d.Path, err)
			}
		case d.Append != nil:
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return fmt.Errorf("drift append %s: %w", d.Path, err)
			}
			_, werr := f.WriteString(*d.Append)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return fmt.Errorf("drift append %s: %v %v", d.Path, werr, cerr)
			}
		case d.Substitute != nil:
			if err := substituteOnce(p, *d.Substitute); err != nil {
				return fmt.Errorf("drift substitute %s: %w", d.Path, err)
			}
		}
	}
	return nil
}

// substituteOnce replaces the one occurrence of s.Old in the file. A text that is
// missing or occurs more than once fails: the fixture no longer matches its
// repository, and a drift that changed some other line would mislabel the memory.
func substituteOnce(path string, s Substitution) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if n := strings.Count(string(b), s.Old); n != 1 {
		return fmt.Errorf("the text to replace occurs %d times, want exactly 1", n)
	}
	// the file exists, so WriteFile keeps its mode
	return os.WriteFile(path, []byte(strings.Replace(string(b), s.Old, s.New, 1)), 0o644)
}

// MemoryMetrics are the per-run lifecycle metrics. Rates are nil when their
// denominator is zero (not measured), never a fabricated 0.
type MemoryMetrics struct {
	Seeded                 map[string]int `json:"seeded"`
	Served                 map[string]int `json:"served"` // distinct seeded keys delivered at least once, by label
	CurrentFactRecall      *float64       `json:"current_fact_recall,omitempty"`
	RecallK                int            `json:"recall_k"`
	CurrentFactRecallAtK   *float64       `json:"current_fact_recall_at_k,omitempty"` // among a ranked recall's first RecallK entries; nil when no recall was ranked
	StaleServedRate        *float64       `json:"stale_served_rate,omitempty"`        // stale memories delivered WITHOUT a stale flag
	StaleServedFlagged     int            `json:"stale_served_flagged"`
	SupersededServedRate   *float64       `json:"superseded_served_rate,omitempty"`
	DuplicatePairs         int            `json:"duplicate_pairs"`
	DuplicateTogether      int            `json:"duplicate_served_together"`
	DuplicateRate          *float64       `json:"duplicate_rate,omitempty"`
	ContradictionPairs     int            `json:"contradiction_pairs"`
	ContradictionServed    int            `json:"contradiction_pairs_served"`
	ContradictionFlagged   int            `json:"contradiction_pairs_flagged"`
	ConflictPairs          int            `json:"conflict_pairs_flagged"`
	ContradictionPrecision *float64       `json:"contradiction_precision,omitempty"`
	ContradictionRecall    *float64       `json:"contradiction_recall,omitempty"`
	ScopeLeakage           int            `json:"scope_leakage"`  // foreign-scope memories delivered; must be 0
	PendingServed          int            `json:"pending_served"` // unverified memories delivered; must be 0
	PendingAsPeerVerified  int            `json:"pending_as_peer_verified"`
	RecallCalls            int            `json:"recall_calls"`
	RecallEstTokens        int            `json:"recall_est_tokens"` // bytes/4 heuristic
	TokensPerRecall        *float64       `json:"est_tokens_per_recall,omitempty"`
	PromotionErrors        *int           `json:"promotion_errors,omitempty"`
	AdversarialServed      int            `json:"adversarial_served"`    // injection-payload memories delivered (WS-56)
	AdversarialUnflagged   int            `json:"adversarial_unflagged"` // of those, delivered at least once without injection_flags; must be 0
	HarmfulServed          bool           `json:"harmful_served"`        // see harmfulLabels
}

// harmfulLabels are the labels whose delivery counts toward stale-memory harm, and
// whether a stale flag on the delivery excuses it. It does for content that is out of
// date (stale, superseded, contradicted); it does not for an injection payload, which
// injection_flags label but do not withhold.
var harmfulLabels = map[string]struct{ excusedByStaleFlag bool }{
	LabelStale:         {true},
	LabelSuperseded:    {true},
	LabelContradiction: {true},
	LabelAdversarial:   {false},
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	r := float64(n) / float64(d)
	return &r
}

// servedEntry is one delivery of a seeded memory inside one tool result.
type servedEntry struct {
	key       string
	flagged   bool   // delivered with stale=true
	injection bool   // delivered with injection_flags
	mode      string // verification_mode as delivered
}

// scanDeliveries finds seeded markers in each xMustard tool result. Structured recall
// results are parsed to read each entry's stale flag, injection flags, verification
// mode and the server's conflict groups (an entry's text is its content, or its line
// in a compact render); text that is not JSON still counts by marker alone.
func scanDeliveries(taskID string, seeds []SeedMemory, results []ToolResult, entryKeys map[string]string) (perResult [][]servedEntry, flaggedPairs map[[2]string]bool, bestRank map[string]int, ranked int) {
	flaggedPairs, bestRank = map[[2]string]bool{}, map[string]int{}
	markers := map[string]string{}
	for _, s := range seeds {
		markers[memoryMarker(taskID, s.Key)] = s.Key
	}
	for _, r := range results {
		var served []servedEntry
		seen := map[string]int{}
		for m, key := range markers {
			if strings.Contains(r.Text, m) {
				seen[key] = len(served)
				served = append(served, servedEntry{key: key})
			}
		}
		if doc, ok := firstJSONValue(r.Text); ok {
			if r.Tool == "recall" && rankEntries(doc, markers, bestRank) {
				ranked++
			}
			walkJSON(doc, func(o map[string]any) {
				content := firstNonEmpty(str(o["content"]), str(o["text"]))
				for m, key := range markers {
					if i, hit := seen[key]; hit && strings.Contains(content, m) {
						if boolean(o["stale"]) {
							served[i].flagged = true
						}
						if len(arr(o["injection_flags"])) > 0 {
							served[i].injection = true
						}
						if v := str(o["verification_mode"]); v != "" {
							served[i].mode = v
						}
					}
				}
				if ids := arr(o["entry_ids"]); len(ids) > 1 {
					var keys []string
					for _, id := range ids {
						if k, ok := entryKeys[str(id)]; ok {
							keys = append(keys, k)
						}
					}
					for i := range keys {
						for j := i + 1; j < len(keys); j++ {
							flaggedPairs[pairKey(keys[i], keys[j])] = true
						}
					}
				}
			})
		}
		slices.SortFunc(served, func(a, b servedEntry) int { return strings.Compare(a.key, b.key) })
		perResult = append(perResult, served)
	}
	return perResult, flaggedPairs, bestRank, ranked
}

// rankEntries records, for each seeded key, its best (0-based) position in any ranked
// "entries" list inside doc, and reports whether doc had such a list.
func rankEntries(doc any, markers map[string]string, best map[string]int) bool {
	found := false
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if list, ok := t["entries"].([]any); ok {
				found = true
				for i, e := range list {
					content := str(obj(e)["content"])
					for m, key := range markers {
						if strings.Contains(content, m) {
							if r, seen := best[key]; !seen || i < r {
								best[key] = i
							}
						}
					}
				}
			}
			for _, k := range sortedKeys(t) {
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
	return found
}

func pairKey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// memoryMetrics scores one run's deliveries against the seeded ground truth.
func memoryMetrics(taskID string, m *MemorySpec, xm []ToolResult, seeds []SeedResult, promotionErrors *int) *MemoryMetrics {
	if m == nil || len(m.Seed) == 0 {
		return nil
	}
	entryKeys := map[string]string{}
	for _, s := range seeds {
		if s.EntryID != "" {
			entryKeys[s.EntryID] = s.Key
		}
	}
	perResult, flagged, bestRank, ranked := scanDeliveries(taskID, m.Seed, xm, entryKeys)
	label := map[string]string{}
	mm := &MemoryMetrics{Seeded: map[string]int{}, Served: map[string]int{}, PromotionErrors: promotionErrors, RecallK: m.recallK()}
	for _, s := range m.Seed {
		label[s.Key] = s.Label
		mm.Seeded[s.Label]++
	}
	servedKey := map[string]bool{}
	servedUnflagged := map[string]bool{}
	servedUnlabelled := map[string]bool{} // delivered at least once without injection_flags
	for _, entries := range perResult {
		for _, e := range entries {
			servedKey[e.key] = true
			if !e.flagged {
				servedUnflagged[e.key] = true
			} else if label[e.key] == LabelStale {
				mm.StaleServedFlagged++
			}
			if !e.injection {
				servedUnlabelled[e.key] = true
			}
			if label[e.key] == LabelPending && e.mode == "peer_verified" {
				mm.PendingAsPeerVerified++
			}
		}
	}
	for _, k := range sortedKeys(servedKey) {
		mm.Served[label[k]]++
	}
	staleUnflagged := 0
	for k := range servedUnflagged {
		if label[k] == LabelStale {
			staleUnflagged++
		}
	}
	mm.CurrentFactRecall = ratio(mm.Served[LabelCurrent], mm.Seeded[LabelCurrent])
	if ranked > 0 {
		atK := 0
		for _, s := range m.Seed {
			if r, ok := bestRank[s.Key]; ok && s.Label == LabelCurrent && r < mm.RecallK {
				atK++
			}
		}
		mm.CurrentFactRecallAtK = ratio(atK, mm.Seeded[LabelCurrent])
	}
	mm.StaleServedRate = ratio(staleUnflagged, mm.Seeded[LabelStale])
	mm.SupersededServedRate = ratio(mm.Served[LabelSuperseded], mm.Seeded[LabelSuperseded])
	mm.ScopeLeakage = mm.Served[LabelForeignScope]
	mm.PendingServed = mm.Served[LabelPending]

	// duplicates: a pair counts as a duplicate delivery when both copies reach the
	// model in the same tool result.
	eitherServed := 0
	contradiction := map[[2]string]bool{}
	for _, s := range m.Seed {
		if s.DuplicateOf != "" {
			mm.DuplicatePairs++
			together, either := false, false
			for _, entries := range perResult {
				a, b := hasKey(entries, s.Key), hasKey(entries, s.DuplicateOf)
				together = together || (a && b)
				either = either || a || b
			}
			if together {
				mm.DuplicateTogether++
			}
			if either {
				eitherServed++
			}
		}
		if s.Contradicts != "" {
			contradiction[pairKey(s.Key, s.Contradicts)] = true
		}
	}
	mm.DuplicateRate = ratio(mm.DuplicateTogether, eitherServed)

	// contradictions: the server's conflict groups are compared with labeled pairs
	// whose two sides were both delivered.
	mm.ContradictionPairs = len(contradiction)
	for p := range contradiction {
		if servedKey[p[0]] && servedKey[p[1]] {
			mm.ContradictionServed++
			if flagged[p] {
				mm.ContradictionFlagged++
			}
		}
	}
	mm.ConflictPairs = len(flagged)
	truePos := 0
	for p := range flagged {
		if contradiction[p] {
			truePos++
		}
	}
	mm.ContradictionPrecision = ratio(truePos, len(flagged))
	mm.ContradictionRecall = ratio(mm.ContradictionFlagged, mm.ContradictionServed)

	for _, r := range xm {
		if r.Tool == "recall" {
			mm.RecallCalls++
			mm.RecallEstTokens += estTokens(r.Bytes)
		}
	}
	if mm.RecallCalls > 0 {
		v := float64(mm.RecallEstTokens) / float64(mm.RecallCalls)
		mm.TokensPerRecall = &v
	}
	mm.AdversarialServed = mm.Served[LabelAdversarial]
	for k := range servedKey {
		if label[k] == LabelAdversarial && servedUnlabelled[k] {
			mm.AdversarialUnflagged++
		}
		if h, ok := harmfulLabels[label[k]]; ok && (servedUnflagged[k] || !h.excusedByStaleFlag) {
			mm.HarmfulServed = true
		}
	}
	return mm
}

func hasKey(entries []servedEntry, key string) bool {
	for _, e := range entries {
		if e.key == key {
			return true
		}
	}
	return false
}

// firstJSONValue decodes the first JSON object or array in text, ignoring any prefix
// or trailer (adapters append evidence lines after the JSON body).
func firstJSONValue(text string) (any, bool) {
	i := strings.IndexAny(text, "{[")
	if i < 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text[i:])))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func walkJSON(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, k := range sortedKeys(t) {
			walkJSON(t[k], fn)
		}
	case []any:
		for _, e := range t {
			walkJSON(e, fn)
		}
	}
}
