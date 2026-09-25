package rustcore

import (
	"os/exec"
	"sync"
)

// activeChildren tracks every started helper child (Rust core, bounded captures,
// ast-grep) so shutdown can end the owned process tree: children run in their own
// process groups, so a terminal signal to the API no longer reaches them.
var activeChildren = struct {
	sync.Mutex
	m map[*exec.Cmd]struct{}
}{m: map[*exec.Cmd]struct{}{}}

// TrackChild registers a started command; call the returned func after Wait.
func TrackChild(cmd *exec.Cmd) (untrack func()) {
	activeChildren.Lock()
	activeChildren.m[cmd] = struct{}{}
	activeChildren.Unlock()
	return func() {
		activeChildren.Lock()
		delete(activeChildren.m, cmd)
		activeChildren.Unlock()
	}
}

// KillActiveChildren kills every tracked helper child's process tree and reports how
// many were running. Used at shutdown after the bounded drain.
func KillActiveChildren() int {
	activeChildren.Lock()
	cmds := make([]*exec.Cmd, 0, len(activeChildren.m))
	for c := range activeChildren.m {
		cmds = append(cmds, c)
	}
	activeChildren.Unlock()
	for _, c := range cmds {
		KillProcessTree(c)
	}
	return len(cmds)
}

// startTracked starts cmd and tracks it for shutdown until the returned untrack is
// called (after Wait). The resident worker uses it directly; one-shot children use
// runTracked.
func startTracked(cmd *exec.Cmd) (untrack func(), err error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return TrackChild(cmd), nil
}

// runTracked starts cmd, tracks it until it exits, and waits for it.
func runTracked(cmd *exec.Cmd) error {
	untrack, err := startTracked(cmd)
	if err != nil {
		return err
	}
	defer untrack()
	return cmd.Wait()
}
