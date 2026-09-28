//! Hardened Git diffs and the change-scope vocabulary (WS-64, folded into WS-35 as its
//! first deliverable; PAR-IMP-06, PAR-REV-01).
//!
//! Every diff xMustard reads runs under one isolation policy, so what a diff says does
//! not depend on the user's or the repository's Git configuration:
//!
//! - the system, global and XDG configuration and the system attributes are ignored
//!   (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL`, `GIT_ATTR_NOSYSTEM`), and every `GIT_*`
//!   and `XMUSTARD_*` variable is removed from the child's environment (`GIT_DIR`,
//!   `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_EXTERNAL_DIFF`, `GIT_DIFF_OPTS`, tokens);
//! - hooks, the fsmonitor and the attributes file are pinned off with `-c`, and so are
//!   the options repository configuration could set that change the bytes of a diff:
//!   `diff.suppressBlankEmpty` and `core.bigFileThreshold` (which makes a large text
//!   file binary);
//! - every diff option is on the command line: the myers algorithm, the indent
//!   heuristic, inter-hunk context 0, no order file (`-O/dev/null`), no relative paths,
//!   no renames, fixed prefixes, the submodule format, no external diff, no text
//!   conversion and no color; revisions follow `--end-of-options`.
//!
//! Two modes share the policy. [`Mode::Review`] is the byte-stable `-U3 --binary` diff a
//! merge attestation digests (WS-57's `reviewDiffArgs`, which the Go copy in
//! `api-go/internal/gitdiff` keeps). [`Mode::Symbols`] is the `-U0` diff impact maps
//! onto symbols; it also ignores a CR at the end of a line, so a line-ending conversion
//! does not mark every symbol of a file changed. A review diff keeps CR changes: they are
//! changes a reviewer has to see. Both copies are tested against
//! `testdata/gitdiff_policy.golden`.
//!
//! Scopes (one vocabulary for impact and review; PAR-IMP-06 and PAR-REV-01 reconciled):
//!
//! | scope | old side | new side | untracked files |
//! |---|---|---|---|
//! | `unstaged` | the Git index | the working tree | added |
//! | `staged` | `HEAD` (empty tree when unborn) | the Git index | no |
//! | `all` (alias `workspace`) | `HEAD` (empty tree when unborn) | the working tree | added |
//! | `compare` | `base` | the working tree, or `head` when given (two-dot) | without `head` |
//! | `commit` | first parent of `head` (default `HEAD`; empty tree for a root commit) | `head` | no |
//! | `range` | `merge-base(base, head)` (three-dot; `head` defaults to `HEAD`) | `head` | no |
//!
//! A worktree scope runs the repository's own clean filters, as Git does when it
//! compares working-tree files with the index; commit-to-commit scopes compare blobs.
//! Untracked files are listed with the repository's ignore rules; the user's global
//! excludes file is not read (it is global configuration).

use std::borrow::Cow;
use std::io::{Read, Write};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use serde::Serialize;

/// The null device every pin points at (`/dev/null`, `NUL` on Windows).
pub const DEV_NULL: &str = if cfg!(windows) { "NUL" } else { "/dev/null" };

/// Prefixes of the environment variables no Git child inherits.
pub const SCRUBBED_ENV_PREFIXES: &[&str] = &["GIT_", "XMUSTARD_"];

/// Variables every Git child gets.
pub const ISOLATION_ENV: &[(&str, &str)] = &[
    ("GIT_CONFIG_NOSYSTEM", "1"),
    ("GIT_CONFIG_GLOBAL", "/dev/null"),
    ("GIT_ATTR_NOSYSTEM", "1"),
    ("GIT_TERMINAL_PROMPT", "0"),
    ("GIT_OPTIONAL_LOCKS", "0"),
    ("LC_ALL", "C"),
];

/// `-c` pins on every Git call.
pub const CONFIG_PINS: &[&str] = &[
    "core.hooksPath=/dev/null",
    "core.fsmonitor=false",
    "core.attributesFile=/dev/null",
    "core.bigFileThreshold=512m",
    "diff.suppressBlankEmpty=false",
];

/// Which diff a caller reads.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// Byte-stable `-U3 --binary --full-index`, for digests (merge attestations).
    Review,
    /// `-U0`, CR at end of line ignored, for mapping hunks onto symbols.
    Symbols,
}

/// The policy of one mode: its extra `-c` pins and its diff options.
pub struct ModePolicy {
    pub name: &'static str,
    pub config: &'static [&'static str],
    pub flags: &'static [&'static str],
}

const REVIEW: ModePolicy = ModePolicy {
    name: "review",
    // WS-57's digests keep Git's default path quoting
    config: &["core.quotePath=true"],
    flags: &[
        "--no-color",
        "--no-ext-diff",
        "--no-textconv",
        "--binary",
        "--full-index",
        "--no-renames",
        "--no-relative",
        "--diff-algorithm=myers",
        "--indent-heuristic",
        "--inter-hunk-context=0",
        "-U3",
        "--src-prefix=a/",
        "--dst-prefix=b/",
        "--ignore-submodules=none",
        "--submodule=short",
        "-O/dev/null",
    ],
};

const SYMBOLS: ModePolicy = ModePolicy {
    name: "symbols",
    // raw UTF-8 names; quoted ones (control characters, quotes) are decoded
    config: &["core.quotePath=false"],
    flags: &[
        "--no-color",
        "--no-ext-diff",
        "--no-textconv",
        "--no-renames",
        "--no-relative",
        "--diff-algorithm=myers",
        "--indent-heuristic",
        "--inter-hunk-context=0",
        "-U0",
        "--ignore-cr-at-eol",
        "--src-prefix=a/",
        "--dst-prefix=b/",
        "--ignore-submodules=none",
        "--submodule=short",
        "-O/dev/null",
    ],
};

