package compaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Message formats. Compaction reads a structured message array through a Format, which
// finds the tool calls, tool results and user text of one wire format and writes a
// replacement back into it. Every member a format does not rewrite keeps its bytes, and
// a message nothing replaced in is emitted exactly as it came in.

// Format is one wire format of a message array.
type Format interface {
	// Name is the format's name (anthropic, pi).
	Name() string
	// scan finds the calls, results and user text blocks of msgs.
	scan(msgs []*message) (*transcript, error)
	// setResult replaces the content of the result at l with text.
	setResult(m *message, l loc, text string) error
}

// Formats are the supported formats by name.
var Formats = map[string]Format{"anthropic": anthropicFormat{}, "pi": piFormat{}}

// ErrMalformed reports a message array a format cannot read.
var ErrMalformed = errors.New("malformed message array")

// loc addresses a result or text block: a message and a content block in it (-1: the
// message's whole content).
type loc struct {
	msg, block int
}

// call is one tool call: its id, tool name, decoded input and where it is.
type call struct {
	id, name string
	input    map[string]any
	at       loc
}

// result is one tool result: the id of the call it answers, the name it carries (Pi),
// whether it is an error, and its content's exact bytes.
type result struct {
	id, name string
	isError  bool
	content  json.RawMessage
	at       loc
}

// textBlock is a user text block (a guidance candidate).
type textBlock struct {
	text string
	at   loc
}

// transcript is what a format found, in message order.
type transcript struct {
	calls   []call
	results []result
	texts   []textBlock
}

// message is one message: its original bytes, decoded members and, when its content is
// an array, the content blocks. dirty marks a message a replacement changed.
type message struct {
	raw     json.RawMessage
	fields  map[string]json.RawMessage
	blocks  []json.RawMessage // content blocks; nil when content is not an array
	decoded []map[string]json.RawMessage
	dirty   bool
}

func parseMessage(raw json.RawMessage) (*message, error) {
	m := &message{raw: raw}
	if err := json.Unmarshal(raw, &m.fields); err != nil || m.fields == nil {
		return nil, fmt.Errorf("%w: a message is not a JSON object", ErrMalformed)
	}
	if c := bytes.TrimSpace(m.fields["content"]); len(c) > 0 && c[0] == '[' {
		if err := json.Unmarshal(c, &m.blocks); err != nil {
			return nil, fmt.Errorf("%w: content: %v", ErrMalformed, err)
		}
		m.decoded = make([]map[string]json.RawMessage, len(m.blocks))
	}
	return m, nil
}

// str decodes a string member ("" when absent or not a string).
func str(fields map[string]json.RawMessage, name string) string {
	var s string
	_ = json.Unmarshal(fields[name], &s)
	return s
}

func boolean(fields map[string]json.RawMessage, name string) bool {
	var b bool
	_ = json.Unmarshal(fields[name], &b)
	return b
}

// block decodes content block i of m (cached).
func (m *message) block(i int) map[string]json.RawMessage {
	if m.decoded[i] == nil {
		_ = json.Unmarshal(m.blocks[i], &m.decoded[i])
		if m.decoded[i] == nil {
			m.decoded[i] = map[string]json.RawMessage{}
		}
	}
	return m.decoded[i]
}

// setBlock replaces content block i of m with fields.
func (m *message) setBlock(i int, fields map[string]json.RawMessage) error {
	b, err := marshal(fields)
	if err != nil {
		return err
	}
	m.blocks[i], m.decoded[i], m.dirty = b, fields, true
	return nil
}

// setField replaces member name of m.
func (m *message) setField(name string, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	m.fields[name], m.dirty = b, true
	return nil
}

// encode returns m's bytes: the original when nothing changed.
func (m *message) encode() (json.RawMessage, error) {
	if !m.dirty {
		return m.raw, nil
	}
	if m.blocks != nil {
		c, err := marshal(m.blocks)
		if err != nil {
			return nil, err
		}
		m.fields["content"] = c
	}
	return marshal(m.fields)
}

