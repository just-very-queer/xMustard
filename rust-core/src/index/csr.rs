//! The graph segment (PAR-IMP-02): a compact, immutable file derived from `index.db`
//! that the resident service reads instead of deserializing a JSON graph.
//!
//! # Layout
//!
//! `MAGIC`, then the sections in [`Sec`] order, then a JSON footer, its length (u32 LE)
//! and `MAGIC` again. The footer names each section's byte range, the counts and the
//! string tables of the small closed sets (edge kinds, layers, provenances, symbol
//! kinds). Every integer is little-endian.
//!
//! - Files are dense ids `0..F` in path order, so sorting by id is sorting by path.
//!   A file row is [`FileRow`] (32 B); paths are a sorted string heap (`PathOff` holds
//!   `F + 1` offsets into `PathHeap`).
//! - Symbols are dense ids `0..S` in `index.db` id order; a row is [`SymRow`] (16 B).
//!   Symbol names are a sorted, interned heap (`NameOff`/`NameHeap`); `NamePost` lists
//!   each name's symbols in (path, ord) order and `FileSyms` each file's symbols.
//! - Graph nodes are files `0..F` then symbols `F..F+S`. `Out`/`In` hold every edge of
//!   `index.db` as a forward and a reverse CSR (u32 offsets per node, 12-byte
//!   [`Edge`] records). A symbol-level edge (WS-17) sits on its symbol nodes; a
//!   file-level one (the lexical layer) on its file nodes.
//! - `FileOut`/`FileIn` are the file projection of the `structure` layer: one 8-byte
//!   [`FileEdge`] per ordered file pair (self pairs dropped), weights summed. Impact,
//!   trace, clusters and hotspots walk it.
//!
//! # Storage
//!
//! A segment is either read into memory whole ([`GraphStorage::Mem`]) or kept on disk
//! and read with `pread` through one process-wide block cache of bounded size
//! ([`GraphStorage::File`], the default). The choice was measured, see
//! `docs/benchmarks/2026-09-26-ws14-resident-index.md`: in memory, a 100k-symbol graph
//! at five edges per symbol is ~18 MiB per snapshot and twice that while a swap
//! double-buffers; file-backed, the service's resident cost is the cache line whatever
//! the graph size and however many snapshots overlap.
//!
//! A segment is written once per index generation, by the index worker after a build
//! or update commits (or by a reader that finds none), to a temporary name and renamed
//! into place. A reader holds its file descriptor, so a later rename never changes the
//! bytes under it: snapshots are immutable.

use std::collections::{BTreeMap, HashMap};
use std::fs::{self, File};
use std::io::{self, BufWriter, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, OnceLock};

use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::meta;

const MAGIC: &[u8; 8] = b"XMCSR01\n";
/// Bump on any layout change; a segment of another version is rebuilt.
pub const FORMAT_VERSION: u32 = 1;
const SEGMENT_PREFIX: &str = "graph.";
const SEGMENT_SUFFIX: &str = ".csr";

/// Sections, in file order.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum Sec {
    FileRows,
    PathOff,
    PathHeap,
    SymRows,
    NameOff,
    NameHeap,
    NamePostOff,
    NamePost,
    FileSymOff,
    FileSyms,
    OutOff,
    Out,
    InOff,
    In,
    FileOutOff,
    FileOut,
    FileInOff,
    FileIn,
}
const SECTIONS: usize = Sec::FileIn as usize + 1;

/// One CSR direction: its per-node offsets and its records.
#[derive(Debug, Clone, Copy)]
pub struct Adj {
    off: Sec,
    rec: Sec,
}
pub const OUT: Adj = Adj {
    off: Sec::OutOff,
    rec: Sec::Out,
};
pub const IN: Adj = Adj {
    off: Sec::InOff,
    rec: Sec::In,
};
pub const FILE_OUT: Adj = Adj {
    off: Sec::FileOutOff,
    rec: Sec::FileOut,
};
pub const FILE_IN: Adj = Adj {
    off: Sec::FileInOff,
    rec: Sec::FileIn,
};

// ---------------------------------------------------------------------------
// Fixed-width rows
// ---------------------------------------------------------------------------

/// A fixed-width record of a section.
pub trait Row: Sized {
    const SIZE: usize;
    fn decode(b: &[u8]) -> Self;
    fn encode(&self, out: &mut Vec<u8>);
}

fn le32(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes(b[at..at + 4].try_into().unwrap_or_default())
}

fn le64(b: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(b[at..at + 8].try_into().unwrap_or_default())
}

impl Row for u32 {
    const SIZE: usize = 4;
    fn decode(b: &[u8]) -> Self {
        le32(b, 0)
    }
    fn encode(&self, out: &mut Vec<u8>) {
        out.extend_from_slice(&self.to_le_bytes());
    }
}

/// A file: its stat at indexing time (for dirty checks) and precomputed structure
/// stats (inbound weight and distinct dependents over the `structure` layer).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct FileRow {
    pub size: u64,
    pub mtime_ns: i64,
    pub inbound: u32,
    pub dependents: u32,
    pub symbols: u32,
}

