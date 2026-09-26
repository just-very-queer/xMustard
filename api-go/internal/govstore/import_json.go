package govstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Legacy JSON migration. The API currently keeps a workspace's memories in
// context_entries.json: one array rewritten in full on every mutation. The importer
// streams that array into the store in one transaction (all or nothing) and is
// idempotent. The exporter writes the same shape back, which is the rollback path
// for the WS-12 cutover.
//
// This is a migration of a workspace's own store, not a foreign import: promoted
// entries stay promoted. Their verification basis is re-derived from the votes, and
// a recorded peer_verified label that the votes do not support is downgraded, never
// kept. Foreign imports (PAR-GOV-18) must land pending instead and use another path.

// legacyMissingSentinel is how the legacy store baselines a path that was missing when
// the entry was verified.
const legacyMissingSentinel = "\x00missing"

// LegacyVerification mirrors workspaceops.ContextVerification.
type LegacyVerification struct {
	Agent   string `json:"agent"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
	At      string `json:"at"`
}

// LegacyEntry mirrors workspaceops.ContextEntry (the w0-kernel shape, which adds
// require_verification and verification_mode).
type LegacyEntry struct {
	ID                    string               `json:"id"`
	WorkspaceID           string               `json:"workspace_id"`
	Title                 string               `json:"title"`
	Content               string               `json:"content"`
	Source                string               `json:"source"`
	Permission            string               `json:"permission"`
	Status                string               `json:"status"`
	Promoted              bool                 `json:"promoted"`
	Verifications         []LegacyVerification `json:"verifications"`
	RequiredVerifications int                  `json:"required_verifications"`
	RequireVerification   bool                 `json:"require_verification,omitempty"`
	VerificationMode      string               `json:"verification_mode"`
	CreatedAt             string               `json:"created_at"`
	UpdatedAt             string               `json:"updated_at"`
	Paths                 []string             `json:"paths,omitempty"`
	PathHashes            map[string]string    `json:"path_hashes,omitempty"`
	SearchTokens          []string             `json:"search_tokens,omitempty"`
	// ContentHash is the legacy 64-bit cache-file name. It is checked on import and
	// never exported.
	ContentHash string `json:"content_hash,omitempty"`
	// ContentDigest is the full SHA-256 of Content. It is checked on import when
	// present and always exported.
	ContentDigest string `json:"content_digest,omitempty"`
	// Stale and StalePaths are computed at read time by the legacy store and ignored.
	Stale      bool     `json:"stale,omitempty"`
	StalePaths []string `json:"stale_paths,omitempty"`
}

// ImportOptions tunes an import.
type ImportOptions struct {
	// Principal is recorded on the import events. Default "xmustard-import".
	Principal string
	// Threshold is the workspace verification threshold, used to label promoted
	// entries the way the w0-kernel verificationMode does. Default 2.
	Threshold int
	// SkipInvalid skips malformed entries and reports them, instead of failing.
	SkipInvalid bool
}

// ImportProblem names one entry the import changed or could not take.
type ImportProblem struct {
	EntryID string `json:"entry_id"`
	Reason  string `json:"reason"`
}

// MaxReportItems caps each per-entry list in an ImportReport, so a report never grows
// with the size of the file. The *Count fields stay exact, and every import event in
// the history records its own entry's outcome.
const MaxReportItems = 100

// ImportReport describes one import.
type ImportReport struct {
	Imported        int `json:"imported"`
	Unchanged       int `json:"unchanged"`
	ConflictCount   int `json:"conflict_count"`
	SkippedCount    int `json:"skipped_count"`
	RelabelledCount int `json:"relabelled_count"`
	WarningCount    int `json:"warning_count"`
	// The first MaxReportItems of each kind, in file order.
	Conflicts    []ImportProblem `json:"conflicts,omitempty"`
	Skipped      []ImportProblem `json:"skipped,omitempty"`
	Relabelled   []ImportProblem `json:"relabelled,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
	SourceDigest string          `json:"source_digest"`
}

func (r *ImportReport) conflict(id, reason string) {
	r.ConflictCount++
	if len(r.Conflicts) < MaxReportItems {
		r.Conflicts = append(r.Conflicts, ImportProblem{EntryID: id, Reason: reason})
	}
}

func (r *ImportReport) skip(id, reason string) {
	r.SkippedCount++
	if len(r.Skipped) < MaxReportItems {
		r.Skipped = append(r.Skipped, ImportProblem{EntryID: id, Reason: reason})
	}
}

