package workspaceops

import (
	"context"
	"encoding/json"
	"path/filepath"

	"xmustard/api-go/internal/rustcore"
)

// RepoIdentity is a repository identity observation used to label captured evidence
// current, stale or unknown. Contract of the Rust `repo-key` command:
//
//	xmustard-core repo-key <root> -> {"key","head","parser_version","identity_complete","limitations":[]}
//
// Complete is true only when the key covers the exact working state (HEAD, dirty and
// untracked content). Anything else — a failed command, bounded/oversized reads, the
// fingerprint fallback — is incomplete and can never make evidence look current.
type RepoIdentity struct {
	Key           string               `json:"key"`
	Head          string               `json:"head,omitempty"`
	ParserVersion string               `json:"parser_version,omitempty"`
	Complete      bool                 `json:"identity_complete"`
	Limitations   []IdentityLimitation `json:"limitations,omitempty"`
	RepoMode      string               `json:"repo_mode,omitempty"`
	Root          string               `json:"root,omitempty"`
	Source        string               `json:"source"` // repo-key | fingerprint-fallback | unavailable
}

// IdentityLimitation is the Rust wire type (rust-core indexcache.rs IdentityLimitation):
// why an identity is incomplete, e.g. reason oversized | unreadable | not_regular |
// identity_budget | git_unavailable | git_status_failed.
type IdentityLimitation struct {
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// String renders a limitation for delivery metadata.
func (l IdentityLimitation) String() string {
	s := l.Reason
	if l.Path != "" {
		s += " " + l.Path
	}
	if l.Detail != "" {
		s += ": " + l.Detail
	}
	return s
}

// WorkspaceRepoIdentity returns the identity of a workspace's repository and its
// canonical root (the evidence trust scope).
func WorkspaceRepoIdentity(ctx context.Context, dataDir, workspaceID string) (RepoIdentity, string) {
	root := WorkspaceRepoScope(dataDir, workspaceID)
	if root == "" {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "workspace_root_unavailable"}}}, ""
	}
	return repoIdentity(ctx, root), root
}

// WorkspaceRepoScope returns a workspace's canonical repository root (the evidence
// trust scope) without sampling its identity, or "" when unavailable.
func WorkspaceRepoScope(dataDir, workspaceID string) string {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil || root == "" {
		return ""
	}
	if canon, err := filepath.EvalSymlinks(root); err == nil {
		root = canon
	}
	return root
}

// repoIdentity asks the Rust core for the repository identity. There is no fallback:
// if `repo-key` fails or answers something undecodable, the identity is unavailable
// and incomplete, so evidence freshness is "unknown" — never inferred from a weaker key.
func repoIdentity(ctx context.Context, root string) RepoIdentity {
	out, err := rustcore.RunRepoKey(ctx, root)
	if err != nil {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_unavailable", Detail: err.Error()}}}
	}
	var id RepoIdentity
	if json.Unmarshal(out, &id) != nil || id.Key == "" {
		return RepoIdentity{Source: "unavailable", Limitations: []IdentityLimitation{{Reason: "repo_key_undecodable"}}}
	}
	id.Source = "repo-key"
	if len(id.Limitations) > 0 {
		id.Complete = false // the contract: any limitation makes the identity incomplete
	}
	return id
}
