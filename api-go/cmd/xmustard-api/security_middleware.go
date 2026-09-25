package main

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// Security kernel (PAR-SEC-02, PAR-SEC-03, PAR-RT-07): the deployment's exposure
// posture and the two middlewares that enforce it.
//
//   exposureMiddleware (outermost): rejects credentials in the query string, a Host
//     header outside the allowlist (DNS rebinding on a loopback bind), and a
//     cross-origin browser request.
//   routeGateMiddleware (after auth, before evidence delivery and the handlers):
//     applies the route's gate from routeGateTable — profile, disabled tools,
//     workspace allowlist, read-only mode and the caller's role.

const (
	profileCore     = "core"     // default: the nine tools, memory, evidence, auth
	profilePlatform = "platform" // adds the platform routes the UI uses
)

// mcpToolOrder is the order tools are listed in (the MCP tools/list order).
var mcpToolOrder = []string{"ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"}

// exposurePosture is what a deployment exposes. Zero value: core profile, writes
// allowed, every tool and workspace enabled, no extra hosts or origins.
type exposurePosture struct {
	Profile        string
	ReadOnly       bool
	DisabledTools  map[string]bool
	Workspaces     map[string]bool // allowlist of workspace ids; empty = all
	AllowedHosts   map[string]bool // extra Host names (lowercase, no port)
	AllowedOrigins map[string]bool // extra origins, "scheme://host[:port]"
	Loopback       bool            // the API binds loopback only
	// RegisterRoots are the directories under which a non-admin principal may
	// register a git work tree (POST /api/workspaces/load); empty = admins only.
	RegisterRoots []string
}

func (p *exposurePosture) platform() bool { return p != nil && p.Profile == profilePlatform }

func (p *exposurePosture) profile() string {
	if p.platform() {
		return profilePlatform
	}
	return profileCore
}

func (p *exposurePosture) readOnly() bool { return p != nil && p.ReadOnly }

func (p *exposurePosture) toolDisabled(tool string) bool {
	return p != nil && tool != "" && p.DisabledTools[tool]
}

func (p *exposurePosture) allowsWorkspace(id string) bool {
	return p == nil || len(p.Workspaces) == 0 || p.Workspaces[id]
}

func (p *exposurePosture) registerRoots() []string {
	if p == nil {
		return nil
	}
	return p.RegisterRoots
}

func (p *exposurePosture) disabledTools() []string {
	out := []string{}
	for _, t := range mcpToolOrder {
		if p.toolDisabled(t) {
			out = append(out, t)
		}
	}
	return out
}

