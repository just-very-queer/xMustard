//! Paths-only updates (WS-RT6): decide a watcher batch from the named paths and their
//! stored rows, without listing the tree.
//!
//! A tree listing (`scan`) applies the envelope in path order over every eligible file,
//! so a change to one path can move the boundary over others. This module proves, for
//! one batch, that it does not. It reads the stored rows' envelope facts (kept files and
//! bytes, loss rows, the last row's path), the stored beyond counts and the listing
//! state the last listing recorded (`meta::ListingState`), classifies each named path's
//! state at that listing and now, and adds up the effect on the envelope. When every
//! effect stays local, the result is a `Scan` covering only the batch (`Scan::only`)
//! with exact beyond, sample and deleted counts, and the update equals a full build.
//! Otherwise it names why the tree has to be listed.
//!
//! The tracked set lives in the Git index, which this module never reads: a batch is
//! decided alone only while HEAD and the Git index file's stat key are what the last
//! listing saw. Then a named path with no row and no exception is untracked, ignored,
//! undecided by the ignore budget, not eligible, or a readable file past the cut; only
//! the last can move a count, by being deleted or becoming a loss, and that case lists
//! the tree.

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::path::{Component, Path};

use rusqlite::{Connection, OptionalExtension, params};
use serde::Serialize;
use serde::de::DeserializeOwned;

use super::config::IndexConfig;
use super::extract::Lang;
use super::ignore::XMUSTARD_IGNORE;
use super::meta::{self, ListingState, MAX_EXCEPTIONS, MAX_LOSS_ENTRIES, NestedRepos};
use super::scan::{
    self, Candidate, DELETED, DOC_DELETED, DocCandidate, Layout, MAX_DOC_FILES, MAX_LOSS_ROWS,
    Mode, Scan, StatKey,
};
use super::{Existing, normalize_paths};

/// Why an update listed the tree (`proven`: a paths update decided from its batch).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ListingReason {
    Proven,
    /// An update without `--paths`.
    NoPaths,
    /// A full rebuild: `build`, or an update that could not run in place.
    Rebuild,
    NonGit,
    /// The index was written before listing state existed.
    NoListingState,
    HeadMoved,
    GitIndexChanged,
    /// Untracked files are indexed, and the untracked set is not in the Git index.
    IncludeUntracked,
    /// The batch names an `.xmustardignore`.
    IgnoreFile,
    /// The batch changes which files the envelope keeps.
    EnvelopeShift,
    /// The byte budget binds, or would, and the batch changes kept bytes.
    ByteBudget,
    /// The batch changes loss rows while they are at their bound.
    LossRows,
    /// The batch changes the docs lane's set while it is full.
    DocsShift,
    /// A path leaves a truncated beyond sample, so the next path is not known.
    SampleRefill,
    /// A named file past the cut was deleted or became a loss, and nothing stored says
    /// whether it was tracked.
    UnknownPrior,
    /// The listing state does not hold every path whose state depends on the worktree.
    ExceptionsIncomplete,
    /// Re-planning the symbol budget rewrites a file outside the batch.
    SymbolBudget,
    /// The batch names a directory that is gone, or that is new and may hold restored
    /// files, so files it does not name may have changed.
    Directory,
}

/// How an update found the files it compared (`IndexReport::listing`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub struct Listing {
    /// `paths` (the batch only) | `tree` (a full listing)
    pub scope: &'static str,
    pub reason: ListingReason,
}

impl Listing {
    pub fn tree(reason: ListingReason) -> Listing {
        Listing {
            scope: "tree",
            reason,
        }
    }

    pub fn paths() -> Listing {
        Listing {
            scope: "paths",
            reason: ListingReason::Proven,
        }
    }
}

/// A batch decided alone: a scan of its paths, and the stored rows of those paths.
pub(super) struct Scoped {
    pub(super) scan: Scan,
    pub(super) rows: HashMap<String, Existing>,
}

/// SQL: the stored row is a pre-read loss, not a kept file. Read failures share names
/// with pre-read losses; a pre-read `symlink`, `not_regular` or `unreadable` row has the
/// empty stat key (?2), and a pre-read `oversized` file is above `max_file_size` (?1).
const LOSS_ROW: &str = "(parse_status = 'envelope_bytes'
    OR (parse_status = 'oversized' AND size > ?1)
    OR (parse_status IN ('symlink', 'not_regular', 'unreadable') AND stat_key = ?2))";

