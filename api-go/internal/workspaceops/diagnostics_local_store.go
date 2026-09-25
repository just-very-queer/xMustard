package workspaceops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"xmustard/api-go/internal/budget"
)

// Local diagnostics storage: one versioned envelope per baseline under
// <dataDir>/workspaces/{ws}/diagnostics/runs/, plus a latest pointer. Writers hold a
// cross-process flock on diagnostics/.lock; readers are lock-free and only ever see a
// complete old or new envelope (temp write, fsync, atomic rename, directory fsync).
const (
	localDiagnosticsSchema         = "xmustard.diagnostics.local.v2"
	localDiagnosticsPointerSchema  = "xmustard.diagnostics.latest.v1"
	diagnosticsRetention           = 24 * time.Hour
	diagnosticsStoreQuotaBytes     = 256 << 20
	diagnosticsStoreQuotaBaselines = 100
	diagnosticsLockWait            = 2 * time.Second
	diagnosticsPointerMaxBytes     = 4 << 10
)

var (
	diagnosticRunIDPattern       = regexp.MustCompile(`^diag_[0-9a-f]{12}$`)
	diagnosticsWorkspaceIDFormat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

	// ErrDiagnosticsQuota: the store is full of unexpired baselines (HTTP 409).
	ErrDiagnosticsQuota = errors.New("diagnostics store quota reached")
	// ErrDiagnosticsStoreCorrupt: a stored pointer or envelope failed validation.
	ErrDiagnosticsStoreCorrupt = errors.New("diagnostics store is corrupt")
	// ErrDiagnosticsStoreUnsupported: no safe cross-process lock on this platform.
	ErrDiagnosticsStoreUnsupported = errors.New("local diagnostics storage requires a Unix platform with flock")
)

// diagnosticsPublishHook lets tests simulate a crash at a publish stage: a non-nil
// error aborts the publish right there, leaving on disk whatever a crash would.
var diagnosticsPublishHook func(stage string) error

// Test seams for filesystem failures the store must not ignore.
var (
	diagnosticsReadDir = os.ReadDir
	diagnosticsSyncDir = func(path string) error {
		d, err := os.Open(path)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
)

// The envelope carries its own SHA-256 so historical reads (which have no pointer
// checksum) still detect corruption. Schema and checksum are the first two fields, so
// the checksum sits at a fixed offset; it is computed with those 64 bytes set to '0'.
const localDiagnosticsEnvelopeSHAPrefix = `{"schema":"` + localDiagnosticsSchema + `","envelope_sha256":"`

type localDiagnosticsEnvelope struct {
	Schema           string                   `json:"schema"`
	EnvelopeSHA256   string                   `json:"envelope_sha256"`
	Run              DiagnosticRun            `json:"run"`
	RawPayloadBase64 string                   `json:"raw_payload_base64"`
	Rows             []DiagnosticRecord       `json:"rows"`
	PathIdentities   []diagnosticPathIdentity `json:"path_identities"`
}

type localDiagnosticsPointer struct {
	Schema          string `json:"schema"`
	DiagnosticRunID string `json:"diagnostic_run_id"`
	EnvelopeSHA256  string `json:"envelope_sha256"`
}

type localDiagnosticsStore struct {
	dataDir string
	// held is set while this instance holds the workspace lock for a whole import.
	held map[string]bool
	// cache is the envelope last read, so Latest followed by Rows reads one version.
	cache *localDiagnosticsEnvelope
}

func newLocalDiagnosticsStore(dataDir string) *localDiagnosticsStore {
	return &localDiagnosticsStore{dataDir: dataDir, held: map[string]bool{}}
}

func (s *localDiagnosticsStore) Backend() string { return diagnosticsBackendLocal }

func localDiagnosticsDir(workspaceID string) (string, error) {
	if !diagnosticsWorkspaceIDFormat.MatchString(workspaceID) {
		return "", fmt.Errorf("%w: invalid workspace id", ErrInvalidDiagnosticsRequest)
	}
	return filepath.Join("workspaces", workspaceID, "diagnostics"), nil
}

// lockImport takes the workspace's cross-process lock for the rest of an import.
func (s *localDiagnosticsStore) lockImport(ctx context.Context, workspaceID string) (func(), error) {
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, err
	}
	lockFile, _, err := createWorkspaceFileBeneath(s.dataDir, filepath.Join(dir, ".lock"), true)
	if err != nil {
		return nil, fmt.Errorf("open diagnostics lock: %w", err)
	}
	unlock, err := lockDiagnosticsStore(ctx, lockFile, diagnosticsLockWait)
	if err != nil {
		lockFile.Close()
		return nil, err
	}
	s.held[workspaceID] = true
	return func() {
		delete(s.held, workspaceID)
		unlock()
		lockFile.Close()
	}, nil
}

