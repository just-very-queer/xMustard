package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuidanceRootsMatchGoldenSpec is the Go half of the shared Go/Rust role spec. It
// reads the SAME golden fixture the Rust repo_role_matches_golden_spec test pins, and
// asserts the Go guidance ROOT definitions (recognized named files + walk-root dirs)
// recognize exactly the paths the spec labels `guide`. If either side's guidance set
// drifts (a root/file added on one side only), one of the two golden tests fails. The
// .tsv is the single source of truth. (Set comparison, not the bounded discovery packet,
// so the replayGuidanceLimit truncation doesn't hide low-priority roots.)
func TestGuidanceRootsMatchGoldenSpec(t *testing.T) {
	specPath := filepath.Join("..", "..", "..", "rust-core", "src", "testdata", "repo_role_golden.tsv")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read golden spec %s: %v", specPath, err)
	}

	var guidePaths []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == "guide" {
			guidePaths = append(guidePaths, filepath.ToSlash(strings.TrimSpace(parts[0])))
		}
	}
	if len(guidePaths) < 10 {
		t.Fatalf("golden spec should list many guide paths, got %d", len(guidePaths))
	}

	// Go's recognized named guidance files (basename, lowercased).
	namedFiles := map[string]bool{}
	for _, c := range guidanceCandidateFiles {
		namedFiles[strings.ToLower(filepath.ToSlash(c.path))] = true
	}
	// Go's recognized walk-root dirs (lowercased, trailing slash).
	walkRoots := []string{}
	for _, w := range guidanceWalkRoots {
		walkRoots = append(walkRoots, strings.ToLower(filepath.ToSlash(w.path))+"/")
	}

	recognized := func(p string) bool {
		lp := strings.ToLower(p)
		if namedFiles[lp] {
			return true // exact named file (e.g. agents.md, .cursorrules)
		}
		for _, root := range walkRoots {
			if strings.HasPrefix(lp, root) {
				return true // a file beneath a guide walk-root dir
			}
		}
		return false
	}

	for _, p := range guidePaths {
		if !recognized(p) {
			t.Fatalf("Go guidance roots do NOT recognize spec-guide path %q (cross-FFI drift vs rust repo_role); fix guidanceCandidateFiles/guidanceWalkRoots or the golden spec", p)
		}
	}
}
