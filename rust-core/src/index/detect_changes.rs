//! Diff-to-symbol change impact (PAR-IMP-06; WS-35): the symbols a scoped diff
//! touches, whether their contract broke, and what depends on them.
//!
//! The diff is the shared hardened `-U0` diff (`gitdiff`, WS-64) of a scope. Each changed
//! file of a language pack is parsed on both sides of the diff (old and new blobs from
//! the Git object database or the index, the new side from the working tree for a
//! working-tree scope), so spans are the current ones even for dirty and untracked files
//! the index has not seen yet. A symbol is touched when a hunk overlaps its lines (a
//! deletion counts when it falls inside the span); for each hunk only the innermost
//! touched symbols count, so editing a method does not list its class. A symbol is
//! `added`, `modified` or `removed` by its identity (qualified name and kind) on the
//! two sides; a modified symbol whose signature hash changed, or a removed exported
//! one, is a `contract_break`.
//!
//! The changed symbols are matched to the graph by path, name and kind, and one upstream
//! walk from all of them (impact v2) gives the dependents by depth, the risk and the
//! completeness envelope. Listings are capped (1,000 files, 200 parsed files, 500
//! symbols) and say so with `truncated` and `partial`.
//!
//! The parse runs in a one-shot process (`symbolgraph changes`), not in the resident
//! service: grammars and parse transients leave with it.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde::Serialize;

use super::config::IndexConfig;
use super::extract;
use super::facts::SymbolFact;
use super::impact::{self, Graph};
use super::lang::Lang;
use crate::gitdiff::{self, Change, FileDiff, Hunk, Mode, Resolved, Scope, Side, Touch};

/// Changed files listed (every one is counted).
pub const MAX_FILES: usize = 1000;
/// Changed files parsed for symbols.
pub const MAX_PARSED: usize = 200;
/// Changed symbols listed by default and at most.
pub const DEFAULT_SYMBOLS: usize = 200;
pub const MAX_SYMBOLS: usize = 500;

#[derive(Debug, Clone)]
pub struct ChangesQuery {
    pub scope: Scope,
    pub base: Option<String>,
    pub head: Option<String>,
    /// Another checkout of the same repository to diff instead of the root.
    pub worktree: Option<PathBuf>,
    /// Changed symbols listed.
    pub limit: usize,
    /// The walk from the changed symbols (`target` unused; depth 2 by default).
    pub impact: impact::Query,
}

impl ChangesQuery {
    pub fn new(scope: Scope) -> ChangesQuery {
        let mut walk = impact::Query::new("");
        walk.max_depth = 2;
        walk.limit = 20;
        ChangesQuery {
            scope,
            base: None,
            head: None,
            worktree: None,
            limit: DEFAULT_SYMBOLS,
            impact: walk,
        }
    }

