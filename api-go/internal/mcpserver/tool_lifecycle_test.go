package mcpserver

import "testing"

// The WS-19A lifecycle arguments reach the API in the shapes its handlers decode:
// integers as JSON numbers, lists as arrays, fetch-by-id on the recall route.
func TestLifecycleArgumentsBuild(t *testing.T) {
	cases := []struct {
		tool       *Tool
		args       map[string]string
		path, body string
	}{
		{rememberTool, map[string]string{"workspace_id": "w", "op": "edit", "entry_id": "e1", "base_revision": "3",
			"reason": "moved", "old_string": "a", "new_string": "b"},
			"/api/workspaces/w/context",
			`{"base_revision":3,"entry_id":"e1","new_string":"b","old_string":"a","op":"edit","reason":"moved"}`},
		{rememberTool, map[string]string{"workspace_id": "w", "content": "c", "supersedes": "e1,e2", "expires": "2026-12-31"},
			"/api/workspaces/w/context", `{"content":"c","expires":"2026-12-31","supersedes":["e1","e2"]}`},
		{verifyTool, map[string]string{"workspace_id": "w", "entry_id": "e1", "outcome": "retract", "revision": "2"},
			"/api/workspaces/w/context/e1/verify?approve=true", `{"outcome":"retract","revision":2}`},
		{recallTool, map[string]string{"workspace_id": "w", "entry_id": "e1", "history": "true"},
			"/api/workspaces/w/context/active?entry_id=e1&history=true", ""},
	}
	for _, c := range cases {
		_, path, body := c.tool.Build(c.args)
		if path != c.path || body != c.body {
			t.Errorf("%s: got %s %s, want %s %s", c.tool.Name, path, body, c.path, c.body)
		}
	}
}
