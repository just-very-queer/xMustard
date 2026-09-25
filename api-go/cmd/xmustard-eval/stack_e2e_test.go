package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestRealStackDryRun drives the real xmustard-api and xmustard-mcp (built from this
// checkout) with the fake agent: seeding goes through propose/verify with distinct
// principals, the agent phase runs the CORE_ONLY surface, the stdio shim is launched
// by the client, and RSS is attributed to the xMustard tree. It needs the Rust core,
// so it is opt-in:
//
//	XMUSTARD_EVAL_REAL_STACK=1 XMUSTARD_CORE_BIN=.../xmustard-core go test -run RealStack ./cmd/xmustard-eval/
func TestRealStackDryRun(t *testing.T) {
	core := os.Getenv("XMUSTARD_CORE_BIN")
	if os.Getenv("XMUSTARD_EVAL_REAL_STACK") != "1" || core == "" {
		t.Skip("set XMUSTARD_EVAL_REAL_STACK=1 and XMUSTARD_CORE_BIN to run against the real stack")
	}
	bin := t.TempDir()
	for _, name := range []string{"xmustard-api", "xmustard-mcp"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, name), "./cmd/"+name)
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
	c := loadTestCorpus(t, "../../../eval/tasks/seed.yaml")
	for _, drv := range driverNames {
		t.Run(drv, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			cfg := prepared(t, &RunConfig{Driver: "fake:" + drv, Arms: []string{ArmXmustardMCP, ArmXmustardMemory}, Tasks: []string{"service-agreed-port"},
				Stack: StackConfig{Kind: StackReal, APIBin: filepath.Join(bin, "xmustard-api"), MCPBin: filepath.Join(bin, "xmustard-mcp"), CoreBin: core}}, c, out)
			if _, err := Execute(context.Background(), cfg, c); err != nil {
				t.Fatal(err)
			}
			for _, r := range readRecords(t, out) {
				if r.Status != StatusCompleted || !r.Resolved {
					t.Fatalf("%s: %s %q", r.Arm, r.Status, r.Reason)
				}
				if r.Stack == nil || r.Stack.Kind != StackReal || !r.Stack.CoreOnly || r.Stack.WorkspaceID == "" {
					t.Fatalf("%s: stack %+v", r.Arm, r.Stack)
				}
				if len(r.Transcript.XmResults) != 2 {
					t.Fatalf("%s: xMustard results %+v", r.Arm, r.Transcript.XmResults)
				}
				for _, x := range r.Transcript.XmResults {
					if x.IsError {
						t.Fatalf("%s: %s returned an error", r.Arm, x.Tool)
					}
				}
				rss := r.RSS
				wantRoles := []string{"xmustard-api"}
				if drv != "pi" { // pi reaches the API directly, like the Pi adapter
					wantRoles = append(wantRoles, "xmustard-mcp")
				}
				for _, role := range wantRoles {
					if !slices.Contains(rss.XmRolesObserved, role) {
						t.Fatalf("%s: role %s not sampled: %+v", r.Arm, role, rss)
					}
				}
				if rss.XmWithinGate == nil || !*rss.XmWithinGate {
					t.Fatalf("%s: xMustard tree over the gate: %+v", r.Arm, rss)
				}
				t.Logf("%s/%s: xMustard peak %d KiB (%s) roles %+v; agent peak %d KiB", drv, r.Arm, rss.XmPeakKiB, rss.XmPeakPhase, rss.XmPeakRoles, rss.AgentPeakKiB)
				if r.Arm != ArmXmustardMemory {
					continue
				}
				for _, s := range r.Stack.Seeds {
					wantMode := "peer_verified"
					if s.Label == LabelPending {
						wantMode = ""
					}
					if s.VerificationMode != wantMode {
						t.Fatalf("seed %s: mode %q", s.Key, s.VerificationMode)
					}
				}
				m := r.Memory
				if m == nil || m.ScopeLeakage != 0 || m.PendingServed != 0 || m.PendingAsPeerVerified != 0 || m.PromotionErrors == nil || *m.PromotionErrors != 0 {
					t.Fatalf("governance invariants: %+v", m)
				}
				if m.CurrentFactRecall == nil || *m.CurrentFactRecall != 1 {
					t.Fatalf("the current fact was not delivered: %+v", m)
				}
				t.Logf("memory metrics: served %v stale-flagged %d superseded-rate %v contradiction P/R %v/%v est-tokens/recall %v",
					m.Served, m.StaleServedFlagged, deref(m.SupersededServedRate), deref(m.ContradictionPrecision), deref(m.ContradictionRecall), deref(m.TokensPerRecall))
			}
		})
	}
}

func deref(p *float64) any {
	if p == nil {
		return "n/a"
	}
	return *p
}
