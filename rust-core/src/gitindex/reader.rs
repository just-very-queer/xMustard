//! A streamed reader of Git's `.git/index` (WS-RT18): versions 2, 3 and 4, SHA-1 and
//! SHA-256 object ids, read through one 64 KiB buffer per stream and handed out one
//! entry at a time. Written from Git's documentation of the format
//! (`Documentation/gitformat-index.txt`, the split index in
//! `Documentation/technical/split-index.txt`); no code is taken from Git.
//!
//! `Index::open` makes a first pass over the whole file before any entry is handed
//! out: it verifies the trailing checksum (a null trailer, `index.skipHash`, is
//! accepted), decodes the cache-tree (`TREE`) and split-index (`link`) extensions, skips
//! the optional ones, and refuses what it does not follow: a sparse index, an unknown
//! required extension, an unknown version or entry flag. A refusal therefore always
//! comes before the first entry. `Index::entries` then streams the entries in index
//! order, merging a split index's shared file with its own; memory is the buffers, one
//! entry, the cache-tree's valid directories and a split index's two bitmaps.

use std::fs::File;
use std::io::ErrorKind;
use std::os::unix::fs::{FileExt, MetadataExt};
use std::path::Path;

use sha1::Digest;

use super::Refusal;

const BUF: usize = 64 << 10;
/// The longest entry a stream accepts (fixed fields plus a path).
const MAX_ENTRY: usize = 1 << 20;
/// A cache-tree extension past this size is skipped, not decoded: the comparison with
/// HEAD then reads every tree of HEAD.
const MAX_TREE_EXT: usize = 4 << 20;
/// A split index's link extension (the shared index id and two bitmaps) past this
/// size is refused.
const MAX_LINK_EXT: usize = MAX_ENTRY;

/// The repository's object format.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Hash {
    Sha1,
    Sha256,
}

impl Hash {
    pub fn oid_len(self) -> usize {
        match self {
            Hash::Sha1 => 20,
            Hash::Sha256 => 32,
        }
    }

    pub fn parse(name: &str) -> Option<Hash> {
        match name {
            "sha1" => Some(Hash::Sha1),
            "sha256" => Some(Hash::Sha256),
            _ => None,
        }
    }

    fn hasher(self) -> Hasher {
        match self {
            Hash::Sha1 => Hasher::Sha1(sha1::Sha1::new()),
            Hash::Sha256 => Hasher::Sha256(sha2::Sha256::new()),
        }
    }
}

enum Hasher {
    Sha1(sha1::Sha1),
    Sha256(sha2::Sha256),
}

impl Hasher {
    fn update(&mut self, b: &[u8]) {
        match self {
            Hasher::Sha1(h) => h.update(b),
            Hasher::Sha256(h) => h.update(b),
        }
    }

    fn finish(self) -> Vec<u8> {
        match self {
            Hasher::Sha1(h) => h.finalize().to_vec(),
            Hasher::Sha256(h) => h.finalize().to_vec(),
        }
    }
}

/// The checksum Git writes after `bytes` in an index of format `hash`.
pub fn checksum(hash: Hash, bytes: &[u8]) -> Vec<u8> {
    let mut h = hash.hasher();
    h.update(bytes);
    h.finish()
}

/// An object id of either format.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Oid {
    bytes: [u8; 32],
    len: u8,
}

impl Oid {
    pub fn from_bytes(b: &[u8]) -> Oid {
        let mut bytes = [0u8; 32];
        let n = b.len().min(32);
        bytes[..n].copy_from_slice(&b[..n]);
        Oid {
            bytes,
            len: n as u8,
        }
    }

    pub fn parse_hex(hex: &str) -> Option<Oid> {
        let hex = hex.trim();
        if !matches!(hex.len(), 40 | 64) {
            return None;
        }
        let mut raw = Vec::with_capacity(hex.len() / 2);
        for pair in hex.as_bytes().chunks(2) {
            raw.push(u8::from_str_radix(std::str::from_utf8(pair).ok()?, 16).ok()?);
        }
        Some(Oid::from_bytes(&raw))
    }

    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes[..self.len as usize]
    }

    pub fn hex(&self) -> String {
        self.as_bytes().iter().map(|b| format!("{b:02x}")).collect()
    }
}

