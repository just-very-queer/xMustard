package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// The memory write policy (WS-31, PAR-GOV-07). Every governed-memory write is checked
// server-side against the workspace's policy before anything is stored: required
// fields, allowed kinds, size caps, topic depth and the core budget, each as a row of a
// rule table. A write that sets a protected field, or whose text, topic or paths match
// a protected pattern, needs a human-approver principal (kind human, human-approver
// role, not open mode). A refusal carries every violation as {field, rule, limit,
// actual}. The policy also names the approval each pushed surface needs: any human
// approver's approval (human_approver, the default) or one cast with a presence-only
// token typed at the ops terminal (user_presence, WS-57; see AssuranceUserPresence for
// why even that is not proof a human was present).
//
// The policy is a file under the workspace's data directory. Only `xmustard-ops policy
// set`, with an admin's human token (SetMemoryPolicy), writes it; each change is an
// event in the governance store. A missing file is the default policy; an unreadable or
// malformed one refuses every write and every push until it is fixed (fail closed).

// MemoryPolicy is a workspace's memory write policy.
type MemoryPolicy struct {
	// MaxEntryChars caps a memory's content in characters (Letta's default).
	MaxEntryChars int `json:"max_entry_chars"`
	// MaxTitleChars and MaxDescriptionChars cap the title and the one-line description.
	MaxTitleChars       int `json:"max_title_chars"`
	MaxDescriptionChars int `json:"max_description_chars"`
	// CoreBudgetChars caps the served core tier: the title and content of every served
	// core entry together. The default fits ProjectionDefaultChars with its frames;
	// Letta's core budget, 65,536, is the ceiling (a projection budget cuts what does not
	// fit, labeled too_large).
	CoreBudgetChars int `json:"core_budget_chars"`
	// MaxTopicDepth caps a topic's "/"-separated segments.
	MaxTopicDepth int `json:"max_topic_depth"`
	// RequiredFields must be given by a proposal (policyFieldSet).
	RequiredFields []string `json:"required_fields"`
	// AllowedKinds limits a proposal's kind (empty: every MemoryKinds kind).
	AllowedKinds []string `json:"allowed_kinds"`
	// ProtectedFields need a human-approver principal when a write sets them
	// (protectedFieldRules); tier is always among them.
	ProtectedFields []string `json:"protected_fields"`
	// ProtectedPatterns mark memory whose text, topic or paths match as protected.
	ProtectedPatterns []ProtectedPattern `json:"protected_patterns"`
	// PushApproval is the approval each pushed memory surface needs (pushApprovalLevels).
	PushApproval map[string]string `json:"push_approval"`
}

// ProtectedPattern marks memory as protected when Field of a write matches Pattern.
type ProtectedPattern struct {
	Name    string `json:"name"`
	Field   string `json:"field"` // title | content | topic | path
	Pattern string `json:"pattern"`
}

// Push approval levels, weakest first.
const (
	PushApprovalHumanApprover = "human_approver"
	PushApprovalUserPresence  = "user_presence"
)

var pushApprovalLevels = []string{PushApprovalHumanApprover, PushApprovalUserPresence}

// pushApprovalSurfaces are the pushed memory surfaces whose approval the policy sets.
var pushApprovalSurfaces = []injection.Surface{injection.SurfaceCore, injection.SurfaceHook}

// DefaultMemoryPolicy is the policy of a workspace that has none.
func DefaultMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		MaxEntryChars: 20000, MaxTitleChars: 200, MaxDescriptionChars: 200, CoreBudgetChars: 4000, MaxTopicDepth: 6,
		RequiredFields: []string{}, AllowedKinds: []string{}, ProtectedFields: []string{protectedTier},
		ProtectedPatterns: []ProtectedPattern{},
		PushApproval: map[string]string{
			string(injection.SurfaceCore): PushApprovalHumanApprover, string(injection.SurfaceHook): PushApprovalHumanApprover,
		},
	}
}

// Violation rules.
const (
	RuleRequired         = "required"
	RuleMaxChars         = "max_chars"
	RuleMaxDepth         = "max_depth"
	RuleAllowed          = "allowed"
	RuleProtectedField   = "protected_field"
	RuleProtectedPattern = "protected_pattern"
	RuleCoreBudget       = "core_budget"
)

