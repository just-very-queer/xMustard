package workspaceops

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dedicated, persistent agent-identity tracking: a durable registry of the
// agents (runtime/model identities) that operate in a workspace, with first/last
// seen and run counts. Unlike the derived ownership history, this is its own
// stored record that survives and can be annotated.

type AgentIdentity struct {
	AgentID     string `json:"agent_id"` // stable slug of runtime/model
	WorkspaceID string `json:"workspace_id"`
	Runtime     string `json:"runtime"`
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
	RunCount    int    `json:"run_count"`
	Notes       string `json:"notes,omitempty"`
}

func agentIdentitiesPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "agent_identities.json")
}

func agentSlug(runtime, model string) string {
	raw := strings.ToLower(strings.TrimSpace(runtime + "-" + model))
	var b strings.Builder
	prevDash := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func loadAgentIdentities(dataDir, workspaceID string) ([]AgentIdentity, error) {
	var agents []AgentIdentity
	if err := readJSON(agentIdentitiesPath(dataDir, workspaceID), &agents); err != nil {
		if os.IsNotExist(err) {
			return []AgentIdentity{}, nil
		}
		return nil, err
	}
	return agents, nil
}

func saveAgentIdentities(dataDir, workspaceID string, agents []AgentIdentity) error {
	return writeJSON(agentIdentitiesPath(dataDir, workspaceID), agents)
}

// upsertAgentIdentity merges a sighting into the registry (pure).
func upsertAgentIdentity(agents []AgentIdentity, workspaceID, runtime, model, at string) ([]AgentIdentity, AgentIdentity) {
	id := agentSlug(runtime, model)
	for i := range agents {
		if agents[i].AgentID == id {
			agents[i].RunCount++
			if at != "" && (agents[i].LastSeenAt == "" || at > agents[i].LastSeenAt) {
				agents[i].LastSeenAt = at
			}
			if at != "" && (agents[i].FirstSeenAt == "" || at < agents[i].FirstSeenAt) {
				agents[i].FirstSeenAt = at
			}
			return agents, agents[i]
		}
	}
	agent := AgentIdentity{
		AgentID:     id,
		WorkspaceID: workspaceID,
		Runtime:     runtime,
		Model:       model,
		DisplayName: strings.TrimSpace(runtime + " / " + model),
		FirstSeenAt: at,
		LastSeenAt:  at,
		RunCount:    1,
	}
	return append(agents, agent), agent
}

// ListAgentIdentities returns the stored agent registry.
func ListAgentIdentities(dataDir, workspaceID string) ([]AgentIdentity, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	agents, err := loadAgentIdentities(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].RunCount > agents[j].RunCount })
	return agents, nil
}

// GetAgentIdentity returns one stored agent (os.ErrNotExist if absent).
func GetAgentIdentity(dataDir, workspaceID, agentID string) (*AgentIdentity, error) {
	agents, err := ListAgentIdentities(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.AgentID == agentID {
			return &a, nil
		}
	}
	return nil, os.ErrNotExist
}

// SyncAgentIdentitiesFromRuns rebuilds the registry from the workspace's runs,
// persisting a durable identity per runtime/model with first/last seen + counts.
func SyncAgentIdentitiesFromRuns(dataDir, workspaceID string) ([]AgentIdentity, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	existing, err := loadAgentIdentities(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	// preserve operator-set notes/display names by keying on agent_id
	notes := map[string]string{}
	display := map[string]string{}
	for _, a := range existing {
		if a.Notes != "" {
			notes[a.AgentID] = a.Notes
		}
		display[a.AgentID] = a.DisplayName
	}
	agents := []AgentIdentity{}
	for _, run := range runs {
		if strings.TrimSpace(run.Runtime) == "" && strings.TrimSpace(run.Model) == "" {
			continue
		}
		agents, _ = upsertAgentIdentity(agents, workspaceID, run.Runtime, run.Model, run.CreatedAt)
	}
	for i := range agents {
		if n, ok := notes[agents[i].AgentID]; ok {
			agents[i].Notes = n
		}
		if d, ok := display[agents[i].AgentID]; ok && strings.TrimSpace(d) != "" {
			agents[i].DisplayName = d
		}
	}
	if err := saveAgentIdentities(dataDir, workspaceID, agents); err != nil {
		return nil, err
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].RunCount > agents[j].RunCount })
	return agents, nil
}