func (r *ImportReport) relabel(id, reason string) {
	r.RelabelledCount++
	if len(r.Relabelled) < MaxReportItems {
		r.Relabelled = append(r.Relabelled, ImportProblem{EntryID: id, Reason: reason})
	}
}

func (r *ImportReport) warn(msgs []string) {
	r.WarningCount += len(msgs)
	for _, m := range msgs {
		if len(r.Warnings) >= MaxReportItems {
			return
		}
		r.Warnings = append(r.Warnings, m)
	}
}

// errInvalidEntry marks a malformed legacy entry.
var errInvalidEntry = errors.New("invalid legacy entry")

// ImportContextEntriesJSON imports a legacy context_entries.json stream.
func (s *SQLStore) ImportContextEntriesJSON(ctx context.Context, workspaceID string, src io.Reader, opts ImportOptions) (ImportReport, error) {
	var rep ImportReport
	if err := validID("workspace", workspaceID); err != nil {
		return rep, err
	}
	if opts.Principal == "" {
		opts.Principal = "xmustard-import"
	}
	if opts.Threshold <= 0 {
		opts.Threshold = 2
	}
	actor := Actor{Principal: opts.Principal}
	if err := actor.validate(); err != nil {
		return rep, err
	}
	release, err := acquireImportSlot(ctx, "context_entries.json")
	if err != nil {
		return rep, err
	}
	defer release()
	h := sha256.New()
	tee := io.TeeReader(bufio.NewReader(src), h)
	err = s.Update(ctx, func(tx Tx) error {
		t := tx.(*txn)
		dec := json.NewDecoder(tee)
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: read legacy entries: %v", ErrInvalid, err)
		}
		if tok == nil { // a legacy "null" file holds no entries
			return nil
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return fmt.Errorf("%w: legacy entries must be a JSON array", ErrInvalid)
		}
		for dec.More() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var le LegacyEntry
			if err := dec.Decode(&le); err != nil {
				return fmt.Errorf("%w: decode legacy entry: %v", ErrInvalid, err)
			}
			if err := t.importEntry(ctx, workspaceID, le, opts, actor, &rep); err != nil {
				if errors.Is(err, errInvalidEntry) && opts.SkipInvalid {
					rep.skip(le.ID, err.Error())
					continue
				}
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("%w: legacy entries array is not closed: %v", ErrInvalid, err)
		}
		if _, err := io.Copy(io.Discard, io.MultiReader(dec.Buffered(), tee)); err != nil {
			return err
		}
		rep.SourceDigest = hex.EncodeToString(h.Sum(nil))
		return t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: workspaceID, Type: EventImport, Data: map[string]any{
				"source": "context_entries.json", "source_digest": rep.SourceDigest, "imported": rep.Imported,
				"unchanged": rep.Unchanged, "conflicts": rep.ConflictCount, "skipped": rep.SkippedCount,
				"relabelled": rep.RelabelledCount, "warnings": rep.WarningCount,
			},
		})
	})
	if err != nil {
		return ImportReport{}, err
	}
	return rep, nil
}

// normalizedLegacy is a legacy entry after validation and conservative labelling:
// exactly what the store will hold and export.
type normalizedLegacy struct {
	entry      LegacyEntry
	relabelled string
	warnings   []string
	// withheld holds verifications cast on content other than the entry's: they are
	// recorded in the import event and never become votes.
	withheld []LegacyVerification
}

// Relabel reasons are fixed strings, so labelling a large file allocates none per row.
var (
	relabelDerived    = modeNotes("verification_mode derived as ")
	relabelDowngraded = modeNotes("recorded peer_verified is not supported by distinct peer approvals; labelled ")
)

const (
	bindingDigestBroken = "content_digest does not match content; votes and promotion withheld"
	bindingHashBroken   = "content_hash does not match content; votes and promotion withheld"
)

func modeNotes(prefix string) map[string]string {
	m := make(map[string]string, len(validModes))
	for mode := range validModes {
		m[mode] = prefix + mode
	}
	return m
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidEntry, fmt.Sprintf(format, args...))
}

