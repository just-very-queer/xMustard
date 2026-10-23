package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/rustcore"
	"xmustard/api-go/internal/workspaceops"
)

func main() {
	// `xmustard-api mint-token <id> [role]` mints a bearer token (local file access,
	// no server needed) — the bootstrap path for the first admin token.
	if len(os.Args) > 1 && os.Args[1] == "mint-token" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: xmustard-api mint-token <id> [role[+role...]]")
			fmt.Fprintln(os.Stderr, "roles: admin human-approver indexer verifier proposer reader; legacy agent (proposer+verifier) and readonly (reader)")
			os.Exit(2)
		}
		role := "agent"
		if len(os.Args) > 3 {
			role = os.Args[3]
		}
		raw, err := workspaceops.MintToken(envDefault("XMUSTARD_DATA_DIR", "../backend/data"), os.Args[2], role)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(raw)
		return
	}

	if err := prepareDaemon(); err != nil { // log file, env hygiene (daemon_lifecycle.go)
		log.Fatal(err)
	}
	cfg := loadServerConfig(dataDir())
	activated, err := adoptActivatedSocket(&cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := validateStartup(cfg); err != nil {
		log.Fatal(err)
	}
	ln, err := listen(cfg, activated)
	if errors.Is(err, errAlreadyRunning) {
		log.Printf("already running at %s; exiting", cfg.addr())
		os.Exit(0)
	}
	if err != nil {
		log.Fatal(err)
	}
	applyRuntimeHygiene()
	// Every request context derives from baseCtx, which shutdown cancels after the
	// bounded drain so in-flight helper children end with the API.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	// the watcher's changed paths are looked up in memory's anchor index (WS-28)
	workspaceops.ObserveIndexedPaths(workspaceops.InvalidateWatchedRoot(baseCtx, dataDir()))
	srv := &http.Server{
		Addr:        cfg.addr(),
		Handler:     buildHandler(cfg, newAPIHandlerFor(cfg.posture)),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
		// Bound slow/oversized clients so a few connections can't pin goroutines/FDs
		// (XM-NEW-019). Read/Write are generous because agent runs can be long, but
		// header + idle timeouts defeat slowloris and leaked keep-alives.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	// Graceful shutdown on SIGINT/SIGTERM. main owns the join: ListenAndServe returns
	// as soon as Shutdown closes the listener, so main must wait for the drain below
	// (interrupted-run persistence, worker/terminal teardown, service close) to finish
	// before the process exits.
	shutdownDone := make(chan struct{})
	// Registered before serving, not in the goroutine: a service manager may stop the
	// daemon as soon as it answers, and a SIGTERM that beat the goroutine to Notify
	// would kill it without the drain.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(shutdownDone)
		<-sigCh
		log.Printf("shutdown: signal received; draining")
		ctx, cancel := context.WithTimeout(context.Background(), shutdownDrain())
		defer cancel()
		// 1. stop admissions + drain in-flight HTTP (bounded), then cancel every
		//    request context and kill owned helper children still running: they are
		//    in their own process groups and would otherwise outlive the API.
		_ = srv.Shutdown(ctx)
		cancelBase()
		if n := rustcore.KillActiveChildren(); n > 0 {
			log.Printf("shutdown: killed %d in-flight helper children", n)
		}
		// 2. workload-aware drain: persist+reap runs, close terminals, flush the PG
		//    mirror — bounded so a stuck flush can't wedge exit.
		done := make(chan struct{})
		go func() {
			workspaceops.ShutdownInFlight(dataDir())
			close(done)
		}()
		select {
		case <-done:
			log.Printf("shutdown: in-flight work drained")
		case <-time.After(10 * time.Second):
			log.Printf("shutdown: in-flight drain exceeded budget; closing anyway")
		}
		// 3. close services.
		workspaceops.ClosePgPool()
		log.Printf("shutdown: services closed")
	}()
	go serveHookSocket(srv.Handler) // the static hook client's Unix socket (WS-23)
	log.Printf("xmustard api-go listening on %s (tls=%v)", cfg.addr(), cfg.hasTLS())
	go checkStoreAtStart(baseCtx)
	var serveErr error
	if cfg.hasTLS() {
		serveErr = srv.ServeTLS(ln, cfg.tlsCert, cfg.tlsKey)
	} else {
		serveErr = srv.Serve(ln)
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatal(serveErr)
	}
	<-shutdownDone
	// per-process high-water of waited-for children (diagnostic, not a tree peak)
	log.Printf("shutdown: %s", childHighWater())
	log.Printf("shutdown: complete")
}