impl Mode {
    pub fn policy(self) -> &'static ModePolicy {
        match self {
            Mode::Review => &REVIEW,
            Mode::Symbols => &SYMBOLS,
        }
    }
}

/// The policy as `testdata/gitdiff_policy.golden` lists it (the Go copy renders the same
/// lines for what it implements).
pub fn render_policy() -> String {
    let mut out = String::new();
    for p in SCRUBBED_ENV_PREFIXES {
        out.push_str(&format!("scrub\t{p}\n"));
    }
    for (k, v) in ISOLATION_ENV {
        out.push_str(&format!("env\t{k}={v}\n"));
    }
    for c in CONFIG_PINS {
        out.push_str(&format!("config\t{c}\n"));
    }
    for mode in [Mode::Review, Mode::Symbols] {
        let p = mode.policy();
        for c in p.config {
            out.push_str(&format!("{}.config\t{c}\n", p.name));
        }
        for f in p.flags {
            out.push_str(&format!("{}\t{f}\n", p.name));
        }
    }
    out
}

fn pinned(s: &str) -> Cow<'_, str> {
    if DEV_NULL == "/dev/null" {
        Cow::Borrowed(s)
    } else {
        Cow::Owned(s.replace("/dev/null", DEV_NULL))
    }
}

// ---------------------------------------------------------------------------
// Running Git
// ---------------------------------------------------------------------------

/// Default bound on each Git child of a diff.
pub const TIMEOUT: Duration = Duration::from_secs(60);

/// A Git child under the isolation policy, run in `dir`, with the `-c` pins of `mode`.
pub fn command(dir: &Path, mode: Mode, args: &[&str]) -> Command {
    let mut cmd = Command::new("git");
    cmd.current_dir(dir);
    for (k, _) in std::env::vars_os() {
        let scrub = k
            .to_str()
            .is_none_or(|k| SCRUBBED_ENV_PREFIXES.iter().any(|p| k.starts_with(p)));
        if scrub {
            cmd.env_remove(&k);
        }
    }
    for (k, v) in ISOLATION_ENV {
        cmd.env(k, pinned(v).as_ref());
    }
    for c in CONFIG_PINS.iter().chain(mode.policy().config) {
        cmd.arg("-c").arg(pinned(c).as_ref());
    }
    cmd.args(args);
    cmd
}

/// Run a Git command whose output is small (at most `cap` bytes): a revision, a count.
pub fn run_small(dir: &Path, args: &[&str], cap: usize) -> Result<Vec<u8>, String> {
    let mut out = Vec::new();
    let status = stream(dir, Mode::Symbols, args, None, TIMEOUT, |chunk| {
        if out.len() + chunk.len() > cap {
            return Err(format!("git {} printed more than {cap} bytes", args[0]));
        }
        out.extend_from_slice(chunk);
        Ok(())
    })?;
    match status {
        true => Ok(out),
        false => Err(format!("git {} failed", args[0])),
    }
}