/// The stored envelope, from aggregates over the rows.
struct Facts {
    kept: usize,
    kept_bytes: u64,
    loss_rows: usize,
    /// Rows the byte budget left out (`envelope_bytes`).
    bytes_rows: usize,
    /// The largest stored path: under a cut, every eligible path above it is past it.
    last_path: String,
    doc_rows: usize,
    last_doc: String,
}

fn facts(conn: &Connection, cfg: &IndexConfig) -> rusqlite::Result<Facts> {
    let empty = StatKey::default().encode();
    let (kept, kept_bytes, loss_rows, bytes_rows, last_path) = conn.query_row(
        &format!(
            "SELECT coalesce(sum(CASE WHEN {LOSS_ROW} THEN 0 ELSE 1 END), 0),
                    coalesce(sum(CASE WHEN {LOSS_ROW} THEN 0 ELSE size END), 0),
                    coalesce(sum(CASE WHEN {LOSS_ROW} THEN 1 ELSE 0 END), 0),
                    coalesce(sum(parse_status = 'envelope_bytes'), 0),
                    coalesce(max(path), '')
             FROM files"
        ),
        params![cfg.max_file_size as i64, empty],
        |r| {
            Ok((
                r.get::<_, i64>(0)?,
                r.get::<_, i64>(1)?,
                r.get::<_, i64>(2)?,
                r.get::<_, i64>(3)?,
                r.get::<_, String>(4)?,
            ))
        },
    )?;
    let (doc_rows, last_doc) = conn.query_row(
        "SELECT count(*), coalesce(max(path), '') FROM docs",
        [],
        |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)),
    )?;
    Ok(Facts {
        kept: kept as usize,
        kept_bytes: kept_bytes as u64,
        loss_rows: loss_rows as usize,
        bytes_rows: bytes_rows as usize,
        last_path,
        doc_rows: doc_rows as usize,
        last_doc,
    })
}

/// The stored row of `path` and whether it is a pre-read loss.
fn named_row(
    conn: &Connection,
    cfg: &IndexConfig,
    path: &str,
) -> rusqlite::Result<Option<(Existing, bool, u64)>> {
    let empty = StatKey::default().encode();
    conn.prepare_cached(&format!(
        "SELECT id, content_hash, parse_status, lang, stat_key, flags, fact_symbols, size,
                {LOSS_ROW}
         FROM files WHERE path = ?3"
    ))?
    .query_row(params![cfg.max_file_size as i64, empty, path], |r| {
        Ok((
            Existing {
                id: r.get(0)?,
                content_hash: r.get(1)?,
                parse_status: r.get(2)?,
                lang: r.get(3)?,
                stat_key: r.get(4)?,
                flags: r.get(5)?,
                fact_symbols: r.get(6)?,
            },
            r.get::<_, bool>(8)?,
            r.get::<_, i64>(7)? as u64,
        ))
    })
    .optional()
}

/// Whether a stored file or doc row sorts under directory `dir` (`dir/` up to `dir0`).
fn rows_under(conn: &Connection, dir: &str) -> rusqlite::Result<bool> {
    let (lo, hi) = (format!("{dir}/"), format!("{dir}0"));
    conn.prepare_cached(
        "SELECT EXISTS(SELECT 1 FROM files WHERE path >= ?1 AND path < ?2)
             OR EXISTS(SELECT 1 FROM docs WHERE path >= ?1 AND path < ?2)",
    )?
    .query_row([&lo, &hi], |r| r.get(0))
}

/// What a named path that no language pack or docs role claims is now.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Other {
    File,
    Dir,
    Gone,
}

fn other_kind(root: &Path, path: &str) -> Other {
    crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
    match std::fs::symlink_metadata(root.join(path)) {
        Ok(m) if m.is_dir() => Other::Dir,
        Ok(_) => Other::File,
        Err(_) => Other::Gone,
    }
}

fn has_doc_row(conn: &Connection, path: &str) -> rusqlite::Result<bool> {
    conn.prepare_cached("SELECT 1 FROM docs WHERE path = ?1")?
        .exists([path])
}

