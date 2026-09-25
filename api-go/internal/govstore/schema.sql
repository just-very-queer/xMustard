-- govstore schema, migration 0001.
--
-- Conventions
--   * Every rowid table declares an INTEGER PRIMARY KEY, so rowids survive VACUUM.
--     The FTS5 tables key their rows on those ids.
--   * Times are TEXT in canonical UTC ('2006-01-02T15:04:05.000000000Z'): fixed width,
--     so text order is time order. NULL means unset.
--   * Principals are compared through *_key columns (trimmed and lower-cased by the Go
--     layer), never through the display name.
--   * Content lives only in revisions. Every other table refers to it by SHA-256 digest.
--   * Closed state machines are CHECK constraints. Vocabularies that later workstreams
--     extend (event types, anchor kinds, claim predicates, relation kinds, job kinds)
--     are validated in Go, so adding one needs code but no migration.

CREATE TABLE store_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- One row per memory. Mutable head state. The history lives in revisions and events.
CREATE TABLE entries (
  pk                     INTEGER PRIMARY KEY,
  id                     TEXT NOT NULL UNIQUE,
  workspace_id           TEXT NOT NULL,
  scope                  TEXT NOT NULL DEFAULT 'workspace'
                           CHECK (scope IN ('workspace', 'dir', 'private', 'session', 'run', 'global', 'shared')),
  scope_key              TEXT NOT NULL DEFAULT '',
  kind                   TEXT NOT NULL DEFAULT '',
  tier                   TEXT NOT NULL DEFAULT '' CHECK (tier IN ('', 'core', 'deferred')),
  topic                  TEXT NOT NULL DEFAULT '',
  title                  TEXT NOT NULL DEFAULT '',
  description            TEXT NOT NULL DEFAULT '',
  tags                   TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
  metadata               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
  permission             TEXT NOT NULL DEFAULT 'readonly' CHECK (permission IN ('readonly', 'readwrite')),
  -- Vote reconciliation result, as in the legacy JSON store.
  status                 TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'rejected')),
  promoted               INTEGER NOT NULL DEFAULT 0 CHECK (promoted IN (0, 1)),
  verification_mode      TEXT NOT NULL DEFAULT ''
                           CHECK (verification_mode IN ('', 'peer_verified', 'self_asserted_open_mode', 'single_agent')),
  -- What is served: promoted = 1 AND lifecycle = 'active' AND not expired.
  lifecycle              TEXT NOT NULL DEFAULT 'active'
                           CHECK (lifecycle IN ('active', 'superseded', 'retracted', 'merged', 'archived', 'purged')),
  required_verifications INTEGER NOT NULL DEFAULT 2 CHECK (required_verifications >= 1),
  require_verification   INTEGER NOT NULL DEFAULT 0 CHECK (require_verification IN (0, 1)),
  source                 TEXT NOT NULL,
  source_key             TEXT NOT NULL CHECK (source_key <> ''),
  source_owner           TEXT NOT NULL DEFAULT '',
  session_id             TEXT NOT NULL DEFAULT '',
  agent_id               TEXT NOT NULL DEFAULT '',
  -- revision is the accepted (served) revision; head_revision is the newest of any state.
  revision               INTEGER NOT NULL DEFAULT 0,
  head_revision          INTEGER NOT NULL DEFAULT 0,
  content_digest         TEXT NOT NULL DEFAULT '',
  search_tokens          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(search_tokens)),
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL,
  promoted_at            TEXT,
  -- Bi-temporal validity (PAR-PROV-02): world time plus system time.
  valid_from             TEXT,
  valid_from_commit      TEXT NOT NULL DEFAULT '',
  invalidated_at         TEXT,
  invalidated_commit     TEXT NOT NULL DEFAULT '',
  expired_at             TEXT,
  expires_at             TEXT,
  superseded_by          TEXT NOT NULL DEFAULT '',
  merged_into            TEXT NOT NULL DEFAULT '',
  needs_reverify         INTEGER NOT NULL DEFAULT 0 CHECK (needs_reverify IN (0, 1)),
  stale_since            TEXT,
  -- Provenance binding (PAR-PROV-04): repository state at propose and at promote.
  propose_head           TEXT NOT NULL DEFAULT '',
  propose_branch         TEXT NOT NULL DEFAULT '',
  propose_dirty          INTEGER,
  promote_head           TEXT NOT NULL DEFAULT '',
  promote_branch         TEXT NOT NULL DEFAULT '',
  promote_dirty          INTEGER,
  worktree               TEXT NOT NULL DEFAULT '',
  retention_class        TEXT NOT NULL DEFAULT 'memory',
  CHECK (promoted = 0 OR (status = 'verified' AND verification_mode <> '')),
  CHECK (revision <= head_revision)
) STRICT;

