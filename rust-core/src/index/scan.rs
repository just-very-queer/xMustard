//! Which files the index covers, and a stat key that identifies unchanged ones.
//!
//! Git mode lists tracked files with `git ls-files -s -v -z` (index only: Git never
//! reads the worktree here) and stats each source file once. Dirty detection is ours:
//! a file whose stat key (size, mtime, ctime, inode, device) matches the stored one, and
//! whose mtime was safely before the last scan, is unchanged without being read; any
//! other file is read and identified by its Git blob id. `git diff-files` is not used:
//! on smudged or racily clean index entries it re-reads every file (measured 33 MB RSS
//! and 24 MB footprint on a fresh cline clone). Non-Git mode (`allow_non_git`) walks the
//! directory without following symlinks and applies `.gitignore` and `.xmustardignore`.

use std::path::{Path, PathBuf};

use crate::indexcache::{MAX_GIT_OUTPUT_BYTES, git_timeout, run_git_bounded};

use super::config::IndexConfig;
use super::extract::Lang;
use super::ignore::{Ignore, XMUSTARD_IGNORE};

#[derive(Debug, Clone)]
pub struct Candidate {
    pub path: String,
    pub lang: Lang,
    /// Stat taken during the scan, before any read (so a concurrent edit makes the next
    /// scan's key differ). Empty for losses.
    pub stat: StatKey,
    /// Pre-read problem that makes the file unindexable (`symlink`, `not_regular`).
    pub loss: Option<&'static str>,
}

/// What `lstat` says about a file; equal keys mean the bytes are assumed unchanged.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StatKey {
    pub size: u64,
    pub mtime_ns: i64,
    pub ctime_ns: i64,
    pub ino: u64,
    pub dev: u64,
}

impl StatKey {
    #[cfg(unix)]
    pub fn of(m: &std::fs::Metadata) -> StatKey {
        use std::os::unix::fs::MetadataExt;
        StatKey {
            size: m.len(),
            mtime_ns: m.mtime() * 1_000_000_000 + m.mtime_nsec(),
            ctime_ns: m.ctime() * 1_000_000_000 + m.ctime_nsec(),
            ino: m.ino(),
            dev: m.dev(),
        }
    }

    #[cfg(not(unix))]
    pub fn of(m: &std::fs::Metadata) -> StatKey {
        let mtime_ns = m
            .modified()
            .ok()
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_nanos() as i64)
            .unwrap_or(0);
        StatKey {
            size: m.len(),
            mtime_ns,
            ..Default::default()
        }
    }

    /// Stored form (`files.stat_key`).
    pub fn encode(&self) -> String {
        format!(
            "{}:{}:{}:{}:{}",
            self.size, self.mtime_ns, self.ctime_ns, self.ino, self.dev
        )
    }
}

/// Classify one listed path by `lstat`: None when it is gone from the worktree.
fn stat_candidate(root: &Path, path: String, lang: Lang, mode: &str) -> Option<Candidate> {
    let meta = match std::fs::symlink_metadata(root.join(&path)) {
        Ok(m) => m,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return None,
        Err(_) => {
            return Some(Candidate {
                path,
                lang,
                stat: StatKey::default(),
                loss: Some("unreadable"),
            });
        }
    };
    let loss = if mode == "120000" || meta.file_type().is_symlink() {
        Some("symlink")
    } else if mode == "160000" || !meta.is_file() {
        Some("not_regular")
    } else {
        None
    };
    Some(Candidate {
        path,
        lang,
        stat: if loss.is_none() {
            StatKey::of(&meta)
        } else {
            StatKey::default()
        },
        loss,
    })
}

#[derive(Debug, Clone)]
pub struct Scan {
    /// `git` | `non-git`
    pub repo_mode: &'static str,
    pub head: String,
    /// Where Git keeps this checkout (None outside Git).
    pub git_dir: Option<PathBuf>,
    /// Canonical index root.
    pub root: PathBuf,
    /// Eligible source files, sorted by path.
    pub candidates: Vec<Candidate>,
    /// Source files excluded by the ignore rules.
    pub ignored: usize,
    /// Files listed but not UTF-8 paths.
    pub invalid_paths: usize,
    /// Tracked files deleted from the worktree (not indexed, not a loss).
    pub worktree_deleted: usize,
    /// Wall clock (ns since the epoch) before any file was stat'ed.
    pub started_ns: i64,
}

fn now_ns() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i64)
        .unwrap_or(0)
}

fn git(root: &Path, args: &[&str]) -> Result<Vec<u8>, String> {
    run_git_bounded(root, args, MAX_GIT_OUTPUT_BYTES, git_timeout())
        .map_err(|e| format!("git {}: {e}", args.join(" ")))
}

/// Git blob id of `bytes` (hex SHA-1 of `blob <len>\0<bytes>`): the one content key for
/// clean and dirty files alike.
pub fn blob_id(bytes: &[u8]) -> String {
    use sha1::{Digest, Sha1};
    let mut h = Sha1::new();
    h.update(format!("blob {}\0", bytes.len()).as_bytes());
    h.update(bytes);
    format!("{:x}", h.finalize())
}

pub fn scan(root: &Path, cfg: &IndexConfig) -> Result<Scan, String> {
    let root = std::fs::canonicalize(root).map_err(|e| format!("{}: {e}", root.display()))?;
    match git_layout(&root) {
        Some(git_dir) => scan_git(&root, git_dir, cfg),
        None if cfg.allow_non_git => scan_walk(&root),
        None => Err(format!(
            "{} is not inside a Git work tree (set allow_non_git to index it anyway)",
            root.display()
        )),
    }
}

