package workspaceops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"xmustard/api-go/internal/govstore"
)

// What a human approver confirms for a write made through an MCP client (WS-57). The
// API holds such a write until the human confirms it (cmd/xmustard-api/human_presence.go)
// and the bridge shows the human this text through MCP elicitation. The text names the
// write and every field that changes what is stored, as the store would record it: the
// op or outcome normalized as the write runs it, safe ids as they are, and free text
// redacted like the write, cut at maxConfirmText and quoted so it reads as data, with
// the size and SHA-256 of anything cut. A verdict names the revision it is cast on with
// that revision's text (or, for a pending edit, its diff). Digest binds the confirmed
// retry to this exact text: when the text differs by the retry (another served
// revision, say) the write is held again, and a verdict is pinned to the revision shown.

// maxConfirmText bounds each free-text field shown to the human.
const maxConfirmText = 300

// HumanConfirmation is the text a human approver confirms for one write and its digest.
type HumanConfirmation struct {
	Text   string
	Digest string
	// Revision is the revision a verdict is cast on, pinned on the confirmed retry; 0
	// for a remember write and for feedback outcomes, which take no revision.
	Revision int64
}

func newConfirmation(lines []string, revision int64) HumanConfirmation {
	text := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(text))
	return HumanConfirmation{Text: text, Digest: hex.EncodeToString(sum[:]), Revision: revision}
}

// confirmLine appends "label: value" to lines unless value is empty.
func confirmLine(lines []string, label, value string) []string {
	if value == "" {
		return lines
	}
	return append(lines, label+": "+value)
}

// shownText renders free text as a quoted Go string (control and format characters
// escaped, so it cannot fake a line of its own), cut at maxConfirmText with the full
// size and SHA-256 of what was cut. Empty text is "".
func shownText(s string) string {
	if s == "" {
		return ""
	}
	cut, clipped := clipRunes(s, maxConfirmText)
	out := strconv.Quote(cut)
	if clipped {
		sum := sha256.Sum256([]byte(s))
		out += fmt.Sprintf(" (cut: %d bytes in all, sha256 %s)", len(s), hex.EncodeToString(sum[:]))
	}
	return out
}

func opSet(ops ...string) map[string]bool {
	out := make(map[string]bool, len(ops))
	for _, op := range ops {
		out[op] = true
	}
	return out
}

var (
	proposeOps = opSet("propose", "supersede")
	contentOps = opSet("propose", "supersede", "edit")
	editOps    = opSet("edit")
	entryOps   = opSet("edit", "retire", "restore") // the ops that name an existing entry
)

// rememberFields are the request fields shown for each op, in order: the fields the op
// reads (rememberOps), so an ignored field is never shown as if it took effect.
var rememberFields = []struct {
	label string
	ops   map[string]bool
	value func(RememberRequest) string
}{
	{"title", proposeOps, func(q RememberRequest) string { return shownText(q.Title) }},
	{"content", contentOps, func(q RememberRequest) string { return shownText(q.Content) }},
	{"permission", proposeOps, func(q RememberRequest) string { return shownText(q.Permission) }},
	{"paths", proposeOps, func(q RememberRequest) string { return shownText(strings.Join(q.Paths, ", ")) }},
	{"supersedes", proposeOps, func(q RememberRequest) string { return shownText(strings.Join(q.Supersedes, ", ")) }},
	{"require_verification", proposeOps, func(q RememberRequest) string {
		if q.RequireVerification == nil {
			return ""
		}
		return strconv.FormatBool(*q.RequireVerification)
	}},
	{"base_revision", editOps, func(q RememberRequest) string {
		if q.BaseRevision == 0 {
			return ""
		}
		return strconv.FormatInt(q.BaseRevision, 10)
	}},
	{"old_string", editOps, func(q RememberRequest) string { return shownText(q.OldString) }},
	{"new_string", editOps, func(q RememberRequest) string { return shownText(q.NewString) }},
	{"description", editOps, func(q RememberRequest) string {
		if q.Description != nil && *q.Description == "" {
			return "(cleared)"
		}
		return shownText(derefStr(q.Description))
	}},
	{"expires", contentOps, func(q RememberRequest) string { return shownText(q.Expires) }},
	{"reason", entryOps, func(q RememberRequest) string { return shownText(q.Reason) }},
}

