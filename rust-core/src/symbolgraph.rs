//! Semantic symbol graph: the canonical symbol layer the cockpit reasons over.
//! Builds files → symbols → reference edges across the repo (lexical reference
//! resolution: a file that uses a symbol defined elsewhere gets an edge), then
//! derives hotspots (most-depended-on files) and blast radius (what a symbol
//! change can affect). This is the brain impact analysis and ownership key off.

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::path::Path;

use crate::repomap;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

const SOURCE_EXTS: &[&str] = &[
    "rs", "go", "py", "ts", "tsx", "js", "jsx", "java", "rb", "c", "h", "cpp", "hpp", "cc",
];
const MAX_FILES: usize = 800;
const MIN_NAME_LEN: usize = 4;
// names this common produce noisy edges; skip as reference anchors.
const STOPWORD_SYMBOLS: &[&str] = &[
    "main", "test", "tests", "init", "new", "build", "run", "string", "error", "result", "value",
    "data", "name", "path", "self", "this", "type", "node", "item", "list",
];

fn is_source(path: &str) -> bool {
    Path::new(path)
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| SOURCE_EXTS.contains(&e))
        .unwrap_or(false)
}

/// Bound on symbols held by one graph (memory); files past it report `symbol_budget`.
pub const MAX_GRAPH_SYMBOLS: usize = 100_000;
/// Bound on per-file loss entries listed in coverage (`loss_counts` stays exact).
pub const MAX_LOSS_ENTRIES: usize = 200;

/// One file whose content or symbols are not fully represented in the graph.
#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct CoverageLoss {
    pub path: String,
    /// `file_cap` | `oversized` | `unreadable` | `symlink` | `not_regular` |
    /// `invalid_path_encoding` | `invalid_utf8` | `excluded_path` |
    /// `unsupported_language` | `symbols_truncated` | `symbol_budget`
    pub reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub detail: String,
    /// Whether the file's text still contributed references/edges to the graph.
    #[serde(default)]
    pub content_indexed: bool,
}

/// Work done by THIS invocation (a cache hit reports the hit, not the original build).
#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct IndexWork {
    /// `hit` | `miss` (built and stored) | `bypass` (built, not cacheable) |
    /// `uncached` (a build command that does not consult the graph cache)
    pub graph_cache: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub graph_cache_detail: String,
    /// `not_used` | `acquired` | `timeout` | `unavailable`
    pub lock: String,
    pub lock_wait_ms: u64,
    /// The lock was held by another builder when first tried (this call waited).
    #[serde(default)]
    pub lock_contended: bool,
    /// Source-identity computations (1 on a hit; 2 when a build re-checks stability).
    pub identity_passes: usize,
    pub identity_files_hashed: usize,
    pub identity_bytes_hashed: u64,
    /// Source files opened and read for indexing (excludes identity hashing).
    pub files_read: usize,
    pub bytes_read: u64,
    /// Files whose text was parsed/analyzed this invocation.
    pub files_parsed: usize,
    pub bytes_parsed: u64,
    /// Files served from the per-file feature cache without reading them.
    pub files_reused: usize,
    /// Symbols extracted by parsing this invocation.
    pub symbols_extracted: usize,
    /// Symbols in the returned graph.
    pub symbols_indexed: usize,
    pub coverage_losses: usize,
    pub stale_temps_removed: usize,
    pub elapsed_ms: u64,
}

/// The source identity the graph was built from (see `indexcache::source_identity`).
#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct SourceIdentitySummary {
    /// Same value `xmustard-core repo-key <root>` reports for an unchanged tree.
    pub key: String,
    pub head: String,
    pub parser_version: String,
    pub identity_complete: bool,
    /// The identity recomputed after the build matched, and every dirty file parsed
    /// had exactly the hashed bytes: the graph reflects `key`. False results are
    /// never cached.
    pub stable: bool,
    /// Number of identity limitations (oversized/unreadable/... dirty files).
    pub limitations: usize,
}

/// Index coverage so consumers can tell a complete answer from a degraded one. File
/// counts never imply symbol completeness: every file that was not fully read and
/// extracted appears in `loss_counts`/`losses`.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct IndexCoverage {
    pub repo_mode: String, // "git" | "git-unavailable"
    /// Tracked source files present in the worktree.
    pub eligible_files: usize,
    /// Files whose text was read (now or from the per-file cache) and analyzed.
    pub indexed_files: usize,
    /// True when the file cap, a per-file symbol bound or the graph symbol budget cut
    /// the result.
    pub truncated: bool,
    pub max_files: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub degraded_reason: Option<String>,
    /// Files selected under `max_files`.
    #[serde(default)]
    pub selected_files: usize,
    /// No losses, Git mode, and a stable source identity.
    #[serde(default)]
    pub complete: bool,
    /// Tracked files deleted in the worktree (not indexed, not a loss).
    #[serde(default)]
    pub worktree_deleted_files: usize,
    #[serde(default)]
    pub symbols_truncated_files: usize,
    /// Exact count per loss reason.
    #[serde(default)]
    pub loss_counts: BTreeMap<String, usize>,
    /// The first MAX_LOSS_ENTRIES losses, sorted by path.
    #[serde(default)]
    pub losses: Vec<CoverageLoss>,
    #[serde(default)]
    pub losses_truncated: bool,
    /// Indexed files per extraction engine (`tree_sitter`, `regex`, `none`,
    /// `excluded_path`, `unsupported_language`).
    #[serde(default)]
    pub extraction: BTreeMap<String, usize>,
    #[serde(default)]
    pub source_identity: SourceIdentitySummary,
    #[serde(default)]
    pub work: IndexWork,
}

/// One `git ls-files -s -z` index entry.
struct IndexEntry {
    mode: String,
    blob: String,
    path: Vec<u8>,
}

fn ls_files_stage(root: &Path) -> Result<Vec<IndexEntry>, crate::indexcache::GitRunError> {
    let out = crate::indexcache::run_git_bounded(
        root,
        &["ls-files", "-s", "-z"],
        crate::indexcache::MAX_GIT_OUTPUT_BYTES,
        crate::indexcache::git_timeout(),
    )?;
    let mut entries: Vec<IndexEntry> = Vec::new();
    for rec in out.split(|b| *b == 0) {
        // "<mode> <blob> <stage>\t<path>"
        let Some(tab) = rec.iter().position(|b| *b == b'\t') else {
            continue;
        };
        let meta = String::from_utf8_lossy(&rec[..tab]);
        let mut parts = meta.split(' ');
        let (Some(mode), Some(blob)) = (parts.next(), parts.next()) else {
            continue;
        };
        let path = rec[tab + 1..].to_vec();
        // unmerged paths appear once per stage; keep the first.
        if entries.last().is_some_and(|e| e.path == path) {
            continue;
        }
        entries.push(IndexEntry {
            mode: mode.to_string(),
            blob: blob.to_string(),
            path,
        });
    }
    Ok(entries)
}

fn git_unavailable_coverage(why: &str) -> IndexCoverage {
    IndexCoverage {
        repo_mode: "git-unavailable".into(),
        max_files: MAX_FILES,
        degraded_reason: Some(format!(
            "git ls-files failed ({why}); the symbol graph is EMPTY, not authoritative"
        )),
        ..Default::default()
    }
}

/// repo_role is the SINGLE canonical classification of a tracked file, so code /
/// tests / docs / guidance / config are decided in ONE place instead of the five
/// divergent per-subsystem extension lists the review flagged (XM-PRO-012). "code"
/// and "test" feed the symbol graph; "doc" and "guide" feed the docs search segment;
/// "config"/"other" are listed but not deeply indexed.
pub fn repo_role(path: &str) -> &'static str {
    let lower = path.to_lowercase();
    // Path-based guidance roots — MUST mirror the Go collectWorkspaceGuidance walk
    // roots (context_replays.go) so the Rust docs/guidance SEARCH segment covers the
    // same guidance the Go grounding packet does — one cross-FFI contract (XM-PRO-012).
    if lower.starts_with(".cursor/rules/")
        || lower.starts_with(".openhands/microagents/")
        || lower.starts_with(".openhands/skills/")
        || lower.starts_with(".agents/skills/")
    {
        return "guide";
    }
    let base = Path::new(&lower)
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("");
    // Named guidance files — keep aligned with the Go candidate list.
    if matches!(
        base,
        "agents.md"
            | "claude.md"
            | "gemini.md"
            | "conventions.md"
            | "readme.md"
            | ".clinerules"
            | ".cursorrules"
    ) {
        return "guide";
    }
    if is_test_file(&lower) {
        return "test";
    }
    if is_source(&lower) {
        return "code";
    }
    match Path::new(&lower).extension().and_then(|e| e.to_str()) {
        Some("md" | "markdown" | "mdx" | "txt" | "rst" | "adoc") => "doc",
        Some("toml" | "json" | "yaml" | "yml" | "ini" | "cfg" | "conf") => "config",
        _ => "other",
    }
}

/// tracked_doc_files lists the repo's git-tracked doc + guidance files — the docs
/// search segment, so the single `search` tool can return hits from prose the symbol
/// graph (code-only) never sees (XM-PRO-012). Empty when git is unavailable.
/// Uses the bounded `ls-files -z` runner, so unusual names are not split or quoted.
pub fn tracked_doc_files(root: &Path) -> Vec<String> {
    let Ok(out) = crate::indexcache::run_git_bounded(
        root,
        &["ls-files", "-z"],
        crate::indexcache::MAX_GIT_OUTPUT_BYTES,
        crate::indexcache::git_timeout(),
    ) else {
        return Vec::new();
    };
    out.split(|b| *b == 0)
        .filter_map(|p| String::from_utf8(p.to_vec()).ok())
        .filter(|l| !l.is_empty() && matches!(repo_role(l), "doc" | "guide"))
        .collect()
}

// SCANNABLE_* is the SINGLE broad "source file worth scanning" set — wider than the
// symbol-graph code set (it adds shell/markup/config) and shared by the repo map +
// signal scanner so those lists stop diverging (XM-PRO-012).
const SCANNABLE_SOURCE_EXTENSIONS: &[&str] = &[
    ".bash", ".c", ".cc", ".cpp", ".cjs", ".cs", ".css", ".go", ".h", ".hpp", ".html", ".java",
    ".js", ".jsx", ".kt", ".kts", ".mjs", ".php", ".py", ".rb", ".rs", ".scala", ".sh", ".sql",
    ".swift", ".ts", ".tsx", ".yaml", ".yml", ".zsh",
];
const SCANNABLE_SOURCE_FILENAMES: &[&str] = &["Dockerfile", "Justfile", "Makefile", "Procfile"];

/// open_repo_file_beneath opens `rel` relative to `root` WITHOUT following a symlink at
/// ANY path component: it opens the root directory, then walks each component with
/// openat()+O_NOFOLLOW (via rustix's SAFE wrappers — the crate's #![forbid(unsafe_code)]
/// rules out a hand-rolled openat). The fd reached is exactly the in-repo file, so a
/// concurrent agent swapping any component (the final file OR an intermediate dir) for a
/// symlink can't redirect the read into a host file — the full check-to-open TOCTOU is
/// closed, matching the Go read oracle (XM-PRO-007). Rejects absolute paths and "..".
/// This is the ONLY repo read/hash primitive; the public read_repo_file_beneath /
/// read_repo_bytes_beneath_capped / hash_repo_file_beneath all hold the fd it returns.
#[cfg(unix)]
fn open_repo_file_beneath(root: &Path, rel: &str) -> std::io::Result<std::fs::File> {
    // byte-exact: a Git path may begin or end with whitespace, and trimming would open
    // a different (decoy) file.
    if rel.contains('\0') {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "empty path",
        ));
    }
    open_repo_path_beneath(root, Path::new(rel))
}

