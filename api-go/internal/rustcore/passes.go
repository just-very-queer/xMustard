package rustcore

import (
	"bytes"
	"encoding/json"

	"xmustard/api-go/internal/budget"
)

// passCountersKey opens the object the core appends as the last field of a JSON object
// result (rust-core `passcount::attach`).
var passCountersKey = []byte(`"pass_counters":`)

// notePasses adds one core call's pass counters to the health block's core_passes.
// The core appends them last, so only the result's tail is read: a nested field of the
// same name is never taken for them, and a result without them counts the call alone.
func notePasses(out []byte) {
	var p budget.CorePasses
	if body, ok := passCountersTail(out); !ok || json.Unmarshal(body, &p) != nil {
		p = budget.CorePasses{}
	} else {
		p.Counted = 1
	}
	budget.NoteCorePasses(p)
}

// passCountersTail is the object after the last `"pass_counters":` in an object result,
// when it is the result's last field.
func passCountersTail(out []byte) ([]byte, bool) {
	doc := bytes.TrimRight(out, " \t\r\n")
	i := bytes.LastIndex(doc, passCountersKey)
	if i < 0 || len(doc) == 0 || doc[0] != '{' || doc[len(doc)-1] != '}' {
		return nil, false
	}
	// the counters are one flat object closing just before the result does
	body := doc[i+len(passCountersKey) : len(doc)-1]
	flat := len(body) > 1 && body[0] == '{' && body[len(body)-1] == '}' &&
		bytes.Count(body, []byte("{")) == 1 && bytes.Count(body, []byte("}")) == 1
	return body, flat
}
