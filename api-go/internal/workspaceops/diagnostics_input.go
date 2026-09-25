package workspaceops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"xmustard/api-go/internal/budget"
)

// Import caps. They are derived from one per-import admission ledger
// (diagnosticsImportAdmissionBytes) so every cap is reachable over HTTP and the CLI,
// which account identically. Worst case charged under the ledger, with R = raw bytes
// and N = normalize stdout ≈ R + 2,500 × ~360 B fixed row overhead ≈ 1.9 MiB:
//
//	capture R (1) + normalize 3N (5.7) + archive 3A, A ≈ R (3.2)
//	+ envelope estimate (≤ 4) + identity buffer and 256 KiB chunk rounding (≤ 1)
//	≈ 15 MiB ≤ 16 MiB.
//
// The ×3 on child stdout is rustcore's capture plus its decode/response reservation.
const (
	diagnosticsMaxRawBytes          = 1 << 20  // 1 MiB of original input
	diagnosticsMaxRows              = 2500     // input items (and therefore normalized rows)
	diagnosticsMaxEnvelopeBytes     = 4 << 20  // 4 MiB serialized local envelope
	diagnosticsImportAdmissionBytes = 16 << 20 // one import's transient ledger
	diagnosticsMaxIdentityPaths     = 2000     // unique row paths hashed at ingestion
	diagnosticsMaxIdentityBytes     = 64 << 20 // bytes streamed through the hasher
	diagnosticsIdentityBufferBytes  = 64 << 10

	diagnosticsImportTimeout    = 120 * time.Second
	diagnosticsNormalizeTimeout = 30 * time.Second
	diagnosticsArchiveTimeout   = 60 * time.Second
	diagnosticsGitProbeTimeout  = 5 * time.Second
)

// ErrDiagnosticsLimit marks an import that breaches a fixed size cap. It is
// deterministic for the input (HTTP 413), unlike budget.ErrOverloaded (503).
var ErrDiagnosticsLimit = errors.New("diagnostics import exceeds a size limit")

// DiagnosticsInputAuthority says which files an import may read. It is a server-side
// option; request JSON cannot select it.
type DiagnosticsInputAuthority int

const (
	// DiagnosticsInputWorkspace confines input to a relative, symlink-free path inside
	// the workspace root (HTTP).
	DiagnosticsInputWorkspace DiagnosticsInputAuthority = iota
	// DiagnosticsInputLocalOperator also admits an explicitly supplied absolute path to
	// a regular file, opened without following a final symlink (CLI).
	DiagnosticsInputLocalOperator
)

// DiagnosticsRunOptions are non-serialized server options for an import.
type DiagnosticsRunOptions struct {
	InputAuthority DiagnosticsInputAuthority
}

// DiagnosticsNormalization reports how much of the input normalized.
type DiagnosticsNormalization struct {
	Status     string `json:"status"` // complete | partial
	Items      int    `json:"items"`
	Normalized int    `json:"normalized"`
	Skipped    int    `json:"skipped"`
}

// DiagnosticsCoverage states what a baseline can and cannot claim.
type DiagnosticsCoverage struct {
	Status string `json:"status"` // known | unknown
	Scope  string `json:"scope"`
	Note   string `json:"note"`
}

// DiagnosticsIngestionIdentity summarizes the bytes observed at import for the paths
// the rows name. It says nothing about when the producer ran (source_revision stays
// "unknown").
type DiagnosticsIngestionIdentity struct {
	Status      string `json:"status"` // matched | changed | partial | unavailable
	PathsTotal  int    `json:"paths_total"`
	PathsHashed int    `json:"paths_hashed"`
	BytesHashed int64  `json:"bytes_hashed"`
	ObservedAt  string `json:"observed_at"`
}

// Per-path identity statuses (also stamped on local rows as identity_status).
const (
	identityObserved       = "observed"
	identityChanged        = "changed_during_import"
	identityOutside        = "path_outside_workspace"
	identityMissing        = "missing"
	identityNotRegular     = "not_regular_file"
	identityBudgetExceeded = "budget_exceeded"
)