fn meta_json<T: DeserializeOwned>(conn: &Connection, key: &str) -> rusqlite::Result<Option<T>> {
    Ok(meta::get(conn, key)?.and_then(|v| serde_json::from_str(&v).ok()))
}

fn meta_count(conn: &Connection, key: &str) -> rusqlite::Result<Option<usize>> {
    Ok(meta::get(conn, key)?.and_then(|v| v.parse().ok()))
}

/// A source path's state at the last listing, as far as the index stores it.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Before {
    /// A kept file of this size (it counts toward the envelope's files and bytes).
    Kept(u64),
    LossRow,
    /// Tracked, eligible and missing from the worktree.
    Deleted,
    /// A pre-read loss with no row: past the cut or the loss-row bound.
    BeyondLoss(String),
    /// No row and no exception (see the module comment).
    Unknown,
}

/// A doc path's state at the last listing.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum DocBefore {
    Row,
    /// Tracked, admitted and missing from the worktree.
    Deleted,
    /// No row and no exception: untracked, ignored, undecided, or past a full lane.
    Unknown,
}

/// A source path's state now, from one `lstat`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum After {
    Readable(u64),
    Loss(&'static str),
    Gone,
}

/// The effect of one batch on the stored envelope.
struct Plan {
    cut: bool,
    kept: i64,
    kept_bytes: i64,
    loss_rows: i64,
    /// A file entered or left the kept set, or changed size.
    kept_moved: bool,
    loss_rows_moved: bool,
    worktree_deleted: i64,
    beyond: BTreeMap<String, usize>,
    sample: Vec<(String, String)>,
    state: ListingState,
    doc_rows: i64,
    doc_full: bool,
    candidates: Vec<Candidate>,
    docs: Vec<DocCandidate>,
}

type Decided = Result<(), ListingReason>;

impl Plan {
    fn beyond_total(&self) -> usize {
        self.beyond.values().sum()
    }

    fn beyond_add(&mut self, path: &str, reason: &str) {
        *self.beyond.entry(reason.to_string()).or_default() += 1;
        self.sample.retain(|(p, _)| p != path);
        self.sample.push((path.to_string(), reason.to_string()));
        self.sample.sort();
        self.sample.truncate(MAX_LOSS_ENTRIES);
    }

    fn beyond_remove(&mut self, path: &str, reason: &str) -> Decided {
        let total = self.beyond_total();
        let n = self.beyond.entry(reason.to_string()).or_default();
        *n = n.saturating_sub(1);
        if *n == 0 {
            self.beyond.remove(reason);
        }
        if let Some(i) = self.sample.iter().position(|(p, _)| p == path) {
            if total > self.sample.len() {
                return Err(ListingReason::SampleRefill);
            }
            self.sample.remove(i);
        }
        Ok(())
    }

    fn except(&mut self, path: &str, state: Option<&str>) {
        match state {
            None => {
                self.state.exceptions.remove(path);
            }
            Some(s)
                if self.state.exceptions.len() < MAX_EXCEPTIONS
                    || self.state.exceptions.contains_key(path) =>
            {
                self.state
                    .exceptions
                    .insert(path.to_string(), s.to_string());
            }
            Some(_) => self.state.complete = false,
        }
    }

    fn keep(&mut self, size: u64) {
        self.kept += 1;
        self.kept_bytes += size as i64;
        self.kept_moved = true;
    }

    fn unkeep(&mut self, size: u64) {
        self.kept -= 1;
        self.kept_bytes -= size as i64;
        self.kept_moved = true;
    }

    fn loss_row(&mut self, delta: i64) {
        self.loss_rows += delta;
        self.loss_rows_moved = true;
    }

