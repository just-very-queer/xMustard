package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

// Knowledge layer delivery: hybrid search + wiki generation over the repo.

// searchWindow is the deepest hit a search page may reach (the core's MAX_WINDOW): a
// cursor past it is rejected, and a page stops at it.
const searchWindow = 200

// maxSearchCursorLen bounds a cursor before it is decoded.
const maxSearchCursorLen = 64

// SearchRequest is one hybrid search call.
type SearchRequest struct {
	Query string
	// Seed anchors the graph-proximity lane.
	Seed string
	// PathGlob keeps only matching paths (gitignore semantics).
	PathGlob string
	// Cursor continues a previous page (next_cursor).
	Cursor string
	Limit  int
}

// fingerprint names the ranking a cursor pages: every argument that orders or filters
// it. The page size may change between pages.
func (r SearchRequest) fingerprint() string {
	b, _ := json.Marshal([]string{r.Query, r.Seed, r.PathGlob})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// Search cursors follow the recall cursor rule (WS-20): base64url of
// "<kind>1.<offset>.<fingerprint>", bound to the ranking it pages; this one is kind s.
func encodeSearchCursor(offset int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("s1." + strconv.Itoa(offset) + "." + fp))
}

// decodeSearchCursor returns the offset a cursor continues from; "" starts at 0. A
// cursor that is malformed, over-long, past the search window or from another query is
// rejected, never clamped.
func decodeSearchCursor(c, fp string) (int, error) {
	if c == "" {
		return 0, nil
	}
	invalid := fmt.Errorf("cursor is not a next_cursor of this search: %w", ErrInvalidInput)
	if len(c) > maxSearchCursorLen {
		return 0, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	parts := strings.Split(string(raw), ".")
	if err != nil || len(parts) != 3 || parts[0] != "s1" || parts[2] != fp {
		return 0, invalid
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 || n >= searchWindow {
		return 0, invalid
	}
	return n, nil
}

// WorkspaceSearch runs the in-process hybrid search. `seed`, when non-empty, is an
// anchor symbol that activates the graph-proximity RRF lane (symbols structurally
// near the seed are pulled up); empty seed keeps the default fusion (with
// auto-seeding from an exact query→symbol match inside the core).
func WorkspaceSearch(dataDir, workspaceID, query string, limit int, seed string) (json.RawMessage, error) {
	return WorkspaceSearchCtx(context.Background(), dataDir, workspaceID, query, limit, seed)
}

// WorkspaceSearchCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func WorkspaceSearchCtx(ctx context.Context, dataDir, workspaceID, query string, limit int, seed string) (json.RawMessage, error) {
	return workspaceSearch(ctx, dataDir, workspaceID, SearchRequest{Query: query, Seed: seed, Limit: limit})
}

// workspaceSearch runs one page of the core search and adds its cursor.
func workspaceSearch(ctx context.Context, dataDir, workspaceID string, req SearchRequest) (json.RawMessage, error) {
	fp := req.fingerprint()
	offset, err := decodeSearchCursor(req.Cursor, fp)
	if err != nil {
		return nil, err
	}
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 25
	}
	limit = min(limit, searchWindow-offset) // a page stops at the window
	read := ensureCodeIndex(ctx, root)
	coreArgs := append(read.flags(), root, workspaceID, req.Query, strconv.Itoa(limit))
	if req.Seed != "" {
		// the seed is the CLI's optional 5th positional arg, after limit.
		coreArgs = append(coreArgs, req.Seed)
	}
	if offset > 0 {
		coreArgs = append(coreArgs, "--offset="+strconv.Itoa(offset))
	}
	if req.PathGlob != "" {
		coreArgs = append(coreArgs, "--path-glob="+req.PathGlob)
	}
	out, err := rustcore.RunSearch(ctx, coreArgs...)
	if err != nil {
		return nil, err
	}
	return withSearchCursor(read.annotate(out), offset, fp), nil
}