// normalizeLegacy validates a legacy entry and computes the state it imports as.
func normalizeLegacy(workspaceID string, le LegacyEntry, threshold int) (normalizedLegacy, error) {
	var n normalizedLegacy
	if err := validID("entry", le.ID); err != nil {
		return n, invalid("%v", err)
	}
	if le.WorkspaceID != "" && le.WorkspaceID != workspaceID {
		return n, invalid("entry %s belongs to workspace %s", le.ID, le.WorkspaceID)
	}
	if strings.TrimSpace(le.Content) == "" {
		return n, invalid("entry %s has no content", le.ID)
	}
	if strings.TrimSpace(le.Source) == "" {
		return n, invalid("entry %s has no source", le.ID)
	}
	if !validPermissions[le.Permission] {
		return n, invalid("entry %s permission %q", le.ID, le.Permission)
	}
	if !validStatuses[le.Status] {
		return n, invalid("entry %s status %q", le.ID, le.Status)
	}
	if le.Promoted != (le.Status == StatusVerified) {
		return n, invalid("entry %s: promoted=%t contradicts status %q", le.ID, le.Promoted, le.Status)
	}
	if le.RequiredVerifications < 1 {
		return n, invalid("entry %s required_verifications %d", le.ID, le.RequiredVerifications)
	}
	out := le
	out.WorkspaceID = workspaceID
	out.Title = strings.TrimSpace(le.Title)
	out.Source = strings.TrimSpace(le.Source) // stored trimmed, so compare it trimmed
	out.ContentHash, out.Stale, out.StalePaths = "", false, nil
	for _, f := range []*string{&out.CreatedAt, &out.UpdatedAt} {
		v, ok := normTime(*f)
		if !ok || v == "" {
			return n, invalid("entry %s time %q is not RFC 3339", le.ID, *f)
		}
		*f = displayTime(v)
	}
	digest := Digest(le.Content)
	out.ContentDigest = digest
	bindingBroken := ""
	switch {
	case le.ContentDigest != "" && le.ContentDigest != digest:
		bindingBroken = bindingDigestBroken
	case le.ContentHash != "" && le.ContentHash != digest[:16]:
		bindingBroken = bindingHashBroken
	}
	// Verifications: blank agents never counted; a principal keeps its first position
	// and its latest verdict, as the legacy tally does.
	out.Verifications = []LegacyVerification{}
	pos := map[string]int{}
	for _, v := range le.Verifications {
		key := principalKey(v.Agent)
		if key == "" {
			n.warnings = append(n.warnings, fmt.Sprintf("entry %s: dropped a verification with no agent", le.ID))
			continue
		}
		t, ok := normTime(v.At)
		if !ok || t == "" {
			return n, invalid("entry %s verification time %q is not RFC 3339", le.ID, v.At)
		}
		v.Agent, v.At = strings.TrimSpace(v.Agent), displayTime(t)
		if i, dup := pos[key]; dup {
			out.Verifications[i] = v
			n.warnings = append(n.warnings, fmt.Sprintf("entry %s: merged duplicate verdicts of %s", le.ID, v.Agent))
			continue
		}
		pos[key] = len(out.Verifications)
		out.Verifications = append(out.Verifications, v)
	}
	out.Paths = CleanPaths(le.Paths)
	if len(out.Paths) == 0 {
		out.Paths = nil
	}
	if len(out.PathHashes) == 0 {
		out.PathHashes = nil
	}
	for p := range out.PathHashes {
		if strings.TrimSpace(p) == "" || p != strings.TrimSpace(p) {
			return n, invalid("entry %s path hash key %q", le.ID, p)
		}
	}
	if len(out.SearchTokens) == 0 {
		out.SearchTokens = nil
	}
	switch {
	case bindingBroken != "":
		// The verdicts were cast on other content. They must not count for this text,
		// now or at any later reconciliation, so the entry starts unverified.
		n.withheld, out.Verifications = out.Verifications, []LegacyVerification{}
		out.Promoted, out.Status, out.VerificationMode = false, StatusPending, ""
		n.relabelled = bindingBroken
	case !out.Promoted:
		out.VerificationMode = ""
	default:
		derived := deriveVerificationMode(out, threshold)
		switch {
		case le.VerificationMode == "":
			out.VerificationMode = derived
			n.relabelled = relabelDerived[derived]
		case !validModes[le.VerificationMode]:
			return n, invalid("entry %s verification_mode %q", le.ID, le.VerificationMode)
		case le.VerificationMode == ModePeerVerified && derived != ModePeerVerified:
			out.VerificationMode = derived
			n.relabelled = relabelDowngraded[derived]
		}
	}
	n.entry = out
	return n, nil
}