fn git_layout(root: &Path) -> Option<PathBuf> {
    let out = run_git_bounded(
        root,
        &["rev-parse", "--is-inside-work-tree", "--absolute-git-dir"],
        64 << 10,
        git_timeout(),
    )
    .ok()?;
    let text = String::from_utf8(out).ok()?;
    let mut lines = text.lines();
    if lines.next()? != "true" {
        return None;
    }
    Some(PathBuf::from(lines.next()?))
}

fn head(root: &Path) -> String {
    git(root, &["rev-parse", "-q", "--verify", "HEAD"])
        .ok()
        .and_then(|b| String::from_utf8(b).ok())
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

fn split_z(out: &[u8]) -> impl Iterator<Item = &[u8]> {
    out.split(|b| *b == 0).filter(|r| !r.is_empty())
}

fn scan_git(root: &Path, git_dir: PathBuf, cfg: &IndexConfig) -> Result<Scan, String> {
    let started_ns = now_ns();
    let listing = git(root, &["ls-files", "-s", "-z"])?;
    struct Entry {
        path: String,
        mode: String,
    }
    let mut entries: Vec<Entry> = Vec::new();
    let mut invalid_paths = 0usize;
    let mut ignore_files: Vec<String> = Vec::new();
    for rec in split_z(&listing) {
        // "<mode> <blob> <stage>\t<path>"
        let Some(tab) = rec.iter().position(|b| *b == b'\t') else {
            continue;
        };
        let meta = String::from_utf8_lossy(&rec[..tab]);
        let Some(mode) = meta.split(' ').next() else {
            continue;
        };
        let raw = &rec[tab + 1..];
        let Ok(path) = String::from_utf8(raw.to_vec()) else {
            if Lang::for_path(&String::from_utf8_lossy(raw)).is_some() {
                invalid_paths += 1;
            }
            continue;
        };
        if path.rsplit('/').next() == Some(XMUSTARD_IGNORE) {
            ignore_files.push(path.clone());
        }
        // unmerged paths appear once per stage
        if entries.last().is_some_and(|e| e.path == path) {
            continue;
        }
        entries.push(Entry {
            path,
            mode: mode.to_string(),
        });
    }
    if cfg.include_untracked {
        let others = git(root, &["ls-files", "-o", "--exclude-standard", "-z"])?;
        for raw in split_z(&others) {
            if let Ok(path) = String::from_utf8(raw.to_vec()) {
                entries.push(Entry {
                    path,
                    mode: "100644".into(),
                });
            }
        }
    }

    let mut ig = Ignore::with_defaults();
    ig.load_xmustard(root, &ignore_files);
    let mut ignored = 0usize;
    let mut worktree_deleted = 0usize;
    let mut candidates = Vec::new();
    for e in entries {
        let Some(lang) = Lang::for_path(&e.path) else {
            continue;
        };
        if ig.is_ignored(&e.path, false) {
            ignored += 1;
            continue;
        }
        match stat_candidate(root, e.path, lang, &e.mode) {
            Some(c) => candidates.push(c),
            None => worktree_deleted += 1,
        }
    }
    candidates.sort_by(|a, b| a.path.cmp(&b.path));
    candidates.dedup_by(|a, b| a.path == b.path);
    Ok(Scan {
        repo_mode: "git",
        head: head(root),
        git_dir: Some(git_dir),
        root: root.to_path_buf(),
        candidates,
        ignored,
        invalid_paths,
        worktree_deleted,
        started_ns,
    })
}

fn scan_walk(root: &Path) -> Result<Scan, String> {
    let started_ns = now_ns();
    let mut candidates = Vec::new();
    let mut ignored = 0usize;
    let mut invalid_paths = 0usize;
    let mut ig = Ignore::with_defaults();
    // .gitignore files are applied top-down as directories are entered.
    let mut it = walkdir::WalkDir::new(root)
        .follow_links(false)
        .sort_by_file_name()
        .into_iter();
    while let Some(entry) = it.next() {
        let Ok(entry) = entry else { continue };
        let Ok(rel) = entry.path().strip_prefix(root) else {
            continue;
        };
        let Some(rel) = rel.to_str().map(|s| s.replace('\\', "/")) else {
            invalid_paths += 1;
            continue;
        };
        if rel.is_empty() {
            load_dir_ignores(root, "", &mut ig);
            continue;
        }
        let ft = entry.file_type();
        if ft.is_dir() {
            if ig.is_ignored(&rel, true) {
                it.skip_current_dir();
                continue;
            }
            load_dir_ignores(root, &rel, &mut ig);
            continue;
        }
        let Some(lang) = Lang::for_path(&rel) else {
            continue;
        };
        if ig.is_ignored(&rel, false) {
            ignored += 1;
            continue;
        }
        if let Some(c) = stat_candidate(root, rel, lang, "100644") {
            candidates.push(c);
        }
    }
    candidates.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(Scan {
        repo_mode: "non-git",
        head: String::new(),
        git_dir: None,
        root: root.to_path_buf(),
        candidates,
        ignored,
        invalid_paths,
        worktree_deleted: 0,
        started_ns,
    })
}

fn load_dir_ignores(root: &Path, rel_dir: &str, ig: &mut Ignore) {
    for name in [".gitignore", XMUSTARD_IGNORE] {
        let rel = if rel_dir.is_empty() {
            name.to_string()
        } else {
            format!("{rel_dir}/{name}")
        };
        if let Ok(bytes) = crate::symbolgraph::read_repo_bytes_beneath_capped(root, &rel, 1 << 20) {
            ig.add_file(rel_dir, &String::from_utf8_lossy(&bytes));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blob_id_matches_git() {
        // `printf 'hello\n' | git hash-object --stdin`
        assert_eq!(
            blob_id(b"hello\n"),
            "ce013625030ba8dba906f756967f9e9ca394464a"
        );
        assert_eq!(blob_id(b""), "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391");
    }
}