impl Row for FileRow {
    const SIZE: usize = 32;
    fn decode(b: &[u8]) -> Self {
        FileRow {
            size: le64(b, 0),
            mtime_ns: le64(b, 8) as i64,
            inbound: le32(b, 16),
            dependents: le32(b, 20),
            symbols: le32(b, 24),
        }
    }
    fn encode(&self, out: &mut Vec<u8>) {
        out.extend_from_slice(&self.size.to_le_bytes());
        out.extend_from_slice(&self.mtime_ns.to_le_bytes());
        for v in [self.inbound, self.dependents, self.symbols, 0] {
            out.extend_from_slice(&v.to_le_bytes());
        }
    }
}

/// A symbol: interned name, file, 1-based name line, kind (footer table) and whether
/// it is local to a function body.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct SymRow {
    pub name: u32,
    pub file: u32,
    pub line: u32,
    pub kind: u8,
    pub local: bool,
}

impl Row for SymRow {
    const SIZE: usize = 16;
    fn decode(b: &[u8]) -> Self {
        SymRow {
            name: le32(b, 0),
            file: le32(b, 4),
            line: le32(b, 8),
            kind: b[12],
            local: b[13] != 0,
        }
    }
    fn encode(&self, out: &mut Vec<u8>) {
        for v in [self.name, self.file, self.line] {
            out.extend_from_slice(&v.to_le_bytes());
        }
        out.extend_from_slice(&[self.kind, self.local as u8, 0, 0]);
    }
}

/// One edge as seen from a node: the other end, weight, and footer-table indexes of
/// kind, layer and provenance; confidence is quantized to 0..=255.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, PartialOrd, Ord, Hash)]
pub struct Edge {
    pub node: u32,
    pub weight: u32,
    pub kind: u8,
    pub layer: u8,
    pub provenance: u8,
    pub confidence: u8,
}

impl Row for Edge {
    const SIZE: usize = 12;
    fn decode(b: &[u8]) -> Self {
        Edge {
            node: le32(b, 0),
            weight: le32(b, 4),
            kind: b[8],
            layer: b[9],
            provenance: b[10],
            confidence: b[11],
        }
    }
    fn encode(&self, out: &mut Vec<u8>) {
        out.extend_from_slice(&self.node.to_le_bytes());
        out.extend_from_slice(&self.weight.to_le_bytes());
        out.extend_from_slice(&[self.kind, self.layer, self.provenance, self.confidence]);
    }
}

/// One file-projection edge: the other file and the summed weight.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct FileEdge {
    pub file: u32,
    pub weight: u32,
}

impl Row for FileEdge {
    const SIZE: usize = 8;
    fn decode(b: &[u8]) -> Self {
        FileEdge {
            file: le32(b, 0),
            weight: le32(b, 4),
        }
    }
    fn encode(&self, out: &mut Vec<u8>) {
        out.extend_from_slice(&self.file.to_le_bytes());
        out.extend_from_slice(&self.weight.to_le_bytes());
    }
}

pub fn quantize_confidence(c: f64) -> u8 {
    (c.clamp(0.0, 1.0) * 255.0).round() as u8
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct Counts {
    pub files: u32,
    pub symbols: u32,
    /// Symbols not local to a function body (the ones queries list).
    pub global_symbols: u32,
    pub names: u32,
    pub edges: u32,
    pub file_edges: u32,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct Footer {
    pub version: u32,
    pub generation: i64,
    pub counts: Counts,
    /// `[offset, length]` per section, in [`Sec`] order.
    sections: Vec<[u64; 2]>,
    pub edge_kinds: Vec<String>,
    pub layers: Vec<String>,
    pub provenances: Vec<String>,
    pub symbol_kinds: Vec<String>,
}

// ---------------------------------------------------------------------------
// Storage and the block cache
// ---------------------------------------------------------------------------

/// Where a segment's bytes live while it is resident.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GraphStorage {
    /// Read whole into the heap on open.
    Mem,
    /// Read on demand with `pread` through the process-wide block cache.
    File,
}

impl GraphStorage {
    /// `XMUSTARD_GRAPH_STORAGE=mem|file`; `file` by default (measured, see module doc).
    pub fn from_env() -> Self {
        match std::env::var("XMUSTARD_GRAPH_STORAGE").as_deref() {
            Ok("mem") => GraphStorage::Mem,
            _ => GraphStorage::File,
        }
    }
}

/// Block size of the cache.
pub const BLOCK: u64 = 16 << 10;
/// Default cache size: the component's steady line leaves room for this, the footer
/// and per-query working sets.
pub const DEFAULT_CACHE_KIB: u64 = 2048;

struct CacheEntry {
    data: Arc<[u8]>,
    used: u64,
}

/// Blocks of every file-backed segment, bounded in total: snapshots that overlap
/// during a swap share it, so a swap never doubles the resident graph.
pub struct BlockCache {
    cap_blocks: usize,
    map: Mutex<HashMap<(u64, u64), CacheEntry>>,
    tick: AtomicU64,
    hits: AtomicU64,
    misses: AtomicU64,
}

impl BlockCache {
    fn new(cap_bytes: u64) -> Self {
        BlockCache {
            cap_blocks: (cap_bytes / BLOCK).max(4) as usize,
            map: Mutex::new(HashMap::new()),
            tick: AtomicU64::new(0),
            hits: AtomicU64::new(0),
            misses: AtomicU64::new(0),
        }
    }

    fn lock(&self) -> MutexGuard<'_, HashMap<(u64, u64), CacheEntry>> {
        self.map.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn get(
        &self,
        key: (u64, u64),
        load: impl FnOnce() -> io::Result<Vec<u8>>,
    ) -> io::Result<Arc<[u8]>> {
        let used = self.tick.fetch_add(1, Ordering::Relaxed);
        if let Some(e) = self.lock().get_mut(&key) {
            e.used = used;
            self.hits.fetch_add(1, Ordering::Relaxed);
            return Ok(e.data.clone());
        }
        self.misses.fetch_add(1, Ordering::Relaxed);
        let data: Arc<[u8]> = load()?.into();
        let mut map = self.lock();
        if map.len() >= self.cap_blocks
            && let Some(old) = map.iter().min_by_key(|(_, e)| e.used).map(|(k, _)| *k)
        {
            map.remove(&old);
        }
        map.insert(
            key,
            CacheEntry {
                data: data.clone(),
                used,
            },
        );
        Ok(data)
    }

    fn purge(&self, segment: u64) {
        self.lock().retain(|(s, _), _| *s != segment);
    }

    pub fn resident_bytes(&self) -> u64 {
        self.lock().values().map(|e| e.data.len() as u64).sum()
    }

    pub fn stats(&self) -> serde_json::Value {
        serde_json::json!({
            "blocks": self.lock().len(),
            "cap_blocks": self.cap_blocks,
            "block_bytes": BLOCK,
            "hits": self.hits.load(Ordering::Relaxed),
            "misses": self.misses.load(Ordering::Relaxed),
        })
    }
}

/// The process-wide block cache (`XMUSTARD_GRAPH_CACHE_KIB`, default 4 MiB).
pub fn block_cache() -> &'static BlockCache {
    static CACHE: OnceLock<BlockCache> = OnceLock::new();
    CACHE.get_or_init(|| {
        let kib = std::env::var("XMUSTARD_GRAPH_CACHE_KIB")
            .ok()
            .and_then(|v| v.trim().parse::<u64>().ok())
            .unwrap_or(DEFAULT_CACHE_KIB);
        BlockCache::new(kib << 10)
    })
}

