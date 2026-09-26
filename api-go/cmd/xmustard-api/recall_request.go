package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// recallRequest reads a ranked recall from GET context/active (WS-20). Lists are
// comma-separated; booleans are "true"; max_chars out of range is rejected by
// RecallWith, never clamped. The caller is the authenticated principal, never an
// argument.
func recallRequest(r *http.Request) (workspaceops.RecallRequest, error) {
	q := r.URL.Query()
	req := workspaceops.RecallRequest{
		Query: q.Get("query"), Paths: csvList(q, "paths"), Kinds: csvList(q, "kind"), Tags: csvList(q, "tags"),
		Topic: q.Get("topic"), PathPrefix: q.Get("path_prefix"), Since: q.Get("since"), Until: q.Get("until"),
		By: q.Get("by"), Status: q.Get("status"), Cursor: q.Get("cursor"), Render: q.Get("render"),
		SessionID: q.Get("session_id"), Caller: callerOf(r).ID,
	}
	for name, dst := range map[string]*bool{
		"explain": &req.Explain, "names_only": &req.NamesOnly, "include_pending": &req.IncludePending,
		"include_superseded": &req.IncludeSuperseded, "show_expired": &req.ShowExpired,
	} {
		*dst = q.Get(name) == "true"
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			req.Limit = min(n, maxRecallLimit) // clamp: recall stays bounded
		}
	}
	if v := q.Get("max_chars"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return req, fmt.Errorf("max_chars %q is not an integer: %w", v, workspaceops.ErrInvalidInput)
		}
		req.MaxChars = n
	}
	return req, nil
}

func csvList(q url.Values, name string) []string {
	var out []string
	for _, v := range q[name] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