/// Run a Git command and hand its stdout to `sink` as it arrives, within `timeout`
/// (the child is killed past it, or when `sink` fails). `input` is written to stdin.
/// Returns whether Git exited with success.
pub fn stream(
    dir: &Path,
    mode: Mode,
    args: &[&str],
    input: Option<Vec<u8>>,
    timeout: Duration,
    mut sink: impl FnMut(&[u8]) -> Result<(), String>,
) -> Result<bool, String> {
    let mut cmd = command(dir, mode, args);
    cmd.stdin(if input.is_some() {
        Stdio::piped()
    } else {
        Stdio::null()
    })
    .stdout(Stdio::piped())
    .stderr(Stdio::null());
    crate::indexcache::note_git_spawn();
    let mut child = cmd
        .spawn()
        .map_err(|e| format!("git could not start: {e}"))?;
    if let (Some(mut w), Some(input)) = (child.stdin.take(), input) {
        // its own writer, so a large request never deadlocks against the reader
        std::thread::spawn(move || {
            let _ = w.write_all(&input);
        });
    }
    let stdout = child.stdout.take().ok_or("git stdout was not piped")?;
    let (tx, rx) = mpsc::sync_channel::<Vec<u8>>(64);
    let reader = std::thread::spawn(move || {
        let mut stdout = stdout;
        let mut chunk = vec![0u8; 64 << 10];
        loop {
            match stdout.read(&mut chunk) {
                Ok(0) => break,
                Ok(n) => {
                    if tx.send(chunk[..n].to_vec()).is_err() {
                        break;
                    }
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(_) => break,
            }
        }
    });
    let deadline = Instant::now() + timeout;
    let outcome = loop {
        let left = deadline.saturating_duration_since(Instant::now());
        match rx.recv_timeout(left) {
            Ok(chunk) => {
                if let Err(e) = sink(&chunk) {
                    break Err(e);
                }
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => break Ok(()),
            Err(mpsc::RecvTimeoutError::Timeout) => {
                break Err(format!(
                    "git {} timed out after {} ms",
                    args[0],
                    timeout.as_millis()
                ));
            }
        }
    };
    finish(child, reader, rx, outcome)
}

fn finish(
    mut child: Child,
    reader: std::thread::JoinHandle<()>,
    rx: mpsc::Receiver<Vec<u8>>,
    outcome: Result<(), String>,
) -> Result<bool, String> {
    if outcome.is_err() {
        let _ = child.kill();
    }
    drop(rx); // a reader blocked on a full channel sees the receiver gone
    let status = child.wait();
    let _ = reader.join();
    outcome?;
    Ok(status.map(|s| s.success()).unwrap_or(false))
}

/// Split a byte stream into lines of at most `cap` bytes each (the rest of a longer
/// line is dropped; `\n` excluded), calling `f` for every complete line.
pub struct Lines<F: FnMut(&[u8]) -> Result<(), String>> {
    buf: Vec<u8>,
    overlong: bool,
    cap: usize,
    f: F,
}

impl<F: FnMut(&[u8]) -> Result<(), String>> Lines<F> {
    pub fn new(cap: usize, f: F) -> Self {
        Lines {
            buf: Vec::new(),
            overlong: false,
            cap,
            f,
        }
    }

    pub fn feed(&mut self, mut chunk: &[u8]) -> Result<(), String> {
        while let Some(i) = chunk.iter().position(|b| *b == b'\n') {
            self.take(&chunk[..i]);
            (self.f)(&self.buf)?;
            self.buf.clear();
            self.overlong = false;
            chunk = &chunk[i + 1..];
        }
        self.take(chunk);
        Ok(())
    }

    fn take(&mut self, part: &[u8]) {
        let room = self.cap.saturating_sub(self.buf.len());
        self.buf.extend_from_slice(&part[..part.len().min(room)]);
        self.overlong |= part.len() > room;
    }

    /// Deliver a last line without a newline.
    pub fn finish(mut self) -> Result<(), String> {
        if !self.buf.is_empty() {
            (self.f)(&self.buf)?;
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Revisions
// ---------------------------------------------------------------------------

/// Longest revision expression accepted.
pub const MAX_REF_LEN: usize = 256;

/// Refuse a revision that could be read as an option or smuggle a second argument,
/// before Git sees it. Checked in order; the first failure is the answer.
pub fn check_ref(reference: &str) -> Result<(), String> {
    if reference.is_empty() {
        return Err("a revision is empty".into());
    }
    if reference.len() > MAX_REF_LEN {
        return Err(format!("revision is longer than {MAX_REF_LEN} bytes"));
    }
    if reference.starts_with('-') {
        return Err(format!("revision {reference:?} starts with '-'"));
    }
    if reference
        .chars()
        .any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(format!(
            "revision {reference:?} contains whitespace or a control character"
        ));
    }
    Ok(())
}

fn is_object_id(s: &str) -> bool {
    matches!(s.len(), 40 | 64) && s.bytes().all(|b| b.is_ascii_hexdigit())
}

/// The commit `reference` names, as a full object id; an error when it names none.
pub fn resolve_commit(dir: &Path, reference: &str) -> Result<String, String> {
    check_ref(reference)?;
    let spec = format!("{reference}^{{commit}}");
    let out = run_small(
        dir,
        &[
            "rev-parse",
            "--verify",
            "--quiet",
            "--end-of-options",
            &spec,
        ],
        256,
    )
    .map_err(|_| format!("revision {reference:?} does not name a commit"))?;
    let id = String::from_utf8_lossy(&out).trim().to_string();
    if !is_object_id(&id) {
        return Err(format!("revision {reference:?} does not name a commit"));
    }
    Ok(id)
}

/// `HEAD`'s commit, or None in a repository without commits.
pub fn head_commit(dir: &Path) -> Option<String> {
    resolve_commit(dir, "HEAD").ok()
}

/// The empty tree's id in this repository's object format.
fn empty_tree(dir: &Path) -> Result<String, String> {
    let out = stream_small_input(dir, &["hash-object", "-t", "tree", "--stdin"], Vec::new())?;
    let id = String::from_utf8_lossy(&out).trim().to_string();
    if !is_object_id(&id) {
        return Err("git could not name the empty tree".into());
    }
    Ok(id)
}

fn stream_small_input(dir: &Path, args: &[&str], input: Vec<u8>) -> Result<Vec<u8>, String> {
    let mut out = Vec::new();
    let ok = stream(dir, Mode::Symbols, args, Some(input), TIMEOUT, |c| {
        if out.len() + c.len() > 4096 {
            return Err(format!("git {} printed too much", args[0]));
        }
        out.extend_from_slice(c);
        Ok(())
    })?;
    ok.then_some(out)
        .ok_or_else(|| format!("git {} failed", args[0]))
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Scope {
    Unstaged,
    Staged,
    All,
    Compare,
    Commit,
    Range,
}

/// Scope names; `workspace` is the review vocabulary's name for `all`.
const SCOPE_NAMES: &[(&str, Scope)] = &[
    ("unstaged", Scope::Unstaged),
    ("staged", Scope::Staged),
    ("all", Scope::All),
    ("workspace", Scope::All),
    ("compare", Scope::Compare),
    ("commit", Scope::Commit),
    ("range", Scope::Range),
];

/// The accepted scope names, for usage text and validation.
pub fn scope_names() -> impl Iterator<Item = &'static str> {
    SCOPE_NAMES.iter().map(|(n, _)| *n)
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Need {
    Refused,
    Optional,
    Required,
}

impl Scope {
    pub fn parse(name: &str) -> Option<Scope> {
        SCOPE_NAMES
            .iter()
            .find(|(n, _)| *n == name)
            .map(|(_, s)| *s)
    }

    pub fn as_str(self) -> &'static str {
        SCOPE_NAMES
            .iter()
            .find(|(_, s)| *s == self)
            .map_or("", |(n, _)| n)
    }

    /// Whether the scope takes `base` and `head`.
    fn needs(self) -> (Need, Need) {
        match self {
            Scope::Unstaged | Scope::Staged | Scope::All => (Need::Refused, Need::Refused),
            Scope::Compare | Scope::Range => (Need::Required, Need::Optional),
            Scope::Commit => (Need::Refused, Need::Optional),
        }
    }
}

/// One side of a diff.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Side {
    Empty,
    Index,
    Worktree,
    Commit(String),
}

impl Side {
    /// The `cat-file` object name of `path` on this side (None for the working tree and
    /// the empty tree).
    pub fn object(&self, path: &str) -> Option<String> {
        match self {
            Side::Index => Some(format!(":{path}")),
            Side::Commit(c) => Some(format!("{c}:{path}")),
            Side::Empty | Side::Worktree => None,
        }
    }
}

/// A scope with its revisions resolved: what is compared with what.
#[derive(Debug, Clone, Serialize)]
pub struct Resolved {
    pub scope: Scope,
    #[serde(skip)]
    pub old: Side,
    #[serde(skip)]
    pub new: Side,
    /// The resolved `base` (compare, range).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub base: Option<String>,
    /// The resolved `head`, or `HEAD` for a working-tree or index scope (None when the
    /// repository has no commits).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub head: Option<String>,
    /// `merge-base(base, head)` for a range.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub merge_base: Option<String>,
    /// Untracked files count as added.
    pub untracked: bool,
    #[serde(skip)]
    empty_tree: Option<String>,
}

/// Resolve `scope` with its optional `base` and `head` in `dir` (the checkout the diff
/// runs in). A missing required revision, a refused one, or one that names no commit
/// is an error.
pub fn resolve(
    dir: &Path,
    scope: Scope,
    base: Option<&str>,
    head: Option<&str>,
) -> Result<Resolved, String> {
    let (need_base, need_head) = scope.needs();
    for (name, need, value) in [("base", need_base, base), ("head", need_head, head)] {
        match (need, value) {
            (Need::Required, None) => {
                return Err(format!("scope {} needs {name}", scope.as_str()));
            }
            (Need::Refused, Some(_)) => {
                return Err(format!("scope {} takes no {name}", scope.as_str()));
            }
            _ => {}
        }
    }
    let base = base.map(|b| resolve_commit(dir, b)).transpose()?;
    let head_given = head.map(|h| resolve_commit(dir, h)).transpose()?;
    let current = || head_commit(dir);
    let commit_or_empty = |c: Option<String>| c.map_or(Side::Empty, Side::Commit);
    let mut r = Resolved {
        scope,
        old: Side::Empty,
        new: Side::Worktree,
        base: base.clone(),
        head: None,
        merge_base: None,
        untracked: false,
        empty_tree: None,
    };
    match scope {
        Scope::Unstaged => {
            (r.old, r.new, r.untracked, r.head) = (Side::Index, Side::Worktree, true, current());
        }
        Scope::Staged => {
            r.head = current();
            (r.old, r.new) = (commit_or_empty(r.head.clone()), Side::Index);
        }
        Scope::All => {
            r.head = current();
            (r.old, r.new, r.untracked) = (commit_or_empty(r.head.clone()), Side::Worktree, true);
        }
        Scope::Compare => {
            r.old = commit_or_empty(base);
            (r.new, r.untracked, r.head) = match head_given {
                Some(h) => (Side::Commit(h.clone()), false, Some(h)),
                None => (Side::Worktree, true, current()),
            };
        }
        Scope::Commit => {
            let h = match head_given {
                Some(h) => h,
                None => current().ok_or("the repository has no commits")?,
            };
            let parent = resolve_commit(dir, &format!("{h}^1")).ok();
            (r.old, r.new, r.head) = (commit_or_empty(parent), Side::Commit(h.clone()), Some(h));
        }
        Scope::Range => {
            let h = match head_given {
                Some(h) => h,
                None => current().ok_or("the repository has no commits")?,
            };
            let b = base.ok_or("scope range needs base")?;
            let out = run_small(dir, &["merge-base", &b, &h], 256)
                .map_err(|_| "base and head have no merge base".to_string())?;
            let mb = String::from_utf8_lossy(&out).trim().to_string();
            if !is_object_id(&mb) {
                return Err("base and head have no merge base".into());
            }
            r.merge_base = Some(mb.clone());
            (r.old, r.new, r.head) = (Side::Commit(mb), Side::Commit(h.clone()), Some(h));
        }
    }
    if r.old == Side::Empty {
        r.empty_tree = Some(empty_tree(dir)?);
    }
    Ok(r)
}

impl Resolved {
    /// The options and revisions of `git diff` for this comparison.
    fn diff_revisions(&self) -> Result<(Vec<String>, Vec<String>), String> {
        let empty = || {
            self.empty_tree
                .clone()
                .ok_or_else(|| "empty tree not resolved".to_string())
        };
        let c = |s: &str| s.to_string();
        Ok(match (&self.old, &self.new) {
            (Side::Index, Side::Worktree) => (vec![], vec![]),
            (Side::Commit(o), Side::Index) => (vec![c("--cached")], vec![o.clone()]),
            (Side::Empty, Side::Index) => (vec![c("--cached")], vec![empty()?]),
            (Side::Commit(o), Side::Worktree) => (vec![], vec![o.clone()]),
            (Side::Empty, Side::Worktree) => (vec![], vec![empty()?]),
            (Side::Commit(o), Side::Commit(n)) => (vec![], vec![o.clone(), n.clone()]),
            (Side::Empty, Side::Commit(n)) => (vec![], vec![empty()?, n.clone()]),
            (o, n) => return Err(format!("no diff compares {o:?} with {n:?}")),
        })
    }

    /// The full `git diff` argument list in `mode`.
    pub fn diff_args(&self, mode: Mode) -> Result<Vec<String>, String> {
        let (opts, revs) = self.diff_revisions()?;
        let mut args = vec!["diff".to_string()];
        args.extend(mode.policy().flags.iter().map(|f| pinned(f).into_owned()));
        args.extend(opts);
        args.push("--end-of-options".into());
        args.extend(revs);
        args.push("--".into());
        Ok(args)
    }
}

// ---------------------------------------------------------------------------
// The -U0 diff, parsed as it streams
// ---------------------------------------------------------------------------

/// How a file changed.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Change {
    Added,
    Modified,
    Deleted,
}

/// One hunk header: `@@ -old_start[,old_len] +new_start[,new_len] @@`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Hunk {
    pub old_start: u32,
    pub old_len: u32,
    pub new_start: u32,
    pub new_len: u32,
}

impl Hunk {
    /// Lines of the new side this hunk touches: its added lines, or for a pure
    /// deletion the gap between `new_start` and the next line.
    pub fn new_lines(&self) -> Touch {
        Touch::of(self.new_start, self.new_len)
    }

    pub fn old_lines(&self) -> Touch {
        Touch::of(self.old_start, self.old_len)
    }
}

/// What a hunk touches on one side.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Touch {
    /// Lines `first..=last`.
    Lines(u32, u32),
    /// A gap right after line `after` (0: before the first line).
    Gap(u32),
}

