package workspaceops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
)

// DiagnosticsReadMetadata is the response information available after prepare,
// before any response byte is written. Baseline omits its inline raw payload;
// WriteJSON adds it without retaining the full response.
type DiagnosticsReadMetadata struct {
	WorkspaceID string
	Baseline    *DiagnosticRun
	Warnings    []string
	Storage     *DiagnosticsStorage
	GeneratedAt string
}

// PreparedDiagnosticsRead is a one-use read at the store/HTTP seam. Prepare
// performs all checks possible before the first response byte; WriteJSON may
// still fail if the immutable file is externally modified or the client leaves.
// The caller must Close even when WriteJSON fails.
type PreparedDiagnosticsRead interface {
	Metadata() DiagnosticsReadMetadata
	WriteJSON(io.Writer) (int64, error)
	Close() error
}

var diagnosticsReadSlots = make(chan struct{}, 2)

// ErrDiagnosticsReadBudgetConfig is deterministic until the operator raises
// the transient pool; it is not a retryable contention refusal.
var ErrDiagnosticsReadBudgetConfig = errors.New("diagnostics read requires a larger configured transient pool")

func acquireDiagnosticsReadSlot(ctx context.Context) (func(), error) {
	timer := time.NewTimer(diagnosticsLockWait)
	defer timer.Stop()
	select {
	case diagnosticsReadSlots <- struct{}{}:
		return func() { <-diagnosticsReadSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("%w (diagnostics read limit %d reached)", budget.ErrOverloaded, cap(diagnosticsReadSlots))
	}
}

// PrepareDiagnosticsReadCtx selects local or PostgreSQL storage without
// changing the existing direct CLI read path. PostgreSQL retains its current
// materialized query/encoder; the local new-format path streams rows.
func PrepareDiagnosticsReadCtx(ctx context.Context, dataDir, workspaceID, diagnosticRunID string) (PreparedDiagnosticsRead, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	settings, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	store, _ := selectDiagnosticsStore(dataDir, settings, nil, nil)
	if store.Backend() == diagnosticsBackendPostgres {
		release, err := acquireDiagnosticsReadSlot(ctx)
		if err != nil {
			return nil, err
		}
		result, err := ReadDiagnosticsCtx(ctx, dataDir, workspaceID, diagnosticRunID)
		if err != nil {
			release()
			return nil, err
		}
		return &materializedDiagnosticsRead{result: result, release: release}, nil
	}
	local := store.(*localDiagnosticsStore)
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(diagnosticRunID)
	latest := runID == ""
	if !latest && !diagnosticRunIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("%w: diagnostic_run_id must match diag_ and 12 hex digits", ErrInvalidDiagnosticsRequest)
	}
	var pointer *localDiagnosticsPointer
	if latest {
		pointer, err = local.readPointer(dir)
		if err != nil {
			return nil, err
		}
		if pointer == nil {
			storage := &DiagnosticsStorage{Backend: diagnosticsBackendLocal, Status: "no_baseline", StaleReasons: []string{}, FreshnessBasis: diagnosticsFreshnessIngestionIdentity}
			result := &DiagnosticsReadResult{WorkspaceID: workspaceID, Diagnostics: []DiagnosticRecord{}, Warnings: []string{"No diagnostics baseline has been materialized."}, Storage: storage, GeneratedAt: nowUTC()}
			return &materializedDiagnosticsRead{result: result}, nil
		}
		runID = pointer.DiagnosticRunID
	}
	if limit := budget.TransientBytes.Max(); limit > 0 && limit < diagnosticsReadReservationBytes {
		return nil, fmt.Errorf("%w: configured %d bytes, need at least %d", ErrDiagnosticsReadBudgetConfig, limit, diagnosticsReadReservationBytes)
	}
	release, err := acquireDiagnosticsReadSlot(ctx)
	if err != nil {
		return nil, err
	}
	// A dedicated scope releases its reservation before the read slot. A request
	// scope closes only after the handler returns; releasing the slot first would
	// briefly admit a third reader against two still-charged reservations.
	scope := budget.NewScope(nil)
	if err := scope.Acquire(diagnosticsReadReservationBytes); err != nil {
		release()
		scope.Close()
		return nil, err
	}
	cleanup := func(file *os.File) {
		if file != nil {
			file.Close()
		}
		scope.Close()
		release()
	}
	var file *os.File
	var scanned *scannedDiagnosticsEnvelope
	for attempt := 0; attempt < 2; attempt++ {
		wantSHA := ""
		if latest {
			if attempt > 0 {
				pointer, err = local.readPointer(dir)
				if err != nil {
					cleanup(nil)
					return nil, err
				}
				if pointer == nil {
					cleanup(nil)
					return nil, fmt.Errorf("%w: latest pointer disappeared during read", ErrDiagnosticsStoreCorrupt)
				}
			}
			runID, wantSHA = pointer.DiagnosticRunID, pointer.EnvelopeSHA256
		}
		file, scanned, err = openAndScanLocalDiagnostics(ctx, local, workspaceID, runID, wantSHA)
		if latest && errors.Is(err, os.ErrNotExist) {
			continue
		}
		break
	}
	if err != nil {
		cleanup(nil)
		if latest && errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: latest pointer names a missing envelope", ErrDiagnosticsStoreCorrupt)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: diagnostics run not found: %s", ErrInvalidDiagnosticsRequest, runID)
		}
		return nil, err
	}
	if !scanned.rawBeforeRows || !scanned.sortedRows {
		file.Close()
		result, err := legacyLocalDiagnosticsRead(ctx, dataDir, workspaceID, runID)
		if err != nil {
			cleanup(nil)
			return nil, err
		}
		return &materializedDiagnosticsRead{result: result, release: release, scope: scope}, nil
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, diagnosticsGitProbeTimeout)
	worktree := readWorktreeStatusCtx(probeCtx, workspace.RootPath)
	cancelProbe()
	if err := ctx.Err(); err != nil {
		cleanup(file)
		return nil, err
	}
	status, reasons := localDiagnosticsStatus(&scanned.run, worktree)
	storage := &DiagnosticsStorage{Backend: diagnosticsBackendLocal, Status: status, StaleReasons: reasons, FreshnessBasis: diagnosticsFreshnessIngestionIdentity}
	compact, err := compactLocalRawPayload(scanned.original)
	if err != nil {
		cleanup(file)
		return nil, diagnosticsEnvelopeParseError(ctx, runID, err)
	}
	baselineJSON, err := localRunWithPlaceholder(scanned.run)
	if err != nil {
		cleanup(file)
		return nil, err
	}
	const marker = `"raw_payload":null`
	markerStart := bytes.Index(baselineJSON, []byte(marker))
	if markerStart < 0 {
		cleanup(file)
		return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("baseline raw payload marker missing"))
	}
	markerEnd := markerStart + len(marker)
	return &localPreparedDiagnosticsRead{
		ctx: ctx, file: file, workspaceID: workspaceID, runID: runID,
		fullSHA: scanned.fullSHA, baseline: &scanned.run,
		baselineBeforeRaw: baselineJSON[:markerStart+len(`"raw_payload":`)],
		baselineAfterRaw:  baselineJSON[markerEnd:], compactRaw: compact,
		warnings: localDiagnosticsWarnings(&scanned.run), storage: storage,
		generatedAt: nowUTC(), release: release, scope: scope,
	}, nil
}

