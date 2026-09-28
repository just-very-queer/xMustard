package main

import (
	"fmt"
	"maps"

	"xmustard/api-go/internal/workspaceops"
)

// impactForm picks the impact read a request names (WS-35): from and to together are a
// trace, symbol a blast radius, scope a diff-to-symbol read, and none of them the
// changes since the index baseline. from or to alone is refused.
func impactForm(params map[string]string) (workspaceops.ImpactForm, error) {
	from, to := params["from"] != "", params["to"] != ""
	switch {
	case from != to:
		return "", fmt.Errorf("a trace needs both from and to: %w", workspaceops.ErrInvalidInput)
	case from:
		return workspaceops.ImpactTrace, nil
	case params["symbol"] != "":
		return workspaceops.ImpactBlast, nil
	case params["scope"] != "":
		return workspaceops.ImpactChanges, nil
	}
	return workspaceops.ImpactSinceIndex, nil
}

// without is params less the named keys (the form's own positional arguments).
func without(params map[string]string, keys ...string) map[string]string {
	out := maps.Clone(params)
	for _, k := range keys {
		delete(out, k)
	}
	return out
}