impl Touch {
    fn of(start: u32, len: u32) -> Touch {
        match len {
            0 => Touch::Gap(start),
            n => Touch::Lines(start, start + n - 1),
        }
    }

    /// Whether this touches a symbol spanning `first..=last`: an overlap of lines, or a
    /// gap strictly inside the span.
    pub fn hits(&self, first: u32, last: u32) -> bool {
        match *self {
            Touch::Lines(a, b) => a <= last && first <= b,
            Touch::Gap(after) => first <= after && after < last,
        }
    }
}

/// One file of a diff.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileDiff {
    /// Repository-relative path (the new path, or the old one when deleted).
    pub path: String,
    pub change: Change,
    pub binary: bool,
    pub hunks: Vec<Hunk>,
    /// More hunks than [`Limits::hunks_per_file`]: treat the whole file as changed.
    pub hunks_truncated: bool,
}

/// Bounds of one parse.
#[derive(Debug, Clone, Copy)]
pub struct Limits {
    /// Files kept (every file is still counted).
    pub files: usize,
    pub hunks_per_file: usize,
}

impl Default for Limits {
    fn default() -> Self {
        Limits {
            files: 1000,
            hunks_per_file: 2000,
        }
    }
}

/// A parsed diff: the files kept, and how many there were.
#[derive(Debug, Clone, Default)]
pub struct Parsed {
    pub files: Vec<FileDiff>,
    pub total: usize,
    /// Headers whose path could not be read.
    pub unreadable: usize,
}