// MemoryPolicyViolation is one broken rule: which field, which rule, the policy's limit
// and what the write had.
type MemoryPolicyViolation struct {
	Field  string `json:"field"`
	Rule   string `json:"rule"`
	Limit  any    `json:"limit"`
	Actual any    `json:"actual"`
}

// MemoryPolicyError refuses a write that breaks the policy.
type MemoryPolicyError struct {
	Violations []MemoryPolicyViolation `json:"violations"`
}

func (e *MemoryPolicyError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		parts[i] = fmt.Sprintf("%s %s (limit %v, actual %v)", v.Field, v.Rule, v.Limit, v.Actual)
	}
	return "memory policy: " + strings.Join(parts, "; ")
}

// NeedsHumanApprover reports whether the write was refused for protection only, which
// a human approver could make.
func (e *MemoryPolicyError) NeedsHumanApprover() bool {
	return !slices.ContainsFunc(e.Violations, func(v MemoryPolicyViolation) bool {
		return v.Rule != RuleProtectedField && v.Rule != RuleProtectedPattern
	})
}

// Unwrap classes the refusal as invalid input for callers that only map error classes.
func (e *MemoryPolicyError) Unwrap() error { return ErrInvalidInput }

// memoryWrite is what a write would store, as the policy reads it.
type memoryWrite struct {
	Proposal                                 bool // a new entry: required fields and kinds apply
	Title, Content, Description, Kind, Topic string
	Paths                                    []string
	Tier, Permission, Expires                string
	Supersedes                               []string
	// Core: the entry the write changes already holds the core tier.
	Core bool
}

// policyFieldSet names the fields required_fields may list, with how a write gives one.
var policyFieldSet = map[string]func(memoryWrite) bool{
	"title":       func(w memoryWrite) bool { return strings.TrimSpace(w.Title) != "" },
	"description": func(w memoryWrite) bool { return strings.TrimSpace(w.Description) != "" },
	"kind":        func(w memoryWrite) bool { return w.Kind != "" },
	"topic":       func(w memoryWrite) bool { return w.Topic != "" },
	"paths":       func(w memoryWrite) bool { return len(w.Paths) > 0 },
}

// protectedTier is the protected field that is always protected: the core tier is
// pushed into every session.
const protectedTier = "tier"

// protectedFieldRules says when a write sets each protectable field.
var protectedFieldRules = map[string]func(memoryWrite) (actual string, set bool){
	protectedTier: func(w memoryWrite) (string, bool) {
		switch {
		case w.Tier != "":
			return w.Tier, true // choosing a tier, either way
		case w.Core:
			return govstore.TierCore, true // changing a core entry
		}
		return "", false
	},
	"permission": func(w memoryWrite) (string, bool) { return w.Permission, w.Permission == "readwrite" },
	"supersedes": func(w memoryWrite) (string, bool) { return strings.Join(w.Supersedes, ","), len(w.Supersedes) > 0 },
	"expires":    func(w memoryWrite) (string, bool) { return w.Expires, w.Expires != "" },
}

// patternFields reads each field a protected pattern may match.
var patternFields = map[string]func(memoryWrite) []string{
	"title":   func(w memoryWrite) []string { return []string{w.Title} },
	"content": func(w memoryWrite) []string { return []string{w.Content} },
	"topic":   func(w memoryWrite) []string { return []string{w.Topic} },
	"path":    func(w memoryWrite) []string { return w.Paths },
}

// sizeRule is one character cap: the field it measures and the policy's limit.
type sizeRule struct {
	field   string
	rule    string
	limit   func(MemoryPolicy) int
	measure func(memoryWrite) int
}

var sizeRules = []sizeRule{
	{"content", RuleMaxChars, func(p MemoryPolicy) int { return p.MaxEntryChars }, func(w memoryWrite) int { return utf8.RuneCountInString(w.Content) }},
	{"title", RuleMaxChars, func(p MemoryPolicy) int { return p.MaxTitleChars }, func(w memoryWrite) int { return utf8.RuneCountInString(w.Title) }},
	{"description", RuleMaxChars, func(p MemoryPolicy) int { return p.MaxDescriptionChars }, func(w memoryWrite) int { return utf8.RuneCountInString(w.Description) }},
	{"topic", RuleMaxDepth, func(p MemoryPolicy) int { return p.MaxTopicDepth }, func(w memoryWrite) int { return topicDepth(w.Topic) }},
}

