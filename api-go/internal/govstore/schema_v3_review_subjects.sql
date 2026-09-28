-- Migration 3 (WS-66; PAR-REV-06): review subjects on the shared tables. A review
-- record and a review finding are subjects, as a memory is: their history is events,
-- a finding's place in the code is an anchor, a principal's triage verdict on a finding
-- is an outcome, and a possible duplicate waits in the consolidation queue as a job.
-- subject_kind names the kind of subject entry_id refers to; every row written before
-- this migration, and every row the memory store writes, is 'memory'.
--
-- anchors and outcomes referred to entries through a foreign key, so they are rebuilt
-- without it. Triggers keep that check for memory rows and add the matching one for
-- review rows (a review subject exists once its creation event does). Review subject
-- ids and entry ids are disjoint, which the triggers on events and entries enforce, so
-- every memory query by entry_id reads memory rows only and stays unchanged.

ALTER TABLE events ADD COLUMN subject_kind TEXT NOT NULL DEFAULT 'memory' CHECK (subject_kind <> '');
ALTER TABLE jobs ADD COLUMN subject_kind TEXT NOT NULL DEFAULT 'memory' CHECK (subject_kind <> '');

-- The append-only rule now also keeps subject_kind; purge redaction is otherwise as in
-- migration 1.
DROP TRIGGER events_no_update;
CREATE TRIGGER events_no_update BEFORE UPDATE ON events
WHEN NOT (
      OLD.redacted = 0 AND NEW.redacted = 1
  AND NEW.note IS NULL AND NEW.data IS NULL
  AND NEW.seq IS OLD.seq AND NEW.workspace_id IS OLD.workspace_id AND NEW.entry_id IS OLD.entry_id
  AND NEW.type IS OLD.type AND NEW.principal IS OLD.principal AND NEW.session_id IS OLD.session_id
  AND NEW.agent_id IS OLD.agent_id AND NEW.head_sha IS OLD.head_sha AND NEW.revision IS OLD.revision
  AND NEW.old_digest IS OLD.old_digest AND NEW.new_digest IS OLD.new_digest AND NEW.at IS OLD.at
  AND NEW.subject_kind IS OLD.subject_kind
  AND EXISTS (SELECT 1 FROM entries e WHERE e.id = OLD.entry_id AND e.lifecycle = 'purged'))
BEGIN
  SELECT RAISE(ABORT, 'govstore: events are append-only');
END;

CREATE TRIGGER events_review_subject_disjoint BEFORE INSERT ON events
WHEN NEW.subject_kind <> 'memory' AND NEW.entry_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM entries WHERE id = NEW.entry_id)
BEGIN
  SELECT RAISE(ABORT, 'govstore: a review subject id may not name a memory');
END;

CREATE TRIGGER entries_review_subject_disjoint BEFORE INSERT ON entries
WHEN EXISTS (SELECT 1 FROM events WHERE entry_id = NEW.id AND subject_kind <> 'memory')
BEGIN
  SELECT RAISE(ABORT, 'govstore: an entry id may not name a review subject');
END;

-- Anchors gain a line range in the file value names (1-based and inclusive, 0 when the
-- anchor has none), the side of the change it counts in, how it was placed, and the
-- lineage of changes a review finding is compared within, indexed with the path so
-- dedupe and a lineage's findings are index lookups.
CREATE TABLE anchors_v3 (
  pk              INTEGER PRIMARY KEY,
  entry_id        TEXT NOT NULL,
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
  subject_kind    TEXT NOT NULL DEFAULT 'memory' CHECK (subject_kind <> ''),
  start_line      INTEGER NOT NULL DEFAULT 0 CHECK (start_line >= 0),
  end_line        INTEGER NOT NULL DEFAULT 0 CHECK (end_line >= start_line),
  side            TEXT NOT NULL DEFAULT '' CHECK (side IN ('', 'new', 'old')),
  anchor_status   TEXT NOT NULL DEFAULT '',
  lineage         TEXT NOT NULL DEFAULT '',
  CHECK (baseline_state <> 'hash' OR baseline_hash <> ''),
  CHECK ((start_line = 0) = (end_line = 0)),
  UNIQUE (entry_id, kind, value)
) STRICT;