/// open_repo_path_beneath is the untrimmed core of open_repo_file_beneath: `rel` is used
/// byte-for-byte, so Git paths with leading/trailing whitespace or newlines open the
/// file Git names rather than a trimmed neighbour.
#[cfg(unix)]
fn open_repo_path_beneath(root: &Path, rel: &Path) -> std::io::Result<std::fs::File> {
    use rustix::fs::{Mode, OFlags, open, openat};
    use std::path::Component;

    let mut comps: Vec<&std::ffi::OsStr> = Vec::new();
    for c in rel.components() {
        match c {
            Component::Normal(s) => comps.push(s),
            Component::CurDir => {}
            // absolute, "..", or a drive prefix would escape — refuse.
            _ => {
                return Err(std::io::Error::new(
                    std::io::ErrorKind::InvalidInput,
                    "path escapes workspace root",
                ));
            }
        }
    }
    if comps.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "empty path",
        ));
    }

    let mut dir = open(
        root,
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
        Mode::empty(),
    )?;
    let last = comps.len() - 1;
    for (i, comp) in comps.iter().enumerate() {
        let mut flags = OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC;
        if i < last {
            flags |= OFlags::DIRECTORY;
        } else {
            // O_NONBLOCK on the final component so opening a fifo/device (a
            // non-terminating target) returns immediately instead of blocking on the
            // open; the regular-file fstat in open_repo_regular_file_beneath then
            // rejects it. Harmless on a regular file (reads stay fully blocking/normal).
            flags |= OFlags::NONBLOCK;
        }
        // O_NOFOLLOW fails (ELOOP) on a symlink at this component — no traversal.
        dir = openat(&dir, *comp, flags, Mode::empty())?;
    }
    Ok(std::fs::File::from(dir))
}

#[cfg(not(unix))]
fn open_repo_file_beneath(root: &Path, rel: &str) -> std::io::Result<std::fs::File> {
    std::fs::File::open(root.join(rel))
}

#[cfg(not(unix))]
fn open_repo_path_beneath(root: &Path, rel: &Path) -> std::io::Result<std::fs::File> {
    std::fs::File::open(root.join(rel))
}

/// MAX_REPO_FILE_BYTES bounds every single-file repo read/hash. It defends the
/// "huge file" (unbounded allocation) and "non-terminating target" (a fifo/device that
/// never EOFs) hazards: the read stops at the cap instead of growing without limit or
/// blocking forever.
pub const MAX_REPO_FILE_BYTES: u64 = 8 << 20; // 8 MiB

/// open_repo_regular_file_beneath opens `rel` under `root` via the no-follow fd-walk AND
/// fstat-verifies the held descriptor is a REGULAR file — so a fifo, character/block
/// device, directory, or socket is refused before any byte is read (closing the
/// non-terminating-target read). This is the single primitive every repo read/hash flows
/// through; raw `root.join(rel)` reads are banned because they follow symlinks and re-open
/// (TOCTOU) instead of holding the checked fd.
fn open_repo_regular_file_beneath(root: &Path, rel: &str) -> std::io::Result<std::fs::File> {
    let f = open_repo_file_beneath(root, rel)?;
    let meta = f.metadata()?;
    if !meta.is_file() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "not a regular file",
        ));
    }
    Ok(f)
}

/// read_repo_bytes_beneath_capped reads at most `cap` bytes of `rel` from the held
/// no-follow descriptor. If the file is larger than `cap` it returns an error rather than
/// silently truncating, so callers consistently skip oversized files.
pub fn read_repo_bytes_beneath_capped(
    root: &Path,
    rel: &str,
    cap: u64,
) -> std::io::Result<Vec<u8>> {
    use std::io::Read;
    let f = open_repo_regular_file_beneath(root, rel)?;
    let mut buf = Vec::new();
    // Take(cap+1) so reading exactly cap+1 bytes proves the file exceeds the cap.
    let n = f.take(cap + 1).read_to_end(&mut buf)?;
    if n as u64 > cap {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "file exceeds size cap",
        ));
    }
    Ok(buf)
}

/// Why a source file's bytes were not read. Each variant is reported as a coverage
/// loss or identity limitation; none is silently treated as empty content.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SourceReadFailure {
    /// Larger than the cap; nothing past the fstat size check was read.
    Oversized(u64),
    /// A symlink at some path component (O_NOFOLLOW refused it).
    Symlink,
    /// A directory, fifo, device or socket.
    NotRegular,
    /// The path does not exist in the worktree.
    Missing,
    /// Permission or I/O error, with its message.
    Unreadable(String),
}

impl SourceReadFailure {
    /// The stable coverage/identity reason code for this failure.
    pub fn reason(&self) -> &'static str {
        match self {
            SourceReadFailure::Oversized(_) => "oversized",
            SourceReadFailure::Symlink => "symlink",
            SourceReadFailure::NotRegular => "not_regular",
            SourceReadFailure::Missing => "missing",
            SourceReadFailure::Unreadable(_) => "unreadable",
        }
    }

    pub fn detail(&self) -> String {
        match self {
            SourceReadFailure::Oversized(n) => {
                format!("{n} bytes exceeds the {MAX_REPO_FILE_BYTES}-byte read cap; not read")
            }
            SourceReadFailure::Unreadable(e) => e.clone(),
            _ => String::new(),
        }
    }
}

fn classify_open_error(e: &std::io::Error) -> SourceReadFailure {
    #[cfg(unix)]
    {
        use rustix::io::Errno;
        match Errno::from_io_error(e) {
            Some(Errno::LOOP) => return SourceReadFailure::Symlink,
            // an intermediate component that is not a directory (e.g. replaced by a
            // file) or a symlinked directory opened with O_DIRECTORY|O_NOFOLLOW.
            Some(Errno::NOTDIR) => return SourceReadFailure::NotRegular,
            _ => {}
        }
    }
    if e.kind() == std::io::ErrorKind::NotFound {
        SourceReadFailure::Missing
    } else {
        SourceReadFailure::Unreadable(e.to_string())
    }
}

/// read_source_beneath reads all bytes of `rel` (untrimmed, byte-exact) under `root`
/// through the no-follow, regular-file-checked descriptor, refusing files larger than
/// `cap` before reading them. The single classified reader for indexing and identity.
pub fn read_source_beneath(
    root: &Path,
    rel: &Path,
    cap: u64,
) -> Result<Vec<u8>, SourceReadFailure> {
    use std::io::Read;
    let f = open_repo_path_beneath(root, rel).map_err(|e| classify_open_error(&e))?;
    let meta = f
        .metadata()
        .map_err(|e| SourceReadFailure::Unreadable(e.to_string()))?;
    if !meta.is_file() {
        return Err(SourceReadFailure::NotRegular);
    }
    if meta.len() > cap {
        return Err(SourceReadFailure::Oversized(meta.len()));
    }
    let mut buf = Vec::with_capacity(meta.len() as usize);
    let n = f
        .take(cap + 1)
        .read_to_end(&mut buf)
        .map_err(|e| SourceReadFailure::Unreadable(e.to_string()))?;
    if n as u64 > cap {
        // grew after the size check: still refuse instead of truncating.
        return Err(SourceReadFailure::Oversized(n as u64));
    }
    Ok(buf)
}

/// read_repo_file_beneath reads `rel` under `root` (bounded, regular-file-checked,
/// no-follow) as strict UTF-8. The bounded read replaces the prior unbounded
/// read_to_string, so a huge or non-terminating in-repo target can't exhaust memory.
pub fn read_repo_file_beneath(root: &Path, rel: &str) -> std::io::Result<String> {
    let bytes = read_repo_bytes_beneath_capped(root, rel, MAX_REPO_FILE_BYTES)?;
    String::from_utf8(bytes)
        .map_err(|_| std::io::Error::new(std::io::ErrorKind::InvalidData, "not utf-8"))
}

/// hash_repo_file_beneath STREAMS the content hash of `rel` from the SAME no-follow,
/// regular-file-checked descriptor — so the bytes hashed are exactly the bytes a parser
/// reading through the same opener would see (no metadata-check-then-reopen swap window),
/// in constant memory, bounded by MAX_REPO_FILE_BYTES. Returns None on any open/read
/// error or oversize. (Hash stays sha256 for drift-baseline + index-cache continuity; the
/// blake3 content-key migration is the separate deferred item.)
pub fn hash_repo_file_beneath(root: &Path, rel: &str) -> Option<String> {
    use sha2::{Digest, Sha256};
    use std::io::Read;
    let mut f = open_repo_regular_file_beneath(root, rel).ok()?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 64 * 1024];
    let mut total: u64 = 0;
    loop {
        let n = f.read(&mut buf).ok()?;
        if n == 0 {
            break;
        }
        total += n as u64;
        if total > MAX_REPO_FILE_BYTES {
            return None; // bounded: refuse to hash past the cap
        }
        hasher.update(&buf[..n]);
    }
    Some(format!("{:x}", hasher.finalize()))
}

/// is_scannable_source reports whether a path is in the broad scannable-source set
/// (repo map / signal scanner). The single owner of that list (XM-PRO-012).
pub fn is_scannable_source(path: &str) -> bool {
    let p = Path::new(path);
    if let Some(name) = p.file_name().and_then(|n| n.to_str()) {
        if SCANNABLE_SOURCE_FILENAMES.contains(&name) {
            return true;
        }
    }
    p.extension()
        .and_then(|e| e.to_str())
        .map(|e| SCANNABLE_SOURCE_EXTENSIONS.contains(&format!(".{e}").as_str()))
        .unwrap_or(false)
}

fn word_set(content: &str) -> HashSet<String> {
    content
        .split(|c: char| !(c.is_alphanumeric() || c == '_'))
        .filter(|w| w.len() >= MIN_NAME_LEN)
        .map(|w| w.to_string())
        .collect()
}

/// A symbol definition: where it lives and what kind it is.
#[derive(Debug, Clone, Copy)]
struct SymbolDef<'a> {
    path: &'a str,
    kind: &'a str,
}

/// Definitions of one symbol name, borrowed from the feature store. A name defined
/// in more than one file is ambiguous: lexical matching cannot tell which definition
/// a reference means, so it anchors no edge.
#[derive(Debug, Clone, Copy)]
enum Definer<'a> {
    One { path: &'a str, kind: &'a str },
    Ambiguous,
}

/// Edges aggregated by (from, to, kind) → (weight, up to 8 `via` symbols), borrowing
/// every string from the feature store until the edges are materialized.
type EdgeAgg<'a> = HashMap<(&'a str, &'a str, &'static str), (usize, BTreeSet<&'a str>)>;

/// Test files reference code under test; they get "tests" edges, not "calls".
fn is_test_file(path: &str) -> bool {
    let lower = path.to_lowercase();
    lower.ends_with("_test.go")
        || lower.ends_with("_test.rs")
        || lower.ends_with("_test.py")
        || lower.ends_with(".test.ts")
        || lower.ends_with(".test.tsx")
        || lower.ends_with(".test.js")
        || lower.ends_with(".test.jsx")
        || lower.ends_with(".spec.ts")
        || lower.ends_with(".spec.tsx")
        || lower.ends_with(".spec.js")
        || lower.starts_with("test_")
        || lower.contains("/test_")
        || lower.contains("/tests/")
        || lower.contains("/__tests__/")
        || lower.starts_with("tests/")
        || lower.starts_with("test/")
}

/// Identifiers that appear on import/use lines — candidates for "imports" edges.
fn import_candidates(content: &str) -> HashSet<String> {
    let mut out = HashSet::new();
    for raw in content.lines() {
        let line = raw.trim_start();
        let is_import = line.starts_with("use ")
            || line.starts_with("import ")
            || line.starts_with("from ")
            || line.starts_with("pub use ")
            || line.starts_with("const ") && line.contains("require(")
            || line.contains(" require(");
        if !is_import {
            continue;
        }
        for tok in line.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
            if tok.len() >= MIN_NAME_LEN {
                out.insert(tok.to_string());
            }
        }
    }
    out
}

