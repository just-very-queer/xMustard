//go:build !unix

package transport

import "os"

// ownedByMe cannot read a file's owner here, so no socket is trusted: the client uses
// the TCP address and the daemon serves no socket.
func ownedByMe(os.FileInfo) bool { return false }
