package rustcore

// RunChangetrack runs `xmustard-core changetrack <args...>` and returns stdout.
// The Rust core owns gitnexus-style change tracking; this is the delivery bridge
// (timeout + bounded output + sanitized errors via runCoreContext).
func RunChangetrack(args ...string) ([]byte, error) {
	return runCoreContext("changetrack", args...)
}
