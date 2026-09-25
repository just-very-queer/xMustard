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
//! start minus `RACY_WINDOW`. The pass start precedes every stat and the write, so this
//! distrusts at least everything Git's "mtime >= index write time" rule would. A racy
//! entry is re-hashed on the next pass and becomes trusted once a later pass records it
//! outside the window.
//!
//! The cache is `<git-dir>/xmustard-cache/filehash-v1/<scope>.bin`, where the scope hashes
//! the canonical root. It is written atomically under an advisory lock, bounded to
//! `MAX_CACHE_BYTES` and checksummed; a missing, oversized or corrupt file means a full
//! hashing pass. Files are opened through the same no-follow, regular-file-checked
//! descriptor as `hash_repo_file_beneath`, and the key is that descriptor's fstat taken
//! before reading, so the hashes equal hashing every file directly.

use std::collections::{BTreeMap, HashMap};
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

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

/// Magic + recorded-at + entry count.
const HEADER_BYTES: usize = 8 + 8 + 8;
/// Path length + size, mtime, ctime, inode, device + mode + SHA-256.
const ENTRY_FIXED_BYTES: usize = 4 + 5 * 8 + 4 + 32;
const CHECKSUM_BYTES: usize = 32;

/// Counters for one hashing pass.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct HashPass {
    /// Files whose bytes were read this pass (new, changed, racy, or no usable cache).
    pub hashed: usize,
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
        let mtime_ns = meta
            .modified()
            .ok()
            .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
            .map_or(i64::MAX, |d| {
                i64::try_from(d.as_nanos()).unwrap_or(i64::MAX)
            });
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
    /// Start of the pass that recorded these entries, in Unix nanoseconds.
    recorded_at_ns: i64,
    entries: HashMap<String, Entry>,
}

enum Loaded {
    Absent,
    Invalid,
    Valid(StatCache),
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

/// The stat-cache file for `root`, or None outside Git.
pub fn cache_file(root: &Path) -> Option<PathBuf> {
    let layout = indexcache::git_layout(root)?;
    let canonical = fs::canonicalize(root).ok()?;
    let mut h = Sha256::new();
    h.update(b"xm-filehash-scope-v1\0");
    h.update(canonical.as_os_str().as_encoded_bytes());
    let scope = symbolgraph::hex_lower(&h.finalize()[..16]);
    Some(
        indexcache::xmustard_cache_dir(&layout)
            .join("filehash-v1")
            .join(format!("{scope}.bin")),
    )
}

fn hash_files_with(
    root: &Path,
    rels: Vec<String>,
    cache: Option<&Path>,
    racy_window: Duration,
) -> (BTreeMap<String, String>, HashPass) {
    // Taken before any stat: the racy cutoff never depends on how long hashing took.
    let pass_started_ns = now_ns();
    let mut pass = HashPass::default();
    let (old, replace_invalid) = match cache.map(load) {
        Some(Loaded::Valid(c)) => {
            pass.cache_loaded = true;
            (c, false)
        }
        Some(Loaded::Invalid) => (StatCache::default(), true),
        Some(Loaded::Absent) | None => (StatCache::default(), false),
    };
    let window_ns = i64::try_from(racy_window.as_nanos()).unwrap_or(i64::MAX);
    let trusted_before = old.recorded_at_ns.saturating_sub(window_ns);

    let mut map = BTreeMap::new();
    let mut fresh: Vec<(String, Entry)> = Vec::with_capacity(rels.len());
    let mut entries_changed = false;
    for rel in rels {
        let Ok((file, meta)) = symbolgraph::open_repo_regular_file_with_meta(root, &rel) else {
            continue;
        };
        if meta.len() > MAX_REPO_FILE_BYTES {
            continue; // the streaming hash would refuse it too; skip the read
        }
        let key = StatKey::of(&meta);
        let prev = old.entries.get(&rel);
        let cached = prev.filter(|e| e.key == key);
        let digest = match cached {
            Some(e) if key.newest_ns() < trusted_before => {
                pass.reused += 1;
                e.digest
            }
            _ => {
                if cached.is_some() {
                    pass.racy += 1;
                }
                pass.hashed += 1;
                let Some(digest) = symbolgraph::sha256_open_file(file) else {
                    continue;
                };
                digest
            }
        };
        let entry = Entry { key, digest };
        entries_changed |= prev != Some(&entry);
        map.insert(rel.clone(), symbolgraph::hex_lower(&digest));
        fresh.push((rel, entry));
    }
    // Without `entries_changed`, `fresh` is a subset of the old entries, so a shorter
    // list means files left. A racy re-hash is rewritten even when identical: the newer
    // pass start is what lets the entry become trusted.
    let removed = fresh.len() != old.entries.len();
    if let Some(path) = cache
        && (replace_invalid || entries_changed || removed || pass.racy > 0)
    {
        pass.cache_written = store(path, pass_started_ns, &fresh);
    }
    (map, pass)
}

fn now_ns() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
}

