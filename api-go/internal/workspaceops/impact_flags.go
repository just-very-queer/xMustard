package workspaceops

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"xmustard/api-go/internal/gitdiff"
)

// Impact v2 (WS-35): the query parameters each impact form takes, the core flag each
// becomes, and the check its value must pass. A value out of range is refused, never
// clamped; a parameter of another form is refused too, so a caller never believes a
// filter applied that did not.

// ImpactForm is one shape of an impact read.
type ImpactForm string

const (
	// ImpactBlast is symbol= (blast radius by depth tier).
	ImpactBlast ImpactForm = "symbol"
	// ImpactFile is path= (WS-FIX-05): the files referencing a file's symbols.
	ImpactFile ImpactForm = "path"
	// ImpactTrace is from=&to= (shortest path with per-hop edges).
	ImpactTrace ImpactForm = "trace"
	// ImpactChanges is scope= (the symbols a scoped diff touches, and their dependents).
	ImpactChanges ImpactForm = "scope"
	// ImpactSinceIndex is the no-argument read: changes since the index baseline.
	ImpactSinceIndex ImpactForm = "since_index"
)

// RelationTypes are the relation types impact walks (PAR-IMP-01's edge kinds).
var RelationTypes = []string{"CALLS", "IMPORTS", "EXTENDS", "IMPLEMENTS", "USES", "ACCESSES", "HAS_METHOD", "HAS_PROPERTY"}

// ChangeScopes are the scope names (gitdiff's vocabulary; workspace is all).
var ChangeScopes = func() []string {
	names := make([]string, len(gitdiff.Scopes))
	for i, s := range gitdiff.Scopes {
		names[i] = s.Name
	}
	return names
}()

type impactParam struct {
	param, flag string
	check       func(string) error
}

func intIn(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		return nil
	}
}