// deriveVerificationMode labels a promoted legacy entry from its votes, like the
// w0-kernel verificationMode: peer_verified needs `need` distinct approvals from
// principals other than the author and the open-mode identity.
func deriveVerificationMode(le LegacyEntry, threshold int) string {
	need := le.RequiredVerifications
	if need <= 1 {
		need = max(threshold, 1)
	}
	author := principalKey(le.Source)
	peers, openAsserted := 0, false
	for _, v := range le.Verifications {
		key := principalKey(v.Agent)
		switch {
		case !v.Approve:
		case key == OpenModeIdentity:
			openAsserted = true
		case key == author:
		default:
			peers++
		}
	}
	switch {
	case peers >= need:
		return ModePeerVerified
	case openAsserted:
		return ModeSelfAssertedOpenMode
	default:
		return ModeSingleAgent
	}
}

// importEntry imports one legacy entry, or classifies it as unchanged or conflicting.
func (t *txn) importEntry(ctx context.Context, workspaceID string, le LegacyEntry, opts ImportOptions, actor Actor, rep *ImportReport) error {
	n, err := normalizeLegacy(workspaceID, le, opts.Threshold)
	if err != nil {
		return err
	}
	want := n.entry
	existing, err := t.GetEntry(ctx, want.ID)
	switch {
	case err == nil:
		if existing.WorkspaceID != workspaceID {
			rep.conflict(want.ID, "id exists in workspace "+existing.WorkspaceID)
			return nil
		}
		have, err := exportEntry(ctx, &t.reader, existing)
		if err != nil {
			return err
		}
		if legacyEqual(have, want) {
			rep.Unchanged++
		} else {
			rep.conflict(want.ID, "stored entry differs; left untouched")
		}
		return nil
	case !isNotFound(err):
		return err
	}

	if _, err := t.insertEntry(ctx, NewEntry{
		ID: want.ID, WorkspaceID: workspaceID, Title: want.Title, Content: want.Content, Permission: want.Permission,
		RequiredVerifications: want.RequiredVerifications, RequireVerification: want.RequireVerification,
		Paths: want.Paths, SearchTokens: want.SearchTokens,
	}, entryOrigin{source: want.Source, op: "import", createdAt: want.CreatedAt, updatedAt: want.UpdatedAt}, actor); err != nil {
		return err
	}
	digest := want.ContentDigest
	for i, v := range want.Verifications {
		verdict := VerdictReject
		if v.Approve {
			verdict = VerdictApprove
		}
		at, _ := normTime(v.At)
		if _, err := t.exec(ctx, `INSERT INTO votes (entry_id, revision, principal, principal_key, verdict, note,
			content_digest, ordinal, at) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)`,
			want.ID, v.Agent, principalKey(v.Agent), verdict, v.Note, digest, i, at); err != nil {
			return err
		}
	}
	// No t.touch: the schema trigger re-checks the peer_verified label against these
	// votes when the UPDATE below writes it, so the importer adds no per-row state.
	if len(want.PathHashes) > 0 {
		cur, err := t.GetEntry(ctx, want.ID)
		if err != nil {
			return err
		}
		baselines := make([]Baseline, 0, len(want.PathHashes))
		for _, p := range sortedKeys(want.PathHashes) {
			h := want.PathHashes[p]
			b := Baseline{Kind: AnchorPath, Value: p, State: BaselineHash, Hash: h, BaselineKind: "file"}
			if h == legacyMissingSentinel {
				b.State, b.Hash = BaselineMissing, ""
			}
			baselines = append(baselines, b)
		}
		if err := t.setBaselinesQuiet(ctx, cur, baselines); err != nil {
			return err
		}
	}
	updatedAt, _ := normTime(want.UpdatedAt)
	if _, err := t.exec(ctx, "UPDATE entries SET status = ?, promoted = ?, verification_mode = ?, updated_at = ? WHERE id = ?",
		want.Status, boolInt(want.Promoted), want.VerificationMode, updatedAt, want.ID); err != nil {
		return fmt.Errorf("import %s: %w", want.ID, err)
	}
	data := map[string]any{
		"author": want.Source, "status": want.Status, "promoted": want.Promoted,
		"verification_mode": want.VerificationMode, "votes": len(want.Verifications),
		"path_baselines": len(want.PathHashes),
	}
	if len(n.withheld) > 0 {
		// kept as history only: who approved or rejected which (other) content, and when
		data["withheld_verifications"] = n.withheld
		data["legacy_content_digest"] = le.ContentDigest
		data["legacy_content_hash"] = le.ContentHash
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: workspaceID, EntryID: want.ID, Type: EventImport, Revision: 1, NewDigest: digest,
		Note: n.relabelled, Data: data,
	}); err != nil {
		return err
	}
	rep.Imported++
	rep.warn(n.warnings)
	if n.relabelled != "" {
		rep.relabel(want.ID, n.relabelled)
	}
	return nil
}