INSERT INTO anchors_v3 (pk, entry_id, ordinal, kind, value, declared, symbol_uid, baseline_state, baseline_hash,
  baseline_kind, baseline_commit, baseline_at, stale_since, stale_commit)
SELECT pk, entry_id, ordinal, kind, value, declared, symbol_uid, baseline_state, baseline_hash,
  baseline_kind, baseline_commit, baseline_at, stale_since, stale_commit FROM anchors;
DROP TABLE anchors;
ALTER TABLE anchors_v3 RENAME TO anchors;
CREATE INDEX anchors_lookup ON anchors (kind, value);
CREATE INDEX anchors_review_lineage ON anchors (lineage, value) WHERE subject_kind = 'review_finding';

CREATE TRIGGER anchors_subject_exists BEFORE INSERT ON anchors
WHEN (NEW.subject_kind = 'memory' AND NOT EXISTS (SELECT 1 FROM entries WHERE id = NEW.entry_id))
  OR (NEW.subject_kind <> 'memory'
      AND NOT EXISTS (SELECT 1 FROM events WHERE entry_id = NEW.entry_id AND subject_kind = NEW.subject_kind))
BEGIN
  SELECT RAISE(ABORT, 'govstore: an anchor must name an existing subject');
END;

CREATE TRIGGER anchors_subject_fixed BEFORE UPDATE OF entry_id, subject_kind ON anchors
WHEN NEW.entry_id IS NOT OLD.entry_id OR NEW.subject_kind IS NOT OLD.subject_kind
BEGIN
  SELECT RAISE(ABORT, 'govstore: an anchor keeps its subject');
END;

-- Outcomes gain the triage verdicts on a review finding. Memory outcomes stay what they
-- were; each subject kind takes only its own verdicts.
CREATE TABLE outcomes_v3 (
  pk            INTEGER PRIMARY KEY,
  entry_id      TEXT NOT NULL,
  revision      INTEGER NOT NULL,
  principal     TEXT NOT NULL,
  principal_key TEXT NOT NULL CHECK (principal_key <> ''),
  outcome       TEXT NOT NULL
                  CHECK (outcome IN ('helpful', 'misleading', 'stale_harm', 'confirm', 'dismiss', 'fixed', 'wont_fix')),
  note          TEXT NOT NULL DEFAULT '',
  session_id    TEXT NOT NULL DEFAULT '',
  at            TEXT NOT NULL,
  subject_kind  TEXT NOT NULL DEFAULT 'memory' CHECK (subject_kind IN ('memory', 'review_finding')),
  CHECK ((subject_kind = 'memory') = (outcome IN ('helpful', 'misleading', 'stale_harm'))),
  UNIQUE (entry_id, principal_key)
) STRICT;

INSERT INTO outcomes_v3 (pk, entry_id, revision, principal, principal_key, outcome, note, session_id, at)
SELECT pk, entry_id, revision, principal, principal_key, outcome, note, session_id, at FROM outcomes;
DROP TABLE outcomes;
ALTER TABLE outcomes_v3 RENAME TO outcomes;

CREATE TRIGGER outcomes_subject_exists BEFORE INSERT ON outcomes
WHEN (NEW.subject_kind = 'memory' AND NOT EXISTS (SELECT 1 FROM entries WHERE id = NEW.entry_id))
  OR (NEW.subject_kind <> 'memory'
      AND NOT EXISTS (SELECT 1 FROM events WHERE entry_id = NEW.entry_id AND subject_kind = NEW.subject_kind))
BEGIN
  SELECT RAISE(ABORT, 'govstore: an outcome must name an existing subject');
END;

CREATE TRIGGER outcomes_subject_fixed BEFORE UPDATE OF entry_id, subject_kind ON outcomes
WHEN NEW.entry_id IS NOT OLD.entry_id OR NEW.subject_kind IS NOT OLD.subject_kind
BEGIN
  SELECT RAISE(ABORT, 'govstore: an outcome keeps its subject');
END;
