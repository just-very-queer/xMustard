package rustcore

import (
	"bytes"
	"fmt"
)

func runCore(sub string, args ...string) ([]byte, error) {
	cmd := coreCommand(sub, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core %s %v: %w: %s", sub, args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// RunSearch runs hybrid repo search; RunWiki generates the repo wiki.
func RunSearch(args ...string) ([]byte, error) { return runCore("search", args...) }
func RunWiki(args ...string) ([]byte, error)   { return runCore("wiki", args...) }
