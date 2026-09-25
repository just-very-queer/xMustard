//go:build !darwin && !linux

package budget

import "time"

// No process-tree sampler on this platform: the watchdog reports unsupported and heavy
// admission projects from the static component reservations instead.

const treeBasis = "unsupported"

func sampleOwnTree() (TreeSample, error) {
	return TreeSample{At: time.Now(), Basis: treeBasis}, errNoRSSSampler
}
