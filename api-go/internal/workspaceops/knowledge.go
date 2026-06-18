package workspaceops

import (
	"encoding/json"
	"strconv"

	"xmustard/api-go/internal/rustcore"
)

// Knowledge layer delivery: hybrid search + wiki generation over the repo.

func WorkspaceSearch(dataDir, workspaceID, query string, limit int) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 25
	}
	out, err := rustcore.RunSearch(root, workspaceID, query, strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func WorkspaceWiki(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunWiki(root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
