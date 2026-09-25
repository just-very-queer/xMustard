package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Feedback signal kinds, as in the legacy agent_feedback.json store.
const (
	FeedbackRetrieval  = "retrieval"
	FeedbackVerify     = "verify"
	FeedbackRunSuccess = "run_success"
	FeedbackRunFail    = "run_fail"
	// feedbackCap bounds each counter, like the legacy store.
	feedbackCap = 10_000
)

var feedbackColumns = map[string]string{
	FeedbackRetrieval:  "retrieval_count",
	FeedbackVerify:     "verify_count",
	FeedbackRunSuccess: "run_success",
	FeedbackRunFail:    "run_fail",
}

// PathFeedback is the agent-feedback signal for one path.
type PathFeedback struct {
	Path           string `json:"path"`
	RetrievalCount int    `json:"retrieval_count"`
	VerifyCount    int    `json:"verify_count"`
	RunSuccess     int    `json:"run_success"`
	RunFail        int    `json:"run_fail"`
	LastUsed       string `json:"last_used"`
}

// FeedbackReader reads path feedback.
type FeedbackReader interface {
	GetFeedback(ctx context.Context, workspaceID string, paths []string) (map[string]PathFeedback, error)
	ListFeedback(ctx context.Context, workspaceID string, afterPath string, limit int) ([]PathFeedback, error)
}

// FeedbackWriter bumps feedback counters with one row upsert per path.
type FeedbackWriter interface {
	BumpFeedback(ctx context.Context, workspaceID, kind string, paths []string) (int, error)
}

// GetFeedback returns the signal for the given paths (absent paths are omitted).
func (r *reader) GetFeedback(ctx context.Context, workspaceID string, paths []string) (map[string]PathFeedback, error) {
	out := make(map[string]PathFeedback, len(paths))
	for start := 0; start < len(paths); start += maxInArgs {
		chunk := paths[start:min(start+maxInArgs, len(paths))]
		args := []any{workspaceID}
		for _, p := range chunk {
			args = append(args, p)
		}
		rows, err := r.query(ctx, `SELECT path, retrieval_count, verify_count, run_success, run_fail, last_used
			FROM path_feedback WHERE workspace_id = ? AND path IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f PathFeedback
			if err := rows.Scan(&f.Path, &f.RetrievalCount, &f.VerifyCount, &f.RunSuccess, &f.RunFail, &f.LastUsed); err != nil {
				rows.Close()
				return nil, err
			}
			out[f.Path] = f
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListFeedback pages a workspace's feedback in path order.
func (r *reader) ListFeedback(ctx context.Context, workspaceID string, afterPath string, limit int) ([]PathFeedback, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx, `SELECT path, retrieval_count, verify_count, run_success, run_fail, last_used
		FROM path_feedback WHERE workspace_id = ? AND path > ? ORDER BY path LIMIT ?`, workspaceID, afterPath, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathFeedback
	for rows.Next() {
		var f PathFeedback
		if err := rows.Scan(&f.Path, &f.RetrievalCount, &f.VerifyCount, &f.RunSuccess, &f.RunFail, &f.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// BumpFeedback increments one counter for each path, capped. It returns the number of
// distinct paths bumped.
func (t *txn) BumpFeedback(ctx context.Context, workspaceID, kind string, paths []string) (int, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return 0, err
	}
	col, ok := feedbackColumns[kind]
	if !ok {
		return 0, fmt.Errorf("%w: feedback kind %q", ErrInvalid, kind)
	}
	clean := CleanPaths(paths)
	now := t.nowText()
	for _, p := range clean {
		if len(p) > maxPathLen || strings.ContainsRune(p, 0) {
			return 0, fmt.Errorf("%w: path %q", ErrInvalid, p)
		}
		// col comes from the fixed feedbackColumns table, never from input.
		if _, err := t.exec(ctx, `INSERT INTO path_feedback (workspace_id, path, `+col+`, last_used) VALUES (?, ?, 1, ?)
			ON CONFLICT (workspace_id, path) DO UPDATE SET `+col+` = min(`+col+` + 1, ?), last_used = excluded.last_used`,
			workspaceID, p, now, feedbackCap); err != nil {
			return 0, err
		}
	}
	return len(clean), nil
}
