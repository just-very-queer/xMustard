package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The RawMessage fast path in writeJSON (written as-is instead of re-encoded, to
// avoid an extra full copy of multi-MB Rust results) must be semantically identical
// to the encoder path: same status, content type, decoded value and trailing newline;
// invalid raw input keeps the old encoder behavior.
func TestWriteJSONRawMessageMatchesEncoder(t *testing.T) {
	cases := []json.RawMessage{
		json.RawMessage(`{"hits":[{"path":"a.go","line":3}],"coverage":{"complete":true}}`),
		json.RawMessage(`{"s":"<tag> & \"quote\"  ","n":[1,2.5,-3e2,null,true]}`),
		json.RawMessage("  {\n \"spaced\" : [ 1 , 2 ]\n}  "),
	}
	for _, raw := range cases {
		rec := httptest.NewRecorder()
		writeJSON(rec, 207, raw)
		var enc bytes.Buffer
		if err := json.NewEncoder(&enc).Encode(raw); err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("fast path wrote invalid JSON: %v", err)
		}
		_ = json.Unmarshal(enc.Bytes(), &want)
		if !reflect.DeepEqual(got, want) || rec.Code != 207 || rec.Header().Get("Content-Type") != "application/json" ||
			!bytes.HasSuffix(rec.Body.Bytes(), []byte("\n")) {
			t.Fatalf("fast path differs from encoder for %s: %q vs %q", raw, rec.Body.String(), enc.String())
		}
	}
	bad := json.RawMessage(`{"unterminated":`)
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, bad)
	var enc bytes.Buffer
	_ = json.NewEncoder(&enc).Encode(bad)
	if rec.Body.String() != enc.String() {
		t.Fatalf("invalid raw input must keep the encoder behavior: %q vs %q", rec.Body.String(), enc.String())
	}
}