CREATE INDEX entries_served ON entries (workspace_id, lifecycle, promoted, pk);
CREATE INDEX entries_status ON entries (workspace_id, status, pk);
CREATE INDEX entries_scope ON entries (scope, scope_key);
CREATE INDEX entries_kind ON entries (workspace_id, kind);
CREATE INDEX entries_expiry ON entries (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX entries_reverify ON entries (workspace_id) WHERE needs_reverify = 1;

-- Every content version of every entry. Content is immutable. Retention or purge can
-- only drop it (content NULL, title and description ''); the digest always stays.
CREATE TABLE revisions (
  pk                     INTEGER PRIMARY KEY,
  entry_id               TEXT NOT NULL REFERENCES entries (id),
  revision               INTEGER NOT NULL CHECK (revision >= 1),
  base_revision          INTEGER NOT NULL DEFAULT 0,
  state                  TEXT NOT NULL CHECK (state IN ('accepted', 'pending', 'rejected', 'withdrawn')),
  op                     TEXT NOT NULL,
  title                  TEXT NOT NULL DEFAULT '',
  description            TEXT NOT NULL DEFAULT '',
  content                TEXT,
  content_digest         TEXT NOT NULL CHECK (length(content_digest) = 64),
  content_bytes          INTEGER NOT NULL,
  reason                 TEXT NOT NULL DEFAULT '',
  author                 TEXT NOT NULL,
  author_key             TEXT NOT NULL,
  session_id             TEXT NOT NULL DEFAULT '',
  head_sha               TEXT NOT NULL DEFAULT '',
  created_at             TEXT NOT NULL,
  decided_at             TEXT,
  content_dropped_at     TEXT,
  content_dropped_reason TEXT CHECK (content_dropped_reason IS NULL OR content_dropped_reason IN ('retention', 'purge')),
  UNIQUE (entry_id, revision)
) STRICT;

CREATE INDEX revisions_retention ON revisions (created_at) WHERE content IS NOT NULL;

CREATE TRIGGER revisions_immutable BEFORE UPDATE ON revisions
WHEN NEW.entry_id IS NOT OLD.entry_id
  OR NEW.revision IS NOT OLD.revision
  OR NEW.base_revision IS NOT OLD.base_revision
  OR NEW.op IS NOT OLD.op
  OR NEW.content_digest IS NOT OLD.content_digest
  OR NEW.content_bytes IS NOT OLD.content_bytes
  OR (NEW.reason IS NOT OLD.reason AND (NEW.reason <> '' OR NEW.content_dropped_reason IS NOT 'purge'))
  OR NEW.author IS NOT OLD.author
  OR NEW.author_key IS NOT OLD.author_key
  OR NEW.session_id IS NOT OLD.session_id
  OR NEW.head_sha IS NOT OLD.head_sha
  OR NEW.created_at IS NOT OLD.created_at
  OR (NEW.content IS NOT OLD.content AND NEW.content IS NOT NULL)
  OR (NEW.title IS NOT OLD.title AND NEW.title <> '')
  OR (NEW.description IS NOT OLD.description AND NEW.description <> '')
  OR ((NEW.content IS NOT OLD.content OR NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description)
      AND NEW.content_dropped_reason IS NULL)
  -- only a pending revision is decided; accepted, rejected and withdrawn are final
  OR (NEW.state IS NOT OLD.state AND OLD.state <> 'pending')
  -- a drop is never undone; retention may escalate to purge, never the reverse
  OR (OLD.content_dropped_reason = 'purge' AND NEW.content_dropped_reason IS NOT 'purge')
  OR (OLD.content_dropped_reason = 'retention' AND NEW.content_dropped_reason IS NULL)
BEGIN
  SELECT RAISE(ABORT, 'govstore: revision history is immutable');
END;

CREATE TRIGGER revisions_no_delete BEFORE DELETE ON revisions
BEGIN
  SELECT RAISE(ABORT, 'govstore: revision history is immutable');
END;

-- Latest verdict per principal on one revision. The full vote history is in events.
CREATE TABLE votes (
  pk              INTEGER PRIMARY KEY,
  entry_id        TEXT NOT NULL REFERENCES entries (id),
  revision        INTEGER NOT NULL,
  principal       TEXT NOT NULL,
  principal_key   TEXT NOT NULL CHECK (principal_key <> ''),
  principal_owner TEXT NOT NULL DEFAULT '',
  principal_kind  TEXT NOT NULL DEFAULT '',
  verdict         TEXT NOT NULL CHECK (verdict IN ('approve', 'reject', 'duplicate_of', 'retract')),
  target          TEXT NOT NULL DEFAULT '',
  note            TEXT NOT NULL DEFAULT '',
  evidence_handle TEXT NOT NULL DEFAULT '',
  content_digest  TEXT NOT NULL,
  session_id      TEXT NOT NULL DEFAULT '',
  ordinal         INTEGER NOT NULL,
  at              TEXT NOT NULL,
  UNIQUE (entry_id, revision, principal_key)
) STRICT;

-- Defense in depth for the core governance invariant: an entry can only be labelled
-- peer_verified when enough distinct principals other than its author and the open-mode
-- identity approved the revision it serves. The Go layer re-checks entries whose votes
-- changed before every commit.
CREATE TRIGGER entries_peer_verified_insert BEFORE INSERT ON entries
WHEN NEW.verification_mode = 'peer_verified'
BEGIN
  SELECT RAISE(ABORT, 'govstore: peer_verified requires distinct peer approvals');
END;

CREATE TRIGGER entries_peer_verified_update
BEFORE UPDATE OF verification_mode, revision, promoted, source_key, required_verifications ON entries
WHEN NEW.verification_mode = 'peer_verified'
  AND (SELECT count(*) FROM votes v
        WHERE v.entry_id = NEW.id AND v.revision = NEW.revision AND v.verdict = 'approve'
          AND v.principal_key <> NEW.source_key AND v.principal_key <> 'anonymous') < NEW.required_verifications
BEGIN
  SELECT RAISE(ABORT, 'govstore: peer_verified requires distinct peer approvals');
END;

-- Append-only memory history (PAR-PROV-01). Updates and deletes are rejected. The one
-- sanctioned change is purge redaction: the free text (note, data) of events that
-- belong to a purged entry may be cleared once, leaving every other column intact.
CREATE TABLE events (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id TEXT NOT NULL,
  entry_id     TEXT,
  type         TEXT NOT NULL CHECK (type <> ''),
  principal    TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  agent_id     TEXT NOT NULL DEFAULT '',
  head_sha     TEXT NOT NULL DEFAULT '',
  revision     INTEGER,
  old_digest   TEXT NOT NULL DEFAULT '',
  new_digest   TEXT NOT NULL DEFAULT '',
  note         TEXT,
  data         TEXT CHECK (data IS NULL OR json_valid(data)),
  redacted     INTEGER NOT NULL DEFAULT 0 CHECK (redacted IN (0, 1)),
  at           TEXT NOT NULL
) STRICT;

CREATE INDEX events_entry ON events (entry_id, seq) WHERE entry_id IS NOT NULL;
CREATE INDEX events_workspace ON events (workspace_id, seq);

CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN
  SELECT RAISE(ABORT, 'govstore: events are append-only');
END;

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
WHEN NOT (
      OLD.redacted = 0 AND NEW.redacted = 1
  AND NEW.note IS NULL AND NEW.data IS NULL
  AND NEW.seq IS OLD.seq AND NEW.workspace_id IS OLD.workspace_id AND NEW.entry_id IS OLD.entry_id
  AND NEW.type IS OLD.type AND NEW.principal IS OLD.principal AND NEW.session_id IS OLD.session_id
  AND NEW.agent_id IS OLD.agent_id AND NEW.head_sha IS OLD.head_sha AND NEW.revision IS OLD.revision
  AND NEW.old_digest IS OLD.old_digest AND NEW.new_digest IS OLD.new_digest AND NEW.at IS OLD.at
  AND EXISTS (SELECT 1 FROM entries e WHERE e.id = OLD.entry_id AND e.lifecycle = 'purged'))
BEGIN
  SELECT RAISE(ABORT, 'govstore: events are append-only');
END;

-- Code anchors: paths, symbols, identifiers, commands, config keys. They form the
-- inverted index anchor -> memories and carry the drift baseline captured at promotion.
CREATE TABLE anchors (
  pk              INTEGER PRIMARY KEY,
  entry_id        TEXT NOT NULL REFERENCES entries (id),
  ordinal         INTEGER NOT NULL,
  kind            TEXT NOT NULL,
  value           TEXT NOT NULL,
  declared        INTEGER NOT NULL DEFAULT 1 CHECK (declared IN (0, 1)),
  symbol_uid      TEXT NOT NULL DEFAULT '',
  baseline_state  TEXT NOT NULL DEFAULT 'none' CHECK (baseline_state IN ('none', 'hash', 'missing')),
  baseline_hash   TEXT NOT NULL DEFAULT '',
  baseline_kind   TEXT NOT NULL DEFAULT '',
  baseline_commit TEXT NOT NULL DEFAULT '',
  baseline_at     TEXT,
  stale_since     TEXT,
  stale_commit    TEXT NOT NULL DEFAULT '',
  CHECK (baseline_state <> 'hash' OR baseline_hash <> ''),
  UNIQUE (entry_id, kind, value)
) STRICT;

CREATE INDEX anchors_lookup ON anchors (kind, value);

-- Structured claims {subject, predicate, object} (PAR-GOV-10).
CREATE TABLE claims (
  pk           INTEGER PRIMARY KEY,
  entry_id     TEXT NOT NULL REFERENCES entries (id),
  revision     INTEGER NOT NULL,
  subject      TEXT NOT NULL,
  subject_kind TEXT NOT NULL DEFAULT '',
  predicate    TEXT NOT NULL,
  object       TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  UNIQUE (entry_id, subject, predicate, object)
) STRICT;

CREATE INDEX claims_subject ON claims (subject, predicate);

-- Typed memory-to-memory edges: supersedes, duplicates, refines, contradicts, ...
CREATE TABLE relations (
  pk         INTEGER PRIMARY KEY,
  from_id    TEXT NOT NULL REFERENCES entries (id),
  to_id      TEXT NOT NULL REFERENCES entries (id),
  kind       TEXT NOT NULL,
  principal  TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  CHECK (from_id <> to_id),
  UNIQUE (from_id, to_id, kind)
) STRICT;

CREATE INDEX relations_to ON relations (to_id, kind);

-- Session ledger (PAR-PROV-06/07) and transcripts (PAR-RCL-07).
CREATE TABLE sessions (
  pk              INTEGER PRIMARY KEY,
  id              TEXT NOT NULL UNIQUE,
  workspace_id    TEXT NOT NULL,
  client          TEXT NOT NULL DEFAULT '',
  principal       TEXT NOT NULL DEFAULT '',
  principal_key   TEXT NOT NULL DEFAULT '',
  agent_id        TEXT NOT NULL DEFAULT '',
  execution_key   TEXT NOT NULL DEFAULT '',
  parent_id       TEXT NOT NULL DEFAULT '',
  thread_id       TEXT NOT NULL DEFAULT '',
  branch          TEXT NOT NULL DEFAULT '',
  worktree        TEXT NOT NULL DEFAULT '',
  head_sha        TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended', 'interrupted')),
  summary         TEXT NOT NULL DEFAULT '',
  started_at      TEXT NOT NULL,
  last_seen_at    TEXT NOT NULL,
  ended_at        TEXT,
  retention_class TEXT NOT NULL DEFAULT 'session'
) STRICT;

CREATE INDEX sessions_workspace ON sessions (workspace_id, last_seen_at);
CREATE INDEX sessions_thread ON sessions (thread_id) WHERE thread_id <> '';
CREATE INDEX sessions_retention ON sessions (retention_class, last_seen_at);

-- Immutable ledger rows. Retention may delete them. Nothing may rewrite them.
CREATE TABLE session_events (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      TEXT NOT NULL REFERENCES sessions (id),
  workspace_id    TEXT NOT NULL,
  kind            TEXT NOT NULL,
  record_class    TEXT NOT NULL DEFAULT 'raw' CHECK (record_class IN ('raw', 'derived', 'verified')),
  tool            TEXT NOT NULL DEFAULT '',
  args_digest     TEXT NOT NULL DEFAULT '',
  handle          TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT '',
  path            TEXT NOT NULL DEFAULT '',
  entry_id        TEXT NOT NULL DEFAULT '',
  role            TEXT NOT NULL DEFAULT '',
  body            TEXT,
  body_digest     TEXT NOT NULL DEFAULT '',
  data            TEXT CHECK (data IS NULL OR json_valid(data)),
  at              TEXT NOT NULL,
  retention_class TEXT NOT NULL DEFAULT 'session'
) STRICT;

CREATE INDEX session_events_session ON session_events (session_id, seq);
CREATE INDEX session_events_retention ON session_events (retention_class, at);

CREATE TRIGGER session_events_immutable BEFORE UPDATE ON session_events
BEGIN
  SELECT RAISE(ABORT, 'govstore: session events are immutable');
END;

-- Per-memory outcome feedback (PAR-GOV-16): the latest outcome per principal.
CREATE TABLE outcomes (
  pk            INTEGER PRIMARY KEY,
  entry_id      TEXT NOT NULL REFERENCES entries (id),
  revision      INTEGER NOT NULL,
  principal     TEXT NOT NULL,
  principal_key TEXT NOT NULL CHECK (principal_key <> ''),
  outcome       TEXT NOT NULL CHECK (outcome IN ('helpful', 'misleading', 'stale_harm')),
  note          TEXT NOT NULL DEFAULT '',
  session_id    TEXT NOT NULL DEFAULT '',
  at            TEXT NOT NULL,
  UNIQUE (entry_id, principal_key)
) STRICT;

-- Durable delivery log: which revision was shown to which session and principal, and
-- through which surface. Stale-harm attribution needs it (critic §12.2).
CREATE TABLE deliveries (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id        TEXT NOT NULL REFERENCES entries (id),
  revision        INTEGER NOT NULL,
  content_digest  TEXT NOT NULL,
  workspace_id    TEXT NOT NULL,
  session_id      TEXT NOT NULL DEFAULT '',
  principal       TEXT NOT NULL DEFAULT '',
  surface         TEXT NOT NULL,
  at              TEXT NOT NULL,
  retention_class TEXT NOT NULL DEFAULT 'delivery'
) STRICT;

CREATE INDEX deliveries_session ON deliveries (session_id, seq);
CREATE INDEX deliveries_entry ON deliveries (entry_id, seq);
CREATE INDEX deliveries_retention ON deliveries (retention_class, at);

-- Shared collections, grants and per-target applicability (PAR-SHARE-02).
CREATE TABLE collections (
  pk              INTEGER PRIMARY KEY,
  id              TEXT NOT NULL UNIQUE,
  name            TEXT NOT NULL,
  owner_workspace TEXT NOT NULL,
  policy          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(policy)),
  created_by      TEXT NOT NULL,
  created_at      TEXT NOT NULL
) STRICT;