#[cfg(unix)]
fn pread(f: &File, buf: &mut [u8], off: u64) -> io::Result<()> {
    use std::os::unix::fs::FileExt;
    f.read_exact_at(buf, off)
}

#[cfg(windows)]
fn pread(f: &File, mut buf: &mut [u8], mut off: u64) -> io::Result<()> {
    use std::os::windows::fs::FileExt;
    while !buf.is_empty() {
        match f.seek_read(buf, off)? {
            0 => return Err(io::ErrorKind::UnexpectedEof.into()),
            n => {
                buf = &mut buf[n..];
                off += n as u64;
            }
        }
    }
    Ok(())
}

enum Bytes {
    Mem(Vec<u8>),
    File { file: File, len: u64, uid: u64 },
}

impl Bytes {
    fn read(&self, off: u64, buf: &mut [u8]) -> io::Result<()> {
        match self {
            Bytes::Mem(v) => {
                let end = off as usize + buf.len();
                let src = v
                    .get(off as usize..end)
                    .ok_or(io::ErrorKind::UnexpectedEof)?;
                buf.copy_from_slice(src);
                Ok(())
            }
            // a range of a block or more is a scan (search walks whole sections):
            // read it straight, so scans never evict the blocks random walks reuse
            Bytes::File { file, .. } if buf.len() as u64 >= BLOCK => pread(file, buf, off),
            Bytes::File { file, len, uid } => {
                let cache = block_cache();
                let mut done = 0usize;
                while done < buf.len() {
                    let pos = off + done as u64;
                    let block = pos / BLOCK;
                    let start = block * BLOCK;
                    let data = cache.get((*uid, block), || {
                        let n = BLOCK.min(len.saturating_sub(start)) as usize;
                        let mut b = vec![0u8; n];
                        pread(file, &mut b, start)?;
                        Ok(b)
                    })?;
                    let from = (pos - start) as usize;
                    let take = (data.len().saturating_sub(from)).min(buf.len() - done);
                    if take == 0 {
                        return Err(io::ErrorKind::UnexpectedEof.into());
                    }
                    buf[done..done + take].copy_from_slice(&data[from..from + take]);
                    done += take;
                }
                Ok(())
            }
        }
    }

    fn resident_bytes(&self) -> u64 {
        match self {
            Bytes::Mem(v) => v.capacity() as u64,
            Bytes::File { .. } => 0,
        }
    }
}

