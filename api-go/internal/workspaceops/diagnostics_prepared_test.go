package workspaceops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
)

func TestPreparedLocalDiagnosticsMatchesExistingResponse(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	input := strings.Replace(localDiagOne, "Example LSP diagnostic.", "<>&\\u2028\\u2029 quoted \\\" and slash \\\\", 1)
	path := writeDiagInput(t, t.TempDir(), "report.json", input)
	run := mustRunLocal(t, dataDir, ws, path)
	for _, runID := range []string{"", run.Baseline.DiagnosticRunID} {
		t.Run("run_id="+runID, func(t *testing.T) {
			old, err := ReadDiagnosticsCtx(context.Background(), dataDir, ws, runID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, runID)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Close()
			old.GeneratedAt = prepared.Metadata().GeneratedAt
			var want, got bytes.Buffer
			if err := json.NewEncoder(&want).Encode(old); err != nil {
				t.Fatal(err)
			}
			if _, err := prepared.WriteJSON(&got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("prepared diagnostics differ at byte %d\n got: %s\nwant: %s", firstDifferentByte(got.Bytes(), want.Bytes()), got.Bytes(), want.Bytes())
			}
		})
	}
}

func TestPreparedLocalDiagnosticsRejectsCorruptEnvelopeBeforeOutput(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	path := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	run := mustRunLocal(t, dataDir, ws, path)
	envelopePath := filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs", run.Baseline.DiagnosticRunID+".json")
	raw, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(raw, []byte("Example LSP diagnostic."), []byte("Example LSP diagnostiX."), 1)
	if bytes.Equal(raw, changed) {
		t.Fatal("fixture message not found")
	}
	if err := os.WriteFile(envelopePath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, ""); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
		t.Fatalf("prepare corrupt envelope: got %v, want ErrDiagnosticsStoreCorrupt", err)
	}
}

func TestPreparedLocalDiagnosticsDetectsMutationOnSecondPass(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	path := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	run := mustRunLocal(t, dataDir, ws, path)
	prepared, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, run.Baseline.DiagnosticRunID)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	envelopePath := filepath.Join(dataDir, "workspaces", ws, "diagnostics", "runs", run.Baseline.DiagnosticRunID+".json")
	raw, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(raw, []byte("Example LSP diagnostic."), []byte("Example LSP diagnostiX."), 1)
	if err := os.WriteFile(envelopePath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := prepared.WriteJSON(&out); !errors.Is(err, ErrDiagnosticsStoreCorrupt) {
		t.Fatalf("second pass mutation: got %v, want ErrDiagnosticsStoreCorrupt", err)
	}
	if bytes.HasSuffix(out.Bytes(), []byte("}\n")) {
		t.Fatal("mutated envelope produced a complete response")
	}
}

func TestPreparedLocalDiagnosticsReadsLegacyFieldOrder(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	path := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	run := mustRunLocal(t, dataDir, ws, path)
	runID := run.Baseline.DiagnosticRunID
	dir := filepath.Join(dataDir, "workspaces", ws, "diagnostics")
	envelopePath := filepath.Join(dir, "runs", runID+".json")
	current, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	var e localDiagnosticsEnvelope
	if err := json.Unmarshal(current, &e); err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Schema           string                   `json:"schema"`
		EnvelopeSHA256   string                   `json:"envelope_sha256"`
		Run              DiagnosticRun            `json:"run"`
		Rows             []DiagnosticRecord       `json:"rows"`
		RawPayloadBase64 string                   `json:"raw_payload_base64"`
		PathIdentities   []diagnosticPathIdentity `json:"path_identities"`
	}{e.Schema, strings.Repeat("0", sha256.Size*2), e.Run, e.Rows, e.RawPayloadBase64, e.PathIdentities}
	oldBytes, err := json.Marshal(legacy)
	if err != nil || !sealLocalDiagnosticsEnvelope(oldBytes) {
		t.Fatalf("encode legacy envelope: %v", err)
	}
	if err := os.WriteFile(envelopePath, oldBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(oldBytes)
	pointer, err := json.Marshal(localDiagnosticsPointer{Schema: localDiagnosticsPointerSchema, DiagnosticRunID: runID, EnvelopeSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if _, ok := prepared.(*materializedDiagnosticsRead); !ok {
		t.Fatalf("legacy envelope did not use bounded fallback: %T", prepared)
	}
	var out bytes.Buffer
	if _, err := prepared.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Example LSP diagnostic.")) {
		t.Fatalf("legacy response missing row: %s", out.Bytes())
	}
}

func TestPreparedLocalDiagnosticsLimitsConcurrentReaders(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	path := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	mustRunLocal(t, dataDir, ws, path)
	first, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, ""); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("third concurrent read: got %v, want overload", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, "")
	if err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
	third.Close()
}

func TestPreparedLocalDiagnosticsRejectsInsufficientPoolWithoutRetryableOverload(t *testing.T) {
	dataDir, ws, _ := localDiagFixture(t)
	path := writeDiagInput(t, t.TempDir(), "report.json", localDiagOne)
	mustRunLocal(t, dataDir, ws, path)
	oldPool := budget.TransientBytes
	budget.TransientBytes = budget.NewByteBudget(diagnosticsReadReservationBytes - 1)
	t.Cleanup(func() { budget.TransientBytes = oldPool })
	if _, err := PrepareDiagnosticsReadCtx(context.Background(), dataDir, ws, ""); !errors.Is(err, ErrDiagnosticsReadBudgetConfig) || errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("small configured pool: got %v, want non-retryable configuration error", err)
	}
}
