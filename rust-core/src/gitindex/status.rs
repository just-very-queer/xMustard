//! Git's dirty view without a Git child that loads the index (WS-RT18): what
//! `git status --porcelain=v1 -z --untracked-files=all --ignore-submodules=none
//! --no-renames` reports, plus the assume-unchanged and skip-worktree entries
//! `git ls-files -v` tags, computed from the streamed index.
//!
//! - Index against HEAD (X): the index stream merged with HEAD's trees
//!   (`objects::HeadWalk`), which skips every subtree the cache-tree vouches for.
//! - Worktree against index (Y): one `lstat` per entry compared with its stat data, as
//!   Git's refresh compares them. An entry whose stat data differs, or is racy (its
//!   mtime is not older than the index file's, Git's `racy-git.txt` rule), is decided by
//!   `git update-index -q --refresh` on a private index holding only such entries with
//!   their stat data zeroed (at most `REFRESH_BATCH` per child), so Git's own filters,
//!   eol and autocrlf rules, `core.filemode` and symlink rules decide content.
//! - A gitlink compares the submodule's HEAD, then the submodule's own status, stopping
//!   at its first change.
//! - Untracked and ignored paths come from `walk`.

use std::fs;
use std::io::ErrorKind;
use std::ops::ControlFlow;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};

use super::Refusal;
use super::objects::{HeadItem, HeadWalk};
use super::reader::{Entry, Hash, Index, Oid, Stat};
use crate::indexcache::{StatusEntry, git_timeout, path_from_bytes, run_git_bounded};

/// The most `git config --list` output the reader takes.
const MAX_CONFIG_BYTES: usize = 1 << 20;

/// Entries decided by one `update-index --refresh` child.
pub const REFRESH_BATCH: usize = 4096;

const MODE_GITLINK: u32 = 0o160000;

/// A repository as the reader sees it, from one `git rev-parse` and one `git config`.
#[derive(Debug, Clone)]
pub struct Repo {
    pub toplevel: PathBuf,
    pub git_dir: PathBuf,
    pub common_dir: PathBuf,
    /// The opened directory relative to the top level, `""` or `dir/`.
    pub prefix: Vec<u8>,
    pub hash: Hash,
    /// `core.excludesFile`, or Git's default (`$XDG_CONFIG_HOME/git/ignore`).
    pub excludes_file: Option<PathBuf>,
    /// `core.ignorecase`: Git's case-insensitive path matching setting.
    pub ignore_case: bool,
    /// `core.symlinks`: false means symlinks are checked out as regular files.
    pub symlinks: bool,
}

fn git_text(root: &Path, args: &[&str]) -> Result<String, Refusal> {
    run_git_bounded(root, args, 64 << 10, git_timeout())
        .map(|b| String::from_utf8_lossy(&b).into_owned())
        .map_err(|e| Refusal::Failed(format!("git {}: {e}", args[0])))
}

impl Repo {
    pub fn open(root: &Path) -> Result<Repo, Refusal> {
        let text = git_text(
            root,
            &[
                "rev-parse",
                "--path-format=absolute",
                "--show-toplevel",
                "--absolute-git-dir",
                "--git-common-dir",
                "--show-object-format",
                "--show-prefix",
            ],
        )?;
        let lines: Vec<&str> = text.split('\n').collect();
        let [top, git_dir, common, format, prefix, ..] = lines[..] else {
            return Err(Refusal::Failed("git rev-parse: not a worktree".into()));
        };
        let hash = Hash::parse(format)
            .ok_or_else(|| Refusal::Unsupported(format!("object format {format}")))?;
        // the whole listing (it exits 0 with no settings), so a config Git cannot read,
        // or one past the bound, is a refusal rather than an empty config that would
        // skip `core.ignorecase` and `core.excludesFile`
        let config = run_git_bounded(
            root,
            &["config", "-z", "--list"],
            MAX_CONFIG_BYTES,
            git_timeout(),
        )
        .map_err(|e| Refusal::Failed(format!("git config: {e}")))?;
        let mut excludes_file = default_excludes_file();
        let mut ignore_case = false;
        let mut symlinks = true;
        for rec in config.split(|b| *b == 0).filter(|r| !r.is_empty()) {
            let rec = String::from_utf8_lossy(rec);
            let (key, value) = rec.split_once('\n').unwrap_or((&rec, ""));
            match key {
                "core.excludesfile" => excludes_file = Some(expand_home(value)),
                "core.ignorecase" => {
                    ignore_case = matches!(
                        value.to_ascii_lowercase().as_str(),
                        "true" | "yes" | "on" | "1" | ""
                    )
                }
                "core.symlinks" => {
                    symlinks = !matches!(
                        value.to_ascii_lowercase().as_str(),
                        "false" | "no" | "off" | "0"
                    )
                }
                _ => {}
            }
        }
        Ok(Repo {
            toplevel: PathBuf::from(top),
            git_dir: PathBuf::from(git_dir),
            common_dir: PathBuf::from(common),
            prefix: prefix.as_bytes().to_vec(),
            hash,
            excludes_file,
            ignore_case,
            symlinks,
        })
    }

