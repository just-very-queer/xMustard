package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Universal observation capture (PAR-CTX-01): the output of ANY tool — a client's
// native Bash/Read/Grep, another MCP server's tool — is streamed into the spool,
// described by capture metadata, reduced by its tool family's reducer and shaped for
// the client. Retention, quota, expiry and principal binding are the store's
// (Store.Capture), reached through the registry hook in Reduce. Bytes delivered by a
// hook were produced at a repository state xMustard did not observe, so they are
// always captured_identity=unknown. Capture never uses the heavy slot: the decode
// and both reducer passes stream in O(window) memory.

// FormatRaw is a body that is the tool output itself.
const FormatRaw HookFormat = "raw"

// ObservationInput is one capture request.
type ObservationInput struct {
	WorkspaceID  string
	RepoScope    string
	Actor        string // stable principal ID when authenticated
	AuthEnforced bool
	Format       HookFormat
	Body         io.Reader
	// Meta supplies what the body does not carry (client, tool version) and, for raw
	// bodies, everything (tool, call id, exit code, content type).
	Meta CaptureMeta
	// Sel overrides selection inputs (command, path, line range, family).
	Sel Selector
	// Redact wraps the spool writer (the WS-05 streaming redactor).
	Redact func(io.Writer) StreamRedactor
}

// ObservationResult is the capture envelope: the store's delivery plus capture
// metadata, extracted facts, the per-kind projection and the client-shaped payload.
type ObservationResult struct {
	*Delivery
	Capture            CaptureMeta  `json:"capture"`
	Family             Family       `json:"family"`
	Facts              Facts        `json:"facts"`
	Structured         any          `json:"projection_struct,omitempty"`
	Footer             string       `json:"footer,omitempty"`
	DeliveredTokensEst int          `json:"delivered_tokens_est"`
	TokenEstimator     string       `json:"token_estimator"`
	Shape              *ShapeResult `json:"shape,omitempty"`
	Policy             ClientPolicy `json:"policy"`
}

// Observe captures one tool output.
func (s *Store) Observe(ctx context.Context, reg *Registry, in ObservationInput) (*ObservationResult, error) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	sp, err := s.NewSpool(in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer sp.Discard()
	meta := in.Meta
	meta.Client = strings.ToLower(strings.TrimSpace(meta.Client))
	if meta.Client == "" {
		meta.Client = string(in.Format)
	}
	if meta.Client == string(FormatRaw) || meta.Client == "" {
		meta.Client = "http"
	}
	meta.Format = string(in.Format)
	var body *HookBody
	if in.Format == FormatRaw || in.Format == "" {
		meta.Format = string(FormatRaw)
		body, err = spoolRaw(in.Body, sp, in.Redact)
	} else {
		body, err = DecodeHookBody(in.Format, in.Body, sp, in.Redact)
	}
	if err != nil {
		return nil, err
	}
	// what the body says wins over what the caller claimed about it
	pick := func(fromBody, given string) string {
		if fromBody != "" {
			return fromBody
		}
		return given
	}
	meta.Tool = pick(body.ToolName, meta.Tool)
	if meta.Tool == "" {
		return nil, fmt.Errorf("%w: the capture names no tool", ErrBadBody)
	}
	meta.CallID = pick(body.CallID, meta.CallID)
	meta.SessionID = pick(body.SessionID, meta.SessionID)
	meta.AgentID = pick(body.AgentID, meta.AgentID)
	meta.ArgsDigest = pick(body.ArgsDigest, meta.ArgsDigest)
	meta.HookEvent = body.Event
	meta.IsError = meta.IsError || body.IsError
	if body.ExitCode != nil {
		meta.ExitCode = body.ExitCode
	}
	if meta.ExitCode != nil && *meta.ExitCode != 0 {
		meta.IsError = true
	}
	if in.Format != FormatRaw && in.Format != "" {
		meta.BodySHA256, meta.BodyBytes = body.BodySHA256, body.BodyBytes
		meta.ContentType = "text/plain; charset=utf-8" // decoded output strings
	}
	if meta.ContentType == "" {
		meta.ContentType = "text/plain; charset=utf-8"
	}
	meta.Principal = in.Actor
	meta.CapturedIdentity = "unknown"
	meta.Sections = body.Sections
	if in.Format == FormatRaw || in.Format == "" {
		meta.BodySHA256, meta.BodyBytes = body.BodySHA256, body.BodyBytes
		meta.Sections = nil
	}
	sel := selectorFor(meta, body, in.Sel)
	red, argv0 := reg.Select(sel)
	pol := PolicyFor(meta.Client)
	meta.OutputShape = shapeName(pol.Client, meta.Tool)
	if in.Format == FormatRaw || in.Format == "" {
		meta.OutputShape = "raw"
	}
	hook := &reduceHook{reducer: red, argv0: argv0, shape: meta.OutputShape, meta: &meta,
		in: Input{Sel: sel, Sections: body.Sections, Target: pol.Target}}
	d, err := s.Capture(withReduceHook(ctx, hook), sp, CaptureRequest{
		WorkspaceID: in.WorkspaceID, RepoScope: in.RepoScope, Actor: in.Actor, AuthEnforced: in.AuthEnforced,
		Issuer: "capture:" + meta.Client, SessionID: meta.SessionID, CallID: meta.CallID, Tool: meta.Tool,
		ToolVersion: meta.ToolVersion, ArgsDigest: meta.ArgsDigest, IsError: meta.IsError, ContentType: meta.ContentType,
		// no RepoKey: the producing repository state was not observed
	})
	if err != nil {
		return nil, err
	}
	res := &ObservationResult{Delivery: d, Capture: meta, Family: red.Family(), Policy: pol, TokenEstimator: TokenEstimator}
	proj := hook.out
	if proj == nil {
		proj = &Projection{Text: d.Projection, Parts: map[string]string{}}
	}
	d.Reducer = proj.Record.Reducer
	if d.Reducer == "" {
		d.Reducer = reducerName(red)
	}
	res.Facts, res.Structured = proj.Facts, proj.Structured
	if d.Handle != "" {
		res.Footer = evidenceFooter(d, in.WorkspaceID)
	}
	res.Shape = ShapeOutput(ShapeInput{Client: meta.Client, Tool: meta.Tool, Body: bodyForShape(in.Format, body),
		Proj: proj, Reduced: d.Reduced, Footer: res.Footer, RawBytes: d.RawBytes})
	delivered := d.Projection
	switch {
	case res.Shape.Mode == ShapeReplace:
		delivered = string(res.Shape.Payload)
	case res.Footer != "":
		delivered += "\n" + res.Footer
	}
	res.DeliveredTokensEst = EstimateTokens(delivered)
	return res, nil
}

