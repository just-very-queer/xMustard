//! Persistent stat cache for tracked-file content hashes.
//!
//! `changetrack` fingerprints a repository by the SHA-256 of every tracked file. This
//! cache stores each hash with the stat key of the descriptor it was read from (size,
//! mtime and ctime in nanoseconds, inode, device, mode), so a later pass reads only files
//! that are new, whose key changed, or whose entry is racy.
//!
//! Racy entries (Git's racy-index problem): a file rewritten within the timestamp
//! granularity of its recorded stat can keep an identical key. An entry is trusted only
//! when the file's newest timestamp (mtime or ctime) is older than the recording pass's
//! start minus `RACY_WINDOW`. File timestamps come from the filesystem's clock, which can
//! differ from the host's (a network share whose server clock lags), so the pass start is
//! the earlier of two readings: the host clock when the pass began, and the filesystem's
//! clock at that moment, which is the cache file's own mtime minus the pass's duration.
//! Git compares against its index file's mtime; the cache file stands in for the index
//! here, and the cutoff is never later than its mtime, so this distrusts at least
//! everything Git's rule would, whichever clock is off. Like Git, it assumes the cache
//! (in the Git dir) and the worktree share a clock. A racy entry is re-hashed on the next
//! pass and becomes trusted once a later pass records it outside the window.
//!
//! The cache is `<git-dir>/xmustard-cache/filehash-v1/<scope>.bin`, where the scope hashes
//! the canonical root. It is written atomically under an advisory lock, bounded to
//! `MAX_CACHE_BYTES` and checksummed; a missing, oversized or corrupt file means a full
//! hashing pass. Files are opened through the same no-follow, regular-file-checked
//! descriptor as `hash_repo_file_beneath`, and the key is that descriptor's fstat taken
//! before reading, so the hashes equal hashing every file directly.

use std::collections::{BTreeMap, HashMap};
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use sha2::{Digest, Sha256};

use crate::indexcache;
use crate::symbolgraph::{self, MAX_REPO_FILE_BYTES};

/// File magic and format version.
const MAGIC: &[u8; 8] = b"XMFHASH1";

/// Largest cache file read or written. Past it the pass hashes every file directly.
pub(crate) const MAX_CACHE_BYTES: u64 = 64 << 20;

/// Entries whose newest timestamp is this close to (or after) the recording pass's start
/// are re-hashed. Two seconds covers one-second and FAT's two-second granularity.
pub(crate) const RACY_WINDOW: Duration = Duration::from_secs(2);

/// Bound on waiting for another writer of the same cache file. Writes take
/// milliseconds; the wait also rides out a lock descriptor briefly inherited by a
/// child that a sibling thread is spawning.
const STORE_LOCK_WAIT: Duration = Duration::from_millis(250);

/// Magic + pass start (host clock) + entry count.
const HEADER_BYTES: usize = 8 + 8 + 8;
/// Size, mtime, ctime, inode, device + mode + SHA-256; each entry adds its path length
/// (4 bytes) and path.
const ENTRY_KEY_BYTES: usize = 5 * 8 + 4 + 32;
const ENTRY_FIXED_BYTES: usize = 4 + ENTRY_KEY_BYTES;
/// Pass duration, written after the entries.
const DURATION_BYTES: usize = 8;
const CHECKSUM_BYTES: usize = 32;

/// Counters for one hashing pass.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct HashPass {
    /// Files whose bytes were read this pass (new, changed, racy, or no usable cache).
    pub hashed: usize,
    /// Bytes those files held.
    pub bytes_hashed: u64,
    /// Files left out because reading them would pass the pass's byte budget.
    pub over_budget: usize,
    /// Files whose hash came from the cache without reading them.
    pub reused: usize,
    /// Cached entries whose stat key matched but fell inside the racy window.
    pub racy: usize,
    /// A valid cache file was loaded.
    pub cache_loaded: bool,
    /// The cache file was rewritten.
    pub cache_written: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct StatKey {
    size: u64,
    mtime_ns: i64,
    ctime_ns: i64,
    ino: u64,
    dev: u64,
    mode: u32,
}

impl StatKey {
    #[cfg(unix)]
    fn of(meta: &fs::Metadata) -> Self {
        use std::os::unix::fs::MetadataExt;
        let ns = |secs: i64, nsec: i64| secs.saturating_mul(1_000_000_000).saturating_add(nsec);
        StatKey {
            size: meta.size(),
            mtime_ns: ns(meta.mtime(), meta.mtime_nsec()),
            ctime_ns: ns(meta.ctime(), meta.ctime_nsec()),
            ino: meta.ino(),
            dev: meta.dev(),
            mode: meta.mode(),
        }
    }

    /// Without ctime or an inode the key is size + mtime; an unknown mtime is never
    /// trusted.
    #[cfg(not(unix))]
    fn of(meta: &fs::Metadata) -> Self {
        let mtime_ns = meta.modified().ok().and_then(unix_ns).unwrap_or(i64::MAX);
        StatKey {
            size: meta.len(),
            mtime_ns,
            ctime_ns: mtime_ns,
            ino: 0,
            dev: 0,
            mode: 0,
        }
    }

