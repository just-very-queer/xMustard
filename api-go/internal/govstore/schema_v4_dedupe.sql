-- govstore schema, migration 0004: duplicate detection and write quotas (WS-27).
--
-- Dedupe postings (PAR-GOV-03). The served content of every entry hashes to one exact
-- key and, when it passes the entropy gate, up to eight MinHash LSH band keys (see
-- internal/dedupe). Every key folds in the workspace, so a lookup reads one workspace's
-- postings. They are replaced whenever the served revision changes and deleted on purge.
CREATE TABLE dedupe_keys (
  key      INTEGER NOT NULL,
  entry_pk INTEGER NOT NULL REFERENCES entries (pk),
  PRIMARY KEY (key, entry_pk)
) STRICT, WITHOUT ROWID;

CREATE INDEX dedupe_keys_entry ON dedupe_keys (entry_pk);

-- Entries that existed before this migration get their postings in bounded batches
-- (BackfillDedupeKeys): every entry with next < pk <= end.
INSERT INTO store_meta (key, value) VALUES ('dedupe_backfill_next', '0');
INSERT INTO store_meta (key, value) SELECT 'dedupe_backfill_end', CAST(coalesce(max(pk), 0) AS TEXT) FROM entries;

-- Corroboration: a principal restated a served memory instead of adding a duplicate.
-- One row per principal and entry; evidence is the union of the handles its
-- restatements cited, at most eight.
CREATE TABLE corroborations (
  entry_id      TEXT NOT NULL REFERENCES entries (id),
  principal_key TEXT NOT NULL CHECK (principal_key <> ''),
  principal     TEXT NOT NULL,
  count         INTEGER NOT NULL DEFAULT 1 CHECK (count >= 1),
  evidence      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence)),
  first_at      TEXT NOT NULL,
  last_at       TEXT NOT NULL,
  PRIMARY KEY (entry_id, principal_key)
) STRICT, WITHOUT ROWID;

-- The per-principal pending quota counts one principal's open proposals.
CREATE INDEX entries_pending_source ON entries (workspace_id, source_key)
  WHERE status = 'pending' AND lifecycle = 'active';