impl std::fmt::Debug for Oid {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.hex())
    }
}

/// An entry's cached stat data, as Git stores it (each field truncated to 32 bits).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Stat {
    pub ctime: (u32, u32),
    pub mtime: (u32, u32),
    pub dev: u32,
    pub ino: u32,
    pub uid: u32,
    pub gid: u32,
    pub size: u32,
}

impl Stat {
    pub fn of(m: &std::fs::Metadata) -> Stat {
        Stat {
            ctime: (m.ctime() as u32, m.ctime_nsec() as u32),
            mtime: (m.mtime() as u32, m.mtime_nsec() as u32),
            dev: m.dev() as u32,
            ino: m.ino() as u32,
            uid: m.uid(),
            gid: m.gid(),
            size: m.size() as u32,
        }
    }
}

/// One index entry. `path` is relative to the worktree top, in Git's bytes.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Entry {
    pub path: Vec<u8>,
    pub mode: u32,
    pub oid: Oid,
    pub stat: Stat,
    /// 0 for a merged entry, 1-3 for the stages of an unmerged path.
    pub stage: u8,
    pub assume_valid: bool,
    pub skip_worktree: bool,
    pub intent_to_add: bool,
}

/// Git's mode of a sparse-directory entry.
const MODE_DIR: u32 = 0o040000;

/// One sequential reader over a byte range of an index file, with its own offset
/// (`pread`), so several may read one file at once.
struct Stream<'f> {
    file: &'f File,
    buf: Vec<u8>,
    start: usize,
    end: usize,
    /// File offset of `buf[end]`.
    offset: u64,
    /// Where the range ends.
    limit: u64,
}

impl<'f> Stream<'f> {
    fn new(file: &'f File, from: u64, limit: u64) -> Self {
        Stream {
            file,
            buf: vec![0; BUF],
            start: 0,
            end: 0,
            offset: from,
            limit,
        }
    }

    /// At least `n` contiguous bytes at the front, or an error at the end of the range.
    fn need(&mut self, n: usize) -> Result<&[u8], Refusal> {
        if self.end - self.start < n {
            if n > MAX_ENTRY {
                return Err(Refusal::Failed(format!(
                    "index record past {MAX_ENTRY} bytes"
                )));
            }
            self.buf.copy_within(self.start..self.end, 0);
            self.end -= self.start;
            self.start = 0;
            if self.buf.len() < n {
                self.buf.resize(n.next_power_of_two(), 0);
            }
            while self.end < n {
                let room = (self.buf.len() - self.end) as u64;
                let want = room.min(self.limit.saturating_sub(self.offset)) as usize;
                if want == 0 {
                    return Err(Refusal::Failed("index ends inside a record".into()));
                }
                let got = read_at(
                    self.file,
                    &mut self.buf[self.end..self.end + want],
                    self.offset,
                )?;
                self.end += got;
                self.offset += got as u64;
            }
        }
        Ok(&self.buf[self.start..self.end])
    }

    /// The bytes available now (up to one buffer), refilled when empty; empty at the end.
    fn chunk(&mut self) -> Result<&[u8], Refusal> {
        if self.start == self.end && self.offset < self.limit {
            self.start = 0;
            let want = (self.buf.len() as u64).min(self.limit - self.offset) as usize;
            self.end = read_at(self.file, &mut self.buf[..want], self.offset)?;
            self.offset += self.end as u64;
        }
        Ok(&self.buf[self.start..self.end])
    }

    fn consume(&mut self, n: usize) {
        self.start += n;
    }

    /// File offset of the next unread byte.
    fn position(&self) -> u64 {
        self.offset - (self.end - self.start) as u64
    }
}

fn read_at(file: &File, buf: &mut [u8], offset: u64) -> Result<usize, Refusal> {
    loop {
        match file.read_at(buf, offset) {
            Ok(0) => return Err(Refusal::Failed("index file shrank while read".into())),
            Ok(n) => return Ok(n),
            Err(e) if e.kind() == ErrorKind::Interrupted => {}
            Err(e) => return Err(Refusal::Failed(format!("index read: {e}"))),
        }
    }
}

fn be32(b: &[u8], at: usize) -> u32 {
    u32::from_be_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]])
}

