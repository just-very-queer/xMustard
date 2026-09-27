package workspaceops

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// A paging cursor (search, WS-18; recall, WS-20) is base64url of
// "<kind>.<n>...<mac>": the numbers the next page continues from, and mac, the first
// 16 bytes of an HMAC-SHA256 under a key held by this process only, over the kind, the
// numbers and the ranking the cursor pages. A client can neither edit a number nor
// reuse a cursor for another ranking or another kind; a cursor stops being valid when
// the API restarts.
type cursorCodec struct {
	kind   string
	fields int // how many numbers the cursor carries
	maxLen int // the longest cursor decoded
}

var (
	searchCursors = cursorCodec{kind: "s2", fields: 1, maxLen: maxSearchCursorLen}
	// recall cursors carry the offset and the stale-penalty block size.
	recallCursors = cursorCodec{kind: "r2", fields: 2, maxLen: 128}
)

// cursorKey is the process's cursor-signing key.
var cursorKey = sync.OnceValue(func() []byte {
	k := make([]byte, 32)
	rand.Read(k) // crypto/rand never fails on supported platforms
	return k
})

func (c cursorCodec) mac(ranking []byte, nums []int) string {
	m := hmac.New(sha256.New, cursorKey())
	m.Write([]byte(c.kind + "|"))
	for _, n := range nums {
		fmt.Fprintf(m, "%d|", n)
	}
	m.Write(ranking)
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func (c cursorCodec) encode(ranking []byte, nums ...int) string {
	parts := []string{c.kind}
	for _, n := range nums {
		parts = append(parts, strconv.Itoa(n))
	}
	parts = append(parts, c.mac(ranking, nums))
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, ".")))
}

// decode returns the numbers a cursor carries, or false for a cursor that is
// over-long, malformed, of another kind, edited or signed for another ranking. Range
// checks on the numbers are the caller's.
func (c cursorCodec) decode(cursor string, ranking []byte) ([]int, bool) {
	if len(cursor) > c.maxLen {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(raw), ".")
	if err != nil || len(parts) != c.fields+2 || parts[0] != c.kind {
		return nil, false
	}
	nums := make([]int, c.fields)
	for i := range nums {
		if nums[i], err = strconv.Atoi(parts[i+1]); err != nil {
			return nil, false
		}
	}
	return nums, hmac.Equal([]byte(parts[len(parts)-1]), []byte(c.mac(ranking, nums)))
}