impl Drop for Bytes {
    fn drop(&mut self) {
        if let Bytes::File { uid, .. } = self {
            block_cache().purge(*uid);
        }
    }
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

/// An open, immutable graph segment.
pub struct Segment {
    bytes: Bytes,
    pub footer: Footer,
    pub path: PathBuf,
    pub storage: GraphStorage,
}

fn corrupt(path: &Path, what: &str) -> String {
    format!("graph segment {} is corrupt: {what}", path.display())
}

impl Segment {
    pub fn open(path: &Path, storage: GraphStorage) -> Result<Segment, String> {
        let err = |e: io::Error| format!("{}: {e}", path.display());
        let file = File::open(path).map_err(err)?;
        let len = file.metadata().map_err(err)?.len();
        let tail_len = (MAGIC.len() + 4) as u64;
        if len < MAGIC.len() as u64 + tail_len {
            return Err(corrupt(path, "too short"));
        }
        let mut tail = vec![0u8; tail_len as usize];
        pread(&file, &mut tail, len - tail_len).map_err(err)?;
        let mut head = [0u8; 8];
        pread(&file, &mut head, 0).map_err(err)?;
        if &tail[4..] != MAGIC || &head != MAGIC {
            return Err(corrupt(path, "bad magic"));
        }
        let footer_len = le32(&tail, 0) as u64;
        let footer_at = (len - tail_len)
            .checked_sub(footer_len)
            .ok_or_else(|| corrupt(path, "footer length"))?;
        let mut raw = vec![0u8; footer_len as usize];
        pread(&file, &mut raw, footer_at).map_err(err)?;
        let footer: Footer =
            serde_json::from_slice(&raw).map_err(|e| corrupt(path, &e.to_string()))?;
        if footer.version != FORMAT_VERSION || footer.sections.len() != SECTIONS {
            return Err(corrupt(path, "unsupported version"));
        }
        if footer.sections.iter().any(|[o, l]| o + l > footer_at) {
            return Err(corrupt(path, "section out of range"));
        }
        let bytes = match storage {
            GraphStorage::Mem => {
                let mut all = vec![0u8; footer_at as usize];
                pread(&file, &mut all, 0).map_err(err)?;
                Bytes::Mem(all)
            }
            GraphStorage::File => {
                static NEXT_UID: AtomicU64 = AtomicU64::new(1);
                Bytes::File {
                    file,
                    len,
                    uid: NEXT_UID.fetch_add(1, Ordering::Relaxed),
                }
            }
        };
        Ok(Segment {
            bytes,
            footer,
            path: path.to_path_buf(),
            storage,
        })
    }

    /// Heap bytes this segment holds itself (not the shared block cache).
    pub fn resident_bytes(&self) -> u64 {
        self.bytes.resident_bytes()
    }

    fn range(&self, sec: Sec) -> [u64; 2] {
        self.footer.sections[sec as usize]
    }

    /// `n` bytes at `at` within `sec`.
    pub(super) fn bytes(&self, sec: Sec, at: u64, n: usize) -> Result<Vec<u8>, String> {
        let [off, len] = self.range(sec);
        if at + n as u64 > len {
            return Err(corrupt(&self.path, "read past a section"));
        }
        let mut buf = vec![0u8; n];
        self.bytes
            .read(off + at, &mut buf)
            .map_err(|e| format!("{}: {e}", self.path.display()))?;
        Ok(buf)
    }

    /// Rows `first..first + n` of a section.
    pub(super) fn rows<T: Row>(&self, sec: Sec, first: u64, n: usize) -> Result<Vec<T>, String> {
        let raw = self.bytes(sec, first * T::SIZE as u64, n * T::SIZE)?;
        Ok(raw.chunks_exact(T::SIZE).map(T::decode).collect())
    }

    pub(super) fn row<T: Row>(&self, sec: Sec, i: u64) -> Result<T, String> {
        Ok(self.rows::<T>(sec, i, 1)?.remove(0))
    }

    /// The `[start, end)` record range of entry `i` of an offsets section.
    pub(super) fn span(&self, off: Sec, i: u32) -> Result<(u64, usize), String> {
        let o = self.rows::<u32>(off, i as u64, 2)?;
        let n = o[1]
            .checked_sub(o[0])
            .ok_or_else(|| corrupt(&self.path, "offsets not monotonic"))?;
        Ok((o[0] as u64, n as usize))
    }

    /// The records of node `i` in one CSR direction.
    pub fn neighbours<T: Row>(&self, adj: Adj, i: u32) -> Result<Vec<T>, String> {
        let (start, n) = self.span(adj.off, i)?;
        self.rows(adj.rec, start, n)
    }

    pub fn counts(&self) -> &Counts {
        &self.footer.counts
    }

    pub fn node_count(&self) -> u32 {
        self.footer.counts.files + self.footer.counts.symbols
    }

    /// The graph node of a symbol.
    pub fn symbol_node(&self, sym: u32) -> u32 {
        self.footer.counts.files + sym
    }

    pub fn file_row(&self, f: u32) -> Result<FileRow, String> {
        self.row(Sec::FileRows, f as u64)
    }

    pub fn file_rows(&self) -> Result<Vec<FileRow>, String> {
        self.rows(Sec::FileRows, 0, self.footer.counts.files as usize)
    }

    pub fn symbol(&self, s: u32) -> Result<SymRow, String> {
        self.row(Sec::SymRows, s as u64)
    }

    pub fn file_symbols(&self, f: u32) -> Result<Vec<u32>, String> {
        let (start, n) = self.span(Sec::FileSymOff, f)?;
        self.rows(Sec::FileSyms, start, n)
    }

    fn table(list: &[String], i: u8) -> &str {
        list.get(i as usize).map(String::as_str).unwrap_or("")
    }

    pub fn edge_kind(&self, i: u8) -> &str {
        Self::table(&self.footer.edge_kinds, i)
    }

    pub fn layer(&self, i: u8) -> &str {
        Self::table(&self.footer.layers, i)
    }

    pub fn provenance(&self, i: u8) -> &str {
        Self::table(&self.footer.provenances, i)
    }

