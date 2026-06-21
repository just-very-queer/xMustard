package workspaceops

import (
	"testing"
	"time"
)

// An idle terminal session is reaped (closed + removed); a fresh one survives — so an
// abandoned session can't leak its shell/PTY/log/goroutine forever (XM-POST-003).
func TestReapIdleTerminals(t *testing.T) {
	idle := &terminalSession{terminalID: "t-idle", workspaceID: "ws", lastActivity: time.Now().Add(-2 * terminalIdleTTL)}
	fresh := &terminalSession{terminalID: "t-fresh", workspaceID: "ws", lastActivity: time.Now()}
	terminalSessions.Store("t-idle", idle)
	terminalSessions.Store("t-fresh", fresh)
	t.Cleanup(func() {
		terminalSessions.Delete("t-idle")
		terminalSessions.Delete("t-fresh")
	})

	reapIdleTerminals()

	if _, ok := terminalSessions.Load("t-idle"); ok {
		t.Fatal("idle terminal must be reaped")
	}
	if _, ok := terminalSessions.Load("t-fresh"); !ok {
		t.Fatal("fresh terminal must survive")
	}
	if !idle.isClosed() {
		t.Fatal("reaped session must be marked closed")
	}
}