// shutdownDrain bounds how long shutdown waits for in-flight requests before
// cancelling them (XMUSTARD_SHUTDOWN_DRAIN_SECONDS, default 15).
func shutdownDrain() time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("XMUSTARD_SHUTDOWN_DRAIN_SECONDS"))); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return 15 * time.Second
}

// serverConfig is the startup configuration read from the environment.
type serverConfig struct {
	host              string
	port              string
	authMode          string // auto | required | off
	authConfigured    bool   // at least one bearer credential exists
	tlsCert, tlsKey   string
	allowInsecureBind bool
	posture           exposurePosture // profile, read-only, tool/workspace/host/origin allowlists
	postureErr        error
	dataDir           string
}

func loadServerConfig(dataDir string) serverConfig {
	posture, postureErr := loadExposurePosture()
	c := serverConfig{
		// Bind to loopback by default. The API makes server-side requests (providers),
		// so it should not be exposed on all interfaces unless the operator opts in.
		host: envDefault("XMUSTARD_API_HOST", "127.0.0.1"),
		// Default to 8042 to match the MCP bridge's API target (XM-NEW-001).
		port:              envDefault("XMUSTARD_API_PORT", "8042"),
		authMode:          strings.ToLower(strings.TrimSpace(envDefault("XMUSTARD_AUTH", "auto"))),
		authConfigured:    workspaceops.HasAuthConfigured(dataDir),
		tlsCert:           os.Getenv("XMUSTARD_API_TLS_CERT"),
		tlsKey:            os.Getenv("XMUSTARD_API_TLS_KEY"),
		allowInsecureBind: os.Getenv("XMUSTARD_ALLOW_INSECURE_BIND") == "1",
		posture:           posture,
		postureErr:        postureErr,
		dataDir:           dataDir,
	}
	c.posture.Loopback = c.loopback()
	return c
}

func (c serverConfig) addr() string   { return net.JoinHostPort(c.host, c.port) }
func (c serverConfig) hasTLS() bool   { return c.tlsCert != "" && c.tlsKey != "" }
func (c serverConfig) loopback() bool { return isLoopbackHost(c.host) }

// isLoopbackHost reports whether host names only the loopback interface. Anything
// else — including the unspecified 0.0.0.0/:: and unresolvable names — is treated as
// an exposed bind.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// validateStartup is the fail-closed startup interlock:
//   - XMUSTARD_AUTH must be auto, required or off;
//   - required without configured credentials is refused on every bind (it would
//     reject every caller, or tempt an operator to weaken it);
//   - a non-loopback bind requires XMUSTARD_AUTH=required AND configured credentials
//     (off is always fatal there, whatever tokens or overrides exist) AND transport
//     security (TLS, or the explicit insecure-bind override for a TLS-terminating proxy).
func validateStartup(c serverConfig) error {
	if c.postureErr != nil {
		return c.postureErr
	}
	switch c.authMode {
	case "auto", "required", "off":
	default:
		return fmt.Errorf("invalid XMUSTARD_AUTH=%q; use auto, required or off", c.authMode)
	}
	if c.authMode == "required" && !c.authConfigured {
		return fmt.Errorf("XMUSTARD_AUTH=required but no credentials are configured; run `xmustard-api mint-token <id> admin` first")
	}
	if c.loopback() {
		return nil
	}
	if c.authMode != "required" {
		return fmt.Errorf("refusing non-loopback bind %s with XMUSTARD_AUTH=%s; set XMUSTARD_AUTH=required and mint credentials", c.host, c.authMode)
	}
	if !c.hasTLS() && !c.allowInsecureBind {
		return fmt.Errorf("refusing non-loopback bind without TLS; set XMUSTARD_API_TLS_CERT/KEY, or XMUSTARD_ALLOW_INSECURE_BIND=1 if TLS is terminated by a front proxy")
	}
	return nil
}

