package compaction

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"xmustard/api-go/internal/evidence"
)

// Recovery. Every tool result compaction replaces is retained first, through a
// Retainer, and its stub carries the returned handle: the exact bytes of the replaced
// content are one resources/read away. A pass whose originals cannot be retained
// replaces nothing.

// Original is the content of one tool result being replaced.
type Original struct {
	ToolUseID string
	Tool      string
	IsError   bool
	// Content is the replaced content's exact JSON bytes (a string or a block array).
	Content []byte
}

// Ref locates one retained original: a handle and the byte range of the original in
// what the handle retains.
type Ref struct {
	Handle string
	// ResourceURI reads exactly the original (the handle's URI with offset and length).
	ResourceURI    string
	Offset, Length int64
	ExpiresAt      time.Time
}

// Retainer retains originals and returns one Ref per original, in order.
type Retainer interface {
	Retain(ctx context.Context, originals []Original) ([]Ref, error)
}

// CaptureTool is the tool name a compaction bundle is captured under. The evidence
// store labels it untrusted_capture:xmustard_compaction (injection.CaptureQuarantine):
// a bundle mixes the output of several tools, some of them untrusted, so it is served
// with the quarantine label (fail closed, WS-56).
const CaptureTool = "xmustard_compaction"

// StoreRetainer retains one pass's originals as one evidence capture (WS-08): the
// originals are written one after another, each followed by a newline, and each Ref is
// the capture's handle with the original's byte range. One capture per pass costs one
// fsync and one minimum quota charge, however many results the pass replaces.
type StoreRetainer struct {
	Store        *evidence.Store
	WorkspaceID  string
	RepoScope    string
	Actor        string
	AuthEnforced bool
	Issuer       string
	SessionID    string
}

// Retain implements Retainer.
func (r StoreRetainer) Retain(ctx context.Context, originals []Original) ([]Ref, error) {
	if len(originals) == 0 {
		return nil, nil
	}
	sp, err := r.Store.NewSpool(r.WorkspaceID)
	if err != nil {
		return nil, err
	}
	ranges := make([][2]int64, len(originals))
	var off int64
	for i, o := range originals {
		if _, err := sp.Write(o.Content); err != nil {
			sp.Discard()
			return nil, err
		}
		if _, err := sp.Write([]byte{'\n'}); err != nil {
			sp.Discard()
			return nil, err
		}
		ranges[i] = [2]int64{off, int64(len(o.Content))}
		off += int64(len(o.Content)) + 1
	}
	d, err := r.Store.Capture(ctx, sp, evidence.CaptureRequest{
		WorkspaceID: r.WorkspaceID, RepoScope: r.RepoScope, Actor: r.Actor, AuthEnforced: r.AuthEnforced,
		Issuer: r.Issuer, SessionID: r.SessionID, Tool: CaptureTool, Status: 200,
		ContentType: "application/x-ndjson", Retain: true,
	})
	if err != nil {
		return nil, err
	}
	if d.Handle == "" {
		return nil, errors.New("compaction: the evidence store retained no original")
	}
	exp, _ := time.Parse(time.RFC3339Nano, d.ExpiresAt)
	refs := make([]Ref, len(originals))
	for i, rg := range ranges {
		refs[i] = Ref{Handle: d.Handle, Offset: rg[0], Length: rg[1], ExpiresAt: exp,
			ResourceURI: d.ResourceURI + "&offset=" + strconv.FormatInt(rg[0], 10) + "&length=" + strconv.FormatInt(rg[1], 10)}
	}
	return refs, nil
}

// MemoRetainer remembers the Ref of each original it retained, by the original's tool
// use id and content digest, and returns it again while it lives. A context hook that
// compacts the client's raw history on every turn then writes the same stub bytes each
// turn, so the compacted prefix stays byte-stable for the provider's prompt cache and
// no original is retained twice. It keeps at most Max refs (the oldest go first).
type MemoRetainer struct {
	Next Retainer
	Max  int
	// MinTTL is the least life a remembered handle must have left to be reused.
	MinTTL time.Duration
	now    func() time.Time

	mu    sync.Mutex
	memo  map[[32]byte]Ref
	order [][32]byte
}

// DefaultMemo is the number of refs a MemoRetainer keeps when Max is 0.
const DefaultMemo = 512

// NewMemoRetainer wraps next.
func NewMemoRetainer(next Retainer, max int) *MemoRetainer {
	if max <= 0 {
		max = DefaultMemo
	}
	return &MemoRetainer{Next: next, Max: max, MinTTL: time.Hour, now: time.Now, memo: map[[32]byte]Ref{}}
}

// SetClock replaces the clock (tests).
func (m *MemoRetainer) SetClock(now func() time.Time) { m.now = now }

// Retain implements Retainer: remembered originals reuse their Ref; the rest are
// retained by Next in one call.
func (m *MemoRetainer) Retain(ctx context.Context, originals []Original) ([]Ref, error) {
	refs := make([]Ref, len(originals))
	keys := make([][32]byte, len(originals))
	var missing []int
	m.mu.Lock()
	now := m.now()
	for i, o := range originals {
		keys[i] = memoKey(o)
		if ref, ok := m.memo[keys[i]]; ok && ref.ExpiresAt.Sub(now) >= m.MinTTL {
			refs[i] = ref
			continue
		}
		missing = append(missing, i)
	}
	m.mu.Unlock()
	if len(missing) == 0 {
		return refs, nil
	}
	batch := make([]Original, len(missing))
	for j, i := range missing {
		batch[j] = originals[i]
	}
	got, err := m.Next.Retain(ctx, batch)
	if err != nil {
		return nil, err
	}
	if len(got) != len(batch) {
		return nil, fmt.Errorf("compaction: retainer returned %d refs for %d originals", len(got), len(batch))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for j, i := range missing {
		refs[i] = got[j]
		if _, ok := m.memo[keys[i]]; !ok {
			m.order = append(m.order, keys[i])
		}
		m.memo[keys[i]] = got[j]
	}
	for len(m.order) > m.Max {
		delete(m.memo, m.order[0])
		m.order = m.order[1:]
	}
	return refs, nil
}

func memoKey(o Original) [32]byte {
	h := sha256.New()
	h.Write([]byte(strconv.Itoa(len(o.ToolUseID))))
	h.Write([]byte{0})
	h.Write([]byte(o.ToolUseID))
	h.Write(o.Content)
	var k [32]byte
	h.Sum(k[:0])
	return k
}
