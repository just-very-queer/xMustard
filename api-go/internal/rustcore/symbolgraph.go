package rustcore

import (
	"bytes"
	"fmt"
	"os/exec"
)

// RunSymbolgraph runs `xmustard-core symbolgraph <args...>` (the semantic symbol
// graph: files/symbols/reference edges, hotspots, blast radius).
func RunSymbolgraph(args ...string) ([]byte, error) {
	full := append([]string{"run", "--quiet", "--bin", "xmustard-core", "--", "symbolgraph"}, args...)
	cmd := exec.Command("cargo", full...)
	cmd.Dir = rustCoreDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core symbolgraph %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
