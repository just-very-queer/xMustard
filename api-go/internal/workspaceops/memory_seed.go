package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// Graph-proximity recall (WS-28, PAR-RCL-03). recall(seed=<symbol>) ranks the memories
// anchored near a symbol in the code graph: a memory's distance is the fewest hops from
// the seed to any file or symbol it is anchored to, walking dependents and dependencies
// (impact's resolved graph, at most seedHops deep), and it earns graphWeight/(1+d).
// Bounded diversity (MMR-style) then reorders the best seedCandidates so near-duplicates
// of one topic and directory do not fill the page.

// Seed bounds: the hops walked, the items read per depth and direction, the files and
// anchor rows one seed reads, and the ranked candidates diversity reorders.
const (
	seedHops       = 3
	seedTierItems  = 200
	seedMaxFiles   = 500
	seedMaxAnchors = 4000
	seedCandidates = 100
	// graphWeight scales the proximity signal: a memory on the seed's own file earns it
	// whole, one hop away half, two hops a third.
	graphWeight = 1.0
	// mmrLambda is how much of a candidate's score a repeated topic or directory costs.
	mmrLambda = 0.3
	// mmrStep separates consecutive picks so a later sort keeps their order.
	mmrStep = 1e-9
)

// seedHood is the seed's neighbourhood: hops to each file and symbol reached.
type seedHood struct {
	Found bool
	// Files maps a repo-relative path to its fewest hops; Symbols maps "path#name".
	Files   map[string]int
	Symbols map[string]int
}

// seedNeighbours reads a seed's neighbourhood from the code graph; tests replace it.
var seedNeighbours = seedNeighboursWithCore

// seedDirections are the walks whose reach a seed's neighbourhood joins.
var seedDirections = []string{"upstream", "downstream"}

func seedNeighboursWithCore(ctx context.Context, dataDir, workspaceID, seed string) (seedHood, error) {
	hood := seedHood{Files: map[string]int{}, Symbols: map[string]int{}}
	for _, direction := range seedDirections {
		raw, err := SymbolImpactCtx(ctx, dataDir, workspaceID, seed, map[string]string{
			"direction": direction, "depth": strconv.Itoa(seedHops), "limit": strconv.Itoa(seedTierItems)})
		if err != nil {
			return seedHood{}, err
		}
		var res struct {
			Graph   string `json:"graph"`
			Targets []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"targets"`
			ByDepth []struct {
				Depth int `json:"depth"`
				Items []struct {
					Name string `json:"name"`
					Path string `json:"path"`
				} `json:"items"`
			} `json:"by_depth"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return seedHood{}, fmt.Errorf("seed impact undecodable: %w", err)
		}
		hood.Found = hood.Found || (res.Graph != "none" && len(res.Targets) > 0)
		for _, t := range res.Targets {
			hood.reach(t.Path, t.Name, 0)
		}
		for _, tier := range res.ByDepth {
			for _, it := range tier.Items {
				hood.reach(it.Path, it.Name, tier.Depth)
			}
		}
	}
	return hood, nil
}

// reach records a file and symbol at d hops, keeping the fewest.
func (h *seedHood) reach(path, name string, d int) {
	if path == "" {
		return
	}
	if cur, ok := h.Files[path]; !ok || d < cur {
		h.Files[path] = d
	}
	if name == "" {
		return
	}
	key := path + "#" + lastSegment(name)
	if cur, ok := h.Symbols[key]; !ok || d < cur {
		h.Symbols[key] = d
	}
}

// distance is the hops from the seed to an anchor: a symbol anchor by its symbol (else
// its file), a path or code anchor by its file; false when it is outside the walk.
func (h seedHood) distance(a govstore.Anchor) (int, bool) {
	file := a.Value
	switch a.Kind {
	case govstore.AnchorSymbol:
		file, _, _ = strings.Cut(a.SymbolUID, "#")
		if d, ok := h.Symbols[a.SymbolUID]; ok {
			return d, true
		}
	case govstore.AnchorCode:
		file, _, _ = strings.Cut(a.Value, "#")
	}
	d, ok := h.Files[file]
	return d, ok
}

// memoryDistances maps each served memory anchored within the seed's neighbourhood to
// its fewest hops, through the anchor index (only the neighbourhood's files are read).
func memoryDistances(ctx context.Context, r govstore.Reader, workspaceID string, hood seedHood) (map[string]int, error) {
	// nearest files first (then by name): the file and anchor caps keep the nearest
	files := slices.Sorted(maps.Keys(hood.Files))
	sort.SliceStable(files, func(a, b int) bool { return hood.Files[files[a]] < hood.Files[files[b]] })
	files = files[:min(len(files), seedMaxFiles)]
	anchors, err := r.AnchorsInPaths(ctx, workspaceID, files, seedMaxAnchors)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, a := range anchors {
		if d, ok := hood.distance(a); ok {
			if cur, seen := out[a.EntryID]; !seen || d < cur {
				out[a.EntryID] = d
			}
		}
	}
	return out, nil
}

// graphSignal is the proximity score of a memory d hops from the seed.
func graphSignal(d int) float64 { return graphWeight / float64(1+d) }

// diversify reorders the first seedCandidates of a ranking by maximal marginal
// relevance, cheaply: each pick costs its score less mmrLambda times the share of its
// topic and directory keys an earlier pick already holds, so the work is one pass over
// at most seedCandidates entries per pick, never a pairwise similarity matrix. Ties
// keep the ranking's order. What lies past the first seedCandidates keeps its place.
func diversify(ranked []scoredEntry) {
	head := ranked[:min(len(ranked), seedCandidates)]
	picked := make([]scoredEntry, 0, len(head))
	held := map[string]bool{}
	floor := math.Inf(1)
	rest := slices.Clone(head)
	for len(rest) > 0 {
		best, bestScore := 0, 0.0
		for i, c := range rest {
			keys := diversityKeys(c.rank)
			covered := 0
			for _, k := range keys {
				if held[k] {
					covered++
				}
			}
			score := c.details.Total
			if len(keys) > 0 {
				score -= mmrLambda * float64(covered) / float64(len(keys))
			}
			if i == 0 || score > bestScore {
				best, bestScore = i, score
			}
		}
		chosen := rest[best]
		// keep the pick order through later re-sorts: a strictly falling key, as an offset
		// from the entry's own total so the stale penalty still moves it relative to it
		key := math.Min(bestScore, floor-mmrStep)
		floor = key
		chosen.shift = key - chosen.details.Total
		for _, k := range diversityKeys(chosen.rank) {
			held[k] = true
		}
		picked = append(picked, chosen)
		rest = slices.Delete(rest, best, best+1)
	}
	copy(head, picked)
	for i := len(head); i < len(ranked); i++ {
		floor = math.Min(ranked[i].details.Total, floor-mmrStep)
		ranked[i].shift = floor - ranked[i].details.Total
	}
}

// diversityKeys are what two memories share when they are alike: a topic, a kind and the
// directories of their paths.
func diversityKeys(e govstore.RankEntry) []string {
	var keys []string
	if e.Topic != "" {
		keys = append(keys, "topic:"+e.Topic)
	}
	for _, p := range e.Paths {
		keys = append(keys, "dir:"+path.Dir(p))
	}
	return keys
}