    pub fn symbol_kind(&self, i: u8) -> &str {
        Self::table(&self.footer.symbol_kinds, i)
    }
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

/// A new segment file name for `generation`: unique per write, so a segment left by a
/// failed update is never taken for a later one (readers follow `meta::GRAPH_SEGMENT`).
fn segment_name(generation: i64) -> String {
    static WRITES: AtomicU64 = AtomicU64::new(0);
    let mut h = Sha256::new();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default();
    h.update(now.as_nanos().to_le_bytes());
    h.update(std::process::id().to_le_bytes());
    h.update(WRITES.fetch_add(1, Ordering::Relaxed).to_le_bytes());
    let nonce = format!("{:x}", h.finalize());
    format!(
        "{SEGMENT_PREFIX}{generation}.{}{SEGMENT_SUFFIX}",
        &nonce[..16]
    )
}

/// Interns the strings of a closed set into footer-table indexes.
#[derive(Default)]
struct Table {
    names: Vec<String>,
    index: HashMap<String, u8>,
}

impl Table {
    fn id(&mut self, s: &str) -> Result<u8, String> {
        if let Some(i) = self.index.get(s) {
            return Ok(*i);
        }
        let i = u8::try_from(self.names.len())
            .map_err(|_| "more than 256 distinct values in a graph table".to_string())?;
        self.names.push(s.to_string());
        self.index.insert(s.to_string(), i);
        Ok(i)
    }
}

/// Sequential section writer that records each section's range.
struct SegWriter {
    out: BufWriter<File>,
    pos: u64,
    sections: Vec<[u64; 2]>,
}

impl SegWriter {
    fn put(&mut self, b: &[u8]) -> Result<(), String> {
        self.out.write_all(b).map_err(|e| e.to_string())?;
        self.pos += b.len() as u64;
        Ok(())
    }

    fn begin(&mut self, sec: Sec) {
        debug_assert_eq!(self.sections.len(), sec as usize, "sections out of order");
        self.sections.push([self.pos, 0]);
    }

    fn end(&mut self) {
        if let Some(s) = self.sections.last_mut() {
            s[1] = self.pos - s[0];
        }
    }

    /// Write a whole section from encoded rows.
    fn section<T: Row>(&mut self, sec: Sec, rows: &[T]) -> Result<(), String> {
        self.begin(sec);
        let mut buf = Vec::with_capacity(rows.len().min(4096) * T::SIZE);
        for r in rows {
            r.encode(&mut buf);
            if buf.len() >= 64 << 10 {
                self.put(&buf)?;
                buf.clear();
            }
        }
        self.put(&buf)?;
        self.end();
        Ok(())
    }

    fn raw_section(&mut self, sec: Sec, bytes: &[u8]) -> Result<(), String> {
        self.begin(sec);
        self.put(bytes)?;
        self.end();
        Ok(())
    }