    fn newest_ns(&self) -> i64 {
        self.mtime_ns.max(self.ctime_ns)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Entry {
    key: StatKey,
    digest: [u8; 32],
}

#[derive(Debug, Default)]
struct StatCache {
    /// Start of the pass that recorded these entries, in Unix nanoseconds: the earlier of
    /// the host and filesystem readings (see the module doc).
    recorded_at_ns: i64,
    entries: HashMap<String, Entry>,
}

enum Loaded {
    Absent,
    Invalid,
    Valid(StatCache),
}

/// The start of a hashing pass: the host clock, and a monotonic mark for its duration.
struct PassStart {
    unix_ns: i64,
    mono: Instant,
}

impl PassStart {
    fn now() -> Self {
        PassStart {
            unix_ns: now_ns(),
            mono: Instant::now(),
        }
    }

    /// Time since the pass started. The longer of the monotonic and wall-clock readings,
    /// so neither a suspended machine nor a clock step shortens it (a shorter duration
    /// would move the filesystem reading of the start later).
    fn elapsed_ns(&self) -> i64 {
        let mono = i64::try_from(self.mono.elapsed().as_nanos()).unwrap_or(i64::MAX);
        mono.max(now_ns().saturating_sub(self.unix_ns))
    }
}

/// SHA-256 (lowercase hex) of every path in `rels` that hashes through the no-follow
/// opener, as hashing each file directly would return, using and refreshing `cache`.
/// With no cache path every file is read.
pub fn hash_files(
    root: &Path,
    rels: Vec<String>,
    cache: Option<&Path>,
) -> (BTreeMap<String, String>, HashPass) {
    hash_files_with(root, rels, cache, RACY_WINDOW)
}

/// `hash_files` for a pass that may read at most `budget` bytes: `rels` are taken in
/// the given order (the caller's priority), a file that is not reused from the cache
/// and does not fit what is left is not read and is left out of the map (counted in
/// `over_budget`). Reused entries cost nothing. The source identity hashes the dirty
/// and untracked files `git status` lists this way (PAR-FRESH-01), so an unchanged
/// dirty file is not read again.
pub fn hash_files_budgeted(
    root: &Path,
    rels: Vec<String>,
    cache: Option<&Path>,
    budget: u64,
) -> (BTreeMap<String, String>, HashPass) {
    hash_files_inner(root, rels, cache, RACY_WINDOW, Some(budget))
}

/// The stat-cache file for `root`, or None outside Git.
pub fn cache_file(root: &Path) -> Option<PathBuf> {
    scoped_cache_file(root, "")
}

/// The stat-cache file of the source identity's pass over the dirty and untracked
/// files of the worktree `layout` describes (top-level relative paths). A file of its
/// own, because a pass keeps only the entries it was given.
pub fn identity_cache_file_for(layout: &indexcache::GitLayout) -> PathBuf {
    scope_file(layout, &layout.toplevel, "identity")
}

fn scoped_cache_file(root: &Path, kind: &str) -> Option<PathBuf> {
    let layout = indexcache::git_layout(root)?;
    let canonical = fs::canonicalize(root).ok()?;
    Some(scope_file(&layout, &canonical, kind))
}

fn scope_file(layout: &indexcache::GitLayout, root: &Path, kind: &str) -> PathBuf {
    let mut h = Sha256::new();
    h.update(b"xm-filehash-scope-v1\0");
    h.update(root.as_os_str().as_encoded_bytes());
    if !kind.is_empty() {
        h.update(b"\0");
        h.update(kind.as_bytes());
    }
    let scope = symbolgraph::hex_lower(&h.finalize()[..16]);
    indexcache::xmustard_cache_dir(layout)
        .join("filehash-v1")
        .join(format!("{scope}.bin"))
}

fn hash_files_with(
    root: &Path,
    rels: Vec<String>,
    cache: Option<&Path>,
    racy_window: Duration,
) -> (BTreeMap<String, String>, HashPass) {
    hash_files_inner(root, rels, cache, racy_window, None)
}

fn hash_files_inner(
    root: &Path,
    mut rels: Vec<String>,
    cache: Option<&Path>,
    racy_window: Duration,
    budget: Option<u64>,
) -> (BTreeMap<String, String>, HashPass) {
    // Taken before any stat. The filesystem reading subtracts the pass's duration, so
    // neither reading of the start depends on how long hashing took.
    let start = PassStart::now();
    // `git ls-files` lists an unmerged path once per conflict stage: every path is
    // hashed once. Without a budget the paths are sorted up front, so rows come out in
    // map order; with one they keep the caller's order and are sorted at the end.
    let mut seen = std::collections::HashSet::new();
    match budget {
        None => {
            rels.sort_unstable();
            rels.dedup();
        }
        Some(_) => rels.retain(|r| seen.insert(r.clone())),
    }
    drop(seen);
    let mut left = budget.unwrap_or(u64::MAX);
    let mut pass = HashPass::default();
    let (mut old, replace_invalid) = match cache.map(load) {
        Some(Loaded::Valid(c)) => {
            pass.cache_loaded = true;
            (c, false)
        }
        Some(Loaded::Invalid) => (StatCache::default(), true),
        Some(Loaded::Absent) | None => (StatCache::default(), false),
    };
    let window_ns = i64::try_from(racy_window.as_nanos()).unwrap_or(i64::MAX);
    let trusted_before = old.recorded_at_ns.saturating_sub(window_ns);

    let mut rows: Vec<(String, Entry)> = Vec::with_capacity(rels.len());
    let mut entries_changed = false;
    for rel in rels {
        // a path skipped here keeps its old entry in `old`, which marks the cache stale.
        let Ok((file, meta)) = symbolgraph::open_repo_regular_file_with_meta(root, &rel) else {
            continue;
        };
        if meta.len() > MAX_REPO_FILE_BYTES {
            continue; // the streaming hash would refuse it too; skip the read
        }
        let key = StatKey::of(&meta);
        let prev = old.entries.remove(rel.as_str());
        let cached = prev.filter(|e| e.key == key);
        let digest = match cached {
            Some(e) if key.newest_ns() < trusted_before => {
                pass.reused += 1;
                e.digest
            }
            _ if meta.len() > left => {
                // not read: the caller reports it; its old entry is dropped
                pass.over_budget += 1;
                entries_changed |= prev.is_some();
                continue;
            }
            _ => {
                if cached.is_some() {
                    pass.racy += 1;
                }
                pass.hashed += 1;
                pass.bytes_hashed += meta.len();
                left -= meta.len();
                let Some(digest) = symbolgraph::sha256_open_file(file) else {
                    entries_changed |= prev.is_some();
                    continue;
                };
                digest
            }
        };
        let entry = Entry { key, digest };
        entries_changed |= prev != Some(entry);
        rows.push((rel, entry));
    }
    if budget.is_some() {
        rows.sort_unstable_by(|a, b| a.0.cmp(&b.0));
    }
    let mut map = BTreeMap::new();
    let mut entries: Vec<Entry> = Vec::with_capacity(rows.len());
    for (rel, entry) in rows {
        map.insert(rel, symbolgraph::hex_lower(&entry.digest));
        entries.push(entry);
    }
    // Entries still in `old` belong to paths that left the tracked set or can no longer
    // be hashed. A racy re-hash is rewritten even when identical: the newer pass start is
    // what lets the entry become trusted.
    let removed = !old.entries.is_empty();
    drop(old);
    if let Some(path) = cache
        && (replace_invalid || entries_changed || removed || pass.racy > 0)
    {
        let rows = map.keys().map(String::as_str).zip(entries.iter());
        pass.cache_written = store(path, &start, rows);
    }
    (map, pass)
}

fn unix_ns(t: SystemTime) -> Option<i64> {
    let d = t.duration_since(UNIX_EPOCH).ok()?;
    i64::try_from(d.as_nanos()).ok()
}

fn now_ns() -> i64 {
    unix_ns(SystemTime::now()).unwrap_or(0)
}

fn load(path: &Path) -> Loaded {
    let Ok(f) = fs::File::open(path) else {
        return Loaded::Absent;
    };
    let meta = match f.metadata() {
        Ok(meta) if meta.is_file() && meta.len() <= MAX_CACHE_BYTES => meta,
        _ => return Loaded::Invalid,
    };
    // the filesystem's clock when the file was written; unknown means trust nothing.
    let written_ns = meta.modified().ok().and_then(unix_ns).unwrap_or(i64::MIN);
    decode(io::BufReader::new(f), meta.len(), written_ns).map_or(Loaded::Invalid, Loaded::Valid)
}

/// Write `rows` (path, entry) as the cache (best effort). Every entry is re-validated by
/// stat on read, so a concurrent pass's last write is equally correct; the lock only
/// serializes writers.
fn store<'a, I>(path: &Path, start: &PassStart, rows: I) -> bool
where
    I: ExactSizeIterator<Item = (&'a str, &'a Entry)> + Clone,
{
    let Some(dir) = path.parent() else {
        return false;
    };
    let size = rows.clone().fold(
        (HEADER_BYTES + DURATION_BYTES + CHECKSUM_BYTES) as u64,
        |n, (p, _)| n.saturating_add((ENTRY_FIXED_BYTES + p.len()) as u64),
    );
    if size > MAX_CACHE_BYTES {
        // too many tracked files to cache: drop an older file instead of rereading it.
        let _ = fs::remove_file(path);
        return false;
    }
    if fs::create_dir_all(dir).is_err() {
        return false;
    }
    let (Some(_lock), _) = indexcache::lock_file(&path.with_extension("lock"), STORE_LOCK_WAIT)
    else {
        return false;
    };
    let written = indexcache::atomic_write_with(path, |w| encode_into(w, start, rows)).is_ok();
    indexcache::sweep_stale_temps(dir, indexcache::STALE_TEMP_AGE);
    written
}

/// Stream the cache file into `out`: header, entries, the pass duration measured once
/// the entries are written (so the file's mtime minus it reads the pass start on the
/// filesystem's clock), then the SHA-256 of all of it.
fn encode_into<'a>(
    out: &mut dyn Write,
    start: &PassStart,
    rows: impl ExactSizeIterator<Item = (&'a str, &'a Entry)>,
) -> io::Result<()> {
    let mut w = HashingWriter {
        inner: out,
        hasher: Sha256::new(),
    };
    w.write_all(MAGIC)?;
    w.write_all(&start.unix_ns.to_le_bytes())?;
    w.write_all(&(rows.len() as u64).to_le_bytes())?;
    for (path, e) in rows {
        let len = u32::try_from(path.len()).map_err(|_| io::ErrorKind::InvalidInput)?;
        w.write_all(&len.to_le_bytes())?;
        w.write_all(path.as_bytes())?;
        let k = &e.key;
        let mut fixed = [0u8; ENTRY_KEY_BYTES];
        let words = [k.size, k.mtime_ns as u64, k.ctime_ns as u64, k.ino, k.dev];
        for (slot, v) in fixed.chunks_exact_mut(8).zip(words) {
            slot.copy_from_slice(&v.to_le_bytes());
        }
        fixed[40..44].copy_from_slice(&k.mode.to_le_bytes());
        fixed[44..].copy_from_slice(&e.digest);
        w.write_all(&fixed)?;
    }
    w.write_all(&start.elapsed_ns().to_le_bytes())?;
    let checksum = w.hasher.finalize();
    w.inner.write_all(&checksum)
}

/// Passes writes through while hashing exactly the bytes the inner writer accepted.
struct HashingWriter<'a> {
    inner: &'a mut dyn Write,
    hasher: Sha256,
}