func topicDepth(topic string) int {
	if topic = normalizeTopic(topic); topic == "" {
		return 0
	}
	return strings.Count(topic, "/") + 1
}

// isHumanApproverActor reports whether actor may write protected memory: a principal of
// kind human holding the human-approver role (admin holds it), never open mode, where
// there is no human approver.
func isHumanApproverActor(a ContextActor) bool {
	return !a.OpenMode && !IsOpenModeIdentity(a.ID) && a.Kind == PrincipalHuman && a.Approver
}

// check returns every rule w breaks for actor, in rule order. Protection applies only
// when actor is not a human approver.
func (p MemoryPolicy) check(w memoryWrite, actor ContextActor) []MemoryPolicyViolation {
	var out []MemoryPolicyViolation
	add := func(field, rule string, limit, actual any) {
		out = append(out, MemoryPolicyViolation{Field: field, Rule: rule, Limit: limit, Actual: actual})
	}
	if w.Proposal {
		for _, f := range p.RequiredFields {
			if given := policyFieldSet[f]; given != nil && !given(w) {
				add(f, RuleRequired, true, false)
			}
		}
		if w.Kind != "" && len(p.AllowedKinds) > 0 && !slices.Contains(p.AllowedKinds, w.Kind) {
			add("kind", RuleAllowed, strings.Join(p.AllowedKinds, ","), w.Kind)
		}
	}
	for _, r := range sizeRules {
		if n, limit := r.measure(w), r.limit(p); n > limit {
			add(r.field, r.rule, limit, n)
		}
	}
	if isHumanApproverActor(actor) {
		return out
	}
	who := fallbackString(actor.Kind, "unauthenticated")
	for _, f := range p.ProtectedFields {
		if actual, set := protectedFieldRules[f](w); set {
			add(f, RuleProtectedField, "human_approver", who+" set "+f+"="+actual)
		}
	}
	for _, pp := range p.ProtectedPatterns {
		re, err := compilePattern(pp.Pattern)
		if err != nil { // validated on load; a pattern that fails here protects the write
			add(pp.Field, RuleProtectedPattern, "human_approver", who+" (pattern "+pp.Name+" does not compile)")
			continue
		}
		if slices.ContainsFunc(patternFields[pp.Field](w), re.MatchString) {
			add(pp.Field, RuleProtectedPattern, "human_approver", who+" matched "+pp.Name)
		}
	}
	return out
}

// patternCache keeps compiled protected patterns; the policy bounds how many exist.
var patternCache sync.Map // pattern -> *regexp.Regexp

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if re, ok := patternCache.Load(pattern); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err == nil {
		patternCache.Store(pattern, re)
	}
	return re, err
}

// enforce refuses w with every violation, or returns nil.
func (p MemoryPolicy) enforce(w memoryWrite, actor ContextActor) error {
	if v := p.check(w, actor); len(v) > 0 {
		return &MemoryPolicyError{Violations: v}
	}
	return nil
}

// --- the policy config ------------------------------------------------------------

// policyBounds bounds each numeric setting.
var policyBounds = []struct {
	field    string
	min, max int
	value    func(*MemoryPolicy) *int
}{
	{"max_entry_chars", 256, 200000, func(p *MemoryPolicy) *int { return &p.MaxEntryChars }},
	{"max_title_chars", 16, 1000, func(p *MemoryPolicy) *int { return &p.MaxTitleChars }},
	{"max_description_chars", 16, 1000, func(p *MemoryPolicy) *int { return &p.MaxDescriptionChars }},
	{"core_budget_chars", 500, 65536, func(p *MemoryPolicy) *int { return &p.CoreBudgetChars }},
	{"max_topic_depth", 1, 32, func(p *MemoryPolicy) *int { return &p.MaxTopicDepth }},
}

