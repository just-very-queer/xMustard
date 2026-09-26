package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Arms (PAR-EVAL-01). Each arm changes one thing relative to its neighbour, so a
// paired difference is attributable to that change:
//
//	baseline            the client exactly as the operator configured it
//	baseline_nomcp      the same client with every MCP server (or extension) disabled
//	xmustard_mcp        baseline_nomcp plus xMustard's nine tools, empty memory
//	xmustard_mcp_hooks  xmustard_mcp plus client hooks; runs only when the run config
//	                    supplies hook arguments for the driver (placeholder until the
//	                    WS-23 hook adapters exist)
//	xmustard_memory     xmustard_mcp plus the task's seeded, peer-verified memories
//	peer:<name>         baseline_nomcp plus one separately installed peer MCP server
const (
	ArmBaseline         = "baseline"
	ArmBaselineNoMCP    = "baseline_nomcp"
	ArmXmustardMCP      = "xmustard_mcp"
	ArmXmustardMCPHooks = "xmustard_mcp_hooks"
	ArmXmustardMemory   = "xmustard_memory"
	peerArmPrefix       = "peer:"
)

// DefaultArms is the arm set a run uses when none is configured.
var DefaultArms = []string{ArmBaseline, ArmBaselineNoMCP, ArmXmustardMCP, ArmXmustardMCPHooks, ArmXmustardMemory}

// Arm describes how one arm configures the client and the xMustard stack.
type Arm struct {
	Name        string
	IsolateMCP  bool   // disable the operator's own MCP servers / extensions
	UsesStack   bool   // start an xMustard stack and give the client its tools
	SeedsMemory bool   // seed the task's memory fixture before the agent starts
	NeedsHooks  bool   // requires per-driver hook arguments from the run config
	Peer        string // peer MCP server name for peer:<name> arms
}

var peerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func parseArm(name string) (Arm, error) {
	switch name {
	case ArmBaseline:
		return Arm{Name: name}, nil
	case ArmBaselineNoMCP:
		return Arm{Name: name, IsolateMCP: true}, nil
	case ArmXmustardMCP:
		return Arm{Name: name, IsolateMCP: true, UsesStack: true}, nil
	case ArmXmustardMCPHooks:
		return Arm{Name: name, IsolateMCP: true, UsesStack: true, NeedsHooks: true}, nil
	case ArmXmustardMemory:
		return Arm{Name: name, IsolateMCP: true, UsesStack: true, SeedsMemory: true}, nil
	}
	if peer, ok := strings.CutPrefix(name, peerArmPrefix); ok {
		if !peerNamePattern.MatchString(peer) {
			return Arm{}, fmt.Errorf("peer arm %q: name must match %s", name, peerNamePattern)
		}
		return Arm{Name: name, IsolateMCP: true, Peer: peer}, nil
	}
	return Arm{}, fmt.Errorf("unknown arm %q (want one of %v or peer:<name>)", name, DefaultArms)
}

// PeerConfig is a separately installed comparator MCP server. Peers are never
// vendored: the operator installs them and names the command here.
type PeerConfig struct {
	Name    string            `yaml:"name" json:"name"`
	License string            `yaml:"license" json:"license"`
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"-"`
	// OwnerDecision must be set for a noncommercial or source-available peer (for
	// example GitNexus, PolyForm Noncommercial): running it needs an explicit owner
	// decision (§2.4).
	OwnerDecision string `yaml:"owner_decision" json:"owner_decision,omitempty"`
	// OwnerDecisionRule records which rule required the decision (set by prepare).
	OwnerDecisionRule string `yaml:"-" json:"owner_decision_rule,omitempty"`
}

// knownNoncommercialPeers are peers whose license needs an owner decision whatever
// the config's free-text license field says.
var knownNoncommercialPeers = []string{"gitnexus"}

var ncLicenseToken = regexp.MustCompile(`(^|[^a-z0-9])nc([^a-z0-9]|$)`)

// ownerDecisionRule returns why this peer needs an owner decision, or "".
func (p PeerConfig) ownerDecisionRule() string {
	lic := strings.ToLower(p.License)
	switch {
	case strings.Contains(lic, "noncommercial") || strings.Contains(lic, "non-commercial"):
		return "license names noncommercial terms"
	case strings.Contains(lic, "polyform"):
		return "PolyForm (source-available) license"
	case ncLicenseToken.MatchString(lic):
		return "license carries an NC (noncommercial) term"
	}
	for _, s := range append([]string{p.Name, p.Command}, p.Args...) {
		for _, n := range knownNoncommercialPeers {
			if strings.Contains(strings.ToLower(s), n) {
				return "known noncommercial peer " + n
			}
		}
	}
	return ""
}

// check validates the peer and returns the rule that requires an owner decision.
func (p PeerConfig) check() (string, error) {
	if !peerNamePattern.MatchString(p.Name) {
		return "", fmt.Errorf("peer %q: name must match %s", p.Name, peerNamePattern)
	}
	if p.Command == "" {
		return "", fmt.Errorf("peer %q: command is required", p.Name)
	}
	if strings.TrimSpace(p.License) == "" {
		return "", fmt.Errorf("peer %q: license is required (record the peer's license before running it)", p.Name)
	}
	rule := p.ownerDecisionRule()
	if rule != "" && strings.TrimSpace(p.OwnerDecision) == "" {
		return rule, fmt.Errorf("peer %q: %s (license %q); set owner_decision to record the owner's explicit approval", p.Name, rule, p.License)
	}
	return rule, nil
}

func (p PeerConfig) validate() error {
	_, err := p.check()
	return err
}

// MCPServer is one stdio MCP server handed to a client.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// armSkipReason reports why an arm cannot run a task, or "" when it can. A skipped
// run is recorded and named in the report; it never silently disappears.
func armSkipReason(arm Arm, t *Task, cfg *RunConfig) string {
	if len(t.Arms) > 0 && !slices.Contains(t.Arms, arm.Name) {
		return "task restricts arms to " + strings.Join(t.Arms, ",")
	}
	if arm.UsesStack && cfg.Stack.Kind == StackNone {
		return "no xMustard stack configured (stack.kind is none)"
	}
	if arm.NeedsHooks && len(cfg.Hooks[cfg.Driver]) == 0 {
		return fmt.Sprintf("placeholder: no client hook adapter configured for driver %s (hooks.%s in the run config; WS-23 builds the adapters)", cfg.Driver, cfg.Driver)
	}
	if arm.SeedsMemory && (t.Memory == nil || len(t.Memory.Seed) == 0) {
		return "task declares no memory fixture (memory.seed is empty)"
	}
	if arm.Peer != "" {
		if _, ok := cfg.peer(arm.Peer); !ok {
			return "peer " + arm.Peer + " is not configured"
		}
		if cfg.Driver == "pi" {
			return "pi has no MCP client; peer MCP arms need claude or codex"
		}
	}
	return ""
}