// Publish writes one baseline: lock → recover/prune → quota → envelope → pointer.
func (s *localDiagnosticsStore) Publish(ctx context.Context, pub *diagnosticsPublication) (*DiagnosticRun, int, error) {
	plan := pub.Plan
	if !s.held[plan.WorkspaceID] {
		unlock, err := s.lockImport(ctx, plan.WorkspaceID)
		if err != nil {
			return nil, 0, err
		}
		defer unlock()
	}
	dir, err := localDiagnosticsDir(plan.WorkspaceID)
	if err != nil {
		return nil, 0, err
	}
	identities, identity, err := observeDiagnosticPaths(ctx, pub.Scope, plan.RootPath, diagnosticPaths(plan.NormalizedBatch.Diagnostics))
	if err != nil {
		return nil, 0, err
	}
	envelope := buildLocalDiagnosticsEnvelope(pub, identities, identity)
	if err := pub.Scope.Acquire(estimateLocalEnvelopeBytes(envelope)); err != nil {
		return nil, 0, diagnosticsAdmissionError(err)
	}
	envelope.EnvelopeSHA256 = strings.Repeat("0", sha256.Size*2)
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, 0, fmt.Errorf("encode diagnostics envelope: %w", err)
	}
	if !sealLocalDiagnosticsEnvelope(payload) {
		return nil, 0, fmt.Errorf("encode diagnostics envelope: unexpected field layout")
	}
	if len(payload) > diagnosticsMaxEnvelopeBytes {
		return nil, 0, fmt.Errorf("%w: diagnostics envelope is %d bytes; the cap is %d", ErrDiagnosticsLimit, len(payload), diagnosticsMaxEnvelopeBytes)
	}
	if err := s.recoverAndCheckQuota(dir, int64(len(payload))); err != nil {
		return nil, 0, err
	}
	runID := envelope.Run.DiagnosticRunID
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.writeAtomic(ctx, filepath.Join(dir, "runs"), runID+".json", payload, "envelope"); err != nil {
		return nil, 0, err
	}
	sum := sha256.Sum256(payload)
	pointer, err := json.Marshal(localDiagnosticsPointer{Schema: localDiagnosticsPointerSchema, DiagnosticRunID: runID, EnvelopeSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return nil, 0, fmt.Errorf("encode diagnostics pointer: %w", err)
	}
	// Past the envelope rename the baseline is committed; the pointer update is not
	// abandoned on cancellation, so readers move to the new baseline promptly.
	if err := s.writeAtomic(context.Background(), dir, "latest.json", pointer, "pointer"); err != nil {
		return nil, 0, err
	}
	run := envelope.Run
	run.ReplayArchive = withLocalRawPayload(run.ReplayArchive, pub.Input.Raw)
	return &run, len(envelope.Rows), nil
}