    /// The index of this checkout, opened and verified.
    pub fn index(&self) -> Result<Index, Refusal> {
        let ix = Index::open(&self.git_dir, self.hash)?;
        ix.verify_order(self.ignore_case)?;
        Ok(ix)
    }

    /// HEAD's tree, None when HEAD is unborn.
    pub(crate) fn head_tree(&self) -> Result<Option<Oid>, Refusal> {
        match run_git_bounded(
            &self.toplevel,
            &["rev-parse", "-q", "--verify", "HEAD^{tree}"],
            64 << 10,
            git_timeout(),
        ) {
            Ok(b) => Oid::parse_hex(&String::from_utf8_lossy(&b))
                .map(Some)
                .ok_or_else(|| Refusal::Failed("git rev-parse HEAD^{tree}: not an id".into())),
            Err(crate::indexcache::GitRunError::Failed) => Ok(None),
            Err(e) => Err(Refusal::Failed(format!("git rev-parse HEAD^{{tree}}: {e}"))),
        }
    }
}

fn default_excludes_file() -> Option<PathBuf> {
    match std::env::var_os("XDG_CONFIG_HOME").filter(|v| !v.is_empty()) {
        Some(x) => Some(PathBuf::from(x).join("git/ignore")),
        None => std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".config/git/ignore")),
    }
}

fn expand_home(value: &str) -> PathBuf {
    match (value.strip_prefix("~/"), std::env::var_os("HOME")) {
        (Some(rest), Some(home)) => PathBuf::from(home).join(rest),
        _ => PathBuf::from(value),
    }
}

/// What `status` reports.
#[derive(Debug, Clone, Copy)]
pub struct Options {
    /// Report untracked paths (`??`), as `--untracked-files=all`.
    pub untracked: bool,
    /// Also report the directories ignored as a whole (`!! dir/`), as
    /// `--ignored=matching` does; individually ignored files are never reported.
    pub list_ignored: bool,
}

/// What a `status` call found besides its entries.
#[derive(Debug, Default)]
pub struct Outcome {
    /// The assume-unchanged and skip-worktree entries, `xy = ['!', tag]` with
    /// `ls-files -v`'s tag, once per path in index order.
    pub hidden: Vec<StatusEntry>,
    /// Why the untracked walk did not finish, when it did not.
    pub untracked_incomplete: Option<String>,
}

/// Git's status view of `repo`, each entry handed to `each`; a `Break` stops it.
pub fn status(
    repo: &Repo,
    opts: Options,
    each: &mut dyn FnMut(StatusEntry) -> ControlFlow<()>,
) -> Result<Outcome, Refusal> {
    let ix = repo.index()?;
    let mut out = Outcome::default();
    let mut emit = |t: Tracked| {
        crate::passcount::note(crate::passcount::Pass::FilesListed, 1);
        each(StatusEntry {
            xy: t.xy,
            path: t.path,
            orig_path: None,
        })
    };
    let flow = track(repo, &ix, &mut emit, Some(&mut out.hidden))?;
    if flow.is_continue() && opts.untracked {
        out.untracked_incomplete = super::walk::untracked(repo, &ix, b"", opts.list_ignored, each)?;
    }
    Ok(out)
}

/// A tracked path with a change: its status code and HEAD's mode and id for it.
pub(crate) struct Tracked {
    pub path: Vec<u8>,
    pub xy: [u8; 2],
    pub head: Option<(u32, Oid)>,
}

/// Git's two-letter codes for an unmerged path, by its mask of stages (1 = base,
/// 2 = ours, 4 = theirs).
const UNMERGED: [[u8; 2]; 8] = [
    *b"  ", *b"DD", *b"AU", *b"UD", *b"UA", *b"DU", *b"AA", *b"UU",
];