/// Streaming parser of a `-U0` (or `-U3`) diff with `a/` and `b/` prefixes. Content
/// lines are counted off the hunk header, so a removed line that reads `-- x` is never
/// taken for a file header.
pub struct Parser {
    limits: Limits,
    out: Parsed,
    cur: Option<Pending>,
    old_left: u32,
    new_left: u32,
}

#[derive(Debug, Default)]
struct Pending {
    header: Option<String>,
    old: Option<Option<String>>,
    new: Option<Option<String>>,
    added: bool,
    deleted: bool,
    binary: bool,
    hunks: Vec<Hunk>,
    hunks_truncated: bool,
}

impl Parser {
    pub fn new(limits: Limits) -> Self {
        Parser {
            limits,
            out: Parsed::default(),
            cur: None,
            old_left: 0,
            new_left: 0,
        }
    }

    pub fn line(&mut self, line: &[u8]) {
        if self.old_left > 0 || self.new_left > 0 {
            match line.first() {
                Some(b'-') => self.old_left = self.old_left.saturating_sub(1),
                Some(b'+') => self.new_left = self.new_left.saturating_sub(1),
                Some(b' ') => {
                    self.old_left = self.old_left.saturating_sub(1);
                    self.new_left = self.new_left.saturating_sub(1);
                }
                _ => {} // `\ No newline at end of file`
            }
            return;
        }
        let Ok(text) = std::str::from_utf8(line) else {
            return;
        };
        if let Some(rest) = text.strip_prefix("diff --git ") {
            self.flush();
            self.cur = Some(Pending {
                header: header_path(rest),
                ..Default::default()
            });
            return;
        }
        let Some(cur) = self.cur.as_mut() else {
            return;
        };
        let Some((kind, rest)) = HEADER_LINES
            .iter()
            .find_map(|(prefix, kind)| text.strip_prefix(prefix).map(|rest| (*kind, rest)))
        else {
            return; // index, mode and similarity lines
        };
        match kind {
            HeaderLine::NewFile => cur.added = true,
            HeaderLine::Deleted => cur.deleted = true,
            HeaderLine::Old => cur.old = Some(side_path(rest, "a/")),
            HeaderLine::New => cur.new = Some(side_path(rest, "b/")),
            HeaderLine::Binary => cur.binary = true,
            HeaderLine::Hunk => {
                let Some(h) = parse_hunk(rest) else { return };
                (self.old_left, self.new_left) = (h.old_len, h.new_len);
                match cur.hunks.len() < self.limits.hunks_per_file {
                    true => cur.hunks.push(h),
                    false => cur.hunks_truncated = true,
                }
            }
        }
    }

    fn flush(&mut self) {
        let Some(p) = self.cur.take() else { return };
        self.out.total += 1;
        let new = p.new.clone().flatten();
        let old = p.old.clone().flatten();
        let change = match (
            p.added || (p.old == Some(None)),
            p.deleted || (p.new == Some(None)),
        ) {
            (true, _) => Change::Added,
            (_, true) => Change::Deleted,
            _ => Change::Modified,
        };
        let path = match change {
            Change::Deleted => old.or(p.header.clone()),
            _ => new.or(p.header.clone()),
        };
        let Some(path) = path else {
            self.out.unreadable += 1;
            return;
        };
        if self.out.files.len() < self.limits.files {
            self.out.files.push(FileDiff {
                path,
                change,
                binary: p.binary,
                hunks: p.hunks,
                hunks_truncated: p.hunks_truncated,
            });
        }
    }