// setBaselinesQuiet writes baselines without a baseline event; the import event
// records them. The search row is rewritten only when a baseline adds an anchor the
// entry did not declare, since only that changes the indexed anchor text.
func (t *txn) setBaselinesQuiet(ctx context.Context, e Entry, baselines []Baseline) error {
	added := false
	for _, b := range baselines {
		if err := validateAnchor(AnchorInput{Kind: b.Kind, Value: b.Value}); err != nil {
			return err
		}
		res, err := t.exec(ctx, `UPDATE anchors SET baseline_state = ?, baseline_hash = ?, baseline_kind = ?
			WHERE entry_id = ? AND kind = ? AND value = ?`, b.State, b.Hash, b.BaselineKind, e.ID, b.Kind, b.Value)
		if err != nil {
			return err
		}
		if rowsAffected(res) > 0 {
			continue
		}
		if _, err := t.exec(ctx, `INSERT INTO anchors (entry_id, ordinal, kind, value, declared, baseline_state,
			baseline_hash, baseline_kind) VALUES (?, (SELECT coalesce(max(ordinal), -1) + 1 FROM anchors WHERE entry_id = ?),
			?, ?, 0, ?, ?, ?)`, e.ID, e.ID, b.Kind, b.Value, b.State, b.Hash, b.BaselineKind); err != nil {
			return err
		}
		added = true
	}
	if !added {
		return nil
	}
	return t.refreshFTS(ctx, e.ID)
}

// exportEntry renders a stored entry in the legacy shape.
func exportEntry(ctx context.Context, r *reader, e Entry) (LegacyEntry, error) {
	contents, err := r.EntryContents(ctx, []string{e.ID})
	if err != nil {
		return LegacyEntry{}, err
	}
	c, ok := contents[e.ID]
	if !ok || c.Withheld != "" {
		return LegacyEntry{}, fmt.Errorf("export %s: content withheld: %s", e.ID, c.Withheld)
	}
	le := LegacyEntry{
		ID: e.ID, WorkspaceID: e.WorkspaceID, Title: e.Title, Content: c.Content, Source: e.Source,
		Permission: e.Permission, Status: e.Status, Promoted: e.Promoted, Verifications: []LegacyVerification{},
		RequiredVerifications: e.RequiredVerifications, RequireVerification: e.RequireVerification,
		VerificationMode: e.VerificationMode, CreatedAt: displayTime(e.CreatedAt), UpdatedAt: displayTime(e.UpdatedAt),
		SearchTokens: e.SearchTokens, ContentDigest: c.Digest,
	}
	votes, err := r.ListVotes(ctx, e.ID, e.Revision)
	if err != nil {
		return LegacyEntry{}, err
	}
	for _, v := range votes {
		if v.Verdict != VerdictApprove && v.Verdict != VerdictReject {
			continue // the legacy shape has no retract or duplicate verdicts
		}
		le.Verifications = append(le.Verifications, LegacyVerification{
			Agent: v.Principal, Approve: v.Verdict == VerdictApprove, Note: v.Note, At: displayTime(v.At),
		})
	}
	anchors, err := r.ListAnchors(ctx, e.ID)
	if err != nil {
		return LegacyEntry{}, err
	}
	for _, a := range anchors {
		if a.Kind != AnchorPath {
			continue
		}
		if a.Declared {
			le.Paths = append(le.Paths, a.Value)
		}
		switch a.BaselineState {
		case BaselineHash:
			if le.PathHashes == nil {
				le.PathHashes = map[string]string{}
			}
			le.PathHashes[a.Value] = a.BaselineHash
		case BaselineMissing:
			if le.PathHashes == nil {
				le.PathHashes = map[string]string{}
			}
			le.PathHashes[a.Value] = legacyMissingSentinel
		}
	}
	return le, nil
}