    /// Apply `--name=value` flags: the scope's own, then impact's.
    pub fn apply_flags<I: IntoIterator<Item = String>>(&mut self, flags: I) -> Result<(), String> {
        let mut rest = Vec::new();
        for flag in flags {
            let Some((name, value)) = flag.strip_prefix("--").and_then(|f| f.split_once('='))
            else {
                return Err(format!("expected --name=value, got {flag:?}"));
            };
            match name {
                "base" => self.base = Some(value.to_string()),
                "head" => self.head = Some(value.to_string()),
                "worktree" => self.worktree = Some(PathBuf::from(value)),
                "symbols" => {
                    self.limit = impact::bounded(value, 1, MAX_SYMBOLS as u64)
                        .map_err(|e| format!("--symbols: {e}"))?
                        as usize
                }
                _ => rest.push(flag),
            }
        }
        self.impact.apply_flags(rest)
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct ChangedFile {
    pub path: String,
    pub change: Change,
    /// Changed symbols found in it.
    pub symbols: usize,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub binary: bool,
    /// Why its symbols were not read, when they were not.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unparsed: Option<&'static str>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChangedSymbol {
    /// The qualified name (`Container.name`).
    pub name: String,
    pub kind: String,
    pub path: String,
    /// Its name line on the side it lives on (the old side when removed).
    pub line: u32,
    /// `added`, `modified` or `removed`.
    pub change: &'static str,
    pub contract_break: bool,
    /// `signature` (its signature hash changed) or `removed` (an exported symbol went).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub signature_change: Option<&'static str>,
    /// Dependents one hop upstream in the graph (callers, importers, subtypes within
    /// the walk's relation types); absent when it has no graph node.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub direct_dependents: Option<usize>,
    /// `exact` (same path, name, kind and line), `moved` (same path, name and kind),
    /// `file` (a language walked on file edges: its file) or `none` (not in the graph:
    /// new since the index generation, or unindexed).
    pub graph_match: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct Changes {
    #[serde(flatten)]
    pub scope: Resolved,
    pub changed_files: Vec<ChangedFile>,
    /// Changed source files for which no symbols could be mapped.
    pub unmapped_files: Vec<String>,
    pub changed_files_total: usize,
    /// Files changed outside the workspace root (the root is a repository subdirectory).
    #[serde(skip_serializing_if = "is_zero")]
    pub outside_root: usize,
    pub changed_symbols: Vec<ChangedSymbol>,
    pub changed_symbols_total: usize,
    pub contract_breaks: usize,
    /// One upstream walk from every changed symbol in the graph.
    pub affected: Option<impact::Impact>,
    /// A listing was cut at its cap.
    pub truncated: bool,
    /// Some changed files were not read for symbols (see `notes`).
    pub partial: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub notes: Vec<String>,
    pub generated_at: String,
}

fn is_zero(n: &usize) -> bool {
    *n == 0
}

/// One changed file to map: its paths, change and hunks.
struct Entry {
    /// Relative to the workspace root.
    rel: String,
    /// Relative to the repository top level.
    full: String,
    change: Change,
    hunks: Vec<Hunk>,
    /// Map the whole file (untracked, binary, too many hunks).
    whole: bool,
    binary: bool,
}

/// Why [`detect`] gave no answer.
#[derive(Debug)]
pub enum DetectError {
    /// The request names what is not there: a worktree that is not a checkout of the
    /// repository, a revision that names no commit, base and head without a merge base,
    /// or a revision the scope does not take. The message names only what the caller
    /// gave.
    Refused(String),
    Failed(String),
}

impl From<String> for DetectError {
    fn from(e: String) -> Self {
        DetectError::Failed(e)
    }
}

/// The checkout the diff runs in: the root, or a worktree of the same repository at the
/// root's place in it. Checked in order, fail closed; a refusal names the worktree as
/// the caller gave it.
fn checkout(root: &Path, worktree: Option<&Path>) -> Result<PathBuf, String> {
    let Some(given) = worktree else {
        return Ok(root.to_path_buf());
    };
    let refuse = |why: &str| format!("worktree {}: {why}", given.display());
    let wt = std::fs::canonicalize(given).map_err(|e| refuse(&e.to_string()))?;
    if !wt.is_dir() {
        return Err(refuse("not a directory"));
    }
    let top = gitdiff::run_small(&wt, &["rev-parse", "--show-toplevel"], 4096)
        .map_err(|_| refuse("not a Git checkout"))?;
    let top = std::fs::canonicalize(String::from_utf8_lossy(&top).trim())
        .map_err(|e| refuse(&e.to_string()))?;
    let repo = |d: &Path| gitdiff::common_dir(d).ok();
    let ours = repo(root);
    if ours.is_none() || repo(&top) != ours {
        return Err(refuse("not a checkout of the workspace's repository"));
    }
    let prefix = gitdiff::prefix(root).map_err(|_| refuse("the workspace has no place in it"))?;
    let dir = std::fs::canonicalize(top.join(prefix)).map_err(|e| refuse(&e.to_string()))?;
    // The root's place there may be a symlink committed on that checkout's branch: every
    // Git child and confined read must still run inside the checkout, in its repository.
    if !dir.starts_with(&top) || repo(&dir) != ours {
        return Err(refuse(
            "the workspace's directory there leaves the checkout",
        ));
    }
    Ok(dir)
}

/// The changed entries of `r` under the root's `prefix`, untracked files included.
fn entries(
    dir: &Path,
    r: &Resolved,
    prefix: &str,
    out: &mut Changes,
) -> Result<Vec<Entry>, String> {
    let parsed = gitdiff::diff(
        dir,
        r,
        Mode::Symbols,
        gitdiff::Limits {
            files: MAX_FILES,
            ..Default::default()
        },
    )?;
    out.changed_files_total = parsed.total;
    if parsed.unreadable > 0 {
        out.partial = true;
        out.notes.push(format!(
            "{} changed file(s) have a name that could not be decoded, so their symbols are missing",
            parsed.unreadable
        ));
    }
    let mut list = Vec::new();
    for f in parsed.files {
        let FileDiff {
            path,
            change,
            binary,
            hunks,
            hunks_truncated,
        } = f;
        let Some(rel) = path.strip_prefix(prefix) else {
            out.outside_root += 1;
            continue;
        };
        list.push(Entry {
            rel: rel.to_string(),
            full: path.clone(),
            change,
            whole: binary || hunks_truncated,
            binary,
            hunks,
        });
    }
    if r.untracked {
        let room = MAX_FILES.saturating_sub(list.len());
        let (names, total) = gitdiff::untracked(dir, room)?;
        out.changed_files_total += total;
        for rel in names {
            list.push(Entry {
                full: format!("{prefix}{rel}"),
                rel,
                change: Change::Added,
                hunks: Vec::new(),
                whole: true,
                binary: false,
            });
        }
    }
    out.changed_files_total -= out.outside_root.min(out.changed_files_total);
    out.truncated |= out.changed_files_total > list.len();
    list.sort_by(|a, b| a.rel.cmp(&b.rel));
    Ok(list)
}

/// The facts of one side of a file, or None when it has no content there.
fn facts_of(lang: Lang, bytes: Option<Vec<u8>>, cfg: &IndexConfig) -> Option<Vec<SymbolFact>> {
    let bytes = bytes?;
    let (text, invalid) = match String::from_utf8(bytes) {
        Ok(t) => (t, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    };
    let facts = extract::extract(lang, &text, invalid, cfg.max_parse_bytes);
    Some(facts.symbols.into_iter().filter(|s| !s.local).collect())
}

/// The innermost symbols `touch` hits: those hit with no other hit symbol inside them.
fn innermost(symbols: &[SymbolFact], touch: Touch, into: &mut BTreeSet<usize>) {
    let hit: Vec<usize> = (0..symbols.len())
        .filter(|&i| touch.hits(symbols[i].start_line, symbols[i].end_line))
        .collect();
    let inside = |outer: &SymbolFact, inner: &SymbolFact| {
        outer.start_line <= inner.start_line
            && inner.end_line <= outer.end_line
            && (outer.start_line, outer.end_line) != (inner.start_line, inner.end_line)
    };
    for &i in &hit {
        if !hit
            .iter()
            .any(|&j| j != i && inside(&symbols[i], &symbols[j]))
        {
            into.insert(i);
        }
    }
}

/// Indexes of the symbols of one side the entry's hunks touch.
fn touched(symbols: &[SymbolFact], e: &Entry, side: fn(&Hunk) -> Touch) -> BTreeSet<usize> {
    let mut out = BTreeSet::new();
    if e.whole {
        out.extend(0..symbols.len());
        return out;
    }
    for h in &e.hunks {
        innermost(symbols, side(h), &mut out);
    }
    out
}

fn key(s: &SymbolFact) -> (String, String) {
    (s.qualified_name.clone(), s.kind.clone())
}

/// The changed symbols of one file from its two sides.
fn changed_symbols(
    e: &Entry,
    old: Option<&[SymbolFact]>,
    new: Option<&[SymbolFact]>,
) -> Vec<ChangedSymbol> {
    let (old, new) = (old.unwrap_or(&[]), new.unwrap_or(&[]));
    let old_by: BTreeMap<_, &SymbolFact> = old.iter().map(|s| (key(s), s)).collect();
    let new_by: BTreeMap<_, &SymbolFact> = new.iter().map(|s| (key(s), s)).collect();
    let mut keys: BTreeSet<(String, String)> = BTreeSet::new();
    keys.extend(
        touched(new, e, Hunk::new_lines)
            .into_iter()
            .map(|i| key(&new[i])),
    );
    keys.extend(
        touched(old, e, Hunk::old_lines)
            .into_iter()
            .map(|i| key(&old[i])),
    );
    let mut out = Vec::new();
    for k in keys {
        let row = match (old_by.get(&k), new_by.get(&k)) {
            (Some(o), Some(n)) => {
                let broke = !o.signature_hash.is_empty()
                    && !n.signature_hash.is_empty()
                    && o.signature_hash != n.signature_hash;
                (n, "modified", broke.then_some("signature"))
            }
            (None, Some(n)) => (n, "added", None),
            (Some(o), None) => (o, "removed", o.exported.then_some("removed")),
            (None, None) => continue,
        };
        let (s, change, signature_change) = row;
        out.push(ChangedSymbol {
            name: s.qualified_name.clone(),
            kind: s.kind.clone(),
            path: e.rel.clone(),
            line: s.name_line,
            change,
            contract_break: signature_change.is_some(),
            signature_change,
            direct_dependents: None,
            graph_match: "none",
        });
    }
    out
}

/// A file's symbols on the old and the new side of a diff (None where it has no
/// readable content).
type Sides = (Option<Vec<SymbolFact>>, Option<Vec<SymbolFact>>);

/// Parse both sides of the entries: blobs stream through one `cat-file --batch` and
/// are parsed as each arrives, working-tree files are read through the confined reader
/// one at a time, so at most one file's text is held.
fn parse_sides(
    dir: &Path,
    r: &Resolved,
    list: &[(&Entry, Lang)],
    cfg: &IndexConfig,
) -> Result<Vec<Sides>, String> {
    let mut specs = Vec::new();
    let mut owner = Vec::new(); // per spec: (entry, is the new side)
    for (k, (e, _)) in list.iter().enumerate() {
        let sides = [
            (e.change != Change::Added, &r.old, false),
            (e.change != Change::Deleted, &r.new, true),
        ];
        for (wanted, side, new) in sides {
            if let Some(spec) = side.object(&e.full).filter(|_| wanted) {
                specs.push(spec);
                owner.push((k, new));
            }
        }
    }
    let mut out: Vec<Sides> = vec![(None, None); list.len()];
    let cap = cfg.max_file_size;
    gitdiff::for_each_object(dir, &specs, cap as usize, |i, blob| {
        let (k, new) = owner[i];
        let facts = facts_of(list[k].1, blob, cfg);
        match new {
            true => out[k].1 = facts,
            false => out[k].0 = facts,
        }
    })?;
    if r.new == Side::Worktree {
        for (k, (e, lang)) in list.iter().enumerate() {
            if e.change != Change::Deleted {
                let bytes =
                    crate::symbolgraph::read_repo_bytes_beneath_capped(dir, &e.rel, cap).ok();
                out[k].1 = facts_of(*lang, bytes, cfg);
            }
        }
    }
    Ok(out)
}

/// Detect the changes of `q` in the repository at `root` (canonical) and map them onto
/// `g`, the graph the workspace's reads use.
pub fn detect<G: Graph + ?Sized>(
    root: &Path,
    cfg: &IndexConfig,
    q: &ChangesQuery,
    g: &G,
    losses: &BTreeMap<String, usize>,
) -> Result<Changes, DetectError> {
    let dir = checkout(root, q.worktree.as_deref()).map_err(DetectError::Refused)?;
    let r = gitdiff::resolve(&dir, q.scope, q.base.as_deref(), q.head.as_deref())
        .map_err(DetectError::Refused)?;
    let prefix = gitdiff::prefix(&dir)?;
    let mut out = Changes {
        scope: r.clone(),
        changed_files: Vec::new(),
        unmapped_files: Vec::new(),
        changed_files_total: 0,
        outside_root: 0,
        changed_symbols: Vec::new(),
        changed_symbols_total: 0,
        contract_breaks: 0,
        affected: None,
        truncated: false,
        partial: false,
        notes: Vec::new(),
        generated_at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
    };
    let list = entries(&dir, &r, &prefix, &mut out)?;

    // the files a language pack reads, parsed up to the cap
    let parse: Vec<(usize, Lang)> = list
        .iter()
        .enumerate()
        .filter_map(|(i, e)| Lang::for_path(&e.rel).map(|l| (i, l)))
        .collect();
    let (read, skipped) = parse.split_at(parse.len().min(MAX_PARSED));
    if !skipped.is_empty() {
        out.partial = true;
        out.notes.push(format!(
            "symbols were read from {} of {} changed source files (cap {MAX_PARSED})",
            read.len(),
            parse.len()
        ));
    }
    let entries: Vec<(&Entry, Lang)> = read.iter().map(|(i, l)| (&list[*i], *l)).collect();
    let sides = parse_sides(&dir, &r, &entries, cfg)?;
    let mut per_file: BTreeMap<usize, (usize, Option<&'static str>)> = BTreeMap::new();
    let mut symbols = Vec::new();
    for ((i, _), (old_f, new_f)) in read.iter().zip(sides) {
        let e = &list[*i];
        let wanted = (e.change != Change::Added, e.change != Change::Deleted);
        let missing = (wanted.0 && old_f.is_none()) || (wanted.1 && new_f.is_none());
        let found = changed_symbols(e, old_f.as_deref(), new_f.as_deref());
        per_file.insert(
            *i,
            (found.len(), missing.then_some("unreadable_or_oversized")),
        );
        out.partial |= missing;
        symbols.extend(found);
    }
    for (i, _) in skipped {
        per_file.insert(*i, (0, Some("parse_cap")));
    }

    // match to the graph, count direct dependents, walk from all of them
    let mut seeds = BTreeSet::new();
    for s in &mut symbols {
        if let Some(node) = graph_node(g, s)? {
            s.direct_dependents = Some(impact::direct_dependents(g, &q.impact, node)?);
            seeds.insert(node);
        }
    }
    out.contract_breaks = symbols.iter().filter(|s| s.contract_break).count();
    out.changed_symbols_total = symbols.len();
    // contract breaks first, then path order
    symbols.sort_by(|a, b| {
        b.contract_break
            .cmp(&a.contract_break)
            .then_with(|| (&a.path, a.line).cmp(&(&b.path, b.line)))
    });
    out.truncated |= symbols.len() > q.limit;
    symbols.truncate(q.limit);
    out.changed_symbols = symbols;
    out.changed_files = list
        .iter()
        .enumerate()
        .map(|(i, e)| {
            let (n, why) = per_file.get(&i).copied().unwrap_or((0, None));
            ChangedFile {
                path: e.rel.clone(),
                change: e.change,
                symbols: n,
                binary: e.binary,
                unparsed: why,
            }
        })
        .collect();
    for (i, e) in list.iter().enumerate() {
        if e.binary || !per_file.contains_key(&i) || per_file[&i].1.is_some() {
            continue;
        }
        let Some(file_node) = g.file_node(&e.rel)? else {
            out.unmapped_files.push(e.rel.clone());
            continue;
        };
        if g.file_symbols(file_node)?.is_empty() {
            out.unmapped_files.push(e.rel.clone());
        }
    }
    if !seeds.is_empty() {
        let mut walk = q.impact.clone();
        walk.direction = impact::Direction::Upstream;
        let mut affected = impact::run_nodes(g, &walk, seeds.into_iter().collect(), losses)?;
        affected.targets.clear(); // they are the changed symbols above
        affected.defined_in.clear();
        out.affected = Some(affected);
    }
    if q.worktree.is_some() {
        out.notes.push(
            "the graph is the workspace root's index; the changed symbols are the worktree's"
                .into(),
        );
    }
    Ok(out)
}

/// The graph node of a changed symbol: its file's symbol with the same name and kind,
/// nearest in line.
fn graph_node<G: Graph + ?Sized>(g: &G, s: &mut ChangedSymbol) -> Result<Option<u32>, String> {
    let Some(file) = g.file_node(&s.path)? else {
        return Ok(None);
    };
    if !g.resolved(file) {
        s.graph_match = "file";
        return Ok(Some(file));
    }
    let name = s.name.rsplit('.').next().unwrap_or(&s.name);
    let mut best: Option<(u32, u32)> = None;
    for n in g.file_symbols(file)? {
        let Some(f) = g.symbol(n)? else { continue };
        if f.name != name || f.kind != s.kind {
            continue;
        }
        let d = f.line.abs_diff(s.line);
        if best.is_none_or(|(_, bd)| d < bd) {
            best = Some((n, d));
        }
    }
    Ok(best.map(|(n, d)| {
        s.graph_match = if d == 0 { "exact" } else { "moved" };
        n
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sym(q: &str, kind: &str, lines: (u32, u32), sig: &str, exported: bool) -> SymbolFact {
        SymbolFact {
            name: q.rsplit('.').next().unwrap().to_string(),
            qualified_name: q.to_string(),
            kind: kind.to_string(),
            container: None,
            arity: None,
            start_byte: 0,
            end_byte: 0,
            start_line: lines.0,
            end_line: lines.1,
            name_line: lines.0,
            name_col: 0,
            signature_hash: sig.to_string(),
            exported,
            depth: 0,
            local: false,
            uid_suffix: String::new(),
        }
    }

    fn entry(change: Change, hunks: &[(u32, u32, u32, u32)]) -> Entry {
        Entry {
            rel: "a.ts".into(),
            full: "a.ts".into(),
            change,
            hunks: hunks
                .iter()
                .map(|h| Hunk {
                    old_start: h.0,
                    old_len: h.1,
                    new_start: h.2,
                    new_len: h.3,
                })
                .collect(),
            whole: false,
            binary: false,
        }
    }

    // editing a method lists the method, not its class; a signature change breaks the
    // contract, a removed export does too, and an added symbol is added
    #[test]
    fn hunks_map_to_innermost_symbols_with_contract_breaks() {
        let old = vec![
            sym("Widget", "Class", (1, 20), "", true),
            sym("Widget.render", "Method", (3, 8), "h1", true),
            sym("Widget.size", "Method", (10, 12), "s1", true),
            sym("gone", "Function", (22, 25), "g", true),
        ];
        let new = vec![
            sym("Widget", "Class", (1, 21), "", true),
            sym("Widget.render", "Method", (3, 9), "h2", true),
            sym("Widget.size", "Method", (11, 13), "s1", true),
            sym("fresh", "Function", (23, 24), "f", false),
        ];
        // render's parameters change (4 -> 4,5), size's body edit, gone replaced by fresh
        let e = entry(
            Change::Modified,
            &[(4, 1, 4, 2), (11, 1, 12, 1), (22, 4, 23, 2)],
        );
        let got: Vec<(String, &str, bool)> = changed_symbols(&e, Some(&old), Some(&new))
            .into_iter()
            .map(|s| (s.name, s.change, s.contract_break))
            .collect();
        assert_eq!(
            got,
            vec![
                ("Widget.render".into(), "modified", true),
                ("Widget.size".into(), "modified", false),
                ("fresh".into(), "added", false),
                ("gone".into(), "removed", true),
            ]
        );
    }

    #[test]
    fn an_untracked_or_whole_file_change_lists_every_symbol_as_added() {
        let new = vec![sym("a", "Function", (1, 2), "x", true)];
        let mut e = entry(Change::Added, &[]);
        e.whole = true;
        let got = changed_symbols(&e, None, Some(&new));
        assert_eq!((got.len(), got[0].change), (1, "added"));
    }
}