// loadExposurePosture reads the posture from the environment:
//
//	XMUSTARD_PROFILE=core|platform   (default core)
//	XMUSTARD_PLATFORM=1              same as XMUSTARD_PROFILE=platform
//	XMUSTARD_CORE_ONLY=0|1           legacy: 0 = platform, 1 = core
//	XMUSTARD_READ_ONLY=1             refuse mutating routes; hide write tools
//	XMUSTARD_DISABLED_TOOLS=a,b      MCP tools this deployment does not serve
//	XMUSTARD_WORKSPACE_ALLOWLIST=a,b workspace ids this deployment serves
//	XMUSTARD_ALLOWED_HOSTS=a,b       extra Host names (a proxy's public name)
//	XMUSTARD_ALLOWED_ORIGINS=a,b     extra browser origins, scheme://host[:port]
//	XMUSTARD_REGISTER_ROOTS=/a:/b    where non-admin tokens may register git work
//	                                 trees (OS path list; default: admins only)
//
// Conflicting profile settings and malformed values are startup errors.
func loadExposurePosture() (exposurePosture, error) {
	p := exposurePosture{Profile: profileCore}
	type vote struct{ name, profile string }
	var votes []vote
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("XMUSTARD_PROFILE"))); v {
	case "":
	case profileCore, profilePlatform:
		votes = append(votes, vote{"XMUSTARD_PROFILE=" + v, v})
	default:
		return p, fmt.Errorf("invalid XMUSTARD_PROFILE=%q; use core or platform", v)
	}
	flags := []struct{ name, on, off string }{
		{"XMUSTARD_PLATFORM", profilePlatform, profileCore},
		{"XMUSTARD_CORE_ONLY", profileCore, profilePlatform},
	}
	for _, f := range flags {
		switch v := strings.TrimSpace(os.Getenv(f.name)); v {
		case "":
		case "1":
			votes = append(votes, vote{f.name + "=1", f.on})
		case "0":
			votes = append(votes, vote{f.name + "=0", f.off})
		default:
			return p, fmt.Errorf("invalid %s=%q; use 0 or 1", f.name, v)
		}
	}
	for _, v := range votes {
		if v.profile != votes[0].profile {
			return p, fmt.Errorf("conflicting profile settings: %s selects %s but %s selects %s", votes[0].name, votes[0].profile, v.name, v.profile)
		}
	}
	if len(votes) > 0 {
		p.Profile = votes[0].profile
	}
	switch v := strings.TrimSpace(os.Getenv("XMUSTARD_READ_ONLY")); v {
	case "", "0":
	case "1":
		p.ReadOnly = true
	default:
		return p, fmt.Errorf("invalid XMUSTARD_READ_ONLY=%q; use 0 or 1", v)
	}
	tools := toolGates()
	for _, t := range splitCSV(os.Getenv("XMUSTARD_DISABLED_TOOLS")) {
		if _, ok := tools[t]; !ok {
			return p, fmt.Errorf("XMUSTARD_DISABLED_TOOLS names unknown tool %q; tools are %s", t, strings.Join(mcpToolOrder, ", "))
		}
		if p.DisabledTools == nil {
			p.DisabledTools = map[string]bool{}
		}
		p.DisabledTools[t] = true
	}
	for _, id := range splitCSV(os.Getenv("XMUSTARD_WORKSPACE_ALLOWLIST")) {
		if !workspaceops.IsSafeID(id) {
			return p, fmt.Errorf("XMUSTARD_WORKSPACE_ALLOWLIST: invalid workspace id %q", id)
		}
		if p.Workspaces == nil {
			p.Workspaces = map[string]bool{}
		}
		p.Workspaces[id] = true
	}
	for _, h := range splitCSV(os.Getenv("XMUSTARD_ALLOWED_HOSTS")) {
		h = strings.ToLower(h)
		if strings.ContainsAny(h, "/:@ *") {
			return p, fmt.Errorf("XMUSTARD_ALLOWED_HOSTS: %q must be a bare host name", h)
		}
		if p.AllowedHosts == nil {
			p.AllowedHosts = map[string]bool{}
		}
		p.AllowedHosts[h] = true
	}
	for _, o := range splitCSV(os.Getenv("XMUSTARD_ALLOWED_ORIGINS")) {
		norm, ok := normalizeOrigin(o)
		if !ok {
			return p, fmt.Errorf("XMUSTARD_ALLOWED_ORIGINS: %q must be scheme://host[:port]", o)
		}
		if p.AllowedOrigins == nil {
			p.AllowedOrigins = map[string]bool{}
		}
		p.AllowedOrigins[norm] = true
	}
	roots, err := workspaceops.ParseRegisterRoots(os.Getenv("XMUSTARD_REGISTER_ROOTS"))
	if err != nil {
		return p, err
	}
	p.RegisterRoots = roots
	return p, nil
}

// postureFromEnv is loadExposurePosture for callers that validated the environment
// at startup (and tests); a bad environment panics here.
func postureFromEnv() exposurePosture {
	p, err := loadExposurePosture()
	if err != nil {
		panic(err)
	}
	return p
}

