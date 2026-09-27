package evidence

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Tail is the last bytes of one retained original, read in a single bounded window
// (why_failed reads the end of a captured failing output, where test runners and
// compilers print their summary). Data starts at Offset in the original.
type Tail struct {
	Handle      string `json:"handle"`
	Tool        string `json:"tool"`
	CallID      string `json:"call_id,omitempty"`
	Status      int    `json:"status"`
	IsError     bool   `json:"is_error"`
	ContentType string `json:"content_type"`
	Offset      int64  `json:"offset"`
	TotalBytes  int64  `json:"total_bytes"`
	RawSHA256   string `json:"raw_sha256"`
	// Family, ExitCode and FailingTests come from the family reducer, when one ran.
	Family       Family   `json:"family,omitempty"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	FailingTests []string `json:"failing_tests,omitempty"`
	Data         []byte   `json:"-"`
}

// Tail reads at most maxBytes from the end of a retained original, under the same
// workspace, principal, expiry and revocation checks as Read. It never reads more than
// maxBytes of the original into memory, whatever the original's size.
func (s *Store) Tail(ctx context.Context, req ReadRequest, maxBytes int64) (*Tail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, obs, err := s.openAuthorized(req)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n := min(max(maxBytes, 0), obs.RawBytes)
	t := &Tail{Handle: req.Handle, Tool: obs.Tool, CallID: obs.CallID, Status: obs.Status, IsError: obs.IsError,
		ContentType: obs.ContentType, Offset: obs.RawBytes - n, TotalBytes: obs.RawBytes, RawSHA256: obs.RawSHA256,
		Data: make([]byte, n)}
	if fam := obs.Projection.Family; fam != nil {
		t.Family, t.ExitCode, t.FailingTests = fam.Family, fam.Facts.ExitCode, fam.Facts.FailingTests
	}
	if m, err := f.ReadAt(t.Data, t.Offset); int64(m) != n || (err != nil && !errors.Is(err, io.EOF)) {
		return nil, ErrCorrupt
	}
	return t, nil
}

// openAuthorized resolves a handle and opens its original under the workspace lock,
// so a concurrent revoke or expiry is observed as such, never as a torn read. The
// opened file stays readable even if it is unlinked afterwards.
func (s *Store) openAuthorized(req ReadRequest) (*os.File, Observation, error) {
	key, err := handleKey(req.Handle)
	if err != nil {
		return nil, Observation{}, err
	}
	wsd, err := s.wsDir(req.WorkspaceID)
	if err != nil {
		return nil, Observation{}, err
	}
	st := s.state(req.WorkspaceID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return s.openOriginalLocked(req, filepath.Join(wsd, key), st)
}