// buildHandler wraps the route table in the configured middleware stack:
// exposure (host/origin/query credentials) → body limit → auth → route gates
// (newAPIHandlerFor) → evidence delivery → handlers.
func buildHandler(c serverConfig, api http.Handler) http.Handler {
	handler := api
	if c.posture.platform() {
		log.Printf("surface: profile=platform — core and platform routes served (the UI needs this profile)")
	} else {
		log.Printf("surface: profile=core — nine tools, memory, evidence and auth only; platform routes 404 (XMUSTARD_PROFILE=platform serves them)")
	}
	if c.posture.ReadOnly {
		log.Printf("surface: READ-ONLY — mutating routes refused, write tools hidden")
	}
	logRegisterRoots(c.posture)
	if c.authMode != "off" {
		handler = authMiddleware(c.dataDir, c.authMode, handler)
		if c.authMode == "required" || c.authConfigured {
			log.Printf("auth: ENFORCED (mode=%s, bearer token required)", c.authMode)
		} else {
			log.Printf("auth: open — no tokens configured (mode=%s); unauthenticated callers collapse to one identity. Mint a token to enforce.", c.authMode)
		}
	} else {
		log.Printf("auth: DISABLED (XMUSTARD_AUTH=off; loopback only)")
	}
	if c.authMode != "required" && !c.authConfigured {
		log.Printf("memory: open mode: every caller is the single identity %q, so no peer can verify a memory; `remember` promotes it at once, labelled verification_mode=%s. Mint one agent token per agent (xmustard-api mint-token <id> agent) for peer-verified memory.",
			workspaceops.OpenModeIdentity, workspaceops.VerificationSelfAssertedOpen)
	}
	// Cap every request body so a hostile/buggy client can't drive unbounded memory
	// by POSTing a huge payload — the HTTP analogue of the MCP stdio framing cap
	// (readBoundedLine). It applies before any handler reads the body.
	handler = bodyLimitMiddleware(handler)
	// Outermost: refuse rebinding Hosts, cross-origin browsers and query-string keys
	// before any other work.
	return withLoopback(exposureMiddleware(c.posture, c.dataDir, handler))
}

// --- auth middleware + principal helpers ---

type ctxKey string

const principalCtxKey ctxKey = "principal"

func principalFromContext(ctx context.Context) *workspaceops.Principal {
	p, _ := ctx.Value(principalCtxKey).(*workspaceops.Principal)
	return p
}

// requireRole enforces a role inside a handler (admin holds every role; the legacy
// "agent" gate means proposer). In open mode (no auth configured) it allows the
// operation locally; once auth is configured the middleware has already rejected
// unauthenticated requests. A denial names the missing role and is audited.
func requireRole(w http.ResponseWriter, r *http.Request, role string) bool {
	p := principalFromContext(r.Context())
	if p == nil {
		if !workspaceops.HasAuthConfigured(dataDir()) {
			return true
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
		return false
	}
	if need := workspaceops.GateRole(role); !p.Has(need) {
		denyMissingRole(w, r, p, need)
		return false
	}
	return true
}

// memoryCaller is who is acting on governed memory: an authenticated principal, or
// (open mode) the single identity every unauthenticated caller collapses to.
type memoryCaller struct {
	principal *workspaceops.Principal // nil in open mode
	openMode  bool
	// session and callID are the transport's provenance of the write (the MCP bridge
	// sends its session and the JSON-RPC id of the tool call).
	session, callID string
	// approval labels a human approver's write (human_presence.go); "" for anyone else.
	approval string
}

// id is the identity recorded as author/verifier. It is never caller-asserted.
func (c memoryCaller) id() string {
	if c.principal != nil {
		return c.principal.ID
	}
	return workspaceops.OpenModeIdentity
}

// actor binds a memory write to the caller and marks whether it happened in open mode.
// For content edits an admin edits any entry, anyone else only entries they authored;
// the open-mode caller already passes every requireRole gate (it is the local
// operator), so it edits as admin.
func (c memoryCaller) actor() workspaceops.ContextActor {
	a := workspaceops.ContextActor{
		ID: c.id(), OpenMode: c.openMode,
		Admin:     c.openMode || c.principal.Has(workspaceops.RoleAdmin),
		Approver:  c.openMode || c.principal.Has(workspaceops.RoleHumanApprover),
		Verifier:  c.openMode || c.principal.Has(workspaceops.RoleVerifier),
		SessionID: c.session, CallID: c.callID, Approval: c.approval,
	}
	if c.principal != nil {
		a.Owner, a.Kind = c.principal.Owner, c.principal.Kind
	}
	return a
}

// requireMemoryCaller gates a governed-memory write on role (proposer to propose or
// edit, verifier to verify) and resolves the caller. requireRole admits an
// unauthenticated request only when no credentials are configured, so a nil principal
// here means open mode. A principal holding the reserved open-mode identity (an env
// token, or a token minted before the id was reserved) is refused, since it would pass
// as the author of every open-mode memory.
func requireMemoryCaller(w http.ResponseWriter, r *http.Request, role string) (memoryCaller, bool) {
	if !requireRole(w, r, role) {
		return memoryCaller{}, false
	}
	p := principalFromContext(r.Context())
	if p != nil && workspaceops.IsOpenModeIdentity(p.ID) {
		dd := envDefault("XMUSTARD_DATA_DIR", "../backend/data")
		workspaceops.RecordAuthAudit(dd, workspaceops.AuthAuditEvent{
			Action: "denied", Actor: p.ID, Detail: "principal id is reserved for open mode",
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": fmt.Sprintf("principal id %q is reserved for open mode; mint a token under another id", p.ID)})
		return memoryCaller{}, false
	}
	session, err := workspaceops.ProvenanceLabel("X-Xmustard-Session-Id", r.Header.Get("X-Xmustard-Session-Id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return memoryCaller{}, false
	}
	callID, err := workspaceops.ProvenanceLabel("X-Xmustard-Call-Id", r.Header.Get("X-Xmustard-Call-Id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return memoryCaller{}, false
	}
	return memoryCaller{principal: p, openMode: p == nil, session: session, callID: callID, approval: approvalLabel(r, p)}, true
}

// requireWellFormedJSON decodes a body that may be absent or blank (the handler then
// falls back to query params). A malformed body is a 400, never silently empty, and a
// body cut off at the size cap is a 413. Returns false once it has written the error.
func requireWellFormedJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return writeJSONBodyError(w, decodeOptionalJSONBody(r, dst))
}

// requireJSONBody is requireWellFormedJSON for a handler with no query fallback: an
// absent or blank body is a 400 too.
func requireJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	var raw json.RawMessage // left empty only by an absent or blank body
	err := decodeOptionalJSONBody(r, &raw)
	if err == nil && len(raw) == 0 {
		err = errors.New("request body is required")
	}
	if err == nil {
		err = json.Unmarshal(raw, dst)
	}
	return writeJSONBodyError(w, err)
}

// writeJSONBodyError maps a body read/decode error to 413 (cut off at the size cap) or
// 400, and reports whether the body was fine.
func writeJSONBodyError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "request body too large"})
		return false
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
	return false
}