// normalizeOrigin returns "scheme://host[:port]" (lowercase) for an http(s) origin.
func normalizeOrigin(origin string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

// hostName strips the port and IPv6 brackets from a Host header value.
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

func isLoopbackName(name string) bool {
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// hostAllowed guards against DNS rebinding: on a loopback bind a browser tricked
// into resolving an attacker's name to 127.0.0.1 still sends that name as Host.
func (p *exposurePosture) hostAllowed(host string) bool {
	if host == "" {
		return true // HTTP/1.0 without Host: not a browser, so not a rebinding vector
	}
	name := hostName(host)
	if p.AllowedHosts[name] {
		return true
	}
	if p.Loopback {
		return isLoopbackName(name)
	}
	return len(p.AllowedHosts) == 0
}

// originAllowed rejects cross-origin browser requests: a page on another site must
// not drive the API (open mode has no credential to stop it). Loopback origins are
// allowed on a loopback bind (the dev UI proxies from localhost:5173).
func (p *exposurePosture) originAllowed(origin, host string) bool {
	norm, ok := normalizeOrigin(origin)
	if !ok {
		return false // includes "null" (sandboxed frames, file://)
	}
	if p.AllowedOrigins[norm] {
		return true
	}
	u, _ := url.Parse(norm)
	if p.Loopback {
		return isLoopbackName(hostName(u.Host))
	}
	return strings.EqualFold(u.Host, host) // same origin
}

// queryCredentialParams are query parameter names that carry credentials. Keys in
// URLs end up in logs, browser history and Referer headers, so they are refused
// rather than ignored (cursor-bridge's ?api_key= browser auth is the counter-example).
var queryCredentialParams = map[string]bool{
	"token": true, "access_token": true, "auth_token": true, "id_token": true,
	"api_key": true, "apikey": true, "api-key": true, "x-api-key": true, "key": true,
	"bearer": true, "authorization": true, "auth": true, "password": true, "secret": true,
}

func queryCredential(q url.Values) string {
	names := make([]string, 0, len(q))
	for k := range q {
		if queryCredentialParams[strings.ToLower(k)] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func exposureMiddleware(p exposurePosture, dataDir string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deny := func(status int, reason, msg string) {
			workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
				Action: "denied", Actor: "anonymous", Detail: reason,
				Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, status, map[string]any{"error": msg, "reason": reason})
		}
		if name := queryCredential(r.URL.Query()); name != "" {
			deny(http.StatusBadRequest, "query_credentials", "credentials are not accepted in the query string (parameter "+name+"); send Authorization: Bearer <token>")
			return
		}
		if !p.hostAllowed(r.Host) {
			deny(http.StatusForbidden, "host_not_allowed", "Host "+hostName(r.Host)+" is not allowed; add it to XMUSTARD_ALLOWED_HOSTS if a proxy serves the API under that name")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !p.originAllowed(origin, r.Host) {
			deny(http.StatusForbidden, "origin_not_allowed", "cross-origin request refused; add the origin to XMUSTARD_ALLOWED_ORIGINS to allow it")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- route gates ---

const postureCtxKey ctxKey = "posture"

func postureFrom(r *http.Request) *exposurePosture {
	p, _ := r.Context().Value(postureCtxKey).(*exposurePosture)
	return p
}

// patternValues maps a pattern's {wildcards} to the request's path segments, read
// from the escaped path and unescaped per segment exactly as ServeMux does, so an
// encoded "/" can't hide a second segment from the check.
// ok is false when the path does not line up with the pattern (callers fail closed).
func patternValues(pattern, escapedPath string) (map[string]string, bool) {
	pat := strings.Split(strings.Trim(patternPath(pattern), "/"), "/")
	got := strings.Split(strings.Trim(escapedPath, "/"), "/")
	out := map[string]string{}
	if len(pat) != len(got) {
		return out, false
	}
	for i, seg := range pat {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			v, err := url.PathUnescape(got[i])
			if err != nil {
				return out, false
			}
			out[strings.Trim(seg, "{}")] = v
		}
	}
	return out, true
}

// strictIDWildcards are path wildcards whose values the server generates or
// validates as plain identifiers (workspaceops.IsSafeID). Every other wildcard must
// still be one safe path segment (safeSegment).
var strictIDWildcards = map[string]bool{
	"workspace_id": true, "entry_id": true, "run_id": true, "terminal_id": true, "id": true,
}

// safeSegment reports whether a decoded wildcard value is one path segment that
// cannot climb out of a directory: not empty, no "/", "\\" or control character,
// and no "..". Free-form platform ids (issue, view and provider names) keep their
// other characters.
func safeSegment(v string) bool {
	if v == "" || v == "." || strings.Contains(v, "..") || strings.ContainsAny(v, "/\\") {
		return false
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// wildcardValid applies the rule for a wildcard's name to its decoded value.
func wildcardValid(name, v string) bool {
	if strictIDWildcards[name] {
		return workspaceops.IsSafeID(v)
	}
	return safeSegment(v)
}

func routeGateMiddleware(p exposurePosture, mux *gatedMux, next http.Handler) http.Handler {
	posture := &p
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		g, ok := mux.gates[pattern]
		if !ok {
			if pattern != "" {
				// a route reached the mux without a gate row: refuse rather than
				// serve it unclassified (gatedMux registration normally prevents this)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "route " + pattern + " has no gate", "reason": "unclassified_route"})
				return
			}
			next.ServeHTTP(w, r) // unmatched: the mux answers 404 or 405
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), postureCtxKey, posture))
		if !g.Core && !posture.platform() {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error":   "platform route not served in the core profile; start the API with XMUSTARD_PROFILE=platform",
				"reason":  "platform_route",
				"profile": profileCore,
			})
			return
		}
		if posture.toolDisabled(g.Tool) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "tool " + g.Tool + " is disabled on this deployment", "reason": "tool_disabled"})
			return
		}
		// Every path wildcard is checked, not only the workspace id: a handler joins
		// run, issue and other ids into file paths. A path that does not line up
		// with a pattern that has wildcards fails closed.
		values, aligned := patternValues(pattern, r.URL.EscapedPath())
		if !aligned && strings.Contains(pattern, "{") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path for " + pattern, "reason": "invalid_id"})
			return
		}
		for _, name := range slices.Sorted(maps.Keys(values)) {
			if !wildcardValid(name, values[name]) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid " + name, "reason": "invalid_id"})
				return
			}
		}
		if ws, ok := values["workspace_id"]; ok && !posture.allowsWorkspace(ws) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "workspace " + ws + " is not served by this deployment", "reason": "workspace_not_allowed"})
			return
		}
		if posture.readOnly() && g.mutating(pattern) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "the API is in read-only mode (XMUSTARD_READ_ONLY=1)", "reason": "read_only"})
			return
		}
		// A nil principal got past auth: open mode, or auth switched off. Handlers
		// that need more (requireRole) keep their own check for that case.
		if pr := principalFromContext(r.Context()); pr != nil && !pr.Has(g.Role) {
			denyMissingRole(w, r, pr, g.Role)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// denyMissingRole audits and answers a 403 that names the missing role.
func denyMissingRole(w http.ResponseWriter, r *http.Request, p *workspaceops.Principal, role string) {
	held := strings.Join(p.RoleSet(), ", ")
	workspaceops.RecordAuthAudit(dataDir(), workspaceops.AuthAuditEvent{
		Action: "denied", Actor: p.ID, Role: p.Role,
		Detail: role + " role required (has " + held + ")",
		Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
	})
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":        fmt.Sprintf("%s role required; principal %q holds: %s", role, p.ID, held),
		"reason":       "missing_role",
		"missing_role": role,
	})
}

