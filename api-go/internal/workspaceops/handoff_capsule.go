package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
	"xmustard/api-go/internal/redact"
)

// Handoff capsule (WS-33; PAR-PROV-08): what one session leaves the next, in a fixed
// schema instead of free prose, so any client can write and read it. An agent stores
// one with remember(kind=handoff, content=<capsule JSON>, paths=...); the ledger builds
// one at SessionEnd from what the session did (record class derived). A capsule is
// session-scoped evidence, not memory: it is a ledger row of its session with the
// content hashes of the paths it names, never proposed, verified or pushed as memory.
// ground returns the latest capsules with the paths that changed since (stale_paths).

// HandoffSchema names the capsule format; a capsule may carry it as "schema".
const HandoffSchema = "xmustard.handoff/v1"

// Capsule bounds (PAR-PROV-08: at most 500 words).
const (
	handoffMaxWords = 500
	handoffMaxItems = 20
	handoffItemMax  = 1000 // bytes of one text field
	handoffMaxPaths = 32
)

// HandoffCapsule is the schema. Identifiers are kept verbatim (file paths, symbols, ids,
// commands): they are what the next session searches for.
type HandoffCapsule struct {
	Schema         string            `json:"schema,omitempty"`
	Goals          []string          `json:"goals"`
	WhatHappened   []string          `json:"what_happened"`
	Identifiers    []string          `json:"identifiers,omitempty"`
	ErrorsAndFixes []HandoffErrorFix `json:"errors_and_fixes,omitempty"`
	CurrentState   string            `json:"current_state"`
	NextStep       string            `json:"next_step"`
	LookupHints    []string          `json:"lookup_hints,omitempty"`
}

// HandoffErrorFix is one error the session met and how it was fixed ("" while open).
type HandoffErrorFix struct {
	Error string `json:"error"`
	Fix   string `json:"fix,omitempty"`
}

// texts are the capsule's text fields, for counting, scanning and redacting.
func (c *HandoffCapsule) texts() []*string {
	out := []*string{&c.CurrentState, &c.NextStep}
	for _, list := range [][]string{c.Goals, c.WhatHappened, c.Identifiers, c.LookupHints} {
		for i := range list {
			out = append(out, &list[i])
		}
	}
	for i := range c.ErrorsAndFixes {
		out = append(out, &c.ErrorsAndFixes[i].Error, &c.ErrorsAndFixes[i].Fix)
	}
	return out
}

func (c *HandoffCapsule) words() int {
	n := 0
	for _, t := range c.texts() {
		n += len(strings.Fields(*t))
	}
	return n
}

// capsuleRules are the schema checks beyond the member names, in order; each returns
// the violation or "".
var capsuleRules = []func(*HandoffCapsule) string{
	func(c *HandoffCapsule) string {
		if c.Schema != "" && c.Schema != HandoffSchema {
			return "schema must be " + HandoffSchema
		}
		return ""
	},
	func(c *HandoffCapsule) string { return required("goals", len(nonBlank(c.Goals)) > 0) },
	func(c *HandoffCapsule) string {
		return required("current_state", strings.TrimSpace(c.CurrentState) != "")
	},
	func(c *HandoffCapsule) string { return required("next_step", strings.TrimSpace(c.NextStep) != "") },
	func(c *HandoffCapsule) string {
		for _, l := range []struct {
			name string
			n    int
		}{{"goals", len(c.Goals)}, {"what_happened", len(c.WhatHappened)}, {"identifiers", len(c.Identifiers)},
			{"errors_and_fixes", len(c.ErrorsAndFixes)}, {"lookup_hints", len(c.LookupHints)}} {
			if l.n > handoffMaxItems {
				return fmt.Sprintf("%s has %d items (at most %d)", l.name, l.n, handoffMaxItems)
			}
		}
		return ""
	},
	func(c *HandoffCapsule) string {
		for _, t := range c.texts() {
			if len(*t) > handoffItemMax {
				return fmt.Sprintf("a text field is %d bytes (at most %d)", len(*t), handoffItemMax)
			}
		}
		return ""
	},
	func(c *HandoffCapsule) string {
		if n := c.words(); n > handoffMaxWords {
			return fmt.Sprintf("%d words (at most %d)", n, handoffMaxWords)
		}
		return ""
	},
}