    /// Write one CSR direction from records that arrive grouped by node in ascending
    /// order: the records first, then the `nodes + 1` offsets.
    fn csr<T: Row>(
        &mut self,
        adj: Adj,
        nodes: u32,
        mut rows: impl FnMut(&mut dyn FnMut(u32, T) -> Result<(), String>) -> Result<(), String>,
    ) -> Result<u32, String> {
        let mut counts = vec![0u32; nodes as usize + 1];
        let mut last = 0u32;
        let mut buf = Vec::with_capacity(64 << 10);
        // records section first so it streams; offsets follow it
        let rec_start = self.pos;
        let mut total = 0u32;
        {
            let mut push = |node: u32, rec: T| -> Result<(), String> {
                if node < last || node >= nodes {
                    return Err(format!(
                        "edge node {node} out of order (after {last}, of {nodes})"
                    ));
                }
                last = node;
                counts[node as usize + 1] += 1;
                total += 1;
                rec.encode(&mut buf);
                if buf.len() >= 64 << 10 {
                    self.out.write_all(&buf).map_err(|e| e.to_string())?;
                    self.pos += buf.len() as u64;
                    buf.clear();
                }
                Ok(())
            };
            rows(&mut push)?;
        }
        self.put(&buf)?;
        let rec_len = self.pos - rec_start;
        for i in 1..counts.len() {
            counts[i] += counts[i - 1];
        }
        // sections are recorded in Sec order: offsets precede records in the table
        self.begin(adj.off);
        let off_at = self.pos;
        let mut ob = Vec::with_capacity(counts.len() * 4);
        for c in &counts {
            ob.extend_from_slice(&c.to_le_bytes());
        }
        self.put(&ob)?;
        self.sections[adj.off as usize] = [off_at, self.pos - off_at];
        self.sections.push([rec_start, rec_len]);
        Ok(total)
    }
}

/// Dense ids for `index.db` ids.
struct Remap {
    files: HashMap<i64, u32>,
    /// Store ids of the symbols, ascending: a symbol's dense id is its index.
    symbols: Vec<i64>,
    file_count: u32,
}

impl Remap {
    fn node(&self, file: i64, symbol: Option<i64>) -> Option<u32> {
        match symbol {
            Some(s) => self
                .symbols
                .binary_search(&s)
                .ok()
                .map(|d| self.file_count + d as u32),
            None => self.files.get(&file).copied(),
        }
    }
}

fn sql(e: rusqlite::Error) -> String {
    format!("index store: {e}")
}

fn indexed_status_list() -> String {
    meta::INDEXED_STATUSES
        .iter()
        .map(|s| format!("'{s}'"))
        .collect::<Vec<_>>()
        .join(",")
}

/// Order of one CSR direction's rows: grouped by that end's node in dense order (file
/// nodes by path, then symbol nodes by id), then by the other end, kind and layer.
fn edge_query(from: &str, to: &str) -> String {
    format!(
        "SELECT e.{from}_file, e.{from}_symbol, e.{to}_file, e.{to}_symbol, e.kind, e.layer,
                e.weight, e.confidence, e.provenance
         FROM edges e JOIN files a ON a.id = e.{from}_file JOIN files b ON b.id = e.{to}_file
         ORDER BY e.{from}_symbol IS NOT NULL, CASE WHEN e.{from}_symbol IS NULL THEN a.path END,
                  e.{from}_symbol, e.{to}_symbol IS NOT NULL,
                  CASE WHEN e.{to}_symbol IS NULL THEN b.path END, e.{to}_symbol, e.kind, e.layer"
    )
}

fn file_edge_query(from: &str, to: &str) -> String {
    format!(
        "SELECT e.{from}_file, e.{to}_file, sum(e.weight) FROM edges e
         JOIN files a ON a.id = e.{from}_file JOIN files b ON b.id = e.{to}_file
         WHERE e.layer = 'structure' AND e.src_file != e.dst_file
         GROUP BY e.{from}_file, e.{to}_file ORDER BY a.path, b.path"
    )
}

/// Write the segment of the state `conn` sees into `dir` and return its file name. Inside
/// the caller's open transaction it sees that transaction's writes (an update writes
/// the segment before it commits, so readers never meet a generation without one);
/// otherwise it runs in its own read transaction. Either way the segment is one
/// generation's graph. Memory is the dense-id maps and per-node offsets; edges stream
/// from SQLite. Only index writers call this: readers never write.
pub fn write_segment(conn: &Connection, dir: &Path) -> Result<String, String> {
    // small page caches while the segment's sorts and groupings run: they spill to
    // temporary files instead of growing the worker (restored after)
    let cache: i64 = conn
        .query_row("PRAGMA cache_size", [], |r| r.get(0))
        .map_err(sql)?;
    conn.execute_batch(&format!(
        "PRAGMA cache_size=-{SEGMENT_CACHE_KIB}; PRAGMA temp.cache_size=-{SEGMENT_CACHE_KIB};"
    ))
    .map_err(sql)?;
    let res = if conn.is_autocommit() {
        conn.execute_batch("BEGIN").map_err(sql)?;
        let res = write_segment_in_tx(conn, dir);
        let _ = conn.execute_batch("COMMIT");
        res
    } else {
        write_segment_in_tx(conn, dir)
    };
    let _ = conn.execute_batch(&format!("PRAGMA cache_size={cache};"));
    res
}

/// Page cache while a segment is written, in KiB.
const SEGMENT_CACHE_KIB: i64 = 256;

fn write_segment_in_tx(conn: &Connection, dir: &Path) -> Result<String, String> {
    let generation: i64 = meta::get(conn, "generation")
        .map_err(sql)?
        .and_then(|g| g.parse().ok())
        .unwrap_or(0);
    let name = segment_name(generation);
    let final_path = dir.join(&name);
    let tmp = dir.join(format!("{name}.tmp"));
    let file = {
        let mut o = fs::OpenOptions::new();
        o.create(true).write(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            o.mode(0o600);
        }
        o.open(&tmp)
            .map_err(|e| format!("{}: {e}", tmp.display()))?
    };
    let mut w = SegWriter {
        out: BufWriter::with_capacity(64 << 10, file),
        pos: 0,
        sections: Vec::with_capacity(SECTIONS),
    };
    let result = write_body(conn, &mut w, generation);
    let result = result.and_then(|()| {
        w.out.flush().map_err(|e| e.to_string())?;
        fs::rename(&tmp, &final_path).map_err(|e| format!("{}: {e}", final_path.display()))
    });
    if let Err(e) = result {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    Ok(name)
}

fn write_body(conn: &Connection, w: &mut SegWriter, generation: i64) -> Result<(), String> {
    w.put(MAGIC)?;
    let statuses = indexed_status_list();

    // ---- files, in path order, with structure stats ----
    let mut stats: HashMap<i64, (u32, u32)> = HashMap::new();
    {
        let mut st = conn
            .prepare(
                "SELECT dst_file, sum(weight), count(DISTINCT src_file) FROM edges
                 WHERE layer = 'structure' AND src_file != dst_file GROUP BY dst_file",
            )
            .map_err(sql)?;
        let rows = st
            .query_map([], |r| {
                Ok((
                    r.get::<_, i64>(0)?,
                    r.get::<_, i64>(1)?,
                    r.get::<_, i64>(2)?,
                ))
            })
            .map_err(sql)?;
        for row in rows {
            let (f, wsum, deps) = row.map_err(sql)?;
            stats.insert(f, (wsum.clamp(0, u32::MAX as i64) as u32, deps as u32));
        }
    }
    let mut files = HashMap::new();
    let mut file_rows = Vec::new();
    let mut path_off = vec![0u32];
    let mut path_heap = Vec::new();
    {
        let mut st = conn
            .prepare(&format!(
                "SELECT id, path, size, mtime_ns FROM files WHERE parse_status IN ({statuses}) ORDER BY path"
            ))
            .map_err(sql)?;
        let rows = st
            .query_map([], |r| {
                Ok((
                    r.get::<_, i64>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, i64>(2)?,
                    r.get::<_, i64>(3)?,
                ))
            })
            .map_err(sql)?;
        for row in rows {
            let (id, path, size, mtime_ns) = row.map_err(sql)?;
            files.insert(id, file_rows.len() as u32);
            let (inbound, dependents) = stats.get(&id).copied().unwrap_or_default();
            file_rows.push(FileRow {
                size: size.max(0) as u64,
                mtime_ns,
                inbound,
                dependents,
                symbols: 0,
            });
            path_heap.extend_from_slice(path.as_bytes());
            path_off.push(u32::try_from(path_heap.len()).map_err(|_| "path heap over 4 GiB")?);
        }
    }
    drop(stats);
    let file_count = file_rows.len() as u32;

    // ---- symbols, in id order; a dense id is the rank of the store id ----
    let mut kinds = Table::default();
    let mut sym_ids: Vec<i64> = Vec::new();
    let mut sym_name_ids: Vec<i64> = Vec::new();
    let mut sym_ord: Vec<u32> = Vec::new();
    let mut sym_rows = Vec::new();
    {
        let mut st = conn
            .prepare(
                "SELECT id, file_id, name_id, name_line, kind, local, ord FROM symbols ORDER BY id",
            )
            .map_err(sql)?;
        let mut rows = st.query([]).map_err(sql)?;
        while let Some(r) = rows.next().map_err(sql)? {
            let Some(&file) = files.get(&r.get::<_, i64>(1).map_err(sql)?) else {
                continue;
            };
            let kind: String = r.get(4).map_err(sql)?;
            sym_ids.push(r.get(0).map_err(sql)?);
            sym_name_ids.push(r.get(2).map_err(sql)?);
            sym_ord.push(r.get::<_, i64>(6).map_err(sql)?.max(0) as u32);
            file_rows[file as usize].symbols += 1;
            sym_rows.push(SymRow {
                name: 0, // set once the names are ranked
                file,
                line: r.get::<_, i64>(3).map_err(sql)?.max(0) as u32,
                kind: kinds.id(&kind)?,
                local: r.get::<_, i64>(5).map_err(sql)? != 0,
            });
        }
    }
    let sym_count = sym_rows.len() as u32;
    let global_symbols = sym_rows.iter().filter(|s| !s.local).count() as u32;

    // ---- the names those symbols use, sorted and interned: a scan of the names
    // index in order (no sort), keeping the used ones ----
    let mut used = sym_name_ids.clone();
    used.sort_unstable();
    used.dedup();
    let mut rank = vec![0u32; used.len()];
    let mut name_off = vec![0u32];
    let mut name_heap = Vec::new();
    {
        let mut st = conn
            .prepare("SELECT id, name FROM names ORDER BY name")
            .map_err(sql)?;
        let mut rows = st.query([]).map_err(sql)?;
        while let Some(r) = rows.next().map_err(sql)? {
            let Ok(i) = used.binary_search(&r.get::<_, i64>(0).map_err(sql)?) else {
                continue;
            };
            rank[i] = name_off.len() as u32 - 1;
            name_heap.extend_from_slice(
                r.get_ref(1)
                    .map_err(sql)?
                    .as_bytes()
                    .map_err(|e| e.to_string())?,
            );
            name_off.push(u32::try_from(name_heap.len()).map_err(|_| "name heap over 4 GiB")?);
        }
    }
    for (row, id) in sym_rows.iter_mut().zip(&sym_name_ids) {
        row.name = used
            .binary_search(id)
            .map(|i| rank[i])
            .map_err(|_| "symbol names a missing name")?;
    }
    drop((sym_name_ids, used, rank));

    // name -> symbols and file -> symbols postings, both in (path, ord) order
    let key = |s: &u32| {
        let r = &sym_rows[*s as usize];
        (r.file, sym_ord[*s as usize])
    };
    let mut file_syms: Vec<u32> = (0..sym_count).collect();
    file_syms.sort_unstable_by_key(key);
    let mut name_post = file_syms.clone();
    name_post.sort_by_key(|s| sym_rows[*s as usize].name); // stable: (path, ord) within a name
    drop(sym_ord);
    let offsets = |groups: usize, key: &dyn Fn(u32) -> u32, list: &[u32]| -> Vec<u32> {
        let mut off = vec![0u32; groups + 1];
        for &s in list {
            off[key(s) as usize + 1] += 1;
        }
        for i in 1..off.len() {
            off[i] += off[i - 1];
        }
        off
    };
    let name_post_off = offsets(
        name_off.len() - 1,
        &|s| sym_rows[s as usize].name,
        &name_post,
    );
    let file_sym_off = offsets(
        file_count as usize,
        &|s| sym_rows[s as usize].file,
        &file_syms,
    );

    w.section(Sec::FileRows, &file_rows)?;
    w.section(Sec::PathOff, &path_off)?;
    w.raw_section(Sec::PathHeap, &path_heap)?;
    w.section(Sec::SymRows, &sym_rows)?;
    w.section(Sec::NameOff, &name_off)?;
    w.raw_section(Sec::NameHeap, &name_heap)?;
    w.section(Sec::NamePostOff, &name_post_off)?;
    w.section(Sec::NamePost, &name_post)?;
    w.section(Sec::FileSymOff, &file_sym_off)?;
    w.section(Sec::FileSyms, &file_syms)?;
    let name_count = name_off.len() as u32 - 1;
    drop((
        file_rows, path_off, path_heap, sym_rows, name_off, name_heap,
    ));
    drop((name_post, file_syms, name_post_off, file_sym_off));

    // ---- every edge, forward and reverse ----
    let remap = Remap {
        files,
        symbols: sym_ids,
        file_count,
    };
    let nodes = file_count + sym_count;
    let (mut edge_kinds, mut layers, mut provs) =
        (Table::default(), Table::default(), Table::default());
    let mut edge_dir = |w: &mut SegWriter, adj: Adj, from: &str, to: &str| -> Result<u32, String> {
        let mut st = conn.prepare(&edge_query(from, to)).map_err(sql)?;
        w.csr::<Edge>(adj, nodes, |push| {
            let mut rows = st.query([]).map_err(sql)?;
            while let Some(r) = rows.next().map_err(sql)? {
                let node = |f: usize, s: usize| -> Result<Option<u32>, String> {
                    Ok(remap.node(r.get(f).map_err(sql)?, r.get(s).map_err(sql)?))
                };
                let (Some(a), Some(b)) = (node(0, 1)?, node(2, 3)?) else {
                    continue;
                };
                let kind: String = r.get(4).map_err(sql)?;
                let layer: String = r.get(5).map_err(sql)?;
                let prov: String = r.get(8).map_err(sql)?;
                push(
                    a,
                    Edge {
                        node: b,
                        weight: r.get::<_, i64>(6).map_err(sql)?.clamp(0, u32::MAX as i64) as u32,
                        kind: edge_kinds.id(&kind)?,
                        layer: layers.id(&layer)?,
                        provenance: provs.id(&prov)?,
                        confidence: quantize_confidence(r.get(7).map_err(sql)?),
                    },
                )?;
            }
            Ok(())
        })
    };
    let edges = edge_dir(w, OUT, "src", "dst")?;
    let reverse = edge_dir(w, IN, "dst", "src")?;
    if edges != reverse {
        return Err(format!(
            "forward and reverse edge counts differ ({edges} != {reverse})"
        ));
    }

    // ---- file projection of the structure layer ----
    let file_dir = |w: &mut SegWriter, adj: Adj, from: &str, to: &str| -> Result<u32, String> {
        let mut st = conn.prepare(&file_edge_query(from, to)).map_err(sql)?;
        w.csr::<FileEdge>(adj, file_count, |push| {
            let rows = st
                .query_map([], |r| {
                    Ok((
                        r.get::<_, i64>(0)?,
                        r.get::<_, i64>(1)?,
                        r.get::<_, i64>(2)?,
                    ))
                })
                .map_err(sql)?;
            for row in rows {
                let (a, b, weight) = row.map_err(sql)?;
                if let (Some(&a), Some(&b)) = (remap.files.get(&a), remap.files.get(&b)) {
                    push(
                        a,
                        FileEdge {
                            file: b,
                            weight: weight.clamp(0, u32::MAX as i64) as u32,
                        },
                    )?;
                }
            }
            Ok(())
        })
    };
    let file_edges = file_dir(w, FILE_OUT, "src", "dst")?;
    file_dir(w, FILE_IN, "dst", "src")?;

    let footer = Footer {
        version: FORMAT_VERSION,
        generation,
        counts: Counts {
            files: file_count,
            symbols: sym_count,
            global_symbols,
            names: name_count,
            edges,
            file_edges,
        },
        sections: std::mem::take(&mut w.sections),
        edge_kinds: edge_kinds.names,
        layers: layers.names,
        provenances: provs.names,
        symbol_kinds: kinds.names,
    };
    let raw = serde_json::to_vec(&footer).map_err(|e| e.to_string())?;
    w.put(&raw)?;
    w.put(&(raw.len() as u32).to_le_bytes())?;
    w.put(MAGIC)
}

/// Remove segments other than `keep` and temporaries older than ten minutes (index
/// writers call this after their commit). Readers keep their open descriptors, so
/// removing a segment in use is safe; a reader that has not opened it yet rereads meta.
pub fn sweep_segments(dir: &Path, keep: &str) {
    let Ok(rd) = fs::read_dir(dir) else { return };
    let now = std::time::SystemTime::now();
    for e in rd.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if !name.starts_with(SEGMENT_PREFIX) || name == keep {
            continue;
        }
        let stale_tmp = name.ends_with(".tmp")
            && e.metadata()
                .ok()
                .and_then(|m| m.modified().ok())
                .and_then(|m| now.duration_since(m).ok())
                .is_some_and(|age| age.as_secs() > 600);
        if name.ends_with(SEGMENT_SUFFIX) || stale_tmp {
            let _ = fs::remove_file(e.path());
        }
    }
}

/// The edges of a segment as `(from node, edge)` in forward order, for checks.
pub fn all_edges(seg: &Segment, adj: Adj) -> Result<BTreeMap<u32, Vec<Edge>>, String> {
    let mut out = BTreeMap::new();
    for n in 0..seg.node_count() {
        let e = seg.neighbours::<Edge>(adj, n)?;
        if !e.is_empty() {
            out.insert(n, e);
        }
    }
    Ok(out)
}