impl Write for HashingWriter<'_> {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        let n = self.inner.write(buf)?;
        self.hasher.update(&buf[..n]);
        Ok(n)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}

/// Parse the first `len` bytes of a cache file whose mtime (Unix ns) is `written_ns`,
/// streaming them rather than holding the whole file; None when the checksum, magic,
/// counts or lengths disagree. Nothing is returned before the checksum matches, and
/// every allocation is bounded by `len`.
fn decode(mut input: impl Read, len: u64, written_ns: i64) -> Option<StatCache> {
    let fixed = (HEADER_BYTES + DURATION_BYTES + CHECKSUM_BYTES) as u64;
    let room = len.checked_sub(fixed)?;
    let mut r = HashingReader {
        inner: (&mut input).take(len - CHECKSUM_BYTES as u64),
        hasher: Sha256::new(),
    };
    if &read_array::<8>(&mut r)? != MAGIC {
        return None;
    }
    let started_ns = i64::from_le_bytes(read_array(&mut r)?);
    let count = u64::from_le_bytes(read_array(&mut r)?);
    // every entry takes at least ENTRY_FIXED_BYTES, so a bad count cannot over-allocate.
    if count > room / ENTRY_FIXED_BYTES as u64 {
        return None;
    }
    let mut entries = HashMap::with_capacity(count as usize);
    for _ in 0..count {
        let path_len = u32::from_le_bytes(read_array(&mut r)?);
        if u64::from(path_len) > r.inner.limit() {
            return None;
        }
        let mut path = vec![0; path_len as usize];
        r.read_exact(&mut path).ok()?;
        let path = String::from_utf8(path).ok()?;
        let key = StatKey {
            size: u64::from_le_bytes(read_array(&mut r)?),
            mtime_ns: i64::from_le_bytes(read_array(&mut r)?),
            ctime_ns: i64::from_le_bytes(read_array(&mut r)?),
            ino: u64::from_le_bytes(read_array(&mut r)?),
            dev: u64::from_le_bytes(read_array(&mut r)?),
            mode: u32::from_le_bytes(read_array(&mut r)?),
        };
        let digest = read_array(&mut r)?;
        entries.insert(path, Entry { key, digest });
    }
    let elapsed_ns = i64::from_le_bytes(read_array(&mut r)?).max(0);
    if r.inner.limit() != 0 {
        return None;
    }
    let body_sum = r.hasher.finalize();
    let checksum: [u8; CHECKSUM_BYTES] = read_array(&mut input)?;
    if checksum[..] != body_sum[..] {
        return None;
    }
    Some(StatCache {
        recorded_at_ns: started_ns.min(written_ns.saturating_sub(elapsed_ns)),
        entries,
    })
}

