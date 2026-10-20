//! Untracked discovery without Git (WS-RT18, critique C7): a streamed walk of the
//! worktree in Git's path order, merged with an index stream for membership, applying
//! Git's ignore rules (`gitignore(5)`): `.gitignore` per directory (deeper files win),
//! then `$GIT_DIR/info/exclude`, then `core.excludesFile`; within a file the last
//! matching line wins, and nothing under an ignored directory is re-included.
//!
//! It reports what `git status --untracked-files=all` reports as untracked: each
//! untracked file or symlink, a nested repository as `dir/`, never an empty directory.
//! With `list_ignored` it also reports the directories `--ignored=matching` lists as
//! ignored (`dir/`, not descended; individually ignored files are not reported).
//!
//! Memory is the directory stack: the sorted names of each directory on the current
//! path (`MAX_HELD_NAME_BYTES` in all), the ignore rules of its `.gitignore` files
//! (`MAX_HELD_RULES`), one index entry and the gitlinks passed so far. A walk that
//! cannot finish within those bounds, the Git timeout or an unreadable directory
//! returns the reason instead of a partial answer that reads as complete.

use std::cmp::Ordering;
use std::fs;
use std::io::Read;
use std::ops::ControlFlow;
use std::path::Path;
use std::time::Instant;

use super::Refusal;
use super::reader::{Entries, Index};
use super::status::Repo;
use crate::index::ignore::{Glob, Line, parse_line};
use crate::indexcache::{StatusEntry, git_timeout, path_from_bytes};

/// Bytes held for the children of the directories on the current path: each name plus
/// `CHILD_OVERHEAD`.
pub const MAX_HELD_NAME_BYTES: usize = 8 << 20;
/// What one child costs besides its name: the vector slot (a `Vec` header and its
/// kind) and the allocator's rounding of the name.
const CHILD_OVERHEAD: usize = std::mem::size_of::<Child>() + 16;
/// Ignore rules held across the ignore files that apply to the current directory.
pub const MAX_HELD_RULES: usize = 8192;
/// The largest ignore file read; a larger one leaves the walk incomplete.
pub const MAX_IGNORE_FILE_BYTES: u64 = 1 << 20;
/// Gitlinks remembered for membership; past it the walk is incomplete.
pub const MAX_GITLINKS: usize = 65_536;
/// The largest `.git` file read for a `gitdir:` pointer (Git's own limit is 1 MiB).
const MAX_GITFILE_BYTES: u64 = 1 << 20;

const MODE_GITLINK: u32 = 0o160000;

/// Whether `dir` is the worktree of a repository of its own, as Git decides it when it
/// meets a directory that is not in the index: `dir/.git` is a Git dir, or a file whose
/// `gitdir:` names one. A `.git` file that cannot be opened or read counts as one, as
/// in Git (the safe side: nothing inside is listed).
pub(crate) fn is_repository(dir: &Path) -> bool {
    let dot = dir.join(".git");
    crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
    let Ok(meta) = fs::symlink_metadata(&dot) else {
        return false;
    };
    if !meta.is_file() {
        return is_git_dir(&dot);
    }
    if meta.len() > MAX_GITFILE_BYTES {
        return false;
    }
    let mut text = Vec::new();
    let read = fs::File::open(&dot).and_then(|f| f.take(MAX_GITFILE_BYTES).read_to_end(&mut text));
    if read.is_err() {
        return true;
    }
    match text.strip_prefix(b"gitdir:") {
        Some(target) => {
            let target = path_from_bytes(target.trim_ascii());
            is_git_dir(&dir.join(target))
        }
        None => false,
    }
}

/// Git's test for a Git dir: a valid `HEAD`, and `objects` and `refs` in its common dir.
fn is_git_dir(dir: &Path) -> bool {
    if !valid_head(&dir.join("HEAD")) {
        return false;
    }
    let mut common = dir.to_path_buf();
    if let Ok(text) = fs::read(dir.join("commondir")) {
        let rel = path_from_bytes(text.trim_ascii());
        common = dir.join(rel);
    }
    common.join("objects").is_dir() && common.join("refs").is_dir()
}

