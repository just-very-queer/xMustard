package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

const localDiagOne = `{"path":"src/app.py","diagnostics":[{"range":{"start":{"line":0,"character":6},"end":{"line":0,"character":19}},"severity":1,"code":"PY100","message":"Example LSP diagnostic."}]}`

// localDiagFixture is a git-backed workspace with no PostgreSQL configured. Any
// PostgreSQL dial fails the test, which is the no-DB proof for every local case.
func localDiagFixture(t *testing.T) (dataDir, workspaceID, repoRoot string) {
	t.Helper()
	dataDir, workspaceID, _, repoRoot = writeIssueContextFixture(t, false)
	previous := connectSemanticPostgres
	connectSemanticPostgres = func(context.Context, string) (semanticMaterializationConn, error) {
		t.Errorf("local diagnostics dialed PostgreSQL")
		return nil, errors.New("no database in this test")
	}
	t.Cleanup(func() { connectSemanticPostgres = previous })
	if _, err := exec.LookPath("git"); err == nil {
		gitIn(t, repoRoot, "init", "-q")
		gitIn(t, repoRoot, "add", "-A")
		gitIn(t, repoRoot, "commit", "-q", "-m", "fixture")
	}
	return dataDir, workspaceID, repoRoot
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// writeDiagInput writes an input file into dir and returns its absolute path.
func writeDiagInput(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runLocal(t *testing.T, dataDir, ws, input string) (*DiagnosticsRunResult, error) {
	t.Helper()
	return RunDiagnosticsCtx(context.Background(), dataDir, ws, DiagnosticsRequest{InputPath: input, SourceKind: "compiler", SourceName: "fixture"}, DiagnosticsRunOptions{InputAuthority: DiagnosticsInputLocalOperator})
}

func mustRunLocal(t *testing.T, dataDir, ws, input string) *DiagnosticsRunResult {
	t.Helper()
	result, err := runLocal(t, dataDir, ws, input)
	if err != nil {
		t.Fatalf("local import: %v", err)
	}
	if result.Baseline == nil {
		t.Fatalf("local import did not publish: %#v", result.Plan)
	}
	return result
}

func latestRunID(t *testing.T, dataDir, ws string) string {
	t.Helper()
	read, err := ReadDiagnostics(dataDir, ws, "")
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if read.Baseline == nil {
		return ""
	}
	return read.Baseline.DiagnosticRunID
}

func TestDiagnosticsStatusLocalNoBaselineWithoutPostgres(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	status, err := ReadDiagnosticsStatus(dataDir, ws)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "no_baseline" || status.PostgresConfigured || status.StorageBackend != "local" || status.FreshnessBasis != "ingestion_identity" || status.Baseline != nil {
		t.Fatalf("want local no_baseline, got %#v", status)
	}
	read, err := ReadDiagnosticsCtx(context.Background(), dataDir, ws, "")
	if err != nil {
		t.Fatalf("no-DSN read must succeed: %v", err)
	}
	encoded, _ := json.Marshal(read)
	if read.Baseline != nil || len(read.Diagnostics) != 0 || read.Storage == nil || read.Storage.Backend != "local" || read.Storage.Status != "no_baseline" || strings.Contains(string(encoded), `"baseline"`) {
		t.Fatalf("want empty local read with baseline omitted, got %s", encoded)
	}
	if !strings.Contains(strings.Join(read.Warnings, "\n"), "No diagnostics baseline has been materialized.") {
		t.Fatalf("missing no-baseline warning: %#v", read.Warnings)
	}
}

func TestLocalDiagnosticsImportSurvivesNewReader(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	result := mustRunLocal(t, dataDir, ws, input)
	if result.StorageBackend != "local" || result.Plan.StorageBackend != "local" || result.Plan.PostgresConfigured || result.Plan.PostgresSchema != "" {
		t.Fatalf("want local storage on plan and result, got %#v", result)
	}
	if strings.Contains(strings.Join(result.Plan.NextActions, "\n"), "Configure Postgres") || len(result.Plan.Blockers) != 0 {
		t.Fatalf("local plan must not ask for Postgres: %#v", result.Plan)
	}
	run := result.Baseline
	if run.StorageBackend != "local" || run.SourceRevision != "unknown" || run.FreshnessBasis != "ingestion_identity" || run.PostgresSchema != "" || run.SemanticBaseline != nil {
		t.Fatalf("unexpected local run fields: %#v", run)
	}
	if run.Normalization == nil || run.Normalization.Status != "complete" || run.Normalization.Items != 1 || run.Coverage == nil || run.Coverage.Status != "known" {
		t.Fatalf("unexpected normalization/coverage: %#v %#v", run.Normalization, run.Coverage)
	}
	if run.IngestionIdentity == nil || run.IngestionIdentity.Status != "matched" || run.Retention == nil {
		t.Fatalf("unexpected identity/retention: %#v %#v", run.IngestionIdentity, run.Retention)
	}

	// A fresh read (new store instance, as after a restart) sees the same record.
	read, err := ReadDiagnostics(dataDir, ws, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Baseline == nil || read.Baseline.DiagnosticRunID != run.DiagnosticRunID || len(read.Diagnostics) != 1 {
		t.Fatalf("read did not return the imported baseline: %#v", read)
	}
	row := read.Diagnostics[0]
	if row.Severity != "error" || row.Path != "src/app.py" || row.RangeStartLine != 1 || row.RangeStartColumn != 7 || row.Message != "Example LSP diagnostic." || len(row.Fingerprint) != 40 {
		t.Fatalf("unexpected row: %#v", row)
	}
	if row.LinkStatus != diagnosticLinkStatusSymbolsUnavailable || row.LinkedSymbol != nil || row.IdentityStatus != identityObserved || row.ContentHash == nil {
		t.Fatalf("unexpected local link/identity: %#v", row)
	}
	if read.Storage.Status != "available" || read.Storage.FreshnessBasis != "ingestion_identity" {
		t.Fatalf("unexpected storage: %#v", read.Storage)
	}
	if !strings.Contains(strings.Join(read.Warnings, "\n"), "Symbol linking requires PostgreSQL") {
		t.Fatalf("missing local symbol warning: %#v", read.Warnings)
	}
	archive := read.Baseline.ReplayArchive
	if archive == nil || archive.RawPayloadSHA256 != sha256Hex([]byte(localDiagOne)) || archive.RawPayloadBytes != len(localDiagOne) {
		t.Fatalf("raw provenance mismatch: %#v", archive)
	}
	byID, err := ReadDiagnostics(dataDir, ws, run.DiagnosticRunID)
	if err != nil || byID.Baseline == nil || byID.Diagnostics[0].Fingerprint != row.Fingerprint {
		t.Fatalf("read by id: %v %#v", err, byID)
	}
	// The envelope keeps the exact original bytes, not Rust's re-emitted JSON.
	envelope := readEnvelopeFile(t, dataDir, ws, run.DiagnosticRunID)
	raw, _ := base64.StdEncoding.DecodeString(envelope.RawPayloadBase64)
	if string(raw) != localDiagOne {
		t.Fatalf("envelope raw bytes differ from input")
	}
	// Data lives under the data dir, never in the repository.
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", ws, "diagnostics", "latest.json")); err != nil {
		t.Fatalf("latest pointer: %v", err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readEnvelopeFile(t *testing.T, dataDir, ws, runID string) localDiagnosticsEnvelope {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs", runID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope localDiagnosticsEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestLocalDiagnosticsEmptyArrayIsCompleteZeroRows(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "empty.json", "[]"))
	if result.DiagnosticRows != 0 || result.Baseline.DiagnosticCount != 0 || result.Baseline.Normalization.Status != "complete" {
		t.Fatalf("want complete zero-row baseline, got %#v", result.Baseline)
	}
	read, err := ReadDiagnostics(dataDir, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(read)
	if read.Baseline == nil || read.Storage.Status != "available" || !strings.Contains(string(encoded), `"diagnostics":[]`) {
		t.Fatalf("an imported empty baseline is not 'no baseline': %#v", read)
	}
	if !strings.Contains(read.Baseline.Coverage.Note, "not proof") {
		t.Fatalf("coverage must not claim whole-repo cleanliness: %#v", read.Baseline.Coverage)
	}
}

func TestLocalDiagnosticsRejectsBadInputAndKeepsBaseline(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	dir := t.TempDir()
	good := mustRunLocal(t, dataDir, ws, writeDiagInput(t, dir, "good.json", localDiagOne))
	for name, body := range map[string]string{
		"object-without-diagnostics": `{}`,
		"diagnostics-not-array":      `{"diagnostics":{}}`,
		"duplicate-key":              `{"diagnostics":[],"diagnostics":[]}`,
		"scalar":                     `42`,
		"trailing":                   `[] []`,
		"not-json":                   `nope`,
		"all-invalid":                `[{"message":"no path"},{"path":"src/app.py"}]`,
		"not-utf8":                   "[\"\xff\"]",
	} {
		_, err := runLocal(t, dataDir, ws, writeDiagInput(t, dir, name+".json", body))
		if !errors.Is(err, ErrInvalidDiagnosticsRequest) {
			t.Fatalf("%s: want invalid request, got %v", name, err)
		}
	}
	if got := latestRunID(t, dataDir, ws); got != good.Baseline.DiagnosticRunID {
		t.Fatalf("failed imports replaced the baseline: %s", got)
	}
}

func TestLocalDiagnosticsPartialNormalizationIsLabelled(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	body := `[{"path":"src/app.py","message":"kept","severity":2},{"message":"no path"}]`
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "partial.json", body))
	n := result.Baseline.Normalization
	if n.Status != "partial" || n.Items != 2 || n.Normalized != 1 || n.Skipped != 1 || result.Baseline.Coverage.Status != "unknown" {
		t.Fatalf("want partial normalization, got %#v %#v", n, result.Baseline.Coverage)
	}
	status, err := ReadDiagnosticsStatus(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "available" || !containsString(status.StaleReasons, "normalization_partial") {
		t.Fatalf("want available + normalization_partial, got %#v", status)
	}
}

func TestLocalDiagnosticsOutsideRowPathsAreNeverTouched(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	// A missing absolute canary: had it been stat'ed it would read as "missing".
	canary := filepath.Join(t.TempDir(), "canary-never-created.txt")
	body := fmt.Sprintf(`[{"path":%q,"message":"abs"},{"path":"../escape.txt","message":"up"},{"path":"src/app.py","message":"in"}]`, canary)
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "outside.json", body))
	envelope := readEnvelopeFile(t, dataDir, ws, result.Baseline.DiagnosticRunID)
	statuses := map[string]string{}
	for _, row := range envelope.Rows {
		statuses[row.Message] = row.IdentityStatus
		if row.Message != "in" && row.ContentHash != nil {
			t.Fatalf("outside row got a content hash: %#v", row)
		}
	}
	if statuses["abs"] != identityOutside || statuses["up"] != identityOutside || statuses["in"] != identityObserved {
		t.Fatalf("unexpected identity statuses: %#v", statuses)
	}
	if result.Baseline.IngestionIdentity.Status != "partial" {
		t.Fatalf("want partial identity, got %#v", result.Baseline.IngestionIdentity)
	}
}

// brokenCore makes any Rust invocation fail loudly, proving a check ran before Rust.
func brokenCore(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_BIN", filepath.Join(t.TempDir(), "no-such-core"))
}

func TestLocalDiagnosticsCapsRejectBeforeRust(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	dir := t.TempDir()
	brokenCore(t)
	big := "[" + strings.Repeat(" ", diagnosticsMaxRawBytes) + "]"
	if _, err := runLocal(t, dataDir, ws, writeDiagInput(t, dir, "big.json", big)); !errors.Is(err, ErrDiagnosticsLimit) {
		t.Fatalf("raw cap: want ErrDiagnosticsLimit, got %v", err)
	}
	rows := "[" + strings.TrimSuffix(strings.Repeat(`{"path":"a","message":"m"},`, diagnosticsMaxRows+1), ",") + "]"
	if _, err := runLocal(t, dataDir, ws, writeDiagInput(t, dir, "rows.json", rows)); !errors.Is(err, ErrDiagnosticsLimit) {
		t.Fatalf("row cap: want ErrDiagnosticsLimit, got %v", err)
	}
}

func TestLocalDiagnosticsMaxRowsFitTheAdmissionLedger(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	rows := "[" + strings.TrimSuffix(strings.Repeat(`{"path":"src/app.py","message":"m","severity":1},`, diagnosticsMaxRows), ",") + "]"
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "max.json", rows))
	if result.DiagnosticRows != diagnosticsMaxRows {
		t.Fatalf("want %d rows, got %d", diagnosticsMaxRows, result.DiagnosticRows)
	}
}

func TestDiagnosticsIdentityWorkIsBounded(t *testing.T) {
	_, _, repoRoot := localDiagFixture(t)
	paths := make([]string, diagnosticsMaxIdentityPaths+1)
	for i := range paths {
		paths[i] = "src/app.py"
	}
	paths[diagnosticsMaxIdentityPaths] = "src/feature_test.py"
	ids, summary, err := observeDiagnosticPaths(context.Background(), budget.NewScope(nil), repoRoot, paths)
	if err != nil {
		t.Fatal(err)
	}
	if ids[diagnosticsMaxIdentityPaths].Status != identityBudgetExceeded || summary.Status != "partial" {
		t.Fatalf("path cap not enforced: %#v %#v", ids[diagnosticsMaxIdentityPaths], summary)
	}
	if id, n := hashDiagnosticPath(repoRoot, "src/app.py", make([]byte, 1024), 3); id.Status != identityBudgetExceeded || id.SHA256 != "" || n != 0 {
		t.Fatalf("byte cap must not produce a prefix hash: %#v (streamed %d)", id, n)
	}
}

func TestLocalDiagnosticsBusyIs503NotCapBreach(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	holder := newLocalDiagnosticsStore(dataDir)
	unlock, err := holder.lockImport(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = runLocal(t, dataDir, ws, input)
	unlock()
	if !errors.Is(err, budget.ErrOverloaded) || errors.Is(err, ErrDiagnosticsLimit) {
		t.Fatalf("held workspace lock: want ErrOverloaded, got %v", err)
	}
	if waited := time.Since(start); waited < diagnosticsLockWait || waited > diagnosticsLockWait+3*time.Second {
		t.Fatalf("lock wait not bounded to ~%s: %s", diagnosticsLockWait, waited)
	}
	for i := 0; i < cap(diagnosticsImportSlots); i++ {
		diagnosticsImportSlots <- struct{}{}
	}
	_, err = runLocal(t, dataDir, ws, input)
	for i := 0; i < cap(diagnosticsImportSlots); i++ {
		<-diagnosticsImportSlots
	}
	if !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("process import limit: want ErrOverloaded, got %v", err)
	}
	mustRunLocal(t, dataDir, ws, input)
}

func TestLocalDiagnosticsCorruptEnvelopeAndUnknownRun(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "r.json", localDiagOne))
	if _, err := ReadDiagnostics(dataDir, ws, "diag_000000000000"); !errors.Is(err, ErrInvalidDiagnosticsRequest) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown run: %v", err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, "../../settings"); !errors.Is(err, ErrInvalidDiagnosticsRequest) {
		t.Fatalf("malformed run id: %v", err)
	}
	path := filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs", result.Baseline.DiagnosticRunID+".json")
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(raw[:len(raw)-2], ' ', '}'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, ""); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
		t.Fatalf("tampered envelope must be an explicit error, got %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, ""); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
		t.Fatalf("pointer to a missing envelope must not read as no_baseline, got %v", err)
	}
}

