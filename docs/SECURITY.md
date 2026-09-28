# Security kernel

This page describes who may call the xMustard HTTP API, which routes a deployment
serves, and how the API limits its exposure. The code is authoritative:
`api-go/cmd/xmustard-api/route_gates.go` (route table),
`api-go/cmd/xmustard-api/security_middleware.go` (posture and middlewares) and
`api-go/internal/workspaceops/auth.go`, `auth_roles.go` (tokens and roles). The
route table at the end of this page is generated from the code by a test.

This page describes `v0.1.1`. What shipped and its known limits are in the
[release notes](releases/v0.1.1.md); the [v0.1.0 notes](releases/v0.1.0.md) cover the
first release.

## Posture at a glance

| What you get | Why it holds |
|---|---|
| Nothing listens beyond your machine by default | The API binds `127.0.0.1:8042`. A non-loopback bind stops startup unless `XMUSTARD_AUTH=required`, credentials are minted, and TLS is set (or `XMUSTARD_ALLOW_INSECURE_BIND=1` behind a TLS proxy). See [Exposure posture](#exposure-posture). |
| Every route has a role | `gatedMux` refuses a route with no gate row, and a test keeps the [route gate table](#route-gate-table) generated from the code. |
| Agents cannot vouch for their own memory | With tokens minted, promotion needs approvals from distinct principals other than the author, unless the operator turns on single-agent mode (`require_multi_agent_verification: false` in settings). The default threshold is 2. Open-mode writes are labelled `self_asserted_open_mode`, never peer-verified. |
| Known secret shapes stay out of memory | Memory text is redacted before it is stored, and `why_failed` output before it is analyzed. See [Secret redaction and secret paths](#secret-redaction-and-secret-paths). |
| Credential files stay out of search results | Search refuses secret paths such as `.env`, SSH keys and `.netrc` before reading their text, and masks credential-shaped words in snippets. |
| Recalled text is labelled as data | Recall and every evidence projection carry `injection_flags`. A flagged result says it is data, not instructions. See [Injection safety](#injection-safety-ws-56). |
| Nothing runs a command unless you opt in | `why_failed` command mode is off on every bind. It needs `XMUSTARD_WHY_FAILED_COMMANDS=1` at startup and an authenticated admin. See [Commands why_failed runs](#commands-why_failed-runs-ws-21). The platform profile, also opt-in, adds terminal and run routes that execute commands. |
| People keep the merge | Human approvals are recorded, not enforced by xMustard. A merge attestation never merges, changes branch protection or posts to a pull request. See [Human approvals](#human-approvals-ws-57). |
| Native tool output is never retained unredacted | Capture retains each original and makes it searchable, so every capture, from the capture route or a client hook, streams through the secret redactor before a byte is stored. The output of a secret path such as `.env` is refused (`422 secret_path`), and capture fails closed without a working redactor (`503 redaction_unavailable` or `redaction_failed`). In v0.1.0 no redactor was wired, so every capture was refused. See [Evidence capture redaction](#evidence-capture-redaction-ws-fix-03). |
| Hooks never decide for the agent | The Claude Code hook service adds context, replaces a native output with its reduction (PostToolUse only) or says nothing; it never allows, denies or rewrites a tool call, and a slow or failed answer leaves Claude Code's own behavior. Authentication still fails closed. See [Client hooks](#client-hooks-ws-23). |
| No credential in the service unit | `xmustard-ops setup` writes a loopback-only launchd agent or systemd unit that carries no token and refuses credential-named variables. See [Daemon service](#daemon-service-ws-58). |

## Authentication modes

`XMUSTARD_AUTH` picks the mode. It is read at startup, and anything else stops the API.

| `XMUSTARD_AUTH` | Behavior |
|---|---|
| `auto` (default) | Open mode while no credentials exist: every caller is the single identity `anonymous` and passes every role gate. Minting the first token (`xmustard-api mint-token <id> [role]`) enforces auth on the running API; the check runs on every request, so no restart is needed. |
| `required` | Every request needs a bearer token. Startup stops when no credentials are configured. A non-loopback bind needs this mode. |
| `off` | No authentication. Loopback binds only: a non-loopback bind stops startup. |

`/api/health` stays public in every mode ([Health endpoint](#health-endpoint)). Send
tokens only as `Authorization: Bearer <token>`; a credential in the query string is
refused.

## Profiles

The API serves one of two route sets.

| Profile | Selected by | Serves |
|---|---|---|
| `core` (default) | nothing, `XMUSTARD_PROFILE=core`, or the legacy `XMUSTARD_CORE_ONLY=1` | the nine MCP tools, governed memory (propose, verify, edit, full list), evidence capture (redacted) and recovery, the Claude Code hook routes, token administration, workspace list and registration, index rebaseline, health |
| `platform` | `XMUSTARD_PROFILE=platform`, `XMUSTARD_PLATFORM=1`, or the legacy `XMUSTARD_CORE_ONLY=0` | core plus the platform routes: issues, runs, terminals, providers, Postgres, integrations and the other routes the React UI calls |

In the core profile a platform route answers
`404 {"reason":"platform_route"}`. Conflicting settings (for example
`XMUSTARD_PROFILE=core` with `XMUSTARD_PLATFORM=1`) stop the API at startup.

**The React UI needs the platform profile.** `make dev` starts the API with
`XMUSTARD_PROFILE=platform` and the UI dev server together. `make backend-platform`
starts only the API in that profile. `make backend` starts the API in the default
core profile. The Pi end-to-end harness sets the platform profile because its
fixture setup uses platform routes.

Each route is classified when it is registered. `gatedMux` panics if a route has no
row in `routeGateTable`, and a test fails if a row names a route that no longer
exists. A new core route therefore cannot be dropped from the core profile by
accident, and a new platform route cannot be served without a role.

## Roles

A token carries a role spec: one role, or several joined with `+`.

| Role | Grants |
|---|---|
| `reader` | read routes and the read tools (`ground`, `recall`, `search`, `explain`, `impact`, `diagnostics`, and `why_failed` reading a run or outcome by id) |
| `proposer` | reader, plus `remember`, `why_failed` with a `log` or `evidence_handle` (records a run-independent outcome; never runs anything), editing memory it authored, platform writes, and registering a git work tree under `XMUSTARD_REGISTER_ROOTS` ([Workspace registration](#workspace-registration)) |
| `verifier` | reader, plus `verify` |
| `human-approver` | reader, plus policy changes and approvals (workspace policy, security acceptance criteria and dispositions, run-plan approve/reject, run accept); with a token of kind `human`, the `xmustard-ops` approval commands ([Human approvals](#human-approvals-ws-57)) |
| `indexer` | reader, plus `POST /api/workspaces/{id}/index` (rebaseline) |
| `admin` | every role, plus token administration, settings, providers, Postgres bootstrap, integration credentials, verification-profile definitions, terminals, registering any directory as a workspace and, only where the operator enabled command mode, `why_failed` with a `command` ([Commands why_failed runs](#commands-why_failed-runs-ws-21)) |

The legacy names stay valid: `agent` is `proposer+verifier`, and `readonly` is
`reader`. A blank role mints `agent`. An unknown role is refused at mint time, and a
stored or environment role that cannot be parsed resolves to `reader`. A principal
that holds only `reader` may use only GET routes, `/api/health` and the `/mcp`
endpoint (each tool call through it meets that tool's own route gate): every other
non-GET route needs a higher role, and a test enforces that. A reader's write is refused by the route gate, so
the answer names the missing role like any other refusal.

The `agent` role does not include `indexer`, so an agent token cannot reset the
index baseline. Give automation that rebaselines a dedicated `indexer` token. Keep
`admin` and `human-approver` tokens with people. An approval route never authorizes
a Git merge; merges stay a human action.

Mint with the CLI or the admin API:

```bash
xmustard-api mint-token alice agent              # proposer+verifier
xmustard-api mint-token ci-indexer indexer
xmustard-api mint-token reviewer verifier+human-approver
curl -H "Authorization: Bearer $ADMIN" -d '{"id":"bob","roles":["proposer"]}' \
  http://127.0.0.1:8042/api/auth/tokens
```

`XMUSTARD_AUTH_TOKENS="id:role:token,..."` accepts the same role specs.

A refused call answers `403` with the missing role named:

```json
{"error":"verifier role required; principal \"pat\" holds: proposer, reader",
 "reason":"missing_role","missing_role":"verifier"}
```

`GET /api/auth/whoami` returns the caller's id, role spec, expanded `roles`,
`open_mode`, the deployment `profile`, `read_only`, `disabled_tools`, and `tools`:
the MCP tools this caller can use here. The MCP server (`api-go/internal/mcpserver`)
filters `tools/list` by that list, on the `/mcp` endpoint and in the deprecated stdio
shim alike. If the API cannot answer within 2 seconds, it advertises all nine tools,
and the API still enforces every gate. `ground` adds a `principal` block with the
caller's id and roles.

In open mode no credentials exist. Every caller is the single identity
`anonymous`, passes every role gate, and memory it writes is labelled
`self_asserted_open_mode`, never peer-verified.

### Who counts as a peer verifier

Promotion needs approvals from distinct principals other than the author. Since the
govstore cutover (WS-12) the author of the entry's *served revision* is excluded
too, not only the original proposer: an admin who rewrites an entry cannot then
count toward that revision's quorum, so rewriting and approving one text needs a
further principal. This is a deliberate tightening; it matches the store's own
peer invariant, so the API and the store agree on who is a peer.

### Who may change a memory's lifecycle (WS-19A)

- A focused edit (`remember op=edit`) is a pending revision. It is accepted by the
  same rule that promotes entries, with the edit's author excluded like the entry's,
  so an author cannot accept their own edit in multi-agent mode.
- One edit is pending at a time. A new edit is refused (409) until the pending one is
  accepted or rejected.
- Retiring or retracting promoted memory takes as many distinct retract verdicts as
  the entry's gate, and each verdict needs the verifier role (`remember op=retire` on
  promoted memory is a retract verdict). An author may archive only their own
  unpromoted proposal at once.
- A replacement (`supersedes`) takes the strictest gate of the entries it replaces,
  so an entry that required peers is never superseded on one principal's word.
- Only an admin or a human approver retracts, restores a retired entry or purges.
  Retract and purge (`DELETE .../context/{id}`, `purge=true`) and restore
  (`POST .../context/{id}/restore`) are not MCP tools. Purge keeps a tombstone with
  every revision's digest.
- An author may set or clear the expiry of their own unpromoted entry, or of one
  that rests on a single assertion. On peer-verified memory the expiry is part of
  what peers approved, so only an admin or a human approver changes it.
- `recall(entry_id)` shows a reader verified content only. Unverified text (a pending
  or rejected entry, a pending edit with its diff and votes) and `history=true` need
  the verifier or human-approver role. History lists revisions without content and
  is capped at 48 KiB, dropping the oldest events and revisions first.

### Provenance, evidence-bound votes and ingest redaction (WS-19B)

- Tokens record an owner and a kind (`POST /api/auth/tokens` takes `owner` and
  `kind`, `agent` or `human`). A token minted without them is its own owner, of kind
  agent; rotation keeps them. Env tokens (`XMUSTARD_AUTH_TOKENS`) are always their own
  owner.
- `principal_distinctness` in settings is `token` (the default: every token is a
  distinct verifier) or `owner`. Under `owner`, the quorum counts owners: on a
  peer-gated entry a principal cannot approve or reject a revision when its owner is
  the owner of the entry's author or of the revision's author, or when another
  principal of the same owner already has an approve or reject on that revision
  (403, audited); a principal may still replace its own verdict. Owners are the ones
  recorded at write time (the entry's source owner, the owner the edit event
  carries), so revoking a token does not detach its writes from its owner. An unreadable settings file or an
  unknown value takes `owner`. The check runs when a vote is cast; votes cast before
  the policy was switched on keep counting.
- Every memory write records its principal, owner and kind, the session and tool-call
  ids the transport sent (`X-Xmustard-Session-Id`, `X-Xmustard-Call-Id`: printable
  text, else 400; an id over 128 bytes is recorded as its prefix plus a digest, not
  refused), and the run and evidence handles it cites
  (`remember evidence, run_id`; `verify evidence_handle`). Each event the write appends
  carries them under `provenance`. Entries keep HEAD and branch at propose and at
  promote; the dirty flag is not sampled (no git process is spawned per write).
- An evidence handle is checked when it is cited, under the evidence store's own
  rules: it must be a retained, unrevoked, unexpired original of the workspace that the
  caller itself may read. A run must exist in the workspace. Anything else is 400 and
  nothing is written.
- `verify` outcomes `duplicate_of` (with `target`) is a vote; `helpful`,
  `misleading` and `stale_harm` report feedback on the served revision and never change
  verification. `recall(entry_id)` returns `provenance`, `verification_basis` (each
  counting verdict with its owner, kind, evidence and target) and `feedback`.
- Secrets are redacted with the shared `redact` rules before anything is stored:
  remember's title, content, new_string, description and reason, the full content an
  edit produces (so a secret spliced from stored text and `new_string` is caught), the
  legacy PUT content, a vote's note and an approver's lifecycle reason. The write answers
  `redactions` (count per rule) and a warning naming the rules. `old_string` only
  locates stored text and is never stored.

### Injection safety (WS-56)

Everything xMustard puts into an agent's context is text that agents wrote or tools
produced, and it can carry instructions aimed at the agent reading it.
`api-go/internal/injection` holds the policy as data tables, and the checks that
refuse text are explicit and fail closed.

- **Scan.** A rule table flags the shapes of an injection, not imperative text as
  such (a memory is meant to say "run make check before committing"): overriding
  earlier instructions, a new task or role, forged authority, secrecy towards the
  user, prompt leaks, exfiltration of secrets, `curl | sh`, chat-template tokens,
  `Human:`/`Assistant:` turns, frame and system tags, forged hook JSON, and invisible
  characters (the soft hyphen, zero-width and filler characters, bidirectional
  controls, Unicode tag characters, and variation selectors in a run or from the
  supplementary block). The text is folded once (lowercase, whitespace runs to one
  space or, across a paragraph break, to one blank line, markdown's `_emphasis_` and `__bold__` underscores to a space; underscores
  inside a word stay). Every match starts with one of its rule's trigger literals, so
  a pattern runs only from a trigger, over a window its longest match fits in, and a
  rule that needs a delimiter (`|` for `curl | sh`, `>` for a tag) runs only when its
  window holds it. A scan reads at most 128 KiB and reports `scan_truncated` past
  that. A match budget (128 pattern tries, plus one per 64 bytes scanned) bounds the
  matching to about 6 ms for 128 KiB of text dense with triggers; a scan that spends
  it stops and reports `scan_saturated`. Both flags count as matches.
- **Where it runs.** `recall` (every render, the memory index and fetch by id) labels
  each entry with `injection_flags` and `quarantine` and adds a `data_notice`.
  `remember` keeps flagged text and returns the flags with a warning. Every evidence
  projection, which is every MCP tool result and every captured native output,
  carries `injection_flags`. A projection that is JSON (every xMustard tool result,
  and another server's JSON) is scanned as the text it encodes: member names and
  decoded string values, so a phrase that wraps across an escaped `\n`, and the
  `<` and `>` that Go's encoder writes as `\u003c` and `\u003e`, read as they do in
  raw text. Other text is scanned as it is. A flagged result carries an
  `[xmustard injection-check]` line saying it is data, not instructions: the MCP
  bridge adds it to the tool result, a hook adds it to the shaped output it puts in
  place of a native tool's (when it reduces that output; otherwise the native output
  reaches the agent unchanged, with no such line), and the Pi adapter adds it to the
  text it renders. The initialize instructions say that memory, tool output and
  `<xmustard-data>` blocks are data, not instructions. `ground` returns counts and
  ids only, so it carries no agent-written text.
- **Pushed surfaces.** Memory pushed into context unasked goes through
  `workspaceops.AdmitMemory`. Its production caller is the Claude Code hook service
  (WS-23): the memories bound to a file a tool reads or edits, those matching a search
  pattern, those a prompt keyword triggers, and the core-tier memories at SessionStart
  and SubagentStart. The core tier's own surface (WS-31) is planned and will use the
  same gate. No pushed surface shipped in v0.1.0. The checks run in order:
  the entry exists in the workspace, it is served, its content matches its digest, it
  is not quarantined, it has a human approval, and it scans clean (a truncated scan
  counts as flagged). What passes is framed in `<xmustard-data>` blocks after a notice.
  The frame cannot be closed from inside: a frame tag in the text is written with
  `&lt;`.
- **Human approval** of a memory is an `approve` verdict on its served revision, in the
  current vote epoch. The verdict must be recorded with kind `human`, and its principal
  must be a human approver of the workspace now: a file-backed token of kind `human`
  with the `human-approver` role (admin holds it), scoped to the workspace, unexpired
  and not revoked. The approver must not have written the entry or the served
  revision. Approval by a quorum of agents, or by anyone in open mode, is not human
  approval. So without a human approver, nothing is pushed.
- **Quarantine.** A write that cites an evidence handle captured from a tool other than
  the workspace's own file, search, list, diff and shell tools or xMustard's tools
  (for example WebFetch, WebSearch, a browser or another MCP server) marks the entry
  `quarantine: untrusted_capture:<tool>`. The list of trusted tools is an allowlist, so
  an unknown tool name is untrusted. A capture of one of xMustard's own tools records
  the first quarantined memory its original carries (a `"quarantine"` member, which
  text quoted inside a JSON string cannot form), and a write that cites it is
  quarantined for the same reason. The capture records its quarantine when it is
  taken; a capture from before that is judged by its tool name. An importer marks
  foreign memory `foreign_import`. The mark is sticky: an edit that cites such a
  capture adds it, a classification change keeps it, and the classify event that adds
  it records it in the history. A quarantined entry is still served on recall,
  labeled, but it is never pushed. It never holds the core tier: the store refuses it
  on creation and on a tier change, and a core entry refuses an edit that would mark
  it. Capture is on since v0.1.1, so a hook's capture of WebFetch or another server's
  MCP tool can now mark a memory that cites it. In v0.1.0 capture was
  refused, every retained original was filed under one of xMustard's own nine tools,
  and `untrusted_capture` had nothing to fire on.
- **Limits.** The scan is a pattern check: a paraphrase it has no rule for passes, and
  a legitimate memory that quotes an injection is flagged (and so never pushed).
  Quarantine sees only what a write cites; content an agent copies from the web
  without citing the capture is not marked. The capture's tool name is the one the
  capturing client recorded. A native tool's output that is not reduced passes
  through unchanged, so no injection-check line reaches the agent with it. The
  scan reads the JSON of a projection up to 256 KiB as decoded text; longer JSON is
  scanned raw (and is flagged `scan_truncated` in any case).

### Changes for existing tokens

Before the role table, an `agent` token could call every route except the admin
routes (settings, providers, `POST /api/routes`, token administration, the auth
audit log, the full memory list and the workspace-wide evidence purge). These routes
now need a role that `agent` (`proposer+verifier`) does not hold, so an `agent`
token gets `403 missing_role`:

| Route | Role now required |
|---|---|
| `POST /api/workspaces/{id}/index` (rebaseline) | `indexer` |
| `POST /api/postgres/bootstrap`, `POST /api/integrations/test`, `POST /api/workspaces/{id}/integrations` | `admin` |
| `POST`/`DELETE .../verification-profiles` (definitions) | `admin` |
| `POST /api/workspaces/{id}/audit-log` | `admin` |
| the five `/api/terminal` routes, including `GET .../read` (a `readonly` token could read terminal output before) | `admin` |
| `PUT /api/workspaces/{id}/policy`, `PUT .../security/acceptance-criteria`, `PUT .../security/findings/{finding_id}/disposition` | `human-approver` |
| `POST .../runs/{run_id}/accept`, `POST .../runs/{run_id}/plan/approve`, `POST .../runs/{run_id}/plan/reject` | `human-approver` |

Workspace registration (`POST /api/workspaces/load`) was also admin-only for a
while, which broke the MCP shim's auto-registration for every `agent` token (`403
admin role required`). It now needs `proposer`. An `admin` token still registers any
directory. Any other token registers only the top level of a git work tree under a
directory the operator names in `XMUSTARD_REGISTER_ROOTS`. That setting is empty by
default, so without it only `admin` registers, and other tokens get
`403 registration_not_allowed`. A registered root becomes readable, through `search`,
`explain` and `ground`, by every token that can reach the workspace, so the roots
bound what an agent can expose. See [Workspace registration](#workspace-registration).
Unauthenticated open mode is unchanged: the single local identity passes every role
gate and registers any directory.

## Exposure posture

| Control | Setting | Behavior |
|---|---|---|
| Loopback bind | `XMUSTARD_API_HOST` (default `127.0.0.1`) | A non-loopback bind needs `XMUSTARD_AUTH=required`, minted credentials, and TLS (or `XMUSTARD_ALLOW_INSECURE_BIND=1` behind a TLS proxy). |
| Host allowlist | `XMUSTARD_ALLOWED_HOSTS=name,...` | On a loopback bind only `localhost` and loopback IPs are accepted as `Host`, plus listed names. This blocks DNS rebinding. On other binds the list applies when it is set. A refusal answers `403 host_not_allowed`. |
| Origin check | `XMUSTARD_ALLOWED_ORIGINS=https://ui.example,...` | A request that carries `Origin` must come from a loopback origin (loopback bind), the same origin (other binds), or a listed origin. `Origin: null` is refused. A refusal answers `403 origin_not_allowed`. |
| No keys in URLs | none | `token`, `access_token`, `api_key`, `key`, `password` and similar query parameters answer `400 query_credentials`, even when an `Authorization` header is also present. Send `Authorization: Bearer <token>`. |
| Read-only mode | `XMUSTARD_READ_ONLY=1` | Mutating routes answer `403 read_only`, and `remember`/`verify` disappear from `tools/list`. Three kinds of non-GET route stay available: capture and revocation of the caller's own tool output (the Claude Code hook routes included; read-only mode records no run outcome from them), policy evaluation, and token revoke and rotate, so an admin can cut off a leaked token without leaving read-only mode. The `/mcp` endpoint stays available too; its tool calls meet their own gates. Minting and the workspace-wide evidence purge are refused. |
| Tool disable | `XMUSTARD_DISABLED_TOOLS=impact,why_failed` | The tool's route answers `403 tool_disabled` and the tool leaves `tools/list`. Unknown names stop startup. |
| Workspace allowlist | `XMUSTARD_WORKSPACE_ALLOWLIST=ws-a,ws-b` | Other workspace ids answer `403 workspace_not_allowed`. The same rule filters `GET /api/workspaces` and checks `POST /api/workspaces/load` (by the id the root would get) and the terminal routes. |
| Remote execution | profile | Terminals exist only in the platform profile and need `admin`. |
| why_failed command mode | `XMUSTARD_WHY_FAILED_COMMANDS=1` (default `0`: off on every bind, loopback included) | Read at startup. On, `why_failed` with `command` runs a test, build or lint command for an authenticated `admin` only; off, every command answers `403 commands_disabled`. Enabled command mode is trusted execution of the repository's code on the server host by admin credentials; the program allow-list is not a sandbox ([Commands why_failed runs](#commands-why_failed-runs-ws-21)). Logs and evidence handles are explained either way. Anything but `0` or `1` stops startup. |
| Registration roots | `XMUSTARD_REGISTER_ROOTS=/srv/checkouts:/home/ci/src` (OS path list: `:` on Unix, `;` on Windows) | Where a non-admin token may register a git work tree. Empty (the default) means only `admin` registers. Each entry must be an absolute path and not a filesystem root, or the API stops at startup. See [Workspace registration](#workspace-registration). |
| Registration limit | `XMUSTARD_REGISTER_LIMIT=50` (the default) | How many workspaces one non-admin principal may register. A load over the limit answers `403 registration_not_allowed`, refusal `register_limit`. Anything but a positive integer stops startup. |

Every `{wildcard}` in a route is checked before the handler runs. Values are read
from the escaped path and decoded per segment, as `ServeMux` reads them, so an
encoded `/` cannot hide a second segment. Workspace, memory entry, run, terminal and
token ids must be plain identifiers (letters, digits, `_`, `.`, `-`, no `..`). The
other ids (issue, view, provider name and the rest) may hold other characters but
must be one path segment: no `/`, `\`, control character or `..`, and not `.`.
Anything else answers `400 invalid_id`. The run store also refuses a run id that is
not a plain identifier.

If the mux matches a route that has no gate row, the request answers
`500 unclassified_route` instead of reaching the handler. `gatedMux` does not expose
the underlying `ServeMux`, so routes can only be registered through the table.

Bearer tokens are compared as SHA-256 digests with `crypto/subtle`. Each credential
is compared in constant time, the scan does not stop at the first match, and
environment credentials take precedence over file credentials. The token file is
parsed once and reused while its inode, modification time and size are unchanged.
A file written within the last two seconds is re-read on the next request. Mint,
rotate and revoke drop the cached copy explicitly.

## Workspace registration

`POST /api/workspaces/load` registers a directory as a workspace and scans it into
xMustard's store. The MCP server calls it on an agent's first tool call in an
unregistered git repository, on the `/mcp` endpoint and in the stdio shim alike
(auto-registration, `api-go/internal/mcpserver/resolve_workspace.go`;
`XMUSTARD_MCP_AUTO_REGISTER=0` turns it off), with the agent's own token. The code is
`api-go/cmd/xmustard-api/workspace_register.go` (the HTTP policy) and
`api-go/internal/workspaceops/register_roots.go` (the path checks).

| Caller | May register |
|---|---|
| `admin` token | any directory, as before (inside its scope when the token is workspace-scoped) |
| open mode (no credentials), `XMUSTARD_AUTH=off` | any directory, as before |
| `proposer` token (`agent` included) | the top level of a git work tree that resolves under a directory in `XMUSTARD_REGISTER_ROOTS` |
| `reader`, `verifier`, `indexer` or `human-approver` token without `proposer` | nothing: `403 missing_role` naming `proposer` |

A workspace-scoped token, `admin` included, registers only a workspace inside its
scope: a load whose root would get another workspace id answers `403
registration_not_allowed` with refusal `token_scope`.

For a non-admin token the API checks that:

1. At least one registration root is configured.
2. `root_path` is absolute.
3. The path, with symlinks resolved (`EvalSymlinks`), exists and is at or below a
   registration root, also resolved. A symlink or `..` that leads outside every
   root is refused. Every path that fails this check gets one answer,
   `outside_register_roots`, whether it is missing, lies outside, or leads outside
   through a symlink planted inside a root, and the answer never names where the
   path leads. So the check cannot be used to learn whether a path outside the
   roots exists or where a link points.
4. No path element is `.git`, before or after symlinks are resolved. This refuses
   `.git` and anything inside it.
5. The directory is the top level of a git work tree whose git directory is inside
   the same registration root. `.git` is a directory, a symlink, or a file that
   starts with `gitdir: ` (a linked worktree or a submodule; a relative path is
   taken from the work tree). It must resolve, inside the root, to a directory with
   `HEAD`, and `objects` either there or in the directory its `commondir` file names
   (a linked worktree), which must be inside the root as well. A `.git` leading to a
   repository elsewhere is refused with one answer, whether that repository exists
   or not: git follows the pointer, so `ground` and `impact` would otherwise report
   the other repository's tracked files, `HEAD`, branch and authors. A bare
   repository, a subdirectory of a work tree (the answer names the top level to
   register), a plain directory and a regular file are refused.
6. The workspace id the path gets passes `XMUSTARD_WORKSPACE_ALLOWLIST`. A miss
   answers `403 workspace_not_allowed`, as it does on every route and for every
   caller, and is not written to the auth audit log.
7. The principal has registered fewer than `XMUSTARD_REGISTER_LIMIT` workspaces
   (default 50). The count is taken under the registry lock, so concurrent loads
   cannot exceed it. Reloading a workspace that is already registered does not
   count.

A refusal of checks 1 to 5 and 7, or of the token scope, answers `403` with
`reason: "registration_not_allowed"`, a `refusal` code (`no_register_roots`,
`invalid_path`, `outside_register_roots`, `git_internals`, `bare_repository`,
`not_work_tree_top`, `not_git_work_tree`, `register_limit` or `token_scope`) and a
message that names what to do. It is written to the auth audit log as a `denied`
event (denied events are throttled to one a second, with a count of those
suppressed). The MCP server puts the message in its resolution error, so the agent
can tell the user that an admin has to register the repository or add its parent
directory to `XMUSTARD_REGISTER_ROOTS`.

When the path is admitted, the API registers the resolved path, not the path as
sent, so a symlink in the request is never stored and repointed later. A directory
that is already registered is reused, whatever spelling or link reached it: the
lookup matches the registered roots by `os.SameFile`, so a case variant on a
case-insensitive filesystem (macOS, Windows) or a path an admin registered
through a symlink is the same workspace, not a second one. A non-admin caller
never decides whether to scan. A new root is registered with its first scan,
whatever `auto_scan` says. A registered root is served from its cached snapshot,
or answers `404` when that snapshot is missing, stale or over 25 MiB; only an
admin load, `POST /api/workspaces/{id}/index` (indexer) or `POST .../scan`
(platform profile) rescans it. If the first scan fails, the entry stays and one of
those rescans it. A non-admin load keeps an existing workspace's name. `admin` and
open-mode loads behave as before.

Every load that creates a registry entry, whoever made it, is recorded in the auth
audit log (`GET /api/auth/audit`, admin) as a `register` event. The event's `actor`
is the principal, or `anonymous` in open mode, and its detail names the workspace
id and root. The detail also says when a non-admin registered it under
`XMUSTARD_REGISTER_ROOTS`. The log keeps the newest 5000 events, and token
administration (`mint`, `revoke`, `rotate`) rolls off last: registrations and
denials are dropped before it, so neither can push an admin's token history out of
the log.

### A registered root stays checked

The checks above hold when the directory is registered, but an agent can write its
own checkout, and every work tree nested in it, afterwards. It could register a
nested directory, then swap that directory (or one of its parents) for a symlink
to anywhere the API can read, or rewrite its `.git` to point at another
repository. Reads follow such a swap at once: no rescan is needed.

So the registry entry of a non-admin registration records the registration root
(`register_root`) and the principal (`registered_by`). Neither changes afterwards,
whoever reloads the workspace. Every use of that workspace's root, from the
registry or from its snapshot, re-runs the checks the filesystem can invalidate:
the root must still resolve to itself, with no element a symlink, and its `.git`
must still lead to a git repository inside the registration root. When either
fails, the workspace answers `409` naming the problem, and xMustard reads nothing
through it (no scan, search, explain, ground, memory anchor or worktree read) until
the directory is restored. A root an admin or open mode registered is used as
registered, symlinks included.

Two gaps remain, and both need an agent that can write inside a registered work
tree or its parent directory:

- The check runs before each use, not inside it. A swap made in the instant
  between the check and the file read or `git`/`xmustard-core` call that follows
  can still redirect that one operation. Closing it would need every read to walk
  from a directory handle opened without following links, which the Rust core and
  `git` do not do.
- xMustard runs `git` in registered work trees, and git honours the repository's
  own configuration and layout: `core.worktree`, `core.fsmonitor`, alternates, and
  symlinks inside `.git`. An agent that can write a work tree's `.git` can use them
  whoever registered it; this predates non-admin registration.

Choose registration roots that hold only checkouts agents are meant to index, with
no directory above a checkout that agents can write. The first gap then needs a
work tree nested inside an agent's own checkout, which the agent can create and
register itself. Where an agent must not reach files outside its checkouts through
xMustard even by that race, leave `XMUSTARD_REGISTER_ROOTS` empty and register
repositories as admin.

## Path confinement

Memory anchor paths (`remember` `paths`) must stay inside the workspace root.
Absolute paths, `..` escapes, and paths that leave the root through a symlink
(`EvalSymlinks` on the target or its nearest existing ancestor) are refused with
`path escapes workspace` (HTTP 400). Anchor hashing then reads the file through a
symlink-refusing descriptor walk (`api-go/internal/workspaceops/safepath_unix.go`),
so a path swapped for a symlink later is still refused. `explain` applies the same
symlink check to its `path` before the core reads the file.

Fixed in v0.1.1, a known issue in v0.1.0: that walk compared its final descriptor
with the root descriptor it had already closed, so when the OS handed the same number
back, a present file read as missing (every path of even depth, such as
`pkg/auth.go`). It failed closed: nothing outside the root was read. The cost was
freshness, since a memory anchored to such a path was baselined as missing and never
flagged stale when the file changed. The walk now tracks whether it opened a
component (`TestConfinedReadOfEveryDepth`;
[Architecture](ARCHITECTURE.md#fixed-since-v010)).

## Commands why_failed runs (WS-21)

**Command mode is off by default.** `why_failed` with `command` (`POST
/api/workspaces/{id}/why-failed`, or the MCP tool's `command` argument, which reaches
the same route as its caller) runs a process on the server host. It runs only when both
hold:

1. the operator started the server with `XMUSTARD_WHY_FAILED_COMMANDS=1` (read once at
   startup; there is no per-request or loopback default), and
2. the caller is an authenticated principal holding `admin`. Open mode (no credentials)
   has no authenticated principal, so it never runs a command.

The route checks, in this order and before anything is spawned, a command slot is taken
or an analysis window is admitted: the route gate (the `proposer` role, read-only mode,
`XMUSTARD_DISABLED_TOOLS=why_failed`), exactly one source (a request that also carries
`log` or `evidence_handle`, or a `run_id`, is refused with 400, so a log or an evidence
handle can never start a command), the operator's opt-in (`403 commands_disabled`), an
authenticated principal (`403 admin_required`) and the `admin` role (`403
missing_role`, audited). `workspaceops.RunFailureCommand` checks the same permit again
before its own checks. A read-only MCP connection refuses the argument before any API
call, and the tool is annotated `readOnlyHint: false`, `destructiveHint: true`, so a
client that auto-approves read-only tools still asks.

**Enabled, command mode is trusted host-code execution by admin credentials.** A test
or build runs the repository's own code as the daemon's operating-system user, and a
check-named task runs whatever the repository's build file says it does. The closed
program table below is defense in depth that narrows what a command looks like; it is
**not a sandbox**, and an admin token on a server with command mode enabled should be
treated like shell access to that host. Command-mode trust policy and hardening
(isolation, and one environment-scrub policy in the Rust core for every `run-*`
subcommand) are follow-up WS-21B in the parity build plan.

The command checks (`api-go/internal/workspaceops/outcome_commands.go`) run in this
order and fail closed:

- the command runs as argv through the bounded Rust runner, never through a shell. A
  word that starts like a shell operator or redirection (`|`, `&&`, `;`, `>`,
  `2>/dev/null`, `<`) or holds a substitution (`$(`, a backtick) is refused; other text,
  such as `$HOME` or `*`, reaches the program literally, unexpanded;
- the program must be in a closed table, looked up by its last path element, and used
  the way the table allows:
  - subcommands: `go test|vet|build` of local packages only (`.`, `./...`, `./pkg`; not
    `all`, the standard library or another module's import path) with `-mod` only
    `readonly` or `vendor`; `cargo test|nextest|check|clippy|build`; `dotnet` and
    `swift` with `test|build`; `golangci-lint run`; `ruff check`; `biome check|lint|ci`;
  - package scripts: `npm`, `pnpm`, `yarn` and `bun` running a check-named script
    (`npm test`, `npm run lint`, `yarn build:prod`, `bun test`);
  - tasks: `make`, `just`, `task`, `gradle`/`gradlew` and `mvn`/`mvnw` with check-named
    tasks and only the flags `-k`, `-s`, `-q`, `-B` and `-jN`, their long forms, and
    `--offline`, `--continue`, `--stacktrace`, `--info` and `--no-daemon` (no `-f`, `-C`, `-I`,
    `VAR=value` or default target). Maven takes lifecycle phases only: a word with a
    colon names a plugin goal (`prefix:goal`, `group:artifact:version:goal`), which
    Maven resolves and downloads, and is refused;
  - test runners, linters and type checkers with any arguments: `pytest`, `py.test`, `unittest`, `jest`,
    `vitest`, `mocha`, `rspec`, `phpunit`, `ctest`, `tsc`, `eslint`, `mypy`, `pylint`,
    `flake8`, `pyright`, `staticcheck`, `shellcheck`, `rubocop`, `stylelint`, `oxlint`,
    and `python -m pytest|unittest|mypy|pylint|flake8|ruff`.

  A check name is `test`, `tests`, `check`, `lint`, `build`, `vet`, `verify`,
  `typecheck`, `compile` or `e2e`, alone or continued by a separator or a capital
  (`test-unit`, `lint:fix`, `testDebugUnitTest`). Everything else is refused: shells,
  interpreters, wrappers (`sudo`, `env`, `npx`), `go run|generate|install`,
  `cargo run|install`, `npm install|exec|publish`, `make deploy`, `mvn deploy`,
  `gradle publish`. So are flags that choose a program, shell, linker, module file or
  configuration: `go -exec|-toolexec|-vettool|-ldflags|-gccgoflags|-compiler|-modfile|-overlay`,
  `cargo --config|-Z`, and the package managers'
  `--script-shell|--node-options|--onload-script|--config`;
- only the `gradlew` and `mvnw` wrappers may be named by a path; every other program is
  a bare name (`./node_modules/.bin/jest`, `venv/bin/python` and `./make` are refused);
- the working directory is the workspace root or a directory inside it (the path
  confinement above, symlinks included);
- no argument names a path outside the working directory: an absolute or `~` path, or a
  `..` element, wherever a program may read a path in the argument (its start, after a
  one-letter flag such as `-I`, or after `=`, `:`, `,`, `;`, `@`, a space or a quote), is
  refused, and so is an argument or a flag's value that resolves through a symlink out
  of the root. Pass paths relative to `cwd`, and set `cwd` to reach another part of the
  tree;
- a wrapper named by path is resolved from the working directory with its symlinks and
  must be an executable file inside the workspace root (`./gradlew`); a bare name must
  be on the daemon's `PATH`. The resolved file is what runs, and a program that cannot
  start answers 400, not a retryable 503;
- the timeout is 1 to 240 s (1 to 50 s through MCP, below the clients' call timeout);
  at the timeout the runner sends TERM, then KILL, to the command's whole process group.
  The run is detached from the request: a caller that disconnects or cancels gets no
  answer, the command still ends by its timeout at the latest, and no outcome is
  recorded;
- one command runs at a time per server. A command holds one of the helper-process
  slots every tool call shares, so a second one answers 503 at once instead of queueing.
  The analysis window is admitted after the command ends, sized to its output.

The command runs with the daemon's environment minus its own configuration (every
`XMUSTARD_*` variable: tokens, the Postgres DSN, TLS keys, the data directory) and minus
every variable the redactor classifies as a secret, so the repository's code cannot
read the daemon's credentials from its environment. This scrub lives in the Go caller
(`workspaceops.commandEnv`, handed to `rustcore.RunManagedCommandEnv`); the other `run-*` subcommands of the core do not
share it yet (WS-21B). Output is redacted (secret rules plus this process's
secret-named environment values) before it is analyzed or stored, and only the last MiB
is read. The command's processes are external to the owned process tree the budget
gate measures.

An outcome keeps a redacted tail, error lines and failing test names from its output.
Revoking an evidence original (`DELETE .../evidence/{handle}`) or the admin purge
(`DELETE .../evidence`) removes the outcomes made from it. An admin removes any other
outcome with `DELETE .../outcomes/{outcome_id}`, for example one holding a secret the
redactor missed.

## Human approvals (WS-57)

A human approver records a human's decision on memory and on merges. The record is
advisory: it says what a person decided, and enforcement stays with branch
protection or whatever policy consumes it. xMustard never merges.

**Who is a human approver.** A principal of kind `human` that holds the
`human-approver` role (`admin` holds it too), unexpired, not revoked, and allowed on
the workspace. `xmustard-api mint-token` mints tokens of kind `agent`, so mint an
approver through the admin API:

```bash
curl -H "Authorization: Bearer $ADMIN" \
  -d '{"id":"alice","role":"human-approver","kind":"human","presence_only":true}' \
  http://127.0.0.1:8042/api/auth/tokens
```

`presence_only` is optional. A presence-only token is accepted only when typed at the
`xmustard-ops` prompt: the API refuses it as a bearer token (`401
presence_only_token`), and `xmustard-ops` refuses it from a file or the environment.
Both refusals are audited.

**The CLI.** `xmustard-ops` reads the approver's token from `--token-file`, then from
`XMUSTARD_APPROVER_TOKEN`, and otherwise prompts at the terminal with echo off.

| Command | Does |
|---|---|
| `xmustard-ops approve <workspace_id> <entry_id> [--revision N] [--note TEXT]` | Casts an approve verdict on the served revision, or on a pending edit with `--revision` |
| `xmustard-ops reject <workspace_id> <entry_id> [--revision N] [--note TEXT]` | Casts a reject verdict the same way |
| `xmustard-ops queue <workspace_id> [--limit N] [--baselines N]` | Lists the pending memory writes this approver may vote on, oldest first, and recent index baseline builds |
| `xmustard-ops review approve <workspace_id> --base REF [--head REF] [--review ID]... [--note TEXT]` | Records a merge attestation |
| `xmustard-ops review revoke <workspace_id> --approval SEQ --reason TEXT` | Withdraws an attestation |
| `xmustard-ops review gate <workspace_id> --base REF [--head REF]` | Reports the attestation state; needs no token |

Every command takes `--data-dir` (default `XMUSTARD_DATA_DIR`, else
`../backend/data`). Exit codes: 0 done, 1 error, 2 usage; `review gate` exits 0 for a
current attestation, 3 for none and 4 for a stale one.

**What a verdict counts for.** A human verdict is one more distinct vote. It counts
toward the entry's quorum like any peer's, under the same owner-distinct policy, and
never promotes on its own when the gate needs more peers. A human approver cannot
approve or reject memory they wrote or a revision they authored. A verdict on the
served revision is pinned to the revision that was checked, so a newer revision is
refused rather than voted on unseen.

**Merge attestations.** `review approve` binds to one reviewed change: the repository,
the merge base of `--base` and `--head`, the head commit, and the SHA-256 of the diff
between them. The attestation turns stale as soon as any of those differ. A human
approver can revoke it, and it stops counting when its approver's token is revoked.
Attestations are created only from `xmustard-ops`, never over MCP or HTTP. Each one is
labelled `attestation only: xMustard never merges, never changes branch protection and
never posts to a pull request; enforce merges with branch protection`. `review gate`
is meant for a person's own pre-push hook. It has no signed export, so it is not a CI
control.

An attestation may cite review records (`--review ID`). It is refused, failing closed,
when a cited record was written by the approver, was written in open mode, or, under
the owner-distinct policy, was written under the approver's owner. An id that names
no record is bound as given and listed as `review_records_unknown`. Review records
themselves (`xmustard-ops review record|show|triage`) exist only in builds with the
`review` tag, which release builds leave off: `record` needs the proposer role and
`triage` the verifier or human-approver role, the author of a finding can mark it
`fixed` or `wont_fix` but never confirm or dismiss it, and only a distinct principal's
`confirm` corroborates. Secrets are redacted from a finding's text, quoted code and
notes before it is stored. A review record is evidence; it never approves a change.

**MCP elicitation.** A governed-memory write made under a human approver's token
through the MCP bridge is held with `human_presence_required`, together with the text
the human must confirm and its SHA-256 digest. When the client declared the
elicitation capability, the bridge shows the human that text and repeats the call
once with the confirmation and the digest. The write runs only while the digest still
matches. A decline, a cancel, a timeout, or a client that cannot be asked leaves the
write unrecorded.

**Assurance labels.** Every human-approver write records `<surface>/<assurance>`.
The surface is `ops`, `mcp_elicitation` or `http`. The assurance is `user_presence`
only for a presence-only token typed at the `xmustard-ops` prompt; every other write
is `advisory`, because the same token also works from files, the environment and
client configurations that agent processes of the same user can read.
`user_presence` is not proof of presence either: a process that learns a
presence-only token can type it into a pseudo-terminal.

## Secret redaction and secret paths

**Redaction.** `api-go/internal/redact` is one engine for strings and streams. It
replaces each secret with a marker that names its rule, `[REDACTED:rule]`, or
`[REDACTED:env:NAME]` for an environment value. Its rules cover bearer and basic
credentials, AWS, GitHub, GitLab, Slack, OpenAI, Anthropic, Google, Stripe, npm,
Hugging Face and xMustard tokens, JWTs, URL passwords and PEM private keys, plus a
key-aware detector for secret fields in JSON, YAML, env files, headers and command
flags. Generic key/value hits pass an entropy and placeholder check, so hashes, UUIDs
and `${TOKEN}` references are left alone.

It runs on memory ingest ([Provenance, evidence-bound votes and ingest
redaction](#provenance-evidence-bound-votes-and-ingest-redaction-ws-19b)), on
`why_failed` output, on review findings recorded under the review build tag, and,
since v0.1.1, on every evidence capture.

### Evidence capture redaction (WS-FIX-03)

Capture is on in every build, and every capture is redacted.
`POST /api/workspaces/{id}/evidence/capture` streams the tool output (a raw body, or
the output strings of a Claude, Codex, Cursor, Pi or OpenCode hook body) through the
shared `redact` rules before a byte reaches the spool. The retained original, its
pages, search inside it and the projection returned to the client therefore all hold
the redacted text. The route refuses a `client` it does not know with
`400 invalid_client`.

In v0.1.0 no redactor was wired into `captureRedactor`
(`api-go/cmd/xmustard-api/evidence_capture_routes.go`), so every capture answered
`503 redaction_unavailable`: the Pi adapter's built-in reduction and compaction fell
back to Pi's own behavior, and no hook body could be captured. Only the
`-tags xmustard_e2e` test build captured, with a test-only marker. That build now
chains its marker in front of the production redactor, so the e2e runs the
production rules too.

- **Rules.** Capture uses the rules `why_failed` applies to command output
  (`workspaceops.OutputRedactor`): the default secret patterns (bearer and basic
  credentials; AWS, GitHub, GitLab, Slack, OpenAI, Anthropic, Google, Stripe, npm,
  Hugging Face and xMustard tokens; JWTs; URL userinfo passwords; private keys), the
  key-aware detector for secret fields in JSON, YAML, env files, headers and flags,
  and the literal values of this daemon's secret-named environment variables. Each
  secret becomes `[REDACTED:<rule>]`.
- **Streaming.** `redact.Writer` buffers 128 KiB and decides it 8 KiB at a time.
  Each window is read with the lookahead after it, about 33 KiB, the longest span
  any detector reads past a match (a private key body and its END marker). A secret
  split across writes or windows is therefore seen whole, and the output is byte
  for byte what one pass over the whole input gives. The stream is never buffered,
  and a window's scratch and output are sized by the 8 KiB it decides, not by how
  many secrets it holds. Measured on 16 MiB in the decoder's 32 KiB writes, the
  redactor's live heap peaks at 0.4 MiB at most, on input that is nothing but
  secrets (an 8-byte secret environment value repeated, the densest case), under a
  quarter of the 2 MiB capture window. The hook decoder flushes it at each section
  boundary, so each output string is redacted as one input.
- **Secret paths.** When any path the capture names is on the secret-path denylist
  (below), the capture is refused with `422 secret_path` and nothing is retained. The
  paths checked are the caller's `path`, every path field of the hook body's tool
  input (`file_path`, `filePath`, `path`, `target_file`, `notebook_path`,
  `directory`, `dir_path`), and every such field of its response (Claude Read's
  `file.filePath`), wherever they appear in the body. One path cannot hide another: a
  caller's `path=README.md` does not admit a body that reads `.env`. The client keeps
  its own result.
- **Fail closed.** A server without a redactor answers `503 redaction_unavailable`.
  A redactor that fails (panics) answers `503 redaction_failed`. On every refusal
  the spool is discarded, so nothing is retained. The [hook routes](#client-hooks-ws-23)
  capture through the same redactor, wrapper and checks; a hook whose capture is
  refused retains nothing and leaves the client its native output.
- **Limits.** Redaction is pattern-based. A secret that no rule recognizes is
  retained as written: for example, a bare password in a line of code, or an
  unquoted token-named value without a digit (the `redact` package documentation
  lists the limits). Such an original can be revoked with
  `DELETE .../evidence/{handle}`, and the admin purge (`DELETE .../evidence`)
  removes every original of a workspace. Only named paths are matched: a secret
  file read through a shell (`cat .env`), and the matches of a search over a
  directory (a Grep over `/repo`, with or without a `glob` such as `.env*`), pass
  through the content rules only. A secret split across two output strings of one
  hook body, which are redacted as separate inputs, is not joined. The 16 MiB limit on
  a retained original applies to the redacted bytes. `capture.body_sha256` is the
  digest of the body as received; it names the body but does not reveal it.

### Secret paths

Some files are credential stores whatever redaction would find in them: `**/.ssh/**`, `id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`, `.netrc`, `_netrc`,
`.npmrc`, `.pypirc`, `.dockercfg`, `.env` and `.env.*`, except the `.env.example`,
`.env.sample` and `.env.template` templates. The list lives in
`api-go/internal/redact/secretpath.go`, with a copy in `rust-core/src/secretpath.rs`;
both are tested against `rust-core/src/testdata/secret_path_golden.tsv`. Search never
shows their text: the index lists a secret doc with the `secret_path` loss and never
reads it, and a snippet read refuses a secret path before it opens the file. Snippet
lines also mask credential-shaped words. Capture refuses the output of a secret path
(`422 secret_path`, above), and the hook service's syntax check refuses one too.

## Client hooks (WS-23)

Claude Code posts hook events to `POST /api/hooks/claude/<Event>` (an http hook) or
through the static client `xmustard-hook` over a Unix socket (a command hook).

- **Authentication and roles.** Every hook route has a row in the route gate table
  below: core, the proposer role, served in read-only mode (a hook stores only the
  caller's own tool output, and read-only mode records no outcome). A reader-only
  token drives no hook. The plugin sends `Authorization: Bearer ${XMUSTARD_API_TOKEN}`
  (an http hook's `allowedEnvVars`); the client sends the same variable.
- **Workspace.** A hook acts on the workspace the `X-Xmustard-Workspace` header names,
  else on the registered root that holds the event's `cwd` (the deepest one; a hook
  never registers a repository). The workspace then passes the checks a workspace path
  gets, in order and failing closed: a safe id, served by this deployment, in the
  token's scope. A refusal answers empty and is audited.
- **The socket.** Both sides check it in order, failing closed
  (`transport.CheckDir`, `transport.CheckSocket`): its directory must be a directory
  (not a symlink) owned by the user, with no group or other permission bits, and the
  socket must be a socket owned by the user. Another local user can create the
  default directory under a shared temp dir first (`/tmp/xmustard-<uid>` when
  `XDG_RUNTIME_DIR` is unset); the daemon then does not listen there, and the client
  does not dial it, so its token and event go to the TCP address instead. The daemon
  creates the directory 0700 and the socket 0600, replaces only a stale socket the user
  owns (never another daemon's live socket, another user's file or a file that is not
  a socket), and serves only `/api/hooks/` there through the full middleware stack.
  `XMUSTARD_HOOK_SOCKET=off` disables it.
- **Captures.** A PostToolUse or PostToolUseFailure body is captured through the
  capture route's redactor and checks ([Evidence capture
  redaction](#evidence-capture-redaction-ws-fix-03)) before
  anything is retained, bound to the calling principal, and recorded
  `captured_identity=unknown` (the producing repository state was not observed). The
  session and subagent ids are recorded for attribution only. A capture refused for a
  secret path or a redactor failure retains nothing and fails open: the client keeps
  its native output, and the refusal does not count as the daemon being busy.
- **What a hook may do.** An answer adds context, replaces a native output with its
  shape-matched reduction (PostToolUse only), sets the FileChanged watch list
  (SessionStart and CwdChanged), or says nothing. It never
  allows, denies or rewrites a tool call. A timeout past the ~200 ms budget, a body the
  service cannot read and a workspace out of scope are empty 200s, so Claude Code's own
  behavior is unchanged. Authentication stays fail-closed: a missing, expired, revoked
  or reader-only token gets 401 or 403, which Claude Code shows as a non-blocking hook
  error on every matched call (the static client prints nothing for it). A missing
  daemon is a non-blocking error for an http hook and silence for the client.
  WorktreeCreate is not hooked: its hook replaces git worktree creation. WorktreeRemove
  goes through the client, which always exits 0, because a failing WorktreeRemove hook
  blocks removal, and acts only on a worktree at or under the caller's workspace root.
- **Pushed memory** follows the pushed-surface policy above; index hits are framed as
  data and scanned. Each context (the main thread, and each subagent by its
  `agent_id`) gets a memory at most once until compaction, and a memory counts as
  given only when the answer that carries its frame, or the note that withheld it, was
  written. A memory that does not fit in the ~10,000-character context left is counted
  in the withheld note as `too_large`, with the `recall` call that shows it.
- **Processes.** The daemon starts no process for a hook: Rust work runs only on a
  resident worker that is already running. Claude Code itself starts the static client
  once per event for the two command hooks, SessionStart and WorktreeRemove; every
  other event is an http hook to the running daemon.

## Daemon service (WS-58)

`xmustard-ops setup` installs the API as a per-user service: a launchd agent on macOS,
a systemd socket and service on Linux (`api-go/internal/daemon`).

- **Loopback only, no credential in the unit.** Every unit binds `127.0.0.1`. Setup
  refuses an extra variable (`--env`) whose name looks like a credential (the four
  token variables, or any name containing TOKEN, SECRET, PASSWORD, PASSWD,
  CREDENTIAL, APIKEY, API_KEY, PRIVATE_KEY or DSN), one that sets what setup sets
  itself, and values with control characters. The systemd service also unsets
  `XMUSTARD_APPROVER_TOKEN`, `XMUSTARD_API_TOKEN`, `XMUSTARD_TOKEN` and
  `XMUSTARD_AUTH_TOKENS`, so a token imported into the user manager does not reach
  the daemon; such a daemon takes bearer tokens only from its data dir
  (`xmustard-api mint-token`). `launchctl` and `systemctl` run without the credential
  variables in their environment.
- **Only its own units.** Setup writes units atomically (0644) with the marker
  `generated by xmustard-ops setup`, and replaces or removes only files that carry
  it; a hand-written unit under the same label is refused before any service-manager
  command runs. On a first install it refuses when another xMustard API already
  answers on the port. When the new daemon does not answer `/api/health`, the units
  it changed are put back.
- **The process.** The API drops `XMUSTARD_APPROVER_TOKEN` from its own environment
  before anything starts, so no helper it runs inherits it. A socket that systemd
  passes is adopted only when `LISTEN_PID` names this process and it is exactly one
  TCP socket, and the address it serves meets the same non-loopback interlock as a
  bind ([Exposure posture](#exposure-posture)). The data dir and the log dir are
  created 0700, the rotated log 0600 (10 MiB, 3 generations by default).
- **The store.** Once it listens, the API migrates the governance store and runs
  `PRAGMA quick_check`; a store that fails is refused to every memory call, reads
  included, until a backup is restored. `xmustard-ops store backup` writes a verified
  `VACUUM INTO` copy (0600, created exclusively) on a read-only connection;
  `store restore` refuses a source with a write-ahead log beside it or one that fails
  verification, needs every connection closed, and moves the replaced store aside
  instead of deleting it.
- **Limits.** launchd has no socket activation for the cgo-free API, so the macOS
  agent is resident from login. Health is checked when setup or `daemon restart`
  starts the daemon, not continuously: a hung daemon is not detected.

## Health endpoint

`/api/health` stays public so liveness probes need no token. Its full view shows
host-wide activity: the data-movement counters (spawns, hashed bytes, captures),
the owned process tree and the stdio shims on the host, live external processes,
the heavy-slot owner labels and queue, the resident Rust worker's pid and memory,
the live pool and child counters, the MCP and hook usage counters (`mcp_usage`,
`hook_usage`), and the governance store's startup check with its timing and detail.
While authentication is enforced
(`XMUSTARD_AUTH=required`, or `auto` with credentials minted), only an operator sees
the full view: an `admin` token, or another token that holds more than `reader`, with
no workspace scope. A reader-only token and a workspace-scoped token of any role
(`admin` included) get the public view, which is the same as for a caller without a
token: `status`, the pool size, the child cap, the budget gate and the soft ceiling,
with a `detail` that says why, and, once the daemon has checked its store, the store's
`status` and `schema_version`. A public-view poll never samples the process tree.
With `XMUSTARD_AUTH=off`, or `auto` with no credentials (the open loopback default),
everyone gets the full view.

## Route gate table

Every registered route appears here, core routes first. "Read-only mode" says
whether `XMUSTARD_READ_ONLY=1` still serves the route.

<!-- route-gates:begin (generated by TestSecurityDocRouteGateTable; XMUSTARD_UPDATE_DOCS=1 rewrites it) -->
| Route | Profile | Role | Read-only mode | Tool | Note |
|---|---|---|---|---|---|
| `GET /api/auth/audit` | core | admin | served |  |  |
| `GET /api/auth/principals` | core | admin | served |  |  |
| `POST /api/auth/tokens` | core | admin | refused |  | mint |
| `DELETE /api/auth/tokens/{id}` | core | admin | served |  | revoke; served in read-only mode to cut off a leaked token |
| `POST /api/auth/tokens/{id}/rotate` | core | admin | served |  | replaces the secret; the old one stops working |
| `GET /api/auth/whoami` | core | reader | served |  | caller principal, roles and usable tools |
| `ANY /api/health` | core | reader | served |  | public liveness and limits; the budget block needs an operator token while auth is enforced |
| `POST /api/hooks/claude/CwdChanged` | core | proposer | served |  | hook: watchPaths of the new directory's workspace ([] clears the list) |
| `POST /api/hooks/claude/FileChanged` | core | proposer | served |  | hook: feeds the watcher's pending batch; drops the cached repository identity |
| `POST /api/hooks/claude/PostCompact` | core | proposer | served |  | hook: queued; answered at once |
| `POST /api/hooks/claude/PostToolBatch` | core | proposer | served |  | hook: batch search nudge |
| `POST /api/hooks/claude/PostToolUse` | core | proposer | served |  | hook: captures the caller's own tool output; shape-matched updatedToolOutput |
| `POST /api/hooks/claude/PostToolUseFailure` | core | proposer | served |  | hook: captures the caller's own failed tool output for the run outcome |
| `POST /api/hooks/claude/PreCompact` | core | proposer | served |  | hook: queued; answered at once |
| `POST /api/hooks/claude/PreToolUse` | core | proposer | served |  | hook: index hits and memories for a search pattern or a file |
| `POST /api/hooks/claude/SessionEnd` | core | proposer | served |  | hook: queued; answered at once (1.5 s SessionEnd budget) |
| `POST /api/hooks/claude/SessionStart` | core | proposer | served |  | hook: ground's spawn-free part and core-tier memories as context; watchPaths |
| `POST /api/hooks/claude/Stop` | core | proposer | served |  | hook: queued; answered at once |
| `POST /api/hooks/claude/SubagentStart` | core | proposer | served |  | hook: the same context for a subagent, which has steering state of its own |
| `POST /api/hooks/claude/SubagentStop` | core | proposer | served |  | hook: queued; answered at once |
| `POST /api/hooks/claude/UserPromptSubmit` | core | proposer | served |  | hook: memories a prompt keyword triggers |
| `POST /api/hooks/claude/WorktreeRemove` | core | proposer | served |  | hook: forgets the worktree's cached identity |
| `GET /api/workspaces` | core | reader | served |  | filtered by token scope and workspace allowlist |
| `POST /api/workspaces/load` | core | proposer | refused |  | workspace registration; below admin only a git work tree top level under XMUSTARD_REGISTER_ROOTS; id checked against the allowlist and token scope |
| `GET /api/workspaces/{workspace_id}/changes/since-index` | core | reader | served | impact |  |
| `GET /api/workspaces/{workspace_id}/context` | core | admin | served |  | full memory history |
| `POST /api/workspaces/{workspace_id}/context` | core | proposer | refused | remember | author is the principal |
| `GET /api/workspaces/{workspace_id}/context/active` | core | reader | served | recall | scope=all needs admin; unverified text and history need verifier or human-approver |
| `DELETE /api/workspaces/{workspace_id}/context/{entry_id}` | core | human-approver | refused |  | retract; purge=true deletes the text and keeps a digest tombstone |
| `PUT /api/workspaces/{workspace_id}/context/{entry_id}` | core | proposer | refused |  | memory edit; author or admin only |
| `POST /api/workspaces/{workspace_id}/context/{entry_id}/restore` | core | human-approver | refused |  | restore a retired, retracted, superseded or expired entry |
| `POST /api/workspaces/{workspace_id}/context/{entry_id}/verify` | core | verifier | refused | verify | verifier is the principal |
| `GET /api/workspaces/{workspace_id}/diagnostics` | core | reader | served | diagnostics |  |
| `DELETE /api/workspaces/{workspace_id}/evidence` | core | admin | refused |  | workspace-wide purge of every principal's originals |
| `POST /api/workspaces/{workspace_id}/evidence` | core | proposer | served |  | projection of the caller's own tool result |
| `POST /api/workspaces/{workspace_id}/evidence/capture` | core | proposer | served |  | any tool's output (raw or a client hook body), the caller's own |
| `GET /api/workspaces/{workspace_id}/evidence/search` | core | reader | served |  | issuer-bound search in an original |
| `DELETE /api/workspaces/{workspace_id}/evidence/{handle}` | core | proposer | served |  | issuer revokes its own original |
| `GET /api/workspaces/{workspace_id}/evidence/{handle}` | core | reader | served |  | issuer-bound expansion |
| `GET /api/workspaces/{workspace_id}/explain-path` | core | reader | served | explain |  |
| `POST /api/workspaces/{workspace_id}/index` | core | indexer | refused |  | rebaseline the index; agents cannot reset it |
| `GET /api/workspaces/{workspace_id}/outcomes` | core | reader | served |  | run-independent outcomes, newest first; reads only |
| `DELETE /api/workspaces/{workspace_id}/outcomes/{outcome_id}` | core | admin | refused |  | removes one outcome (a secret the redactor missed in its command or tail) |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/why-failed` | core | reader | served | why_failed |  |
| `GET /api/workspaces/{workspace_id}/search` | core | reader | served | search |  |
| `GET /api/workspaces/{workspace_id}/session-grounding` | core | reader | served | ground |  |
| `POST /api/workspaces/{workspace_id}/why-failed` | core | proposer | refused | why_failed | reads an evidence tail or a log and records the outcome; a command runs only with XMUSTARD_WHY_FAILED_COMMANDS=1 and an authenticated admin (host-code execution; the closed program table is not a sandbox) |
| `DELETE /mcp` | core | reader | served |  | ends the caller's own MCP session |
| `GET /mcp` | core | reader | served |  | no server-initiated stream: 405 |
| `POST /mcp` | core | reader | served |  | MCP messages; each tool call re-enters the API through its own route gate as the caller |
| `GET /api/agent/capabilities` | platform | reader | served |  |  |
| `POST /api/integrations/test` | platform | admin | refused |  | uses supplied credentials |
| `POST /api/postgres/bootstrap` | platform | admin | refused |  | schema DDL |
| `GET /api/postgres/plan` | platform | reader | served |  |  |
| `GET /api/postgres/render` | platform | reader | served |  |  |
| `GET /api/providers` | platform | reader | served |  |  |
| `POST /api/providers` | platform | admin | refused |  |  |
| `DELETE /api/providers/{name}` | platform | admin | refused |  |  |
| `POST /api/providers/{name}/chat` | platform | proposer | refused |  |  |
| `GET /api/providers/{name}/models` | platform | reader | served |  |  |
| `POST /api/providers/{name}/probe` | platform | proposer | refused |  |  |
| `POST /api/route` | platform | proposer | refused |  |  |
| `POST /api/route/chat` | platform | proposer | refused |  |  |
| `GET /api/routes` | platform | reader | served |  |  |
| `POST /api/routes` | platform | admin | refused |  |  |
| `GET /api/runtimes` | platform | reader | served |  |  |
| `GET /api/settings` | platform | reader | served |  |  |
| `POST /api/settings` | platform | admin | refused |  | includes memory verification policy |
| `POST /api/terminal/open` | platform | admin | refused |  | remote shell |
| `DELETE /api/terminal/{terminal_id}` | platform | admin | refused |  | remote shell |
| `GET /api/terminal/{terminal_id}/read` | platform | admin | served |  | remote shell output |
| `POST /api/terminal/{terminal_id}/resize` | platform | admin | refused |  | remote shell |
| `POST /api/terminal/{terminal_id}/write` | platform | admin | refused |  | remote shell |
| `GET /api/workspaces/{workspace_id}/activity` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/activity/overview` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/agent/probe` | platform | proposer | refused |  | executes the configured runtime |
| `POST /api/workspaces/{workspace_id}/agent/query` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/agents` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/agents/sync` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/agents/{agent_id}` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/audit-log` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/audit-log` | platform | admin | refused |  | audit integrity |
| `GET /api/workspaces/{workspace_id}/blast-radius` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/changes` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/changes/drift` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/clusters` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/costs` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/coverage` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/coverage/parse` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/dashboard` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/diagnostics/live` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/diagnostics/run` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/diagnostics/status` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/document-symbols` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/drift` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/eval-report` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/eval-scenarios` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/eval-scenarios` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/eval-scenarios/{scenario_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/eval-timeline` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/export` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/fingerprint` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/fixes` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/go-to-definition` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/goals` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/goals` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/goals/{goal_id}` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/goals/{goal_id}/context` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/goals/{goal_id}/iterations` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/goals/{goal_id}/ledger` | platform | reader | served |  |  |
| `PATCH /api/workspaces/{workspace_id}/goals/{goal_id}/status` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/guidance` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/guidance/customize` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/guidance/starters` | platform | proposer | refused |  | writes files into the repository |
| `GET /api/workspaces/{workspace_id}/hotspots` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/impact` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/incorporate` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/ingestion-plan` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/integrations` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/integrations` | platform | admin | refused |  | stores integration credentials |
| `POST /api/workspaces/{workspace_id}/integrations/github/import` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/integrations/github/pr` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/integrations/slack/notify` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issue-symbol-edges` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}` | platform | reader | served |  |  |
| `PATCH /api/workspaces/{workspace_id}/issues/{issue_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps/{dump_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/context` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/context-replays/{replay_id}/compare` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/coverage-delta` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/drift` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/duplicates` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/eval-scenarios/replay` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/fix-draft` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/fixes` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/handoff` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/ingest-ticket` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/ingested-ticket` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/owner-suggestions` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/ownership-history` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/quality` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/quality` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/review-packet` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/runs` | platform | proposer | refused |  | launches an agent run |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/threat-models/{threat_model_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context/{context_id}` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/triage` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/issues/{issue_id}/verification-profiles/{profile_id}/run` | platform | proposer | refused |  | runs a saved profile |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/work` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/lineage` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/lsp/document-symbols` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/lsp/workspace-symbols` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/metrics` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/owners` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/path-symbols` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/path-symbols/materialize` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/pg/issues/search` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/pg/materialize` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/pg/ops/materialize` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/pg/run-plans` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/pg/runs` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/pg/search` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/pg/verifications` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/pg/verifications/materialize` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/policy` | platform | reader | served |  |  |
| `PUT /api/workspaces/{workspace_id}/policy` | platform | human-approver | refused |  | policy change |
| `POST /api/workspaces/{workspace_id}/policy/evaluate` | platform | proposer | served |  | evaluation only |
| `GET /api/workspaces/{workspace_id}/project-info` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/quality/score-all` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/references` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/repo-config` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/repo-config/health` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/repo-context` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/repo-map` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/repo-state` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/retrieval-search` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/review-queue` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/run-targets` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runbooks` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runbooks` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/runbooks/{runbook_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/runs` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/accept` | platform | human-approver | refused |  | approval |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/brief` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/cancel` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/confidence` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/critique` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/critique` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/improvements` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/improvements/{suggestion_id}/dismiss` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/insights` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/log` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/metrics` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/plan` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/plan` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/approve` | platform | human-approver | refused |  | approval; never a merge authorization |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/plan/reject` | platform | human-approver | refused |  | approval |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/retry` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/runs/{run_id}/review` | platform | proposer | refused |  |  |
| `POST /api/workspaces/{workspace_id}/scan` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/security/acceptance-criteria` | platform | reader | served |  |  |
| `PUT /api/workspaces/{workspace_id}/security/acceptance-criteria` | platform | human-approver | refused |  | policy change |
| `GET /api/workspaces/{workspace_id}/security/acceptance-evaluation` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/security/dispositions` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition` | platform | reader | served |  |  |
| `PUT /api/workspaces/{workspace_id}/security/findings/{finding_id}/disposition` | platform | human-approver | refused |  | risk acceptance |
| `GET /api/workspaces/{workspace_id}/security/review-packet` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/semantic-index/materialize` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/semantic-search` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/semantic-search/materialize` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/signals` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/snapshot` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/sources` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/subsystems` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/symbol-graph` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/tree` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/triage/all` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/verification-outcomes` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/verification-profile-history` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/verification-profile-reports` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/verification-profiles` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/verification-profiles` | platform | admin | refused |  | defines commands a run executes |
| `DELETE /api/workspaces/{workspace_id}/verification-profiles/{profile_id}` | platform | admin | refused |  |  |
| `GET /api/workspaces/{workspace_id}/verifications` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/verifier-telemetry` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/verify-targets` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/views` | platform | reader | served |  |  |
| `POST /api/workspaces/{workspace_id}/views` | platform | proposer | refused |  |  |
| `DELETE /api/workspaces/{workspace_id}/views/{view_id}` | platform | proposer | refused |  |  |
| `PUT /api/workspaces/{workspace_id}/views/{view_id}` | platform | proposer | refused |  |  |
| `GET /api/workspaces/{workspace_id}/wiki` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/workspace-symbols` | platform | reader | served |  |  |
| `GET /api/workspaces/{workspace_id}/worktree` | platform | reader | served |  |  |
<!-- route-gates:end -->
