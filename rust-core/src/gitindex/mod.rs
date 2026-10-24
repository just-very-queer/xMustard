//! Git's index and dirty view without a Git child that loads the index (WS-RT18).
//!
//! Every Git command that reads `.git/index` holds all of it: 285-337 MiB at 1M tracked
//! paths whatever its pathspec, 6-21 MiB already at 1,400-50,000 entries. The source
//! identity, change tracking and the index scan read the index here instead, streamed
//! (`reader`), decide Git's dirty view in-process (`status`), find untracked paths with
//! a walk that applies Git's ignore rules (`walk`), and read HEAD's trees through one
//! `git cat-file --batch` child (`objects`), which reads objects only.
//!
//! One switch, `XMUSTARD_GIT_INDEX_READER`, picks the source for every caller:
//! - `auto` (the default): the reader at every size. When it refuses an index (a sparse
//!   index, an unknown required extension, a damaged file), Git answers instead only
//!   while the predicted cost of an index-loading Git child is within `GIT_LINE_BYTES`
//!   (`falls_back`); above it the refusal stands as a named limitation.
//! - `reader`: the reader only, never Git.
//! - `git`: Git's own commands (`status`, `ls-files`, `diff`), as before this module.

pub mod objects;
pub mod reader;
pub mod status;
pub mod walk;

use std::cmp::Ordering;
use std::fs::File;
use std::io::Read;
use std::ops::ControlFlow;
use std::path::Path;

use crate::indexcache::StatusEntry;

pub use reader::Oid;
pub use status::HeadChange;

/// Compares two index paths in Git's byte-sorted index order.
pub(crate) fn cmp_paths(a: &[u8], b: &[u8]) -> Ordering {
    a.cmp(b)
}

/// Why the reader gave no answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refusal {
    /// A layout the reader does not follow: a sparse index, an unknown required
    /// extension, or an unsupported index version or object format.
    Unsupported(String),
    /// The index or the worktree could not be read: a checksum mismatch, a truncated
    /// file, an I/O error, a Git child that failed.
    Failed(String),
    /// A refusal while reading a nested repository; fallback from the parent could load
    /// the nested repository's index without accounting for its cost.
    Submodule(String),
}

impl Refusal {
    /// The identity limitation that names this refusal.
    pub fn reason(&self) -> &'static str {
        match self {
            Refusal::Unsupported(_) => "git_index_unsupported",
            Refusal::Failed(_) | Refusal::Submodule(_) => "git_index_failed",
        }
    }
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refusal::Unsupported(m) => write!(f, "Git index layout not supported: {m}"),
            Refusal::Failed(m) | Refusal::Submodule(m) => f.write_str(m),
        }
    }
}

/// Which source answers (see the module docs).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Setting {
    Auto,
    Reader,
    Git,
}

/// The environment variable that picks the source.
pub const SETTING_ENV: &str = "XMUSTARD_GIT_INDEX_READER";

impl Setting {
    /// `XMUSTARD_GIT_INDEX_READER`: `reader`, `git`, else `auto`.
    pub fn from_env() -> Setting {
        match std::env::var(SETTING_ENV).as_deref() {
            Ok("reader") => Setting::Reader,
            Ok("git") => Setting::Git,
            _ => Setting::Auto,
        }
    }
}

/// The Git line: the most an index-loading Git child may cost (critique C3, measured
/// object-only classes at 6.4-7.8 MiB).
pub const GIT_LINE_BYTES: u64 = 8 << 20;

/// A Git child that loads an index of `entries` entries, predicted from the measured
/// 5,116 KiB plus 346 bytes per entry (critique C3, `critique-probe` lines 7-16).
pub fn predicted_git_bytes(entries: u64) -> u64 {
    (5_116 << 10) + 346 * entries
}

/// Entries in the header of the index file `path` (0 when there is none), read from
/// its first 12 bytes; None when the header cannot be read.
fn header_entries(path: &Path) -> Option<u64> {
    let mut head = [0u8; 12];
    match File::open(path) {
        Ok(mut f) => f.read_exact(&mut head).ok()?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Some(0),
        Err(_) => return None,
    }
    (&head[..4] == b"DIRC")
        .then(|| u32::from_be_bytes([head[8], head[9], head[10], head[11]]) as u64)
}