pub(crate) fn is_unmerged(xy: [u8; 2]) -> bool {
    UNMERGED[1..].contains(&xy)
}

/// An unmerged path being read: its path, the mask of its stages so far and HEAD's
/// mode and id for it.
type Unmerged = (Vec<u8>, usize, Option<(u32, Oid)>);

/// An entry waiting for `update-index --refresh` to decide its worktree side.
struct Pending {
    path: Vec<u8>,
    mode: u32,
    oid: Oid,
    /// The size the index recorded for it.
    size: u32,
    x: u8,
    head: Option<(u32, Oid)>,
}

/// The worktree side of one entry, or the need to ask Git.
enum Side {
    Is(u8),
    Ask,
}

struct Run<'a> {
    repo: &'a Repo,
    index_mtime: u32,
    each: &'a mut dyn FnMut(Tracked) -> ControlFlow<()>,
    stopped: bool,
    batch: Vec<Pending>,
    /// The deepest directory known to be a real directory (no symlink on the way),
    /// with its trailing `/`.
    real_dir: Vec<u8>,
}

impl Run<'_> {
    fn emit(&mut self, t: Tracked) {
        if !self.stopped && (self.each)(t).is_break() {
            self.stopped = true;
        }
    }

    fn emit_changed(&mut self, path: Vec<u8>, xy: [u8; 2], head: Option<(u32, Oid)>) {
        if xy != *b"  " {
            self.emit(Tracked { path, xy, head });
        }
    }

    /// Whether every directory leading to `path` is a real directory (Git's
    /// leading-symlink check: an entry under a symlinked directory is deleted).
    fn leading_dirs_real(&mut self, path: &[u8]) -> Result<bool, Refusal> {
        let Some(slash) = path.iter().rposition(|c| *c == b'/') else {
            return Ok(true);
        };
        let dir = &path[..=slash];
        if self.real_dir == dir {
            return Ok(true);
        }
        let mut known = common_dirs(&self.real_dir, dir);
        while let Some(k) = dir[known..].iter().position(|c| *c == b'/') {
            let end = known + k;
            crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
            match fs::symlink_metadata(self.repo.toplevel.join(path_from_bytes(&dir[..end]))) {
                Ok(m) if m.is_dir() => known = end + 1,
                Ok(_) => return Ok(false),
                Err(e) if missing(&e) => return Ok(false),
                Err(e) => return Err(Refusal::Failed(format!("lstat: {e}"))),
            }
        }
        self.real_dir.clear();
        self.real_dir.extend_from_slice(dir);
        Ok(true)
    }

    /// The worktree side (Y) of a merged entry that is not hidden.
    fn worktree(&mut self, e: &Entry) -> Result<Side, Refusal> {
        let full = self.repo.toplevel.join(path_from_bytes(&e.path));
        crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
        let meta = match fs::symlink_metadata(&full) {
            Ok(m) => m,
            Err(err) if missing(&err) => return Ok(Side::Is(b'D')),
            Err(err) => return Err(Refusal::Failed(format!("lstat {}: {err}", full.display()))),
        };
        if !self.leading_dirs_real(&e.path)? {
            return Ok(Side::Is(b'D'));
        }
        let kind = meta.mode() & 0o170000;
        let want = e.mode & 0o170000;
        if e.intent_to_add {
            let gone = kind == 0o040000 && !super::walk::is_repository(&full);
            return Ok(Side::Is(if gone { b'D' } else { b'A' }));
        }
        match (want, kind) {
            (MODE_GITLINK, 0o040000) => Ok(Side::Is(match submodule_dirty(&full, &e.oid)? {
                true => b'M',
                false => b' ',
            })),
            (MODE_GITLINK, _) => Ok(Side::Is(b'T')),
            (_, 0o040000) => match super::walk::is_repository(&full) {
                true => Ok(Side::Is(b'T')),
                false => Ok(Side::Is(b'D')),
            },
            (want, kind) if want == 0o120000 && kind == 0o100000 && !self.repo.symlinks => {
                Ok(Side::Ask)
            }
            (w, k) if w != k => Ok(Side::Is(b'T')),
            _ => {
                let racy = self.index_mtime != 0 && self.index_mtime <= e.stat.mtime.0;
                let exec_changed = kind == 0o100000 && (e.mode ^ meta.mode()) & 0o100 != 0;
                match e.stat == Stat::of(&meta) && !racy && !exec_changed {
                    true => Ok(Side::Is(b' ')),
                    false => Ok(Side::Ask),
                }
            }
        }
    }

    fn flush(&mut self) -> Result<(), Refusal> {
        if self.batch.is_empty() {
            return Ok(());
        }
        let batch = std::mem::take(&mut self.batch);
        let items: Vec<Probe<'_>> = batch
            .iter()
            .map(|p| Probe {
                path: &p.path,
                mode: p.mode,
                oid: p.oid,
                size: p.size,
            })
            .collect();
        let dirty = refresh(self.repo, &items)?;
        for (p, dirty) in batch.into_iter().zip(dirty) {
            let y = if dirty { b'M' } else { b' ' };
            self.emit_changed(p.path, [p.x, y], p.head);
        }
        Ok(())
    }
}