/// A `HEAD` Git accepts: a symlink into `refs/`, `ref: refs/...`, or an object id.
fn valid_head(path: &Path) -> bool {
    match fs::symlink_metadata(path) {
        Ok(m) if m.file_type().is_symlink() => fs::read_link(path)
            .is_ok_and(|t| t.as_os_str().as_encoded_bytes().starts_with(b"refs/")),
        Ok(m) if m.is_file() => {
            let mut text = Vec::new();
            let ok = fs::File::open(path)
                .and_then(|f| f.take(4096).read_to_end(&mut text))
                .is_ok();
            let t = text.trim_ascii_end();
            match t.strip_prefix(b"ref:") {
                Some(r) => ok && r.trim_ascii_start().starts_with(b"refs/"),
                None => ok && matches!(t.len(), 40 | 64) && t.iter().all(u8::is_ascii_hexdigit),
            }
        }
        _ => false,
    }
}

/// One ignore rule, with the directory of its file (`""` or `dir/`).
struct Rule {
    base: Vec<u8>,
    glob: Glob,
    negate: bool,
    dir_only: bool,
    anchored: bool,
}

impl Rule {
    /// Whether this rule matches `path` (top-level relative, no trailing `/`).
    fn matches(&self, path: &[u8], is_dir: bool) -> bool {
        if self.dir_only && !is_dir {
            return false;
        }
        let Some(rel) = path.strip_prefix(self.base.as_slice()) else {
            return false;
        };
        let text = match self.anchored {
            true => rel,
            false => rel.rsplit(|b| *b == b'/').next().unwrap_or(rel),
        };
        self.glob.matches(text)
    }
}

/// The child kinds the walk tells apart (Git's `d_type`).
#[derive(Clone, Copy, PartialEq, Eq)]
enum Kind {
    Dir,
    File,
}

/// A child of a directory: its key (the name, plus `/` for a directory) and kind.
type Child = (Vec<u8>, Kind);

/// A directory being walked: its children, sorted by their key (the name, plus `/`
/// for a directory), and where its rules start.
struct Frame {
    /// Top-level relative, `""` or `dir/`.
    path: Vec<u8>,
    children: Vec<Child>,
    next: usize,
    rules_from: usize,
    excluded: bool,
    held: usize,
}

/// What the walk compares worktree paths with: the index, one entry at a time.
///
/// The comparison folds case when the repository's `core.ignorecase` is set, the
/// way Git's own index lookup does (WS-RT18b): on a case-insensitive volume the
/// worktree's path may differ in case from the index's, and a byte-wise comparison
/// would call the file deleted and untracked at once.
struct Cursor<'a> {
    entries: Entries<'a>,
    cur: Option<Vec<u8>>,
    gitlinks: Vec<Vec<u8>>,
    started: bool,
    ignore_case: bool,
}

impl Cursor<'_> {
    /// Move past every entry before `key`.
    fn advance(&mut self, key: &[u8]) -> Result<(), Refusal> {
        loop {
            if !self.started {
                self.started = true;
                self.load()?;
            }
            match &self.cur {
                Some(p)
                    if crate::gitindex::cmp_paths(p, key, self.ignore_case) == Ordering::Less =>
                {
                    self.load()?
                }
                _ => return Ok(()),
            }
        }
    }

    fn load(&mut self) -> Result<(), Refusal> {
        self.cur = None;
        if let Some(e) = self.entries.read_next()? {
            if e.mode == MODE_GITLINK && self.gitlinks.last() != Some(&e.path) {
                if self.gitlinks.len() >= MAX_GITLINKS {
                    return Err(Refusal::Failed(format!(
                        "more than {MAX_GITLINKS} gitlinks"
                    )));
                }
                self.gitlinks.push(e.path.clone());
            }
            self.cur = Some(e.path.clone());
        }
        Ok(())
    }

    /// The index holds the file `path` (after `advance(path)`).
    fn holds_file(&self, path: &[u8]) -> bool {
        match &self.cur {
            Some(p) => crate::gitindex::cmp_paths(p, path, self.ignore_case) == Ordering::Equal,
            None => false,
        }
    }

    /// The index holds paths under `dir` (`a/b/`, after `advance(dir)`).
    fn holds_under(&self, dir: &[u8]) -> bool {
        self.cur
            .as_deref()
            .is_some_and(|p| crate::gitindex::path_starts_with(p, dir, self.ignore_case))
    }

    /// `dir` (`a/b/`) is a gitlink of the index (after `advance(dir)`).
    fn is_gitlink(&self, dir: &[u8]) -> bool {
        let path = &dir[..dir.len() - 1];
        self.gitlinks
            .binary_search_by(|g| g.as_slice().cmp(path))
            .is_ok()
    }
}

