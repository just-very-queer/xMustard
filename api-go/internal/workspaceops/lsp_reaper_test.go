package workspaceops

import (
	"testing"
	"time"
)

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// An idle LSP session is reaped (removed + closed) by the autonomous sweep, while a
// recently-used one survives — without this an abandoned session leaks its child
// process, readLoop goroutine, and FDs forever.
func TestReapIdleLSPSessions(t *testing.T) {
	idleKey := lspSessionKey{WorkspaceID: "wsR", ServerID: "fake-idle"}
	freshKey := lspSessionKey{WorkspaceID: "wsR", ServerID: "fake-fresh"}

	idle := &lspSession{stdin: nopWriteCloser{}, lastUsedAt: time.Now().Add(-2 * lspSessionIdleTTL), done: make(chan struct{})}
	fresh := &lspSession{stdin: nopWriteCloser{}, lastUsedAt: time.Now(), done: make(chan struct{})}
	lspSessions.Store(idleKey, idle)
	lspSessions.Store(freshKey, fresh)
	t.Cleanup(func() {
		lspSessions.Delete(idleKey)
		lspSessions.Delete(freshKey)
	})

	reapIdleLSPSessions()

	if _, ok := lspSessions.Load(idleKey); ok {
		t.Fatal("idle session must be reaped")
	}
	if _, ok := lspSessions.Load(freshKey); !ok {
		t.Fatal("recently-used session must survive")
	}
}