func TestLocalDiagnosticsQuotaRejectsAndExpiryPrunes(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	first := mustRunLocal(t, dataDir, ws, input)
	runsDir := filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs")
	fillers := []string{}
	for i := 0; len(fillers)+1 < diagnosticsStoreQuotaBaselines; i++ {
		name := filepath.Join(runsDir, fmt.Sprintf("diag_%012x.json", i+1))
		if err := os.WriteFile(name, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		fillers = append(fillers, name)
	}
	_, err := runLocal(t, dataDir, ws, input)
	if !errors.Is(err, ErrDiagnosticsQuota) || !strings.Contains(err.Error(), "expires") {
		t.Fatalf("want quota rejection with earliest expiry, got %v", err)
	}
	for _, name := range fillers {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("unexpired baseline evicted: %s", name)
		}
	}
	// Age everything past retention, including the latest; only non-latest is pruned.
	old := time.Now().Add(-diagnosticsRetention - time.Hour)
	entries, _ := os.ReadDir(runsDir)
	for _, entry := range entries {
		_ = os.Chtimes(filepath.Join(runsDir, entry.Name()), old, old)
	}
	second := mustRunLocal(t, dataDir, ws, input)
	if _, err := os.Stat(fillers[0]); !os.IsNotExist(err) {
		t.Fatalf("expired baseline not pruned: %v", err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, first.Baseline.DiagnosticRunID); err != nil {
		t.Fatalf("the then-latest baseline must survive the prune that published its successor: %v", err)
	}
	if latestRunID(t, dataDir, ws) != second.Baseline.DiagnosticRunID {
		t.Fatal("latest pointer not updated")
	}
}

func TestLocalDiagnosticsCrashWindowsKeepReadersConsistent(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	first := mustRunLocal(t, dataDir, ws, input)
	crash := errors.New("simulated crash")
	for _, stage := range []string{"before_envelope_rename", "before_pointer_rename"} {
		diagnosticsPublishHook = func(s string) error {
			if s == stage {
				return crash
			}
			return nil
		}
		_, err := runLocal(t, dataDir, ws, input)
		diagnosticsPublishHook = nil
		if !errors.Is(err, crash) {
			t.Fatalf("%s: want simulated crash, got %v", stage, err)
		}
		if got := latestRunID(t, dataDir, ws); got != first.Baseline.DiagnosticRunID {
			t.Fatalf("%s: readers must still see the previous baseline, got %s", stage, got)
		}
	}
	// The next import recovers leftover temp files and publishes normally.
	third := mustRunLocal(t, dataDir, ws, input)
	if latestRunID(t, dataDir, ws) != third.Baseline.DiagnosticRunID {
		t.Fatal("recovery import not visible")
	}
	for _, dir := range []string{"", "runs"} {
		entries, _ := os.ReadDir(filepath.Join(dataDir, "workspaces", ws, "diagnostics", dir))
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("orphan temp file survived recovery: %s", entry.Name())
			}
		}
	}
}