struct Walk<'a> {
    repo: &'a Repo,
    /// Only paths under this directory (`""` or `dir/`) are reported.
    prefix: &'a [u8],
    list_ignored: bool,
    rules: Vec<Rule>,
    held: usize,
    started: Instant,
}

impl Walk<'_> {
    /// Load the ignore file at `path` with rules relative to `base`. A missing file
    /// adds nothing; one past the bounds is an error.
    fn load_rules(&mut self, path: &Path, base: &[u8], follow: bool) -> Result<(), String> {
        let meta = match follow {
            true => fs::metadata(path),
            false => fs::symlink_metadata(path),
        };
        let Ok(meta) = meta else {
            return Ok(());
        };
        if !meta.is_file() {
            return Ok(());
        }
        if meta.len() > MAX_IGNORE_FILE_BYTES {
            return Err(format!(
                "{} is past {MAX_IGNORE_FILE_BYTES} bytes",
                path.display()
            ));
        }
        let mut bytes = Vec::new();
        fs::File::open(path)
            .and_then(|f| f.take(MAX_IGNORE_FILE_BYTES).read_to_end(&mut bytes))
            .map_err(|e| format!("{}: {e}", path.display()))?;
        let text = String::from_utf8_lossy(&bytes);
        for line in text.lines() {
            match parse_line(line) {
                Line::NotARule => {}
                Line::TooLong => {
                    return Err(format!("{}: a pattern past the line bound", path.display()));
                }
                Line::Rule {
                    pattern,
                    negate,
                    dir_only,
                    anchored,
                } => {
                    if self.rules.len() >= MAX_HELD_RULES {
                        return Err(format!("more than {MAX_HELD_RULES} ignore rules apply"));
                    }
                    self.rules.push(Rule {
                        base: base.to_vec(),
                        glob: Glob::compile(pattern.as_bytes()),
                        negate,
                        dir_only,
                        anchored,
                    });
                }
            }
        }
        Ok(())
    }

    /// Whether `path` (no trailing `/`) is ignored by the rules in force.
    fn excluded(&self, path: &[u8], is_dir: bool) -> bool {
        self.rules
            .iter()
            .rev()
            .find(|r| r.matches(path, is_dir))
            .is_some_and(|r| !r.negate)
    }

    /// The sorted children of directory `path` (`""` or `dir/`), without `.git`, and
    /// the bytes their keys hold.
    fn read_children(&mut self, path: &[u8]) -> Result<(Vec<Child>, usize), String> {
        let dir = self.repo.toplevel.join(path_from_bytes(path));
        let read = match fs::read_dir(&dir) {
            Ok(r) => r,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok((Vec::new(), 0)),
            Err(e) => return Err(format!("{}: {e}", dir.display())),
        };
        let mut children = Vec::new();
        let mut held = 0usize;
        for entry in read {
            let entry = entry.map_err(|e| format!("{}: {e}", dir.display()))?;
            let name = entry.file_name();
            let name = name.as_encoded_bytes();
            if name == b".git" {
                continue;
            }
            let kind = match entry.file_type() {
                Ok(t) if t.is_dir() => Kind::Dir,
                Ok(t) if t.is_file() || t.is_symlink() => Kind::File,
                Ok(_) => continue,
                Err(e) => return Err(format!("{}: {e}", entry.path().display())),
            };
            let mut key = name.to_vec();
            if kind == Kind::Dir {
                key.push(b'/');
            }
            held += key.len() + CHILD_OVERHEAD;
            if self.held + held > MAX_HELD_NAME_BYTES {
                return Err(format!(
                    "directory listings past {MAX_HELD_NAME_BYTES} bytes held"
                ));
            }
            children.push((key, kind));
        }
        children.sort_unstable_by(|a, b| a.0.cmp(&b.0));
        crate::passcount::note(crate::passcount::Pass::FilesStatted, children.len() as u64);
        self.held += held;
        Ok((children, held))
    }

    /// Push directory `path` (`""` or `dir/`) on the stack: its `.gitignore` (unless
    /// it is excluded, as Git reads none there) and its sorted children.
    fn enter(
        &mut self,
        stack: &mut Vec<Frame>,
        path: Vec<u8>,
        excluded: bool,
    ) -> Result<(), String> {
        let rules_from = self.rules.len();
        if !excluded {
            let file = self
                .repo
                .toplevel
                .join(path_from_bytes(&path))
                .join(".gitignore");
            self.load_rules(&file, &path, false)?;
        }
        let (children, held) = self.read_children(&path)?;
        stack.push(Frame {
            path,
            children,
            next: 0,
            rules_from,
            excluded,
            held,
        });
        Ok(())
    }

    /// Whether directory `path` (`dir/`) is reported or entered at all: it is under
    /// the prefix, or leads to it.
    fn in_scope(&self, path: &[u8]) -> bool {
        path.starts_with(self.prefix) || self.prefix.starts_with(path)
    }
}