    /// The effect of a source path that went from `before` to `after`. `beyond`: the
    /// path sorts past the cut, so it can have no row.
    fn source(&mut self, path: &str, before: &Before, after: After, beyond: bool) -> Decided {
        use After::{Gone, Loss, Readable};
        use ListingReason::{EnvelopeShift, LossRows, UnknownPrior};
        let cut = self.cut;
        match (before, after, beyond) {
            (Before::Kept(old), Readable(new), _) => {
                self.kept_bytes += new as i64 - *old as i64;
                self.kept_moved |= new != *old;
            }
            (Before::Kept(_), Loss(_) | Gone, _) if cut => return Err(EnvelopeShift),
            (Before::Kept(old), Loss(_), _) => {
                self.unkeep(*old);
                self.loss_row(1);
            }
            (Before::Kept(old), Gone, _) => {
                self.unkeep(*old);
                self.worktree_deleted += 1;
                self.except(path, Some(DELETED));
            }
            (Before::LossRow, Readable(_), _) if cut => return Err(EnvelopeShift),
            (Before::LossRow, Readable(size), _) => {
                self.keep(size);
                self.loss_row(-1);
            }
            (Before::LossRow, Loss(_), _) => {}
            (Before::LossRow, Gone, _) => {
                self.loss_row(-1);
                self.worktree_deleted += 1;
                self.except(path, Some(DELETED));
            }
            (Before::Deleted, Gone, _) => {}
            (Before::Deleted, Readable(_), true) => {
                self.worktree_deleted -= 1;
                self.beyond_add(path, "envelope_files");
                self.except(path, None);
            }
            (Before::Deleted, Loss(reason), true) => {
                self.worktree_deleted -= 1;
                self.beyond_add(path, reason);
                self.except(path, Some(reason));
            }
            (Before::Deleted, Readable(_), false) if cut => return Err(EnvelopeShift),
            (Before::Deleted, Readable(size), false) => {
                self.worktree_deleted -= 1;
                self.keep(size);
                self.except(path, None);
            }
            (Before::Deleted, Loss(_), false) => {
                self.worktree_deleted -= 1;
                self.loss_row(1);
                self.except(path, None);
            }
            (Before::BeyondLoss(_), _, false) => return Err(LossRows),
            (Before::BeyondLoss(old), Readable(_), true) => {
                self.beyond_remove(path, old)?;
                self.beyond_add(path, "envelope_files");
                self.except(path, None);
            }
            (Before::BeyondLoss(old), Loss(reason), true) => {
                self.beyond_remove(path, old)?;
                self.beyond_add(path, reason);
                self.except(path, Some(reason));
            }
            (Before::BeyondLoss(old), Gone, true) => {
                self.beyond_remove(path, old)?;
                self.worktree_deleted += 1;
                self.except(path, Some(DELETED));
            }
            (Before::Unknown, Loss(_) | Gone, true) => return Err(UnknownPrior),
            (Before::Unknown, _, _) => {}
        }
        Ok(())
    }

    /// The effect of a doc path; `exists`: it is listed now. `beyond`: the docs lane is
    /// full and the path sorts past its last row.
    fn doc(&mut self, path: &str, before: DocBefore, exists: bool, beyond: bool) -> Decided {
        use ListingReason::{DocsShift, UnknownPrior};
        let full = self.doc_full;
        match (before, exists, beyond) {
            (DocBefore::Row, true, _) | (DocBefore::Deleted, false, _) => {}
            (DocBefore::Row, false, _) if full => return Err(DocsShift),
            (DocBefore::Row, false, _) => {
                self.doc_rows -= 1;
                self.except(path, Some(DOC_DELETED));
            }
            (DocBefore::Deleted, true, true) => {
                self.state.docs_beyond += 1;
                self.except(path, None);
            }
            (DocBefore::Deleted, true, false) if full => return Err(DocsShift),
            (DocBefore::Deleted, true, false) => {
                self.doc_rows += 1;
                self.except(path, None);
            }
            (DocBefore::Unknown, false, true) => return Err(UnknownPrior),
            (DocBefore::Unknown, _, _) => {}
        }
        Ok(())
    }

    /// Batch-wide bounds: the effects above are local only while these hold.
    fn bounds(
        &self,
        facts: &Facts,
        cfg: &IndexConfig,
        beyond_before: &BTreeMap<String, usize>,
    ) -> Decided {
        let kept = facts.kept as i64 + self.kept;
        if kept > cfg.max_files as i64 {
            return Err(ListingReason::EnvelopeShift);
        }
        let loss_rows = facts.loss_rows as i64 + self.loss_rows;
        if self.loss_rows_moved
            && (facts.loss_rows >= MAX_LOSS_ROWS || loss_rows > MAX_LOSS_ROWS as i64)
        {
            return Err(ListingReason::LossRows);
        }
        let binding = facts.bytes_rows > 0 || beyond_before.contains_key("envelope_bytes");
        let bytes = facts.kept_bytes as i64 + self.kept_bytes;
        if self.kept_moved && (binding || bytes > cfg.max_total_bytes as i64) {
            return Err(ListingReason::ByteBudget);
        }
        if facts.doc_rows as i64 + self.doc_rows > MAX_DOC_FILES as i64 {
            return Err(ListingReason::DocsShift);
        }
        Ok(())
    }
}