func oneOf(values ...string) func(string) error {
	return func(v string) error {
		for _, ok := range values {
			if v == ok {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(values, ", "))
	}
}

func listOf(values ...string) func(string) error {
	one := oneOf(values...)
	return func(v string) error {
		for _, e := range strings.Split(v, ",") {
			if err := one(strings.ToUpper(strings.TrimSpace(e))); err != nil {
				return err
			}
		}
		return nil
	}
}

func unitInterval(v string) error {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < 0 || f > 1 {
		return fmt.Errorf("must be a number from 0 to 1")
	}
	return nil
}

func boolean(v string) error { return oneOf("true", "false")(v) }

// checkoutPath takes an absolute path: a relative one would resolve against the core
// process's working directory.
func checkoutPath(v string) error {
	if len(v) > 4096 || strings.ContainsRune(v, 0) || !filepath.IsAbs(v) {
		return fmt.Errorf("is not an absolute path")
	}
	return nil
}

// cursorParam lists cursor= as an argument the blast and diff-scope forms accept, so
// the form tables decide whether a cursor applies. It is never forwarded as a core flag
// (ResolveImpactCursor deletes it before ImpactFlags runs and leaves the offset it
// names); the flag name here is only the parameter's identity in a refusal message.
var cursorParam = impactParam{"cursor", "cursor", func(string) error { return nil }}

var (
	walkParams = []impactParam{
		{"relation_types", "relation-types", listOf(RelationTypes...)},
		{"min_confidence", "min-confidence", unitInterval},
		{"include_tests", "include-tests", boolean},
	}
	pageParams = []impactParam{
		{"limit", "limit", intIn(1, 500)},
		{"offset", "offset", intIn(0, 50_000)},
		{"summary_only", "summary-only", boolean},
		{"timeout_ms", "timeout-ms", intIn(1, 60_000)},
	}
	impactParams = map[ImpactForm][]impactParam{
		ImpactBlast: concat(walkParams, pageParams, []impactParam{
			{"depth", "max-depth", intIn(1, 32)},
			{"direction", "direction", oneOf("upstream", "downstream")},
			cursorParam,
		}),
		ImpactTrace: concat(walkParams, []impactParam{{"depth", "max-depth", intIn(1, 30)}}),
		// the legacy file-level walk takes no filters; its core takes depth positionally
		ImpactFile: {{"depth", "max-depth", intIn(1, 4)}},
		ImpactChanges: concat(walkParams, pageParams, []impactParam{
			{"depth", "max-depth", intIn(1, 32)},
			{"scope", "scope", oneOf(ChangeScopes...)},
			{"base", "base", gitdiff.CheckRef},
			{"head", "head", gitdiff.CheckRef},
			{"worktree", "worktree", checkoutPath},
			{"symbols", "symbols", intIn(1, 500)},
			cursorParam,
		}),
		ImpactSinceIndex: nil,
	}
)

func concat(lists ...[]impactParam) []impactParam {
	var out []impactParam
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// impactTargets are the arguments that name a form's targets: the core takes them as
// positional arguments, not flags, so CheckImpactRequest strips them.
var impactTargets = map[ImpactForm][]string{ImpactBlast: {"symbol"}, ImpactFile: {"path"}, ImpactTrace: {"from", "to"}}

// ImpactFormOf picks the impact read params name: from and to together are a trace,
// symbol a blast radius, path a file's, scope a diff-to-symbol read, and none of them
// the changes since the index baseline. from or to alone is refused; two targets are
// refused by CheckImpactRequest, since each form's table lacks the others'.
func ImpactFormOf(params map[string]string) (ImpactForm, error) {
	from, to := params["from"] != "", params["to"] != ""
	switch {
	case from != to:
		return "", fmt.Errorf("a trace needs both from and to: %w", ErrInvalidInput)
	case from:
		return ImpactTrace, nil
	case params["symbol"] != "":
		return ImpactBlast, nil
	case params["path"] != "":
		return ImpactFile, nil
	case params["scope"] != "":
		return ImpactChanges, nil
	}
	return ImpactSinceIndex, nil
}

// CheckImpactRequest picks the form of an impact request and checks every parameter
// against it, so a request is refused whole before any core runs. It returns the form
// and the parameters less the form's targets (the ImpactFlags input). A cursor= is
// resolved into the offset it names by ResolveImpactCursor before this runs, so this
// sees one page position, not two.
func CheckImpactRequest(params map[string]string) (ImpactForm, map[string]string, error) {
	form, err := ImpactFormOf(params)
	if err != nil {
		return "", nil, err
	}
	rest := maps.Clone(params)
	for _, k := range impactTargets[form] {
		delete(rest, k)
	}
	if _, err := ImpactFlags(form, rest); err != nil {
		return "", nil, err
	}
	return form, rest, nil
}

// ResolveImpactCursor turns one impact request's cursor= into the offset the page
// continues from, so the core takes one page position (--offset) and cursor= and
// offset= can never disagree. The cursor is signed for the ranking the caller's own
// arguments describe, so a cursor from another ranking, another form or another
// workspace is refused here. A request with both is refused too: one names the
// position twice. The cursor is deleted from params, so it never reaches the core as a
// flag. An absent cursor is the first page; one that is present but empty is refused,
// because silently treating it as the first page would answer a malformed cursor with
// the wrong page instead of an error.
func ResolveImpactCursor(workspaceID string, q url.Values, params map[string]string) error {
	cursor := params["cursor"]
	if cursor == "" {
		if _, given := q["cursor"]; given {
			return fmt.Errorf("cursor is empty: pass the next_cursor of the previous page, or leave cursor out for the first: %w", ErrInvalidInput)
		}
		return nil
	}
	if params["offset"] != "" {
		return fmt.Errorf("cursor and offset both name the page position: %w", ErrInvalidInput)
	}
	if _, given := q["offset"]; given {
		// An offset given but empty names no position, and silently letting the cursor
		// supply one would answer a request the caller wrote twice.
		return fmt.Errorf("offset is empty: give it a page position or leave it out to use cursor: %w", ErrInvalidInput)
	}
	offset, err := DecodeImpactCursor(workspaceID, q, cursor)
	if err != nil {
		return err
	}
	delete(params, "cursor")
	params["offset"] = strconv.Itoa(offset)
	return nil
}

// impactParamNames is every parameter any form takes, the forms' own targets
// (impactTargets) included: a caller strips its form's before ImpactFlags.
func impactParamNames() map[string]bool {
	names := map[string]bool{}
	for _, targets := range impactTargets {
		for _, t := range targets {
			names[t] = true
		}
	}
	for _, table := range impactParams {
		for _, p := range table {
			names[p.param] = true
		}
	}
	return names
}

// ImpactFlags checks params against form and returns the core flags, in the table's
// order. A parameter of another form, or a value that fails its check, is
// ErrInvalidInput. A cursor= is not a flag and is not read here; CheckImpactRequest
// decodes it into the offset it names and drops it before it gets this far.
func ImpactFlags(form ImpactForm, params map[string]string) ([]string, error) {
	table, known := impactParams[form]
	if !known {
		return nil, fmt.Errorf("unknown impact form %q: %w", form, ErrInvalidInput)
	}
	takes := map[string]bool{}
	for _, p := range table {
		takes[p.param] = true
	}
	for name := range impactParamNames() {
		if params[name] != "" && !takes[name] {
			return nil, fmt.Errorf("%s does not apply to impact %s: %w", name, form, ErrInvalidInput)
		}
	}
	var out []string
	for _, p := range table {
		v := params[p.param]
		if v == "" {
			continue
		}
		if err := p.check(v); err != nil {
			return nil, fmt.Errorf("%s %q %v: %w", p.param, v, err, ErrInvalidInput)
		}
		out = append(out, "--"+p.flag+"="+v)
	}
	if form == ImpactChanges {
		if err := gitdiff.CheckScope(params["scope"], params["base"], params["head"]); err != nil {
			return nil, fmt.Errorf("%v: %w", err, ErrInvalidInput)
		}
	}
	return out, nil
}