// fakeCore installs a shell script as the Rust core.
func fakeCore(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-core")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", path)
}

func TestLocalDiagnosticsFailureTimeoutAndCancelNeverOverwrite(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	good := mustRunLocal(t, dataDir, ws, input)

	t.Run("failed producer blocks", func(t *testing.T) {
		fakeCore(t, "exit 3")
		result, err := runLocal(t, dataDir, ws, input)
		if err != nil || result.Baseline != nil || result.Plan.CanRun {
			t.Fatalf("want a blocked run, got %v %#v", err, result)
		}
	})
	t.Run("timeout kills the child", func(t *testing.T) {
		fakeCore(t, "exec sleep 30")
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := RunDiagnosticsCtx(ctx, dataDir, ws, DiagnosticsRequest{InputPath: input}, DiagnosticsRunOptions{InputAuthority: DiagnosticsInputLocalOperator})
		if err == nil || time.Since(start) > 10*time.Second {
			t.Fatalf("want a prompt timeout error, got %v after %s", err, time.Since(start))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := RunDiagnosticsCtx(ctx, dataDir, ws, DiagnosticsRequest{InputPath: input}, DiagnosticsRunOptions{InputAuthority: DiagnosticsInputLocalOperator}); err == nil {
			t.Fatal("cancelled import must fail")
		}
	})
	if got := latestRunID(t, dataDir, ws); got != good.Baseline.DiagnosticRunID {
		t.Fatalf("baseline overwritten: %s", got)
	}
}

func TestLocalDiagnosticsStatusLabels(t *testing.T) {
	requireGit(t)
	dataDir, ws, repoRoot := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	statusOf := func() *DiagnosticsStatus {
		t.Helper()
		status, err := ReadDiagnosticsStatus(dataDir, ws)
		if err != nil {
			t.Fatal(err)
		}
		read, err := ReadDiagnostics(dataDir, ws, "")
		if err != nil {
			t.Fatal(err)
		}
		if read.Storage.Status != status.Status || strings.Join(read.Storage.StaleReasons, ",") != strings.Join(status.StaleReasons, ",") {
			t.Fatalf("GET storage %#v does not mirror status %#v", read.Storage, status)
		}
		if status.Status == "fresh" || status.Status == "dirty_provisional" {
			t.Fatalf("local storage must never claim %s", status.Status)
		}
		return status
	}
	mustRunLocal(t, dataDir, ws, input)
	if s := statusOf(); s.Status != "available" || len(s.StaleReasons) != 0 {
		t.Fatalf("unchanged: %#v", s)
	}
	app := filepath.Join(repoRoot, "src", "app.py")
	original, _ := os.ReadFile(app)
	same := strings.Replace(string(original), "ok", "no", 1) // same size
	if err := os.WriteFile(app, []byte(same), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(); s.Status != "available" || strings.Join(s.StaleReasons, ",") != "dirty_worktree" {
		t.Fatalf("dirty same-size change: %#v", s)
	}
	gitIn(t, repoRoot, "commit", "-qam", "move head")
	if s := statusOf(); s.Status != "stale" || strings.Join(s.StaleReasons, ",") != "head_moved" {
		t.Fatalf("head moved: %#v", s)
	}
	if err := os.Rename(filepath.Join(repoRoot, "src", "feature_test.py"), filepath.Join(repoRoot, "src", "renamed_test.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(app); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(); s.Status != "stale" || strings.Join(s.StaleReasons, ",") != "head_moved,dirty_worktree" {
		t.Fatalf("head moved + delete/rename: %#v", s)
	}
	if err := os.WriteFile(app, original, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repoRoot, "add", "-A")
	gitIn(t, repoRoot, "commit", "-qm", "restore")

	// A file mutated while it was hashed makes the ingestion identity "changed".
	diagnosticIdentityHook = func(rel string) {
		later := time.Now().Add(time.Minute)
		_ = os.WriteFile(filepath.Join(repoRoot, filepath.FromSlash(rel)), append(original, '#'), 0o644)
		_ = os.Chtimes(filepath.Join(repoRoot, filepath.FromSlash(rel)), later, later)
	}
	result, err := runLocal(t, dataDir, ws, input)
	diagnosticIdentityHook = nil
	if err != nil {
		t.Fatal(err)
	}
	if result.Baseline.IngestionIdentity.Status != "changed" || result.Baseline.SourceRevision != "unknown" {
		t.Fatalf("mutation during import: %#v", result.Baseline.IngestionIdentity)
	}
	if s := statusOf(); s.Status != "stale" || !containsString(s.StaleReasons, "ingestion_identity_changed") {
		t.Fatalf("changed identity must be stale: %#v", s)
	}
}

func TestDiagnosticsInputAuthority(t *testing.T) {
	dataDir, ws, repoRoot := localDiagFixture(t)
	outside := writeDiagInput(t, t.TempDir(), "outside.json", localDiagOne)
	if err := os.Symlink(outside, filepath.Join(repoRoot, "link.json")); err != nil {
		t.Fatal(err)
	}
	http := DiagnosticsRunOptions{InputAuthority: DiagnosticsInputWorkspace}
	for _, path := range []string{outside, "../outside.json", "link.json", "src/../../outside.json"} {
		_, err := RunDiagnosticsCtx(context.Background(), dataDir, ws, DiagnosticsRequest{InputPath: path}, http)
		if !errors.Is(err, ErrInvalidDiagnosticsRequest) {
			t.Fatalf("HTTP authority accepted %q: %v", path, err)
		}
	}
	// A workspace-relative regular file is admitted over HTTP.
	if err := os.MkdirAll(filepath.Join(repoRoot, ".xmustard-e2e"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDiagInput(t, filepath.Join(repoRoot, ".xmustard-e2e"), "d.json", localDiagOne)
	if result, err := RunDiagnosticsCtx(context.Background(), dataDir, ws, DiagnosticsRequest{InputPath: ".xmustard-e2e/d.json"}, http); err != nil || result.Baseline == nil {
		t.Fatalf("workspace-relative input: %v", err)
	}
	// The CLI's local-operator authority admits an explicit external regular file,
	// but still refuses a final-component symlink.
	if _, err := runLocal(t, dataDir, ws, outside); err != nil {
		t.Fatalf("local operator external file: %v", err)
	}
	if _, err := runLocal(t, dataDir, ws, filepath.Join(repoRoot, "link.json")); !errors.Is(err, ErrInvalidDiagnosticsRequest) {
		t.Fatalf("local operator followed a symlink: %v", err)
	}
}

func TestDiagnosticsPostgresSelectionHasNoLocalFallback(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	dialed := 0
	connectSemanticPostgres = func(context.Context, string) (semanticMaterializationConn, error) {
		dialed++
		return nil, errors.New("forced PostgreSQL failure")
	}
	dsn := "postgres://user:secret@127.0.0.1:1/xmustard"
	_, err := RunDiagnosticsCtx(context.Background(), dataDir, ws, DiagnosticsRequest{InputPath: input, DSN: &dsn}, DiagnosticsRunOptions{InputAuthority: DiagnosticsInputLocalOperator})
	if err == nil || !strings.Contains(err.Error(), "forced PostgreSQL failure") || dialed == 0 {
		t.Fatalf("explicit DSN must use PostgreSQL and surface its failure, got %v (dials %d)", err, dialed)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", ws, "diagnostics", "latest.json")); !os.IsNotExist(err) {
		t.Fatalf("PostgreSQL failure fell back to local storage: %v", err)
	}
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), appSettings{LocalAgentType: "codex", PostgresDSN: &dsn, PostgresSchema: "xmustard"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, ""); err == nil || !strings.Contains(err.Error(), "forced PostgreSQL failure") {
		t.Fatalf("configured DSN read must fail, not fall back: %v", err)
	}
	if _, err := ReadDiagnosticsStatus(dataDir, ws); err == nil {
		t.Fatal("configured DSN status must fail, not fall back")
	}
}

// TestDiagnosticsLockHelper is a child process for the cross-process lock test.
func TestDiagnosticsLockHelper(t *testing.T) {
	dataDir, ws := os.Getenv("XM_DIAG_LOCK_DATA"), os.Getenv("XM_DIAG_LOCK_WS")
	if dataDir == "" {
		t.Skip("helper process only")
	}
	unlock, err := newLocalDiagnosticsStore(dataDir).lockImport(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("locked")
	time.Sleep(time.Minute)
}

func TestLocalDiagnosticsLockIsCrossProcess(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDiagnosticsLockHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "XM_DIAG_LOCK_DATA="+dataDir, "XM_DIAG_LOCK_WS="+ws)
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	deadline := time.Now().Add(20 * time.Second)
	seen := ""
	for !strings.Contains(seen, "locked") && time.Now().Before(deadline) {
		n, err := stdout.Read(buf)
		seen += string(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(seen, "locked") {
		_ = cmd.Process.Kill()
		t.Fatalf("helper did not take the lock: %q", seen)
	}
	if _, err := runLocal(t, dataDir, ws, input); !errors.Is(err, budget.ErrOverloaded) {
		_ = cmd.Process.Kill()
		t.Fatalf("another process's lock must make this import busy, got %v", err)
	}
	_ = cmd.Process.Kill() // the kernel drops a dead holder's flock
	_ = cmd.Wait()
	result := mustRunLocal(t, dataDir, ws, input)
	if latestRunID(t, dataDir, ws) != result.Baseline.DiagnosticRunID {
		t.Fatal("import after the holder died is not visible")
	}
}

// Regression: every read path releases what it reserves (the RSS gate once caught a
// per-GET leak from the latest-pointer read).
func TestLocalDiagnosticsReadsReleaseTransientBytes(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "r.json", localDiagOne))
	before := budget.TransientBytes.InUse()
	for i := 0; i < 20; i++ {
		scope := budget.NewScope(nil)
		ctx := budget.WithScope(context.Background(), scope)
		if _, err := ReadDiagnosticsCtx(ctx, dataDir, ws, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDiagnosticsStatusCtx(ctx, dataDir, ws); err != nil {
			t.Fatal(err)
		}
		scope.Close()
	}
	if _, err := ReadDiagnostics(dataDir, ws, ""); err != nil {
		t.Fatal(err)
	}
	if after := budget.TransientBytes.InUse(); after != before {
		t.Fatalf("reads leaked %d transient bytes", after-before)
	}
}

// Fable P2-1: once Publish returns, the baseline is committed. An activity-log
// failure after that is a warning on a successful result, never a lost run ID.
func TestLocalDiagnosticsActivityFailureKeepsCommittedRunID(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	activity := filepath.Join(dataDir, "workspaces", ws, "activity.jsonl")
	_ = os.Remove(activity)
	if err := os.MkdirAll(activity, 0o755); err != nil { // appends now fail with EISDIR
		t.Fatal(err)
	}
	result, err := runLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "r.json", localDiagOne))
	if err != nil || result.Baseline == nil {
		t.Fatalf("a committed import must succeed despite the activity failure: %v %#v", err, result)
	}
	id := result.Baseline.DiagnosticRunID
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], id) || !strings.Contains(result.Warnings[0], "activity") {
		t.Fatalf("want one activity warning naming %s, got %#v", id, result.Warnings)
	}
	if got := latestRunID(t, dataDir, ws); got != id {
		t.Fatalf("returned run %s is not the committed latest %s", id, got)
	}
}

// Fable P2-2: a directory fsync failure after either rename fails the publish; after
// a failed envelope-directory fsync the pointer is never moved to that envelope.
func TestLocalDiagnosticsDirectoryFsyncFailureIsReturned(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	first := mustRunLocal(t, dataDir, ws, input)
	fsyncFailed := errors.New("simulated directory fsync failure")
	previous := diagnosticsSyncDir
	t.Cleanup(func() { diagnosticsSyncDir = previous })
	for _, failDir := range []string{"runs", "diagnostics"} {
		diagnosticsSyncDir = func(path string) error {
			if filepath.Base(path) == failDir {
				return fsyncFailed
			}
			return previous(path)
		}
		_, err := runLocal(t, dataDir, ws, input)
		diagnosticsSyncDir = previous
		if !errors.Is(err, fsyncFailed) {
			t.Fatalf("%s: want the fsync error, got %v", failDir, err)
		}
		if failDir == "runs" && latestRunID(t, dataDir, ws) != first.Baseline.DiagnosticRunID {
			t.Fatal("pointer moved over an envelope whose rename was not durable")
		}
	}
}

type failingInfoEntry struct{ os.DirEntry }

func (failingInfoEntry) Info() (os.FileInfo, error) { return nil, errors.New("simulated stat failure") }

// Fable P2-3: the pruner fails closed. A corrupt or unreadable pointer, a failed
// listing, a failed entry stat, or a failed temp cleanup refuses the import and
// prunes nothing, so an expired-but-current baseline and its pointer survive.
func TestLocalDiagnosticsPruneFailsClosed(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	good := mustRunLocal(t, dataDir, ws, input)
	store := filepath.Join(dataDir, "workspaces", ws, "diagnostics")
	envelopePath := filepath.Join(store, "runs", good.Baseline.DiagnosticRunID+".json")
	pointerPath := filepath.Join(store, "latest.json")
	goodPointer, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-diagnosticsRetention - time.Hour)
	if err := os.Chtimes(envelopePath, old, old); err != nil { // expired, still latest
		t.Fatal(err)
	}
	assertUntouched := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(envelopePath); err != nil {
			t.Fatalf("the current baseline was pruned: %v", err)
		}
		if got, _ := os.ReadFile(pointerPath); string(got) != string(goodPointer) {
			t.Fatalf("latest pointer changed: %s", got)
		}
	}

	sum := strings.Repeat("a", 64)
	for name, pointer := range map[string]string{
		"malformed":    `{"schema":`,
		"wrong-schema": `{"schema":"other","diagnostic_run_id":"` + good.Baseline.DiagnosticRunID + `","envelope_sha256":"` + sum + `"}`,
		"bad-run-id":   `{"schema":"` + localDiagnosticsPointerSchema + `","diagnostic_run_id":"../x","envelope_sha256":"` + sum + `"}`,
		"short-sha":    `{"schema":"` + localDiagnosticsPointerSchema + `","diagnostic_run_id":"` + good.Baseline.DiagnosticRunID + `","envelope_sha256":"abc"}`,
	} {
		t.Run("corrupt pointer "+name, func(t *testing.T) {
			if err := os.WriteFile(pointerPath, []byte(pointer), 0o644); err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(pointerPath, goodPointer, 0o644)
			if _, err := ReadDiagnostics(dataDir, ws, ""); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
				t.Fatalf("read: want explicit corrupt error, got %v", err)
			}
			if _, err := runLocal(t, dataDir, ws, input); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
				t.Fatalf("import: want refusal with the corrupt error, got %v", err)
			}
			if _, err := os.Stat(envelopePath); err != nil {
				t.Fatalf("pruned on a corrupt pointer: %v", err)
			}
			if got, _ := os.ReadFile(pointerPath); string(got) != pointer {
				t.Fatalf("a refused import rewrote the pointer: %s", got)
			}
		})
	}

	listFailed := errors.New("simulated listing failure")
	previous := diagnosticsReadDir
	t.Cleanup(func() { diagnosticsReadDir = previous })
	for name, readDir := range map[string]func(string) ([]os.DirEntry, error){
		"store listing": func(path string) ([]os.DirEntry, error) {
			if filepath.Base(path) == "diagnostics" {
				return nil, listFailed
			}
			return previous(path)
		},
		"runs listing": func(path string) ([]os.DirEntry, error) {
			if filepath.Base(path) == "runs" {
				return nil, listFailed
			}
			return previous(path)
		},
		"entry stat": func(path string) ([]os.DirEntry, error) {
			entries, err := previous(path)
			for i := range entries {
				entries[i] = failingInfoEntry{entries[i]}
			}
			return entries, err
		},
	} {
		t.Run(name, func(t *testing.T) {
			diagnosticsReadDir = readDir
			defer func() { diagnosticsReadDir = previous }()
			if _, err := runLocal(t, dataDir, ws, input); err == nil {
				t.Fatal("import must be refused")
			}
			assertUntouched(t)
		})
	}

	t.Run("temp cleanup", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		runs := filepath.Join(store, "runs")
		if err := os.WriteFile(filepath.Join(runs, ".orphan.tmp"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(runs, 0o555); err != nil {
			t.Fatal(err)
		}
		_, err := runLocal(t, dataDir, ws, input)
		if err := os.Chmod(runs, 0o755); err != nil {
			t.Fatal(err)
		}
		if err == nil || !strings.Contains(err.Error(), "recover diagnostics temp file") {
			t.Fatalf("want a refused import on a failed temp cleanup, got %v", err)
		}
		assertUntouched(t)
	})

	// With every failure cleared the same import succeeds (and may now prune).
	if next := mustRunLocal(t, dataDir, ws, input); latestRunID(t, dataDir, ws) != next.Baseline.DiagnosticRunID {
		t.Fatal("import after recovery is not visible")
	}
}