func buildLocalDiagnosticsEnvelope(pub *diagnosticsPublication, identities []diagnosticPathIdentity, identity *DiagnosticsIngestionIdentity) *localDiagnosticsEnvelope {
	plan := pub.Plan
	createdAt := time.Now().UTC()
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	runID := "diag_" + hashID(plan.WorkspaceID, firstNonEmptyPtr(plan.BatchFingerprint), createdAt.Format(time.RFC3339Nano), hex.EncodeToString(nonce))[:12]
	byPath := map[string]diagnosticPathIdentity{}
	for _, item := range identities {
		byPath[item.Path] = item
	}
	rows := make([]DiagnosticRecord, 0, len(plan.NormalizedBatch.Diagnostics))
	for _, row := range plan.NormalizedBatch.Diagnostics {
		record := DiagnosticRecord{
			WorkspaceID:      row.WorkspaceID,
			DiagnosticRunID:  runID,
			Path:             row.Path,
			RangeStartLine:   row.RangeStartLine,
			RangeStartColumn: row.RangeStartColumn,
			RangeEndLine:     row.RangeEndLine,
			RangeEndColumn:   row.RangeEndColumn,
			Severity:         row.Severity,
			Message:          row.Message,
			SourceKind:       row.SourceKind,
			SourceName:       row.SourceName,
			RuleCode:         row.RuleCode,
			Fingerprint:      row.Fingerprint,
			HeadSHA:          plan.HeadSHA,
			LinkStatus:       diagnosticLinkStatusSymbolsUnavailable,
			IdentityStatus:   byPath[row.Path].Status,
			GeneratedAt:      row.GeneratedAt,
		}
		if sha := byPath[row.Path].SHA256; sha != "" {
			record.ContentHash = &sha
		}
		rows = append(rows, record)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Severity != rows[j].Severity {
			return rows[i].Severity < rows[j].Severity
		}
		return rows[i].Path < rows[j].Path
	})
	archive := *plan.ReplayArchive
	archive.RawPayload = nil // the exact bytes live in raw_payload_base64
	expiresAt := createdAt.Add(diagnosticsRetention)
	return &localDiagnosticsEnvelope{
		Schema: localDiagnosticsSchema,
		Run: DiagnosticRun{
			DiagnosticRunID:   runID,
			WorkspaceID:       plan.WorkspaceID,
			IssueID:           trimOptional(plan.IssueID),
			RunID:             trimOptional(plan.RunID),
			SourceKind:        plan.SourceKind,
			SourceName:        plan.SourceName,
			BatchFingerprint:  firstNonEmptyPtr(plan.BatchFingerprint),
			ReplayArchive:     &archive,
			HeadSHA:           plan.HeadSHA,
			DirtyFiles:        plan.DirtyFiles,
			WorktreeDirty:     plan.WorktreeDirty,
			DiagnosticCount:   len(rows),
			SeverityCounts:    plan.SeverityCounts,
			InputPath:         plan.InputPath,
			PostgresSchema:    "",
			CreatedAt:         createdAt.Format(time.RFC3339Nano),
			StorageBackend:    diagnosticsBackendLocal,
			SourceRevision:    "unknown",
			FreshnessBasis:    diagnosticsFreshnessIngestionIdentity,
			Normalization:     plan.Normalization,
			Coverage:          diagnosticsCoverageFor(plan.Normalization),
			IngestionIdentity: identity,
			Retention:         &DiagnosticsRetention{ExpiresAt: expiresAt.Format(time.RFC3339), Promise: "full envelope and exact original bytes kept for at least 24h"},
		},
		Rows:             rows,
		RawPayloadBase64: base64.StdEncoding.EncodeToString(pub.Input.Raw),
		PathIdentities:   identities,
	}
}

// sealLocalDiagnosticsEnvelope writes the envelope's self-checksum in place. The
// payload must carry 64 '0's at the checksum offset.
func sealLocalDiagnosticsEnvelope(payload []byte) bool {
	off := len(localDiagnosticsEnvelopeSHAPrefix)
	if len(payload) < off+sha256.Size*2 || string(payload[:off]) != localDiagnosticsEnvelopeSHAPrefix {
		return false
	}
	sum := sha256.Sum256(payload)
	hex.Encode(payload[off:off+sha256.Size*2], sum[:])
	return true
}

// localDiagnosticsEnvelopeIntact recomputes the self-checksum without copying raw.
func localDiagnosticsEnvelopeIntact(raw []byte) bool {
	off := len(localDiagnosticsEnvelopeSHAPrefix)
	end := off + sha256.Size*2
	if len(raw) < end || string(raw[:off]) != localDiagnosticsEnvelopeSHAPrefix {
		return false
	}
	want := make([]byte, sha256.Size)
	if _, err := hex.Decode(want, raw[off:end]); err != nil {
		return false
	}
	hasher := sha256.New()
	hasher.Write(raw[:off])
	hasher.Write([]byte(strings.Repeat("0", sha256.Size*2)))
	hasher.Write(raw[end:])
	return string(hasher.Sum(nil)) == string(want)
}

