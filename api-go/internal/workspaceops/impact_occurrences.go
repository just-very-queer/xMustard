package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	"xmustard/api-go/internal/rustcore"
)

// occurrenceCoreParams reduces one occurrence request to the parameters the core is
// given, refusing what no occurrence operation accepts. The order is the point, which is
// why it is one function: the cursor and offset pair is refused while both are still
// present, because checkOccurrenceArgs reads them together and dropping the cursor first
// would hide the conflict and leave the decoded position to be appended after the
// caller's own, which the core then reads as the last one. One page position per request,
// or a refusal (5.1 rule 7).
//
// The cursor is dropped because it is this process's own signed position: it must never
// become a --cursor flag, since the core's occurrences query reads that flag as a decimal
// offset and a signed cursor would be refused by the flag parser before the offset beside
// it was used, failing every page past the first.
//
// No occurrence operation walks the graph — refs reads the reference table — so a
// tier-walk argument is refused here rather than forwarded and refused by the core as an
// unknown flag, which is a different error shape for the same refusal (PAR-IMP-03, spec
// rule 6).
func occurrenceCoreParams(params map[string]string) (map[string]string, error) {
	if err := checkOccurrenceArgs(ImpactBlast, params); err != nil {
		return nil, err
	}
	core := maps.Clone(params)
	delete(core, "cursor")
	if err := checkNoWalkArgs(core); err != nil {
		return nil, err
	}
	return core, nil
}

// SymbolOccurrencesCtx is impact op= (WS-59d, PAR-IMP-03): the symbol-level
// occurrences of a name — its references, its definitions, or the types implementing
// it — each row carrying the containing symbol's name path, kind and line, a ±1-line
// snippet, and the edge's own provenance and confidence.
//
// params are the ImpactBlast parameters and must carry op=. The cursor pages the
// occurrence list; it is signed over the symbol and the operation, so one call's cursor
// never continues another's, and an unsigned or edited one is refused rather than
// clamped. Cancelling ctx cancels the Rust work.
func SymbolOccurrencesCtx(ctx context.Context, dataDir, workspaceID, symbol string, params map[string]string) (json.RawMessage, error) {
	op := params["op"]
	if op == "" {
		return nil, fmt.Errorf("op must be one of %v for an occurrence read: %w", OccurrenceOps, ErrInvalidInput)
	}
	core, err := occurrenceCoreParams(params)
	if err != nil {
		return nil, err
	}
	flags, err := ImpactFlags(ImpactBlast, core)
	if err != nil {
		return nil, err
	}
	// the cursor travels as an offset, signed here; the core never sees a client's
	// cursor and cannot be asked to trust one
	offset, err := DecodeOccurrenceCursor(workspaceID, symbol, op, params["cursor"])
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		flags = append(flags, fmt.Sprintf("--offset=%d", offset))
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"occurrences"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID, symbol)
		out, err := rustcore.RunSymbolgraph(ctx, args...)
		if err != nil {
			return nil, err
		}
		return signOccurrencePage(out, occurrenceRanking(workspaceID, symbol, op))
	})
}

// occurrenceRanking is what an occurrence cursor is signed over: the workspace, the
// symbol and the operation. A cursor for one symbol cannot page another, and a refs
// cursor cannot page implementations.
func occurrenceRanking(workspaceID, symbol, op string) []byte {
	b, _ := json.Marshal([]string{workspaceID, symbol, op})
	return b
}

// OccurrenceCursor signs the continuation of one occurrence page. A non-positive
// offset is the first page and gets no cursor, so a caller can never be handed a cursor
// that decodes back to the start.
func OccurrenceCursor(workspaceID, symbol, op string) func(offset int) string {
	ranking := occurrenceRanking(workspaceID, symbol, op)
	return func(offset int) string {
		if offset <= 0 {
			return ""
		}
		return occurrenceCursors.encode(ranking, offset)
	}
}

// DecodeOccurrenceCursor returns the row offset a cursor continues from; "" is 0. A
// cursor that is over-long, malformed, edited or signed for another occurrence list is
// refused.
func DecodeOccurrenceCursor(workspaceID, symbol, op, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	nums, ok := occurrenceCursors.decode(cursor, occurrenceRanking(workspaceID, symbol, op))
	if !ok || nums[0] <= 0 {
		return 0, fmt.Errorf("cursor is not a next_cursor of this occurrence call: %w", ErrInvalidInput)
	}
	return nums[0], nil
}

// walkArgs are the arguments that describe impact's depth-tier walk, which the
// definition and implementations operations replace. They are listed once here and
// nowhere else, so adding a walk argument makes the check catch it.
var walkArgs = []string{
	"direction", "relation_types", "min_confidence", "include_tests",
	"max_depth", "offset", "summary_only", "timeout_ms",
}

// checkNoWalkArgs refuses a tier-walk argument on an occurrence operation that has no
// walk. A refused argument is never silently dropped (§5.1 rule 7).
func checkNoWalkArgs(params map[string]string) error {
	for _, name := range walkArgs {
		if params[name] != "" {
			return fmt.Errorf("%s does not apply to impact op= definition or implementations: %w", name, ErrInvalidInput)
		}
	}
	return nil
}