fn read_array<const N: usize>(r: &mut impl Read) -> Option<[u8; N]> {
    let mut out = [0u8; N];
    r.read_exact(&mut out).ok()?;
    Some(out)
}

/// Passes reads through while hashing exactly the bytes returned.
struct HashingReader<R> {
    inner: R,
    hasher: Sha256,
}

impl<R: Read> Read for HashingReader<R> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        let n = self.inner.read(buf)?;
        self.hasher.update(&buf[..n]);
        Ok(n)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    fn write(root: &Path, rel: &str, body: &[u8]) {
        fs::write(root.join(rel), body).unwrap();
    }

    fn rels(names: &[&str]) -> Vec<String> {
        names.iter().map(|s| s.to_string()).collect()
    }

    /// What hashing each file directly (the pre-cache behavior) returns.
    fn direct(root: &Path, names: &[&str]) -> BTreeMap<String, String> {
        names
            .iter()
            .filter_map(|n| {
                symbolgraph::hash_repo_file_beneath(root, n).map(|h| (n.to_string(), h))
            })
            .collect()
    }

    fn sha_hex(bytes: &[u8]) -> String {
        symbolgraph::hex_lower(&Sha256::digest(bytes))
    }

    fn cached_paths(cache: &Path) -> Vec<String> {
        let Loaded::Valid(c) = load(cache) else {
            panic!("cache file is not valid")
        };
        let mut paths: Vec<String> = c.entries.into_keys().collect();
        paths.sort();
        paths
    }

    /// Outlast a coarse filesystem timestamp tick, so a pass recorded after this starts
    /// strictly after the files' timestamps on the filesystem's clock too.
    fn settle() {
        std::thread::sleep(Duration::from_millis(50));
    }

    /// Record `rows` as a pass that started at `started_ns`. The cache file's mtime is
    /// then moved an hour ahead so the filesystem reading (mtime minus the recorded
    /// duration) is later, which leaves the host start as the cutoff.
    fn store_at(cache: &Path, started_ns: i64, rows: &[(&str, &Entry)]) {
        let start = PassStart {
            unix_ns: started_ns,
            mono: Instant::now(),
        };
        assert!(store(cache, &start, rows.iter().copied()));
        fs::File::options()
            .write(true)
            .open(cache)
            .unwrap()
            .set_modified(SystemTime::now() + Duration::from_secs(3600))
            .unwrap();
    }

    fn decode_bytes(bytes: &[u8], written_ns: i64) -> Option<StatCache> {
        decode(bytes, bytes.len() as u64, written_ns)
    }

    fn encode(start: &PassStart, rows: &[(&str, &Entry)]) -> Vec<u8> {
        let mut out = Vec::new();
        encode_into(&mut out, start, rows.iter().copied()).unwrap();
        out
    }

    fn sample_entry() -> Entry {
        Entry {
            key: StatKey {
                size: 1,
                mtime_ns: 2,
                ctime_ns: 3,
                ino: 4,
                dev: 5,
                mode: 6,
            },
            digest: [7; 32],
        }
    }

    fn setup(names: &[&str]) -> (TempDir, TempDir, PathBuf) {
        let repo = TempDir::new().unwrap();
        for n in names {
            write(repo.path(), n, format!("content of {n}\n").as_bytes());
        }
        let store_dir = TempDir::new().unwrap();
        let cache = store_dir.path().join("filehash-v1").join("scope.bin");
        (repo, store_dir, cache)
    }

    // A zero racy window trusts any entry recorded after the file's last change, so a
    // second pass over an untouched tree must not read a single file.
    #[test]
    fn unchanged_tree_rehashes_zero_files() {
        let names = ["a.rs", "b.rs", " lead space.rs"];
        let (repo, _s, cache) = setup(&names);
        settle();
        let (m1, p1) = hash_files_with(repo.path(), rels(&names), Some(&cache), Duration::ZERO);
        assert_eq!((p1.hashed, p1.reused, p1.cache_loaded), (3, 0, false));
        assert!(p1.cache_written && cache.exists());
        let (m2, p2) = hash_files_with(repo.path(), rels(&names), Some(&cache), Duration::ZERO);
        assert_eq!((p2.hashed, p2.reused, p2.racy), (0, 3, 0), "{p2:?}");
        assert!(p2.cache_loaded && !p2.cache_written, "{p2:?}");
        assert_eq!(m1, direct(repo.path(), &names));
        assert_eq!(m2, m1);
    }

    // Files changed just before a pass sit in the racy window, so their cached hashes
    // are not trusted on the very next pass even though the stat keys match.
    #[test]
    fn freshly_written_files_are_racy_and_rehashed() {
        let names = ["a.rs", "b.rs"];
        let (repo, _s, cache) = setup(&names);
        let (_, p1) = hash_files(repo.path(), rels(&names), Some(&cache));
        assert_eq!(p1.hashed, 2);
        let (m2, p2) = hash_files(repo.path(), rels(&names), Some(&cache));
        assert_eq!((p2.hashed, p2.racy, p2.reused), (2, 2, 0), "{p2:?}");
        assert!(
            p2.cache_written,
            "a racy re-hash records the newer pass start"
        );
        assert_eq!(m2, direct(repo.path(), &names));
    }

    // Racy guard in isolation: an entry whose stat key matches the file exactly but
    // carries a stale digest is re-hashed when the file's timestamp is within the
    // window of the recording pass, and (control) served when it is outside.
    #[test]
    fn identical_stat_key_inside_racy_window_is_rehashed() {
        let (repo, _s, cache) = setup(&["m.rs"]);
        let key = StatKey::of(&fs::metadata(repo.path().join("m.rs")).unwrap());
        let stale = Entry {
            key,
            digest: [0xAA; 32],
        };
        let real = sha_hex(b"content of m.rs\n");
        let second = 1_000_000_000;
        for recorded_at in [key.newest_ns(), key.newest_ns() + 900_000_000] {
            store_at(&cache, recorded_at, &[("m.rs", &stale)]);
            let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
            assert_eq!(m["m.rs"], real, "racy entry served a stale hash");
            assert_eq!((p.racy, p.hashed, p.reused), (1, 1, 0), "{p:?}");
        }
        store_at(&cache, key.newest_ns() + 3 * second, &[("m.rs", &stale)]);
        let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        assert_eq!(
            m["m.rs"],
            "aa".repeat(32),
            "outside the window the key is trusted"
        );
        assert_eq!((p.racy, p.hashed, p.reused), (0, 0, 1), "{p:?}");
    }

    // A same-size rewrite that restores the old mtime (what `touch -r`, rsync -t or a
    // formatter can do) keeps size, mtime and inode, so only ctime can reject the entry.
    // The racy guard is out of play: zero window, and the entry is recorded after the
    // file's timestamps, so a key without ctime would serve the stale hash.
    #[test]
    fn restored_mtime_edit_is_caught_by_ctime_alone() {
        let (repo, _s, cache) = setup(&[]);
        let path = repo.path().join("m.rs");
        write(repo.path(), "m.rs", b"aaaa\n");
        let before = StatKey::of(&fs::metadata(&path).unwrap());
        settle();
        let (m1, _) = hash_files_with(repo.path(), rels(&["m.rs"]), Some(&cache), Duration::ZERO);
        let Loaded::Valid(recorded) = load(&cache) else {
            panic!("cache not written")
        };
        assert!(
            before.mtime_ns < recorded.recorded_at_ns,
            "the racy guard alone would re-hash this entry"
        );
        settle();
        write(repo.path(), "m.rs", b"bbbb\n");
        let mtime = SystemTime::UNIX_EPOCH + Duration::from_nanos(before.mtime_ns as u64);
        fs::File::options()
            .write(true)
            .open(&path)
            .unwrap()
            .set_modified(mtime)
            .unwrap();
        let after = StatKey::of(&fs::metadata(&path).unwrap());
        assert_eq!(
            (after.size, after.mtime_ns, after.ino),
            (before.size, before.mtime_ns, before.ino)
        );
        assert!(after.ctime_ns > before.ctime_ns, "ctime did not move");
        let (m2, p2) = hash_files_with(repo.path(), rels(&["m.rs"]), Some(&cache), Duration::ZERO);
        assert_eq!(m1["m.rs"], sha_hex(b"aaaa\n"));
        assert_eq!(m2["m.rs"], sha_hex(b"bbbb\n"), "same-size edit missed");
        assert_eq!((p2.hashed, p2.reused, p2.racy), (1, 0, 0), "{p2:?}");
    }

    // The cutoff also reads the filesystem's clock. A pass start from a host clock an
    // hour ahead of the filesystem (a share whose server lags) must not make a file
    // written just now look old; the cache file's own mtime bounds the start.
    #[test]
    fn host_clock_ahead_of_the_filesystem_does_not_trust_fresh_files() {
        let (repo, _s, cache) = setup(&["m.rs"]);
        let key = StatKey::of(&fs::metadata(repo.path().join("m.rs")).unwrap());
        let stale = Entry {
            key,
            digest: [0xAA; 32],
        };
        let ahead = || PassStart {
            unix_ns: now_ns() + 3_600_000_000_000,
            mono: Instant::now(),
        };
        assert!(store(&cache, &ahead(), [("m.rs", &stale)].into_iter()));
        let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        assert_eq!(
            m["m.rs"],
            sha_hex(b"content of m.rs\n"),
            "stale hash served"
        );
        assert_eq!((p.racy, p.hashed, p.reused), (1, 1, 0), "{p:?}");
        // control: once the filesystem reading is an hour on as well, the entry is old
        // on both clocks and is served.
        store_at(&cache, ahead().unix_ns, &[("m.rs", &stale)]);
        let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        assert_eq!(m["m.rs"], "aa".repeat(32));
        assert_eq!((p.racy, p.hashed, p.reused), (0, 0, 1), "{p:?}");
    }

    // `git ls-files` lists an unmerged path once per conflict stage. Each path is hashed
    // and stored once, so the cache settles instead of being rewritten on every call.
    #[test]
    fn duplicate_paths_are_hashed_once_and_the_cache_settles() {
        let (repo, _s, cache) = setup(&["a.rs", "b.rs"]);
        settle();
        let listed = rels(&["a.rs", "a.rs", "b.rs", "a.rs"]);
        let (m1, p1) = hash_files_with(repo.path(), listed.clone(), Some(&cache), Duration::ZERO);
        assert_eq!((p1.hashed, p1.cache_written), (2, true), "{p1:?}");
        let two_entries = HEADER_BYTES + 2 * (ENTRY_FIXED_BYTES + 4) + DURATION_BYTES;
        assert_eq!(
            fs::metadata(&cache).unwrap().len(),
            (two_entries + CHECKSUM_BYTES) as u64
        );
        let (m2, p2) = hash_files_with(repo.path(), listed, Some(&cache), Duration::ZERO);
        assert_eq!(
            (p2.hashed, p2.reused, p2.cache_written),
            (0, 2, false),
            "{p2:?}"
        );
        assert_eq!(m1, direct(repo.path(), &["a.rs", "b.rs"]));
        assert_eq!(m2, m1);
    }

    #[test]
    fn added_and_deleted_files_update_the_cache() {
        let (repo, _s, cache) = setup(&["a.rs", "b.rs"]);
        settle();
        hash_files_with(
            repo.path(),
            rels(&["a.rs", "b.rs"]),
            Some(&cache),
            Duration::ZERO,
        );
        assert_eq!(cached_paths(&cache), ["a.rs", "b.rs"]);
        fs::remove_file(repo.path().join("b.rs")).unwrap();
        write(repo.path(), "c.rs", b"new\n");
        settle();
        let (m, p) = hash_files_with(
            repo.path(),
            rels(&["a.rs", "c.rs"]),
            Some(&cache),
            Duration::ZERO,
        );
        assert_eq!(
            (p.hashed, p.reused),
            (1, 1),
            "only the new file is read: {p:?}"
        );
        assert!(p.cache_written);
        assert_eq!(m, direct(repo.path(), &["a.rs", "c.rs"]));
        assert_eq!(
            cached_paths(&cache),
            ["a.rs", "c.rs"],
            "deleted entry dropped"
        );
        // a tracked path missing from the worktree is left out, as direct hashing does.
        let (m, p) = hash_files_with(
            repo.path(),
            rels(&["a.rs", "b.rs", "c.rs"]),
            Some(&cache),
            Duration::ZERO,
        );
        assert_eq!(m, direct(repo.path(), &["a.rs", "b.rs", "c.rs"]));
        assert_eq!(
            (p.hashed, p.reused, p.cache_written),
            (0, 2, false),
            "{p:?}"
        );
    }

    #[test]
    fn corrupt_cache_falls_back_to_full_hashing() {
        let names = ["a.rs", "b.rs"];
        let (repo, _s, cache) = setup(&names);
        let want = direct(repo.path(), &names);
        settle();
        hash_files_with(repo.path(), rels(&names), Some(&cache), Duration::ZERO);
        let valid = fs::read(&cache).unwrap();
        let mut flipped = valid.clone();
        flipped[HEADER_BYTES + 4 + 4 + ENTRY_FIXED_BYTES - 4 - 32] ^= 0x01; // a's digest
        let corruptions: [(&str, Vec<u8>); 4] = [
            ("garbage", b"not a cache file at all".to_vec()),
            ("flipped digest byte", flipped),
            ("truncated", valid[..valid.len() - 10].to_vec()),
            ("empty", Vec::new()),
        ];
        for (what, bytes) in corruptions {
            fs::write(&cache, &bytes).unwrap();
            let (m, p) = hash_files_with(repo.path(), rels(&names), Some(&cache), Duration::ZERO);
            assert_eq!(m, want, "{what}: wrong hashes");
            assert_eq!((p.hashed, p.cache_loaded), (2, false), "{what}: {p:?}");
            assert!(p.cache_written, "{what}: corrupt file not replaced");
            let (_, p) = hash_files_with(repo.path(), rels(&names), Some(&cache), Duration::ZERO);
            assert_eq!((p.hashed, p.reused), (0, 2), "{what}: rewrite not reusable");
        }
    }

    #[test]
    fn oversized_cache_file_is_ignored_and_replaced() {
        let (repo, _s, cache) = setup(&["a.rs"]);
        fs::create_dir_all(cache.parent().unwrap()).unwrap();
        fs::File::create(&cache)
            .unwrap()
            .set_len(MAX_CACHE_BYTES + 1)
            .unwrap();
        let (m, p) = hash_files_with(repo.path(), rels(&["a.rs"]), Some(&cache), Duration::ZERO);
        assert_eq!(m, direct(repo.path(), &["a.rs"]));
        assert_eq!(
            (p.hashed, p.cache_loaded, p.cache_written),
            (1, false, true)
        );
        assert!(fs::metadata(&cache).unwrap().len() < 1024);
    }

    // Rows past the bound are refused from their lengths alone, before anything is
    // encoded, and an older file is removed rather than reread on every pass.
    #[test]
    fn store_refuses_rows_past_the_bound_and_drops_the_old_file() {
        let (_repo, _s, cache) = setup(&[]);
        let entry = sample_entry();
        store_at(&cache, 1, &[("a.rs", &entry)]);
        let long = "p".repeat(1 << 20);
        let rows = std::iter::repeat_n((long.as_str(), &entry), 64);
        assert!(!store(&cache, &PassStart::now(), rows));
        assert!(!cache.exists(), "oversized store kept the old file");
        let dir = fs::read_dir(cache.parent().unwrap()).unwrap();
        let names: Vec<_> = dir.map(|e| e.unwrap().file_name()).collect();
        assert!(
            names.iter().all(|n| !n.to_string_lossy().contains(".tmp.")),
            "{names:?}"
        );
    }

    // Stored baselines hold `format!("{:x}", digest)`; cached hashes must match it.
    #[test]
    fn hex_matches_the_stored_digest_form() {
        for input in [&b""[..], b"x", b"content of a.rs\n"] {
            let digest = Sha256::digest(input);
            assert_eq!(symbolgraph::hex_lower(&digest), format!("{digest:x}"));
        }
    }

    #[test]
    fn decode_rejects_inflated_counts_and_trailing_bytes() {
        let entry = sample_entry();
        let start = PassStart {
            unix_ns: 9,
            mono: Instant::now(),
        };
        let good = encode(&start, &[("p.rs", &entry)]);
        let back = decode_bytes(&good, i64::MAX).expect("round trip");
        assert_eq!((back.recorded_at_ns, back.entries["p.rs"]), (9, entry));
        let reseal = |mut body: Vec<u8>| {
            let sum = Sha256::digest(&body);
            body.extend_from_slice(&sum);
            body
        };
        let body = &good[..good.len() - CHECKSUM_BYTES];
        // the pass start is the earlier of the host start and the file mtime minus the
        // recorded duration.
        let mut timed = body.to_vec();
        let at = timed.len() - DURATION_BYTES;
        timed[at..].copy_from_slice(&100i64.to_le_bytes());
        let timed = reseal(timed);
        assert_eq!(decode_bytes(&timed, 1_000).unwrap().recorded_at_ns, 9);
        assert_eq!(decode_bytes(&timed, 50).unwrap().recorded_at_ns, -50);
        let mut inflated = body.to_vec();
        inflated[16..24].copy_from_slice(&u64::MAX.to_le_bytes());
        assert!(
            decode_bytes(&reseal(inflated), i64::MAX).is_none(),
            "huge count accepted"
        );
        let mut trailing = body.to_vec();
        trailing.push(0);
        assert!(
            decode_bytes(&reseal(trailing), i64::MAX).is_none(),
            "trailing bytes accepted"
        );
        let mut long_path = body.to_vec();
        long_path[HEADER_BYTES..HEADER_BYTES + 4].copy_from_slice(&u32::MAX.to_le_bytes());
        assert!(
            decode_bytes(&reseal(long_path), i64::MAX).is_none(),
            "path length past the file accepted"
        );
        assert!(
            decode(&good[..], good.len() as u64 + 1, i64::MAX).is_none(),
            "a file shorter than its stat accepted"
        );
        let mut magic = body.to_vec();
        magic[7] = b'9';
        assert!(
            decode_bytes(&reseal(magic), i64::MAX).is_none(),
            "other format version accepted"
        );
    }

    // Symlinks, missing, non-regular and over-cap files are excluded exactly as the
    // direct no-follow hash excludes them.
    #[cfg(unix)]
    #[test]
    fn exclusions_match_direct_hashing() {
        let (repo, _s, cache) = setup(&["ok.rs"]);
        std::os::unix::fs::symlink(repo.path().join("ok.rs"), repo.path().join("link.rs")).unwrap();
        fs::create_dir(repo.path().join("dir")).unwrap();
        fs::File::create(repo.path().join("big.bin"))
            .unwrap()
            .set_len(MAX_REPO_FILE_BYTES + 1)
            .unwrap();
        let names = ["ok.rs", "link.rs", "dir", "big.bin", "missing.rs"];
        for _ in 0..2 {
            let (m, _) = hash_files(repo.path(), rels(&names), Some(&cache));
            assert_eq!(m, direct(repo.path(), &names));
            assert_eq!(m.keys().collect::<Vec<_>>(), ["ok.rs"]);
        }
    }

    // The identity's pass: the caller's order claims the byte budget, a file that does
    // not fit is left out and counted, and reused entries cost nothing.
    #[test]
    fn a_budgeted_pass_keeps_the_callers_order_and_reuses_for_free() {
        let (repo, _s, cache) = setup(&[]);
        write(repo.path(), "z_tracked.rs", &[b'a'; 100]);
        write(repo.path(), "a_untracked.rs", &[b'b'; 100]);
        settle();
        let order = rels(&["z_tracked.rs", "a_untracked.rs"]);
        let (m, p) = hash_files_inner(
            repo.path(),
            order.clone(),
            Some(&cache),
            Duration::ZERO,
            Some(150),
        );
        assert_eq!(
            m.keys().collect::<Vec<_>>(),
            ["z_tracked.rs"],
            "first in order wins the budget"
        );
        assert_eq!((p.hashed, p.bytes_hashed, p.over_budget), (1, 100, 1));
        let (m, p) = hash_files_inner(repo.path(), order, Some(&cache), Duration::ZERO, Some(150));
        assert_eq!(
            m.len(),
            2,
            "the reused entry leaves the budget to the other file"
        );
        assert_eq!((p.hashed, p.reused, p.over_budget), (1, 1, 0));
        assert_eq!(m["a_untracked.rs"], sha_hex(&[b'b'; 100]));
    }

    #[test]
    fn cache_file_lives_under_the_git_dir() {
        let repo = TempDir::new().unwrap();
        assert!(cache_file(repo.path()).is_none(), "no cache outside Git");
        Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["init", "-q"])
            .output()
            .unwrap();
        let path = cache_file(repo.path()).expect("git repo has a cache file");
        let git_dir = fs::canonicalize(repo.path().join(".git")).unwrap();
        assert!(
            path.starts_with(git_dir.join("xmustard-cache").join("filehash-v1")),
            "{path:?}"
        );
        fs::create_dir(repo.path().join("sub")).unwrap();
        assert_ne!(
            cache_file(&repo.path().join("sub")),
            Some(path),
            "a subdirectory root lists different relative paths"
        );
    }
}