/// Git's offset varint (index v4 prefix lengths): the value and its byte count.
fn varint(b: &[u8]) -> Option<(usize, usize)> {
    let mut i = 0;
    let mut c = *b.first()?;
    let mut val = (c & 127) as usize;
    while c & 128 != 0 {
        i += 1;
        c = *b.get(i)?;
        val = val.checked_add(1)?.checked_mul(128)? + (c & 127) as usize;
    }
    Some((val, i + 1))
}

/// Decode the next entry from `s` into `e` (whose `path` is the previous entry's, which
/// v4 needs). Returns false at the end of the entries.
fn next_entry(
    s: &mut Stream<'_>,
    version: u32,
    hash: Hash,
    e: &mut Entry,
) -> Result<bool, Refusal> {
    if s.position() >= s.limit {
        return Ok(false);
    }
    let h = hash.oid_len();
    let base = 40 + h + 2;
    let head = s.need(base)?;
    let flags = u16::from_be_bytes([head[40 + h], head[41 + h]]);
    let extended = flags & 0x4000 != 0;
    let fixed = base + if extended { 2 } else { 0 };
    let bad = |what: &str| Refusal::Failed(format!("malformed index entry: {what}"));
    if extended && version < 3 {
        return Err(bad("extended flags in a version 2 index"));
    }
    // the name: v4 strips a prefix of the previous path; v2/v3 store it whole, padded
    let (name_start, total) = match version {
        4 => {
            let mut n = fixed + 1;
            let (strip, used) = loop {
                let b = s.need(n)?;
                match varint(&b[fixed..n]) {
                    Some(v) => break v,
                    None if n - fixed < 10 => n += 1,
                    None => return Err(bad("v4 prefix length")),
                }
            };
            let at = fixed + used;
            let mut want = at + 1;
            let nul = loop {
                let b = s.need(want)?;
                if let Some(k) = b[at..].iter().position(|c| *c == 0) {
                    break at + k;
                }
                want = b.len() + 1;
            };
            if strip > e.path.len() {
                return Err(bad("v4 prefix past the previous path"));
            }
            e.path.truncate(e.path.len() - strip);
            (at, nul + 1)
        }
        _ => {
            let len = (flags & 0xfff) as usize;
            let nul = if len < 0xfff {
                fixed + len
            } else {
                let mut want = fixed + len + 1;
                loop {
                    let b = s.need(want)?;
                    if let Some(k) = b[fixed + len..].iter().position(|c| *c == 0) {
                        break fixed + len + k;
                    }
                    want = b.len() + 1;
                }
            };
            e.path.clear();
            (fixed, (nul + 8) & !7)
        }
    };
    let b = s.need(total)?;
    let name_end = match version {
        4 => total - 1,
        _ => b[name_start..total]
            .iter()
            .position(|c| *c == 0)
            .map_or(total, |k| name_start + k),
    };
    e.path.extend_from_slice(&b[name_start..name_end]);
    e.stat = Stat {
        ctime: (be32(b, 0), be32(b, 4)),
        mtime: (be32(b, 8), be32(b, 12)),
        dev: be32(b, 16),
        ino: be32(b, 20),
        uid: be32(b, 28),
        gid: be32(b, 32),
        size: be32(b, 36),
    };
    e.mode = be32(b, 24);
    e.oid = Oid::from_bytes(&b[40..40 + h]);
    e.stage = ((flags >> 12) & 3) as u8;
    e.assume_valid = flags & 0x8000 != 0;
    let ext = if extended {
        u16::from_be_bytes([b[base], b[base + 1]])
    } else {
        0
    };
    if ext & !0x6000 != 0 {
        return Err(bad("unknown extended flag"));
    }
    e.skip_worktree = ext & 0x4000 != 0;
    e.intent_to_add = ext & 0x2000 != 0;
    s.consume(total);
    Ok(true)
}

/// A split index's link to its shared index.
struct Link {
    shared: Oid,
    /// The two serialized bitmaps, decoded once the shared index's size is known.
    bitmaps: Vec<u8>,
    delete: Bits,
    replace: Bits,
}

/// A decoded EWAH bitmap: one bit per shared-index position.
#[derive(Default)]
struct Bits(Vec<u64>);