/// Relative module specifiers (./x, ../y) on import lines — resolved to repo files.
fn relative_import_specs(content: &str) -> Vec<String> {
    let mut out = Vec::new();
    for raw in content.lines() {
        let line = raw.trim_start();
        if !(line.starts_with("import ")
            || line.starts_with("export ")
            || line.contains("require(")
            || line.starts_with("from "))
        {
            continue;
        }
        // pull quoted specifiers that look relative
        for quote in ['\'', '"'] {
            let mut rest = line;
            while let Some(start) = rest.find(quote) {
                let after = &rest[start + 1..];
                if let Some(end) = after.find(quote) {
                    let spec = &after[..end];
                    if spec.starts_with("./") || spec.starts_with("../") {
                        out.push(spec.to_string());
                    }
                    rest = &after[end + 1..];
                } else {
                    break;
                }
            }
        }
    }
    out
}

/// Resolve a relative import spec (from `from_path`) to a tracked file path.
fn resolve_relative_import(
    from_path: &str,
    spec: &str,
    tracked: &HashSet<String>,
) -> Option<String> {
    let from_dir = Path::new(from_path)
        .parent()
        .unwrap_or_else(|| Path::new(""));
    let mut joined = from_dir.to_path_buf();
    for part in spec.split('/') {
        match part {
            "." | "" => {}
            ".." => {
                joined.pop();
            }
            other => joined.push(other),
        }
    }
    let base = joined.to_string_lossy().replace('\\', "/");
    let candidates = [
        base.clone(),
        format!("{base}.ts"),
        format!("{base}.tsx"),
        format!("{base}.js"),
        format!("{base}.jsx"),
        format!("{base}/index.ts"),
        format!("{base}/index.tsx"),
        format!("{base}/index.js"),
    ];
    candidates.into_iter().find(|c| tracked.contains(c))
}

/// Supertype names declared on inheritance lines — candidates for "inherits" edges.
/// Covers `extends`/`implements` (TS/JS/Java/PHP), `impl Trait for` (Rust),
/// and `class X(Base)` (Python).
fn inheritance_candidates(content: &str) -> HashSet<String> {
    let mut out = HashSet::new();
    for raw in content.lines() {
        let line = raw.trim();
        // TS/JS/Java: ... extends A implements B, C ...
        for kw in ["extends ", "implements "] {
            if let Some(idx) = line.find(kw) {
                let tail = &line[idx + kw.len()..];
                for tok in tail.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                    if tok.len() >= MIN_NAME_LEN {
                        out.insert(tok.to_string());
                    }
                    // stop at the block start
                    if tail.starts_with('{') {
                        break;
                    }
                }
            }
        }
        // Rust: impl Trait for Type  -> Trait is the supertype
        if line.starts_with("impl ")
            && let Some(for_idx) = line.find(" for ")
        {
            let trait_part = &line["impl ".len()..for_idx];
            for tok in trait_part.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                if tok.len() >= MIN_NAME_LEN {
                    out.insert(tok.to_string());
                }
            }
        }
        // Python: class X(Base1, Base2):
        if line.starts_with("class ")
            && let (Some(open), Some(close)) = (line.find('('), line.find(')'))
            && close > open
        {
            let bases = &line[open + 1..close];
            for tok in bases.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                if tok.len() >= MIN_NAME_LEN {
                    out.insert(tok.to_string());
                }
            }
        }
    }
    out
}

/// Classify a code-level reference edge from `from_path` to a symbol of `def_kind`.
fn reference_edge_kind(from_path: &str, def_kind: &str) -> &'static str {
    if is_test_file(from_path) {
        "tests"
    } else if def_kind == "function" || def_kind == "method" {
        "calls"
    } else {
        "references"
    }
}

fn is_ident_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// Byte index of a whole-word occurrence of `word` in `line` (so `if` doesn't match
/// inside `notify`), or None. Reference implementation for `line_flows`.
#[cfg(test)]
fn find_word(line: &str, word: &str) -> Option<usize> {
    let bytes = line.as_bytes();
    let mut from = 0;
    while let Some(rel) = line[from..].find(word) {
        let idx = from + rel;
        let before_ok = idx == 0 || !is_ident_byte(bytes[idx - 1]);
        let after = idx + word.len();
        let after_ok = after >= bytes.len() || !is_ident_byte(bytes[after]);
        if before_ok && after_ok {
            return Some(idx);
        }
        from = idx + word.len();
    }
    None
}

const BRANCH_KEYWORDS: &[&str] = &["if", "while", "match", "switch", "elif", "when", "case"];

/// Flow classifications (`returns` / `branches` / `writes`) for every identifier of at
/// least MIN_NAME_LEN bytes on `line`: the same result as `flow_edge_kind` per
/// identifier, but keyword positions are read from the line's identifier spans once
/// instead of running a substring search per keyword per identifier.
fn line_flows(line: &str) -> Vec<(&str, &'static str)> {
    let bytes = line.as_bytes();
    let spans = identifier_spans(line);
    // a span is a whole word unless glued to a preceding digit (e.g. `9return`).
    let whole = |s: &(&str, usize, usize)| s.1 == 0 || !is_ident_byte(bytes[s.1 - 1]);
    let ret = spans
        .iter()
        .find(|s| s.0 == "return" && whole(s))
        .map(|s| s.1);
    let branch = spans
        .iter()
        .find(|s| BRANCH_KEYWORDS.contains(&s.0) && whole(s))
        .map(|s| s.1);
    let mut out = Vec::new();
    for (word, start, end) in spans {
        if word.len() < MIN_NAME_LEN {
            continue;
        }
        let kind = if ret.is_some_and(|i| i < start) {
            "returns"
        } else if branch.is_some_and(|i| i < start) {
            "branches"
        } else if is_assignment_target(line, end) {
            "writes"
        } else {
            continue;
        };
        out.push((word, kind));
    }
    out
}

/// Classify the control/data-flow role of a referenced symbol occurring at
/// `[sym_start, sym_end)` on `line`. Returns a flow-edge kind when the syntactic
/// context is a `return` value, a branch condition, or an assignment target; None for
/// a plain expression read (already covered by the calls/references edge). Reference
/// implementation that `line_flows` must match.
#[cfg(test)]
fn flow_edge_kind(line: &str, sym_start: usize, sym_end: usize) -> Option<&'static str> {
    // returns: a `return` keyword precedes the symbol on this line.
    if let Some(i) = find_word(line, "return")
        && i < sym_start
    {
        return Some("returns");
    }
    // branches: a control keyword precedes the symbol (it's inside the condition).
    for kw in BRANCH_KEYWORDS {
        if let Some(i) = find_word(line, kw)
            && i < sym_start
        {
            return Some("branches");
        }
    }
    if is_assignment_target(line, sym_end) {
        return Some("writes");
    }
    None
}

/// writes: the symbol is immediately followed by a plain/compound assignment.
fn is_assignment_target(line: &str, sym_end: usize) -> bool {
    let after = line.get(sym_end..).unwrap_or("").trim_start();
    (after.starts_with('=') && !after.starts_with("=="))
        || after.starts_with("+=")
        || after.starts_with("-=")
        || after.starts_with("*=")
        || after.starts_with("/=")
}