/// Bytes of `known` (a directory with a trailing `/`) that `dir` shares, at a `/`.
fn common_dirs(known: &[u8], dir: &[u8]) -> usize {
    let n = known.iter().zip(dir).take_while(|(a, b)| a == b).count();
    known[..n]
        .iter()
        .rposition(|c| *c == b'/')
        .map_or(0, |k| k + 1)
}

fn missing(e: &std::io::Error) -> bool {
    matches!(e.kind(), ErrorKind::NotFound | ErrorKind::NotADirectory)
}

/// HEAD's entry for `path`, consuming HEAD's earlier paths (each reported deleted from
/// the index). The second value is true when the cache-tree vouches that HEAD equals
/// the index under a directory holding `path`.
fn head_for(
    run: &mut Run<'_>,
    head: &mut HeadWalk<'_>,
    path: &[u8],
) -> Result<(Option<(u32, Oid)>, bool), Refusal> {
    use std::cmp::Ordering;
    loop {
        let order = match head.peek()? {
            None => return Ok((None, false)),
            Some(HeadItem::Same { prefix }) if path.starts_with(prefix) => return Ok((None, true)),
            Some(HeadItem::Same { prefix }) if path < prefix.as_slice() => {
                return Ok((None, false));
            }
            Some(HeadItem::Same { .. }) => None,
            Some(HeadItem::File { path: p, .. }) => Some(p.as_slice().cmp(path)),
        };
        let item = match order {
            Some(Ordering::Greater) => return Ok((None, false)),
            _ => head.next()?,
        };
        if let Some(HeadItem::File { path: p, mode, oid }) = item {
            if order == Some(Ordering::Equal) {
                return Ok((Some((mode, oid)), false));
            }
            run.emit(Tracked {
                path: p,
                xy: *b"D ",
                head: Some((mode, oid)),
            });
            if run.stopped {
                return Ok((None, false));
            }
        }
    }
}

/// X: the index entry `e` against HEAD's `h`.
fn index_side(e: &Entry, h: Option<(u32, Oid)>, same: bool) -> u8 {
    match (same, h) {
        (true, _) => b' ',
        (false, None) if e.intent_to_add => b' ',
        (false, None) => b'A',
        (false, Some(_)) if e.intent_to_add => b'D',
        (false, Some((mode, _))) if mode >> 12 != e.mode >> 12 => b'T',
        (false, Some((mode, oid))) if mode != e.mode || oid != e.oid => b'M',
        (false, Some(_)) => b' ',
    }
}

/// `ls-files -v`'s tag for a hidden entry (assume-unchanged in lower case,
/// skip-worktree `S`), or None.
fn hidden_tag(e: &Entry) -> Option<u8> {
    let tag = match (e.stage, e.skip_worktree) {
        (0, true) => b'S',
        (0, false) => b'H',
        _ => b'M',
    };
    match (e.assume_valid, tag) {
        (true, t) => Some(t.to_ascii_lowercase()),
        (false, b'S') => Some(b'S'),
        _ => None,
    }
}

