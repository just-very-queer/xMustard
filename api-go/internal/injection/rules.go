// Package injection is xMustard's injection-safety policy (WS-56). Everything xMustard
// puts into an agent's context (recalled memory, reduced tool output, and the memory
// that hooks and session start push unasked) is agent- or tool-written text, and that
// text can carry instructions aimed at the agent reading it. The package holds four
// pieces, each a small table or an explicit check:
//
//   - a scan for instruction patterns (rules.go, scan.go): overriding instructions,
//     role reassignment, forged authority, secrecy towards the user, prompt leaks,
//     exfiltration and remote execution, chat-template and frame tokens, forged hook
//     output and invisible text;
//   - data framing (frame.go): delimited blocks that no framed text can close, and one
//     notice saying that framed text is data, never instructions;
//   - the surface policy (policy.go): pulled surfaces serve labeled text, pushed surfaces
//     admit only human-approved, unflagged and unquarantined memory, and every refusal
//     is explicit and fail-closed;
//   - quarantine of content derived from untrusted captures (sources.go).
//
// The scan does not judge imperative text as such: a memory is meant to tell an agent
// what to do ("run make check before committing"). It looks for the shapes of an
// injection, which legitimate guidance rarely has.
package injection

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule is one instruction pattern. The scan folds the text once (lowercase, every run
// of whitespace one space, a paragraph break "\n\n", and the text itself starting after
// one), so a pattern separates words with a single space. Every match starts with one of
// the rule's triggers: the scan tries the pattern only where a trigger occurs, and a
// trigger that starts with a letter or digit only at the start of a word. The pattern
// is matched from that position over at most matchWindow bytes, so a scan costs one pass
// over the text plus a short anchored match per trigger occurrence.
type Rule struct {
	// ID is the flag reported when the rule matches.
	ID string
	// Triggers are lowercase literals of the folded text; every match starts with one.
	Triggers []string
	// Pattern is RE2 over the folded text, matched from the start of a trigger. Its
	// matches are shorter than matchWindow (checked when the table is compiled).
	Pattern string
}

// Rules is the pattern table, in report order.
var Rules = []Rule{
	{ID: "override_instructions", Triggers: []string{"ignore ", "disregard ", "forget ", "override "},
		Pattern: `(?:ignore|disregard|forget|override) (?:(?:all|any|the|your|these|those|every|of) ){0,4}` +
			`(?:previous|prior|above|earlier|preceding|system|original|initial|existing|other) ` +
			`(?:instructions?|prompts?|directions?|directives?|rules|guidelines|guidance|messages?)\b` +
			`|(?:ignore|disregard|forget) (?:all of |everything )(?:the )?(?:above|previous|prior|before)\b`},
	{ID: "new_instructions", Triggers: []string{"your new ", "your real ", "your actual ", "your true ", "your secret ",
		"your hidden ", "the secret ", "the hidden "},
		Pattern: `(?:your (?:new|real|actual|true|secret|hidden)|the (?:secret|hidden)) (?:system )?` +
			`(?:instructions?|task|objective|goal|orders) ?(?:is\b|are\b|:)`},
	{ID: "role_reassignment", Triggers: []string{"you are now ", "pretend ", "act as ", "developer mode", "god mode",
		"dan mode", "jailbreak mode"},
		Pattern: `you are now (?:an?|the|my) (?:[a-z-]{1,24} ){0,3}?` +
			`(?:assistant|agent|ai|model|bot|admin|administrator|developer|system|user)\b` +
			`|pretend (?:to be|you are)\b` +
			`|act as (?:an? )?(?:unrestricted|unfiltered|jailbroken|uncensored)\b` +
			`|(?:developer|god|dan|jailbreak) mode\b`},
	{ID: "authority_claim", Triggers: []string{"message from ", "note from ", "instruction from ", "instructions from ",
		"notice from ", "directive from ", "system override", "admin override", "important", "urgent", "official"},
		Pattern: `(?:message|note|instructions?|notice|directive) from (?:the )?` +
			`(?:system|administrator|admin|developers?|anthropic|openai|operator)\b` +
			`|(?:system|admin) override\b` +
			`|(?:important|urgent|official) ?[:!-]? ?(?:system|admin|security) (?:message|notice|update|instruction)`},
	{ID: "secrecy", Triggers: []string{"do not ", "don't ", "don\xe2\x80\x99t ", "never ", "without ", "hide ", "conceal ", "keep "},
		Pattern: `(?:do not|don(?:'|\x{2019})t|never) (?:tell|inform|notify|alert) (?:the )?user` +
			`(?: ?[.!;]| about (?:this|it|that|these)\b| of (?:this|it)\b)` +
			`|(?:do not|don(?:'|\x{2019})t|never) (?:mention|reveal|show) (?:this|it|that) to (?:the )?user\b` +
			`|without (?:telling|informing|notifying|alerting) (?:the )?user\b` +
			`|(?:hide|conceal|keep) (?:this|it|that) (?:hidden )?from (?:the )?user\b`},
	{ID: "prompt_leak", Triggers: []string{"reveal ", "print ", "show ", "output ", "repeat ", "dump ", "leak ", "disclose ", "echo "},
		Pattern: `(?:reveal|print|show|output|repeat|dump|leak|disclose|echo) (?:me )?(?:your|the) ` +
			`(?:full |entire |original |hidden )?(?:system prompt|instructions above|initial instructions|hidden instructions)`},
	{ID: "exfiltration", Triggers: []string{"send ", "upload ", "post ", "exfiltrate ", "leak ", "forward ", "email ", "transmit "},
		Pattern: `(?:send|upload|post|exfiltrate|leak|forward|email|transmit) (?:the |all |your |any )?` +
			`(?:contents? of )?(?:~/\.ssh|id_rsa|id_ed25519|\.env\b|\.aws\b|\.netrc\b|api[ _-]?keys?\b|secrets\b|` +
			`credentials\b|private keys?\b|access tokens?\b|passwords\b)`},
	{ID: "remote_exec", Triggers: []string{"curl", "wget"},
		Pattern: `(?:curl|wget)\b[^\n|]{0,100}\| ?(?:sudo )?(?:ba|z|da|k)?sh\b`},
	{ID: "chat_template", Triggers: []string{"<|", "[inst]", "[/inst]", "<<sys>>", "<</sys>>"},
		Pattern: `<\|(?:im_start|im_end|im_sep|system|user|assistant|endoftext|eot_id|start_header_id|end_header_id|begin_of_text)\|>` +
			`|\[/?inst\]|<</?sys>>`},
	{ID: "turn_marker", Triggers: []string{"\n\nhuman", "\n\nassistant"},
		Pattern: `\n\n(?:human|assistant) ?:`},
	{ID: "frame_spoof", Triggers: []string{"<" + FrameTag, "</" + FrameTag, "<system", "</system", "<instructions",
		"</instructions", "<tool_", "</tool_", "<function_", "</function_", "<invoke", "</invoke", "<antml", "</antml",
		"<user_query", "</user_query"},
		Pattern: `</?(?:` + FrameTag + `|system|system-reminder|system_prompt|instructions|tool_result|tool_use|function_results|` +
			`function_calls|invoke|antml:[a-z_]{1,40}|user_query)\b[^>\n]{0,100}>`},
	{ID: "hook_spoof", Triggers: []string{`"hookspecificoutput"`, `"additionalcontext"`, `"permissiondecision"`,
		`"updatedtooloutput"`, `"updatedinput"`},
		Pattern: `"(?:hookspecificoutput|additionalcontext|permissiondecision|updatedtooloutput|updatedinput)" ?:`},
	// Invisible text: zero-width characters, bidirectional overrides and isolates, the
	// byte-order mark and Unicode tag characters (which can carry hidden ASCII). The
	// triggers are the UTF-8 lead bytes of those blocks.
	{ID: "hidden_text", Triggers: []string{"\xe2\x80", "\xe2\x81", "\xef\xbb\xbf", "\xf3\xa0"},
		Pattern: `[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{2066}-\x{2069}\x{FEFF}\x{E0000}-\x{E007F}]`},
}

// matchWindow bounds every pattern's longest match. A pattern is matched from its
// trigger over one byte more than its longest match, so the window never cuts a match
// and a \b at a match's end sees the byte after it.
const matchWindow = 512

// maxRules is how many rules the scan's match set holds.
const maxRules = 64

// compiledRule is a rule ready to run: its pattern anchored at the trigger, and the
// bytes it is matched over.
type compiledRule struct {
	id     string
	re     *regexp.Regexp
	window int
}

// trigger is one rule trigger, indexed by its first byte.
type trigger struct {
	lit  string
	rule int
}

// compiled is Rules compiled once, at package initialization; byFirst indexes their
// triggers by first byte.
var compiled, byFirst = compileRules(Rules)

// compileRules compiles the table. A malformed or duplicate rule, a rule without
// triggers, a trigger that is not lowercase, a pattern whose matches are unbounded or
// can reach matchWindow, or more rules than the match set holds is a programming error
// and panics at startup.
func compileRules(rules []Rule) ([]compiledRule, *[256][]trigger) {
	if len(rules) > maxRules {
		panic(fmt.Sprintf("injection: %d rules exceed the %d the scan tracks", len(rules), maxRules))
	}
	out := make([]compiledRule, len(rules))
	index := new([256][]trigger)
	seen := map[string]bool{FlagTruncated: true}
	for i, r := range rules {
		if r.ID == "" || seen[r.ID] || len(r.Triggers) == 0 {
			panic(fmt.Sprintf("injection: rule %d (%q) has an empty or duplicate id, or no triggers", i, r.ID))
		}
		seen[r.ID] = true
		n := maxMatchLen(r.Pattern)
		if n < 0 || n >= matchWindow {
			panic(fmt.Sprintf("injection: rule %s matches up to %d bytes, not under the %d-byte window", r.ID, n, matchWindow))
		}
		out[i] = compiledRule{id: r.ID, re: regexp.MustCompile(`^(?:` + r.Pattern + `)`), window: n + 1}
		for _, lit := range r.Triggers {
			if lit == "" || strings.IndexFunc(lit, unicode.IsUpper) >= 0 {
				panic(fmt.Sprintf("injection: rule %s trigger %q is empty or not lowercase", r.ID, lit))
			}
			index[lit[0]] = append(index[lit[0]], trigger{lit: lit, rule: i})
		}
	}
	return out, index
}

// isWordByte reports whether b is an ASCII letter, digit or underscore, as RE2's \b
// counts word characters.
func isWordByte(b byte) bool { return wordBytes[b] }

// wordBytes is isWordByte's table.
var wordBytes = func() (t [256]bool) {
	for _, r := range "_0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		t[r] = true
	}
	return t
}()

