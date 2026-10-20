package hooks

import "encoding/json"

// Client is one host's hook protocol: the events it posts to /api/hooks/<Name>/<Event>,
// the body format its PostToolUse capture decodes, and how an answer is written in the
// host's output schema. The event handlers are shared: a Codex PreToolUse is answered
// by the same code as a Claude Code one, and only the answer's shape differs.
type Client struct {
	Name string
	// Format is the evidence.HookFormat of the client's PostToolUse bodies.
	Format string
	Events []Event
	reply  func(Event, Result) *Reply
}

// Result is what an event's handler produced, before any client's output schema.
type Result struct {
	Context string
	// Updated is the shaped replacement of a tool's output (evidence.ShapeResult
	// Payload): Claude Code's updatedToolOutput, Codex's {"decision":"block","reason"}.
	Updated json.RawMessage
	// Watch is the watch list (nil says nothing; an empty one clears it).
	Watch []string
}

// Reply is a written answer: the JSON body the client reads and the additional context
// it carries, whose memory frames count as delivered once the body is written.
type Reply struct {
	Body    []byte
	Context string
}

// Reply writes the answer to one of the client's events; nil when there is nothing to
// say, which the route answers with an empty 200.
func (c Client) Reply(ev Event, r Result) *Reply { return c.reply(ev, r) }

// Event returns the client's event with a route, if any.
func (c Client) Event(name string) (Event, bool) {
	for _, e := range c.Events {
		if e.Name == name {
			return e, true
		}
	}
	return Event{}, false
}

// Clients are the hook clients the service routes.
var Clients = []Client{Claude, Codex, Cursor}
