package rustcore

// RunSymbolgraph runs `xmustard-core symbolgraph <args...>` (symbol graph,
// hotspots, blast radius) via the shared hardened bridge runner.
func RunSymbolgraph(args ...string) ([]byte, error) {
	return runCoreContext("symbolgraph", args...)
}