func required(field string, ok bool) string {
	if ok {
		return ""
	}
	return field + " is required"
}

func nonBlank(list []string) []string {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParseHandoffCapsule schema-checks a capsule: one JSON object with only the schema's
// members, each of its type, and the rules above. The error lists every violation.
func ParseHandoffCapsule(raw string) (HandoffCapsule, error) {
	var c HandoffCapsule
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, capsuleError([]string{"not a capsule object: " + err.Error()})
	}
	if dec.More() {
		return c, capsuleError([]string{"trailing data after the capsule object"})
	}
	if violations := capsuleViolations(&c); len(violations) > 0 {
		return c, capsuleError(violations)
	}
	c.Schema = HandoffSchema
	return c, nil
}

func capsuleError(violations []string) error {
	return fmt.Errorf("the content is not a %s handoff capsule {goals[], what_happened[], identifiers[], "+
		"errors_and_fixes[{error, fix}], current_state, next_step, lookup_hints[]}: %s: %w",
		HandoffSchema, strings.Join(violations, "; "), ErrInvalidInput)
}

// Render is the capsule as text (the ledger row's body, which session search indexes).
func (c *HandoffCapsule) Render() string {
	var b strings.Builder
	list := func(label string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", label, strings.Join(items, "; "))
		}
	}
	list("goals", c.Goals)
	list("what happened", c.WhatHappened)
	list("identifiers", c.Identifiers)
	for _, ef := range c.ErrorsAndFixes {
		fmt.Fprintf(&b, "error: %s", ef.Error)
		if ef.Fix != "" {
			fmt.Fprintf(&b, " -> fix: %s", ef.Fix)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "current state: %s\nnext step: %s\n", c.CurrentState, c.NextStep)
	list("lookup hints", c.LookupHints)
	return strings.TrimSuffix(b.String(), "\n")
}

// handoffData is what a capsule row keeps besides its body.
type handoffData struct {
	Schema     string            `json:"schema"`
	Capsule    HandoffCapsule    `json:"capsule"`
	PathHashes map[string]string `json:"path_hashes,omitempty"`
	Words      int               `json:"words"`
	Auto       bool              `json:"auto,omitempty"`
}

// HandoffRecord is a stored capsule as remember and ground return it.
type HandoffRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	SessionID   string `json:"session_id"`
	Client      string `json:"client,omitempty"`
	Principal   string `json:"principal,omitempty"`
	// RecordClass is raw for an agent's capsule and derived for one the ledger built;
	// Auto marks the latter.
	RecordClass string         `json:"record_class"`
	Auto        bool           `json:"auto,omitempty"`
	At          string         `json:"at"`
	Words       int            `json:"words"`
	Capsule     HandoffCapsule `json:"capsule"`
	Paths       []string       `json:"paths,omitempty"`
	// StalePaths are the named paths whose content changed since the capsule was stored.
	StalePaths     []string       `json:"stale_paths,omitempty"`
	InjectionFlags []string       `json:"injection_flags,omitempty"`
	Redactions     *redact.Report `json:"redactions,omitempty"`
	Warnings       []string       `json:"warnings,omitempty"`
}

// handoffID names a capsule row.
func handoffID(seq int64) string { return "handoff:" + strconv.FormatInt(seq, 10) }

// HandoffSessionKey is the session a capsule is stored under: sessionID when it is an
// execution key (<client>:<session>...), else the MCP session of that id (the
// transport's X-Xmustard-Session-Id when sessionID is ""), else the principal's own
// ad hoc session "mcp:adhoc-<principal>" (a session row is per workspace, so no other
// principal or workspace shares it).
func HandoffSessionKey(sessionID, transportSession, principal string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if strings.Contains(sessionID, ":") {
		if !executionKeyPattern.MatchString(sessionID) || strings.Contains(sessionID, "..") {
			return "", fmt.Errorf("session_id %q is not an execution key: %w", sessionID, ErrInvalidInput)
		}
		return sessionID, nil
	}
	return ExecutionKey("mcp", cmp.Or(sessionID, transportSession, "adhoc-"+ledgerPrincipal(principal)), ""), nil
}

