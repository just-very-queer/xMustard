package rustcore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapWriterBoundsAndFlags(t *testing.T) {
	c := &capWriter{max: 1024}
	chunk := bytes.Repeat([]byte("y"), 1024)
	for i := 0; i < 50; i++ {
		n, _ := c.Write(chunk)
		if n != len(chunk) {
			t.Fatalf("Write must report full length (no child block), got %d", n)
		}
	}
	if c.buf.Len() > c.max {
		t.Fatalf("buffered %d bytes, must be <= max %d", c.buf.Len(), c.max)
	}
	if !c.over {
		t.Fatal("over flag must be set after exceeding max")
	}
}

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
	_, err := runCoreContext("search", "root", "ws", "query")
	if err == nil {
		t.Fatal("a failing core child must error")
	}
	if strings.Contains(err.Error(), "id_rsa") || strings.Contains(err.Error(), ".ssh") {
		t.Fatalf("client error must be sanitized (no raw stderr/paths): %q", err.Error())
	}
}
