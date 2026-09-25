//go:build race

package evidence

// raceEnabled: the race detector disables sync.Pool reuse and instruments
// allocation, so allocation-bound assertions are skipped.
const raceEnabled = true