/// Every tracked path with a change, to `each`; the hidden entries to `hidden`.
pub(crate) fn track(
    repo: &Repo,
    ix: &Index,
    each: &mut dyn FnMut(Tracked) -> ControlFlow<()>,
    mut hidden: Option<&mut Vec<StatusEntry>>,
) -> Result<ControlFlow<()>, Refusal> {
    let root = repo.head_tree()?;
    let mut head = HeadWalk::new(&repo.toplevel, repo.hash, root, &ix.cache_tree);
    let mut run = Run {
        repo,
        index_mtime: ix.mtime as u32,
        each,
        stopped: false,
        batch: Vec::new(),
        real_dir: Vec::new(),
    };
    let mut entries = ix.entries()?;
    // an unmerged path: its stages so far and HEAD's entry
    let mut unmerged: Option<Unmerged> = None;
    let mut last_hidden: Vec<u8> = Vec::new();
    while let Some(e) = entries.read_next()? {
        if run.stopped {
            break;
        }
        if let Some(tag) = hidden_tag(e)
            && let Some(list) = hidden.as_deref_mut()
            && last_hidden != e.path
        {
            last_hidden.clone_from(&e.path);
            list.push(StatusEntry {
                xy: [b'!', tag],
                path: e.path.clone(),
                orig_path: None,
            });
        }
        if let Some((p, mask, h)) = unmerged.take_if(|(p, _, _)| *p != e.path) {
            run.emit(Tracked {
                path: p,
                xy: UNMERGED[mask],
                head: h,
            });
        }
        if e.stage > 0 {
            let bit = 1usize << (e.stage - 1);
            match unmerged.as_mut() {
                Some((_, mask, _)) => *mask |= bit,
                None => {
                    let (h, _) = head_for(&mut run, &mut head, &e.path)?;
                    unmerged = Some((e.path.clone(), bit, h));
                }
            }
            continue;
        }
        let (h, same) = head_for(&mut run, &mut head, &e.path)?;
        let h = match same && !e.intent_to_add {
            true => Some((e.mode, e.oid)),
            false => h,
        };
        let x = index_side(e, h, same);
        let side = match e.assume_valid || e.skip_worktree {
            true => Side::Is(b' '),
            false => run.worktree(e)?,
        };
        match side {
            Side::Is(y) => run.emit_changed(e.path.clone(), [x, y], h),
            Side::Ask => {
                run.batch.push(Pending {
                    path: e.path.clone(),
                    mode: e.mode,
                    oid: e.oid,
                    size: e.stat.size,
                    x,
                    head: h,
                });
                if run.batch.len() >= REFRESH_BATCH {
                    run.flush()?;
                }
            }
        }
    }
    if let Some((p, mask, h)) = unmerged.take() {
        run.emit(Tracked {
            path: p,
            xy: UNMERGED[mask],
            head: h,
        });
    }
    run.flush()?;
    while !run.stopped {
        match head.next()? {
            None => break,
            Some(HeadItem::Same { .. }) => {}
            Some(HeadItem::File { path, mode, oid }) => run.emit(Tracked {
                path,
                xy: *b"D ",
                head: Some((mode, oid)),
            }),
        }
    }
    Ok(match run.stopped {
        true => ControlFlow::Break(()),
        false => ControlFlow::Continue(()),
    })
}

/// Whether the submodule checked out at `dir` differs from commit `oid`: another HEAD,
/// or changed or untracked content. A directory that is not a checked-out repository
/// (an uninitialised submodule) does not.
fn submodule_dirty(dir: &Path, oid: &Oid) -> Result<bool, Refusal> {
    submodule_dirty_with_head(dir, oid, |dir| {
        run_git_bounded(
            dir,
            &["rev-parse", "-q", "--verify", "HEAD"],
            64 << 10,
            git_timeout(),
        )
    })
}

fn submodule_dirty_with_head(
    dir: &Path,
    oid: &Oid,
    head_probe: impl FnOnce(&Path) -> Result<Vec<u8>, crate::indexcache::GitRunError>,
) -> Result<bool, Refusal> {
    if !super::walk::is_repository(dir) {
        return Ok(false);
    }
    let head = match head_probe(dir) {
        Ok(bytes) => Some(
            Oid::parse_hex(&String::from_utf8_lossy(&bytes)).ok_or_else(|| {
                Refusal::Failed(format!(
                    "submodule {}: rev-parse returned an invalid HEAD",
                    dir.display()
                ))
            })?,
        ),
        Err(crate::indexcache::GitRunError::Failed) => None,
        Err(error) => {
            return Err(Refusal::Failed(format!(
                "submodule {}: rev-parse: {error}",
                dir.display()
            )));
        }
    };
    if head.is_some_and(|head| head != *oid) {
        return Ok(true);
    }
    let sub = Repo::open(dir)?;
    let mut dirty = false;
    let opts = Options {
        untracked: true,
        list_ignored: false,
    };
    let outcome = status(&sub, opts, &mut |_| {
        dirty = true;
        ControlFlow::Break(())
    })
    .map_err(|e| Refusal::Failed(format!("submodule {}: {e}", dir.display())))?;
    if !dirty {
        ensure_complete_submodule(dir, &outcome)?;
    }
    Ok(dirty)
}