// DescribeRemember is what a human approver confirms for a remember write (see the
// file comment). It fails as the write would on an unknown op, an unsafe id or a
// missing entry, so nothing is asked for a write that cannot run.
func DescribeRemember(dataDir, workspaceID string, req RememberRequest) (HumanConfirmation, error) {
	op, err := NormalizeRememberOp(req.Op)
	if err != nil {
		return HumanConfirmation{}, err
	}
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return HumanConfirmation{}, err
	}
	var red ingestRedaction
	red.scrub(&req.Title, &req.Content, &req.NewString, &req.Reason)
	req.Description = red.scrubOptional(req.Description)
	lines := []string{fmt.Sprintf("write: remember (op %s) in workspace %s", op, workspaceID)}
	if entryOps[op] {
		if err := validateSafeID("entry", req.EntryID); err != nil {
			return HumanConfirmation{}, err
		}
		ctx := context.Background()
		err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
			e, _, err := loadAnyEntryTx(ctx, r, workspaceID, req.EntryID)
			if err == nil {
				lines = append(lines, fmt.Sprintf("memory: %s, served revision %d, %s, %s, title %s", e.ID, e.Revision,
					e.Status, e.Lifecycle, shownText(e.Title)))
			}
			return err
		})
		if err != nil {
			return HumanConfirmation{}, err
		}
	}
	for _, f := range rememberFields {
		if f.ops[op] {
			lines = confirmLine(lines, f.label, f.value(req))
		}
	}
	return newConfirmation(lines, 0), nil
}

// feedbackOutcomes report on the served revision and take no revision (reportOutcome).
var feedbackOutcomes = opSet(OutcomeHelpful, OutcomeMisleading, OutcomeStaleHarm)

// DescribeVerify is what a human approver confirms for a verify call (see the file
// comment): the outcome, the revision it is cast on (the served one when req names
// none) with its text or, for a pending edit, its diff, the target, the evidence and the
// note. Revision pins that revision for a vote.
func DescribeVerify(dataDir, workspaceID, entryID string, req VerifyRequest) (HumanConfirmation, error) {
	outcome, err := NormalizeVerifyOutcome(req.Outcome)
	if err != nil {
		return HumanConfirmation{}, err
	}
	for _, id := range [...]struct{ kind, v string }{{"workspace", workspaceID}, {"entry", entryID}} {
		if err := validateSafeID(id.kind, id.v); err != nil {
			return HumanConfirmation{}, err
		}
	}
	if req.Revision < 0 {
		return HumanConfirmation{}, fmt.Errorf("revision must be positive: %w", ErrInvalidInput)
	}
	if req.Target = strings.TrimSpace(req.Target); req.Target != "" {
		if err := validateSafeID("target entry", req.Target); err != nil {
			return HumanConfirmation{}, err
		}
	}
	var red ingestRedaction
	red.scrub(&req.Note)
	var lines []string
	var pin int64
	ctx := context.Background()
	err = memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		e, _, err := loadEntryTx(ctx, r, workspaceID, entryID)
		if err != nil {
			return err
		}
		revision := cmp.Or(req.Revision, e.Revision)
		rv, err := r.GetRevision(ctx, e.ID, revision)
		if err != nil {
			return err
		}
		which := "the served revision"
		if revision != e.Revision {
			which = "a pending edit"
		}
		label, shown, err := shownRevision(ctx, r, e, rv)
		if err != nil {
			return err
		}
		lines = []string{fmt.Sprintf("write: verify (outcome %s) on memory %s revision %d (%s) in workspace %s",
			outcome, e.ID, revision, which, workspaceID)}
		lines = confirmLine(lines, "title", shownText(fallbackString(rv.Title, e.Title)))
		lines = confirmLine(lines, label, shown)
		if !feedbackOutcomes[outcome] {
			pin = revision
		}
		return nil
	})
	if err != nil {
		return HumanConfirmation{}, err
	}
	lines = confirmLine(lines, "target", req.Target)
	lines = confirmLine(lines, "evidence", shownText(strings.TrimSpace(req.EvidenceHandle)))
	lines = confirmLine(lines, "note", shownText(req.Note))
	return newConfirmation(lines, pin), nil
}

// shownRevision is what the human reads of the revision a verdict is cast on: a pending
// edit's diff against the served revision, or the served revision's text unless it was
// purged or does not match its digest.
func shownRevision(ctx context.Context, r govstore.Reader, e govstore.Entry, rv govstore.Revision) (string, string, error) {
	switch {
	case rv.Revision != e.Revision:
		diff, err := revisionDiff(ctx, r, e.ID, e.Revision, rv.Revision)
		return "change", shownText(diff), err
	case rv.ContentDropped:
		return "text", "(purged)", nil
	case contentDigest(rv.Content) != rv.ContentDigest:
		return "text", "(withheld: it does not match its digest)", nil
	}
	return "text", shownText(rv.Content), nil
}