// estimateLocalEnvelopeBytes bounds the encoder's buffer from above so it can be
// admitted before json.Marshal allocates it.
func estimateLocalEnvelopeBytes(e *localDiagnosticsEnvelope) int64 {
	n := int64(len(e.RawPayloadBase64)) + 16<<10
	for _, row := range e.Rows {
		n += int64(2*(len(row.Path)+len(row.Message)) + 640)
		if row.RuleCode != nil {
			n += int64(2 * len(*row.RuleCode))
		}
	}
	for _, item := range e.PathIdentities {
		n += int64(2*len(item.Path) + 160)
	}
	if a := e.Run.ReplayArchive; a != nil {
		if b, err := json.Marshal(a.ServerProvenance); err == nil {
			n += int64(len(b))
		}
	}
	return n
}

// recoverAndCheckQuota runs under the lock: it removes temp files a crashed writer
// left, prunes expired envelopes the pointer does not name, and refuses a new
// baseline that would exceed the count or byte quota. Unexpired originals are never
// evicted to make room. It fails closed: if the pointer, a listing, an entry's size or
// a temp cleanup cannot be read or done, nothing is pruned and the import is refused,
// since pruning or counting on partial knowledge could drop the current baseline or
// under-count the quota.
func (s *localDiagnosticsStore) recoverAndCheckQuota(dir string, incoming int64) error {
	root := filepath.Join(s.dataDir, dir)
	for _, sub := range []string{root, filepath.Join(root, "runs")} {
		entries, err := diagnosticsReadDir(sub)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("list diagnostics store for recovery: %w", err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".tmp") {
				continue
			}
			if err := os.Remove(filepath.Join(sub, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("recover diagnostics temp file: %w", err)
			}
		}
	}
	pointer, err := s.readPointer(dir)
	if err != nil {
		return fmt.Errorf("refusing to prune without the latest pointer: %w", err)
	}
	latestID := ""
	if pointer != nil {
		latestID = pointer.DiagnosticRunID
	}
	entries, err := diagnosticsReadDir(filepath.Join(root, "runs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("list diagnostics store: %w", err)
	}
	now := time.Now()
	count, total := 0, int64(0)
	var earliest time.Time
	for _, entry := range entries {
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue // removed since the listing: it holds no quota
		}
		if err != nil {
			return fmt.Errorf("stat diagnostics store entry %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		expires := info.ModTime().Add(diagnosticsRetention)
		if id != latestID && now.After(expires) {
			if os.Remove(filepath.Join(root, "runs", entry.Name())) == nil {
				continue
			}
		}
		count++
		total += info.Size()
		if id != latestID && (earliest.IsZero() || expires.Before(earliest)) {
			earliest = expires
		}
	}
	if count+1 > diagnosticsStoreQuotaBaselines || total+incoming > diagnosticsStoreQuotaBytes {
		when := "no prunable baseline"
		if !earliest.IsZero() {
			when = "earliest prunable baseline expires " + earliest.UTC().Format(time.RFC3339)
		}
		return fmt.Errorf("%w: %d baselines, %d bytes (limits %d baselines, %d bytes); %s", ErrDiagnosticsQuota, count, total, diagnosticsStoreQuotaBaselines, diagnosticsStoreQuotaBytes, when)
	}
	return nil
}

// writeAtomic writes data to dir/name through a unique temp file created no-follow
// beneath the data dir: write, fsync, rename, fsync the directory.
func (s *localDiagnosticsStore) writeAtomic(ctx context.Context, dir, name string, data []byte, stage string) error {
	nonce := make([]byte, 6)
	_, _ = rand.Read(nonce)
	tmpRel := filepath.Join(dir, "."+name+"."+hex.EncodeToString(nonce)+".tmp")
	file, _, err := createWorkspaceFileBeneath(s.dataDir, tmpRel, false)
	if err != nil || file == nil {
		return fmt.Errorf("create diagnostics %s temp file: %w", stage, err)
	}
	tmpAbs := filepath.Join(s.dataDir, tmpRel)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = os.Remove(tmpAbs)
		return fmt.Errorf("write diagnostics %s: %w", stage, err)
	}
	if diagnosticsPublishHook != nil {
		if err := diagnosticsPublishHook("before_" + stage + "_rename"); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpAbs, filepath.Join(s.dataDir, dir, name)); err != nil {
		_ = os.Remove(tmpAbs)
		return fmt.Errorf("publish diagnostics %s: %w", stage, err)
	}
	// The rename is durable only once its directory is; the pointer must never be
	// published over an envelope rename that might not survive a crash.
	if err := diagnosticsSyncDir(filepath.Join(s.dataDir, dir)); err != nil {
		return fmt.Errorf("fsync diagnostics %s directory: %w", stage, err)
	}
	return nil
}

func (s *localDiagnosticsStore) readPointer(dir string) (*localDiagnosticsPointer, error) {
	file, err := openWorkspaceFileBeneath(s.dataDir, filepath.Join(dir, "latest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: open latest pointer: %v", ErrDiagnosticsStoreCorrupt, err)
	}
	defer file.Close()
	scope := budget.NewScope(nil) // the pointer is tiny; release it as soon as it is read
	defer scope.Close()
	raw, err := budget.ReadAllAdmitted(scope, file, diagnosticsPointerMaxBytes)
	if errors.Is(err, budget.ErrOverloaded) {
		return nil, fmt.Errorf("read latest pointer: %w", err) // transient: 503, not corrupt
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read latest pointer: %v", ErrDiagnosticsStoreCorrupt, err)
	}
	var pointer localDiagnosticsPointer
	if err := json.Unmarshal(raw, &pointer); err != nil || pointer.Schema != localDiagnosticsPointerSchema || !diagnosticRunIDPattern.MatchString(pointer.DiagnosticRunID) || len(pointer.EnvelopeSHA256) != 64 {
		return nil, fmt.Errorf("%w: latest pointer is invalid", ErrDiagnosticsStoreCorrupt)
	}
	return &pointer, nil
}

// readEnvelope loads and validates one envelope. It returns os.ErrNotExist when the
// file is absent so callers can tell a missing run from a corrupt one.
func (s *localDiagnosticsStore) readEnvelope(ctx context.Context, workspaceID, dir, runID, wantSHA string) (*localDiagnosticsEnvelope, error) {
	if s.cache != nil && s.cache.Run.DiagnosticRunID == runID && s.cache.Run.WorkspaceID == workspaceID {
		return s.cache, nil
	}
	file, err := openWorkspaceFileBeneath(s.dataDir, filepath.Join(dir, "runs", runID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("%w: open envelope %s: %v", ErrDiagnosticsStoreCorrupt, runID, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > diagnosticsMaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: envelope %s is not a regular file within %d bytes", ErrDiagnosticsStoreCorrupt, runID, diagnosticsMaxEnvelopeBytes)
	}
	scope, owned := budget.ScopeFor(ctx)
	if owned {
		defer scope.Close()
	}
	// the file read, plus its decode and the response built from it
	if err := scope.Acquire(2 * info.Size()); err != nil {
		return nil, err
	}
	raw, err := budget.ReadAllAdmitted(scope, file, diagnosticsMaxEnvelopeBytes)
	if err != nil {
		if errors.Is(err, budget.ErrTooLarge) {
			return nil, fmt.Errorf("%w: envelope %s exceeds %d bytes", ErrDiagnosticsStoreCorrupt, runID, diagnosticsMaxEnvelopeBytes)
		}
		return nil, err
	}
	if wantSHA != "" {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != wantSHA {
			return nil, fmt.Errorf("%w: envelope %s does not match the latest pointer checksum", ErrDiagnosticsStoreCorrupt, runID)
		}
	}
	if !localDiagnosticsEnvelopeIntact(raw) {
		return nil, fmt.Errorf("%w: envelope %s does not match its own checksum", ErrDiagnosticsStoreCorrupt, runID)
	}
	var envelope localDiagnosticsEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: decode envelope %s: %v", ErrDiagnosticsStoreCorrupt, runID, err)
	}
	original, err := base64.StdEncoding.DecodeString(envelope.RawPayloadBase64)
	archive := envelope.Run.ReplayArchive
	switch {
	case err != nil, envelope.Schema != localDiagnosticsSchema, envelope.Run.DiagnosticRunID != runID,
		envelope.Run.WorkspaceID != workspaceID, archive == nil,
		len(envelope.Rows) != envelope.Run.DiagnosticCount:
		return nil, fmt.Errorf("%w: envelope %s failed validation", ErrDiagnosticsStoreCorrupt, runID)
	}
	sum := sha256.Sum256(original)
	if hex.EncodeToString(sum[:]) != archive.RawPayloadSHA256 || len(original) != archive.RawPayloadBytes {
		return nil, fmt.Errorf("%w: envelope %s original bytes do not match their checksum", ErrDiagnosticsStoreCorrupt, runID)
	}
	envelope.Run.ReplayArchive = withLocalRawPayload(archive, original)
	envelope.RawPayloadBase64 = ""
	s.cache = &envelope
	return &envelope, nil
}

// withLocalRawPayload exposes the exact original bytes as the archive's raw_payload.
func withLocalRawPayload(archive *DiagnosticReplayArchive, raw []byte) *DiagnosticReplayArchive {
	if archive == nil {
		return nil
	}
	copied := *archive
	copied.RawPayload = json.RawMessage(raw)
	return &copied
}

// Latest reads the pointer then its envelope. A concurrent prune can race the pair,
// so it is retried once; a pointer naming a missing envelope is then an explicit
// error, never "no baseline".
func (s *localDiagnosticsStore) Latest(ctx context.Context, workspaceID string) (*DiagnosticRun, error) {
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		pointer, err := s.readPointer(dir)
		if err != nil || pointer == nil {
			return nil, err
		}
		envelope, err := s.readEnvelope(ctx, workspaceID, dir, pointer.DiagnosticRunID, pointer.EnvelopeSHA256)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		run := envelope.Run
		return &run, nil
	}
	return nil, fmt.Errorf("%w: latest pointer names a missing envelope", ErrDiagnosticsStoreCorrupt)
}

func (s *localDiagnosticsStore) ByID(ctx context.Context, workspaceID, runID string) (*DiagnosticRun, error) {
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if !diagnosticRunIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("%w: diagnostic_run_id must match diag_ and 12 hex digits", ErrInvalidDiagnosticsRequest)
	}
	envelope, err := s.readEnvelope(ctx, workspaceID, dir, runID, "")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run := envelope.Run
	return &run, nil
}