fn ensure_complete_submodule(dir: &Path, outcome: &Outcome) -> Result<(), Refusal> {
    if let Some(reason) = &outcome.untracked_incomplete {
        return Err(Refusal::Failed(format!(
            "submodule {}: untracked walk incomplete: {reason}",
            dir.display()
        )));
    }
    Ok(())
}

/// One path whose worktree side Git decides: the index entry to compare it with.
#[derive(Clone, Copy)]
pub(crate) struct Probe<'a> {
    pub path: &'a [u8],
    pub mode: u32,
    pub oid: Oid,
    /// The size recorded in the index, or 0 to make Git compare content whatever the
    /// worktree's size.
    pub size: u32,
}

/// Ask Git whether each probe's worktree path differs from its entry: true for those
/// it could not refresh. A private index holds them with their mode, id and recorded
/// size and every other stat field zeroed, so Git decides each one as `git status`
/// would after a stat change: a size change against a nonzero recorded size is a
/// change, anything else is a content comparison through Git's filters, eol and
/// autocrlf rules.
pub(crate) fn refresh(repo: &Repo, items: &[Probe<'_>]) -> Result<Vec<bool>, Refusal> {
    let mut out = Vec::with_capacity(items.len());
    for chunk in items.chunks(REFRESH_BATCH) {
        out.extend(refresh_one(repo, chunk)?);
    }
    Ok(out)
}

fn refresh_one(repo: &Repo, items: &[Probe<'_>]) -> Result<Vec<bool>, Refusal> {
    let mut sorted: Vec<usize> = (0..items.len()).collect();
    sorted.sort_by(|a, b| items[*a].path.cmp(items[*b].path));
    let h = repo.hash.oid_len();
    let mut bytes = Vec::with_capacity(12 + items.len() * 80);
    bytes.extend_from_slice(b"DIRC");
    bytes.extend_from_slice(&2u32.to_be_bytes());
    bytes.extend_from_slice(&(items.len() as u32).to_be_bytes());
    for &i in &sorted {
        let Probe {
            path,
            mode,
            oid,
            size,
        } = items[i];
        let start = bytes.len();
        // ctime, mtime, dev, ino; mode; uid, gid; size
        bytes.extend_from_slice(&[0u8; 24]);
        bytes.extend_from_slice(&mode.to_be_bytes());
        bytes.extend_from_slice(&[0u8; 8]);
        bytes.extend_from_slice(&size.to_be_bytes());
        bytes.extend_from_slice(oid.as_bytes());
        bytes.extend_from_slice(&(path.len().min(0xfff) as u16).to_be_bytes());
        bytes.extend_from_slice(path);
        bytes.resize(start + ((40 + h + 2 + path.len() + 8) & !7), 0);
    }
    let sum = super::reader::checksum(repo.hash, &bytes);
    bytes.extend_from_slice(&sum);
    let file = PrivateIndex::create(&bytes)?;
    crate::indexcache::note_git_spawn();
    let mut child = std::process::Command::new("git")
        .arg("-C")
        .arg(&repo.toplevel)
        .args([
            "-c",
            "core.splitIndex=false",
            "-c",
            "core.fsmonitor=false",
            "-c",
            "core.untrackedCache=false",
            "update-index",
            "-q",
            "--refresh",
        ])
        .env("GIT_INDEX_FILE", &file.path)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .map_err(|e| Refusal::Failed(format!("git update-index could not start: {e}")))?;
    let status = wait_bounded(&mut child, git_timeout())?;
    if !status.success() {
        return Err(Refusal::Failed("git update-index --refresh failed".into()));
    }
    let back = Index::open_file(&file.path, repo.hash)?;
    let mut entries = back.entries()?;
    let mut dirty = vec![true; items.len()];
    let mut at = 0usize;
    while let Some(e) = entries.read_next()? {
        // the same entries in the same order; a refreshed one carries its stat data
        let Some(&i) = sorted.get(at) else { break };
        if e.path != items[i].path {
            return Err(Refusal::Failed(
                "git update-index changed the entries".into(),
            ));
        }
        dirty[i] = e.stat.mtime == (0, 0) && e.stat.ctime == (0, 0);
        at += 1;
    }
    Ok(dirty)
}

/// Wait for `child` at most `limit`; past it the child is killed and the wait fails.
fn wait_bounded(
    child: &mut std::process::Child,
    limit: std::time::Duration,
) -> Result<std::process::ExitStatus, Refusal> {
    let started = std::time::Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return Ok(status),
            Ok(None) if started.elapsed() < limit => {
                std::thread::sleep(std::time::Duration::from_millis(5))
            }
            Ok(None) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(Refusal::Failed(format!(
                    "git update-index --refresh timed out after {} ms",
                    limit.as_millis()
                )));
            }
            Err(e) => return Err(Refusal::Failed(format!("git update-index: {e}"))),
        }
    }
}