/// Identifier spans (word, start, end) on a line, for per-occurrence flow analysis.
fn identifier_spans(line: &str) -> Vec<(&str, usize, usize)> {
    let bytes = line.as_bytes();
    let mut spans = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if is_ident_byte(bytes[i]) && !bytes[i].is_ascii_digit() {
            let start = i;
            while i < bytes.len() && is_ident_byte(bytes[i]) {
                i += 1;
            }
            spans.push((&line[start..i], start, i));
        } else {
            i += 1;
        }
    }
    spans
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphFileNode {
    pub path: String,
    pub symbol_count: usize,
    /// Precomputed authority = total inbound reference weight (how depended-on this
    /// file is). Computed at index time so search/ranking need not recompute it.
    #[serde(default)]
    pub authority: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphSymbolNode {
    pub name: String,
    pub kind: String,
    pub path: String,
    pub line_start: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphEdge {
    pub from_path: String,
    pub to_path: String,
    pub kind: String, // "imports" | "calls" | "inherits" | "tests" | "references"
    pub weight: usize,
    pub via_symbols: Vec<String>,
    /// How the edge was resolved: "lexical" (identifier-name matching) or "lsp"
    /// (a real language-server reference). Defaults to lexical for older caches.
    #[serde(default = "default_resolution")]
    pub resolution: String,
}

fn default_resolution() -> String {
    "lexical".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SymbolGraph {
    pub workspace_id: String,
    pub file_count: usize,
    pub symbol_count: usize,
    pub edge_count: usize,
    pub files: Vec<GraphFileNode>,
    pub symbols: Vec<GraphSymbolNode>,
    pub edges: Vec<GraphEdge>,
    /// Control/data-flow edges (returns / branches / writes) classified from the
    /// syntactic context of each reference. Kept SEPARATE from `edges` so the
    /// structural authority/impact/proximity machinery (which sums edge weight) is
    /// unperturbed; these answer "how" a dependency is used, not just "that" it is.
    #[serde(default)]
    pub flow_edges: Vec<GraphEdge>,
    #[serde(default)]
    pub flow_edge_count: usize,
    /// Index coverage (git mode, eligible vs indexed file counts, truncation) so a
    /// consumer can distinguish complete evidence from a degraded/empty/truncated graph.
    #[serde(default)]
    pub coverage: IndexCoverage,
    pub generated_at: String,
}

/// A community of files that reference each other more than the rest of the repo —
/// a functional cluster that can cross directory boundaries (unlike top-dir
/// grouping). `label` is the most common directory among members (a readable name).
#[derive(Debug, Clone, Serialize)]
pub struct FileCluster {
    pub cluster_id: usize,
    pub label: String,
    pub files: Vec<String>,
    pub size: usize,
}

/// Detect file communities by label propagation over the reference-edge graph: each
/// file starts in its own community and repeatedly adopts the highest-edge-weight
/// community among its neighbours until stable. Deterministic (files processed in
/// sorted order, ties broken by smaller community id). Clusters can span directories
/// — two tightly-coupled files in different folders land together, which top-dir
/// grouping (ownership.rs) cannot do.
pub fn compute_clusters(graph: &SymbolGraph) -> Vec<FileCluster> {
    // undirected weighted adjacency over files.
    let mut adj: HashMap<String, HashMap<String, usize>> = HashMap::new();
    for e in &graph.edges {
        if e.from_path == e.to_path {
            continue;
        }
        *adj.entry(e.from_path.clone())
            .or_default()
            .entry(e.to_path.clone())
            .or_insert(0) += e.weight;
        *adj.entry(e.to_path.clone())
            .or_default()
            .entry(e.from_path.clone())
            .or_insert(0) += e.weight;
    }
    // every file is a node; isolated files keep their own community.
    let mut nodes: Vec<String> = graph.files.iter().map(|f| f.path.clone()).collect();
    nodes.sort();
    let mut community: HashMap<String, String> =
        nodes.iter().map(|n| (n.clone(), n.clone())).collect();

    for _ in 0..20 {
        let mut changed = false;
        for n in &nodes {
            let Some(neighbours) = adj.get(n) else {
                continue;
            };
            // tally neighbour communities by total edge weight.
            let mut weight_by_comm: BTreeMap<String, usize> = BTreeMap::new();
            for (nbr, w) in neighbours {
                if let Some(c) = community.get(nbr) {
                    *weight_by_comm.entry(c.clone()).or_insert(0) += *w;
                }
            }
            // pick the heaviest community (ties → smallest id via BTreeMap order).
            if let Some((best, _)) = weight_by_comm
                .iter()
                .max_by(|a, b| a.1.cmp(b.1).then_with(|| b.0.cmp(a.0)))
                && community.get(n) != Some(best)
            {
                community.insert(n.clone(), best.clone());
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // group files by final community label, then relabel to compact ids by size.
    let mut groups: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for n in &nodes {
        groups
            .entry(community[n].clone())
            .or_default()
            .push(n.clone());
    }
    let mut clusters: Vec<Vec<String>> = groups.into_values().collect();
    clusters.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a[0].cmp(&b[0])));
    clusters
        .into_iter()
        .enumerate()
        .map(|(i, mut files)| {
            files.sort();
            FileCluster {
                cluster_id: i,
                label: dominant_directory(&files),
                size: files.len(),
                files,
            }
        })
        .collect()
}

// the most common top-level directory among a cluster's files (a readable name).
fn dominant_directory(files: &[String]) -> String {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    for f in files {
        let dir = f.split('/').next().unwrap_or("(root)");
        *counts.entry(dir.to_string()).or_insert(0) += 1;
    }
    counts
        .into_iter()
        .max_by(|a, b| a.1.cmp(&b.1).then_with(|| b.0.cmp(&a.0)))
        .map(|(d, _)| d)
        .unwrap_or_else(|| "(root)".to_string())
}

/// Upgrade a (lexically-built) graph with scope-resolved CALLS edges from a real
/// language server. For the most-connected files first, within a bounded budget of
/// reference calls, it asks the LSP for each callable symbol's references and adds
/// `calls` edges (resolution = "lsp") that a language server actually resolved —
/// not name matches. Degrades to the lexical graph unchanged when no server is
/// installed or any request fails. Bounded so it never blocks on a huge repo.
pub fn upgrade_graph_with_lsp(root: &Path, mut graph: SymbolGraph, budget: usize) -> SymbolGraph {
    use crate::lsp_session::{LspSessionError, LspWorkspaceSession};

    // hard caps so the pass never blocks a large repo: a wall-clock deadline (a hung
    // server can't run it forever) and a fixed pre-open ceiling (independent of the
    // reference budget).
    const MAX_PREOPEN: usize = 300;
    let phase_deadline = std::time::Instant::now() + std::time::Duration::from_secs(120);

    // process files by inbound authority (hotspots first) so the budget is spent
    // where resolution matters most. Dedup with a set to stay O(n).
    let hot: Vec<String> = compute_hotspots(&graph, graph.files.len())
        .into_iter()
        .map(|h| h.path)
        .collect();
    let mut seen: HashSet<String> = hot.iter().cloned().collect();
    let mut ordered: Vec<String> = hot;
    for f in &graph.files {
        if seen.insert(f.path.clone()) {
            ordered.push(f.path.clone());
        }
    }

    // files grouped by their language server, so we can pre-open each language's
    // files before querying (cross-file references need the project loaded).
    let mut by_lang: std::collections::HashMap<&'static str, Vec<String>> =
        std::collections::HashMap::new();
    for f in &graph.files {
        if let Some(cmd) = crate::lsp_session::server_command_for(&f.path) {
            by_lang.entry(cmd).or_default().push(f.path.clone());
        }
    }

    let mut sessions: std::collections::HashMap<String, Option<LspWorkspaceSession>> =
        std::collections::HashMap::new();
    let mut remaining = budget;
    // collected lsp edges: (from, to) -> via symbols
    let mut lsp_edges: HashMap<(String, String), BTreeSet<String>> = HashMap::new();
    let mut lsp_resolved = false;

    'files: for rel in ordered {
        if remaining == 0 || std::time::Instant::now() >= phase_deadline {
            break;
        }
        let lang = match crate::lsp_session::server_command_for(&rel) {
            Some(cmd) => cmd,
            None => continue,
        };
        // lazily start one session per language; pre-open that language's files (up
        // to a fixed cap) so the server has the whole project in view.
        let session = sessions.entry(lang.to_string()).or_insert_with(|| {
            let mut s = LspWorkspaceSession::start(root, &rel, 20).ok();
            if let (Some(sess), Some(paths)) = (s.as_mut(), by_lang.get(lang)) {
                for p in paths.iter().take(MAX_PREOPEN) {
                    sess.open(p);
                }
            }
            s
        });
        if session.is_none() {
            continue;
        }

        let content = match read_repo_file_beneath(root, &rel) {
            Ok(c) => c,
            Err(_) => continue,
        };
        let Some(symbols) = crate::treesitter::extract_symbols(&rel, &content) else {
            continue;
        };
        let mut session_died = false;
        for sym in symbols {
            if remaining == 0 || std::time::Instant::now() >= phase_deadline {
                break 'files;
            }
            // only callable symbols anchor CALLS edges.
            if sym.kind != "function" && sym.kind != "method" {
                continue;
            }
            let Some(session) = sessions.get_mut(lang).and_then(|s| s.as_mut()) else {
                break;
            };
            let line = (sym.line_start.saturating_sub(1)) as u32;
            match session.references(&rel, line, sym.name_column as u32) {
                Ok(refs) => {
                    remaining -= 1; // only consume budget on a successful call
                    lsp_resolved = true;
                    for (ref_path, _) in refs {
                        if ref_path == rel {
                            continue;
                        }
                        lsp_edges
                            .entry((ref_path, rel.clone()))
                            .or_default()
                            .insert(sym.symbol.clone());
                    }
                }
                // a timeout/disconnect likely corrupts the server: drop the session
                // so we don't keep querying a broken connection.
                Err(LspSessionError::Failed(_)) => {
                    session_died = true;
                    break;
                }
                Err(_) => continue,
            }
        }
        if session_died {
            sessions.insert(lang.to_string(), None);
        }
    }

    if !lsp_resolved {
        return graph; // no server resolved anything → leave the lexical graph as-is
    }
    // merge: an LSP-confirmed call edge supersedes the lexical edge for the same
    // (from, to) pair regardless of the lexical kind (calls/tests/references), so we
    // upgrade it in place to a resolved "calls" edge instead of adding a duplicate.
    for ((from, to), via) in lsp_edges {
        if let Some(existing) = graph
            .edges
            .iter_mut()
            .find(|e| e.from_path == from && e.to_path == to)
        {
            existing.kind = "calls".to_string();
            existing.resolution = "lsp".to_string();
            for v in &via {
                if existing.via_symbols.len() < 8 && !existing.via_symbols.contains(v) {
                    existing.via_symbols.push(v.clone());
                }
            }
        } else {
            graph.edges.push(GraphEdge {
                from_path: from,
                to_path: to,
                kind: "calls".to_string(),
                weight: via.len(),
                via_symbols: via.into_iter().take(8).collect(),
                resolution: "lsp".to_string(),
            });
        }
    }
    graph.edge_count = graph.edges.len();
    graph
}

/// A symbol as the graph stores it per file.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
struct IndexedSymbol {
    name: String,
    kind: String,
    line_start: Option<usize>,
}

/// Everything graph assembly needs from one file, so an unchanged file is neither
/// read nor parsed again and no file body is retained after analysis.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
struct FileFeatures {
    /// `git:<index blob>` for files clean against the index, else `sha256:<bytes>`.
    identity: String,
    engine: String,
    symbols: Vec<IndexedSymbol>,
    symbols_truncated: bool,
    invalid_utf8: bool,
    words: Vec<String>,
    imports: Vec<String>,
    inherits: Vec<String>,
    rel_specs: Vec<String>,
    /// (identifier, flow kind, occurrences)
    flows: Vec<(String, String, usize)>,
}

#[derive(Debug, Serialize, Deserialize, Default)]
struct FeatureCache {
    parser_version: String,
    files: BTreeMap<String, FileFeatures>,
}

fn sorted<I: IntoIterator<Item = String>>(items: I) -> Vec<String> {
    let mut v: Vec<String> = items.into_iter().collect();
    v.sort();
    v
}

/// Analyze one file's bytes into graph features, then drop the bytes.
fn analyze_source(rel: &str, bytes: Vec<u8>, identity: String) -> FileFeatures {
    let (text, invalid_utf8) = match String::from_utf8(bytes) {
        Ok(t) => (t, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    };
    let ex = repomap::extract_source_symbols(rel, &text, crate::treesitter::MAX_SYMBOLS_PER_FILE);
    let mut flows: BTreeMap<(String, &'static str), usize> = BTreeMap::new();
    for line in text.lines() {
        for (word, kind) in line_flows(line) {
            *flows.entry((word.to_string(), kind)).or_insert(0) += 1;
        }
    }
    FileFeatures {
        identity,
        engine: ex.engine.to_string(),
        symbols: ex
            .symbols
            .into_iter()
            .map(|s| IndexedSymbol {
                name: s.symbol,
                kind: s.kind,
                line_start: s.line_start,
            })
            .collect(),
        symbols_truncated: ex.truncated,
        invalid_utf8,
        words: sorted(word_set(&text)),
        imports: sorted(import_candidates(&text)),
        inherits: sorted(inheritance_candidates(&text)),
        rel_specs: relative_import_specs(&text),
        flows: flows
            .into_iter()
            .map(|((w, k), n)| (w, k.to_string(), n))
            .collect(),
    }
}

/// Build the symbol graph through the shared, repo/trust/parser-scoped cache. The
/// cache key is the full source identity (HEAD + NUL-parsed status + dirty and
/// untracked content hashes), so any content change misses; builders for one scope
/// are serialized across processes, and a waiter re-checks the cache after the lock.
pub fn build_symbol_graph_cached(root: &Path, workspace_id: &str) -> SymbolGraph {
    use crate::indexcache::{LockOutcome, cache_scope, lock_timeout, source_identity};
    let started = std::time::Instant::now();
    let id = source_identity(root);
    let mut work = IndexWork {
        lock: "not_used".into(),
        identity_passes: 1,
        identity_files_hashed: id.files_hashed,
        identity_bytes_hashed: id.bytes_hashed,
        ..Default::default()
    };
    let scope = cache_scope(&id);
    let Some(scope) = scope.filter(|_| id.graph_cacheable()) else {
        work.graph_cache = "bypass".into();
        work.graph_cache_detail = format!(
            "source identity does not determine the graph: {}",
            id.graph_bypass_reason().unwrap_or_default()
        );
        return build_graph(
            root,
            workspace_id,
            &id,
            cache_scope(&id).as_ref(),
            false,
            work,
            started,
        );
    };
    if let Some(graph) = scope.load_graph(&id.key) {
        return serve_cached(graph, workspace_id, work, started);
    }
    let (lock, outcome) = scope.lock_build(lock_timeout());
    match outcome {
        LockOutcome::Acquired {
            waited_ms,
            contended,
        } => {
            work.lock = "acquired".into();
            work.lock_wait_ms = waited_ms;
            work.lock_contended = contended;
        }
        LockOutcome::TimedOut { waited_ms } => {
            work.lock = "timeout".into();
            work.lock_wait_ms = waited_ms;
            work.lock_contended = true;
        }
        LockOutcome::Unavailable(e) => {
            work.lock = "unavailable".into();
            work.graph_cache_detail = format!("build lock unavailable: {e}");
        }
    }
    if lock.is_some()
        && let Some(graph) = scope.load_graph(&id.key)
    {
        // another process built this snapshot while we waited.
        return serve_cached(graph, workspace_id, work, started);
    }
    let store = lock.is_some();
    if !store {
        work.graph_cache_detail = format!(
            "built without the build lock ({}); result not stored",
            work.lock
        );
    }
    let graph = build_graph(root, workspace_id, &id, Some(&scope), store, work, started);
    drop(lock);
    graph
}

fn serve_cached(
    mut graph: SymbolGraph,
    workspace_id: &str,
    mut work: IndexWork,
    started: std::time::Instant,
) -> SymbolGraph {
    work.graph_cache = "hit".into();
    work.symbols_indexed = graph.symbols.len();
    work.coverage_losses = graph.coverage.loss_counts.values().sum();
    work.elapsed_ms = started.elapsed().as_millis() as u64;
    graph.workspace_id = workspace_id.to_string();
    graph.coverage.work = work;
    graph
}

/// Build the symbol graph over tracked source files without consulting the graph
/// cache. The per-file feature cache still applies: only files whose identity changed
/// are read and parsed.
pub fn build_symbol_graph(root: &Path, workspace_id: &str) -> SymbolGraph {
    use crate::indexcache::{cache_scope, source_identity};
    let started = std::time::Instant::now();
    let id = source_identity(root);
    let work = IndexWork {
        graph_cache: "uncached".into(),
        lock: "not_used".into(),
        identity_passes: 1,
        identity_files_hashed: id.files_hashed,
        identity_bytes_hashed: id.bytes_hashed,
        ..Default::default()
    };
    build_graph(
        root,
        workspace_id,
        &id,
        cache_scope(&id).as_ref(),
        false,
        work,
        started,
    )
}

fn empty_graph(workspace_id: &str, coverage: IndexCoverage) -> SymbolGraph {
    SymbolGraph {
        workspace_id: workspace_id.to_string(),
        file_count: 0,
        symbol_count: 0,
        edge_count: 0,
        files: Vec::new(),
        symbols: Vec::new(),
        edges: Vec::new(),
        flow_edges: Vec::new(),
        flow_edge_count: 0,
        coverage,
        generated_at: now(),
    }
}

fn identity_summary(id: &crate::indexcache::SourceIdentity, stable: bool) -> SourceIdentitySummary {
    SourceIdentitySummary {
        key: id.key.clone(),
        head: id.head.clone(),
        parser_version: id.parser_version.clone(),
        identity_complete: id.identity_complete,
        stable,
        limitations: id.limitations.len(),
    }
}

#[allow(clippy::too_many_arguments)]
fn build_graph(
    root: &Path,
    workspace_id: &str,
    id: &crate::indexcache::SourceIdentity,
    scope: Option<&crate::indexcache::CacheScope>,
    store_graph: bool,
    mut work: IndexWork,
    started: std::time::Instant,
) -> SymbolGraph {
    use crate::indexcache::{DirtyContent, source_identity};

    let listing = match id.layout {
        Some(_) => ls_files_stage(root).map_err(|e| e.to_string()),
        None => Err("no .git / git missing / unsafe directory".to_string()),
    };
    let (layout, listing) = match (id.layout.as_ref(), listing) {
        (Some(layout), Ok(listing)) => (layout, listing),
        (_, res) => {
            let mut cov = git_unavailable_coverage(&res.err().unwrap_or_default());
            cov.source_identity = identity_summary(id, false);
            work.elapsed_ms = started.elapsed().as_millis() as u64;
            cov.work = work;
            return empty_graph(workspace_id, cov);
        }
    };
    let status_known = !id.limitations.iter().any(|l| {
        matches!(
            l.reason.as_str(),
            "git_status_failed" | "git_index_flags_failed"
        )
    });
    let parser = crate::indexcache::parser_version();
    let mut cache: FeatureCache = scope
        .and_then(|s| s.load_features::<FeatureCache>())
        .filter(|c| c.parser_version == parser)
        .unwrap_or_default();
    let mut next = FeatureCache {
        parser_version: parser,
        files: BTreeMap::new(),
    };

    let mut losses: Vec<CoverageLoss> = Vec::new();
    let mut loss = |path: &str, reason: &str, detail: String, content_indexed: bool| {
        losses.push(CoverageLoss {
            path: path.to_string(),
            reason: reason.to_string(),
            detail,
            content_indexed,
        });
    };
    let mut eligible = 0usize;
    let mut selected = 0usize;
    let mut deleted = 0usize;
    let mut identity_mismatch = false;
    let mut clean_read_failed = false;
    // `next.files` is the single store of this build's features (path order equals
    // Git's index order); it is written to the feature cache and dropped before the
    // graph snapshot is stored, so features are never held twice.

    for entry in listing {
        let Ok(rel) = String::from_utf8(entry.path.clone()) else {
            if is_source(&String::from_utf8_lossy(&entry.path)) {
                eligible += 1;
                loss(
                    &String::from_utf8_lossy(&entry.path),
                    "invalid_path_encoding",
                    "path is not UTF-8".into(),
                    false,
                );
            }
            continue;
        };
        if !is_source(&rel) {
            continue;
        }
        let mut top_rel = layout.prefix.as_bytes().to_vec();
        top_rel.extend_from_slice(&entry.path);
        let dirty = if status_known {
            id.dirty.get(&top_rel)
        } else {
            Some(&DirtyContent::Unhashed)
        };
        if matches!(dirty, Some(DirtyContent::Deleted)) {
            deleted += 1;
            continue;
        }
        eligible += 1;
        if selected >= MAX_FILES {
            loss(
                &rel,
                "file_cap",
                format!("beyond the {MAX_FILES}-file cap"),
                false,
            );
            continue;
        }
        selected += 1;
        match (entry.mode.as_str(), dirty) {
            ("120000", _) | (_, Some(DirtyContent::Symlink(_))) => {
                loss(&rel, "symlink", "symlinks are not followed".into(), false);
                continue;
            }
            ("160000", _) => {
                loss(&rel, "not_regular", "submodule".into(), false);
                continue;
            }
            (_, Some(DirtyContent::Incomplete(f))) => {
                loss(&rel, f.reason(), f.detail(), false);
                continue;
            }
            _ => {}
        }
        let expected = match dirty {
            Some(DirtyContent::Sha256(h)) => Some(h.clone()),
            _ => None,
        };
        let clean_identity = dirty.is_none().then(|| format!("git:{}", entry.blob));
        // reuse key: the index blob for clean files, the identity pass's full SHA-256
        // for dirty ones. Dirty reuse stays source-consistent because the post-build
        // identity pass must re-hash the same bytes for the result to be stable.
        let reuse_identity = clean_identity
            .clone()
            .or_else(|| expected.as_ref().map(|h| format!("sha256:{h}")));
        if let Some(ident) = &reuse_identity
            && let Some(f) = cache.files.remove(&rel)
            && &f.identity == ident
        {
            work.files_reused += 1;
            next.files.insert(rel, f);
            continue;
        }
        match read_source_beneath(root, Path::new(&rel), MAX_REPO_FILE_BYTES) {
            Ok(bytes) => {
                work.files_read += 1;
                work.bytes_read += bytes.len() as u64;
                work.files_parsed += 1;
                work.bytes_parsed += bytes.len() as u64;
                let identity = match clean_identity {
                    Some(i) => i,
                    None => {
                        let actual =
                            format!("{:x}", <sha2::Sha256 as sha2::Digest>::digest(&bytes));
                        if expected.as_ref().is_some_and(|e| *e != actual) {
                            identity_mismatch = true;
                        }
                        format!("sha256:{actual}")
                    }
                };
                let f = analyze_source(&rel, bytes, identity);
                work.symbols_extracted += f.symbols.len();
                next.files.insert(rel, f);
            }
            Err(f) => {
                if dirty.is_none() && matches!(f, SourceReadFailure::Unreadable(_)) {
                    clean_read_failed = true;
                }
                if matches!(f, SourceReadFailure::Missing) {
                    // deleted after status ran: the identity no longer holds.
                    identity_mismatch = true;
                }
                loss(&rel, f.reason(), f.detail(), false);
            }
        }
    }

    // ---- assemble symbols and definitions ----
    // Everything below borrows from the feature store until the features are written
    // to the cache; only then are symbol names/kinds *moved* into graph nodes, so no
    // symbol string is held twice at the peak.
    let tracked: HashSet<String> = next.files.keys().cloned().collect();
    let mut file_nodes = Vec::with_capacity(next.files.len());
    let mut kept_total = 0usize;
    let mut name_to_defs: HashMap<&str, Definer<'_>> = HashMap::new();
    let mut extraction: BTreeMap<String, usize> = BTreeMap::new();
    let mut symbols_truncated_files = 0usize;
    let mut budget_hit = false;
    for (rel, f) in &next.files {
        *extraction.entry(f.engine.clone()).or_insert(0) += 1;
        match f.engine.as_str() {
            "excluded_path" => loss(
                rel,
                "excluded_path",
                "vendored/generated directory: symbols not extracted".into(),
                true,
            ),
            "unsupported_language" => loss(
                rel,
                "unsupported_language",
                "no symbol extractor for this language; references only".into(),
                true,
            ),
            _ => {}
        }
        if f.invalid_utf8 {
            loss(
                rel,
                "invalid_utf8",
                "decoded with U+FFFD replacement".into(),
                true,
            );
        }
        if f.symbols_truncated {
            symbols_truncated_files += 1;
            loss(
                rel,
                "symbols_truncated",
                format!(
                    "more than {} symbols; the rest are not indexed",
                    crate::treesitter::MAX_SYMBOLS_PER_FILE
                ),
                true,
            );
        }
        let room = MAX_GRAPH_SYMBOLS.saturating_sub(kept_total);
        if f.symbols.len() > room {
            budget_hit = true;
            loss(
                rel,
                "symbol_budget",
                format!(
                    "graph symbol budget {MAX_GRAPH_SYMBOLS} reached; {} of {} symbols indexed",
                    room,
                    f.symbols.len()
                ),
                true,
            );
        }
        let kept = &f.symbols[..f.symbols.len().min(room)];
        kept_total += kept.len();
        file_nodes.push(GraphFileNode {
            path: rel.clone(),
            symbol_count: kept.len(),
            authority: 0,
        });
        for s in kept {
            let lname = s.name.to_lowercase();
            if s.name.len() >= MIN_NAME_LEN && !STOPWORD_SYMBOLS.contains(&lname.as_str()) {
                name_to_defs
                    .entry(s.name.as_str())
                    .and_modify(|d| {
                        // a second defining FILE makes the name ambiguous; a repeat in
                        // the same file keeps the first definition.
                        if let Definer::One { path, .. } = d
                            && *path != rel.as_str()
                        {
                            *d = Definer::Ambiguous;
                        }
                    })
                    .or_insert(Definer::One {
                        path: rel.as_str(),
                        kind: s.kind.as_str(),
                    });
            }
        }
    }
    let unique_definer = |name: &str| -> Option<SymbolDef<'_>> {
        match name_to_defs.get(name) {
            Some(Definer::One { path, kind }) => Some(SymbolDef { path, kind }),
            _ => None,
        }
    };

    // ---- edges from features (all dependents re-resolved every build) ----
    let mut agg: EdgeAgg<'_> = HashMap::new();
    let mut flow_agg: EdgeAgg<'_> = HashMap::new();
    fn add_edge<'a>(
        from: &'a str,
        to: &'a str,
        kind: &'static str,
        via: &'a str,
        n: usize,
        agg: &mut EdgeAgg<'a>,
    ) {
        if from == to {
            return;
        }
        let entry = agg.entry((from, to, kind)).or_insert((0, BTreeSet::new()));
        entry.0 += n;
        if entry.1.len() < 8 {
            entry.1.insert(via);
        }
    }
    for (path, f) in &next.files {
        for spec in &f.rel_specs {
            if let Some(target) = resolve_relative_import(path, spec, &tracked)
                && let Some(target) = tracked.get(&target)
            {
                add_edge(path, target, "imports", spec, 1, &mut agg);
            }
        }
        for name in &f.inherits {
            if let Some(def) = unique_definer(name)
                && def.path != path
            {
                add_edge(path, def.path, "inherits", name, 1, &mut agg);
            }
        }
        for name in &f.imports {
            if let Some(def) = unique_definer(name)
                && def.path != path
            {
                add_edge(path, def.path, "imports", name, 1, &mut agg);
            }
        }
        for word in &f.words {
            if f.inherits.binary_search(word).is_ok() {
                continue; // already captured as a typed inheritance edge
            }
            if let Some(def) = unique_definer(word)
                && def.path != path
            {
                let kind = reference_edge_kind(path, def.kind);
                add_edge(path, def.path, kind, word, 1, &mut agg);
            }
        }
        for (word, kind, n) in &f.flows {
            if let Some(def) = unique_definer(word)
                && def.path != path
            {
                let kind: &'static str = match kind.as_str() {
                    "returns" => "returns",
                    "branches" => "branches",
                    _ => "writes",
                };
                add_edge(path, def.path, kind, word, *n, &mut flow_agg);
            }
        }
    }
    let to_edges = |agg: EdgeAgg<'_>| {
        let mut edges: Vec<GraphEdge> = agg
            .into_iter()
            .map(|((from, to, kind), (weight, via))| GraphEdge {
                from_path: from.to_string(),
                to_path: to.to_string(),
                kind: kind.to_string(),
                weight,
                via_symbols: via.into_iter().map(str::to_string).collect(),
                resolution: "lexical".to_string(),
            })
            .collect();
        edges.sort_by(|a, b| {
            b.weight
                .cmp(&a.weight)
                .then(a.from_path.cmp(&b.from_path))
                .then(a.to_path.cmp(&b.to_path))
                .then(a.kind.cmp(&b.kind))
        });
        edges
    };
    let edges = to_edges(agg);
    let flow_edges = to_edges(flow_agg);
    drop(name_to_defs);
    let mut inbound: HashMap<&str, usize> = HashMap::new();
    for e in &edges {
        *inbound.entry(e.to_path.as_str()).or_insert(0) += e.weight;
    }
    for node in &mut file_nodes {
        node.authority = inbound.get(node.path.as_str()).copied().unwrap_or(0);
    }

    // ---- stability: the graph reflects exactly the identity it reports ----
    let after = source_identity(root);
    work.identity_passes += 1;
    work.identity_files_hashed += after.files_hashed;
    work.identity_bytes_hashed += after.bytes_hashed;
    let stable = !identity_mismatch && after.key == id.key;

    let mut stored_temps = 0;
    let indexed_files = next.files.len();
    if let Some(scope) = scope
        && stable
    {
        stored_temps += scope.store_features(&next);
    }
    // move (not clone) symbol names/kinds into graph nodes, releasing each file's
    // remaining features as it goes; the node vector is allocated once at full size.
    let mut symbols = Vec::with_capacity(kept_total);
    for ((rel, f), node) in next.files.into_iter().zip(&file_nodes) {
        for s in f.symbols.into_iter().take(node.symbol_count) {
            symbols.push(GraphSymbolNode {
                name: s.name,
                kind: s.kind,
                path: rel.clone(),
                line_start: s.line_start,
            });
        }
    }

    // ---- coverage ----
    losses.sort_by(|a, b| a.path.cmp(&b.path).then(a.reason.cmp(&b.reason)));
    let mut loss_counts: BTreeMap<String, usize> = BTreeMap::new();
    for l in &losses {
        *loss_counts.entry(l.reason.clone()).or_insert(0) += 1;
    }
    let total_losses = losses.len();
    let losses_truncated = total_losses > MAX_LOSS_ENTRIES;
    losses.truncate(MAX_LOSS_ENTRIES);
    let file_capped = loss_counts.contains_key("file_cap");
    let truncated = file_capped || symbols_truncated_files > 0 || budget_hit;
    let mut reasons: Vec<String> = Vec::new();
    if file_capped {
        reasons.push(format!(
            "indexed first {selected} of {eligible} source files; results beyond the cap are incomplete"
        ));
    }
    let other: Vec<String> = loss_counts
        .iter()
        .filter(|(r, _)| r.as_str() != "file_cap")
        .map(|(r, n)| format!("{r}={n}"))
        .collect();
    if !other.is_empty() {
        reasons.push(format!("coverage losses: {}", other.join(", ")));
    }
    if !stable {
        reasons.push("source changed during indexing; this result is not cached".into());
    }
    // `complete` covers the graph's input: no coverage loss, a stable build, and an
    // identity that determines every tracked file the graph reads. Limitations on
    // untracked files (never indexed) are reported in `source_identity` only.
    let identity_determines_graph = id.graph_bypass_reason().is_none();
    let complete = loss_counts.is_empty() && stable && identity_determines_graph;
    if !identity_determines_graph {
        reasons.push(format!(
            "source identity is incomplete for indexed files ({})",
            id.graph_bypass_reason().unwrap_or_default()
        ));
    }
    let cacheable = store_graph && stable && !clean_read_failed;
    work.graph_cache = match (work.graph_cache.as_str(), cacheable) {
        ("uncached", _) => "uncached".into(),
        ("bypass", _) => "bypass".into(),
        (_, true) => "miss".into(),
        (_, false) => {
            if work.graph_cache_detail.is_empty() {
                work.graph_cache_detail = if !stable {
                    "source changed during the build".into()
                } else if clean_read_failed {
                    "an unchanged file could not be read; retried on the next call".into()
                } else {
                    "not stored".into()
                };
            }
            "bypass".into()
        }
    };
    work.symbols_indexed = symbols.len();
    work.coverage_losses = total_losses;

    let mut graph = SymbolGraph {
        workspace_id: workspace_id.to_string(),
        file_count: file_nodes.len(),
        symbol_count: symbols.len(),
        edge_count: edges.len(),
        files: file_nodes,
        symbols,
        edges,
        flow_edge_count: flow_edges.len(),
        flow_edges,
        coverage: IndexCoverage {
            repo_mode: "git".into(),
            eligible_files: eligible,
            indexed_files,
            truncated,
            max_files: MAX_FILES,
            degraded_reason: (!reasons.is_empty()).then(|| reasons.join("; ")),
            selected_files: selected,
            complete,
            worktree_deleted_files: deleted,
            symbols_truncated_files,
            loss_counts,
            losses,
            losses_truncated,
            extraction,
            source_identity: identity_summary(id, stable),
            work: IndexWork::default(),
        },
        generated_at: now(),
    };
    if cacheable && let Some(scope) = scope {
        // the stored snapshot carries this build's counters; hits replace them.
        work.elapsed_ms = started.elapsed().as_millis() as u64;
        graph.coverage.work = work.clone();
        stored_temps += scope.store_graph(&id.key, &graph);
    }
    work.stale_temps_removed = stored_temps;
    work.elapsed_ms = started.elapsed().as_millis() as u64;
    graph.coverage.work = work;
    graph
}

#[derive(Debug, Clone, Serialize)]
pub struct Hotspot {
    pub path: String,
    pub inbound_weight: usize,
    pub dependent_count: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct ImpactedFile {
    pub path: String,
    pub distance: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct SymbolImpact {
    pub symbol: String,
    pub defined_in: Vec<String>,
    pub impacted: Vec<ImpactedFile>,
    pub impacted_count: usize,
    pub max_depth: usize,
    pub generated_at: String,
}

// files that define a given symbol (by symbol-node match).
fn files_defining(graph: &SymbolGraph, symbol: &str) -> Vec<String> {
    let mut set = BTreeSet::new();
    for s in &graph.symbols {
        if s.name == symbol {
            set.insert(s.path.clone());
        }
    }
    set.into_iter().collect()
}

// reverse adjacency: to_path -> set of files that reference it.
fn reverse_adjacency(graph: &SymbolGraph) -> HashMap<String, BTreeSet<String>> {
    let mut rev: HashMap<String, BTreeSet<String>> = HashMap::new();
    for e in &graph.edges {
        if e.from_path != e.to_path {
            rev.entry(e.to_path.clone())
                .or_default()
                .insert(e.from_path.clone());
        }
    }
    rev
}

/// True blast radius of a symbol: a bounded breadth-first traversal of the
/// precomputed reference graph outward from the file(s) defining the symbol — every
/// file that transitively depends on it, with its distance. This is the symbol-level
/// `impact?symbol=` answer, not just the dirty-symbols view.
pub fn symbol_impact(graph: &SymbolGraph, symbol: &str, max_depth: usize) -> SymbolImpact {
    let defined_in = files_defining(graph, symbol);
    let rev = reverse_adjacency(graph);
    let mut visited: BTreeSet<String> = defined_in.iter().cloned().collect();
    let mut impacted: Vec<ImpactedFile> = Vec::new();
    let mut frontier: Vec<String> = defined_in.clone();
    let mut depth = 1;
    while !frontier.is_empty() && depth <= max_depth {
        let mut next: BTreeSet<String> = BTreeSet::new();
        for f in &frontier {
            if let Some(callers) = rev.get(f) {
                for c in callers {
                    if visited.insert(c.clone()) {
                        next.insert(c.clone());
                    }
                }
            }
        }
        for f in &next {
            impacted.push(ImpactedFile {
                path: f.clone(),
                distance: depth,
            });
        }
        frontier = next.into_iter().collect();
        depth += 1;
    }
    SymbolImpact {
        symbol: symbol.to_string(),
        defined_in,
        impacted_count: impacted.len(),
        impacted,
        max_depth,
        generated_at: now(),
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct SymbolTrace {
    pub from: String,
    pub to: String,
    pub path: Vec<String>,
    pub length: usize,
    pub found: bool,
    pub generated_at: String,
}

/// Shortest dependency path between two symbols: BFS over the undirected file graph
/// from a file defining `from` to a file defining `to`. Answers "how does A reach B".
pub fn trace_symbols(graph: &SymbolGraph, from: &str, to: &str) -> SymbolTrace {
    let from_files: BTreeSet<String> = files_defining(graph, from).into_iter().collect();
    let to_files: BTreeSet<String> = files_defining(graph, to).into_iter().collect();
    // undirected adjacency.
    let mut adj: HashMap<String, BTreeSet<String>> = HashMap::new();
    for e in &graph.edges {
        if e.from_path != e.to_path {
            adj.entry(e.from_path.clone())
                .or_default()
                .insert(e.to_path.clone());
            adj.entry(e.to_path.clone())
                .or_default()
                .insert(e.from_path.clone());
        }
    }
    // multi-source BFS from all `from` files, tracking predecessors.
    let mut prev: HashMap<String, String> = HashMap::new();
    let mut visited: BTreeSet<String> = from_files.iter().cloned().collect();
    let mut queue: std::collections::VecDeque<String> = from_files.iter().cloned().collect();
    let mut hit: Option<String> = None;
    'bfs: while let Some(f) = queue.pop_front() {
        if to_files.contains(&f) {
            hit = Some(f);
            break 'bfs;
        }
        if let Some(neighbours) = adj.get(&f) {
            for n in neighbours {
                if visited.insert(n.clone()) {
                    prev.insert(n.clone(), f.clone());
                    queue.push_back(n.clone());
                }
            }
        }
    }
    let mut path = Vec::new();
    if let Some(end) = hit {
        let mut cur = end;
        loop {
            path.push(cur.clone());
            match prev.get(&cur) {
                Some(p) => cur = p.clone(),
                None => break,
            }
        }
        path.reverse();
    }
    SymbolTrace {
        from: from.to_string(),
        to: to.to_string(),
        length: path.len().saturating_sub(1),
        found: !path.is_empty(),
        path,
        generated_at: now(),
    }
}

/// Most-depended-on files (high inbound reference weight) — risky to touch.
/// `dependent_count` is the number of distinct files that depend on the target,
/// regardless of how many typed edges connect them.
pub fn compute_hotspots(graph: &SymbolGraph, limit: usize) -> Vec<Hotspot> {
    let mut inbound_weight: HashMap<String, usize> = HashMap::new();
    let mut dependents: HashMap<String, BTreeSet<String>> = HashMap::new();
    for e in &graph.edges {
        *inbound_weight.entry(e.to_path.clone()).or_insert(0) += e.weight;
        dependents
            .entry(e.to_path.clone())
            .or_default()
            .insert(e.from_path.clone());
    }
    let mut out: Vec<Hotspot> = inbound_weight
        .into_iter()
        .map(|(path, w)| {
            let d = dependents.get(&path).map(BTreeSet::len).unwrap_or(0);
            Hotspot {
                path,
                inbound_weight: w,
                dependent_count: d,
            }
        })
        .collect();
    out.sort_by(|a, b| {
        b.inbound_weight
            .cmp(&a.inbound_weight)
            .then(a.path.cmp(&b.path))
    });
    out.truncate(limit);
    out
}

#[derive(Debug, Clone, Serialize)]
pub struct BlastRadius {
    pub workspace_id: String,
    pub symbol: String,
    pub defined_in: Vec<String>,
    pub referencing_files: Vec<String>,
    pub referencing_file_count: usize,
    pub generated_at: String,
    /// Files that could not be read or fully extracted, so the answer may miss them.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub coverage_losses: Vec<CoverageLoss>,
}

/// What files reference a given symbol — the blast radius of changing it.
pub fn blast_radius(root: &Path, workspace_id: &str, symbol: &str) -> BlastRadius {
    let mut defined_in = Vec::new();
    let mut referencing = BTreeSet::new();
    let mut coverage_losses = Vec::new();
    let mut lose = |path: &str, reason: &str, detail: String| {
        coverage_losses.push(CoverageLoss {
            path: path.to_string(),
            reason: reason.to_string(),
            detail,
            content_indexed: false,
        })
    };
    let files: Vec<String> = match ls_files_stage(root) {
        Ok(entries) => entries
            .into_iter()
            .filter_map(|e| String::from_utf8(e.path).ok())
            .filter(|p| is_source(p))
            .collect(),
        Err(e) => {
            lose("", "git_unavailable", e.to_string());
            Vec::new()
        }
    };
    for rel in &files {
        let bytes = match read_source_beneath(root, Path::new(rel), MAX_REPO_FILE_BYTES) {
            Ok(b) => b,
            Err(SourceReadFailure::Missing) => continue,
            Err(f) => {
                lose(rel, f.reason(), f.detail());
                continue;
            }
        };
        let content = String::from_utf8_lossy(&bytes);
        if word_set(&content).contains(symbol) {
            referencing.insert(rel.clone());
        }
        let ex =
            repomap::extract_source_symbols(rel, &content, crate::treesitter::MAX_SYMBOLS_PER_FILE);
        if ex.truncated {
            lose(rel, "symbols_truncated", String::new());
        }
        if ex.symbols.iter().any(|s| s.symbol == symbol) {
            defined_in.push(rel.clone());
        }
    }
    for d in &defined_in {
        referencing.remove(d);
    }
    let referencing_files: Vec<String> = referencing.into_iter().collect();
    BlastRadius {
        workspace_id: workspace_id.to_string(),
        symbol: symbol.to_string(),
        defined_in,
        referencing_file_count: referencing_files.len(),
        referencing_files,
        generated_at: now(),
        coverage_losses,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    // The openat fd-walk refuses a symlink at EVERY path component (final file AND
    // intermediate directory), so a swap can't redirect the explain/symbols read into
    // a host file — the full check-to-open TOCTOU is closed (XM-PRO-007).
    #[test]
    #[cfg(unix)]
    fn read_repo_file_beneath_resists_symlinks_at_every_component() {
        use std::os::unix::fs::symlink;
        let root = TempDir::new().unwrap();
        std::fs::create_dir_all(root.path().join("sub/dir")).unwrap();
        std::fs::write(root.path().join("sub/dir/doc.txt"), "IN-REPO").unwrap();
        assert_eq!(
            read_repo_file_beneath(root.path(), "sub/dir/doc.txt").unwrap(),
            "IN-REPO"
        );

        let outside = TempDir::new().unwrap();
        std::fs::write(outside.path().join("secret.txt"), "SECRET").unwrap();

        // (1) final-component symlink to the secret -> refused
        symlink(
            outside.path().join("secret.txt"),
            root.path().join("sub/dir/link.txt"),
        )
        .unwrap();
        assert!(
            read_repo_file_beneath(root.path(), "sub/dir/link.txt").is_err(),
            "a symlink final component must be refused"
        );

        // (2) INTERMEDIATE-directory symlink: an interior component points outside.
        // The full fd-walk refuses it; a final-component-only check would NOT.
        symlink(outside.path(), root.path().join("evil")).unwrap();
        assert!(
            read_repo_file_beneath(root.path(), "evil/secret.txt").is_err(),
            "an intermediate-directory symlink must be refused (full fd-walk)"
        );

        // (3) ".." escape and absolute paths refused
        assert!(read_repo_file_beneath(root.path(), "../etc/hosts").is_err());
        assert!(read_repo_file_beneath(root.path(), "/etc/hosts").is_err());
    }

    // The single opener bounds the "huge file" hazard: a file over MAX_REPO_FILE_BYTES is
    // refused (not partially/silently truncated), and the streaming hash refuses it too.
    #[test]
    fn repo_reader_refuses_oversized_file() {
        let root = TempDir::new().unwrap();
        let small = vec![b'a'; 1024];
        std::fs::write(root.path().join("small.txt"), &small).unwrap();
        assert_eq!(
            read_repo_bytes_beneath_capped(root.path(), "small.txt", MAX_REPO_FILE_BYTES)
                .unwrap()
                .len(),
            1024
        );
        assert!(hash_repo_file_beneath(root.path(), "small.txt").is_some());

        let huge = vec![b'x'; (MAX_REPO_FILE_BYTES as usize) + 4096];
        std::fs::write(root.path().join("huge.bin"), &huge).unwrap();
        assert!(
            read_repo_file_beneath(root.path(), "huge.bin").is_err(),
            "a file over the cap must be refused, not truncated"
        );
        assert!(
            read_repo_bytes_beneath_capped(root.path(), "huge.bin", MAX_REPO_FILE_BYTES).is_err()
        );
        assert!(
            hash_repo_file_beneath(root.path(), "huge.bin").is_none(),
            "the streaming hash must refuse an over-cap file rather than hash unbounded"
        );
    }

    // A non-terminating target (a fifo with no writer) must be refused WITHOUT blocking:
    // O_NONBLOCK makes the open return immediately and the regular-file check rejects it.
    // If the defense regressed, this test would hang forever (the bound proves it doesn't).
    #[cfg(unix)]
    #[test]
    fn repo_reader_refuses_fifo_without_blocking() {
        let root = TempDir::new().unwrap();
        let fifo = root.path().join("pipe");
        let status = Command::new("mkfifo")
            .arg(&fifo)
            .status()
            .expect("mkfifo available");
        assert!(status.success(), "mkfifo failed");
        // No writer is ever opened; a blocking O_RDONLY open would hang here.
        assert!(
            read_repo_file_beneath(root.path(), "pipe").is_err(),
            "a fifo (non-regular, non-terminating) target must be refused"
        );
        assert!(hash_repo_file_beneath(root.path(), "pipe").is_none());
    }

    // hash_repo_file_beneath streams the SAME sha256 a single read-then-hash would
    // produce (continuity with existing drift baselines + index-cache keys), from the
    // one no-follow descriptor.
    #[test]
    fn repo_hash_matches_read_then_sha256_same_bytes() {
        use sha2::{Digest, Sha256};
        let root = TempDir::new().unwrap();
        let content = b"fn main() { println!(\"hi\"); }\n";
        std::fs::write(root.path().join("a.rs"), content).unwrap();
        let mut h = Sha256::new();
        h.update(content);
        let expected = format!("{:x}", h.finalize());
        assert_eq!(
            hash_repo_file_beneath(root.path(), "a.rs").unwrap(),
            expected
        );
    }

    // The repo_role classifier must match the SHARED golden spec (one source of truth
    // both Go and Rust pin to, so the code/test/doc/guide/config/other taxonomy and the
    // guidance set can't drift across the FFI). The matching Go test is
    // TestGuidanceRootsMatchGoldenSpec.
    #[test]
    fn repo_role_matches_golden_spec() {
        let spec = include_str!("testdata/repo_role_golden.tsv");
        let mut checked = 0;
        for (i, line) in spec.lines().enumerate() {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            let (path, role) = line
                .split_once('\t')
                .unwrap_or_else(|| panic!("golden line {} not tab-separated: {line:?}", i + 1));
            assert_eq!(
                repo_role(path.trim()),
                role.trim(),
                "repo_role({:?}) disagrees with the golden spec",
                path.trim()
            );
            checked += 1;
        }
        assert!(
            checked >= 20,
            "golden spec should pin many paths, got {checked}"
        );
    }

    // A directory target is not a regular file and must be refused by the read/hash path.
    #[test]
    fn repo_reader_refuses_directory_target() {
        let root = TempDir::new().unwrap();
        std::fs::create_dir_all(root.path().join("adir")).unwrap();
        assert!(read_repo_file_beneath(root.path(), "adir").is_err());
        assert!(hash_repo_file_beneath(root.path(), "adir").is_none());
    }

    fn git_repo(files: &[(&str, &str)]) -> TempDir {
        let dir = TempDir::new().unwrap();
        for (rel, content) in files {
            let path = dir.path().join(rel);
            if let Some(parent) = path.parent() {
                std::fs::create_dir_all(parent).unwrap();
            }
            std::fs::write(path, content).unwrap();
        }
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git")
                .arg("-C")
                .arg(dir.path())
                .args(&args)
                .output()
                .unwrap();
        }
        Command::new("git")
            .arg("-C")
            .arg(dir.path())
            .args(["add", "-A"])
            .output()
            .unwrap();
        Command::new("git")
            .arg("-C")
            .arg(dir.path())
            .args(["commit", "-qm", "c"])
            .output()
            .unwrap();
        dir
    }

    #[test]
    fn graph_builds_edges_and_hotspots() {
        let repo = git_repo(&[
            (
                "lib.rs",
                "pub fn compute_widget() -> i32 { 1 }\npub struct WidgetFactory {}\n",
            ),
            (
                "a.rs",
                "use crate::lib; fn run() { let _ = compute_widget(); let _f = WidgetFactory{}; }\n",
            ),
            ("b.rs", "fn other() { let _ = compute_widget(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        assert_eq!(graph.file_count, 3);
        assert!(graph.symbol_count >= 2);
        // a.rs and b.rs reference compute_widget defined in lib.rs -> edges to lib.rs
        assert!(
            graph
                .edges
                .iter()
                .any(|e| e.from_path == "a.rs" && e.to_path == "lib.rs")
        );
        assert!(
            graph
                .edges
                .iter()
                .any(|e| e.from_path == "b.rs" && e.to_path == "lib.rs")
        );
        let hot = compute_hotspots(&graph, 5);
        assert_eq!(
            hot[0].path, "lib.rs",
            "lib.rs should be the top hotspot: {hot:?}"
        );
        assert!(hot[0].dependent_count >= 2);
    }

    #[test]
    fn incremental_symbol_cache_is_correct_and_sets_authority() {
        let repo = git_repo(&[
            ("lib.rs", "pub fn helper() {}\n"),
            ("a.rs", "fn run() { helper(); }\n"),
            ("b.rs", "fn go() { helper(); }\n"),
        ]);
        // first build populates the per-file symbol cache.
        let g1 = build_symbol_graph(repo.path(), "ws");
        // second build reuses the cache — must produce the identical graph.
        let g2 = build_symbol_graph(repo.path(), "ws");
        assert_eq!(g1.symbol_count, g2.symbol_count);
        assert_eq!(g1.edge_count, g2.edge_count);
        // authority is precomputed: lib.rs is referenced by a.rs and b.rs.
        let lib = g2.files.iter().find(|f| f.path == "lib.rs").unwrap();
        assert!(
            lib.authority >= 2,
            "lib.rs authority should reflect 2 dependents: {lib:?}"
        );
    }

    #[test]
    fn symbol_impact_and_trace_traverse_the_graph() {
        // chain: core.rs defines `seed`; mid.rs calls seed; top.rs calls a mid symbol.
        let repo = git_repo(&[
            ("core.rs", "pub fn seed_value() -> i32 { 1 }\n"),
            ("mid.rs", "pub fn mid_layer() -> i32 { seed_value() }\n"),
            ("top.rs", "fn app() { let _ = mid_layer(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        // impact of seed_value reaches mid.rs (distance 1) and top.rs (distance 2).
        let impact = symbol_impact(&graph, "seed_value", 4);
        assert!(impact.defined_in.contains(&"core.rs".to_string()));
        let reached: Vec<&str> = impact.impacted.iter().map(|f| f.path.as_str()).collect();
        assert!(
            reached.contains(&"mid.rs"),
            "impact should reach mid.rs: {impact:?}"
        );
        // trace from seed_value to mid_layer finds a path.
        let trace = trace_symbols(&graph, "seed_value", "mid_layer");
        assert!(
            trace.found && trace.path.len() >= 2,
            "trace should find a path: {trace:?}"
        );
    }

    #[test]
    fn clusters_span_directories() {
        // core/engine.rs and api/handler.rs reference each other heavily (cross-dir);
        // they should land in one cluster, while an unrelated file stays separate —
        // which top-directory grouping could never produce.
        let repo = git_repo(&[
            (
                "core/engine.rs",
                "pub fn run_engine() {}\npub fn engine_step() {}\n",
            ),
            (
                "api/handler.rs",
                "fn handle() { run_engine(); engine_step(); }\npub fn dispatch() {}\n",
            ),
            (
                "core/engine_caller.rs",
                "fn go() { run_engine(); engine_step(); dispatch(); }\n",
            ),
            ("misc/lonely.rs", "pub fn alone() {}\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let clusters = compute_clusters(&graph);
        // find the cluster containing core/engine.rs
        let eng = clusters
            .iter()
            .find(|c| c.files.iter().any(|f| f == "core/engine.rs"))
            .unwrap();
        assert!(
            eng.files.iter().any(|f| f == "api/handler.rs"),
            "cross-directory coupled files should cluster together: {clusters:?}"
        );
        // the unrelated file is not in that cluster.
        assert!(!eng.files.iter().any(|f| f == "misc/lonely.rs"));
    }

    #[test]
    fn ambiguous_name_does_not_misroute_edges() {
        // `handle` is defined in BOTH a.rs and b.rs. The old first-definer-wins
        // routed every reference to whichever file was scanned first. Now the name
        // is ambiguous → no lexical edge is anchored on it, so c.rs does not get a
        // false edge to an arbitrary definer.
        let repo = git_repo(&[
            ("a.rs", "pub fn handle_request() {}\n"),
            ("b.rs", "pub fn handle_request() {}\n"),
            ("c.rs", "fn run() { handle_request(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let edges_from_c: Vec<_> = graph
            .edges
            .iter()
            .filter(|e| e.from_path == "c.rs")
            .collect();
        assert!(
            edges_from_c.is_empty(),
            "ambiguous handle_request must not anchor a misrouted edge: {edges_from_c:?}"
        );
        // a uniquely-defined symbol still resolves.
        assert!(graph.edges.iter().all(|e| e.resolution == "lexical"));
    }

    #[test]
    fn graph_classifies_edge_kinds() {
        let repo = git_repo(&[
            (
                "core.rs",
                "pub fn compute_widget() -> i32 { 1 }\npub trait Renderable {}\n",
            ),
            (
                "user.rs",
                "use crate::core::compute_widget;\nfn run() { let _ = compute_widget(); }\n",
            ),
            ("impl.rs", "struct Panel {}\nimpl Renderable for Panel {}\n"),
            ("core_test.rs", "fn check() { let _ = compute_widget(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let has = |from: &str, to: &str, kind: &str| {
            graph
                .edges
                .iter()
                .any(|e| e.from_path == from && e.to_path == to && e.kind == kind)
        };
        // user.rs imports + calls compute_widget from core.rs
        assert!(
            has("user.rs", "core.rs", "imports"),
            "imports edge: {:?}",
            graph.edges
        );
        assert!(
            has("user.rs", "core.rs", "calls"),
            "calls edge: {:?}",
            graph.edges
        );
        // impl.rs implements the Renderable trait defined in core.rs
        assert!(
            has("impl.rs", "core.rs", "inherits"),
            "inherits edge: {:?}",
            graph.edges
        );
        // core_test.rs is a test file referencing code under test
        assert!(
            has("core_test.rs", "core.rs", "tests"),
            "tests edge: {:?}",
            graph.edges
        );
    }

    #[test]
    fn index_coverage_reports_degraded_and_git_modes() {
        // a non-git directory yields an explicit degraded/empty coverage, not a
        // silent empty graph.
        let plain = TempDir::new().unwrap();
        std::fs::write(plain.path().join("a.rs"), "pub fn x() {}\n").unwrap();
        let empty = build_symbol_graph(plain.path(), "ws");
        let cov = &empty.coverage;
        assert!(empty.files.is_empty());
        assert_eq!(cov.repo_mode, "git-unavailable");
        assert!(cov.degraded_reason.is_some());
        assert!(!cov.complete);

        // a git repo reports git mode with eligible/indexed counts and no truncation.
        let repo = git_repo(&[("a.rs", "pub fn x() {}\n"), ("b.rs", "pub fn y() {}\n")]);
        let graph = build_symbol_graph(repo.path(), "ws");
        assert_eq!(graph.coverage.repo_mode, "git");
        assert_eq!(graph.coverage.eligible_files, 2);
        assert_eq!(graph.coverage.indexed_files, 2);
        assert!(!graph.coverage.truncated);
    }

    #[test]
    fn graph_emits_flow_edges() {
        let repo = git_repo(&[
            (
                "core.rs",
                "pub fn is_ready() -> bool { true }\npub fn make_widget() -> i32 { 7 }\n",
            ),
            (
                "user.rs",
                "use crate::core::{is_ready, make_widget};\n\
                 fn run() -> i32 {\n\
                 \x20   if is_ready() {\n\
                 \x20       return make_widget();\n\
                 \x20   }\n\
                 \x20   0\n\
                 }\n",
            ),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let has_flow = |from: &str, to: &str, kind: &str| {
            graph
                .flow_edges
                .iter()
                .any(|e| e.from_path == from && e.to_path == to && e.kind == kind)
        };
        // `if is_ready()` → a control-flow BRANCH edge on is_ready (core.rs).
        assert!(
            has_flow("user.rs", "core.rs", "branches"),
            "branches edge expected: {:?}",
            graph.flow_edges
        );
        // `return make_widget()` → a RETURNS data-flow edge on make_widget (core.rs).
        assert!(
            has_flow("user.rs", "core.rs", "returns"),
            "returns edge expected: {:?}",
            graph.flow_edges
        );
        // flow edges are tagged with a resolution like the structural edges (S2).
        assert!(graph.flow_edges.iter().all(|e| e.resolution == "lexical"));
        // flow edges must NOT pollute the structural edge set / authority weight.
        assert!(
            graph
                .edges
                .iter()
                .all(|e| e.kind != "returns" && e.kind != "branches")
        );
        assert_eq!(graph.flow_edge_count, graph.flow_edges.len());
    }

    #[test]
    fn flow_edge_kind_classifies_contexts() {
        // unit-level checks of the per-line classifier.
        let probe = |line: &str, sym: &str| {
            let start = line.find(sym).unwrap();
            flow_edge_kind(line, start, start + sym.len())
        };
        assert_eq!(
            probe("    return make_widget();", "make_widget"),
            Some("returns")
        );
        assert_eq!(probe("    if is_ready() {", "is_ready"), Some("branches"));
        assert_eq!(probe("    while pending() {", "pending"), Some("branches"));
        assert_eq!(probe("    CONFIG = load();", "CONFIG"), Some("writes"));
        assert_eq!(probe("    total += amount;", "total"), Some("writes"));
        // a plain call/read is NOT a flow edge (covered by calls/references).
        assert_eq!(probe("    let x = helper();", "helper"), None);
        // `if` must not match inside another identifier.
        assert_eq!(probe("    let notify = thing();", "thing"), None);
    }

    // Source output must reflect the identity it reports: an edit between computing the
    // identity and reading the file makes the build unstable, uncached and labeled.
    #[test]
    fn edit_during_build_is_unstable_and_not_cached() {
        use crate::indexcache::{cache_scope, source_identity};
        let repo = git_repo(&[("a.rs", "pub fn before_edit() {}\n")]);
        std::fs::write(repo.path().join("a.rs"), "pub fn dirty_one() {}\n").unwrap();
        let id = source_identity(repo.path());
        let scope = cache_scope(&id).unwrap();
        // the file changes after the identity was taken, before the build reads it.
        std::fs::write(repo.path().join("a.rs"), "pub fn dirty_two() {}\n").unwrap();
        let work = IndexWork {
            graph_cache: "miss".into(),
            ..Default::default()
        };
        let g = build_graph(
            repo.path(),
            "ws",
            &id,
            Some(&scope),
            true,
            work,
            std::time::Instant::now(),
        );
        let c = &g.coverage;
        assert!(!c.source_identity.stable && !c.complete);
        assert_eq!(c.work.graph_cache, "bypass");
        assert!(
            c.degraded_reason
                .as_deref()
                .unwrap()
                .contains("source changed")
        );
        assert!(
            scope.load_graph(&id.key).is_none(),
            "unstable graph was cached"
        );
        // the next call sees a consistent tree and caches it.
        let g2 = build_symbol_graph_cached(repo.path(), "ws");
        assert!(g2.coverage.source_identity.stable);
        assert!(g2.symbols.iter().any(|s| s.name == "dirty_two"));
    }

    // line_flows (production) must classify exactly like flow_edge_kind (reference)
    // for every identifier it reports and every one it skips.
    #[test]
    fn line_flows_matches_reference_classifier() {
        let lines = [
            "    return make_widget(total_count);",
            "    if is_ready() && other_flag { run_task(); }",
            "    while pending_work() { value_sum += step_size; }",
            "    CONFIG_VALUE = load_config();",
            "    let notify_user = thing_maker();",
            "    x9return some_value; 9return other_value; match_arm = case_value;",
            "    // step 3: accumulate value for f000_001 when ready",
            "    elif branch_cond == target_value: switch_mode = 1",
            "    café_name = résumé_value; return naïve_value",
        ];
        for line in lines {
            let got: Vec<(&str, &str)> = line_flows(line);
            let mut want = Vec::new();
            for (word, start, end) in identifier_spans(line) {
                if word.len() < MIN_NAME_LEN {
                    continue;
                }
                if let Some(k) = flow_edge_kind(line, start, end) {
                    want.push((word, k));
                }
            }
            assert_eq!(got, want, "line: {line:?}");
        }
    }

    #[test]
    fn blast_radius_finds_referencers() {
        let repo = git_repo(&[
            ("lib.rs", "pub fn compute_widget() {}\n"),
            ("a.rs", "fn run() { compute_widget(); }\n"),
            ("b.rs", "fn nope() {}\n"),
        ]);
        let br = blast_radius(repo.path(), "ws", "compute_widget");
        assert_eq!(br.defined_in, vec!["lib.rs".to_string()]);
        assert!(br.referencing_files.contains(&"a.rs".to_string()));
        assert!(!br.referencing_files.contains(&"b.rs".to_string()));
    }
}
