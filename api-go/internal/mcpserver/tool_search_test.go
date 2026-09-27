package mcpserver

import (
	"strings"
	"testing"
)

// search's paging and path filter (WS-18) travel as query parameters; a cursor over
// its length cap is rejected before any call.
func TestSearchForwardsCursorAndPathGlob(t *testing.T) {
	api := &fakeAPI{}
	s := newSession(t, api, Options{}, nil, LatestProtocolVersion)
	res, rerr := call(t, s, "search", map[string]any{"workspace_id": "ws", "q": "retry", "cursor": "czEuMy5hYmM", "path_glob": "**/*.go"})
	if rerr != nil || res["isError"] == true {
		t.Fatalf("search refused: %v %v", rerr, res)
	}
	if p := api.lastTool(t).Path; p != "/api/workspaces/ws/search?q=retry&cursor=czEuMy5hYmM&path_glob=%2A%2A%2F%2A.go" {
		t.Fatalf("search forwarded %q", p)
	}
	_, rerr = call(t, s, "search", map[string]any{"workspace_id": "ws", "q": "retry", "cursor": strings.Repeat("A", 65)})
	if rerr == nil || rerr.Code != CodeInvalidParams || !strings.Contains(rerr.Message, "at most 64") {
		t.Fatalf("over-long cursor: %v", rerr)
	}
}