CREATE TABLE grants (
  pk               INTEGER PRIMARY KEY,
  id               TEXT NOT NULL UNIQUE,
  source_workspace TEXT NOT NULL,
  target_workspace TEXT NOT NULL DEFAULT '',
  collection_id    TEXT REFERENCES collections (id),
  scope            TEXT NOT NULL DEFAULT 'workspace',
  permission       TEXT NOT NULL CHECK (permission IN ('ro', 'rw')),
  granted_by       TEXT NOT NULL,
  granted_at       TEXT NOT NULL,
  revoked_by       TEXT NOT NULL DEFAULT '',
  revoked_at       TEXT,
  CHECK (target_workspace <> '' OR collection_id IS NOT NULL)
) STRICT;

CREATE UNIQUE INDEX grants_active
  ON grants (source_workspace, target_workspace, coalesce(collection_id, ''), scope)
  WHERE revoked_at IS NULL;
CREATE INDEX grants_target ON grants (target_workspace);

CREATE TABLE applicability (
  entry_id         TEXT NOT NULL REFERENCES entries (id),
  target_workspace TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('verified_here', 'foreign_unchecked', 'stale')),
  revision         INTEGER NOT NULL,
  checked_by       TEXT NOT NULL DEFAULT '',
  checked_at       TEXT NOT NULL,
  PRIMARY KEY (entry_id, target_workspace)
) STRICT, WITHOUT ROWID;