func (s *localDiagnosticsStore) Rows(ctx context.Context, workspaceID, runID string) ([]DiagnosticRecord, error) {
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, err
	}
	envelope, err := s.readEnvelope(ctx, workspaceID, dir, runID, "")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: envelope %s disappeared", ErrDiagnosticsStoreCorrupt, runID)
		}
		return nil, err
	}
	rows := make([]DiagnosticRecord, len(envelope.Rows)) // never nil: [] on the wire
	copy(rows, envelope.Rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Severity != rows[j].Severity {
			return rows[i].Severity < rows[j].Severity
		}
		return rows[i].Path < rows[j].Path
	})
	return rows, nil
}

// localDiagnosticsStatus maps a local baseline to the local label set. Local imports
// never claim fresh or dirty_provisional: the report's production time is unknown.
//
//	no_baseline — nothing imported
//	stale       — HEAD moved since import (head_moved), or a row's file changed while
//	              it was being hashed (ingestion_identity_changed)
//	available   — otherwise; dirty_worktree, ingestion_identity_partial,
//	              ingestion_identity_unavailable and normalization_partial are reasons
//	              that qualify it without making it stale
func localDiagnosticsStatus(run *DiagnosticRun, worktree *WorktreeStatus) (string, []string) {
	if run == nil {
		return "no_baseline", []string{}
	}
	reasons := []string{}
	stale := false
	if worktree != nil && worktree.HeadSHA != nil && (run.HeadSHA == nil || *run.HeadSHA != *worktree.HeadSHA) {
		reasons = append(reasons, "head_moved")
		stale = true
	}
	if worktree != nil && worktree.DirtyFiles > 0 {
		reasons = append(reasons, "dirty_worktree")
	}
	if run.IngestionIdentity != nil {
		switch run.IngestionIdentity.Status {
		case "changed":
			reasons = append(reasons, "ingestion_identity_changed")
			stale = true
		case "partial":
			reasons = append(reasons, "ingestion_identity_partial")
		case "unavailable":
			reasons = append(reasons, "ingestion_identity_unavailable")
		}
	}
	if run.Normalization != nil && run.Normalization.Status == "partial" {
		reasons = append(reasons, "normalization_partial")
	}
	if stale {
		return "stale", reasons
	}
	return "available", reasons
}