    pub fn finish(mut self) -> Parsed {
        self.flush();
        self.out
    }
}

/// The lines of a file's header block the parser reads.
#[derive(Debug, Clone, Copy)]
enum HeaderLine {
    NewFile,
    Deleted,
    Old,
    New,
    Binary,
    Hunk,
}

const HEADER_LINES: &[(&str, HeaderLine)] = &[
    ("new file mode", HeaderLine::NewFile),
    ("deleted file mode", HeaderLine::Deleted),
    ("--- ", HeaderLine::Old),
    ("+++ ", HeaderLine::New),
    ("Binary files ", HeaderLine::Binary),
    ("GIT binary patch", HeaderLine::Binary),
    ("@@ ", HeaderLine::Hunk),
];

/// `@@ -a[,b] +c[,d] @@ ...`
fn parse_hunk(rest: &str) -> Option<Hunk> {
    let spec = rest.split(" @@").next()?;
    let mut parts = spec.split(' ');
    let old = parts.next()?.strip_prefix('-')?;
    let new = parts.next()?.strip_prefix('+')?;
    let range = |s: &str| -> Option<(u32, u32)> {
        match s.split_once(',') {
            Some((a, b)) => Some((a.parse().ok()?, b.parse().ok()?)),
            None => Some((s.parse().ok()?, 1)),
        }
    };
    let (old_start, old_len) = range(old)?;
    let (new_start, new_len) = range(new)?;
    Some(Hunk {
        old_start,
        old_len,
        new_start,
        new_len,
    })
}

/// The path of a `---`/`+++` line: None for `/dev/null`, the unquoted name without
/// its prefix otherwise. Git ends a name that contains a space with a tab.
fn side_path(raw: &str, prefix: &str) -> Option<String> {
    let raw = raw.strip_suffix('\t').unwrap_or(raw);
    if raw == "/dev/null" {
        return None;
    }
    let name = match raw.starts_with('"') {
        true => unquote(raw)?.0,
        false => raw.to_string(),
    };
    name.strip_prefix(prefix).map(str::to_string)
}

/// The path of a `diff --git a/P b/P` header (without renames both names are equal).
fn header_path(rest: &str) -> Option<String> {
    if rest.starts_with('"') {
        return unquote(rest)?.0.strip_prefix("a/").map(str::to_string);
    }
    let body = rest.strip_prefix("a/")?;
    // "P b/P": the name is the first half
    let n = body.len().checked_sub(3)? / 2;
    let (name, tail) = (body.get(..n)?, body.get(n..)?);
    (tail.strip_prefix(" b/")? == name).then(|| name.to_string())
}

/// Decode a C-quoted name (`"a/\303\274 x"`) as Git prints it; returns the name and
/// the bytes of `raw` consumed. None when it is not well formed or not UTF-8.
pub fn unquote(raw: &str) -> Option<(String, usize)> {
    let b = raw.as_bytes();
    if b.first() != Some(&b'"') {
        return None;
    }
    let mut out = Vec::new();
    let mut i = 1;
    while i < b.len() {
        match b[i] {
            b'"' => return String::from_utf8(out).ok().map(|s| (s, i + 1)),
            b'\\' => {
                let e = *b.get(i + 1)?;
                let simple = match e {
                    b'a' => Some(7),
                    b'b' => Some(8),
                    b't' => Some(b'\t'),
                    b'n' => Some(b'\n'),
                    b'v' => Some(11),
                    b'f' => Some(12),
                    b'r' => Some(b'\r'),
                    b'"' => Some(b'"'),
                    b'\\' => Some(b'\\'),
                    _ => None,
                };
                match simple {
                    Some(c) => {
                        out.push(c);
                        i += 2;
                    }
                    None => {
                        let oct = raw.get(i + 1..i + 4)?;
                        out.push(u8::from_str_radix(oct, 8).ok()?);
                        i += 4;
                    }
                }
            }
            c => {
                out.push(c);
                i += 1;
            }
        }
    }
    None
}

/// Longest line the diff parser keeps (content lines only need their first byte).
const LINE_CAP: usize = 16 << 10;

/// Run the `mode` diff of `r` in `dir` and parse it.
pub fn diff(dir: &Path, r: &Resolved, mode: Mode, limits: Limits) -> Result<Parsed, String> {
    let args = r.diff_args(mode)?;
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let mut parser = Parser::new(limits);
    let ok = {
        let mut lines = Lines::new(LINE_CAP, |l| {
            parser.line(l);
            Ok(())
        });
        let ok = stream(dir, mode, &args, None, TIMEOUT, |c| lines.feed(c))?;
        lines.finish()?;
        ok
    };
    if !ok {
        return Err("git diff failed".into());
    }
    Ok(parser.finish())
}

/// Untracked files under `dir` that the repository's ignore rules keep (paths relative
/// to `dir`), at most `cap` kept; returns them and the total.
pub fn untracked(dir: &Path, cap: usize) -> Result<(Vec<String>, usize), String> {
    let mut kept = Vec::new();
    let mut total = 0usize;
    let mut buf = Vec::new();
    let mut take = |name: &[u8]| {
        if name.is_empty() {
            return;
        }
        total += 1;
        if kept.len() < cap
            && let Ok(s) = std::str::from_utf8(name)
        {
            kept.push(s.to_string());
        }
    };
    let ok = stream(
        dir,
        Mode::Symbols,
        &["ls-files", "-z", "--others", "--exclude-standard", "--"],
        None,
        TIMEOUT,
        |chunk| {
            for part in chunk.split_inclusive(|b| *b == 0) {
                match part.last() {
                    Some(0) => {
                        buf.extend_from_slice(&part[..part.len() - 1]);
                        take(&buf);
                        buf.clear();
                    }
                    _ => buf.extend_from_slice(part),
                }
            }
            Ok(())
        },
    )?;
    take(&buf);
    if !ok {
        return Err("git ls-files failed".into());
    }
    Ok((kept, total))
}