// maxRequestBodyBytes bounds any single HTTP request body. ReadTimeout +
// MaxHeaderBytes bound time and headers only; without this a single multi-GB POST
// (e.g. to /context or /settings) is decoded into memory unbounded — the same OOM
// class the MCP framing path explicitly caps. Generous enough for real diagnostics/
// eval payloads; a body past the cap is cut off and the handler's decode surfaces a
// clean error (the malformed-body -> 400 path) instead of the server allocating.
const maxRequestBodyBytes = 32 << 20 // 32 MiB

// maxRequestBodyBytesConfigured returns the per-request body cap, operator-tunable
// via XMUSTARD_MAX_BODY_BYTES (so a tight-RSS deployment can lower it without a
// rebuild). Defaults to maxRequestBodyBytes.
func maxRequestBodyBytesConfigured() int64 {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_MAX_BODY_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return maxRequestBodyBytes
}

// ordinaryBodyCap is the cap for an ordinary request body, the one a body held in the
// transient pool is admitted against: budget.CapToPool of the configured cap, because a
// reserved body can never exceed the pool. /api/health reports it as
// request_body_cap_bytes.
//
// newAPIHandlerFor sets it beside bodyLimitMiddleware's own streamLimit, for the same
// reason as captureBodyCap: the middleware fixes its limit when the handler is built.
var ordinaryBodyCap = budget.CapToPool(maxRequestBodyBytesConfigured())

// captureBodyCap is the lower of the two byte limits a capture request meets:
// bodyLimitMiddleware's streaming admission cap (the configured
// XMUSTARD_MAX_BODY_BYTES), which counts bytes received, and the evidence store's
// per-original limit (XMUSTARD_EVIDENCE_MAX_ORIGINAL_BYTES), which counts bytes written
// to the spool. /api/health reports it as capture_body_cap_bytes, so the advertised
// number is a limit the server actually enforces on capture rather than one it never
// reaches. It is not an exact maximum accepted body: the store counts what redaction
// writes, so a body at or under this value can still spool past the store's limit when a
// secret expands to a longer marker.
//
// newAPIHandlerFor sets it, beside the store it describes, because the middleware and
// the store each fix their own limits when the handler is built and health must report
// those values rather than re-reading the environment on every request.
var captureBodyCap = min(maxRequestBodyBytesConfigured(), evidence.DefaultMaxOriginal)