-- Consolidation work queue (PAR-GOV-13): compare-and-set leases and per-source
-- watermarks that advance only on success.
CREATE TABLE jobs (
  pk                 INTEGER PRIMARY KEY,
  id                 TEXT NOT NULL UNIQUE,
  workspace_id       TEXT NOT NULL,
  kind               TEXT NOT NULL,
  state              TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'leased', 'done', 'failed', 'cancelled')),
  conflict_signature TEXT NOT NULL CHECK (conflict_signature <> ''),
  candidate_ids      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(candidate_ids)),
  evidence_refs      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_refs)),
  instructions       TEXT NOT NULL DEFAULT '',
  payload            TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
  lease_owner        TEXT NOT NULL DEFAULT '',
  lease_expires_at   TEXT,
  lease_version      INTEGER NOT NULL DEFAULT 0,
  attempts           INTEGER NOT NULL DEFAULT 0,
  max_attempts       INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),
  result             TEXT CHECK (result IS NULL OR json_valid(result)),
  error              TEXT NOT NULL DEFAULT '',
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  finished_at        TEXT
) STRICT;

CREATE UNIQUE INDEX jobs_signature ON jobs (workspace_id, conflict_signature) WHERE state <> 'cancelled';
CREATE INDEX jobs_claim ON jobs (workspace_id, state, pk);
CREATE INDEX jobs_retention ON jobs (finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE watermarks (
  workspace_id      TEXT NOT NULL,
  source            TEXT NOT NULL,
  reflected_through TEXT NOT NULL DEFAULT '',
  total_count       INTEGER NOT NULL DEFAULT 0,
  reflected_count   INTEGER NOT NULL DEFAULT 0,
  version           INTEGER NOT NULL DEFAULT 0,
  last_started_at   TEXT,
  last_succeeded_at TEXT,
  PRIMARY KEY (workspace_id, source)
) STRICT, WITHOUT ROWID;

-- Path feedback signal (replaces agent_feedback.json): O(change) counter upserts.
CREATE TABLE path_feedback (
  workspace_id    TEXT NOT NULL,
  path            TEXT NOT NULL,
  retrieval_count INTEGER NOT NULL DEFAULT 0,
  verify_count    INTEGER NOT NULL DEFAULT 0,
  run_success     INTEGER NOT NULL DEFAULT 0,
  run_fail        INTEGER NOT NULL DEFAULT 0,
  last_used       TEXT NOT NULL,
  PRIMARY KEY (workspace_id, path)
) STRICT, WITHOUT ROWID;

-- Evidence handle metadata. The originals stay files owned by the evidence store.
CREATE TABLE evidence_meta (
  pk                         INTEGER PRIMARY KEY,
  handle                     TEXT NOT NULL UNIQUE,
  workspace_id               TEXT NOT NULL,
  repo_scope                 TEXT NOT NULL DEFAULT '',
  actor                      TEXT NOT NULL DEFAULT '',
  auth_enforced              INTEGER NOT NULL DEFAULT 0 CHECK (auth_enforced IN (0, 1)),
  issuer                     TEXT NOT NULL DEFAULT '',
  session_id                 TEXT NOT NULL DEFAULT '',
  call_id                    TEXT NOT NULL DEFAULT '',
  tool                       TEXT NOT NULL,
  tool_version               TEXT NOT NULL DEFAULT '',
  args_digest                TEXT NOT NULL DEFAULT '',
  captured_at                TEXT NOT NULL,
  captured_key               TEXT NOT NULL DEFAULT '',
  captured_identity_complete INTEGER NOT NULL DEFAULT 0 CHECK (captured_identity_complete IN (0, 1)),
  status                     INTEGER NOT NULL DEFAULT 0,
  is_error                   INTEGER NOT NULL DEFAULT 0 CHECK (is_error IN (0, 1)),
  content_type               TEXT NOT NULL DEFAULT '',
  raw_sha256                 TEXT NOT NULL,
  raw_bytes                  INTEGER NOT NULL CHECK (raw_bytes >= 0),
  expires_at                 TEXT NOT NULL,
  revoked                    INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1)),
  retention_class            TEXT NOT NULL DEFAULT 'evidence',
  projection                 TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(projection))
) STRICT;

