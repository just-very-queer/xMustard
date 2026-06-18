package rustcore

import (
	"bytes"
	"fmt"
	"os/exec"
)

// RunChangetrack runs `xmustard-core changetrack <args...>` and returns stdout.
// The Rust core owns gitnexus-style change tracking (fingerprint, baseline,
// drift, changed-since, dirty symbols); this is the delivery bridge.
func RunChangetrack(args ...string) ([]byte, error) {
	full := append([]string{"run", "--quiet", "--bin", "xmustard-core", "--", "changetrack"}, args...)
	cmd := exec.Command("cargo", full...)
	cmd.Dir = rustCoreDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core changetrack %v: %w: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
