package workspaceops

import (
	"os/exec"

	"xmustard/api-go/internal/budget"
)

// Data-movement accounting (PAR-EVAL-04) for call sites in this package, so those
// files record a git spawn or hashed bytes without importing the budget package.

// noteGitSpawn records one git child that was started.
func noteGitSpawn() { budget.NoteSpawn(budget.SpawnGit) }

// noteGitIfStarted records cmd as a git spawn when it actually started (git missing
// from PATH starts nothing).
func noteGitIfStarted(cmd *exec.Cmd) {
	if cmd.Process != nil {
		noteGitSpawn()
	}
}

// noteHelperSpawn records one started short-lived helper (ast-grep, an agent CLI probe).
func noteHelperSpawn() { budget.NoteSpawn(budget.SpawnHelper) }

// noteHashed records n bytes fed through a content hash.
func noteHashed(n int) { budget.NoteHashed(int64(n)) }
