//go:build race

package main

// raceEnabled: the race detector allocates a shadow word for every instrumented
// access, so a runtime.MemStats.TotalAlloc delta measures the detector rather
// than the code under test. Pools are also randomised under -race. The bounds
// these tests assert are meaningful in an ordinary run, so the race build skips
// them and keeps running the rest of the package.
const raceEnabled = true
