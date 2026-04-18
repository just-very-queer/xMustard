package rustcore

import (
	"context"
	"testing"
	"time"
)

func TestReadArchitectureContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	contract, err := ReadArchitectureContract(ctx)
	if err != nil {
		t.Fatalf("read architecture contract: %v", err)
	}
	if contract.DesignVersion == "" || contract.ControlPlaneOwner != "api-go" || contract.CoreOwner != "rust-core" {
		t.Fatalf("unexpected architecture contract: %#v", contract)
	}
	if len(contract.AgentSurfaces) != 3 {
		t.Fatalf("expected three agent surfaces, got %#v", contract.AgentSurfaces)
	}
	if contract.NextRemovablePythonBoundary.BoundaryID != "external_integrations_gateway" {
		t.Fatalf("unexpected python cutline: %#v", contract.NextRemovablePythonBoundary)
	}
}