func bodyForShape(f HookFormat, b *HookBody) *HookBody {
	if f == FormatRaw || f == "" {
		return nil
	}
	return b
}

// spoolRaw streams a raw body into the spool as one "output" section.
func spoolRaw(r io.Reader, dst io.Writer, redact func(io.Writer) StreamRedactor) (*HookBody, error) {
	sink := newSectionSink(dst, redact)
	if _, err := sink.begin("output"); err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(sink, h), r)
	if err != nil {
		return nil, err
	}
	if err := sink.end(); err != nil {
		return nil, err
	}
	return &HookBody{Format: FormatRaw, Sections: sink.secs, Input: map[string]string{},
		BodySHA256: hex.EncodeToString(h.Sum(nil)), BodyBytes: n}, nil
}

// selectorFor derives the reducer selector from the capture metadata, the decoded
// tool input and the caller's overrides.
func selectorFor(meta CaptureMeta, body *HookBody, over Selector) Selector {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := body.Input[k]; v != "" {
				return v
			}
		}
		return ""
	}
	sel := Selector{Client: meta.Client, Tool: meta.Tool, ExitCode: meta.ExitCode, Family: over.Family,
		Command: first("command", "cmd"), Path: first("file_path", "filePath", "path", "target_file", "notebook_path", "directory", "dir_path")}
	if over.Command != "" {
		sel.Command = over.Command
	}
	if over.Path != "" {
		sel.Path = over.Path
	}
	// the first captured line: Claude Read reports it; Pi/Claude read offsets are
	// 1-based line numbers
	if v, err := strconv.Atoi(body.Scalar("file.startLine")); err == nil && v > 0 {
		sel.StartLine = v
	} else if v, err := strconv.Atoi(first("offset", "start_line")); err == nil && v > 0 && sel.Command == "" {
		sel.StartLine = v
	}
	if over.StartLine > 0 {
		sel.StartLine = over.StartLine
	}
	sel.FromLine, sel.ToLine = over.FromLine, over.ToLine
	return sel
}

// evidenceFooter is the one-line recovery notice appended to shaped output.
func evidenceFooter(d *Delivery, ws string) string {
	f := map[string]any{
		"handle": d.Handle, "workspace_id": ws, "resource_uri": d.ResourceURI, "raw_bytes": d.RawBytes,
		"projected_bytes": d.ProjectedBytes, "omissions": len(d.Omissions), "reducer": d.Reducer,
		"expires_at": d.ExpiresAt, "captured_identity": d.CapturedIdentity,
		"search": "GET /api/workspaces/" + url.PathEscape(ws) + "/evidence/search?handle=" + d.Handle + "&pattern=RE2|&lines=A-B",
	}
	raw, _ := json.Marshal(f)
	return "[xmustard evidence] " + string(raw)
}