// signOccurrencePage replaces the core's bare next_cursor offset with a signed one. The
// core reports where the list continues; only this process holds the signing key, so a
// client receives a cursor it cannot edit and the core never has to trust one.
func signOccurrencePage(raw []byte, ranking []byte) ([]byte, error) {
	var page map[string]json.RawMessage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("occurrence page is not a JSON object: %w", err)
	}
	cursor, ok := page["next_cursor"]
	if !ok {
		return raw, nil
	}
	// the core's next_cursor is a decimal string, not a JSON number: it is a position,
	// not a quantity, and the signed cursor it becomes is a string too.
	var text string
	if err := json.Unmarshal(cursor, &text); err != nil {
		return nil, fmt.Errorf("occurrence next_cursor is not an offset: %w", err)
	}
	offset, err := strconv.Atoi(text)
	if err != nil {
		return nil, fmt.Errorf("occurrence next_cursor %q is not an offset: %w", text, err)
	}
	// The core emits this cursor only while its end is strictly below
	// maxOccurrenceOffset, so an offset at or past the ceiling is a position this build
	// cannot have produced and is refused rather than signed: signing it would hand a
	// client a position the next request's own flag parser rejects, so the client would
	// page once and get an error instead of the rows (ocr-review:2). A zero offset is
	// not in that class: the core sends it to mean "nothing follows", and the signer
	// already drops it below, so it is left alone.
	if offset < 0 || offset >= maxOccurrenceOffset {
		return nil, fmt.Errorf("occurrence next_cursor %d is outside the readable window 0..%d: %w",
			offset, maxOccurrenceOffset-1, ErrInvalidInput)
	}
	delete(page, "next_cursor")
	signed := signOffset(ranking, offset)
	if signed == "" {
		// nothing follows the last page, so the answer carries no cursor at all
		return json.Marshal(page)
	}
	encoded, err := json.Marshal(signed)
	if err != nil {
		return nil, err
	}
	page["next_cursor"] = encoded
	return json.Marshal(page)
}

// signOffset signs a bare offset with the occurrence cursor's key, refusing a
// non-positive one exactly as OccurrenceCursor does, so the two paths never disagree.
func signOffset(ranking []byte, offset int) string {
	if offset <= 0 {
		return ""
	}
	return occurrenceCursors.encode(ranking, offset)
}

// ReconcileOccurrencePaging re-derives an occurrence page's cursor after the budget has
// cut the list, the same contract ReconcileImpactPaging keeps for the tiers. The cursor
// the handler signed describes the core's page, so a rung that removed rows would leave a
// client following a position past rows it never received. The occurrence list is
// detected by its own member rather than by the budget's stage names, because the rungs
// it shares with the tiers are named "trimmed", "counts" and "total" and a stage-name
// check would attribute one form's cut to another.
//
// When the budget removed the list outright there is no position to continue from, so
// the answer carries truncated rather than a cursor: a next_cursor that cannot be
// followed is worse than none, because a client follows it and gets the same page again.
// A page the budget did not cut is returned unchanged.
func ReconcileOccurrencePaging(workspaceID, symbol, op string, out []byte) []byte {
	var res map[string]json.RawMessage
	if json.Unmarshal(out, &res) != nil || res == nil {
		return out
	}
	// The member is the discriminator: an occurrence page is the only impact result
	// that carries it, and a tier page that does not is left to the tier reconciler.
	if _, ok := res["occurrences"]; !ok {
		return out
	}
	next, cut, more, trimmed := occurrencePageOffset(res)
	delete(res, "next_cursor")
	switch {
	case cut:
		// the budget replaced the rows with counts, so the list cannot be continued
		// from here and the cut is named instead of being papered over
		res["truncated"] = json.RawMessage(strconv.Quote("occurrences_removed"))
	case more || trimmed:
		// A cursor is signed when the core had more rows OR when the budget cut this
		// page's rows. The second case is the one a page's own next_cursor cannot
		// express: if the LAST page lost rows to max_chars, the core had no more to
		// offer, so the client would stop holding rows it never received and had no way
		// back to them (WS-59d-r5-1). The position is where the budget's cut reached, so
		// following it resumes exactly where the client's list ended.
		cursor, err := json.Marshal(signOffset(occurrenceRanking(workspaceID, symbol, op), next))
		if err != nil {
			return out
		}
		res["next_cursor"] = cursor
	}
	reconciled, err := json.Marshal(res)
	if err != nil {
		return out
	}
	return reconciled
}

// occurrencePageOffset returns where the next page continues after the budget's cut: the
// page's own offset plus the rows it actually delivered. cut is true when the budget
// replaced the rows with their counts, which is the one case with no position to continue
// from — an empty list is otherwise indistinguishable from a page that delivered nothing,
// and signing a cursor onto an emptied list would tell a client to keep paging a list that
// is gone. more is the core's own next_cursor: the core sends it only while rows remain, so
// its absence is the last page. trimmed is the budget's own report that it dropped rows
// from this page, which the last page needs as much as a middle one: the ladder promises
// that next_cursor continues at the first occurrence not returned, and on a trimmed last
// page that row is the one the core had already counted as beyond the list.
func occurrencePageOffset(res map[string]json.RawMessage) (next int, cut, more, trimmed bool) {
	// The map came from a decoded object, so re-marshalling it cannot fail, and reading
	// it into one typed struct is what makes the empty-list case distinguishable from a
	// member the answer never had.
	raw, err := json.Marshal(res)
	if err != nil {
		return 0, false, false, false
	}
	var page struct {
		Offset      *int               `json:"offset"`
		Occurrences *[]json.RawMessage `json:"occurrences"`
		NextCursor  *string            `json:"next_cursor"`
		Budget      *struct {
			Stage string `json:"degraded_stage"`
			Lists map[string]struct {
				Kept  int `json:"kept"`
				Total int `json:"total"`
			} `json:"lists"`
		} `json:"output_budget"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Offset == nil || page.Occurrences == nil {
		return 0, false, false, false
	}
	_, counted := res["occurrences_by_path"]
	if page.Budget != nil && page.Budget.Stage == "trimmed" {
		if l, ok := page.Budget.Lists["occurrences"]; ok {
			trimmed = l.Kept < l.Total
		}
	}
	return *page.Offset + len(*page.Occurrences), counted, page.NextCursor != nil, trimmed
}