type materializedDiagnosticsRead struct {
	result  *DiagnosticsReadResult
	release func()
	scope   *budget.Scope
	once    sync.Once
}

func (p *materializedDiagnosticsRead) Metadata() DiagnosticsReadMetadata {
	return DiagnosticsReadMetadata{WorkspaceID: p.result.WorkspaceID, Baseline: p.result.Baseline, Warnings: p.result.Warnings, Storage: p.result.Storage, GeneratedAt: p.result.GeneratedAt}
}

func (p *materializedDiagnosticsRead) WriteJSON(w io.Writer) (int64, error) {
	counter := &diagnosticsCountingWriter{w: w}
	err := json.NewEncoder(counter).Encode(p.result)
	return counter.n, err
}

func (p *materializedDiagnosticsRead) Close() error {
	p.once.Do(func() {
		if p.scope != nil {
			p.scope.Close()
		}
		if p.release != nil {
			p.release()
		}
	})
	return nil
}

type diagnosticsCountingWriter struct {
	w io.Writer
	n int64
}

func (w *diagnosticsCountingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

type localPreparedDiagnosticsRead struct {
	ctx               context.Context
	file              *os.File
	workspaceID       string
	runID             string
	fullSHA           string
	baseline          *DiagnosticRun
	baselineBeforeRaw []byte
	baselineAfterRaw  []byte
	compactRaw        []byte
	warnings          []string
	storage           *DiagnosticsStorage
	generatedAt       string
	release           func()
	scope             *budget.Scope
	once              sync.Once
	closeErr          error
}

func (p *localPreparedDiagnosticsRead) Metadata() DiagnosticsReadMetadata {
	return DiagnosticsReadMetadata{WorkspaceID: p.workspaceID, Baseline: p.baseline, Warnings: p.warnings, Storage: p.storage, GeneratedAt: p.generatedAt}
}

func (p *localPreparedDiagnosticsRead) WriteJSON(w io.Writer) (int64, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	stream := newJSONStream(w)
	stream.rawString(`{"workspace_id":`)
	stream.str(p.workspaceID)
	stream.rawString(`,"baseline":`)
	stream.raw(p.baselineBeforeRaw)
	stream.htmlEscapedRaw(p.compactRaw)
	stream.raw(p.baselineAfterRaw)
	stream.rawString(`,"diagnostics":[`)
	count := 0
	passTwo, err := scanDiagnosticsEnvelope(p.ctx, p.file, p.workspaceID, p.runID, p.fullSHA, false, func(row *DiagnosticRecord) error {
		if count > 0 {
			stream.byte(',')
		}
		count++
		if err := stream.diagnosticRecord(row); err != nil {
			return err
		}
		return stream.err
	})
	if err != nil {
		return stream.written, err
	}
	if passTwo.fullSHA != p.fullSHA || !passTwo.rawBeforeRows || !passTwo.sortedRows || count != p.baseline.DiagnosticCount {
		return stream.written, fmt.Errorf("%w: envelope changed during diagnostics response", ErrDiagnosticsStoreCorrupt)
	}
	stream.rawString(`],"warnings":`)
	if err := stream.marshal(p.warnings); err != nil {
		return stream.written, err
	}
	stream.rawString(`,"storage":`)
	if err := stream.marshal(p.storage); err != nil {
		return stream.written, err
	}
	stream.rawString(`,"generated_at":`)
	stream.str(p.generatedAt)
	stream.rawString("}\n")
	err = stream.flush()
	return stream.written, err
}

func (p *localPreparedDiagnosticsRead) Close() error {
	p.once.Do(func() {
		p.closeErr = p.file.Close()
		p.scope.Close()
		p.release()
	})
	return p.closeErr
}
