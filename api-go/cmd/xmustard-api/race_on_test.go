//go:build race

package main

// raceEnabled: the race detector shadows every heap allocation and disables
// sync.Pool reuse, so allocation-bound assertions are skipped.
const raceEnabled = true
