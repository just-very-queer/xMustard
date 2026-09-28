package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// impact(path=) over HTTP (WS-FIX-05): the route confines the path to the workspace,
// hands the core the cleaned repo-relative path and the depth, and refuses an escaping
// path or a path mixed with another impact mode instead of letting one mode win.
func TestImpactPathRouteConfinesThePathAndRefusesMixedModes(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedCoreWorkspace(t, dir, "wsImpact")
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `case "$1 $2" in
"symbolgraph impact-file") echo "$*" > "`+argv+`"; printf '{"path":"pkg/a.go","found":true,"impacted":[],"impacted_count":0,"max_depth":2}' ;;
*) echo '{}' ;;
esac
`))
	base := srv.URL + "/api/workspaces/wsImpact/changes/since-index"
	code, out := call(t, "GET", base+"?path=./pkg/a.go&depth=2", "", "", nil)
	if code != http.StatusOK || out["found"] != true {
		t.Fatalf("impact path: %d %v", code, out)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	// symbolgraph impact-file [--identity-key=K] <root> <workspace_id> <path> <max_depth>
	f := strings.Fields(string(raw))
	if n := len(f); n < 6 || f[1] != "impact-file" || f[n-3] != "wsImpact" || f[n-2] != "pkg/a.go" || f[n-1] != "2" {
		t.Fatalf("core argv %q", raw)
	}
	for _, q := range []string{"?path=../outside.go", "?path=%2Fetc%2Fpasswd", "?path=a.go&symbol=Add", "?path=a.go&from=A&to=B", "?path=a.go&to=B"} {
		if code, out := call(t, "GET", base+q, "", "", nil); code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %v", q, code, out)
		}
	}
}
