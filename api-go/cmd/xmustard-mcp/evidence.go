package main

import (
	"context"
	"encoding/json"
	"os"

	"xmustard/api-go/internal/mcpserver"
)

// Evidence delivery and resources for this stdio process live in mcpserver (shared
// with the API's Streamable HTTP endpoint); these names keep the shim's call sites.

const (
	deliveryHeader   = mcpserver.DeliveryHeader
	deliveryVersion  = mcpserver.DeliveryVersion
	resourceScheme   = mcpserver.ResourceScheme
	resourceNotFound = mcpserver.CodeResourceNotFound
)

// evidence is this process's (one session's) delivery and retained-handle map.
var evidence = mcpserver.NewEvidence(backend, os.Getenv)

func withCallID(ctx context.Context, id json.RawMessage) context.Context {
	return mcpserver.WithCallID(ctx, id)
}

func evidenceResult(ctx context.Context, body, ws string) (map[string]any, *rpcError) {
	return evidence.EnvelopeResult(ctx, body, ws)
}

func resourcesListResult() map[string]any {
	return evidence.List(context.Background()).(map[string]any)
}

func resourceTemplatesResult() map[string]any {
	return evidence.Templates(context.Background()).(map[string]any)
}

func readResource(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	return evidence.Read(ctx, params)
}