impl Bits {
    fn get(&self, i: usize) -> bool {
        self.0.get(i / 64).is_some_and(|w| w >> (i % 64) & 1 == 1)
    }

    fn count(&self) -> usize {
        self.0.iter().map(|w| w.count_ones() as usize).sum()
    }

    /// Decode Git's EWAH serialization from the front of `b`, which may declare at
    /// most `max_bits` bits; the bytes it used.
    fn decode(b: &[u8], max_bits: usize) -> Option<(Bits, usize)> {
        let bit_size = be32(b.get(..4)?, 0) as usize;
        if bit_size > max_bits {
            return None;
        }
        let words = be32(b.get(4..8)?, 0) as usize;
        let body = b.get(8..8 + words.checked_mul(8)?)?;
        let word =
            |i: usize| u64::from_be_bytes(body[i * 8..i * 8 + 8].try_into().unwrap_or([0; 8]));
        let mut out = vec![0u64; bit_size.div_ceil(64)];
        let (mut pos, mut i) = (0usize, 0usize);
        while i < words {
            let rlw = word(i);
            i += 1;
            let run = ((rlw >> 1) & 0xffff_ffff) as usize;
            let literals = (rlw >> 33) as usize;
            if rlw & 1 == 1 {
                for w in out.get_mut(pos..pos.checked_add(run)?)? {
                    *w = u64::MAX;
                }
            }
            pos = pos.checked_add(run)?;
            for _ in 0..literals {
                if i >= words {
                    return None;
                }
                *out.get_mut(pos)? = word(i);
                i += 1;
                pos += 1;
            }
        }
        Some((Bits(out), 8 + words * 8 + 4))
    }
}

/// An opened, verified index file and what its first pass found.
pub struct Index {
    file: Option<File>,
    version: u32,
    hash: Hash,
    count: u32,
    entries_end: u64,
    /// The index file's mtime (seconds), for the racy-git rule.
    pub mtime: i64,
    /// The cache-tree's valid directories (`""` for the top, else `a/b`), sorted.
    pub cache_tree: Vec<(Vec<u8>, Oid)>,
    link: Option<Link>,
    shared: Option<Box<Index>>,
}

impl Index {
    /// The index of the Git dir `git_dir` (an absent index is an empty one).
    pub fn open(git_dir: &Path, hash: Hash) -> Result<Index, Refusal> {
        let mut ix = Index::open_file(&git_dir.join("index"), hash)?;
        if let Some(link) = &mut ix.link {
            let path = git_dir.join(format!("sharedindex.{}", link.shared.hex()));
            let shared = Index::open_file(&path, hash)?;
            if shared.file.is_none() || shared.link.is_some() {
                return Err(Refusal::Failed(format!(
                    "shared index {} unusable",
                    path.display()
                )));
            }
            link.decode_bitmaps(shared.count)
                .ok_or_else(|| Refusal::Failed("malformed split index bitmaps".into()))?;
            ix.shared = Some(Box::new(shared));
        }
        Ok(ix)
    }

    /// Entries in the header (the split part's own count for a split index).
    pub fn count(&self) -> u32 {
        self.count + self.shared.as_ref().map_or(0, |s| s.count)
    }

