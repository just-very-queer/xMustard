package rustcore

// runCore routes through the shared hardened bridge runner (timeout, bounded
// output, sanitized errors, server-side logs — see runCoreContext).
func runCore(sub string, args ...string) ([]byte, error) {
	return runCoreContext(sub, args...)
}

// RunSearch runs hybrid repo search; RunWiki generates the repo wiki.
func RunSearch(args ...string) ([]byte, error) { return runCore("search", args...) }
func RunWiki(args ...string) ([]byte, error)   { return runCore("wiki", args...) }

// RunOwnership runs the ownership/subsystem model commands.
func RunOwnership(args ...string) ([]byte, error) { return runCore("ownership", args...) }
