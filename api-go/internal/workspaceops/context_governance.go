package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Context governance: the trust layer for the MCP context engine. Agents don't
// write straight into shared context — they PROPOSE an entry, which other agents
// VERIFY. An entry is promoted into the active (shared) context only once enough
// distinct agents have approved it. Entries carry a permission: "readonly" entries
// can never be mutated after promotion (only superseded by a new proposal). A
// per-workspace/global toggle decides whether multi-agent verification is required
// at all — "run it through multiple agents, or not".

type ContextVerification struct {
	Agent   string `json:"agent"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
	At      string `json:"at"`
}

type ContextEntry struct {
	ID                    string                `json:"id"`
	WorkspaceID           string                `json:"workspace_id"`
	Title                 string                `json:"title"`
	Content               string                `json:"content"`
	Source                string                `json:"source"`     // proposing agent/actor
	Permission            string                `json:"permission"` // readonly | readwrite
	Status                string                `json:"status"`     // pending | verified | rejected
	Promoted              bool                  `json:"promoted"`   // visible in active shared context
	Verifications         []ContextVerification `json:"verifications"`
	RequiredVerifications int                   `json:"required_verifications"`
	CreatedAt             string                `json:"created_at"`
	UpdatedAt             string                `json:"updated_at"`
}

type ProposeContextRequest struct {
	Title      string `json:"title"`
	Content    string `json:"content"`
	Source     string `json:"source"`
	Permission string `json:"permission"`
	// RequireVerification overrides the workspace default: when explicitly false,
	// the entry is promoted immediately (single-agent mode); when true, it needs
	// the multi-agent threshold. nil → use the workspace/global setting.
	RequireVerification *bool `json:"require_verification,omitempty"`
}

// safeIDPattern rejects anything that could escape the data dir or be a path
// traversal: only alphanumerics, dash, underscore, dot (with ".." rejected).
var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateSafeID(kind, id string) error {
	if id == "" || strings.Contains(id, "..") || strings.ContainsRune(id, 0) || !safeIDPattern.MatchString(id) {
		return fmt.Errorf("invalid %s id", kind)
	}
	return nil
}

func contextEntriesPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "context_entries.json")
}

func loadContextEntries(dataDir, workspaceID string) ([]ContextEntry, error) {
	var entries []ContextEntry
	if err := readJSON(contextEntriesPath(dataDir, workspaceID), &entries); err != nil {
		if os.IsNotExist(err) {
			return []ContextEntry{}, nil
		}
		return nil, err
	}
	return entries, nil
}

func saveContextEntries(dataDir, workspaceID string, entries []ContextEntry) error {
	return writeJSON(contextEntriesPath(dataDir, workspaceID), entries)
}

// contextDefaults returns (requireMultiAgent, threshold) from settings.
func contextDefaults(dataDir string) (bool, int) {
	settings, err := loadSettings(dataDir)
	if err != nil {
		return true, 2
	}
	require := true
	if settings.RequireMultiAgentVerification != nil {
		require = *settings.RequireMultiAgentVerification
	}
	threshold := settings.ContextVerificationThreshold
	if threshold <= 0 {
		threshold = 2
	}
	return require, threshold
}

// distinctApprovals counts unique agents that approved (latest verdict per agent).
func distinctApprovals(verifications []ContextVerification) (approvals, rejections int) {
	latest := map[string]bool{}
	for _, v := range verifications {
		agent := strings.TrimSpace(v.Agent)
		if agent == "" {
			continue
		}
		latest[agent] = v.Approve
	}
	for _, approve := range latest {
		if approve {
			approvals++
		} else {
			rejections++
		}
	}
	return approvals, rejections
}

// promote/demote an entry based on its verifications vs its threshold.
func reconcileEntry(entry *ContextEntry) {
	approvals, rejections := distinctApprovals(entry.Verifications)
	switch {
	case rejections >= entry.RequiredVerifications && rejections > 0:
		entry.Status = "rejected"
		entry.Promoted = false
	case approvals >= entry.RequiredVerifications:
		entry.Status = "verified"
		entry.Promoted = true
	default:
		entry.Status = "pending"
		entry.Promoted = false
	}
}

// ProposeContext creates a pending context entry. In single-agent mode (multi-agent
// verification not required) it is promoted immediately.
func ProposeContext(dataDir, workspaceID string, req ProposeContextRequest) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	permission := strings.ToLower(strings.TrimSpace(req.Permission))
	if permission != "readwrite" {
		permission = "readonly" // default to the safest permission
	}
	requireMulti, threshold := contextDefaults(dataDir)
	// A per-request override may only TIGHTEN the gate (force multi-agent ON),
	// never loosen it. Otherwise an untrusted proposer could pass
	// require_verification:false to self-promote and poison the shared context
	// that gets injected into every agent run. Single-agent mode is an operator
	// SETTING (require_multi_agent_verification), not a per-request choice.
	if req.RequireVerification != nil && *req.RequireVerification {
		requireMulti = true
	}
	required := threshold
	if !requireMulti {
		required = 1
	}

	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	entry := ContextEntry{
		ID:                    "ctx_" + hashID(workspaceID, req.Title, req.Content+now)[:12],
		WorkspaceID:           workspaceID,
		Title:                 strings.TrimSpace(req.Title),
		Content:               req.Content,
		Source:                fallbackString(strings.TrimSpace(req.Source), "unknown"),
		Permission:            permission,
		Verifications:         []ContextVerification{},
		RequiredVerifications: required,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if !requireMulti {
		// single-agent mode: the proposer's own assertion promotes it.
		entry.Verifications = append(entry.Verifications, ContextVerification{
			Agent: entry.Source, Approve: true, Note: "single-agent mode", At: now,
		})
	}
	reconcileEntry(&entry)
	entries = append(entries, entry)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return &entry, nil
}

// VerifyContext records an agent's verdict on an entry and re-promotes if the
// approval threshold is now met. Distinct agents only — a single agent cannot
// satisfy a multi-agent gate by voting twice.
func VerifyContext(dataDir, workspaceID, entryID, agent string, approve bool, note string) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == entryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, os.ErrNotExist
	}
	entry := &entries[idx]
	now := nowUTC()
	// replace this agent's prior verdict if it exists (distinct-agent semantics)
	replaced := false
	for i := range entry.Verifications {
		if strings.EqualFold(entry.Verifications[i].Agent, agent) {
			entry.Verifications[i] = ContextVerification{Agent: agent, Approve: approve, Note: note, At: now}
			replaced = true
			break
		}
	}
	if !replaced {
		entry.Verifications = append(entry.Verifications, ContextVerification{Agent: agent, Approve: approve, Note: note, At: now})
	}
	entry.UpdatedAt = now
	reconcileEntry(entry)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return entry, nil
}

// UpdateContextContent amends an entry's content. Readonly entries that are
// already verified/promoted reject edits — they can only be superseded by a new
// proposal (this is the "readonly" permission guarantee).
func UpdateContextContent(dataDir, workspaceID, entryID, content string) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == entryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, os.ErrNotExist
	}
	entry := &entries[idx]
	if entry.Permission == "readonly" && (entry.Promoted || entry.Status == "verified") {
		return nil, fmt.Errorf("entry %s is readonly and verified; propose a new entry to supersede it", entryID)
	}
	entry.Content = content
	entry.UpdatedAt = nowUTC()
	// a content change resets verification — re-approval is required
	entry.Verifications = []ContextVerification{}
	reconcileEntry(entry)
	if err := saveContextEntries(dataDir, workspaceID, entries); err != nil {
		return nil, err
	}
	return entry, nil
}

// ListContextEntries returns entries filtered by status: "" / "all", "pending",
// "promoted"/"active", "rejected".
func ListContextEntries(dataDir, workspaceID, filter string) ([]ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	entries, err := loadContextEntries(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	out := make([]ContextEntry, 0, len(entries))
	for _, e := range entries {
		switch filter {
		case "", "all":
			out = append(out, e)
		case "pending":
			if e.Status == "pending" {
				out = append(out, e)
			}
		case "promoted", "active", "verified":
			if e.Promoted {
				out = append(out, e)
			}
		case "rejected":
			if e.Status == "rejected" {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// GetActiveContext returns only promoted (verified) entries — the read-only view
// that actually enters an agent's working context.
func GetActiveContext(dataDir, workspaceID string) (map[string]any, error) {
	promoted, err := ListContextEntries(dataDir, workspaceID, "promoted")
	if err != nil {
		return nil, err
	}
	requireMulti, threshold := contextDefaults(dataDir)
	return map[string]any{
		"workspace_id":             workspaceID,
		"require_multi_agent":      requireMulti,
		"verification_threshold":   threshold,
		"active_count":             len(promoted),
		"entries":                  promoted,
		"generated_at":             nowUTC(),
	}, nil
}