// bodyInFlight bounds the number of body-bearing requests decoded concurrently, so
// the aggregate in-flight body memory is at most cap × maxInFlight rather than
// cap × (unbounded concurrent agents) — a single 32 MiB cap alone cannot hold the
// 50–100 MB RSS target under fan-in (XM-PRO-008). Tunable via XMUSTARD_MAX_INFLIGHT_BODIES.
// (Per-endpoint limits + decoded-object amplification remain follow-on work.)
var bodyInFlight = make(chan struct{}, inFlightBodyLimit())

func inFlightBodyLimit() int {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_MAX_INFLIGHT_BODIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 12
}

// bodyBudgetThreshold: only requests declaring (or hiding, via chunked encoding) a
// body larger than this acquire an in-flight slot. Normal small requests — tool
// calls, memory proposals, run starts — are never throttled, and a long-running
// request with a tiny body never holds a slot. This targets the actual memory risk
// (large concurrent uploads) without affecting throughput.
const bodyBudgetThreshold = 1 << 20 // 1 MiB

func bodyLimitMiddleware(next http.Handler) http.Handler {
	// A body reserved in the transient pool can never exceed the pool, so its cap is
	// the pool size (a permanent 413, not a retried 503). A body its handler streams to
	// disk (streamsRequestBody) is never reserved, so it keeps the configured cap.
	streamLimit := maxRequestBodyBytesConfigured()
	reservedLimit := budget.CapToPool(streamLimit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every request gets a transient-byte ledger held until the handler returns
		// (deferred, so success, error, cancellation and panic all release it). Rust
		// captures and other streamed reads made under r.Context() reserve into it.
		scope := budget.NewScope(nil)
		defer scope.Close()
		r = r.WithContext(budget.WithScope(r.Context(), scope))
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		streams := streamsRequestBody(r)
		limit := reservedLimit
		if streams {
			limit = streamLimit
		}
		if r.ContentLength > limit {
			// permanently too large: 413, not a retryable overload
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "request body too large"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if streams { // capped, but spooled to disk in O(window) by the handler
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > bodyBudgetThreshold || r.ContentLength < 0 {
			select {
			case bodyInFlight <- struct{}{}:
				defer func() { <-bodyInFlight }()
			case <-r.Context().Done():
				http.Error(w, "request cancelled while waiting for in-flight body budget", 499)
				return
			}
		}
		// Reserve the body before any handler reads it. A declared length is reserved
		// up front; an unknown (chunked) length is read here chunk by chunk under
		// admission, so a refusal is still a protocol-correct 503 (or 413 past the cap)
		// rather than a handler decode error.
		if r.ContentLength > 0 {
			if err := scope.Acquire(r.ContentLength); err != nil {
				writeOverloaded(w)
				return
			}
		} else if r.ContentLength < 0 {
			raw, err := budget.ReadAllAdmitted(scope, r.Body, int(limit))
			switch {
			case errors.Is(err, budget.ErrOverloaded):
				writeOverloaded(w)
				return
			case err != nil:
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "request body too large"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
		}
		next.ServeHTTP(w, r)
	})
}

// writeOverloaded is the protocol-correct admission refusal: 503 + Retry-After.
func writeOverloaded(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": budget.ErrOverloaded.Error(), "overloaded": true})
}

func authMiddleware(dataDir, mode string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" { // health stays public for liveness probes
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
		// single read of the token store yields both the principal and whether auth
		// is configured (was two reads per request).
		principal, configured := workspaceops.ResolveAuth(dataDir, token)
		enforce := mode == "required" || configured
		if enforce && principal == nil {
			workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
				Action:     "denied",
				Actor:      "anonymous",
				Detail:     "missing/invalid/expired bearer token",
				Method:     r.Method,
				Path:       r.URL.Path,
				RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required: provide Authorization: Bearer <token>"})
			return
		}
		// A presence-only token is typed at the xmustard-ops prompt, never sent as a
		// bearer token (WS-57): one that arrives here was read from somewhere else.
		if principal != nil && principal.PresenceOnly {
			workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
				Action: "denied", Actor: principal.ID, Detail: "presence-only token presented as a bearer token",
				Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
			})
			writeJSON(w, http.StatusUnauthorized, map[string]any{"reason": "presence_only_token",
				"error": "this token is presence-only: it is accepted only when typed at the xmustard-ops prompt, never as a bearer token"})
			return
		}
		// A reader-only principal's writes are refused by the route gate, which
		// names the missing role; every non-GET route needs more than reader
		// (TestNoWriteRouteGrantsReader).
		// workspace scope: a scoped (per-worker) token may only touch its workspaces.
		// Unscoped tokens (the default) are unrestricted, so this is backward-compatible.
		if principal != nil {
			if wsID := workspaceIDFromPath(r.URL.Path); wsID != "" && !principal.AllowsWorkspace(wsID) {
				workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
					Action: "denied", Actor: principal.ID,
					Detail: "workspace " + wsID + " not in token scope",
					Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
				})
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "token not scoped to workspace " + wsID})
				return
			}
		}
		if principal == nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalCtxKey, principal)))
	})
}

