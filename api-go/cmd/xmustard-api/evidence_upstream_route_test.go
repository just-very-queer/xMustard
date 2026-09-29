package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// WS-41: a capture that names the original the client persisted keeps a hashed
// reference instead of a copy, the class is hook, and a read answers the reference.
func TestCaptureRouteUpstreamReference(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newEvidenceFixture(t, true)
	tok, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	root := t.TempDir()
	t.Setenv("XMUSTARD_UPSTREAM_ROOTS", root)
	log := goTestLog(4000)
	file := filepath.Join(root, "result.txt")
	if err := os.WriteFile(file, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(log))
	q := url.Values{"format": {"raw"}, "tool": {"Bash"}, "command": {"go test"}, "upstream_path": {file},
		"upstream_sha256": {hex.EncodeToString(sum[:])}, "upstream_bytes": {strconv.Itoa(len(log))}}
	path := "/api/workspaces/" + f.ws + "/evidence/capture?"
	code, b, _ := f.do(t, "POST", path+q.Encode(), tok, strings.NewReader(log), nil)
	if code != http.StatusOK {
		t.Fatalf("capture: %d %s", code, b)
	}
	var res evidence.ObservationResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Handle == "" || res.RetentionClass != evidence.ClassHook || res.Upstream == nil || res.Upstream.Path != file {
		t.Fatalf("envelope: class %q upstream %+v handle %q", res.RetentionClass, res.Upstream, res.Handle)
	}
	code, b, _ = f.do(t, "GET", "/api/workspaces/"+f.ws+"/evidence/"+res.Handle, tok, nil, nil)
	if code != http.StatusConflict || !strings.Contains(string(b), "upstream_reference") {
		t.Fatalf("read of an upstream handle: %d %s", code, b)
	}
	// fail closed: each case runs with its own roots and must name its own reason
	other := filepath.Join(root, "other.txt")
	if err := os.WriteFile(other, []byte(log+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	osum := sha256.Sum256([]byte(log + "x"))
	for name, c := range map[string]struct {
		mutate func(url.Values)
		roots  string
		code   int
		reason string
	}{
		"wrong hash": {func(v url.Values) { v.Set("upstream_sha256", strings.Repeat("0", 64)) }, root, 400, "upstream_invalid"},
		"partial":    {func(v url.Values) { v.Del("upstream_bytes") }, root, 400, "upstream_invalid"},
		"outside":    {func(url.Values) {}, t.TempDir(), 400, "upstream_invalid"},
		"another file than the body": {func(v url.Values) {
			v.Set("upstream_path", other)
			v.Set("upstream_sha256", hex.EncodeToString(osum[:]))
			v.Set("upstream_bytes", strconv.Itoa(len(log)+1))
		}, root, 400, "upstream_invalid"},
		"secret path": {func(v url.Values) { v.Set("upstream_path", filepath.Join(root, ".ssh", "id_rsa")) }, root, 422, "secret_path"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XMUSTARD_UPSTREAM_ROOTS", c.roots)
			v := url.Values{}
			for k, x := range q {
				v[k] = x
			}
			c.mutate(v)
			code, b, _ := f.do(t, "POST", path+v.Encode(), tok, strings.NewReader(log), nil)
			if code != c.code || !strings.Contains(string(b), `"reason":"`+c.reason+`"`) {
				t.Errorf("%d %s, want %d %s", code, b, c.code, c.reason)
			}
		})
	}
}