fn load(path: &Path) -> Loaded {
    let Ok(f) = fs::File::open(path) else {
        return Loaded::Absent;
    };
    match f.metadata() {
        Ok(meta) if meta.is_file() && meta.len() <= MAX_CACHE_BYTES => {}
        _ => return Loaded::Invalid,
    }
    let mut buf = Vec::new();
    if f.take(MAX_CACHE_BYTES + 1).read_to_end(&mut buf).is_err()
        || buf.len() as u64 > MAX_CACHE_BYTES
    {
        return Loaded::Invalid;
    }
    decode(&buf).map_or(Loaded::Invalid, Loaded::Valid)
}

/// Write the cache (best effort). Every entry is re-validated by stat on read, so a
/// concurrent pass's last write is equally correct; the lock only serializes writers.
fn store(path: &Path, recorded_at_ns: i64, entries: &[(String, Entry)]) -> bool {
    let Some(dir) = path.parent() else {
        return false;
    };
    if fs::create_dir_all(dir).is_err() {
        return false;
    }
    let bytes = encode(recorded_at_ns, entries);
    if bytes.len() as u64 > MAX_CACHE_BYTES {
        // too many tracked files to cache: drop an older file instead of rereading it.
        let _ = fs::remove_file(path);
        return false;
    }
    let (Some(_lock), _) = indexcache::lock_file(&path.with_extension("lock"), STORE_LOCK_WAIT)
    else {
        return false;
    };
    let written = indexcache::atomic_write(path, &bytes).is_ok();
    indexcache::sweep_stale_temps(dir, indexcache::STALE_TEMP_AGE);
    written
}

fn encode(recorded_at_ns: i64, entries: &[(String, Entry)]) -> Vec<u8> {
    let paths: usize = entries.iter().map(|(p, _)| p.len()).sum();
    let mut out = Vec::with_capacity(
        HEADER_BYTES + entries.len() * ENTRY_FIXED_BYTES + paths + CHECKSUM_BYTES,
    );
    out.extend_from_slice(MAGIC);
    out.extend_from_slice(&recorded_at_ns.to_le_bytes());
    out.extend_from_slice(&(entries.len() as u64).to_le_bytes());
    for (path, e) in entries {
        out.extend_from_slice(&(path.len() as u32).to_le_bytes());
        out.extend_from_slice(path.as_bytes());
        out.extend_from_slice(&e.key.size.to_le_bytes());
        out.extend_from_slice(&e.key.mtime_ns.to_le_bytes());
        out.extend_from_slice(&e.key.ctime_ns.to_le_bytes());
        out.extend_from_slice(&e.key.ino.to_le_bytes());
        out.extend_from_slice(&e.key.dev.to_le_bytes());
        out.extend_from_slice(&e.key.mode.to_le_bytes());
        out.extend_from_slice(&e.digest);
    }
    let checksum = Sha256::digest(&out);
    out.extend_from_slice(&checksum);
    out
}

/// Parse a cache file; None when the checksum, magic, counts or lengths disagree.
fn decode(bytes: &[u8]) -> Option<StatCache> {
    if bytes.len() < HEADER_BYTES + CHECKSUM_BYTES {
        return None;
    }
    let (body, checksum) = bytes.split_at(bytes.len() - CHECKSUM_BYTES);
    if Sha256::digest(body).as_slice() != checksum || &body[..MAGIC.len()] != MAGIC {
        return None;
    }
    let mut r = Reader {
        buf: body,
        pos: MAGIC.len(),
    };
    let recorded_at_ns = i64::from_le_bytes(r.array()?);
    let count = u64::from_le_bytes(r.array()?);
    // every entry takes at least ENTRY_FIXED_BYTES, so a bad count cannot over-allocate.
    if count > ((body.len() - HEADER_BYTES) / ENTRY_FIXED_BYTES) as u64 {
        return None;
    }
    let mut entries = HashMap::with_capacity(count as usize);
    for _ in 0..count {
        let len = u32::from_le_bytes(r.array()?) as usize;
        let path = std::str::from_utf8(r.take(len)?).ok()?.to_owned();
        let key = StatKey {
            size: u64::from_le_bytes(r.array()?),
            mtime_ns: i64::from_le_bytes(r.array()?),
            ctime_ns: i64::from_le_bytes(r.array()?),
            ino: u64::from_le_bytes(r.array()?),
            dev: u64::from_le_bytes(r.array()?),
            mode: u32::from_le_bytes(r.array()?),
        };
        let digest = r.array()?;
        entries.insert(path, Entry { key, digest });
    }
    if r.pos != body.len() {
        return None;
    }
    Some(StatCache {
        recorded_at_ns,
        entries,
    })
}

struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl Reader<'_> {
    fn take(&mut self, n: usize) -> Option<&[u8]> {
        let end = self.pos.checked_add(n)?;
        let out = self.buf.get(self.pos..end)?;
        self.pos = end;
        Some(out)
    }

    fn array<const N: usize>(&mut self) -> Option<[u8; N]> {
        self.take(N)?.try_into().ok()
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
            assert!(store(&cache, recorded_at, &[("m.rs".into(), stale)]));
            let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
            assert_eq!(m["m.rs"], real, "racy entry served a stale hash");
            assert_eq!((p.racy, p.hashed, p.reused), (1, 1, 0), "{p:?}");
        }
        assert!(store(
            &cache,
            key.newest_ns() + 3 * second,
            &[("m.rs".into(), stale)]
        ));
        let (m, p) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        assert_eq!(
            m["m.rs"],
            "aa".repeat(32),
            "outside the window the key is trusted"
        );
        assert_eq!((p.racy, p.hashed, p.reused), (0, 0, 1), "{p:?}");
    }

    // A same-size rewrite that restores the old mtime (what `touch -r`, rsync -t or a
    // formatter can do) must still be seen.
    #[test]
    fn same_size_edit_with_restored_mtime_is_detected() {
        let (repo, _s, cache) = setup(&[]);
        let path = repo.path().join("m.rs");
        write(repo.path(), "m.rs", b"aaaa\n");
        let (m1, _) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        let mtime = fs::metadata(&path).unwrap().modified().unwrap();
        write(repo.path(), "m.rs", b"bbbb\n");
        fs::File::options()
            .write(true)
            .open(&path)
            .unwrap()
            .set_modified(mtime)
            .unwrap();
        assert_eq!(fs::metadata(&path).unwrap().modified().unwrap(), mtime);
        let (m2, _) = hash_files(repo.path(), rels(&["m.rs"]), Some(&cache));
        assert_eq!(m1["m.rs"], sha_hex(b"aaaa\n"));
        assert_eq!(m2["m.rs"], sha_hex(b"bbbb\n"), "same-size edit missed");
    }

    #[test]
    fn added_and_deleted_files_update_the_cache() {
        let (repo, _s, cache) = setup(&["a.rs", "b.rs"]);
        hash_files_with(
            repo.path(),
            rels(&["a.rs", "b.rs"]),
            Some(&cache),
            Duration::ZERO,
        );
        assert_eq!(cached_paths(&cache), ["a.rs", "b.rs"]);
        fs::remove_file(repo.path().join("b.rs")).unwrap();
        write(repo.path(), "c.rs", b"new\n");
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
        let entry = Entry {
            key: StatKey {
                size: 1,
                mtime_ns: 2,
                ctime_ns: 3,
                ino: 4,
                dev: 5,
                mode: 6,
            },
            digest: [7; 32],
        };
        let good = encode(9, &[("p.rs".into(), entry)]);
        let back = decode(&good).expect("round trip");
        assert_eq!((back.recorded_at_ns, back.entries["p.rs"]), (9, entry));
        let reseal = |mut body: Vec<u8>| {
            let sum = Sha256::digest(&body);
            body.extend_from_slice(&sum);
            body
        };
        let body = &good[..good.len() - CHECKSUM_BYTES];
        let mut inflated = body.to_vec();
        inflated[16..24].copy_from_slice(&u64::MAX.to_le_bytes());
        assert!(decode(&reseal(inflated)).is_none(), "huge count accepted");
        let mut trailing = body.to_vec();
        trailing.push(0);
        assert!(
            decode(&reseal(trailing)).is_none(),
            "trailing bytes accepted"
        );
        let mut magic = body.to_vec();
        magic[7] = b'9';
        assert!(
            decode(&reseal(magic)).is_none(),
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