    /// Open `path` and make the first pass: header, every entry's extent, the
    /// extensions and the trailing checksum.
    pub fn open_file(path: &Path, hash: Hash) -> Result<Index, Refusal> {
        let empty = Index {
            file: None,
            version: 2,
            hash,
            count: 0,
            entries_end: 12,
            mtime: 0,
            cache_tree: Vec::new(),
            link: None,
            shared: None,
        };
        let file = match File::open(path) {
            Ok(f) => f,
            Err(e) if e.kind() == ErrorKind::NotFound => return Ok(empty),
            Err(e) => return Err(Refusal::Failed(format!("{}: {e}", path.display()))),
        };
        let meta = file
            .metadata()
            .map_err(|e| Refusal::Failed(format!("{}: {e}", path.display())))?;
        let h = hash.oid_len();
        let len = meta.len();
        if len < 12 + h as u64 {
            return Err(Refusal::Failed("index shorter than its header".into()));
        }
        let body_end = len - h as u64;
        let mut s = Stream::new(&file, 0, body_end);
        let head = s.need(12)?;
        if &head[..4] != b"DIRC" {
            return Err(Refusal::Failed("index signature is not DIRC".into()));
        }
        let version = be32(head, 4);
        let count = be32(head, 8);
        if !(2..=4).contains(&version) {
            return Err(Refusal::Unsupported(format!("index version {version}")));
        }
        s.consume(12);
        let mut e = Entry::default();
        for _ in 0..count {
            if !next_entry(&mut s, version, hash, &mut e)? {
                return Err(Refusal::Failed("index ends before its entries".into()));
            }
            if e.mode == MODE_DIR {
                return Err(Refusal::Unsupported(
                    "sparse index (a sparse-directory entry)".into(),
                ));
            }
        }
        let entries_end = s.position();
        let mut ix = Index {
            file: None,
            version,
            hash,
            count,
            entries_end,
            mtime: meta.mtime(),
            cache_tree: Vec::new(),
            link: None,
            shared: None,
        };
        while s.position() < body_end {
            let head = s.need(8)?;
            let sig: [u8; 4] = [head[0], head[1], head[2], head[3]];
            let size = be32(head, 4) as usize;
            s.consume(8);
            let name = String::from_utf8_lossy(&sig).into_owned();
            match &sig {
                b"TREE" if size <= MAX_TREE_EXT => ix.cache_tree = decode_tree(&mut s, size, h)?,
                b"link" if size <= MAX_LINK_EXT => {
                    ix.link = Some(
                        decode_link(s.need(size)?, h)
                            .ok_or_else(|| Refusal::Failed("malformed link extension".into()))?,
                    );
                    s.consume(size);
                }
                b"link" => {
                    return Err(Refusal::Unsupported(format!(
                        "split index link of {size} bytes"
                    )));
                }
                b"sdir" => return Err(Refusal::Unsupported("sparse index (sdir)".into())),
                _ if sig[0].is_ascii_uppercase() => skip(&mut s, size)?,
                _ => {
                    return Err(Refusal::Unsupported(format!(
                        "required index extension {name:?}"
                    )));
                }
            }
        }
        if s.position() != body_end {
            return Err(Refusal::Failed(
                "an index extension runs past the checksum".into(),
            ));
        }
        verify(&file, len, hash)?;
        ix.file = Some(file);
        Ok(ix)
    }

    /// A stream over this file's own entries (None for an absent index).
    fn src(&self) -> Option<Src<'_>> {
        self.file.as_ref().map(|f| Src {
            s: Stream::new(f, 12, self.entries_end),
            version: self.version,
            left: self.count,
            e: Entry::default(),
        })
    }

    /// Every entry, in index order, a split index merged with its shared index.
    pub fn entries(&self) -> Result<Entries<'_>, Refusal> {
        let split = match (&self.link, &self.shared) {
            (Some(link), Some(shared)) => {
                let (Some(own), Some(shared)) = (self.src(), shared.src()) else {
                    return Err(Refusal::Failed("split index without its files".into()));
                };
                let mut added = self
                    .src()
                    .ok_or_else(|| Refusal::Failed("split index".into()))?;
                for _ in 0..link.replace.count() {
                    if !added.advance(self.hash)? {
                        return Err(Refusal::Failed(
                            "split index has fewer entries than replacements".into(),
                        ));
                    }
                }
                Some(Split {
                    link,
                    shared,
                    pos: 0,
                    replaced: own,
                    added,
                    head: Entry::default(),
                    shared_has: None,
                    added_has: None,
                })
            }
            _ => None,
        };
        Ok(Entries {
            hash: self.hash,
            plain: if split.is_none() { self.src() } else { None },
            split,
            cur: Entry::default(),
        })
    }

    /// The index's entries are byte-sorted by Git's cache_name_stage_compare, regardless
    /// of core.ignorecase. Equal paths may repeat for unmerged stages.
    pub fn verify_order(&self, ignore_case: bool) -> Result<(), Refusal> {
        if !ignore_case {
            return Ok(());
        }
        let mut entries = self.entries()?;
        let mut last: Option<Vec<u8>> = None;
        while let Some(e) = entries.read_next()? {
            if let Some(prev) = &last
                && e.path < *prev
            {
                return Err(Refusal::Failed("the index is not byte-sorted".into()));
            }
            last = Some(e.path.clone());
        }
        Ok(())
    }
}