// withSearchCursor adds next_cursor when ranked hits remain inside the window.
func withSearchCursor(raw json.RawMessage, offset int, fp string) json.RawMessage {
	var page struct {
		Total int               `json:"total"`
		Hits  []json.RawMessage `json:"hits"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return raw
	}
	next := offset + len(page.Hits)
	if len(page.Hits) == 0 || next >= page.Total || next >= searchWindow {
		return raw
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil {
		return raw
	}
	res["next_cursor"], _ = json.Marshal(encodeSearchCursor(next, fp))
	b, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return b
}

// WorkspaceSearchWithFeedback runs the live search, fuses the agent-feedback boost
// into the ranking, and records that the returned paths were retrieved (closing the
// bidirectional loop). Falls back to the raw result if it can't parse.
func WorkspaceSearchWithFeedback(dataDir, workspaceID, query, seed string, limit int) (json.RawMessage, error) {
	return WorkspaceSearchWithFeedbackCtx(context.Background(), dataDir, workspaceID, query, seed, limit)
}

// WorkspaceSearchWithFeedbackCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func WorkspaceSearchWithFeedbackCtx(ctx context.Context, dataDir, workspaceID, query, seed string, limit int) (json.RawMessage, error) {
	return WorkspaceSearchPage(ctx, dataDir, workspaceID, SearchRequest{Query: query, Seed: seed, Limit: limit})
}

// WorkspaceSearchPage is the full search call: one page of the ranking with the
// feedback boost fused in (within the page, so pages stay disjoint) and next_cursor.
func WorkspaceSearchPage(ctx context.Context, dataDir, workspaceID string, req SearchRequest) (json.RawMessage, error) {
	raw, err := workspaceSearch(ctx, dataDir, workspaceID, req)
	if err != nil {
		return nil, err
	}
	return fuseSearchFeedback(dataDir, workspaceID, raw), nil
}

// fuseSearchFeedback re-ranks a raw search result with the feedback boost and
// buffers a retrieval signal for the top hits. Recording only enqueues in memory
// (feedback_recorder.go), so the search never waits on the feedback store. Returns
// raw unchanged if it can't parse or has no hits.
func fuseSearchFeedback(dataDir, workspaceID string, raw json.RawMessage) json.RawMessage {
	var res searchResult
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Hits) == 0 {
		return raw
	}
	res.Hits = applyFeedbackToHits(dataDir, workspaceID, res.Hits)
	// record retrieval for the top results (the slice the agent actually sees).
	top := res.Hits
	if len(top) > 8 {
		top = top[:8]
	}
	paths := make([]string, 0, len(top))
	for _, h := range top {
		paths = append(paths, h.Path)
	}
	feedbackRec.enqueue(dataDir, workspaceID, "retrieval", paths)
	out, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return out
}

// searchHit is one core search hit. Fields Go does not read travel as raw JSON, so
// re-encoding a result never drops what the core reported.
type searchHit struct {
	Kind         string             `json:"kind"`
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	Line         *int               `json:"line,omitempty"`
	Lines        json.RawMessage    `json:"lines,omitempty"`
	UID          string             `json:"uid,omitempty"`
	Score        float64            `json:"score"`
	Scores       map[string]float64 `json:"scores,omitempty"`
	LanesMatched []string           `json:"lanes_matched,omitempty"`
	Reasons      []string           `json:"reasons,omitempty"`
	Reason       string             `json:"reason"`
	Snippet      json.RawMessage    `json:"snippet,omitempty"`
}

type searchResult struct {
	WorkspaceID  string          `json:"workspace_id"`
	Query        string          `json:"query"`
	Total        int             `json:"total"`
	Offset       int             `json:"offset"`
	Omitted      int             `json:"omitted"`
	NextCursor   string          `json:"next_cursor,omitempty"`
	Hits         []searchHit     `json:"hits"`
	Degradations json.RawMessage `json:"degradations,omitempty"`
	Coverage     json.RawMessage `json:"coverage,omitempty"`  // pass index coverage through to the agent
	Freshness    json.RawMessage `json:"freshness,omitempty"` // and the graph's freshness envelope
	GeneratedAt  string          `json:"generated_at"`
}

// WorkspaceSearchReranked runs the live hybrid search, then fuses a NEURAL lane:
// it embeds the query and each hit (via an OpenAI-compatible provider's /embeddings
// endpoint) and re-ranks with Reciprocal Rank Fusion of the lexical/structural rank
// and the embedding-cosine rank. This is the opt-in neural upgrade to the model-free
// in-process search; default search stays fast and provider-free.
func WorkspaceSearchReranked(dataDir, workspaceID, query, provider, model string, limit int) (json.RawMessage, error) {
	raw, err := WorkspaceSearch(dataDir, workspaceID, query, limit*2, "")
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
	out, err := rustcore.RunWiki(context.Background(), root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