/// A private index file, removed (with Git's lock file) when dropped.
struct PrivateIndex {
    path: PathBuf,
    _dir: tempfile::TempDir,
}

impl PrivateIndex {
    fn create(bytes: &[u8]) -> Result<PrivateIndex, Refusal> {
        use std::io::Write;
        use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
        let dir = tempfile::Builder::new()
            .prefix("xm-refresh-")
            .permissions(fs::Permissions::from_mode(0o700))
            .tempdir()
            .map_err(|e| Refusal::Failed(format!("private index directory: {e}")))?;
        let path = dir.path().join("index");
        let mut f = fs::File::options()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
            .map_err(|e| Refusal::Failed(format!("private index {}: {e}", path.display())))?;
        f.write_all(bytes)
            .map_err(|e| Refusal::Failed(format!("private index: {e}")))?;
        Ok(PrivateIndex { path, _dir: dir })
    }
}

impl Drop for PrivateIndex {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
        let mut lock = self.path.clone().into_os_string();
        lock.push(".lock");
        let _ = fs::remove_file(lock);
    }
}

/// A tracked path whose worktree differs from HEAD, as `git diff --raw HEAD` reports
/// it: HEAD's mode (0 when HEAD has no such path) and id.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HeadChange {
    /// Relative to the opened directory.
    pub path: Vec<u8>,
    pub mode: u32,
    pub oid: Option<Oid>,
}

/// Whether `path`'s worktree side differs from HEAD's `head`, from its status code and
/// the worktree's file type (`kind`, from `lstat`; None when the path is gone), or None
/// when only its content can tell: a worktree file Git calls modified (`git status` may
/// say so from a size change alone, while `git diff HEAD` compares the converted
/// content), a type change that matches HEAD's type again, and an unmerged path HEAD
/// has.
fn differs_from_head(t: &Tracked, kind: impl Fn() -> Option<u32>) -> Option<bool> {
    let y = t.xy[1];
    match (t.head, is_unmerged(t.xy)) {
        (None, true) => Some(kind().is_some()),
        (Some(_), true) => None,
        (None, false) => Some(y != b'D'),
        (Some((mode, _)), false) if mode == MODE_GITLINK => Some(true),
        (Some(_), false) if y == b'M' => None,
        (Some((mode, _)), false) if y == b'T' && kind() == Some(mode & 0o170000) => None,
        // the index differs from HEAD (and the worktree from neither or as a whole),
        // or the worktree path is gone or of another type than HEAD's
        (Some(_), false) => Some(true),
    }
}

/// `git diff --raw --no-renames --relative HEAD` from the index stream; None when HEAD
/// is unborn.
pub(crate) fn head_changes(repo: &Repo) -> Result<Option<Vec<HeadChange>>, Refusal> {
    let ix = repo.index()?;
    if repo.head_tree()?.is_none() {
        return Ok(None);
    }
    let mut changed: Vec<HeadChange> = Vec::new();
    let mut ask: Vec<(Vec<u8>, u32, Oid)> = Vec::new();
    let top = repo.toplevel.clone();
    let mut each = |t: Tracked| {
        let kind = || {
            fs::symlink_metadata(top.join(path_from_bytes(&t.path)))
                .ok()
                .map(|m| m.mode() & 0o170000)
        };
        match (differs_from_head(&t, kind), t.head) {
            (Some(true), head) => {
                let (mode, oid) = head.map_or((0, None), |(m, o)| (m, Some(o)));
                changed.push(HeadChange {
                    path: t.path,
                    mode,
                    oid,
                });
            }
            (None, Some((mode, oid))) => ask.push((t.path, mode, oid)),
            _ => {}
        }
        ControlFlow::Continue(())
    };
    let _ = track(repo, &ix, &mut each, None)?;
    // HEAD's blob has no recorded size: Git compares content, as `git diff HEAD`
    // compares a worktree file that differs from the index with HEAD's blob
    let items: Vec<Probe<'_>> = ask
        .iter()
        .map(|(p, m, o)| Probe {
            path: p,
            mode: *m,
            oid: *o,
            size: 0,
        })
        .collect();
    for ((path, mode, oid), dirty) in ask.iter().zip(refresh(repo, &items)?) {
        if dirty {
            changed.push(HeadChange {
                path: path.clone(),
                mode: *mode,
                oid: Some(*oid),
            });
        }
    }
    let prefix = repo.prefix.as_slice();
    let mut changed: Vec<HeadChange> = changed
        .into_iter()
        .filter(|c| c.path.starts_with(prefix))
        .map(|mut c| {
            c.path.drain(..prefix.len());
            c
        })
        .collect();
    changed.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(Some(changed))
}