// RememberHandoff stores an agent's capsule (remember kind=handoff): schema-checked,
// secrets redacted, the named paths confined to the workspace and hashed, appended to
// the session's ledger as a raw row. The session is started for actor when it is new;
// a session of another principal is refused (a conflict).
func RememberHandoff(dataDir, workspaceID string, req RememberRequest, sessionID string, actor ContextActor) (*HandoffRecord, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if op, err := NormalizeRememberOp(req.Op); err != nil || op != "propose" {
		return nil, fmt.Errorf("a handoff capsule is stored as it is written (op propose), not %q: %w", req.Op, ErrInvalidInput)
	}
	capsule, err := ParseHandoffCapsule(req.Content)
	if err != nil {
		return nil, err
	}
	key, err := HandoffSessionKey(sessionID, actor.SessionID, actor.ID)
	if err != nil {
		return nil, err
	}
	paths, err := confineAnchorPaths(dataDir, workspaceID, cleanPaths(req.Paths))
	if err != nil {
		return nil, err
	}
	if len(paths) > handoffMaxPaths {
		return nil, fmt.Errorf("a capsule names at most %d paths: %w", handoffMaxPaths, ErrInvalidInput)
	}
	var red ingestRedaction
	red.scrub(capsule.texts()...)
	rec, err := storeHandoff(context.Background(), dataDir, workspaceID, key, actor.ID, capsule, paths, false)
	if err != nil {
		return nil, err
	}
	if red.rep.Redacted {
		rec.Redactions = &red.rep
		rec.Warnings = append(rec.Warnings, fmt.Sprintf("redacted %d secret value(s) before storing", red.rep.Count))
	}
	return rec, nil
}