/// Walk the worktree of `repo` below `prefix` (`""` or `dir/`, top-level relative) and
/// hand each untracked path (and with `list_ignored` each ignored directory) to `each`,
/// in Git's order. Returns the reason the walk did not finish, if it did not.
pub(crate) fn untracked(
    repo: &Repo,
    ix: &Index,
    prefix: &[u8],
    list_ignored: bool,
    each: &mut dyn FnMut(StatusEntry) -> ControlFlow<()>,
) -> Result<Option<String>, Refusal> {
    let mut w = Walk {
        repo,
        prefix,
        list_ignored,
        rules: Vec::new(),
        held: 0,
        started: Instant::now(),
    };
    let mut cursor = Cursor {
        entries: ix.entries()?,
        cur: None,
        gitlinks: Vec::new(),
        started: false,
        ignore_case: repo.ignore_case,
    };
    let base_files = [
        (repo.excludes_file.clone(), true),
        (Some(repo.common_dir.join("info/exclude")), true),
    ];
    for (file, follow) in base_files {
        if let Some(file) = file
            && let Err(why) = w.load_rules(&file, b"", follow)
        {
            return Ok(Some(why));
        }
    }
    let limit = git_timeout();
    let mut stack: Vec<Frame> = Vec::new();
    if let Err(why) = w.enter(&mut stack, Vec::new(), false) {
        return Ok(Some(why));
    }
    while let Some(frame) = stack.last_mut() {
        if w.started.elapsed() >= limit {
            return Ok(Some(format!(
                "past the Git timeout ({} ms)",
                limit.as_millis()
            )));
        }
        let Some((key, kind)) = frame.children.get(frame.next) else {
            let done = stack.pop().expect("a frame");
            w.rules.truncate(done.rules_from);
            w.held -= done.held;
            continue;
        };
        frame.next += 1;
        let parent_excluded = frame.excluded;
        let path = [frame.path.as_slice(), key].concat();
        let kind = *kind;
        if !w.in_scope(&path) {
            continue;
        }
        cursor.advance(&path)?;
        let bare = path.strip_suffix(b"/").unwrap_or(&path);
        let emit = |xy: [u8; 2], path: Vec<u8>| StatusEntry {
            xy,
            path,
            orig_path: None,
        };
        let below_prefix = path.starts_with(prefix);
        let flow = match kind {
            Kind::File => {
                if cursor.holds_file(&path)
                    || parent_excluded
                    || w.excluded(bare, false)
                    || !below_prefix
                {
                    continue;
                }
                crate::passcount::note(crate::passcount::Pass::FilesListed, 1);
                each(emit(*b"??", path))
            }
            Kind::Dir => {
                let excluded = parent_excluded || w.excluded(bare, true);
                if excluded && !w.list_ignored {
                    continue;
                }
                let tracked_under = cursor.holds_under(&path);
                let report =
                    |xy: [u8; 2],
                     path: Vec<u8>,
                     each: &mut dyn FnMut(StatusEntry) -> ControlFlow<()>| {
                        match below_prefix {
                            true => {
                                crate::passcount::note(crate::passcount::Pass::FilesListed, 1);
                                each(emit(xy, path))
                            }
                            false => ControlFlow::Continue(()),
                        }
                    };
                match (tracked_under, cursor.is_gitlink(&path)) {
                    (true, _) => match w.enter(&mut stack, path, excluded) {
                        Ok(()) => ControlFlow::Continue(()),
                        Err(why) => return Ok(Some(why)),
                    },
                    (false, true) => ControlFlow::Continue(()),
                    (false, false) => {
                        let full = repo.toplevel.join(path_from_bytes(bare));
                        match (is_repository(&full), excluded) {
                            (true, false) => report(*b"??", path, &mut *each),
                            (_, true) => report(*b"!!", path, &mut *each),
                            (false, false) => match w.enter(&mut stack, path, false) {
                                Ok(()) => ControlFlow::Continue(()),
                                Err(why) => return Ok(Some(why)),
                            },
                        }
                    }
                }
            }
        };
        if flow.is_break() {
            return Ok(None);
        }
    }
    Ok(None)
}
