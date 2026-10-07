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

pub use reader::Oid;

/// Why the reader gave no answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refusal {
    /// A layout the reader does not follow: a sparse index, an unknown required
    /// extension, index version or object format, `core.ignorecase`.
    Unsupported(String),
    /// The index or the worktree could not be read: a checksum mismatch, a truncated
    /// file, an I/O error, a Git child that failed.
    Failed(String),
}

impl Refusal {
    /// The identity limitation that names this refusal.
    pub fn reason(&self) -> &'static str {
        match self {
            Refusal::Unsupported(_) => "git_index_unsupported",
            Refusal::Failed(_) => "git_index_failed",
        }
    }
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refusal::Unsupported(m) => write!(f, "Git index layout not supported: {m}"),
            Refusal::Failed(m) => f.write_str(m),
        }
    }
}
