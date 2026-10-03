package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"

	"xmustard/api-go/internal/rustcore"
)

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
	flags, err := ImpactFlags(ImpactBlast, params)
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
	var offset int
	if err := json.Unmarshal(cursor, &offset); err != nil {
		return nil, fmt.Errorf("occurrence next_cursor is not an offset: %w", err)
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