/// The repository-relative prefix of `dir` (`sub/dir/`, or empty at the top level).
pub fn prefix(dir: &Path) -> Result<String, String> {
    let out = run_small(dir, &["rev-parse", "--show-prefix"], 4096)?;
    Ok(String::from_utf8_lossy(&out)
        .trim_end_matches('\n')
        .to_string())
}

/// The canonical common Git directory of the checkout at `dir`.
pub fn common_dir(dir: &Path) -> Result<std::path::PathBuf, String> {
    let out = run_small(
        dir,
        &["rev-parse", "--path-format=absolute", "--git-common-dir"],
        4096,
    )?;
    let p = String::from_utf8_lossy(&out).trim().to_string();
    std::fs::canonicalize(&p).map_err(|e| format!("{p}: {e}"))
}

/// Read `objects` (`rev:path` or `:path`) with one `git cat-file --batch`; a missing
/// object, one larger than `cap` bytes, or a name with a newline is None.
pub fn read_objects(
    dir: &Path,
    objects: &[String],
    cap: usize,
) -> Result<Vec<Option<Vec<u8>>>, String> {
    let mut out: Vec<Option<Vec<u8>>> = vec![None; objects.len()];
    for_each_object(dir, objects, cap, |i, blob| out[i] = blob)?;
    Ok(out)
}

/// Stream `objects` through one `git cat-file --batch`, handing each one to `f` with its
/// index as it arrives (None: missing, larger than `cap`, or a name with a newline), so
/// at most one object is held at a time.
pub fn for_each_object(
    dir: &Path,
    objects: &[String],
    cap: usize,
    mut f: impl FnMut(usize, Option<Vec<u8>>),
) -> Result<(), String> {
    let (asked, skipped): (Vec<usize>, Vec<usize>) =
        (0..objects.len()).partition(|i| !objects[*i].contains('\n'));
    for i in skipped {
        f(i, None);
    }
    if asked.is_empty() {
        return Ok(());
    }
    let mut input = Vec::new();
    for i in &asked {
        input.extend_from_slice(objects[*i].as_bytes());
        input.push(b'\n');
    }
    let mut reader = BatchReader::new(cap);
    let mut next = asked.iter();
    let ok = stream(
        dir,
        Mode::Symbols,
        &["cat-file", "--batch"],
        Some(input),
        TIMEOUT,
        |chunk| {
            reader.feed(chunk)?;
            for blob in reader.done.drain(..) {
                if let Some(&i) = next.next() {
                    f(i, blob);
                }
            }
            Ok(())
        },
    )?;
    let missing: Vec<usize> = next.copied().collect();
    if !ok && !missing.is_empty() {
        return Err("git cat-file failed".into());
    }
    for i in missing {
        f(i, None);
    }
    Ok(())
}

/// Incremental reader of `cat-file --batch` output.
struct BatchReader {
    cap: usize,
    head: Vec<u8>,
    /// Bytes of the current object still to come (plus its trailing newline).
    body_left: usize,
    body: Option<Vec<u8>>,
    done: Vec<Option<Vec<u8>>>,
}

impl BatchReader {
    fn new(cap: usize) -> Self {
        BatchReader {
            cap,
            head: Vec::new(),
            body_left: 0,
            body: None,
            done: Vec::new(),
        }
    }

