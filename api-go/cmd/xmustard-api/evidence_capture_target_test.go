package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

// A caller may lower its client's projection target for one capture (WS-24: the Pi
// adapter retains an older result before it masks or compacts it). Below the policy
// target nothing is reduced and nothing is retained; with target=1024 the same output
// is reduced, retained exactly and gets a handle. A larger target never raises the
// policy's, and out-of-range values are refused, not clamped.
func TestCaptureRouteLowerTarget(t *testing.T) {
	withCaptureRedactor(t, nil)
	f := newEvidenceFixture(t, true)
	alice, _ := workspaceops.MintToken(f.dir, "alice", "agent")
	var b strings.Builder
	for i := 0; b.Len() < 6<<10; i++ {
		fmt.Fprintf(&b, "line %04d of a pi bash output\n", i)
	}
	body := b.String()
	base := "/api/workspaces/" + f.ws + "/evidence/capture?client=pi&tool=bash&command=seq&call_id=c1"
	type result struct {
		evidence.Delivery
		TargetBytes int                   `json:"target_bytes"`
		Policy      evidence.ClientPolicy `json:"policy"`
		Shape       *evidence.ShapeResult `json:"shape"`
	}
	capture := func(q string) (int, result, string) {
		code, raw, _ := f.do(t, "POST", base+q, alice, strings.NewReader(body), nil)
		var res result
		_ = json.Unmarshal(raw, &res)
		return code, res, string(raw)
	}
	code, res, raw := capture("")
	if code != 200 || res.Reduced || res.Handle != "" || res.TargetBytes != evidence.PolicyFor("pi").Target || res.Shape.Mode != evidence.ShapeUnchanged {
		t.Fatalf("policy target: %d %s", code, raw[:min(len(raw), 300)])
	}
	code, res, raw = capture("&target=1024")
	if code != 200 || !res.Reduced || res.Handle == "" || res.TargetBytes != 1024 || res.RawBytes != int64(len(body)) {
		t.Fatalf("lowered target: %d %s", code, raw[:min(len(raw), 300)])
	}
	// the retained original is the exact body
	var got []byte
	for off := int64(0); ; {
		c, p, _ := f.do(t, "GET", fmt.Sprintf("/api/workspaces/%s/evidence/%s?offset=%d", f.ws, res.Handle, off), alice, nil, nil)
		var page evidence.Page
		if c != 200 || json.Unmarshal(p, &page) != nil {
			t.Fatalf("page: %d %s", c, p)
		}
		data, _ := base64.StdEncoding.DecodeString(page.Data)
		got = append(got, data...)
		if page.EOF {
			break
		}
		off = page.NextOffset
	}
	sum := sha256.Sum256([]byte(body))
	if string(got) != body || res.RawSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("retained original differs (%d of %d bytes)", len(got), len(body))
	}
	// a target above the policy's does not raise it
	if code, res, raw = capture("&target=65536"); code != 200 || res.TargetBytes != evidence.PolicyFor("pi").Target || res.Reduced {
		t.Fatalf("raised target: %d %s", code, raw[:min(len(raw), 300)])
	}
	for _, q := range []string{"&target=1023", "&target=0", "&target=-5", "&target=x", fmt.Sprintf("&target=%d", evidence.MaxCaptureTarget+1)} {
		if code, _, raw := capture(q); code != http.StatusBadRequest || !strings.Contains(raw, "invalid_target") {
			t.Fatalf("%s: want 400 invalid_target, got %d %s", q, code, raw)
		}
	}
}
