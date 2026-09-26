# Security kernel

This page describes who may call the xMustard HTTP API, which routes a deployment
serves, and how the API limits its exposure. The code is authoritative:
`api-go/cmd/xmustard-api/route_gates.go` (route table),
`api-go/cmd/xmustard-api/security_middleware.go` (posture and middlewares) and
`api-go/internal/workspaceops/auth.go`, `auth_roles.go` (tokens and roles). The
route table at the end of this page is generated from the code by a test.

## Profiles

The API serves one of two route sets.

| Profile | Selected by | Serves |
|---|---|---|
| `core` (default) | nothing, `XMUSTARD_PROFILE=core`, or the legacy `XMUSTARD_CORE_ONLY=1` | the nine MCP tools, governed memory (propose, verify, edit, full list), evidence capture and recovery, token administration, workspace list and registration, index rebaseline, health |
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
| `reader` | read routes and the read tools (`ground`, `recall`, `search`, `explain`, `impact`, `diagnostics`, `why_failed`) |
| `proposer` | reader, plus `remember`, editing memory it authored, platform writes, and registering a git work tree under `XMUSTARD_REGISTER_ROOTS` ([Workspace registration](#workspace-registration)) |
| `verifier` | reader, plus `verify` |
| `human-approver` | reader, plus policy changes and approvals (workspace policy, security acceptance criteria and dispositions, run-plan approve/reject, run accept) |
| `indexer` | reader, plus `POST /api/workspaces/{id}/index` (rebaseline) |
| `admin` | every role, plus token administration, settings, providers, Postgres bootstrap, integration credentials, verification-profile definitions, terminals and registering any directory as a workspace |

The legacy names stay valid: `agent` is `proposer+verifier`, and `readonly` is
`reader`. A blank role mints `agent`. An unknown role is refused at mint time, and a
stored or environment role that cannot be parsed resolves to `reader`. A principal
that holds only `reader` may use only GET routes: every non-GET route needs a higher
role, and a test enforces that. A reader's write is refused by the route gate, so
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
the MCP tools this caller can use here. `xmustard-mcp` filters `tools/list` by that
list. If the API cannot answer within 2 seconds, it advertises all nine tools, and
the API still enforces every gate. `ground` adds a `principal` block with the
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
| Read-only mode | `XMUSTARD_READ_ONLY=1` | Mutating routes answer `403 read_only`, and `remember`/`verify` disappear from `tools/list`. Three kinds of non-GET route stay available: capture and revocation of the caller's own tool output, policy evaluation, and token revoke and rotate, so an admin can cut off a leaked token without leaving read-only mode. Minting and the workspace-wide evidence purge are refused. |
| Tool disable | `XMUSTARD_DISABLED_TOOLS=impact,why_failed` | The tool's route answers `403 tool_disabled` and the tool leaves `tools/list`. Unknown names stop startup. |
| Workspace allowlist | `XMUSTARD_WORKSPACE_ALLOWLIST=ws-a,ws-b` | Other workspace ids answer `403 workspace_not_allowed`. The same rule filters `GET /api/workspaces` and checks `POST /api/workspaces/load` (by the id the root would get) and the terminal routes. |
| Remote execution | profile | Terminals exist only in the platform profile and need `admin`. |
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
xMustard's store. `xmustard-mcp` calls it on an agent's first tool call in an
unregistered git repository (auto-registration, `XMUSTARD_MCP_AUTO_REGISTER=0`
turns it off), with the agent's own token. The code is
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
suppressed). The MCP shim puts the message in its resolution error, so the agent
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
| `ANY /api/health` | core | reader | served |  | public liveness and budget counters |
| `GET /api/workspaces` | core | reader | served |  | filtered by token scope and workspace allowlist |
| `POST /api/workspaces/load` | core | proposer | refused |  | workspace registration; below admin only a git work tree top level under XMUSTARD_REGISTER_ROOTS; id checked against the allowlist and token scope |
| `GET /api/workspaces/{workspace_id}/changes/since-index` | core | reader | served | impact |  |
| `GET /api/workspaces/{workspace_id}/context` | core | admin | served |  | full memory history |
| `POST /api/workspaces/{workspace_id}/context` | core | proposer | refused | remember | author is the principal |
| `GET /api/workspaces/{workspace_id}/context/active` | core | reader | served | recall | scope=all needs admin |
| `PUT /api/workspaces/{workspace_id}/context/{entry_id}` | core | proposer | refused |  | memory edit; author or admin only |
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
| `GET /api/workspaces/{workspace_id}/runs/{run_id}/why-failed` | core | reader | served | why_failed |  |
| `GET /api/workspaces/{workspace_id}/search` | core | reader | served | search |  |
| `GET /api/workspaces/{workspace_id}/session-grounding` | core | reader | served | ground |  |
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