// requireTerminalScope enforces that the authenticated principal may touch
// workspaceID. Terminal routes live under /api/terminal (outside /api/workspaces/),
// so the path-based middleware scope check (workspaceIDFromPath) never fires for
// them and a workspace-scoped token could otherwise open/read/write another
// workspace's shell by passing its workspace_id in the body/query (XM-PRO-001).
// Writes 400 on an empty id, 403 on an out-of-scope token; returns false if handled.
func requireTerminalScope(w http.ResponseWriter, r *http.Request, dataDir, workspaceID string) bool {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "workspace_id is required"})
		return false
	}
	if !postureFrom(r).allowsWorkspace(workspaceID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "workspace " + workspaceID + " is not served by this deployment", "reason": "workspace_not_allowed"})
		return false
	}
	if p := principalFromContext(r.Context()); p != nil && !p.AllowsWorkspace(workspaceID) {
		workspaceops.RecordAuthAudit(dataDir, workspaceops.AuthAuditEvent{
			Action: "denied", Actor: p.ID,
			Detail: "workspace " + workspaceID + " not in token scope (terminal)",
			Method: r.Method, Path: r.URL.Path, RemoteAddr: r.RemoteAddr,
		})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "token not scoped to workspace " + workspaceID})
		return false
	}
	return true
}

// httpStatusForClass maps a typed domain-error class to an HTTP status. Status now
// follows the error TYPE, not its English wording (XM-PRO-013).
func httpStatusForClass(c workspaceops.ErrorClass) int {
	switch c {
	case workspaceops.ClassInvalidInput:
		return http.StatusBadRequest
	case workspaceops.ClassNotFound:
		return http.StatusNotFound
	case workspaceops.ClassConflict:
		return http.StatusConflict
	case workspaceops.ClassUnavailable:
		return http.StatusServiceUnavailable
	case workspaceops.ClassForbidden:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// respondError is the single error->HTTP mapper the per-handler substring classifiers
// converge on. A typed DomainError uses its class (and for the internal class the
// wrapped detail stays server-side); os.ErrNotExist -> 404; IsInvalidInput -> 400;
// anything else -> 500 with the raw message (the legacy fallback still being migrated).
func respondError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, budget.ErrOverloaded) {
		writeOverloaded(w)
		return
	}
	if de, ok := workspaceops.AsDomainError(err); ok {
		if de.Class == workspaceops.ClassInternal {
			log.Printf("internal error: %v", err) // detail logged, not disclosed
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			return
		}
		writeJSON(w, httpStatusForClass(de.Class), map[string]any{"error": de.Public})
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Missing resource"})
		return
	}
	if workspaceops.IsInvalidInput(err) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

// workspaceIDFromPath extracts {id} from /api/workspaces/{id}/... ("" if not such a path).
// /api/workspaces/load is the registration route, not a workspace named "load"; its
// handler applies the token's workspace scope to the root being registered.
func workspaceIDFromPath(p string) string {
	const prefix = "/api/workspaces/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	if rest == "load" {
		return ""
	}
	return rest
}

func envDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	// Rust results arrive already encoded: write them as-is instead of re-encoding
	// (the encoder compacts into a second growing buffer — a full extra copy of
	// multi-MB results, measured as the API's retained-heap peak on the index step).
	if raw, ok := payload.(json.RawMessage); ok && json.Valid(raw) {
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		_, _ = w.Write([]byte("\n"))
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// maxRecallLimit caps a caller-supplied recall limit so the bounded path stays bounded.
const maxRecallLimit = 50

// dataDir resolves the runtime data root (XMUSTARD_DATA_DIR, default ../backend/data).
func dataDir() string { return envDefault("XMUSTARD_DATA_DIR", "../backend/data") }
