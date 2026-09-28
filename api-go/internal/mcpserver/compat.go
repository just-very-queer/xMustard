package mcpserver

import (
	"slices"
	"sort"

	"xmustard/api-go/internal/toolcompat"
)

// Tool-argument compatibility (PAR-ADP-03). BuildArgs is strict; when it rejects a
// call, toolcompat gets one chance to repair it (a misspelled key such as "file_path"
// for "path", an enum value in the wrong case, a file:// path, a "**.go" glob). Type
// coercions are not taken: a string where an integer belongs stays a strict rejection
// (numbers are never guessed). A repair is used only if the repaired arguments then
// pass BuildArgs, and every change is recorded in the result's
// _meta["xmustard/normalized"]. The write tools are never repaired: toolcompat reports
// the canonical spelling and the call fails with that as the error.

var compatTypes = map[string]toolcompat.Type{
	typeString:  toolcompat.TypeString,
	typeBoolean: toolcompat.TypeBoolean,
	typeInteger: toolcompat.TypeInteger,
}

// compatSpec is the closed toolcompat spec of a tool: its arguments, advanced
// arguments and hidden aliases (as fields of the canonical type, so BuildArgs folds
// and records them as it does for any call).
func compatSpec(t *Tool) toolcompat.Spec {
	var fields []toolcompat.Field
	add := func(name string, a Arg, required bool) {
		f := toolcompat.Field{Name: name, Type: compatTypes[a.Type], Required: required}
		if !a.List {
			f.Enum = a.Enum
		}
		fields = append(fields, f)
	}
	for _, list := range [][]Arg{t.Args, t.Advanced} {
		for _, a := range list {
			add(a.Name, a, a.Required)
		}
	}
	for alias, canon := range t.Aliases {
		if a, ok := t.arg(canon); ok {
			add(alias, a, false)
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return toolcompat.Spec{Kind: toolcompat.Kind(t.Name), Tool: t.Name, Fields: fields}
}

// buildArgsCompat validates a call with BuildArgs, falling back to a toolcompat
// repair when it is rejected. The original rejection stands when no repair applies.
func buildArgsCompat(t *Tool, raw map[string]any) (map[string]string, []Normalization, *ArgError) {
	args, aliased, aerr := BuildArgs(t, raw)
	if aerr == nil {
		return args, aliasNorms(aliased), nil
	}
	res := toolcompat.Normalize(compatSpec(t), raw)
	if res.Err != nil {
		if res.Err.Code == toolcompat.CodeNeedsRepair {
			return nil, nil, &ArgError{Tool: t.Name, Argument: res.Err.Field, Reason: res.Err.Code, Message: res.Err.Message,
				Detail: map[string]any{"expected": res.Err.Expected, "signature": res.Err.Signature}}
		}
		return nil, nil, aerr
	}
	if !res.Applied() || slices.ContainsFunc(res.Normalizations, func(n toolcompat.Normalization) bool { return n.Op == toolcompat.OpCoerce }) {
		return nil, nil, aerr
	}
	args, aliased, again := BuildArgs(t, res.Args)
	if again != nil {
		return nil, nil, aerr
	}
	norms := aliasNorms(aliased)
	for _, n := range res.Normalizations {
		norms = append(norms, Normalization{Argument: n.Field, Kind: n.Op, From: n.From, To: n.Field, Rule: n.Rule})
	}
	return args, norms, nil
}

// aliasNorms records each hidden alias folded onto its canonical argument.
func aliasNorms(aliased map[string]string) []Normalization {
	var norms []Normalization
	for from, to := range aliased {
		norms = append(norms, Normalization{Argument: to, Kind: "alias", From: from, To: to})
	}
	sort.Slice(norms, func(i, j int) bool { return norms[i].From < norms[j].From })
	return norms
}
