package workspaceops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

const diagnosticsReadReservationBytes = 24 << 20

// envelopeHashReader hashes the actual bytes consumed by a JSON decoder. The
// self hash replaces the fixed-position checksum with zeroes, matching publish.
type envelopeHashReader struct {
	ctx    context.Context
	r      io.Reader
	full   hash.Hash
	self   hash.Hash
	n      int64
	prefix []byte
}

func newEnvelopeHashReader(ctx context.Context, r io.Reader) *envelopeHashReader {
	return &envelopeHashReader{
		ctx: ctx, r: io.LimitReader(r, diagnosticsMaxEnvelopeBytes+1),
		full: sha256.New(), self: sha256.New(),
		prefix: make([]byte, 0, len(localDiagnosticsEnvelopeSHAPrefix)+sha256.Size*2),
	}
}

func (r *envelopeHashReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n == 0 {
		return n, err
	}
	chunk := p[:n]
	start := int(r.n)
	r.n += int64(n)
	r.full.Write(chunk)
	if room := cap(r.prefix) - len(r.prefix); room > 0 {
		r.prefix = append(r.prefix, chunk[:min(room, len(chunk))]...)
	}
	off := len(localDiagnosticsEnvelopeSHAPrefix)
	end := off + sha256.Size*2
	if start < off {
		stop := min(n, off-start)
		r.self.Write(chunk[:stop])
	}
	zeroFrom := max(start, off)
	zeroTo := min(start+n, end)
	if zeroFrom < zeroTo {
		var zeroes [sha256.Size * 2]byte
		for i := range zeroes {
			zeroes[i] = '0'
		}
		r.self.Write(zeroes[:zeroTo-zeroFrom])
	}
	if start+n > end {
		from := max(start, end) - start
		r.self.Write(chunk[from:])
	}
	return n, err
}

type scannedDiagnosticsEnvelope struct {
	run           DiagnosticRun
	original      []byte
	fullSHA       string
	rawBeforeRows bool
	sortedRows    bool
	rowCount      int
}

// scanDiagnosticsEnvelope validates a complete envelope without retaining its
// rows. emit is nil on pass one and writes one row at a time on pass two.
func scanDiagnosticsEnvelope(ctx context.Context, file *os.File, workspaceID, runID, wantSHA string, decodeOriginal bool, emit func(*DiagnosticRecord) error) (*scannedDiagnosticsEnvelope, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := newEnvelopeHashReader(ctx, file)
	dec := json.NewDecoder(reader)
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, err)
	}
	result := &scannedDiagnosticsEnvelope{sortedRows: true}
	var schema, selfSHA string
	var seen uint8
	var rawOrder, rowsOrder, order int
	var previousSeverity, previousPath string
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keyToken, err := dec.Token()
		if err != nil {
			return nil, diagnosticsEnvelopeParseError(ctx, runID, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("non-string envelope key"))
		}
		order++
		var bit uint8
		switch key {
		case "schema":
			bit = 1
			err = dec.Decode(&schema)
		case "envelope_sha256":
			bit = 2
			err = dec.Decode(&selfSHA)
		case "run":
			bit = 4
			err = dec.Decode(&result.run)
		case "raw_payload_base64":
			bit = 8
			rawOrder = order
			var encoded string
			err = dec.Decode(&encoded)
			if err == nil && decodeOriginal {
				result.original, err = base64.StdEncoding.DecodeString(encoded)
				if err == nil && (len(result.original) > diagnosticsMaxRawBytes || !json.Valid(result.original)) {
					err = errors.New("invalid or oversized original JSON")
				}
			}
		case "rows":
			bit = 16
			rowsOrder = order
			err = scanDiagnosticsRows(ctx, dec, result, &previousSeverity, &previousPath, emit)
		default:
			err = skipDiagnosticsJSONValue(ctx, dec)
		}
		if err != nil {
			if errors.Is(err, ErrDiagnosticsLimit) {
				return nil, err
			}
			return nil, diagnosticsEnvelopeParseError(ctx, runID, err)
		}
		if bit != 0 {
			if seen&bit != 0 {
				return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("duplicate envelope field"))
			}
			seen |= bit
		}
	}
	endToken, err := dec.Token()
	if err != nil || endToken != json.Delim('}') {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("trailing envelope data"))
	}
	if reader.n > diagnosticsMaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: envelope %s exceeds %d bytes", ErrDiagnosticsLimit, runID, diagnosticsMaxEnvelopeBytes)
	}
	if seen != 31 || schema != localDiagnosticsSchema || result.run.DiagnosticRunID != runID || result.run.WorkspaceID != workspaceID || result.run.ReplayArchive == nil || result.rowCount != result.run.DiagnosticCount {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("envelope metadata or row count is invalid"))
	}
	if len(reader.prefix) != cap(reader.prefix) || !bytes.Equal(reader.prefix[:len(localDiagnosticsEnvelopeSHAPrefix)], []byte(localDiagnosticsEnvelopeSHAPrefix)) || selfSHA != string(reader.prefix[len(localDiagnosticsEnvelopeSHAPrefix):]) || selfSHA != hex.EncodeToString(reader.self.Sum(nil)) {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("envelope self-checksum mismatch"))
	}
	result.fullSHA = hex.EncodeToString(reader.full.Sum(nil))
	if wantSHA != "" && result.fullSHA != wantSHA {
		return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("latest pointer checksum mismatch"))
	}
	if decodeOriginal {
		sum := sha256.Sum256(result.original)
		archive := result.run.ReplayArchive
		if len(result.original) != archive.RawPayloadBytes || hex.EncodeToString(sum[:]) != archive.RawPayloadSHA256 {
			return nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("original payload checksum mismatch"))
		}
	}
	result.rawBeforeRows = rawOrder < rowsOrder
	return result, nil
}