// storeHandoff appends a capsule row to the session key, starting the session for
// principal when it is new.
func storeHandoff(ctx context.Context, dataDir, workspaceID, key, principal string, c HandoffCapsule, paths []string, auto bool) (*HandoffRecord, error) {
	client, _, _ := strings.Cut(key, ":")
	data := handoffData{Schema: HandoffSchema, Capsule: c, Words: c.words(), Auto: auto,
		PathHashes: capturePathHashes(contextRoot(dataDir, workspaceID), paths)}
	class := govstore.RecordRaw
	if auto {
		class = govstore.RecordDerived
	}
	actor := govstore.Actor{Principal: ledgerPrincipal(principal), SessionID: key}
	sessRow := sessionRow(workspaceID, key)
	var row govstore.SessionEvent
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		if _, err := tx.UpsertSession(ctx, govstore.SessionInput{ID: sessRow, WorkspaceID: workspaceID, Client: client,
			ExecutionKey: key}, actor); err != nil {
			return err
		}
		var err error
		row, err = tx.AppendSessionEvent(ctx, govstore.SessionEventInput{SessionID: sessRow, Kind: LedgerHandoff,
			RecordClass: class, Body: c.Render(), Data: data}, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	touched(dataDir, sessRow)
	rec := handoffRecord(workspaceID, key, client, actor.Principal, row, data, paths)
	rec.Status = "stored"
	if rep := injection.Scan(c.Render(), strings.Join(paths, "\n")); !rep.Clean() {
		rec.InjectionFlags = rep.Flags
		rec.Warnings = append(rec.Warnings, "the capsule matches instruction patterns ("+strings.Join(rep.Flags, ", ")+
			"): it is returned labeled and never pushed into a session")
	}
	return rec, nil
}

// Trust is the capsule's trust label for a data frame: the ledger built a derived one,
// an agent wrote any other; neither is verified.
func (r *HandoffRecord) Trust() string {
	if r.RecordClass == govstore.RecordDerived {
		return "derived"
	}
	return "unverified"
}

// Gist is the part of a capsule a SessionStart hook injects: its first goals, its state
// and next step (each cut as ground's handoffs list cuts them), and the named paths
// that changed since it was stored. It is text of the capsule's frame, so the injection
// scan covers the paths too; the whole capsule is session_handoff.capsule.
func (r *HandoffRecord) Gist() string {
	v := handoffView(*r)
	text := fmt.Sprintf("goals: %s\ncurrent state: %s\nnext step: %s", strings.Join(v.Goals, "; "), v.State, v.NextStep)
	if n := len(r.StalePaths); n > 0 {
		paths := r.StalePaths[:min(n, groundViewPaths)]
		text += "\nchanged since it was stored: " + cutBytes(strings.Join(paths, ", "), groundViewTextMax)
		if n > len(paths) {
			text += fmt.Sprintf(" (and %d more)", n-len(paths))
		}
	}
	return text + "\nthe whole capsule: ground session_id=" + r.SessionID + " (session_handoff.capsule)"
}

func handoffRecord(workspaceID, key, client, principal string, row govstore.SessionEvent, d handoffData, paths []string) *HandoffRecord {
	return &HandoffRecord{ID: handoffID(row.Seq), WorkspaceID: workspaceID, Kind: LedgerHandoff, SessionID: key,
		Client: client, Principal: principal, RecordClass: row.RecordClass, Auto: d.Auto, At: row.At, Words: d.Words,
		Capsule: d.Capsule, Paths: paths}
}

// sessionCapsule is the capsule a session hands over: its newest raw capsule, the one
// its agent wrote (PAR-PROV-08), else, unless rawOnly, its newest derived one.
func sessionCapsule(ctx context.Context, r govstore.Reader, workspaceID, key string, rawOnly bool) (HandoffCapsule, govstore.SessionEvent, bool) {
	rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, SessionID: sessionRow(workspaceID, key),
		Kinds: []string{LedgerHandoff}, Limit: handoffKeep})
	if err != nil {
		return HandoffCapsule{}, govstore.SessionEvent{}, false
	}
	pick := -1
	for i, row := range rows { // newest first
		if row.RecordClass == govstore.RecordRaw {
			pick = i
			break
		}
		if pick < 0 && !rawOnly {
			pick = i
		}
	}
	if pick < 0 {
		return HandoffCapsule{}, govstore.SessionEvent{}, false
	}
	var d handoffData
	if json.Unmarshal(rows[pick].Data, &d) != nil || d.Schema != HandoffSchema {
		return HandoffCapsule{}, govstore.SessionEvent{}, false
	}
	return d.Capsule, rows[pick], true
}

// latestHandoffs reads the workspace's newest capsules (across its most recently seen
// sessions), each with the paths that changed since it was stored.
func latestHandoffs(ctx context.Context, r govstore.Reader, root, workspaceID string, limit int) ([]HandoffRecord, error) {
	rows, err := r.RecentSessionEvents(ctx, govstore.RecentEventFilter{WorkspaceID: workspaceID, Kinds: []string{LedgerHandoff}, Limit: limit,
		ExcludeRetention: RetentionTranscript})
	if err != nil {
		return nil, err
	}
	var out []HandoffRecord
	for _, row := range rows {
		if rec, ok := capsuleRecord(ctx, r, root, workspaceID, row); ok {
			out = append(out, *rec)
		}
	}
	return out, nil
}

// capsuleRecord reads a capsule row as a record with its stale paths and scan flags.
func capsuleRecord(ctx context.Context, r govstore.Reader, root, workspaceID string, row govstore.SessionEvent) (*HandoffRecord, bool) {
	var d handoffData
	if json.Unmarshal(row.Data, &d) != nil || d.Schema != HandoffSchema {
		return nil, false
	}
	sess, err := r.GetSession(ctx, row.SessionID)
	if err != nil {
		return nil, false
	}
	paths := make([]string, 0, len(d.PathHashes))
	for p := range d.PathHashes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	rec := handoffRecord(workspaceID, sess.ExecutionKey, sess.Client, sess.Principal, row, d, paths)
	check := ContextEntry{PathHashes: d.PathHashes}
	computeStaleness(root, &check)
	rec.StalePaths = check.StalePaths
	if rep := injection.Scan(d.Capsule.Render(), strings.Join(paths, "\n")); !rep.Clean() {
		rec.InjectionFlags = rep.Flags
	}
	return rec, true
}