func legacyEqual(a, b LegacyEntry) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// ExportContextEntriesJSON writes the workspace's active entries as a legacy
// context_entries.json array, streaming one entry at a time from one read snapshot.
// Superseded, retracted, merged, archived and purged entries are left out: the
// legacy shape cannot express those states and would serve them again.
func (s *SQLStore) ExportContextEntriesJSON(ctx context.Context, workspaceID string, w io.Writer) error {
	if err := validID("workspace", workspaceID); err != nil {
		return err
	}
	return s.View(ctx, func(rd Reader) error {
		r := rd.(*reader)
		bw := bufio.NewWriter(w)
		if _, err := bw.WriteString("["); err != nil {
			return err
		}
		first := true
		var cursor int64
		for {
			page, err := r.ListEntries(ctx, EntryFilter{WorkspaceID: workspaceID, AfterCursor: cursor, Limit: 500})
			if err != nil {
				return err
			}
			for _, e := range page {
				le, err := exportEntry(ctx, r, e)
				if err != nil {
					return err
				}
				b, err := json.MarshalIndent(le, "  ", "  ")
				if err != nil {
					return err
				}
				sep := ",\n  "
				if first {
					sep, first = "\n  ", false
				}
				if _, err := bw.WriteString(sep); err != nil {
					return err
				}
				if _, err := bw.Write(b); err != nil {
					return err
				}
				cursor = e.Cursor
			}
			if len(page) < 500 {
				break
			}
		}
		if !first {
			if _, err := bw.WriteString("\n"); err != nil {
				return err
			}
		}
		if _, err := bw.WriteString("]\n"); err != nil {
			return err
		}
		return bw.Flush()
	})
}

// legacyFeedback mirrors workspaceops.FeedbackEntry.
type legacyFeedback struct {
	Path           string `json:"path"`
	RetrievalCount int    `json:"retrieval_count"`
	VerifyCount    int    `json:"verify_count"`
	RunSuccess     int    `json:"run_success"`
	RunFail        int    `json:"run_fail"`
	LastUsed       string `json:"last_used"`
}

// unknownLastUsed stands in for a missing or unparsable legacy last_used. It is a
// fixed time, never the import time, so re-importing the same file changes nothing.
const unknownLastUsed = "1970-01-01T00:00:00.000000000Z"

// ImportFeedbackJSON imports a legacy agent_feedback.json array. Counters and
// last_used merge by maximum, so re-importing the same file changes nothing.
func (s *SQLStore) ImportFeedbackJSON(ctx context.Context, workspaceID string, src io.Reader) (int, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return 0, err
	}
	release, err := acquireImportSlot(ctx, "agent_feedback.json")
	if err != nil {
		return 0, err
	}
	defer release()
	n := 0
	err = s.Update(ctx, func(tx Tx) error {
		t := tx.(*txn)
		dec := json.NewDecoder(bufio.NewReader(src))
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: read legacy feedback: %v", ErrInvalid, err)
		}
		if tok == nil {
			return nil
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return fmt.Errorf("%w: legacy feedback must be a JSON array", ErrInvalid)
		}
		for dec.More() {
			var f legacyFeedback
			if err := dec.Decode(&f); err != nil {
				return fmt.Errorf("%w: decode legacy feedback: %v", ErrInvalid, err)
			}
			paths := CleanPaths([]string{f.Path})
			if len(paths) == 0 {
				continue
			}
			lastUsed, ok := normTime(f.LastUsed)
			if !ok || lastUsed == "" {
				lastUsed = unknownLastUsed
			}
			clamp := func(v int) int { return min(max(v, 0), feedbackCap) }
			if _, err := t.exec(ctx, `INSERT INTO path_feedback (workspace_id, path, retrieval_count, verify_count,
				run_success, run_fail, last_used) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (workspace_id, path) DO UPDATE SET
				retrieval_count = max(retrieval_count, excluded.retrieval_count),
				verify_count = max(verify_count, excluded.verify_count),
				run_success = max(run_success, excluded.run_success),
				run_fail = max(run_fail, excluded.run_fail),
				last_used = max(last_used, excluded.last_used)`,
				workspaceID, paths[0], clamp(f.RetrievalCount), clamp(f.VerifyCount), clamp(f.RunSuccess),
				clamp(f.RunFail), lastUsed); err != nil {
				return err
			}
			n++
		}
		_, err = dec.Token()
		return err
	})
	return n, err
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
