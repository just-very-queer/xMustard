package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"xmustard/api-go/internal/budget"
)

// Search inside a retained original through resources/read (PAR-CTX-04):
// xmustard://evidence/{handle}?pattern=RE2|query=text|lines=A-B[&max_matches&context]
// answers with the matching lines (line numbers, byte offsets, context) instead of a
// byte page. The API enforces workspace, principal and expiry exactly as for pages.

// searchParams are the resource query parameters forwarded to the search route.
var searchParams = []string{"pattern", "query", "lines", "max_matches", "context", "offset", "start_line"}

// isSearch reports whether a resource query asks for search rather than a page.
func isSearch(q url.Values) bool {
	return q.Get("pattern") != "" || q.Get("query") != "" || q.Get("lines") != ""
}

func searchResource(ctx context.Context, uri, handle, ws string, q url.Values) (any, *rpcError) {
	fwd := url.Values{"handle": {handle}}
	for _, k := range searchParams {
		if v := q.Get(k); v != "" {
			fwd.Set(k, v)
		}
	}
	path := fmt.Sprintf("/api/workspaces/%s/evidence/search?%s", url.PathEscape(ws), fwd.Encode())
	resp, err := callAPIResp(ctx, "GET", path, "", nil)
	if err != nil {
		if strings.Contains(err.Error(), budget.ErrOverloaded.Error()) {
			return nil, overloadError(err)
		}
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if resp.status != http.StatusOK {
		return nil, evidenceRPCError(uri, resp)
	}
	var res struct {
		Matches         int    `json:"matches"`
		MatchCapReached bool   `json:"match_cap_reached"`
		NextOffset      int64  `json:"next_offset"`
		NextLine        int    `json:"next_line"`
		EOF             bool   `json:"eof"`
		Freshness       string `json:"freshness"`
	}
	if err := json.Unmarshal([]byte(resp.body), &res); err != nil {
		return nil, &rpcError{Code: -32603, Message: "undecodable evidence search result"}
	}
	return map[string]any{
		"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": resp.body}},
		"_meta": map[string]any{"xmustard/search": map[string]any{"matches": res.Matches, "match_cap_reached": res.MatchCapReached,
			"next_offset": res.NextOffset, "next_line": res.NextLine, "eof": res.EOF, "freshness": res.Freshness}},
	}, nil
}

// evidenceRPCError maps an evidence route refusal to a resources/read error: missing,
// expired, revoked and denied are resource-not-found (never substituted).
func evidenceRPCError(uri string, resp *apiResponse) *rpcError {
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(resp.body), &e)
	code := -32603
	switch resp.status {
	case http.StatusNotFound, http.StatusGone, http.StatusForbidden, http.StatusUnauthorized:
		code = resourceNotFound
	case http.StatusRequestedRangeNotSatisfiable, http.StatusBadRequest:
		code = -32602
	case http.StatusServiceUnavailable:
		code = overloadCode
	}
	return &rpcError{Code: code, Message: fmt.Sprintf("evidence %s: %s", e.Reason, e.Error),
		Data: map[string]any{"uri": uri, "status": resp.status, "reason": e.Reason}}
}
