package workspaceops

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// Tiered memory (WS-31; PAR-GOV-08, PAR-HAR-03, PAR-CTX-13). Served memory is in one of
// two tiers. The core tier is pushed into every session: its entries are rendered in
// full, within a character budget, into MCP initialize.instructions, SessionStart and
// the guidance block (xmustard-ops guidance write). Every other served entry is
// deferred: only its title and one-line description appear, in an index capped at 500
// lines, 20,000 characters and 50 entries per kind, and recall fetches the rest. An
// entry proposed without a tier is deferred.
//
// Changing a tier is governed. tier is a protected field of the write policy
// (memory_policy.go): only a human-approver principal proposes a core entry or moves an
// entry between tiers, and only served memory enters the core tier, within the policy's
// core budget; govstore refuses a quarantined entry the core tier. Holding the tier is
// not enough to be pushed: the core surface of the injection policy (AdmitMemory) still
// needs a human approval of the served revision, at the strength the policy asks, and
// what it admits is that revision's text, labeled with the revision (pinned).
//
// The projection is compiled from one read snapshot of promoted state, stamped with the
// workspace's memory revision (govstore MemoryRevision), and cached by (revision,
// budget, approvers, policy) until the next memory write or the earliest expiry of an
// entry it read.

// TierDeferred is the deferred tier. An entry stored with no tier is deferred.
const TierDeferred = "deferred"

// memoryTierNames maps the tier a caller names to the tier stored.
var memoryTierNames = map[string]string{govstore.TierCore: govstore.TierCore, TierDeferred: TierDeferred}

// normalizeTier reads a tier argument: "" (none given), core or deferred.
func normalizeTier(raw string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return "", nil
	}
	if tier, ok := memoryTierNames[t]; ok {
		return tier, nil
	}
	return "", fmt.Errorf("tier %q is not core or deferred: %w", raw, ErrInvalidInput)
}

// memoryChars is what an entry costs the core budget: its title, a newline and its
// content, in characters.
func memoryChars(title, content string) int {
	return utf8.RuneCountInString(title) + 1 + utf8.RuneCountInString(content)
}

// servedCoreChars sums memoryChars over the served core tier, leaving out except.
func servedCoreChars(ctx context.Context, r govstore.Reader, workspaceID, except string) (int, error) {
	core, err := r.ListEntries(ctx, govstore.EntryFilter{WorkspaceID: workspaceID, Tier: govstore.TierCore, ServedOnly: true, Limit: 1000})
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(core))
	for _, e := range core {
		if e.ID != except {
			ids = append(ids, e.ID)
		}
	}
	contents, err := r.EntryContents(ctx, ids)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, e := range core {
		if e.ID != except {
			total += memoryChars(e.Title, contents[e.ID].Content)
		}
	}
	return total, nil
}

// checkCoreBudget refuses a core entry of chars characters that would take the served
// core tier past the policy's budget.
func checkCoreBudget(ctx context.Context, r govstore.Reader, p MemoryPolicy, workspaceID, entryID string, chars int) error {
	used, err := servedCoreChars(ctx, r, workspaceID, entryID)
	if err != nil {
		return err
	}
	if total := used + chars; total > p.CoreBudgetChars {
		return &MemoryPolicyError{Violations: []MemoryPolicyViolation{{Field: "tier", Rule: RuleCoreBudget,
			Limit: p.CoreBudgetChars, Actual: total}}}
	}
	return nil
}