// maxProtectedPatterns bounds the patterns every write is matched against.
const (
	maxProtectedPatterns = 32
	maxPatternLen        = 256
)

// validate checks a policy config; every problem is a violation of the config itself.
func (p MemoryPolicy) validate() error {
	var out []MemoryPolicyViolation
	add := func(field, rule string, limit, actual any) {
		out = append(out, MemoryPolicyViolation{Field: field, Rule: rule, Limit: limit, Actual: actual})
	}
	for _, b := range policyBounds {
		if n := *b.value(&p); n < b.min || n > b.max {
			add(b.field, "range", fmt.Sprintf("%d..%d", b.min, b.max), n)
		}
	}
	for _, f := range p.RequiredFields {
		if policyFieldSet[f] == nil {
			add("required_fields", RuleAllowed, strings.Join(slices.Sorted(maps.Keys(policyFieldSet)), ","), f)
		}
	}
	for _, k := range p.AllowedKinds {
		if !slices.Contains(MemoryKinds, k) {
			add("allowed_kinds", RuleAllowed, strings.Join(MemoryKinds, ","), k)
		}
	}
	for _, f := range p.ProtectedFields {
		if protectedFieldRules[f] == nil {
			add("protected_fields", RuleAllowed, strings.Join(slices.Sorted(maps.Keys(protectedFieldRules)), ","), f)
		}
	}
	if !slices.Contains(p.ProtectedFields, protectedTier) {
		add("protected_fields", RuleRequired, protectedTier, strings.Join(p.ProtectedFields, ","))
	}
	if len(p.ProtectedPatterns) > maxProtectedPatterns {
		add("protected_patterns", "max_items", maxProtectedPatterns, len(p.ProtectedPatterns))
	}
	for _, pp := range p.ProtectedPatterns {
		_, cerr := regexp.Compile(pp.Pattern)
		switch {
		case validateSafeID("pattern", pp.Name) != nil:
			add("protected_patterns.name", "safe_id", "[A-Za-z0-9_.-]+", pp.Name)
		case patternFields[pp.Field] == nil:
			add("protected_patterns."+pp.Name+".field", RuleAllowed, strings.Join(slices.Sorted(maps.Keys(patternFields)), ","), pp.Field)
		case pp.Pattern == "" || len(pp.Pattern) > maxPatternLen:
			add("protected_patterns."+pp.Name+".pattern", RuleMaxChars, maxPatternLen, len(pp.Pattern))
		case cerr != nil:
			add("protected_patterns."+pp.Name+".pattern", "compiles", true, cerr.Error())
		}
	}
	for surface, level := range p.PushApproval {
		if !slices.Contains(pushApprovalSurfaces, injection.Surface(surface)) {
			add("push_approval", RuleAllowed, "core,hook", surface)
		} else if !slices.Contains(pushApprovalLevels, level) {
			add("push_approval."+surface, RuleAllowed, strings.Join(pushApprovalLevels, ","), level)
		}
	}
	if len(out) > 0 {
		return &MemoryPolicyError{Violations: out}
	}
	return nil
}

// normalized fills unset lists and push levels with the defaults' and sorts nothing:
// the order a human wrote is kept.
func (p MemoryPolicy) normalized() MemoryPolicy {
	d := DefaultMemoryPolicy()
	for _, b := range policyBounds {
		if v := b.value(&p); *v == 0 {
			*v = *b.value(&d)
		}
	}
	p.RequiredFields = cleanStrings(p.RequiredFields)
	p.AllowedKinds = cleanStrings(p.AllowedKinds)
	if p.ProtectedFields == nil {
		p.ProtectedFields = d.ProtectedFields
	}
	p.ProtectedFields = cleanStrings(p.ProtectedFields)
	if p.ProtectedPatterns == nil {
		p.ProtectedPatterns = []ProtectedPattern{}
	}
	levels := maps.Clone(d.PushApproval)
	maps.Copy(levels, p.PushApproval)
	p.PushApproval = levels
	return p
}

// pushLevel is the approval surface needs; a surface the policy does not name needs a
// human approver. An unknown stored level never reaches here (validate refuses it).
func (p MemoryPolicy) pushLevel(surface injection.Surface) string {
	return fallbackString(p.PushApproval[string(surface)], PushApprovalHumanApprover)
}