type diagnosticPathIdentity struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// capturedDiagnosticsInput is the Go-owned original input: the exact bytes, their
// SHA-256, and the immutable temp file handed to Rust.
type capturedDiagnosticsInput struct {
	Raw        []byte
	SHA256     string
	Path       string // resolved original path (provenance only; never re-read)
	TempPath   string
	Items      int
	removeTemp func()
}

func (c *capturedDiagnosticsInput) Close() {
	if c != nil && c.removeTemp != nil {
		c.removeTemp()
		c.removeTemp = nil
	}
}

// errDiagnosticsInputMissing is the one input failure that stays a plan blocker (as
// before) instead of an error.
var errDiagnosticsInputMissing = errors.New("diagnostics input not found")

// captureDiagnosticsInput opens the input under authority, reads at most
// diagnosticsMaxRawBytes into bytes admitted by scope, validates encoding and shape,
// and writes the bytes to a private read-only temp file for Rust.
func captureDiagnosticsInput(scope *budget.Scope, rootPath, inputPath string, authority DiagnosticsInputAuthority) (*capturedDiagnosticsInput, error) {
	trimmed := strings.TrimSpace(inputPath)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: input_path is required", ErrInvalidDiagnosticsRequest)
	}
	file, resolved, err := openDiagnosticsInput(rootPath, trimmed, authority)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: diagnostics input must be a regular JSON file: %s", ErrInvalidDiagnosticsRequest, trimmed)
	}
	if info.Size() > diagnosticsMaxRawBytes {
		return nil, fmt.Errorf("%w: input is %d bytes; the cap is %d", ErrDiagnosticsLimit, info.Size(), diagnosticsMaxRawBytes)
	}
	raw, err := budget.ReadAllAdmitted(scope, file, diagnosticsMaxRawBytes)
	switch {
	case errors.Is(err, budget.ErrTooLarge):
		return nil, fmt.Errorf("%w: input exceeds %d bytes", ErrDiagnosticsLimit, diagnosticsMaxRawBytes)
	case err != nil:
		return nil, diagnosticsAdmissionError(err)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: diagnostics input is not UTF-8", ErrInvalidDiagnosticsRequest)
	}
	items, err := countDiagnosticItems(raw)
	if err != nil {
		return nil, err
	}
	if items > diagnosticsMaxRows {
		return nil, fmt.Errorf("%w: input has %d diagnostics; the cap is %d", ErrDiagnosticsLimit, items, diagnosticsMaxRows)
	}
	sum := sha256.Sum256(raw)
	captured := &capturedDiagnosticsInput{Raw: raw, SHA256: hex.EncodeToString(sum[:]), Path: resolved, Items: items}
	if err := captured.writeTemp(); err != nil {
		return nil, err
	}
	return captured, nil
}

func (c *capturedDiagnosticsInput) writeTemp() error {
	dir, err := os.MkdirTemp("", "xmustard-diagnostics-*")
	if err != nil {
		return fmt.Errorf("create diagnostics temp dir: %w", err)
	}
	path := filepath.Join(dir, "input.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(c.Raw)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Chmod(path, 0o400)
	}
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("write diagnostics temp input: %w", err)
	}
	c.TempPath = path
	c.removeTemp = func() { os.RemoveAll(dir) }
	return nil
}

func openDiagnosticsInput(rootPath, inputPath string, authority DiagnosticsInputAuthority) (*os.File, string, error) {
	if filepath.IsAbs(inputPath) {
		if authority != DiagnosticsInputLocalOperator {
			return nil, "", fmt.Errorf("%w: input_path must be relative to the workspace root", ErrInvalidDiagnosticsRequest)
		}
		file, err := openRegularNoFollow(inputPath)
		if err != nil {
			return nil, "", diagnosticsInputOpenError(inputPath, err)
		}
		return file, inputPath, nil
	}
	file, err := openWorkspaceFileBeneath(rootPath, inputPath)
	if err != nil {
		return nil, "", diagnosticsInputOpenError(inputPath, err)
	}
	return file, filepath.Join(rootPath, filepath.Clean(inputPath)), nil
}

