package workspaceops

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"xmustard/api-go/internal/rustcore"
)

// Knowledge layer delivery: hybrid search + wiki generation over the repo.

func WorkspaceSearch(dataDir, workspaceID, query string, limit int) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 25
	}
	out, err := rustcore.RunSearch(root, workspaceID, query, strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

type searchHit struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Path   string  `json:"path"`
	Line   *int    `json:"line,omitempty"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type searchResult struct {
	WorkspaceID string      `json:"workspace_id"`
	Query       string      `json:"query"`
	Total       int         `json:"total"`
	Hits        []searchHit `json:"hits"`
	GeneratedAt string      `json:"generated_at"`
}

// WorkspaceSearchReranked runs the live hybrid search, then fuses a NEURAL lane:
// it embeds the query and each hit (via an OpenAI-compatible provider's /embeddings
// endpoint) and re-ranks with Reciprocal Rank Fusion of the lexical/structural rank
// and the embedding-cosine rank. This is the opt-in neural upgrade to the model-free
// in-process search; default search stays fast and provider-free.
func WorkspaceSearchReranked(dataDir, workspaceID, query, provider, model string, limit int) (json.RawMessage, error) {
	raw, err := WorkspaceSearch(dataDir, workspaceID, query, limit*2)
	if err != nil {
		return nil, err
	}
	var res searchResult
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Hits) == 0 {
		return raw, nil //nolint:nilerr // nothing to rerank
	}

	inputs := make([]string, 0, len(res.Hits)+1)
	inputs = append(inputs, query)
	for _, h := range res.Hits {
		inputs = append(inputs, h.Name+" "+h.Path)
	}
	vecs, err := OpenAIEmbeddings(dataDir, provider, model, inputs)
	if err != nil || len(vecs) != len(inputs) {
		// neural lane unavailable → return the lexical/structural ranking unchanged.
		return raw, nil //nolint:nilerr
	}
	qv := vecs[0]

	// rank by the original (lexical/structural) score and by neural cosine, then RRF.
	cos := make([]float64, len(res.Hits))
	for i := range res.Hits {
		cos[i] = cosineFloat(qv, vecs[i+1])
	}
	lexRank := rankIndices(len(res.Hits), func(i int) float64 { return res.Hits[i].Score })
	neuRank := rankIndices(len(res.Hits), func(i int) float64 { return cos[i] })

	const k = 60.0
	fused := make([]float64, len(res.Hits))
	for rank, i := range lexRank {
		fused[i] += 1.0 / (k + float64(rank) + 1.0)
	}
	for rank, i := range neuRank {
		fused[i] += 1.0 / (k + float64(rank) + 1.0)
	}
	order := make([]int, len(res.Hits))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return fused[order[a]] > fused[order[b]] })

	out := make([]searchHit, 0, limit)
	for _, i := range order {
		h := res.Hits[i]
		h.Score = fused[i]
		h.Reason = h.Reason + " · neural-rerank"
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	res.Hits = out
	res.Total = len(out)
	return json.Marshal(res)
}

func cosineFloat(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func rankIndices(n int, key func(int) float64) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return key(idx[a]) > key(idx[b]) })
	return idx
}

func WorkspaceWiki(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunWiki(root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