// marshal encodes without HTML escaping, so text keeps its characters.
func marshal(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// textContent is content made of one text block, in the shape of the content it
// replaces: a string stays a string.
func textContent(original json.RawMessage, text string) any {
	if c := bytes.TrimSpace(original); len(c) > 0 && c[0] == '"' {
		return text
	}
	return []map[string]string{{"type": "text", "text": text}}
}

// decodeInput decodes a call's input object, keeping numbers as written.
func decodeInput(raw json.RawMessage) map[string]any {
	var in map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&in)
	return in
}

// userTexts collects the text blocks (or string content) of a user message.
func userTexts(t *transcript, m *message, i int) {
	if m.blocks == nil {
		var s string
		if json.Unmarshal(m.fields["content"], &s) == nil {
			t.texts = append(t.texts, textBlock{text: s, at: loc{i, -1}})
		}
		return
	}
	for j := range m.blocks {
		if b := m.block(j); str(b, "type") == "text" {
			t.texts = append(t.texts, textBlock{text: str(b, "text"), at: loc{i, j}})
		}
	}
}

// setText replaces the text block (or string content) at l.
func setText(m *message, l loc, text string) error {
	if l.block < 0 {
		return m.setField("content", text)
	}
	b := m.block(l.block)
	c := make(map[string]json.RawMessage, len(b))
	for k, v := range b {
		c[k] = v
	}
	t, err := marshal(text)
	if err != nil {
		return err
	}
	c["text"] = t
	return m.setBlock(l.block, c)
}

// anthropicFormat is the Anthropic Messages API: assistant tool_use blocks
// {id, name, input}; user tool_result blocks {tool_use_id, content, is_error}.
type anthropicFormat struct{}

func (anthropicFormat) Name() string { return "anthropic" }

func (anthropicFormat) scan(msgs []*message) (*transcript, error) {
	t := &transcript{}
	for i, m := range msgs {
		role := str(m.fields, "role")
		if role == "user" {
			userTexts(t, m, i)
		}
		for j := range m.blocks {
			b := m.block(j)
			switch {
			case role == "assistant" && str(b, "type") == "tool_use":
				t.calls = append(t.calls, call{id: str(b, "id"), name: str(b, "name"), input: decodeInput(b["input"]), at: loc{i, j}})
			case role == "user" && str(b, "type") == "tool_result":
				t.results = append(t.results, result{id: str(b, "tool_use_id"), isError: boolean(b, "is_error"),
					content: b["content"], at: loc{i, j}})
			}
		}
	}
	return t, nil
}

func (anthropicFormat) setResult(m *message, l loc, text string) error {
	b := m.block(l.block)
	c := make(map[string]json.RawMessage, len(b))
	for k, v := range b {
		c[k] = v
	}
	content, err := marshal(textContent(b["content"], text))
	if err != nil {
		return err
	}
	c["content"] = content
	return m.setBlock(l.block, c)
}

// piFormat is Pi's AgentMessage array: assistant toolCall blocks {id, name,
// arguments}; toolResult messages {toolCallId, toolName, content, isError}.
type piFormat struct{}

func (piFormat) Name() string { return "pi" }

func (piFormat) scan(msgs []*message) (*transcript, error) {
	t := &transcript{}
	for i, m := range msgs {
		switch str(m.fields, "role") {
		case "user":
			userTexts(t, m, i)
		case "assistant":
			for j := range m.blocks {
				if b := m.block(j); str(b, "type") == "toolCall" {
					t.calls = append(t.calls, call{id: str(b, "id"), name: str(b, "name"), input: decodeInput(b["arguments"]), at: loc{i, j}})
				}
			}
		case "toolResult":
			t.results = append(t.results, result{id: str(m.fields, "toolCallId"), name: str(m.fields, "toolName"),
				isError: boolean(m.fields, "isError"), content: m.fields["content"], at: loc{i, -1}})
		}
	}
	return t, nil
}

func (piFormat) setResult(m *message, _ loc, text string) error {
	m.blocks, m.decoded = nil, nil // the message's content is replaced whole
	return m.setField("content", textContent(m.fields["content"], text))
}