/// The effect of a named path that is not a source or doc file. A file needs
/// nothing. A directory may stand for files the batch does not name: one that is gone
/// held rows, or files past the cut or the docs lane's last row; one that is new (or
/// replaced) may hold restored tracked files (exceptions under it).
fn other_effect(kind: Other, rows_under: bool, exceptions_under: bool, past: bool) -> Decided {
    match (kind, rows_under, exceptions_under, past) {
        (Other::File, ..) => Ok(()),
        (Other::Dir, _, true, _) | (Other::Gone, true, _, _) | (Other::Gone, _, _, true) => {
            Err(ListingReason::Directory)
        }
        (Other::Dir, ..) | (Other::Gone, ..) => Ok(()),
    }
}

/// Whether `p` can name a file inside the root: relative, with no `.` or `..` parts.
fn inside_root(p: &str) -> bool {
    !p.is_empty()
        && Path::new(p)
            .components()
            .all(|c| matches!(c, Component::Normal(_)))
}

/// Decide `paths` alone (`Ok(Ok)`), or say why the tree must be listed (`Ok(Err)`).
pub(super) fn scope(
    conn: &Connection,
    layout: &Layout,
    cfg: &IndexConfig,
    paths: &[String],
) -> Result<Result<Scoped, ListingReason>, String> {
    scope_inner(conn, layout, cfg, paths).map_err(super::sql_err)
}