// --- the automatic capsule ---

// autoCapsule builds the capsule of a session from its ledger (SessionEnd): what it
// set out to do, what it did, the errors it fixed and left, and what is next. ok is
// false when the session did nothing worth handing off.
func autoCapsule(sess govstore.Session, evs []govstore.SessionEvent, reason string, now time.Time) (HandoffCapsule, []string, bool) {
	var goal string
	calls, failed, proposed := 0, 0, 0
	for _, ev := range evs {
		switch ev.Kind {
		case LedgerPrompt:
			goal = cmp.Or(goal, rowText(ev).Text)
		case LedgerToolCall:
			calls++
			if ev.Status == LedgerError {
				failed++
			}
		case LedgerMemoryProposed:
			proposed++
		}
	}
	files := activeFiles(evs)
	var modified []string
	for _, f := range files {
		if f.Modified && len(modified) < 12 {
			modified = append(modified, f.Path)
		}
	}
	if calls == 0 && goal == "" && len(modified) == 0 {
		return HandoffCapsule{}, nil, false
	}
	c := HandoffCapsule{Schema: HandoffSchema, Goals: []string{cmp.Or(goal, "(no prompt recorded)")}}
	c.WhatHappened = []string{fmt.Sprintf("%d tool call(s), %d failed; %d file(s) modified; %d memory proposal(s)",
		calls, failed, len(modified), proposed)}
	c.Identifiers = modified
	for _, fx := range fixes(evs) {
		if len(c.ErrorsAndFixes) < 8 {
			c.ErrorsAndFixes = append(c.ErrorsAndFixes, HandoffErrorFix{Error: fx.failure, Fix: fx.fix()})
		}
	}
	open := openFailures(evs, now)
	for _, f := range open {
		if len(c.ErrorsAndFixes) < 12 {
			c.ErrorsAndFixes = append(c.ErrorsAndFixes, HandoffErrorFix{Error: f.text()})
		}
	}
	c.CurrentState = fmt.Sprintf("session %s ended (%s) with %d open failure(s)", sess.ExecutionKey, cmp.Or(reason, "unknown"), len(open))
	c.NextStep = "none recorded"
	if len(open) > 0 {
		c.NextStep = "resolve the open failure: " + open[0].text()
	}
	c.LookupHints = []string{"ground session_id=" + sess.ExecutionKey}
	for _, t := range c.texts() {
		*t = cutBytes(*t, handoffItemMax)
	}
	return c, modified, len(capsuleViolations(&c)) == 0
}

func capsuleViolations(c *HandoffCapsule) []string {
	var out []string
	for _, rule := range capsuleRules {
		if v := rule(c); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// storeAutoCapsule builds and stores a session's automatic capsule (a SessionEnd job),
// unless its agent wrote one: the agent's capsule is what the session hands over
// (sessionCapsule), and the snapshot beside it carries the ledger's view, work done
// after the capsule included.
func storeAutoCapsule(ctx context.Context, dataDir, workspaceID, key, principal, reason string) error {
	var c HandoffCapsule
	var paths []string
	var ok bool
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		if _, _, wrote := sessionCapsule(ctx, r, workspaceID, key, true); wrote {
			return nil
		}
		sess, evs, err := sessionTail(ctx, r, workspaceID, key)
		if err != nil {
			return err
		}
		c, paths, ok = autoCapsule(sess, evs, reason, time.Now().UTC())
		return nil
	})
	if err != nil || !ok {
		return err
	}
	_, err = storeHandoff(ctx, dataDir, workspaceID, key, principal, c, paths, true)
	return err
}

// isNotExist reports a missing store record (mapStoreErr's class).
func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
