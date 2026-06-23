package workspaceops

import (
	"log"
)

// ShutdownInFlight performs a workload-aware drain AFTER HTTP admissions have stopped
// (srv.Shutdown). Order matters: persist+reap in-flight runs, close terminals, then
// flush the Postgres mirror so the durable interrupted states reach PG before the pool
// closes. The caller closes the PG pool afterward. Every step is best-effort and bounded
// so shutdown can't hang.
//
//   1. each live run: durably mark it `interrupted` (under the run transaction, so JSON +
//      the PG mirror converge) and signal its process group, so a restart never re-attaches
//      to or double-launches an orphaned worker.
//   2. each terminal: mark closed, terminate the shell, close the PTY.
//   3. flush the inline PG mirror workers so the interrupted run snapshots are mirrored.
func ShutdownInFlight(dataDir string) {
	interruptInFlightRuns(dataDir)
	closeAllTerminals()
	PgInlineFlush()
}

func interruptInFlightRuns(dataDir string) {
	type live struct {
		runID string
		mp    *managedRun
	}
	var runs []live
	activeRunProcesses.Range(func(k, v any) bool {
		runID, _ := k.(string)
		mp, _ := v.(*managedRun)
		if runID != "" {
			runs = append(runs, live{runID: runID, mp: mp})
		}
		return true
	})
	for _, lv := range runs {
		// mark cancellation intent so the run's own finalize goroutine (if it races us)
		// converges to a terminal, non-running status rather than "completed".
		cancelledRunIDs.Store(lv.runID, struct{}{})
		if lv.mp == nil || lv.mp.workspaceID == "" {
			continue
		}
		// Persist `interrupted` BEFORE signalling, so even an abrupt exit mid-shutdown
		// leaves a durable non-running record (no zombie "running" run after restart).
		_, err := mutateRun(dataDir, lv.mp.workspaceID, lv.runID, func(r *runRecord) (bool, error) {
			if isTerminalRunStatus(r.Status) {
				return false, nil
			}
			now := nowUTC()
			code := -15
			r.Status = "interrupted"
			r.CompletedAt = &now
			r.PID = nil // cleared so nothing signals a reused PID after reap
			if r.ExitCode == nil {
				r.ExitCode = &code
			}
			return true, nil
		})
		if err != nil {
			log.Printf("shutdown: persist interrupted run %s: %v", lv.runID, err)
		}
		if lv.mp.cmd != nil {
			terminateManagedRunCommand(lv.mp.cmd)
		}
	}
}

func closeAllTerminals() {
	var toClose []*terminalSession
	terminalSessions.Range(func(k, v any) bool {
		if s, ok := v.(*terminalSession); ok {
			toClose = append(toClose, s)
			terminalSessions.Delete(k)
		}
		return true
	})
	for _, s := range toClose {
		s.markClosed()
		terminateTerminalProcess(s.process)
		s.closePTY()
	}
}
