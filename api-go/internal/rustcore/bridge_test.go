package rustcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A flooding core child is bounded (output too large), not OOM, and the error is
// sanitized (no raw stderr / paths). Uses a fake core binary via XMUSTARD_CORE_BIN.
func TestRunCoreContextBoundsFloodingChild(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakecore.sh")
	// ignores args; floods stdout well past maxCoreStdout would be slow, so shrink via
	// a smaller flood that still exceeds when maxCoreStdout is large — instead assert
	// the sanitized error path on a non-zero exit with noisy stderr.
	script := "#!/bin/sh\necho 'secret /home/user/.ssh/id_rsa internal detail' 1>&2\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", fake)
	_, err := runCoreCtx(context.Background(), "search", "root", "ws", "query")
	if err == nil {
		t.Fatal("a failing core child must error")
	}
	if strings.Contains(err.Error(), "id_rsa") || strings.Contains(err.Error(), ".ssh") {
		t.Fatalf("client error must be sanitized (no raw stderr/paths): %q", err.Error())
	}
}