fn scope_inner(
    conn: &Connection,
    layout: &Layout,
    cfg: &IndexConfig,
    paths: &[String],
) -> rusqlite::Result<Result<Scoped, ListingReason>> {
    use ListingReason::*;
    let Some(git_dir) = layout.git_dir.as_deref() else {
        return Ok(Err(NonGit));
    };
    if cfg.include_untracked {
        return Ok(Err(IncludeUntracked));
    }
    let stored = (
        meta_json::<ListingState>(conn, meta::LISTING_STATE)?,
        meta_json::<BTreeMap<String, usize>>(conn, meta::BEYOND)?,
        meta_json::<Vec<(String, String)>>(conn, meta::BEYOND_SAMPLE)?,
        meta_json::<NestedRepos>(conn, meta::NESTED_REPOS)?,
        meta_count(conn, "worktree_deleted")?,
        meta_count(conn, "ignored_files")?,
        meta_count(conn, "ignore_rules_dropped")?,
        meta_count(conn, "invalid_paths")?,
    );
    let (
        Some(state),
        Some(beyond),
        Some(sample),
        Some(nested_repos),
        Some(worktree_deleted),
        Some(ignored),
        Some(ignore_rules_dropped),
        Some(invalid_paths),
    ) = stored
    else {
        return Ok(Err(NoListingState));
    };
    let head = scan::head(&layout.root);
    if meta::get(conn, "last_commit")?.as_deref() != Some(head.as_str()) {
        return Ok(Err(HeadMoved));
    }
    if scan::git_index_key(git_dir) != state.git_index {
        return Ok(Err(GitIndexChanged));
    }
    let named: BTreeSet<String> = normalize_paths(&layout.root, paths)
        .into_iter()
        .filter(|p| inside_root(p))
        .collect();
    if named
        .iter()
        .any(|p| p.rsplit('/').next() == Some(XMUSTARD_IGNORE))
    {
        return Ok(Err(IgnoreFile));
    }

    let facts = facts(conn, cfg)?;
    let started_ns = scan::now_ns();
    let mut plan = Plan {
        cut: beyond.get("envelope_files").is_some_and(|n| *n > 0),
        kept: 0,
        kept_bytes: 0,
        loss_rows: 0,
        kept_moved: false,
        loss_rows_moved: false,
        worktree_deleted: worktree_deleted as i64,
        beyond: beyond.clone(),
        sample,
        doc_rows: 0,
        doc_full: facts.doc_rows >= MAX_DOC_FILES,
        candidates: Vec::new(),
        docs: Vec::new(),
        state,
    };
    let exceptions_complete = plan.state.complete;
    let mut rows: HashMap<String, Existing> = HashMap::new();
    for path in &named {
        let exception = plan.state.exceptions.get(path).cloned();
        let decided = match (Lang::for_path(path), scan::doc_role(path)) {
            (Some(lang), _) => {
                let row = named_row(conn, cfg, path)?;
                crate::passcount::note(crate::passcount::Pass::RowsLoaded, row.is_some() as u64);
                let before = match (&row, exception.as_deref()) {
                    (Some((_, false, size)), _) => Before::Kept(*size),
                    (Some((_, true, _)), _) => Before::LossRow,
                    (None, Some(DELETED)) => Before::Deleted,
                    (None, Some(reason)) => Before::BeyondLoss(reason.to_string()),
                    (None, None) => Before::Unknown,
                };
                if before == Before::Unknown && !exceptions_complete {
                    return Ok(Err(ExceptionsIncomplete));
                }
                let stat = scan::stat_file(&layout.root, path, Mode::Regular, cfg.max_file_size);
                let after = match &stat {
                    None => After::Gone,
                    Some((_, Some(loss))) => After::Loss(loss),
                    Some((st, None)) => After::Readable(st.size),
                };
                let beyond = plan.cut && *path > facts.last_path;
                let decided = plan.source(path, &before, after, beyond);
                let listed = matches!(
                    (&before, after, beyond),
                    (
                        Before::Kept(_) | Before::LossRow,
                        After::Readable(_) | After::Loss(_),
                        _
                    ) | (Before::Deleted, After::Readable(_) | After::Loss(_), false)
                );
                if let (true, Some((st, loss))) = (listed, stat) {
                    plan.candidates.push(Candidate {
                        path: path.clone(),
                        lang,
                        stat: st,
                        loss,
                    });
                }
                if let Some((ex, _, _)) = row {
                    rows.insert(path.clone(), ex);
                }
                decided
            }
            (None, Some(role)) => {
                // a doc path only ever carries the doc_deleted exception
                let before = match (has_doc_row(conn, path)?, exception.is_some()) {
                    (true, _) => DocBefore::Row,
                    (false, true) => DocBefore::Deleted,
                    (false, false) => DocBefore::Unknown,
                };
                if before == DocBefore::Unknown && !exceptions_complete {
                    return Ok(Err(ExceptionsIncomplete));
                }
                let doc = scan::doc_candidate(&layout.root, path.clone(), role, Mode::Regular, cfg);
                let beyond = plan.doc_full && *path > facts.last_doc;
                let decided = plan.doc(path, before, doc.is_ok(), beyond);
                let listed = before == DocBefore::Row || (before == DocBefore::Deleted && !beyond);
                if let (Ok(d), true) = (doc, listed) {
                    plan.docs.push(d);
                }
                decided
            }
            (None, None) => {
                let kind = other_kind(&layout.root, path);
                let hi = format!("{path}0");
                let past =
                    (plan.cut && hi > facts.last_path) || (plan.doc_full && hi > facts.last_doc);
                let under = kind != Other::File && rows_under(conn, path)?;
                let lo = format!("{path}/");
                let exceptions_under = plan.state.exceptions.range(lo..hi).next().is_some();
                other_effect(kind, under, exceptions_under, past)
            }
        };
        if let Err(reason) = decided {
            return Ok(Err(reason));
        }
    }
    if let Err(reason) = plan.bounds(&facts, cfg, &beyond) {
        return Ok(Err(reason));
    }
    let docs_beyond = plan.state.docs_beyond;
    Ok(Ok(Scoped {
        scan: Scan {
            repo_mode: "git",
            head,
            git_dir: layout.git_dir.clone(),
            root: layout.root.clone(),
            candidates: plan.candidates,
            beyond: plan.beyond,
            beyond_sample: plan.sample,
            ignored,
            ignore_rules_dropped,
            invalid_paths,
            worktree_deleted: plan.worktree_deleted.max(0) as usize,
            started_ns,
            docs: plan.docs,
            docs_beyond,
            nested_repos,
            listing_state: Some(plan.state),
            only: Some(named),
        },
        rows,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn plan(cut: bool) -> Plan {
        Plan {
            cut,
            kept: 0,
            kept_bytes: 0,
            loss_rows: 0,
            kept_moved: false,
            loss_rows_moved: false,
            worktree_deleted: 0,
            beyond: BTreeMap::new(),
            sample: Vec::new(),
            state: ListingState {
                complete: true,
                ..Default::default()
            },
            doc_rows: 0,
            doc_full: false,
            candidates: Vec::new(),
            docs: Vec::new(),
        }
    }

    #[test]
    fn a_kept_file_cannot_leave_a_cut_envelope_alone() {
        let mut p = plan(true);
        assert_eq!(
            p.source("a.ts", &Before::Kept(4), After::Gone, false),
            Err(ListingReason::EnvelopeShift)
        );
        let mut p = plan(false);
        assert_eq!(
            p.source("a.ts", &Before::Kept(4), After::Gone, false),
            Ok(())
        );
        assert_eq!((p.kept, p.kept_bytes, p.worktree_deleted), (-1, -4, 1));
        assert_eq!(
            p.state.exceptions.get("a.ts").map(String::as_str),
            Some(DELETED)
        );
    }

    #[test]
    fn a_deleted_file_restored_past_the_cut_moves_the_counts_back() {
        let mut p = plan(true);
        p.worktree_deleted = 1;
        p.state.exceptions.insert("z.ts".into(), DELETED.into());
        assert_eq!(
            p.source("z.ts", &Before::Deleted, After::Readable(9), true),
            Ok(())
        );
        assert_eq!(p.worktree_deleted, 0);
        assert_eq!(p.beyond.get("envelope_files"), Some(&1));
        assert_eq!(p.sample, vec![("z.ts".into(), "envelope_files".into())]);
        assert!(p.state.exceptions.is_empty());
    }

    #[test]
    fn a_path_leaving_a_truncated_sample_needs_the_listing() {
        let mut p = plan(true);
        p.beyond.insert("symlink".into(), MAX_LOSS_ENTRIES + 1);
        p.sample = (0..MAX_LOSS_ENTRIES)
            .map(|i| (format!("z{i:04}.ts"), "symlink".to_string()))
            .collect();
        assert_eq!(
            p.source(
                "z0000.ts",
                &Before::BeyondLoss("symlink".into()),
                After::Gone,
                true
            ),
            Err(ListingReason::SampleRefill)
        );
    }

    #[test]
    fn an_unknown_path_past_the_cut_is_decided_only_while_readable() {
        let mut p = plan(true);
        assert_eq!(
            p.source("z.ts", &Before::Unknown, After::Readable(3), true),
            Ok(())
        );
        assert_eq!(
            p.source("z.ts", &Before::Unknown, After::Gone, true),
            Err(ListingReason::UnknownPrior)
        );
        assert_eq!(
            p.source("a.ts", &Before::Unknown, After::Gone, false),
            Ok(())
        );
    }

    #[test]
    fn a_full_docs_lane_lists_the_tree_when_its_set_moves() {
        let mut p = plan(false);
        p.doc_full = true;
        assert_eq!(
            p.doc("a.md", DocBefore::Row, false, false),
            Err(ListingReason::DocsShift)
        );
        assert_eq!(p.doc("z.md", DocBefore::Deleted, true, true), Ok(()));
        assert_eq!(p.state.docs_beyond, 1);
    }

    #[test]
    fn a_gone_or_restored_directory_lists_the_tree() {
        assert_eq!(other_effect(Other::File, true, true, true), Ok(()));
        assert_eq!(other_effect(Other::Gone, false, true, false), Ok(()));
        assert_eq!(
            other_effect(Other::Gone, true, false, false),
            Err(ListingReason::Directory)
        );
        assert_eq!(
            other_effect(Other::Gone, false, false, true),
            Err(ListingReason::Directory)
        );
        assert_eq!(other_effect(Other::Dir, true, false, true), Ok(()));
        assert_eq!(
            other_effect(Other::Dir, false, true, false),
            Err(ListingReason::Directory)
        );
    }

    #[test]
    fn paths_outside_the_root_are_not_named() {
        for (p, ok) in [
            ("a.ts", true),
            ("src/a.ts", true),
            ("../a.ts", false),
            ("/etc/passwd", false),
            ("./a.ts", false),
            ("", false),
        ] {
            assert_eq!(inside_root(p), ok, "{p}");
        }
    }
}
