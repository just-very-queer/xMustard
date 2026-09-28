package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Standing memory (WS-31, PAR-HAR-03). The API compiles, per workspace, a projection of
// promoted memory: the core tier that passes the injection policy's core surface (a
// human approver approved the served revision), each entry framed as data and pinned
// to that revision, and the deferred index of titles. It is stamped with the memory
// revision and cached by it, so every session of a workspace at one revision reads the
// same text. initialize appends it after the static Instructions when the session's
// workspace resolves without a tool call (the connection's binding,
// XMUSTARD_WORKSPACE_ID, or a registered workspace holding the working directory;
// initialize never registers). A client that drops the instructions reads the same
// text as the resource xmustard://memory/core or through the orient prompt
// (the initial_instructions fallback).

// MemoryCoreURI is the standing-memory projection of the session's workspace.
const MemoryCoreURI = "xmustard://memory/core"

const (
	// instructionsMemoryChars budgets the projection initialize appends: about 1k
	// tokens, read once per session.
	instructionsMemoryChars = 4000
	// standingMemoryTimeout bounds the projection lookup so a slow API cannot stall
	// session start; on any failure the static instructions are served.
	standingMemoryTimeout = 1500 * time.Millisecond
)

// projection is the part of the API's projection a session renders.
type projection struct {
	Revision int64  `json:"revision"`
	Text     string `json:"text"`
}

// initWorkspace resolves the session's workspace from the signals that need no tool
// call and never register: the binding, XMUSTARD_WORKSPACE_ID, then a registered
// workspace that holds the working directory. Roots cannot be asked before the client
// finished initializing.
func (s *Session) initWorkspace(ctx context.Context) string {
	for _, id := range []string{s.srv.opts.WorkspaceID, s.srv.opts.Getenv("XMUSTARD_WORKSPACE_ID")} {
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
	}
	if cwd := s.srv.opts.Cwd; cwd != "" {
		if ws, err := s.byPathSignal(ctx, cwd, SourceCWD, "initialize never registers"); err == nil {
			return ws.ID
		}
	}
	return ""
}

// readProjection asks the API for workspaceID's projection within maxChars.
func (s *Session) readProjection(ctx context.Context, workspaceID string, maxChars int) (*projection, *APIResponse, error) {
	path := "/api/workspaces/" + url.PathEscape(workspaceID) + "/memory/core?max_chars=" + strconv.Itoa(maxChars)
	resp, err := s.srv.opts.Backend.Do(ctx, Request{Method: http.MethodGet, Path: path})
	if err != nil || resp.Status != http.StatusOK {
		return nil, resp, err
	}
	var p projection
	if err := json.Unmarshal([]byte(resp.Body), &p); err != nil {
		return nil, resp, err
	}
	return &p, resp, nil
}

// standingMemory is the projection text initialize appends, or "" when the session
// has none: the option is off, no workspace resolves, or the API cannot answer in time.
func (s *Session) standingMemory(ctx context.Context) string {
	if !s.srv.opts.StandingMemory {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, standingMemoryTimeout)
	defer cancel()
	id := s.initWorkspace(ctx)
	if id == "" {
		return ""
	}
	p, _, err := s.readProjection(ctx, id, instructionsMemoryChars)
	if err != nil || p == nil {
		return ""
	}
	return strings.TrimSpace(p.Text)
}

// instructionsWith is the initialize instructions: the static workflow, then the
// standing memory when there is any.
func instructionsWith(memory string) string {
	if memory == "" {
		return Instructions
	}
	return Instructions + "\n\n" + memory
}

// memoryCore reads the projection resource for the workspace a tool call naming none
// would use (roots and registration included, as for a tool call).
func (s *Session) memoryCore(ctx context.Context) (any, *RPCError) {
	ws, err := s.resolveWorkspace(ctx, &Tool{Name: "memory_core"}, map[string]string{})
	if err != nil {
		return nil, &RPCError{Code: CodeInternal, Message: err.Error(), Data: map[string]any{"uri": MemoryCoreURI}}
	}
	_, resp, err := s.readProjection(ctx, ws.ID, instructionsMemoryChars)
	switch {
	case err != nil:
		return nil, &RPCError{Code: CodeInternal, Message: err.Error(), Data: map[string]any{"uri": MemoryCoreURI}}
	case resp.Status != http.StatusOK:
		return nil, &RPCError{Code: CodeInternal, Message: "memory projection: " + statusText(resp.Status) + ": " + clip(apiErrorText(resp.Body), 200),
			Data: map[string]any{"uri": MemoryCoreURI}}
	}
	return map[string]any{"contents": []map[string]any{{"uri": MemoryCoreURI, "mimeType": "application/json", "text": resp.Body}}}, nil
}
