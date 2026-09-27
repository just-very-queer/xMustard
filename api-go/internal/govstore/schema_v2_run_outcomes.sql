-- Migration 2 (WS-21; PAR-HAR-06, PAR-RET-11): run-independent outcomes. A test,
-- build or lint command why_failed ran, a pasted log, a retained evidence original,
-- or a captured failing test/build output, with the failure analysis made from the
-- bounded tail of its output when it was recorded. ground lists the open failures, so
-- a pure-MCP user sees failures without platform runs.

CREATE TABLE run_outcomes (
  pk              INTEGER PRIMARY KEY,
  id              TEXT NOT NULL UNIQUE,
  workspace_id    TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('command', 'evidence', 'log', 'capture')),
  -- The idempotency key: a second report of the same log, evidence original or
  -- captured call returns the first outcome instead of recording (and feeding back) twice.
  source_key      TEXT NOT NULL CHECK (source_key <> ''),
  -- What the outcome verified (the command it ran, as normalized argv), so a later
  -- outcome of the same subject resolves an earlier open failure. '' has no subject.
  subject_key     TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'timed_out')),
  failed          INTEGER NOT NULL CHECK (failed IN (0, 1)),
  exit_code       INTEGER,
  command         TEXT NOT NULL DEFAULT '',
  cwd             TEXT NOT NULL DEFAULT '',
  tool            TEXT NOT NULL DEFAULT '',
  evidence_handle TEXT NOT NULL DEFAULT '',
  output_bytes    INTEGER NOT NULL DEFAULT 0 CHECK (output_bytes >= 0),
  analyzed_bytes  INTEGER NOT NULL DEFAULT 0 CHECK (analyzed_bytes >= 0),
  output_sha256   TEXT NOT NULL DEFAULT '',
  analysis        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(analysis)),
  tail            TEXT NOT NULL DEFAULT '',
  principal       TEXT NOT NULL,
  session_id      TEXT NOT NULL DEFAULT '',
  head_sha        TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  resolved_by     TEXT NOT NULL DEFAULT '',
  resolved_at     TEXT,
  retention_class TEXT NOT NULL DEFAULT 'outcome',
  UNIQUE (workspace_id, source_key),
  CHECK ((status = 'passed') = (failed = 0))
) STRICT;

CREATE INDEX run_outcomes_recent ON run_outcomes (workspace_id, created_at);
CREATE INDEX run_outcomes_open ON run_outcomes (workspace_id, subject_key)
  WHERE failed = 1 AND resolved_at IS NULL;
CREATE INDEX run_outcomes_retention ON run_outcomes (retention_class, created_at);
