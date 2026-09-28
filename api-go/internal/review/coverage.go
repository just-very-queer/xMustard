package review

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Coverage is a reviewer's report on one changed file (WS-66): reviewed, or
// not_reviewed with the reason the file was left out.
type Coverage struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// coverageKeys are the member names of a coverage item, closed and exact.
var coverageKeys = []string{"path", "status", "reason"}

// DecodeCoverage reads a coverage report, a JSON array of {path, status, reason}, under
// the rules findings are read by: at most MaxFindingsBytes, member names exact and
// closed, and repository-relative paths. Whether a report fits the change (its paths,
// statuses and reasons) is for the store to judge against every changed file.
func DecodeCoverage(data []byte) ([]Coverage, error) {
	if len(data) > MaxFindingsBytes {
		return nil, fmt.Errorf("%w: coverage over %d bytes", ErrInvalid, MaxFindingsBytes)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &items); err != nil {
		return nil, fmt.Errorf("%w: coverage must be a JSON array: %v", ErrInvalid, err)
	}
	out := make([]Coverage, 0, len(items))
	for i, raw := range items {
		if err := exactKeys(raw, coverageKeys, true); err != nil {
			return nil, fmt.Errorf("%w: coverage %d: %v", ErrInvalid, i, err)
		}
		var c Coverage
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("%w: coverage %d: %v", ErrInvalid, i, err)
		}
		p, err := cleanPath(c.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: coverage %d: %v", ErrInvalid, i, err)
		}
		c.Path = p
		out = append(out, c)
	}
	return out, nil
}