/// At least as many entries as an index-loading Git child holds for the Git dir
/// `git_dir`: its index's own entries plus the largest `sharedindex.*` file's (a split
/// index loads one shared index; an unused one only makes the bound larger).
pub fn loaded_entries(git_dir: &Path) -> Option<u64> {
    let own = header_entries(&git_dir.join("index"))?;
    let mut shared = 0u64;
    for e in std::fs::read_dir(git_dir).ok()?.filter_map(Result::ok) {
        if e.file_name()
            .as_encoded_bytes()
            .starts_with(b"sharedindex.")
        {
            shared = shared.max(header_entries(&e.path())?);
        }
    }
    Some(own + shared)
}

/// Whether a reader refusal goes to Git: only under `auto`, and only while an
/// index-loading Git child on this Git dir is predicted within the Git line.
pub fn falls_back(setting: Setting, git_dir: &Path) -> bool {
    setting == Setting::Auto
        && loaded_entries(git_dir).is_some_and(|n| predicted_git_bytes(n) <= GIT_LINE_BYTES)
}

/// A listing of the opened directory, as `git ls-files` run there gives it.
#[derive(Debug, Clone, Copy)]
pub enum Listing<'a> {
    /// `ls-files -s`: every index entry (an unmerged path once per stage) with its mode.
    Tracked,
    /// `ls-files -o --exclude-standard`: untracked paths not ignored (mode 0), a nested
    /// repository as `dir/`.
    Untracked,
    /// The tracked (or untracked) paths whose last component is `name`, as
    /// `ls-files [-o --exclude-standard] --glob-pathspecs -- '**/<name>'` lists them.
    Named { name: &'a str, untracked: bool },
}

impl Listing<'_> {
    /// The `git` arguments that give this listing.
    fn git_args(&self) -> Vec<String> {
        let words: &[&str] = match self {
            Listing::Tracked => &["ls-files", "-s", "-z"],
            Listing::Untracked => &["ls-files", "-o", "--exclude-standard", "-z"],
            Listing::Named {
                untracked: false, ..
            } => &["--glob-pathspecs", "ls-files", "-z", "--"],
            Listing::Named {
                untracked: true, ..
            } => &[
                "--glob-pathspecs",
                "ls-files",
                "-o",
                "--exclude-standard",
                "-z",
                "--",
            ],
        };
        let mut args: Vec<String> = words.iter().map(|w| w.to_string()).collect();
        if let Listing::Named { name, .. } = self {
            args.push(format!("**/{name}"));
        }
        args
    }
}

fn basename(path: &[u8]) -> &[u8] {
    path.rsplit(|b| *b == b'/').next().unwrap_or(path)
}

/// `listing` of the directory `root` from `setting`'s source; `each` gets every path
/// (relative to `root`) and its index mode (0 for an untracked path). A reader that
/// fails after it has listed a path is an error, never a second listing.
pub fn list_using(
    root: &Path,
    setting: Setting,
    listing: Listing<'_>,
    each: &mut dyn FnMut(&[u8], u32),
) -> Result<(), Refusal> {
    if setting == Setting::Git {
        return list_git(root, listing, each);
    }
    let repo = status::Repo::open(root)?;
    let mut listed = 0u64;
    let read = list_reader(&repo, listing, &mut |p, m| {
        listed += 1;
        each(p, m)
    });
    match read {
        Err(error)
            if !matches!(&error, Refusal::Submodule(_))
                && listed == 0
                && falls_back(setting, &repo.git_dir) =>
        {
            list_git(root, listing, each)
        }
        Err(e) if listed > 0 => Err(Refusal::Failed(format!("{e} (after {listed} paths)"))),
        other => other,
    }
}