// maxMatchLen is the most bytes a match of pattern can span, or -1 when it is
// unbounded.
func maxMatchLen(pattern string) int {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		panic(fmt.Sprintf("injection: pattern %q: %v", pattern, err))
	}
	return reMaxLen(re.Simplify())
}

func reMaxLen(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			n += utf8.RuneLen(r)
		}
		return n
	case syntax.OpCharClass:
		n := 1 // the class's widest rune, from the upper bound of each range
		for i := 1; i < len(re.Rune); i += 2 {
			n = max(n, utf8.RuneLen(re.Rune[i]))
		}
		return n
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return utf8.UTFMax
	case syntax.OpCapture, syntax.OpQuest:
		return reMaxLen(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus:
		return -1
	case syntax.OpRepeat:
		sub := reMaxLen(re.Sub[0])
		if re.Max < 0 || sub < 0 {
			return -1
		}
		return re.Max * sub
	case syntax.OpConcat:
		return combineLen(re.Sub, func(a, b int) int { return a + b })
	case syntax.OpAlternate:
		return combineLen(re.Sub, func(a, b int) int { return max(a, b) })
	}
	return 0 // empty-width assertions and the empty match
}

// combineLen folds the sub-expressions' lengths with f; any unbounded one makes the
// whole unbounded.
func combineLen(subs []*syntax.Regexp, f func(a, b int) int) int {
	n := 0
	for _, s := range subs {
		m := reMaxLen(s)
		if m < 0 {
			return -1
		}
		n = f(n, m)
	}
	return n
}
