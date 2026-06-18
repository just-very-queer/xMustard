package rustcore

import (
	"bytes"
	"fmt"
)

// RunSymbolgraph runs `xmustard-core symbolgraph <args...>` (symbol graph,
// hotspots, blast radius).
func RunSymbolgraph(args ...string) ([]byte, error) {
	cmd := coreCommand("symbolgraph", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core symbolgraph %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