    fn feed(&mut self, mut chunk: &[u8]) -> Result<(), String> {
        while !chunk.is_empty() {
            if self.body_left > 0 {
                let n = self.body_left.min(chunk.len());
                if let Some(b) = self.body.as_mut() {
                    b.extend_from_slice(&chunk[..n]);
                }
                self.body_left -= n;
                chunk = &chunk[n..];
                if self.body_left == 0 {
                    let mut b = self.body.take();
                    if let Some(v) = b.as_mut() {
                        v.pop(); // the newline after the content
                    }
                    self.done.push(b);
                }
                continue;
            }
            let Some(i) = chunk.iter().position(|b| *b == b'\n') else {
                if self.head.len() + chunk.len() > 4096 {
                    return Err("cat-file header too long".into());
                }
                self.head.extend_from_slice(chunk);
                break;
            };
            self.head.extend_from_slice(&chunk[..i]);
            chunk = &chunk[i + 1..];
            let header = String::from_utf8_lossy(&self.head).to_string();
            self.head.clear();
            let mut parts = header.rsplitn(3, ' ');
            let size = parts.next().and_then(|s| s.parse::<usize>().ok());
            let kind = parts.next();
            match (kind, size) {
                (Some("blob"), Some(size)) => {
                    self.body_left = size + 1;
                    self.body = (size <= self.cap).then(|| Vec::with_capacity(size + 1));
                }
                (Some(_), Some(size)) => {
                    // not a blob (a submodule commit, a tree): skip its content
                    self.body_left = size + 1;
                    self.body = None;
                }
                _ => self.done.push(None), // `<name> missing`
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn policy_matches_the_golden_spec() {
        let golden = include_str!("testdata/gitdiff_policy.golden");
        let spec: String = golden
            .lines()
            .filter(|l| !l.starts_with('#') && !l.is_empty())
            .map(|l| format!("{l}\n"))
            .collect();
        assert_eq!(render_policy(), spec);
    }

    #[test]
    fn scope_names_round_trip_and_workspace_is_all() {
        for name in scope_names() {
            let s = Scope::parse(name).unwrap();
            assert_eq!(Scope::parse(s.as_str()), Some(s));
        }
        assert_eq!(Scope::parse("workspace"), Some(Scope::All));
        assert_eq!(Scope::All.as_str(), "all");
        assert_eq!(Scope::parse("HEAD"), None);
    }

    #[test]
    fn refs_shaped_like_options_or_arguments_are_refused() {
        for bad in [
            "",
            "-x",
            "--output=/tmp/x",
            "a b",
            "a\nb",
            "a\0b",
            &"x".repeat(300),
        ] {
            assert!(check_ref(bad).is_err(), "{bad:?} must be refused");
        }
        for good in [
            "HEAD",
            "main",
            "origin/main",
            "HEAD~2",
            "v1.0^{commit}",
            "abc123",
        ] {
            assert!(check_ref(good).is_ok(), "{good:?}");
        }
    }

    #[test]
    fn unquote_decodes_octal_and_escapes() {
        assert_eq!(
            unquote(r#""a/d/\303\274.go""#).unwrap().0,
            "a/d/\u{fc}.go".to_string()
        );
        assert_eq!(unquote(r#""a/x\"y\\z\tw""#).unwrap().0, "a/x\"y\\z\tw");
        assert!(unquote(r#""a/\3""#).is_none());
        assert!(unquote("a/plain").is_none());
    }

    #[test]
    fn touches_are_lines_or_gaps() {
        let t = Touch::of(10, 3);
        assert_eq!(t, Touch::Lines(10, 12));
        assert!(t.hits(12, 20) && t.hits(1, 10) && !t.hits(13, 20));
        let g = Touch::of(10, 0);
        assert!(g.hits(5, 11), "a deletion inside a body touches it");
        assert!(
            !g.hits(5, 10),
            "a deletion right after the last line does not"
        );
        assert!(!g.hits(11, 20), "nor one right before the first");
    }

    fn parse(text: &str) -> Parsed {
        let mut p = Parser::new(Limits::default());
        for l in text.split('\n') {
            p.line(l.as_bytes());
        }
        p.finish()
    }

    #[test]
    fn parser_reads_headers_hunks_and_ignores_content_that_looks_like_headers() {
        let d = parse(
            "diff --git a/src/x.ts b/src/x.ts\nindex 1..2 100644\n--- a/src/x.ts\n+++ b/src/x.ts\n\
             @@ -3,2 +3,0 @@ fn\n--- not a header\n-+++ nor this\n@@ -10 +8,2 @@\n-a\n+b\n+c\n\
             diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1 @@\n+x\n\
             diff --git a/old.go b/old.go\ndeleted file mode 100644\n--- a/old.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n\
             diff --git a/sp ace.go b/sp ace.go\n--- a/sp ace.go\t\n+++ b/sp ace.go\t\n@@ -1 +1 @@\n-a\n+b\n\
             diff --git \"a/q\\\"x.go\" \"b/q\\\"x.go\"\n--- \"a/q\\\"x.go\"\n+++ \"b/q\\\"x.go\"\n@@ -1 +1 @@\n-a\n+b\n\
             diff --git a/img.png b/img.png\nindex 1..2 100644\nBinary files a/img.png and b/img.png differ\n",
        );
        let summary: Vec<(String, Change, usize, bool)> = d
            .files
            .iter()
            .map(|f| (f.path.clone(), f.change, f.hunks.len(), f.binary))
            .collect();
        assert_eq!(
            summary,
            vec![
                ("src/x.ts".into(), Change::Modified, 2, false),
                ("new.go".into(), Change::Added, 1, false),
                ("old.go".into(), Change::Deleted, 1, false),
                ("sp ace.go".into(), Change::Modified, 1, false),
                ("q\"x.go".into(), Change::Modified, 1, false),
                ("img.png".into(), Change::Modified, 0, true),
            ]
        );
        assert_eq!(d.total, 6);
        assert_eq!(
            d.files[0].hunks[1],
            Hunk {
                old_start: 10,
                old_len: 1,
                new_start: 8,
                new_len: 2
            }
        );
    }

    #[test]
    fn parser_keeps_counting_past_its_file_limit() {
        let mut p = Parser::new(Limits {
            files: 1,
            hunks_per_file: 1,
        });
        for l in "diff --git a/a b/a\n@@ -1 +1 @@\n-a\n+b\n@@ -5 +5 @@\n-a\n+b\ndiff --git a/b b/b\n@@ -1 +1 @@\n-x\n+y\n"
            .split('\n')
        {
            p.line(l.as_bytes());
        }
        let d = p.finish();
        assert_eq!((d.files.len(), d.total), (1, 2));
        assert!(d.files[0].hunks_truncated);
    }

    #[test]
    fn lines_cap_overlong_lines_and_keep_the_last_one() {
        let mut got = Vec::new();
        {
            let mut l = Lines::new(4, |line| {
                got.push(String::from_utf8_lossy(line).to_string());
                Ok(())
            });
            l.feed(b"abcdefgh\nij").unwrap();
            l.feed(b"k\nlast").unwrap();
            l.finish().unwrap();
        }
        assert_eq!(got, vec!["abcd", "ijk", "last"]);
    }

    #[test]
    fn batch_reader_splits_blobs_and_skips_missing_and_oversized() {
        let mut r = BatchReader::new(5);
        r.feed(b"aaaa blob 3\nxyz\nbbbb missing\ncccc blob 9\n012345678\ndddd blob 0\n\n")
            .unwrap();
        assert_eq!(
            r.done,
            vec![Some(b"xyz".to_vec()), None, None, Some(Vec::new())]
        );
    }
}