#[cfg(test)]
mod tests {
    use super::{
        Outcome, PrivateIndex, Refusal, ensure_complete_submodule, submodule_dirty_with_head,
    };
    use crate::indexcache::GitRunError;
    use std::os::unix::fs::PermissionsExt;

    #[test]
    fn private_refresh_index_is_owner_only() {
        let file = PrivateIndex::create(b"private index").unwrap();
        let mode = std::fs::metadata(&file.path).unwrap().permissions().mode() & 0o777;
        let dir_mode = std::fs::metadata(file.path.parent().unwrap())
            .unwrap()
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(mode, 0o600);
        assert_eq!(dir_mode, 0o700);
    }

    #[test]
    fn private_refresh_directory_protects_git_replacement_and_lock_files() {
        let dir = tempfile::tempdir().unwrap();
        let repo = dir.path();
        let git = |args: &[&str]| {
            std::process::Command::new("git")
                .current_dir(repo)
                .args(args)
                .output()
                .unwrap()
        };
        assert!(git(&["init", "-q"]).status.success());
        assert!(
            git(&["config", "user.email", "test@example.com"])
                .status
                .success()
        );
        assert!(git(&["config", "user.name", "Test"]).status.success());
        std::fs::write(repo.join("tracked"), b"content").unwrap();
        assert!(git(&["add", "tracked"]).status.success());
        assert!(git(&["commit", "-qm", "tracked"]).status.success());
        let source_index = std::fs::read(repo.join(".git/index")).unwrap();
        std::fs::write(repo.join("tracked"), b"content").unwrap();
        let private = PrivateIndex::create(&source_index).unwrap();
        let index_dir = private.path.parent().unwrap().to_owned();
        let lock_path = private.path.with_file_name("index.lock");
        let status = std::process::Command::new("sh")
            .args(["-c", "umask 022; exec git update-index -q --refresh"])
            .current_dir(repo)
            .env("GIT_INDEX_FILE", &private.path)
            .status()
            .unwrap();
        assert!(status.success());
        assert_eq!(
            std::fs::metadata(&index_dir).unwrap().permissions().mode() & 0o777,
            0o700
        );
        for entry in std::fs::read_dir(&index_dir).unwrap() {
            let entry = entry.unwrap();
            assert_eq!(
                entry.metadata().unwrap().permissions().mode() & 0o777,
                0o644
            );
        }
        assert!(private.path.exists());
        assert!(!lock_path.exists());
        drop(private);
        assert!(!index_dir.exists());
    }

    #[test]
    fn incomplete_nested_walk_refuses_a_clean_submodule() {
        let outcome = Outcome {
            hidden: Vec::new(),
            untracked_incomplete: Some("walk exceeded its bound".into()),
        };
        let refusal =
            ensure_complete_submodule(std::path::Path::new("child"), &outcome).unwrap_err();
        assert!(matches!(refusal, Refusal::Failed(message)
            if message.contains("child") && message.contains("walk exceeded its bound")));
    }

    #[test]
    fn submodule_head_execution_error_is_a_refusal() {
        let dir = tempfile::tempdir().unwrap();
        let git_dir = dir.path().join(".git");
        std::fs::create_dir_all(git_dir.join("objects")).unwrap();
        std::fs::create_dir_all(git_dir.join("refs")).unwrap();
        std::fs::write(git_dir.join("HEAD"), b"ref: refs/heads/main\n").unwrap();
        let oid = super::Oid::parse_hex(&"0".repeat(40)).unwrap();
        let refusal = submodule_dirty_with_head(dir.path(), &oid, |_| {
            Err(GitRunError::Spawn("shimmed spawn failure".into()))
        })
        .unwrap_err();
        assert!(matches!(refusal, Refusal::Failed(message)
            if message.contains("submodule") && message.contains("rev-parse")));
    }
}