// Fable P3-4: bytes streamed for a file that changed mid-read count against the
// identity byte bound, so the bound is on bytes read.
func TestDiagnosticsIdentityChargesChangedFileBytes(t *testing.T) {
	_, _, repoRoot := localDiagFixture(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(repoRoot, name), []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	diagnosticIdentityHook = func(rel string) {
		if rel == "a.txt" {
			later := time.Now().Add(time.Minute)
			_ = os.WriteFile(filepath.Join(repoRoot, rel), []byte("0123456789!"), 0o644)
			_ = os.Chtimes(filepath.Join(repoRoot, rel), later, later)
		}
	}
	t.Cleanup(func() { diagnosticIdentityHook = nil })
	ids, summary, err := observeDiagnosticPathsWithin(context.Background(), budget.NewScope(nil), repoRoot, []string{"a.txt", "b.txt"}, 15)
	if err != nil {
		t.Fatal(err)
	}
	if ids[0].Status != identityChanged {
		t.Fatalf("a.txt: want changed, got %#v", ids[0])
	}
	// 10 bytes of a.txt were read; b.txt (10 bytes) no longer fits the 15-byte bound.
	if ids[1].Status != identityBudgetExceeded || summary.BytesHashed != 0 {
		t.Fatalf("b.txt must exceed the bound after a.txt's bytes are charged: %#v %#v", ids[1], summary)
	}
	// A file exactly the remaining size is still observed; no probe byte is read past it.
	if id, n := hashDiagnosticPath(repoRoot, "b.txt", make([]byte, 4), 10); id.Status != identityObserved || n != 10 {
		t.Fatalf("exact fit: %#v (streamed %d)", id, n)
	}
}

// Fable P3-6: a historical read (no pointer checksum) still detects a flipped byte
// that leaves the envelope parseable and its original bytes intact.
func TestLocalDiagnosticsHistoricalReadVerifiesEnvelopeChecksum(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	first := mustRunLocal(t, dataDir, ws, input)
	mustRunLocal(t, dataDir, ws, input) // first is now historical
	id := first.Baseline.DiagnosticRunID
	if _, err := ReadDiagnostics(dataDir, ws, id); err != nil {
		t.Fatalf("intact historical read: %v", err)
	}
	path := filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs", id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	flipped := strings.Replace(string(raw), `"message":"Example LSP diagnostic."`, `"message":"Example LSP diagnostiX."`, 1)
	if flipped == string(raw) {
		t.Fatal("fixture row message not found in the envelope")
	}
	if err := os.WriteFile(path, []byte(flipped), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiagnostics(dataDir, ws, id); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
		t.Fatalf("flipped historical envelope must be corrupt, got %v", err)
	}
}

// Fable P3-8: the worst-case input the ledger arithmetic assumes (≈1 MiB raw, 2,500
// rows, 2,000 real paths hashed) imports within the 16 MiB per-import ledger.
func TestLocalDiagnosticsWorstCaseFitsTheAdmissionLedger(t *testing.T) {
	dataDir, ws, repoRoot := localDiagFixture(t)
	dir := filepath.Join(repoRoot, "gen")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(strings.Repeat("x", 8<<10))
	items := make([]string, 0, diagnosticsMaxRows)
	for i := 0; i < diagnosticsMaxRows; i++ {
		path := fmt.Sprintf("gen/missing_%04d.py", i)
		if i < diagnosticsMaxIdentityPaths {
			path = fmt.Sprintf("gen/f_%04d.py", i)
			if err := os.WriteFile(filepath.Join(repoRoot, path), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		items = append(items, fmt.Sprintf(`{"path":%q,"message":"%%s","severity":1}`, path))
	}
	// Pad messages so the raw input sits just under the 1 MiB cap.
	skeleton := len("[]") + len(items) - 1
	for _, item := range items {
		skeleton += len(item) - 2
	}
	pad := (diagnosticsMaxRawBytes - 4096 - skeleton) / len(items)
	message := strings.Repeat("m", pad)
	for i := range items {
		items[i] = fmt.Sprintf(items[i], message)
	}
	body := "[" + strings.Join(items, ",") + "]"
	if len(body) > diagnosticsMaxRawBytes || len(body) < diagnosticsMaxRawBytes-8192 {
		t.Fatalf("fixture is %d bytes; want just under %d", len(body), diagnosticsMaxRawBytes)
	}
	result := mustRunLocal(t, dataDir, ws, writeDiagInput(t, t.TempDir(), "worst.json", body))
	identity := result.Baseline.IngestionIdentity
	if result.DiagnosticRows != diagnosticsMaxRows || identity.PathsHashed != diagnosticsMaxIdentityPaths || identity.Status != "partial" {
		t.Fatalf("want %d rows and %d hashed paths, got %d rows, %#v", diagnosticsMaxRows, diagnosticsMaxIdentityPaths, result.DiagnosticRows, identity)
	}
	if identity.BytesHashed != int64(diagnosticsMaxIdentityPaths*len(content)) {
		t.Fatalf("hashed %d bytes", identity.BytesHashed)
	}
}

// P3-8: a timed-out import kills its Rust child; the test observes the child's death
// rather than only a prompt error.
func TestLocalDiagnosticsTimeoutTerminatesTheChild(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := writeDiagInput(t, t.TempDir(), "r.json", localDiagOne)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	fakeCore(t, fmt.Sprintf("echo $$ > %q\nexec sleep 30", pidFile))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := RunDiagnosticsCtx(ctx, dataDir, ws, DiagnosticsRequest{InputPath: input}, DiagnosticsRunOptions{InputAuthority: DiagnosticsInputLocalOperator}); err == nil {
		t.Fatal("want a timeout error")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("fake core never started: %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(raw), &pid); err != nil || pid <= 0 {
		t.Fatalf("bad pid %q", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("core child %d outlived the import deadline", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// Fable P3-5: PostgreSQL publish runs under the import's context, not a fresh one.
func TestPostgresDiagnosticsPublishUsesTheImportContext(t *testing.T) {
	type ctxKey struct{}
	previous := connectSemanticPostgres
	t.Cleanup(func() { connectSemanticPostgres = previous })
	var got context.Context
	connectSemanticPostgres = func(ctx context.Context, _ string) (semanticMaterializationConn, error) {
		got = ctx
		return nil, errors.New("stop after dial")
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "import"))
	store := &postgresDiagnosticsStore{dataDir: t.TempDir(), dsn: "postgres://x", schema: "xmustard"}
	if _, _, err := store.Publish(ctx, &diagnosticsPublication{Plan: &DiagnosticsPlan{}}); err == nil {
		t.Fatal("want the dial error")
	}
	if got == nil || got.Value(ctxKey{}) != "import" {
		t.Fatal("PostgreSQL publish did not derive its context from the import's")
	}
	if deadline, ok := got.Deadline(); !ok || time.Until(deadline) > 60*time.Second {
		t.Fatalf("PostgreSQL publish must keep its 60 s cap, got %v %v", deadline, ok)
	}
	cancel()
	if got.Err() == nil {
		t.Fatal("cancelling the import must cancel the PostgreSQL publish")
	}
}