func diagnosticsInputOpenError(inputPath string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w: %s", ErrInvalidDiagnosticsRequest, errDiagnosticsInputMissing, inputPath)
	}
	if errors.Is(err, errEscape) || errors.Is(err, errAbsPath) || errors.Is(err, errEmptyPath) {
		return fmt.Errorf("%w: input_path escapes the workspace or names a symlink: %s", ErrInvalidDiagnosticsRequest, inputPath)
	}
	return fmt.Errorf("%w: cannot open diagnostics input %s: %v", ErrInvalidDiagnosticsRequest, inputPath, err)
}

// diagnosticsAdmissionError keeps busy (503) and deterministic (413) refusals apart.
func diagnosticsAdmissionError(err error) error {
	if errors.Is(err, budget.ErrAdmissionLimit) {
		return fmt.Errorf("%w: %w", ErrDiagnosticsLimit, err)
	}
	return err
}

// countDiagnosticItems accepts exactly the shapes Rust normalizes — a top-level array,
// or an object with one "diagnostics" array — and returns the item count. It streams
// tokens so validation costs no second copy of the input.
func countDiagnosticItems(raw []byte) (int, error) {
	unsupported := func(detail string) error {
		return fmt.Errorf("%w: unsupported diagnostics shape: %s (want a JSON array or an object with a \"diagnostics\" array)", ErrInvalidDiagnosticsRequest, detail)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil {
		return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
	}
	count := -1
	switch first {
	case json.Delim('['):
		count, err = countArrayItems(dec)
		if err != nil {
			return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
		}
		if count > diagnosticsMaxRows {
			return count, nil
		}
	case json.Delim('{'):
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
			}
			if key != "diagnostics" {
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
				}
				continue
			}
			if count >= 0 {
				return 0, unsupported("duplicate \"diagnostics\" key")
			}
			open, err := dec.Token()
			if err != nil {
				return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
			}
			if open != json.Delim('[') {
				return 0, unsupported("\"diagnostics\" is not an array")
			}
			if count, err = countArrayItems(dec); err != nil {
				return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
			}
			if count > diagnosticsMaxRows {
				return count, nil
			}
		}
		if _, err := dec.Token(); err != nil {
			return 0, fmt.Errorf("%w: decode diagnostics JSON: %v", ErrInvalidDiagnosticsRequest, err)
		}
		if count < 0 {
			return 0, unsupported("object without a \"diagnostics\" array")
		}
	default:
		return 0, unsupported("top-level scalar")
	}
	if _, err := dec.Token(); err != io.EOF {
		return 0, unsupported("trailing data after the JSON value")
	}
	return count, nil
}

func countArrayItems(dec *json.Decoder) (int, error) {
	n := 0
	for dec.More() {
		var item json.RawMessage
		if err := dec.Decode(&item); err != nil {
			return 0, err
		}
		n++
		if n > diagnosticsMaxRows {
			return n, nil // the caller reports the cap; no need to scan further
		}
	}
	_, err := dec.Token() // closing ]
	return n, err
}

// diagnosticsNormalizationFor classifies a normalized batch against the input count.
// All-invalid nonempty input is an error; partial normalization is labelled.
func diagnosticsNormalizationFor(items, normalized int) (*DiagnosticsNormalization, error) {
	if items > 0 && normalized == 0 {
		return nil, fmt.Errorf("%w: none of the %d input diagnostics normalized (each needs a path or URI and a message)", ErrInvalidDiagnosticsRequest, items)
	}
	status := "complete"
	if normalized < items {
		status = "partial"
	}
	return &DiagnosticsNormalization{Status: status, Items: items, Normalized: normalized, Skipped: items - normalized}, nil
}

func diagnosticsCoverageFor(n *DiagnosticsNormalization) *DiagnosticsCoverage {
	if n != nil && n.Status == "complete" {
		return &DiagnosticsCoverage{Status: "known", Scope: "imported_report", Note: "Every input diagnostic normalized. This is not proof the producer ran or that the whole repository is clean."}
	}
	return &DiagnosticsCoverage{Status: "unknown", Scope: "imported_report", Note: "Some input diagnostics were skipped; absence of a diagnostic here does not mean it is absent from the report."}
}

// diagnosticIdentityHook runs after a file is hashed and before it is re-stated; tests
// use it to mutate a file mid-import.
var diagnosticIdentityHook func(relPath string)