// Digest identifies the policy's content.
func (p MemoryPolicy) Digest() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func memoryPolicyPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "memory_policy.json")
}

// ErrMemoryPolicyUnreadable: the stored policy cannot be read or is invalid, so every
// write and push is refused until `xmustard-ops policy set` replaces it.
var ErrMemoryPolicyUnreadable = errors.New("the workspace's memory policy is unreadable; replace it with xmustard-ops policy set")

// LoadMemoryPolicy reads the workspace's policy: the default when it has none, an error
// when the stored one is unreadable or invalid (fail closed).
func LoadMemoryPolicy(dataDir, workspaceID string) (MemoryPolicy, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return MemoryPolicy{}, err
	}
	raw, err := os.ReadFile(memoryPolicyPath(dataDir, workspaceID))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultMemoryPolicy(), nil
	}
	if err != nil {
		return MemoryPolicy{}, fmt.Errorf("%w: %v", ErrMemoryPolicyUnreadable, err)
	}
	var p MemoryPolicy
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return MemoryPolicy{}, fmt.Errorf("%w: %v", ErrMemoryPolicyUnreadable, err)
	}
	p = p.normalized()
	if err := p.validate(); err != nil {
		return MemoryPolicy{}, fmt.Errorf("%w: %v", ErrMemoryPolicyUnreadable, err)
	}
	return p, nil
}

// MemoryPolicyChange is the result of SetMemoryPolicy.
type MemoryPolicyChange struct {
	WorkspaceID string       `json:"workspace_id"`
	Policy      MemoryPolicy `json:"policy"`
	FromDigest  string       `json:"from_digest"`
	Digest      string       `json:"digest"`
	Changed     bool         `json:"changed"`
	Approver    string       `json:"approver"`
	Approval    string       `json:"approval"`
	Seq         int64        `json:"seq,omitempty"`
}

// ErrAdminRequired: the policy config changes only under an admin's human token.
var ErrAdminRequired = errors.New("only an admin who is a human approver may change the memory policy")

// SetMemoryPolicy replaces the workspace's policy. The checks run in order and fail
// closed: the approver (already authenticated by AuthorizeHumanApprover: kind human,
// the human-approver role, scoped, a presence-only token only typed) is also an admin;
// the new policy is valid. The change is recorded as a memory_policy event with the
// old and new digests and the approval label, then the file is replaced atomically.
func SetMemoryPolicy(dataDir, workspaceID string, p MemoryPolicy, h HumanApprover) (*MemoryPolicyChange, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if !IsHumanApprover(&h.Principal) || !h.Principal.Has(RoleAdmin) {
		RecordAuthAudit(dataDir, AuthAuditEvent{Action: "denied", Actor: h.Principal.ID, Detail: "memory policy: " + ErrAdminRequired.Error(),
			Method: "CLI", Path: "xmustard-ops policy set"})
		return nil, ErrAdminRequired
	}
	p = p.normalized()
	if err := p.validate(); err != nil {
		return nil, err
	}
	path := memoryPolicyPath(dataDir, workspaceID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	unlock, err := lockStore(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	from := ""
	if cur, err := LoadMemoryPolicy(dataDir, workspaceID); err == nil {
		from = cur.Digest()
	}
	out := &MemoryPolicyChange{WorkspaceID: workspaceID, Policy: p, FromDigest: from, Digest: p.Digest(),
		Changed: from != p.Digest(), Approver: h.Principal.ID, Approval: h.Label()}
	if !out.Changed {
		return out, nil
	}
	ctx := context.Background()
	actor := h.actor().storeActor(contextRoot(dataDir, workspaceID))
	// the file is replaced inside the transaction, so a failed write records no change
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		ev, err := tx.AppendEvent(ctx, govstore.EventInput{WorkspaceID: workspaceID, Type: govstore.EventMemoryPolicy,
			Data: map[string]any{"from_digest": from, "digest": out.Digest, "policy": p}}, actor)
		if err != nil {
			return err
		}
		out.Seq = ev.Seq
		return writeJSON(path, p)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