fn skip(s: &mut Stream<'_>, mut n: usize) -> Result<(), Refusal> {
    while n > 0 {
        let have = s.chunk()?.len();
        if have == 0 {
            return Err(Refusal::Failed(
                "an index extension runs past the end".into(),
            ));
        }
        let k = have.min(n);
        s.consume(k);
        n -= k;
    }
    Ok(())
}

/// Hash everything before the trailer and compare; an all-zero trailer is Git's
/// `index.skipHash` and is accepted.
fn verify(file: &File, len: u64, hash: Hash) -> Result<(), Refusal> {
    let h = hash.oid_len();
    let body_end = len - h as u64;
    let mut trailer = vec![0u8; h];
    let mut got = 0;
    while got < h {
        got += read_at(file, &mut trailer[got..], body_end + got as u64)?;
    }
    if trailer.iter().all(|b| *b == 0) {
        return Ok(());
    }
    let mut hasher = hash.hasher();
    let mut s = Stream::new(file, 0, body_end);
    loop {
        let c = s.chunk()?;
        if c.is_empty() {
            break;
        }
        hasher.update(c);
        let n = c.len();
        s.consume(n);
    }
    if hasher.finish() != trailer {
        return Err(Refusal::Failed("index checksum mismatch".into()));
    }
    Ok(())
}

/// The bytes of `s` up to `delim` (consumed with it), within the `left` bytes of the
/// current extension.
fn take_until(s: &mut Stream<'_>, delim: u8, left: &mut usize) -> Result<Vec<u8>, Refusal> {
    let malformed = || Refusal::Failed("malformed cache-tree extension".into());
    let mut want = 1usize;
    loop {
        if want > *left {
            return Err(malformed());
        }
        let b = s.need(want)?;
        let avail = b.len().min(*left);
        if let Some(k) = b[..avail].iter().position(|c| *c == delim) {
            let out = b[..k].to_vec();
            s.consume(k + 1);
            *left -= k + 1;
            return Ok(out);
        }
        want = avail + 1;
    }
}

/// The cache-tree's valid nodes, streamed from `size` bytes of `s`: per node
/// `path NUL entry_count SP subtrees LF [oid]`, in pre-order. Returned sorted by
/// directory path (`""` for the top, else `a/b`).
fn decode_tree(s: &mut Stream<'_>, size: usize, h: usize) -> Result<Vec<(Vec<u8>, Oid)>, Refusal> {
    let malformed = || Refusal::Failed("malformed cache-tree extension".into());
    let number = |b: &[u8]| -> Result<i64, Refusal> {
        std::str::from_utf8(b)
            .ok()
            .and_then(|t| t.parse().ok())
            .ok_or_else(malformed)
    };
    let mut out = Vec::new();
    let mut left = size;
    // (path of the directory, children still to read)
    let mut stack: Vec<(Vec<u8>, usize)> = Vec::new();
    while left > 0 {
        let name = take_until(s, 0, &mut left)?;
        let entries = number(&take_until(s, b' ', &mut left)?)?;
        let subtrees =
            usize::try_from(number(&take_until(s, b'\n', &mut left)?)?).map_err(|_| malformed())?;
        while stack.last().is_some_and(|(_, children)| *children == 0) {
            stack.pop();
        }
        let path = match stack.last_mut() {
            None => name,
            Some((parent, children)) => {
                *children -= 1;
                match parent.is_empty() {
                    true => name,
                    false => [parent.as_slice(), b"/", &name].concat(),
                }
            }
        };
        if entries >= 0 {
            if left < h {
                return Err(malformed());
            }
            out.push((path.clone(), Oid::from_bytes(&s.need(h)?[..h])));
            s.consume(h);
            left -= h;
        }
        stack.push((path, subtrees));
    }
    out.sort_unstable();
    Ok(out)
}

fn decode_link(b: &[u8], h: usize) -> Option<Link> {
    Some(Link {
        shared: Oid::from_bytes(b.get(..h)?),
        bitmaps: b.get(h..)?.to_vec(),
        delete: Bits::default(),
        replace: Bits::default(),
    })
}