// observeDiagnosticPaths hashes each unique in-root row path once, through no-follow
// descriptors, within the path and byte bounds. A path is validated before any stat:
// absolute or escaping paths are marked outside without touching the filesystem.
func observeDiagnosticPaths(ctx context.Context, scope *budget.Scope, rootPath string, paths []string) ([]diagnosticPathIdentity, *DiagnosticsIngestionIdentity, error) {
	return observeDiagnosticPathsWithin(ctx, scope, rootPath, paths, diagnosticsMaxIdentityBytes)
}

// observeDiagnosticPathsWithin charges every byte streamed through the hasher to
// maxBytes, including bytes of a file that changed mid-read, so the bound is on bytes
// read, not only on bytes of files that ended up observed.
func observeDiagnosticPathsWithin(ctx context.Context, scope *budget.Scope, rootPath string, paths []string, maxBytes int64) ([]diagnosticPathIdentity, *DiagnosticsIngestionIdentity, error) {
	summary := &DiagnosticsIngestionIdentity{ObservedAt: nowUTC()}
	out := make([]diagnosticPathIdentity, 0, len(paths))
	if len(paths) > 0 {
		if err := scope.Acquire(diagnosticsIdentityBufferBytes); err != nil {
			return nil, nil, diagnosticsAdmissionError(err)
		}
	}
	buf := make([]byte, diagnosticsIdentityBufferBytes)
	changed, other := 0, 0
	streamed := int64(0)
	for i, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		id := diagnosticPathIdentity{Path: rel}
		switch {
		case !diagnosticPathInsideRoot(rel):
			id.Status = identityOutside
		case i >= diagnosticsMaxIdentityPaths:
			id.Status = identityBudgetExceeded
		default:
			var n int64
			id, n = hashDiagnosticPath(rootPath, rel, buf, maxBytes-streamed)
			streamed += n
		}
		switch id.Status {
		case identityObserved:
			summary.PathsHashed++
			summary.BytesHashed += id.Size
		case identityChanged:
			changed++
		default:
			other++
		}
		out = append(out, id)
	}
	summary.PathsTotal = len(paths)
	switch {
	case changed > 0:
		summary.Status = "changed"
	case len(paths) > 0 && summary.PathsHashed == 0:
		summary.Status = "unavailable"
	case other > 0:
		summary.Status = "partial"
	default:
		summary.Status = "matched" // includes zero paths: nothing to observe, nothing missed
	}
	return out, summary, nil
}

// diagnosticPathInsideRoot is the lexical gate applied before any filesystem access.
func diagnosticPathInsideRoot(rel string) bool {
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || strings.HasPrefix(rel, "file:") {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// hashDiagnosticPath returns the path's identity and the bytes it streamed, which never
// exceed remaining.
func hashDiagnosticPath(rootPath, rel string, buf []byte, remaining int64) (diagnosticPathIdentity, int64) {
	id := diagnosticPathIdentity{Path: rel}
	file, err := openWorkspaceFileBeneath(rootPath, filepath.FromSlash(rel))
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			id.Status = identityMissing
		case errors.Is(err, errEscape):
			id.Status = identityNotRegular // a symlink component: refused, target never read
		default:
			id.Status = identityMissing
		}
		return id, 0
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		id.Status = identityNotRegular
		return id, 0
	}
	if before.Size() > remaining {
		id.Status = identityBudgetExceeded // never a prefix hash presented as identity
		return id, 0
	}
	hasher := sha256.New()
	// One byte past the stat'ed size detects growth, unless that byte is over budget;
	// the second stat still catches growth then.
	limit := before.Size() + 1
	if limit > remaining {
		limit = remaining
	}
	n, err := io.CopyBuffer(hasher, io.LimitReader(file, limit), buf)
	if diagnosticIdentityHook != nil {
		diagnosticIdentityHook(rel)
	}
	after, statErr := file.Stat()
	if err != nil || statErr != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		id.Status = identityChanged
		return id, n
	}
	id.Status = identityObserved
	id.SHA256 = hex.EncodeToString(hasher.Sum(nil))
	id.Size = n
	return id, n
}
