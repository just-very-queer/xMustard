package workspaceops

import (
	"fmt"
	"slices"
	"strings"
)

// Principal roles (PAR-SEC-02). A token carries a role spec: one role or several
// joined with "+" (for example "proposer+verifier"). Every role implies reader, and
// admin implies every role. The legacy names stay valid: "agent" is
// reader+proposer+verifier (what an agent token could always do) and "readonly" is
// reader.
const (
	RoleReader        = "reader"         // read tools and routes
	RoleProposer      = "proposer"       // remember, edit own memory, platform writes
	RoleVerifier      = "verifier"       // verify a peer's proposed memory
	RoleHumanApprover = "human-approver" // policy changes and approvals
	RoleIndexer       = "indexer"        // rebaseline the index (POST /index)
	RoleAdmin         = "admin"          // operator: tokens, settings, every other role

	roleAgent    = "agent"    // legacy alias: reader+proposer+verifier
	roleReadonly = "readonly" // legacy alias: reader
)

// roleOrder is the canonical order used for role lists and specs.
var roleOrder = []string{RoleAdmin, RoleHumanApprover, RoleIndexer, RoleVerifier, RoleProposer, RoleReader}

// roleGrants maps each role name a spec may use to the roles it holds.
var roleGrants = map[string][]string{
	RoleReader:        {RoleReader},
	RoleProposer:      {RoleReader, RoleProposer},
	RoleVerifier:      {RoleReader, RoleVerifier},
	RoleHumanApprover: {RoleReader, RoleHumanApprover},
	RoleIndexer:       {RoleReader, RoleIndexer},
	RoleAdmin:         roleOrder,
	roleAgent:         {RoleReader, RoleProposer, RoleVerifier},
	roleReadonly:      {RoleReader},
}

// Roles lists every role in canonical order.
func Roles() []string { return slices.Clone(roleOrder) }

// ParseRoleSpec validates a role spec ("agent", "proposer+verifier", ...) and returns
// it deduplicated in a stable order. Blank means the historical default, "agent".
func ParseRoleSpec(spec string) (string, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" {
		return roleAgent, nil
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, "+") {
		part = strings.TrimSpace(part)
		if _, ok := roleGrants[part]; !ok {
			return "", fmt.Errorf("unknown role %q: use %s, or the legacy agent|readonly, joined with +", part, strings.Join(roleOrder, "|"))
		}
		seen[part] = true
	}
	if seen[RoleAdmin] {
		return RoleAdmin, nil // admin already holds every role
	}
	out := make([]string, 0, len(seen))
	for _, name := range append(slices.Clone(roleOrder), roleAgent, roleReadonly) {
		if seen[name] {
			out = append(out, name)
		}
	}
	return strings.Join(out, "+"), nil
}

// ExpandRoles returns the roles a spec holds, in canonical order. An unknown spec
// holds only reader (the least privilege), so a hand-edited or misspelled role can
// never widen access.
func ExpandRoles(spec string) []string {
	canon, err := ParseRoleSpec(spec)
	if err != nil {
		return []string{RoleReader}
	}
	held := map[string]bool{}
	for _, part := range strings.Split(canon, "+") {
		for _, r := range roleGrants[part] {
			held[r] = true
		}
	}
	out := make([]string, 0, len(held))
	for _, r := range roleOrder {
		if held[r] {
			out = append(out, r)
		}
	}
	return out
}

// GateRole maps a role a gate asks for to the role that satisfies it: the legacy
// "agent" gate means proposer and "readonly" means reader.
func GateRole(role string) string {
	switch role {
	case roleAgent:
		return RoleProposer
	case roleReadonly, "":
		return RoleReader
	}
	return role
}

// RoleSet returns the roles the principal holds. Roles is filled when a token
// resolves; a Principal built without it derives the set from Role.
func (p *Principal) RoleSet() []string {
	if p == nil {
		return nil
	}
	if len(p.Roles) > 0 {
		return p.Roles
	}
	return ExpandRoles(fallbackString(p.Role, roleAgent))
}

// Has reports whether the principal holds role (admin holds every role).
func (p *Principal) Has(role string) bool {
	role = GateRole(role)
	for _, r := range p.RoleSet() {
		if r == role || r == RoleAdmin {
			return true
		}
	}
	return false
}

// ReadOnly reports whether the principal holds nothing beyond reader, so it may only
// use GET routes.
func (p *Principal) ReadOnly() bool {
	for _, r := range p.RoleSet() {
		if r != RoleReader {
			return false
		}
	}
	return true
}
