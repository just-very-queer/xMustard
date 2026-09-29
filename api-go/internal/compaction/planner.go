package compaction

import (
	"fmt"
	"strings"
)

// Planner state. Todo and plan tools re-send the whole list on every call, so an old
// planner result is mostly a stale copy of the latest one. Every planner result but the
// most recent is replaced by what it changed: the todo delta against the list before it
// (added, removed, completed, reopened, newly ready), the list's counts and the next
// action it named.

// todo is one planner item.
type todo struct {
	id, content, status string
	ready               bool
}

// plannerState is what one planner exchange holds.
type plannerState struct {
	plan     map[string]any
	todos    []todo
	original []todo // the list before the call, when the result reports it
	hasOrig  bool
	next     map[string]any
}

// plannerSummaries describe each planner tool (name without underscores or case) from
// its state and the previous planner state in the history.
var plannerSummaries = map[string]func(s, prev *plannerState) []string{
	"todowrite": func(s, prev *plannerState) []string {
		return []string{"todo_write updated todos", s.delta(prev), s.collection(), s.nextAction()}
	},
	"todoread": func(s, _ *plannerState) []string {
		return []string{"todo_read snapshot", s.collection(), s.nextAction()}
	},
	"createplan": func(s, _ *plannerState) []string {
		return []string{"create_plan stored " + planSummary(s.plan), s.collection(), s.nextAction()}
	},
	"readproject": func(s, _ *plannerState) []string {
		return []string{"read_project snapshot", planSummary(s.plan), s.collection(), s.nextAction()}
	},
	"updateproject": func(s, prev *plannerState) []string {
		return []string{"update_project refreshed " + planSummary(s.plan), s.delta(prev), s.collection(), s.nextAction()}
	},
	"updateplan": func(s, prev *plannerState) []string {
		return []string{"update_plan updated the plan", s.delta(prev), s.collection()}
	},
}

// plannerKey is a planner tool's table key.
func plannerKey(name string) string {
	return strings.ReplaceAll(normalizeTool(name), "_", "")
}

// isPlanner reports whether x is a planner tool's exchange.
func isPlanner(x *exchange) bool {
	_, ok := plannerSummaries[plannerKey(toolName(x))]
	return ok && !x.result.isError
}

// plannerStateOf reads the planner state from the result's structured payload
// (structured, attachments) and the call's input (Claude/OpenCode todos, Codex plan).
func plannerStateOf(x *exchange) *plannerState {
	p := payloadOf(x)
	structured, _ := p["structured"].(map[string]any)
	attachments, _ := p["attachments"].(map[string]any)
	s := &plannerState{plan: firstMap(structured["updated_plan"], structured["plan"])}
	s.todos = decodeTodos(firstPresent(structured["final_todos"], structured["todos"], attachments["updated_todos"],
		mapValue(s.plan, "todos"), x.call.input["todos"], x.call.input["plan"]))
	if orig := firstPresent(attachments["original_todos"], structured["initial_todos"]); orig != nil {
		s.original, s.hasOrig = decodeTodos(orig), true
	}
	s.next, _ = structured["next_action"].(map[string]any)
	return s
}

// plannerSummary is the summary of a planner exchange given the previous planner state.
func plannerSummary(x *exchange, s, prev *plannerState) string {
	parts := plannerSummaries[plannerKey(toolName(x))](s, prev)
	kept := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}

func (s *plannerState) collection() string {
	if len(s.todos) == 0 {
		return "0 todos"
	}
	counts := map[string]int{}
	ready := 0
	for _, t := range s.todos {
		counts[group(t.status)]++
		if t.ready {
			ready++
		}
	}
	parts := []string{fmt.Sprintf("%d todos", len(s.todos))}
	if ready > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", ready))
	}
	for _, g := range []string{"in progress", "completed", "pending"} {
		if counts[g] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[g], g))
		}
	}
	return strings.Join(parts, ", ")
}

// statusGroup maps a todo status to its count; anything unlisted is pending.
var statusGroup = map[string]string{
	"completed": "completed", "done": "completed", "in_progress": "in progress", "in-progress": "in progress",
}

func group(status string) string {
	if g, ok := statusGroup[status]; ok {
		return g
	}
	return "pending"
}

func completed(t todo) bool { return group(t.status) == "completed" }

// delta compares the list with the one before the call: the result's original list, or
// else the previous planner state in the history.
func (s *plannerState) delta(prev *plannerState) string {
	before := s.original
	if !s.hasOrig && prev != nil {
		before = prev.todos
	}
	if len(before) == 0 && len(s.todos) == 0 {
		return ""
	}
	index := func(ts []todo) map[string]todo {
		m := make(map[string]todo, len(ts))
		for i, t := range ts {
			m[todoKey(t, i)] = t
		}
		return m
	}
	was, now := index(before), index(s.todos)
	var added, removed, done, reopened, ready int
	for k, t := range now {
		p, ok := was[k]
		switch {
		case !ok:
			added++
			ready += b2i(t.ready)
			done += b2i(completed(t))
		default:
			ready += b2i(!p.ready && t.ready)
			done += b2i(!completed(p) && completed(t))
			reopened += b2i(completed(p) && !completed(t))
		}
	}
	for k := range was {
		if _, ok := now[k]; !ok {
			removed++
		}
	}
	var parts []string
	for _, c := range []struct {
		n      int
		format string
	}{{added, "+%d added"}, {removed, "-%d removed"}, {done, "%d completed"}, {reopened, "%d reopened"}, {ready, "%d newly ready"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf(c.format, c.n))
		}
	}
	if len(parts) == 0 {
		return "todo set unchanged"
	}
	return strings.Join(parts, ", ")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func todoKey(t todo, i int) string {
	switch {
	case t.id != "":
		return "id:" + t.id
	case t.content != "":
		return "content:" + t.content
	}
	return fmt.Sprintf("index:%d", i)
}

// nextAction names the next action a planner result recommends: its title (or else its
// type) and the tool to use.
func (s *plannerState) nextAction() string {
	head := firstNonEmpty(prefixed("next ", cleanText(anyString(s.next["title"]), 80)),
		prefixed("next_action ", cleanText(anyString(s.next["type"]), 40)))
	if tool := cleanText(anyString(s.next["recommended_tool"]), 40); head != "" && tool != "" {
		head += " via " + tool
	}
	return head
}

// prefixed is prefix+s, or "" when s is empty.
func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

// planSummary names a plan by its title (or summary) and counts its steps, phases and
// todos.
func planSummary(plan map[string]any) string {
	label := "planner state"
	if title := cleanText(firstNonEmpty(anyString(plan["title"]), anyString(plan["summary"])), 72); title != "" {
		label = `plan "` + title + `"`
	}
	parts := []string{label}
	for _, member := range []string{"steps", "phases", "todos"} {
		if n := len(asSlice(plan[member])); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, member))
		}
	}
	return strings.Join(parts, ", ")
}

func decodeTodos(v any) []todo {
	var out []todo
	for _, item := range asSlice(v) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, todo{
			id:      anyString(m["id"]),
			content: firstNonEmpty(anyString(m["content"]), anyString(m["step"])),
			status:  strings.ToLower(anyString(m["status"])),
			ready:   truthy(m["ready"]),
		})
	}
	return out
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func firstMap(vals ...any) map[string]any {
	for _, v := range vals {
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			return m
		}
	}
	return nil
}

// firstPresent is the first value that is a non-empty list.
func firstPresent(vals ...any) any {
	for _, v := range vals {
		if s, ok := v.([]any); ok && len(s) > 0 {
			return s
		}
	}
	return nil
}

func mapValue(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}