impl Link {
    /// Decode the delete and replace bitmaps of a shared index of `entries` entries; a
    /// bitmap declaring more bits than that is malformed, so no allocation exceeds one
    /// bit per shared entry.
    fn decode_bitmaps(&mut self, entries: u32) -> Option<()> {
        let rest = std::mem::take(&mut self.bitmaps);
        if rest.is_empty() {
            return Some(());
        }
        let (delete, used) = Bits::decode(&rest, entries as usize)?;
        let (replace, _) = Bits::decode(rest.get(used..)?, entries as usize)?;
        self.delete = delete;
        self.replace = replace;
        Some(())
    }
}

/// One stream of entries from one index file; `e` holds the last entry (v4 names
/// are relative to it).
struct Src<'a> {
    s: Stream<'a>,
    version: u32,
    left: u32,
    e: Entry,
}

impl Src<'_> {
    fn advance(&mut self, hash: Hash) -> Result<bool, Refusal> {
        if self.left == 0 {
            return Ok(false);
        }
        if !next_entry(&mut self.s, self.version, hash, &mut self.e)? {
            return Err(Refusal::Failed("index ends before its entries".into()));
        }
        self.left -= 1;
        Ok(true)
    }
}

/// A split index being merged: the shared index's entries not deleted, those marked
/// replaced swapped for the split part's first entries (an empty name keeps the shared
/// one), merged by (path, stage) with the split part's remaining, added entries.
struct Split<'a> {
    link: &'a Link,
    shared: Src<'a>,
    pos: usize,
    replaced: Src<'a>,
    added: Src<'a>,
    /// The next surviving shared entry, when `shared_has` is Some(true).
    head: Entry,
    shared_has: Option<bool>,
    added_has: Option<bool>,
}

impl Split<'_> {
    fn load_shared(&mut self, hash: Hash) -> Result<bool, Refusal> {
        while self.shared.advance(hash)? {
            let pos = self.pos;
            self.pos += 1;
            if self.link.delete.get(pos) {
                continue;
            }
            if self.link.replace.get(pos) {
                if !self.replaced.advance(hash)? {
                    return Err(Refusal::Failed("split index replacement missing".into()));
                }
                self.head.clone_from(&self.replaced.e);
                if self.head.path.is_empty() {
                    self.head.path.clone_from(&self.shared.e.path);
                }
            } else {
                self.head.clone_from(&self.shared.e);
            }
            return Ok(true);
        }
        Ok(false)
    }

    fn next_into(&mut self, hash: Hash, cur: &mut Entry) -> Result<bool, Refusal> {
        if self.shared_has.is_none() {
            self.shared_has = Some(self.load_shared(hash)?);
        }
        if self.added_has.is_none() {
            self.added_has = Some(self.added.advance(hash)?);
        }
        let shared_first = match (self.shared_has, self.added_has) {
            (Some(true), Some(true)) => {
                (&self.head.path, self.head.stage) <= (&self.added.e.path, self.added.e.stage)
            }
            (Some(true), _) => true,
            (_, Some(true)) => false,
            _ => return Ok(false),
        };
        match shared_first {
            true => {
                cur.clone_from(&self.head);
                self.shared_has = None;
            }
            false => {
                cur.clone_from(&self.added.e);
                self.added_has = None;
            }
        }
        Ok(true)
    }
}

/// The entries of an `Index`, streamed (see `Index::entries`).
pub struct Entries<'a> {
    hash: Hash,
    plain: Option<Src<'a>>,
    split: Option<Split<'a>>,
    cur: Entry,
}

impl Entries<'_> {
    /// The next entry, or None after the last.
    pub fn read_next(&mut self) -> Result<Option<&Entry>, Refusal> {
        let more = match (&mut self.plain, &mut self.split) {
            (Some(src), _) => src.advance(self.hash)?,
            (None, Some(split)) => split.next_into(self.hash, &mut self.cur)?,
            (None, None) => false,
        };
        Ok(match (more, &self.plain) {
            (false, _) => None,
            (true, Some(src)) => Some(&src.e),
            (true, None) => Some(&self.cur),
        })
    }
}