// --- the caller's view of itself (whoami, ground) ---

// callerView is who the caller is and what it may do.
type callerView struct {
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Roles      []string `json:"roles"`
	Workspaces []string `json:"workspaces,omitempty"`
	// OpenMode: no credentials exist, every caller is the one local identity and
	// passes every role gate; memory it writes is self-asserted, never peer-verified.
	OpenMode bool `json:"open_mode"`
}

func callerOf(r *http.Request) callerView {
	if p := principalFromContext(r.Context()); p != nil {
		return callerView{ID: p.ID, Role: p.Role, Roles: p.RoleSet(), Workspaces: p.Workspaces}
	}
	if !workspaceops.HasAuthConfigured(dataDir()) {
		return callerView{ID: workspaceops.OpenModeIdentity, Role: "open-mode", Roles: workspaceops.Roles(), OpenMode: true}
	}
	// XMUSTARD_AUTH=off while tokens exist: route gates pass, but handlers that check
	// a role themselves (memory writes, admin routes) answer 401, so only reads work.
	return callerView{Role: "anonymous", Roles: []string{workspaceops.RoleReader}}
}

func (c callerView) has(role string) bool {
	for _, r := range c.Roles {
		if r == role || r == workspaceops.RoleAdmin {
			return true
		}
	}
	return false
}

// usableTools lists the MCP tools this caller can call on this deployment.
func usableTools(p *exposurePosture, c callerView) []string {
	gates := toolGates()
	out := []string{}
	for _, tool := range mcpToolOrder {
		pattern, ok := gates[tool]
		if !ok {
			continue
		}
		g := routeGateTable[pattern]
		if (!g.Core && !p.platform()) || p.toolDisabled(tool) || (p.readOnly() && g.mutating(pattern)) || !c.has(g.Role) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// whoamiResponse is GET /api/auth/whoami: the caller, the deployment posture and the
// tools the caller can use (the MCP shim filters tools/list by it).
func whoamiResponse(r *http.Request) map[string]any {
	c := callerOf(r)
	p := postureFrom(r)
	return map[string]any{
		"id": c.ID, "role": c.Role, "roles": c.Roles, "workspaces": c.Workspaces, "open_mode": c.OpenMode,
		"profile": p.profile(), "read_only": p.readOnly(), "disabled_tools": p.disabledTools(),
		"tools": usableTools(p, c),
	}
}

// groundResponse adds the caller's principal and roles to a ground result.
func groundResponse(r *http.Request, g *workspaceops.SessionGrounding) any {
	if g == nil {
		return nil
	}
	return struct {
		*workspaceops.SessionGrounding
		Principal callerView `json:"principal"`
	}{g, callerOf(r)}
}