CREATE INDEX evidence_meta_workspace ON evidence_meta (workspace_id, pk);
CREATE INDEX evidence_meta_session ON evidence_meta (session_id) WHERE session_id <> '';
CREATE INDEX evidence_meta_expiry ON evidence_meta (expires_at);

-- Full-text search. Contentless: the text lives in entries, revisions, anchors and
-- session_events, so nothing is stored twice. rowid = entries.pk / session_events.seq.
CREATE VIRTUAL TABLE memory_fts USING fts5 (
  title, body, anchors,
  content = '', contentless_delete = 1,
  tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE transcript_fts USING fts5 (
  body,
  content = '', contentless_delete = 1,
  tokenize = 'porter unicode61 remove_diacritics 2'
);

-- Cap each FTS5 table's in-memory pending-terms buffer at 256 KiB (default 1 MiB), so a
-- bulk transaction flushes index segments sooner instead of growing the C heap.
INSERT INTO memory_fts (memory_fts, rank) VALUES ('hashsize', 262144);
INSERT INTO transcript_fts (transcript_fts, rank) VALUES ('hashsize', 262144);

CREATE TRIGGER session_events_fts_insert AFTER INSERT ON session_events
WHEN NEW.body IS NOT NULL AND NEW.body <> ''
BEGIN
  INSERT INTO transcript_fts (rowid, body) VALUES (NEW.seq, NEW.body);
END;

CREATE TRIGGER session_events_fts_delete AFTER DELETE ON session_events
WHEN OLD.body IS NOT NULL AND OLD.body <> ''
BEGIN
  DELETE FROM transcript_fts WHERE rowid = OLD.seq;
END;
