package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type SubsystemBoundary struct {
	Name         string `json:"name"`
	CurrentOwner string `json:"current_owner"`
	TargetOwner  string `json:"target_owner"`
	Notes        string `json:"notes"`
}

type AgentProtocol struct {
	ProtocolID string   `json:"protocol_id"`
	Transport  string   `json:"transport"`
	Direction  string   `json:"direction"`
	OwnedBy    string   `json:"owned_by"`
	Purpose    string   `json:"purpose"`
	Payloads   []string `json:"payloads"`
}

type AgentSurface struct {
	SurfaceID           string          `json:"surface_id"`
	Title               string          `json:"title"`
	ProductPromise      string          `json:"product_promise"`
	ControlPlaneOwner   string          `json:"control_plane_owner"`
	CoreOwner           string          `json:"core_owner"`
	SteadyStateBudgetMB int             `json:"steady_state_budget_mb"`
	Protocols           []AgentProtocol `json:"protocols"`
	DurableArtifacts    []string        `json:"durable_artifacts"`
	ExampleEndpoints    []string        `json:"example_endpoints"`
}

type PythonBoundaryCutline struct {
	BoundaryID    string   `json:"boundary_id"`
	CurrentOwner  string   `json:"current_owner"`
	TargetOwner   string   `json:"target_owner"`
	RustRole      string   `json:"rust_role"`
	PythonModules []string `json:"python_modules"`
	WhyNext       string   `json:"why_next"`
	FirstSlice    string   `json:"first_slice"`
	RemovableWhen []string `json:"removable_when"`
}

type ArchitectureContract struct {
	DesignVersion               string                `json:"design_version"`
	SteadyStateRuntimeBudgetMB  int                   `json:"steady_state_runtime_budget_mb"`
	ControlPlaneOwner           string                `json:"control_plane_owner"`
	CoreOwner                   string                `json:"core_owner"`
	PythonEndState              string                `json:"python_end_state"`
	Boundaries                  []SubsystemBoundary   `json:"boundaries"`
	AgentSurfaces               []AgentSurface        `json:"agent_surfaces"`
	NextRemovablePythonBoundary PythonBoundaryCutline `json:"next_removable_python_boundary"`
}

func ReadArchitectureContract(ctx context.Context) (*ArchitectureContract, error) {
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"describe-architecture",
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core describe-architecture failed: %w: %s", err, stderr.String())
	}

	var contract ArchitectureContract
	if err := json.Unmarshal(stdout.Bytes(), &contract); err != nil {
		return nil, fmt.Errorf("decode rust-core architecture contract: %w", err)
	}
	return &contract, nil
}