func diagnosticsEnvelopeParseError(ctx context.Context, runID string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: envelope %s failed validation: %v", ErrDiagnosticsStoreCorrupt, runID, err)
}

func scanDiagnosticsRows(ctx context.Context, dec *json.Decoder, result *scannedDiagnosticsEnvelope, previousSeverity, previousPath *string, emit func(*DiagnosticRecord) error) error {
	open, err := dec.Token()
	if err != nil || open != json.Delim('[') {
		return errors.New("rows must be an array")
	}
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row DiagnosticRecord
		if err := dec.Decode(&row); err != nil {
			return err
		}
		if result.rowCount > 0 && (row.Severity < *previousSeverity || row.Severity == *previousSeverity && row.Path < *previousPath) {
			result.sortedRows = false
		}
		*previousSeverity, *previousPath = row.Severity, row.Path
		result.rowCount++
		if result.rowCount > diagnosticsMaxRows {
			return fmt.Errorf("%w: too many stored diagnostics rows", ErrDiagnosticsLimit)
		}
		if emit != nil {
			if err := emit(&row); err != nil {
				return err
			}
		}
	}
	close, err := dec.Token()
	if err != nil || close != json.Delim(']') {
		return errors.New("unterminated rows array")
	}
	return nil
}

func skipDiagnosticsJSONValue(ctx context.Context, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	depth := 0
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		depth = 1
	}
	for depth > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err = dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func openAndScanLocalDiagnostics(ctx context.Context, store *localDiagnosticsStore, workspaceID, runID, wantSHA string) (*os.File, *scannedDiagnosticsEnvelope, error) {
	dir, err := localDiagnosticsDir(workspaceID)
	if err != nil {
		return nil, nil, err
	}
	file, err := openWorkspaceFileBeneath(store.dataDir, filepath.Join(dir, "runs", runID+".json"))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, diagnosticsEnvelopeParseError(ctx, runID, errors.New("envelope is not a regular file"))
	}
	if info.Size() > diagnosticsMaxEnvelopeBytes {
		file.Close()
		return nil, nil, fmt.Errorf("%w: envelope %s exceeds %d bytes", ErrDiagnosticsLimit, runID, diagnosticsMaxEnvelopeBytes)
	}
	scanned, err := scanDiagnosticsEnvelope(ctx, file, workspaceID, runID, wantSHA, true, nil)
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, scanned, nil
}

func compactLocalRawPayload(original []byte) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, original); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func localRunWithPlaceholder(run DiagnosticRun) ([]byte, error) {
	if run.ReplayArchive == nil {
		return nil, errors.New("missing replay archive")
	}
	archive := *run.ReplayArchive
	archive.RawPayload = json.RawMessage("null")
	run.ReplayArchive = &archive
	return json.Marshal(run)
}

// Legacy v2 files are read through the old bounded adapter. The latest file can
// remain legacy indefinitely until another import supersedes it.
func legacyLocalDiagnosticsRead(ctx context.Context, dataDir, workspaceID, runID string) (*DiagnosticsReadResult, error) {
	return ReadDiagnosticsCtx(ctx, dataDir, workspaceID, runID)
}
