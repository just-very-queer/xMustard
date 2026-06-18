package rustcore

import (
	"bytes"
	"fmt"
)

// RunChangetrack runs `xmustard-core changetrack <args...>` and returns stdout.
// The Rust core owns gitnexus-style change tracking; this is the delivery bridge.
func RunChangetrack(args ...string) ([]byte, error) {
	cmd := coreCommand("changetrack", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core changetrack %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