fn list_reader(
    repo: &status::Repo,
    listing: Listing<'_>,
    each: &mut dyn FnMut(&[u8], u32),
) -> Result<(), Refusal> {
    let prefix = repo.prefix.as_slice();
    let ix = repo.index()?;
    let wanted = |path: &[u8]| match listing {
        Listing::Named { name, .. } => basename(path) == name.as_bytes(),
        _ => true,
    };
    match listing {
        Listing::Tracked
        | Listing::Named {
            untracked: false, ..
        } => {
            let mut entries = ix.entries()?;
            while let Some(e) = entries.read_next()? {
                if let Some(rel) = e.path.strip_prefix(prefix)
                    && wanted(rel)
                {
                    crate::passcount::note(crate::passcount::Pass::FilesListed, 1);
                    each(rel, e.mode);
                }
            }
            Ok(())
        }
        Listing::Untracked
        | Listing::Named {
            untracked: true, ..
        } => {
            let mut emit = |s: StatusEntry| {
                if let Some(rel) = s.path.strip_prefix(prefix)
                    && wanted(rel)
                {
                    each(rel, 0);
                }
                ControlFlow::Continue(())
            };
            match walk::untracked(repo, &ix, prefix, false, &mut emit)? {
                None => Ok(()),
                Some(why) => Err(Refusal::Failed(format!("untracked walk incomplete: {why}"))),
            }
        }
    }
}

fn list_git(
    root: &Path,
    listing: Listing<'_>,
    each: &mut dyn FnMut(&[u8], u32),
) -> Result<(), Refusal> {
    let args = listing.git_args();
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let tracked = matches!(listing, Listing::Tracked);
    crate::index::scan::stream_git_records(root, &args, |rec| match tracked {
        // "<mode> <blob> <stage>\t<path>"
        true => {
            let Some(tab) = rec.iter().position(|b| *b == b'\t') else {
                return;
            };
            let mode = std::str::from_utf8(&rec[..tab.min(6)])
                .ok()
                .and_then(|m| u32::from_str_radix(m, 8).ok())
                .unwrap_or(0);
            each(&rec[tab + 1..], mode);
        }
        false => each(rec, 0),
    })
    .map_err(Refusal::Failed)
}

/// `git diff --raw --no-renames --relative HEAD` for the directory `root` from
/// `setting`'s source: every tracked path whose worktree differs from HEAD, with
/// HEAD's mode and id. None when HEAD is unborn.
pub fn head_changes_using(
    root: &Path,
    setting: Setting,
) -> Result<Option<Vec<HeadChange>>, Refusal> {
    if setting == Setting::Git {
        return head_changes_git(root);
    }
    let repo = status::Repo::open(root)?;
    match status::head_changes(&repo) {
        Err(error)
            if !matches!(&error, Refusal::Submodule(_)) && falls_back(setting, &repo.git_dir) =>
        {
            head_changes_git(root)
        }
        other => other,
    }
}

fn head_changes_git(root: &Path) -> Result<Option<Vec<HeadChange>>, Refusal> {
    let head = crate::indexcache::run_git_bounded(
        root,
        &["rev-parse", "-q", "--verify", "HEAD^{commit}"],
        64 << 10,
        crate::indexcache::git_timeout(),
    );
    let head = match head {
        Ok(b) => String::from_utf8_lossy(&b).trim().to_string(),
        Err(crate::indexcache::GitRunError::Failed) => return Ok(None),
        Err(e) => return Err(Refusal::Failed(format!("git rev-parse HEAD: {e}"))),
    };
    let args = [
        "diff",
        "--raw",
        "-z",
        "--no-renames",
        "--relative",
        "--no-abbrev",
        "--no-ext-diff",
        "--no-color",
        "--ignore-submodules=none",
        head.as_str(),
        "--",
    ];
    let malformed = || Refusal::Failed("git diff: malformed --raw record".into());
    let mut out: Vec<HeadChange> = Vec::new();
    let mut meta: Option<(u32, Option<Oid>)> = None;
    let mut bad = false;
    crate::index::scan::stream_git_records(root, &args, |rec| match meta.take() {
        None => {
            // ":srcmode dstmode srcoid dstoid status"
            let text = String::from_utf8_lossy(rec);
            let parts: Vec<&str> = text.trim_start_matches(':').split(' ').collect();
            match parts[..] {
                [mode, _, oid, _, _] => {
                    let mode = u32::from_str_radix(mode, 8).unwrap_or(0);
                    let oid = Oid::parse_hex(oid).filter(|o| o.as_bytes().iter().any(|b| *b != 0));
                    meta = Some((mode, oid));
                }
                _ => bad = true,
            }
        }
        Some((mode, oid)) => out.push(HeadChange {
            path: rec.to_vec(),
            mode,
            oid,
        }),
    })
    .map_err(Refusal::Failed)?;
    if bad || meta.is_some() {
        return Err(malformed());
    }
    Ok(Some(out))
}
