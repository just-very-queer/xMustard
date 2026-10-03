-- govstore schema, migration 0005: collection membership (WS-29a; PAR-SHARE-02).
--
-- Migrations 1..4 created `collections`, `grants` and `applicability`, and `grants`
-- carries a `collection_id` foreign key, so a collection could be named as a grant
-- target while nothing ever put a memory in it. This step adds the missing relation:
-- which entries a collection holds.
--
-- Membership is a relation, not a column on `entries`. A memory belongs to its own
-- workspace, and a collection is a named set inside that workspace that a grant can
-- name instead of a workspace; an entry with a `collection_id` column would have to
-- carry the collection's owner with it, and one entry can be in several collections
-- while each collection keeps its own order. `grants_active` already keys on
-- `coalesce(collection_id, '')` and `applicability` already keys on
-- `(entry_id, target_workspace)`, so neither needs to change.
CREATE TABLE collection_members (
  collection_id TEXT NOT NULL REFERENCES collections (id),
  entry_id      TEXT NOT NULL REFERENCES entries (id),
  added_by      TEXT NOT NULL CHECK (added_by <> ''),
  added_at      TEXT NOT NULL,
  seq           INTEGER NOT NULL,
  PRIMARY KEY (collection_id, entry_id)
) STRICT, WITHOUT ROWID;

-- Listing a collection's members reads one collection's rows in the order they were
-- added, and `seq` is the membership's own order rather than the entry's `pk`, so a
-- re-add moves an entry to the end instead of leaving it where it first appeared. It is
-- allocated per collection as max(seq)+1 inside the writing IMMEDIATE transaction, so
-- two writers never collide on it.
CREATE INDEX collection_members_order ON collection_members (collection_id, seq);

-- The entry side is read when a memory is shown or purged: which collections hold it.
CREATE INDEX collection_members_entry ON collection_members (entry_id);