// SetMemoryTier moves an entry into or out of the core tier on behalf of actor, with a
// reason recorded on the tier_change event. The checks run in order and fail closed:
// valid ids, tier and reason; a readable policy; the write policy (tier is protected, so
// actor must be a human approver, and size caps apply to the entry's text); for core,
// the entry is served, not quarantined, and fits the core budget. Nothing is pushed
// until the core surface admits the entry (AdmitMemory).
func SetMemoryTier(dataDir, workspaceID, entryID, tier, reason string, actor ContextActor) (*ContextEntry, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if err := validateSafeID("entry", entryID); err != nil {
		return nil, err
	}
	t, err := normalizeTier(tier)
	if err != nil {
		return nil, err
	}
	if t == "" {
		return nil, fmt.Errorf("tier is required (core or deferred): %w", ErrInvalidInput)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrInvalidInput)
	}
	policy, err := LoadMemoryPolicy(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	sa := actor.storeActor(contextRoot(dataDir, workspaceID))
	sa.Note = reason
	var out ContextEntry
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		e, ce, err := loadEntryTx(ctx, tx, workspaceID, entryID)
		if err != nil {
			return err
		}
		contents, err := tx.EntryContents(ctx, []string{e.ID})
		if err != nil {
			return err
		}
		content := contents[e.ID].Content
		if err := policy.enforce(memoryWrite{Title: e.Title, Content: content, Description: e.Description, Kind: e.Kind,
			Topic: e.Topic, Paths: ce.Paths, Tier: t, Core: e.Tier == govstore.TierCore}, actor); err != nil {
			return err
		}
		if t == govstore.TierCore {
			switch {
			case !e.Served(time.Now()):
				return fmt.Errorf("entry %s is not served (promoted, active, unexpired); only served memory holds the core tier: %w", e.ID, ErrInvalidInput)
			case e.Metadata[govstore.MetaQuarantine] != "":
				return fmt.Errorf("entry %s is quarantined (%s) and cannot hold the core tier: %w", e.ID, e.Metadata[govstore.MetaQuarantine], ErrInvalidInput)
			}
			if err := checkCoreBudget(ctx, tx, policy, workspaceID, e.ID, memoryChars(e.Title, content)); err != nil {
				return err
			}
		}
		if _, err := tx.SetTier(ctx, e.ID, t, sa); err != nil {
			return err
		}
		out, err = entryAfter(ctx, tx, workspaceID, entryID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// TierActor is a human approver authenticated by the ops CLI as a memory writer that may
// change tiers: the human-approver role is what AuthorizeHumanApprover checked.
func (h HumanApprover) TierActor() ContextActor {
	a := h.actor()
	a.Approver, a.Admin = true, h.Principal.Has(RoleAdmin)
	return a
}

// --- the projection ------------------------------------------------------------------

// Projection budget bounds, in characters of the rendered text. The default (about
// 1.5k tokens, read once per session in initialize.instructions) holds the default core
// budget with the frames' overhead of about 90 characters per entry for up to about 18
// entries; the deferred index takes what the core leaves.
const (
	ProjectionDefaultChars = 6000
	ProjectionMinChars     = 500
	ProjectionMaxChars     = 20000
)

// Deferred index caps (PAR-GOV-08) and how much of each field a line shows.
const (
	indexMaxLines   = 500
	indexMaxChars   = 20000
	indexPerGroup   = 50
	indexFieldRunes = 120
	// maxProjectionScan bounds the served entries one compile reads.
	maxProjectionScan = 5000
)

// Why the projection leaves out a core entry: the policy admitted it but it does not
// fit the budget, or it is past the newest maxInjectionCandidates core entries.
const (
	withheldTooLarge = "too_large"
	withheldTooMany  = "too_many"
)

// ProjectedMemory is a core entry the projection pushes: the served revision whose
// human approval admitted it.
type ProjectedMemory struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	Title     string `json:"title"`
	Basis     string `json:"basis"`
	Chars     int    `json:"chars"`
	TokensEst int    `json:"tokens_est"`
	// Content and Frame are the entry's text and its data frame in Text.
	Content string `json:"-"`
	Frame   string `json:"-"`
}

// DeferredIndex describes the index the projection rendered.
type DeferredIndex struct {
	Entries   int            `json:"entries"`  // served entries the index policy admits
	Shown     int            `json:"shown"`    // lines rendered
	Withheld  map[string]int `json:"withheld"` // entries the index policy refused, by reason
	Groups    map[string]int `json:"groups"`   // admitted entries per kind
	Truncated bool           `json:"truncated"`
	Chars     int            `json:"chars"`
}

// MemoryProjection is the standing memory pushed into a session.
type MemoryProjection struct {
	WorkspaceID string            `json:"workspace_id"`
	Revision    int64             `json:"revision"`
	Budget      int               `json:"budget"`
	CoreBudget  int               `json:"core_budget"`
	Core        []ProjectedMemory `json:"core"`
	// CoreChars sums the pushed entries' memoryChars, the unit of CoreBudget.
	CoreChars int              `json:"chars_used"`
	Withheld  []WithheldMemory `json:"withheld"`
	Deferred  DeferredIndex    `json:"deferred_index"`
	// Onboarding: no served memory yet; Text carries the onboarding line.
	Onboarding bool `json:"onboarding,omitempty"`
	// Complete is false when the workspace has more served entries than one compile
	// reads (maxProjectionScan, newest first).
	Complete       bool   `json:"complete"`
	Text           string `json:"text"`
	Chars          int    `json:"text_chars"`
	TokensEst      int    `json:"tokens_est"`
	TokenEstimator string `json:"token_estimator"`
	Cached         bool   `json:"cached"`
}

// projectionKey is what a cached projection is reused for.
type projectionKey struct {
	dataDir, workspaceID string
	budget               int
}

type cachedProjection struct {
	revision   int64
	gate       string // approvalGate.digest
	validUntil string // earliest expiry among the entries read ("" none)
	p          MemoryProjection
}

// maxCachedProjections bounds the cache; past it the cache starts over.
const maxCachedProjections = 64

var projectionCache = struct {
	sync.Mutex
	m map[projectionKey]*cachedProjection
}{m: map[projectionKey]*cachedProjection{}}

// CoreProjection compiles, or reuses, the workspace's projection within budget
// characters (0: ProjectionDefaultChars). The revision, the admission and the text come
// from one read snapshot, so every pushed entry is the served revision the snapshot's
// approvals admit. Fail closed: an unreadable token store, policy or memory store is an
// error and nothing is pushed.
func CoreProjection(ctx context.Context, dataDir, workspaceID string, budget int) (*MemoryProjection, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if budget == 0 {
		budget = ProjectionDefaultChars
	}
	if budget < ProjectionMinChars || budget > ProjectionMaxChars {
		return nil, fmt.Errorf("max_chars must be between %d and %d (got %d): %w", ProjectionMinChars, ProjectionMaxChars, budget, ErrInvalidInput)
	}
	gate, err := loadApprovalGate(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	key, digest := projectionKey{dataDir, workspaceID, budget}, gate.digest()
	var out *MemoryProjection
	err = memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		rev, err := r.MemoryRevision(ctx, workspaceID)
		if err != nil {
			return err
		}
		if hit := cachedProjectionFor(key, rev, digest); hit != nil {
			out = hit
			return nil
		}
		p, validUntil, err := compileProjection(ctx, r, workspaceID, rev, budget, gate)
		if err != nil {
			return err
		}
		storeProjection(key, &cachedProjection{revision: rev, gate: digest, validUntil: validUntil, p: *p})
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func cachedProjectionFor(key projectionKey, rev int64, gate string) *MemoryProjection {
	projectionCache.Lock()
	defer projectionCache.Unlock()
	c := projectionCache.m[key]
	if c == nil || c.revision != rev || c.gate != gate || (c.validUntil != "" && c.validUntil <= time.Now().UTC().Format(storeTimeLayout)) {
		return nil
	}
	p := c.p
	p.Cached = true
	return &p
}

func storeProjection(key projectionKey, c *cachedProjection) {
	projectionCache.Lock()
	defer projectionCache.Unlock()
	if len(projectionCache.m) >= maxCachedProjections {
		clear(projectionCache.m)
	}
	projectionCache.m[key] = c
}

// servedForProjection pages through the served entries, newest update first, at most
// maxProjectionScan; complete says every one was read.
func servedForProjection(ctx context.Context, r govstore.Reader, workspaceID string) (entries []govstore.Entry, complete bool, err error) {
	var after int64
	for len(entries) < maxProjectionScan {
		page, err := r.ListEntries(ctx, govstore.EntryFilter{WorkspaceID: workspaceID, ServedOnly: true, AfterCursor: after, Limit: 1000})
		if err != nil {
			return nil, false, err
		}
		entries = append(entries, page...)
		if len(page) < 1000 {
			complete = true
			break
		}
		after = page[len(page)-1].Cursor
	}
	if len(entries) > maxProjectionScan {
		entries, complete = entries[:maxProjectionScan], false
	}
	slices.SortStableFunc(entries, func(a, b govstore.Entry) int {
		return cmp.Or(cmp.Compare(b.UpdatedAt, a.UpdatedAt), cmp.Compare(b.Cursor, a.Cursor))
	})
	return entries, complete, nil
}

// compileProjection builds the projection at rev inside r. validUntil is the earliest
// expiry among the served entries read, after which the served set changes without a
// write.
func compileProjection(ctx context.Context, r govstore.Reader, workspaceID string, rev int64, budget int,
	gate approvalGate) (*MemoryProjection, string, error) {
	served, complete, err := servedForProjection(ctx, r, workspaceID)
	if err != nil {
		return nil, "", err
	}
	validUntil := ""
	var coreIDs []string
	var overflow []WithheldMemory // core entries past one admission's candidates
	for _, e := range served {
		if e.ExpiresAt != "" && (validUntil == "" || e.ExpiresAt < validUntil) {
			validUntil = e.ExpiresAt
		}
		switch {
		case e.Tier != govstore.TierCore:
		case len(coreIDs) < maxInjectionCandidates:
			coreIDs = append(coreIDs, e.ID)
		default:
			overflow = append(overflow, WithheldMemory{ID: e.ID, Reason: withheldTooMany})
		}
	}
	adm, err := admitIn(ctx, r, workspaceID, injection.SurfaceCore, coreIDs, gate)
	if err != nil {
		return nil, "", err
	}
	adm.Withheld = append(adm.Withheld, overflow...)
	p := &MemoryProjection{WorkspaceID: workspaceID, Revision: rev, Budget: budget, CoreBudget: gate.policy.CoreBudgetChars,
		Core: []ProjectedMemory{}, Withheld: adm.Withheld, Complete: complete, TokenEstimator: evidence.TokenEstimator,
		Deferred: DeferredIndex{Withheld: map[string]int{}, Groups: map[string]int{}}}
	if len(served) == 0 {
		p.Onboarding = true
		p.Text = fmt.Sprintf("[xmustard memory] workspace %s has no promoted memory yet (memory revision %d). %s", workspaceID, rev, onboardingLine)
		p.finish()
		return p, validUntil, nil
	}
	header := fmt.Sprintf("[xmustard memory] workspace %s at memory revision %d: core memory is human-approved standing "+
		"guidance; the deferred index lists the rest by title (recall fetches it).", workspaceID, rev)
	// the core goes first, newest first; the withheld note and the index take what it
	// leaves, and a frame gives way to the note that counts what was left out
	used := hooksSize(header) + hooksSize(injection.Notice)
	for _, m := range adm.Admitted {
		frame := injection.Frame(m.Block())
		if size := hooksSize(frame); used+size <= budget {
			used += size
			p.Core = append(p.Core, ProjectedMemory{ID: m.ID, Revision: m.Revision, Title: m.Title, Basis: m.Basis,
				Chars: memoryChars(m.Title, m.Content), TokensEst: evidence.EstimateTokens(frame), Content: m.Content, Frame: frame})
			continue
		}
		p.Withheld = append(p.Withheld, WithheldMemory{ID: m.ID, Reason: withheldTooLarge})
	}
	note := withheldCoreNote(p.Withheld)
	for len(p.Core) > 0 && note != "" && used+hooksSize(note) > budget {
		last := p.Core[len(p.Core)-1]
		p.Core, used = p.Core[:len(p.Core)-1], used-hooksSize(last.Frame)
		p.Withheld = append(p.Withheld, WithheldMemory{ID: last.ID, Reason: withheldTooLarge})
		note = withheldCoreNote(p.Withheld)
	}
	parts := []string{header}
	if note != "" {
		parts, used = append(parts, note), used+hooksSize(note)
	}
	pushed := make(map[string]bool, len(p.Core))
	frames := make([]string, len(p.Core))
	for i, m := range p.Core {
		pushed[m.ID], frames[i] = true, m.Frame
		p.CoreChars += m.Chars
	}
	index := renderDeferredIndex(served, pushed, budget-used, &p.Deferred)
	if len(frames) > 0 || index != "" {
		parts = append(parts, injection.Notice)
	}
	parts = append(parts, frames...)
	if index != "" {
		parts = append(parts, index)
	}
	p.Text = joinWithin(budget, parts)
	p.finish()
	return p, validUntil, nil
}

// joinWithin joins parts with blank lines, keeping whole parts in order while they fit
// in budget characters. The parts were sized to fit; this holds the budget when an id or
// note is longer than planned.
func joinWithin(budget int, parts []string) string {
	var kept []string
	n := 0
	for _, part := range parts {
		if size := hooksSize(part); n+size <= budget+2 {
			kept, n = append(kept, part), n+size
		}
	}
	return strings.Join(kept, "\n\n")
}

// finish sets the text's size and token estimate.
func (p *MemoryProjection) finish() {
	p.Chars = utf8.RuneCountInString(p.Text)
	p.TokensEst = evidence.EstimateTokens(p.Text)
}

// hooksSize is the room a part takes when joined: its characters and a separator.
func hooksSize(part string) int { return utf8.RuneCountInString(part) + 2 }

// withheldCoreNote counts the core entries not pushed, by reason; "" when none.
func withheldCoreNote(withheld []WithheldMemory) string {
	if len(withheld) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, w := range withheld {
		counts[w.Reason]++
	}
	var parts []string
	for _, reason := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", reason, counts[reason]))
	}
	return fmt.Sprintf("[xmustard memory] %d core entr%s not pushed (%s); recall(tier=core) lists the core tier and "+
		"`xmustard-ops memory tokens` explains each.", len(withheld), plural(len(withheld), "y", "ies"), strings.Join(parts, ", "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// indexGroupOrder is the order kind groups are rendered in: remember's kinds, then
// entries with no kind (or one this build does not know).
var indexGroupOrder = append(slices.Clone(MemoryKinds), indexOtherGroup)

const indexOtherGroup = "other"

// indexFrameRoom is what the index's heading line, frame tags and truncation line take
// besides its entry lines.
const indexFrameRoom = 400

// renderDeferredIndex renders the served entries not pushed in full as one data frame
// of "- id: title — description" lines grouped by kind, newest first, within room
// characters and the index caps, with a truncation line when any was left out; "" when
// no entry is admitted or none fits. Each entry passes the index surface's policy first
// (served, not quarantined, no instruction pattern in its title or description).
func renderDeferredIndex(served []govstore.Entry, pushed map[string]bool, room int, idx *DeferredIndex) string {
	groups := map[string][]govstore.Entry{}
	for _, e := range served {
		if pushed[e.ID] {
			continue
		}
		d := injection.Decide(injection.SurfaceIndex, injection.Candidate{
			Basis: injection.BasisOf(e.Promoted, e.VerificationMode, false), Quarantine: e.Metadata[govstore.MetaQuarantine],
			Scan: injection.Scan(e.Title, e.Description)})
		if !d.Admit {
			idx.Withheld[d.Reason]++
			continue
		}
		g := e.Kind
		if !slices.Contains(MemoryKinds, g) {
			g = indexOtherGroup
		}
		groups[g] = append(groups[g], e)
		idx.Groups[g]++
		idx.Entries++
	}
	limit := min(indexMaxChars, room-indexFrameRoom)
	if idx.Entries == 0 || limit <= 0 {
		idx.Truncated = idx.Entries > 0
		return ""
	}
	var b strings.Builder
	lines, chars := 0, 0
	write := func(line string) bool {
		n := utf8.RuneCountInString(line) + 1
		if lines == indexMaxLines || chars+n > limit {
			return false
		}
		b.WriteString(line + "\n")
		lines, chars = lines+1, chars+n
		return true
	}
	for _, g := range indexGroupOrder {
		list := groups[g]
		if len(list) == 0 || !write(fmt.Sprintf("%s (%d):", g, len(list))) {
			continue
		}
		for _, e := range list[:min(len(list), indexPerGroup)] {
			if !write(indexLine(e)) {
				break
			}
			idx.Shown++
		}
	}
	if idx.Shown == 0 {
		idx.Truncated = true
		return ""
	}
	idx.Truncated = idx.Shown < idx.Entries
	text := strings.TrimRight(b.String(), "\n")
	if idx.Truncated {
		text += fmt.Sprintf("\n… %d of %d deferred entries shown (at most %d per kind, %d lines, %d characters, and the budget)",
			idx.Shown, idx.Entries, indexPerGroup, indexMaxLines, indexMaxChars)
	}
	idx.Chars = utf8.RuneCountInString(text)
	return "Deferred memory index (recall(entry_id=ID) fetches one; recall(tier=deferred) or recall(kind=K) lists them):\n" +
		injection.Frame(injection.Block{Kind: "memory_index", Trust: "served", Text: text})
}

// indexLine is one deferred entry: its id, title and one-line description, each cut.
func indexLine(e govstore.Entry) string {
	line := "- " + e.ID + ": " + oneLine(e.Title, indexFieldRunes)
	if d := oneLine(e.Description, indexFieldRunes); d != "" {
		line += " — " + d
	}
	return line
}

// onboardingLine is what the projection says when a workspace has no promoted memory.
var onboardingLine = func() string {
	cats := make([]string, len(onboardingChecklist))
	for i, c := range onboardingChecklist {
		cats[i] = c.Prompt
	}
	return "Propose, with remember and the paths each is about: " + strings.Join(cats, "; ") + " (ground's onboarding lists them)."
}()

// --- the token report ------------------------------------------------------------------

// MemoryTokenReport is the core-memory token report (PAR-CTX-13): what the projection
// pushes and what each core entry costs, as `xmustard-ops memory tokens` prints it.
type MemoryTokenReport struct {
	WorkspaceID    string            `json:"workspace_id"`
	Revision       int64             `json:"revision"`
	TokenEstimator string            `json:"token_estimator"`
	Core           []ProjectedMemory `json:"core"`
	CoreChars      int               `json:"core_chars"`
	CoreBudget     int               `json:"core_budget"`
	CoreTokensEst  int               `json:"core_tokens_est"`
	Withheld       []WithheldMemory  `json:"withheld"`
	Deferred       DeferredIndex     `json:"deferred_index"`
	Projection     struct {
		Budget    int `json:"budget"`
		Chars     int `json:"chars"`
		TokensEst int `json:"tokens_est"`
	} `json:"projection"`
}

// MemoryTokens reports the projection at budget characters.
func MemoryTokens(ctx context.Context, dataDir, workspaceID string, budget int) (*MemoryTokenReport, error) {
	p, err := CoreProjection(ctx, dataDir, workspaceID, budget)
	if err != nil {
		return nil, err
	}
	out := &MemoryTokenReport{WorkspaceID: workspaceID, Revision: p.Revision, TokenEstimator: p.TokenEstimator, Core: p.Core,
		CoreChars: p.CoreChars, CoreBudget: p.CoreBudget, Withheld: p.Withheld, Deferred: p.Deferred}
	for _, m := range p.Core {
		out.CoreTokensEst += m.TokensEst
	}
	out.Projection.Budget, out.Projection.Chars, out.Projection.TokensEst = p.Budget, p.Chars, p.TokensEst
	return out, nil
}

// MemoryBudget is ground's memory_budget (PAR-CTX-13): the projection a session is
// pushed, at the default budget, in counts.
type MemoryBudget struct {
	Revision        int64  `json:"revision"`
	CoreEntries     int    `json:"core_entries"`
	CoreChars       int    `json:"core_chars"`
	CoreBudget      int    `json:"core_budget"`
	CoreWithheld    int    `json:"core_withheld"`
	DeferredIndexed int    `json:"deferred_indexed"`
	ProjectionChars int    `json:"projection_chars"`
	TokensEst       int    `json:"tokens_est"`
	TokenEstimator  string `json:"token_estimator"`
}

func memoryBudgetOf(p *MemoryProjection) *MemoryBudget {
	return &MemoryBudget{Revision: p.Revision, CoreEntries: len(p.Core), CoreChars: p.CoreChars, CoreBudget: p.CoreBudget,
		CoreWithheld: len(p.Withheld), DeferredIndexed: p.Deferred.Entries, ProjectionChars: p.Chars, TokensEst: p.TokensEst,
		TokenEstimator: p.TokenEstimator}
}

// ProjectionOffered maps each core entry the projection pushed to its frame, the text
// whose presence in a hook answer marks it seen.
func (p *MemoryProjection) ProjectionOffered() map[string]string {
	out := make(map[string]string, len(p.Core))
	for _, m := range p.Core {
		out[m.ID] = m.Frame
	}
	return out
}
