//go:build !(darwin || linux)

package daemon

import "os"

// redirectStdio is a no-op where no service manager runs the daemon.
func redirectStdio(*os.File) error { return nil }
