//! The query side of the code index (PAR-RT-01, WS-14): snapshots of the graph segment,
//! kept resident by `xmustard-core serve`, and the graph queries search, explain and
//! impact run on them.
//!
//! A [`Snapshot`] is one generation's segment plus its coverage. Inside `serve`,
//! [`Snapshots`] keeps one per repository root (LRU-bounded, dropped by the idle trim).
//! Each read stats `index.db` and its WAL: when neither changed and the commit relation
//! is younger than `envelope::TTL`, the resident snapshot answers without opening the
//! store or running Git. When the store changed, the reader rereads its meta and swaps
//! in the segment meta names, which the index writer wrote before committing the
//! generation; readers never write. Readers of the old snapshot keep it through their
//! `Arc` and its open descriptor, so a swap never tears a read. Concurrent readers of
//! one root wait for one reload (in-flight dedupe).
//!
//! Outside `serve` every call opens the store and the segment once.
//!
//! Queries walk the file projection of the `structure` layer with the legacy graph's
//! rules and orderings (files sorted by path, ties by path), so while edges are lexical
//! the answers equal the legacy graph's on the same edges (`tests/index_query.rs`).

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet, VecDeque};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, OnceLock};

use rusqlite::{Connection, OpenFlags};
use serde_json::{Value, json};

use super::config::IndexConfig;
use super::csr::{self, FILE_IN, FILE_OUT, FileEdge, GraphStorage, Segment};
use super::envelope::{self, Freshness, Relation};
use super::{DB_FILE, meta, scan, schema};
use crate::symbolgraph::{
    CoverageLoss, FileCluster, FileRef, Hotspot, ImpactedFile, IndexCoverage, IndexWork,
    QueryGraph, SourceIdentitySummary, SymbolImpact, SymbolRef, SymbolTrace, dominant_directory,
};

fn sql(e: rusqlite::Error) -> String {
    format!("index store: {e}")
}

/// Where a root's index lives.
#[derive(Debug, Clone)]
struct Location {
    db: PathBuf,
}

/// The index of `root`, when the root is indexable and has a store.
fn locate(root: &Path) -> Option<Location> {
    let cfg = IndexConfig::load(root).ok()?;
    let layout = scan::layout(root, &cfg).ok()?;
    let db = super::index_dir(&layout.root, layout.git_dir.as_deref(), &cfg).join(DB_FILE);
    db.exists().then_some(Location { db })
}

/// Stat of the store and its WAL: a commit or a swap changes it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Stamp([(u64, u64, i64); 2]);

fn stamp(db: &Path) -> Option<Stamp> {
    let key = |p: &Path| {
        fs::metadata(p).ok().map(|m| {
            let k = scan::StatKey::of(&m);
            (k.ino, k.size, k.mtime_ns)
        })
    };
    let wal = PathBuf::from(format!("{}-wal", db.display()));
    Some(Stamp([key(db)?, key(&wal).unwrap_or_default()]))
}

/// The meta a reader needs, read in one statement (one consistent snapshot).
#[derive(Debug, Clone, Default)]
pub struct StoreMeta {
    pub generation: i64,
    /// File name of the generation's graph segment.
    pub graph_segment: String,
    pub last_commit: String,
    pub repo_mode: String,
    pub schema_version: String,
    pub analyzer_version: String,
    /// The repository identity key the last update was run for (`index update
    /// --identity-key`, stamped by the Go orchestrator), or empty.
    pub identity_key: String,
    /// The last run's counters (meta `last_run`).
    pub last_run: Option<envelope::RefreshCounters>,
    /// Coverage as of the generation; taken by the snapshot at load.
    pub coverage: Option<meta::Coverage>,
}

fn read_meta(conn: &Connection) -> Result<StoreMeta, String> {
    let mut st = conn.prepare("SELECT key, value FROM meta").map_err(sql)?;
    let rows = st
        .query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)))
        .map_err(sql)?;
    let mut m = StoreMeta::default();
    for row in rows {
        let (k, v) = row.map_err(sql)?;
        match k.as_str() {
            "generation" => m.generation = v.parse().unwrap_or(0),
            meta::GRAPH_SEGMENT => m.graph_segment = v,
            "last_commit" => m.last_commit = v,
            "repo_mode" => m.repo_mode = v,
            "schema_version" => m.schema_version = v,
            "analyzer_version" => m.analyzer_version = v,
            meta::IDENTITY_KEY => m.identity_key = v,
            "last_run" => m.last_run = serde_json::from_str(&v).ok(),
            "coverage" => m.coverage = serde_json::from_str(&v).ok(),
            _ => {}
        }
    }
    Ok(m)
}

fn open_store(db: &Path) -> Result<Connection, String> {
    let conn = Connection::open_with_flags(db, OpenFlags::SQLITE_OPEN_READ_ONLY).map_err(sql)?;
    schema::configure(&conn).map_err(sql)?;
    Ok(conn)
}

/// The graph segment of the store's current generation, opened, and that generation's
/// meta. Readers never write: a store without a segment (built before WS-14) is an
/// error until its next update writes one. A segment removed by a newer update between
/// the meta read and the open sends the reader back to meta.
pub fn load_segment(db: &Path, storage: GraphStorage) -> Result<(Segment, StoreMeta), String> {
    let dir = db.parent().ok_or("index store has no directory")?;
    let conn = open_store(db)?;
    for _ in 0..3 {
        let m = read_meta(&conn)?;
        if m.graph_segment.is_empty() {
            return Err(
                "the store has no graph segment yet; the next index update writes it".into(),
            );
        }
        let Ok(seg) = Segment::open(&dir.join(&m.graph_segment), storage) else {
            continue;
        };
        if seg.footer.generation != m.generation {
            return Err(format!(
                "graph segment {} is generation {}, meta says {}",
                m.graph_segment, seg.footer.generation, m.generation
            ));
        }
        return Ok((seg, m));
    }
    Err("the store kept changing while its graph segment was opened".into())
}

/// The per-file table every query touches, resident with its snapshot (about 50 bytes
/// per file, so at most ~0.5 MiB inside the envelope): paths as one string heap, and
/// the structure stats of the file rows.
struct Files {
    heap: String,
    off: Vec<u32>,
    inbound: Vec<u32>,
    dependents: Vec<u32>,
    symbols: Vec<u32>,
}

impl Files {
    fn load(seg: &Segment) -> Result<Files, String> {
        let (heap, off) = seg.path_heap()?;
        let rows = seg.file_rows()?;
        Ok(Files {
            heap,
            off,
            inbound: rows.iter().map(|r| r.inbound).collect(),
            dependents: rows.iter().map(|r| r.dependents).collect(),
            symbols: rows.iter().map(|r| r.symbols).collect(),
        })
    }

    fn path(&self, f: u32) -> &str {
        let f = f as usize;
        &self.heap[self.off[f] as usize..self.off[f + 1] as usize]
    }
}

/// One generation of the index, as queries read it.
pub struct Snapshot {
    pub seg: Segment,
    pub generation: i64,
    pub index_version: String,
    pub coverage: IndexCoverage,
    files: Files,
    root: PathBuf,
    clusters: OnceLock<Result<Communities, String>>,
    /// The store this generation was read from.
    db: PathBuf,
    /// The text lanes' read-only connection (`fts`), opened on first use.
    text: Mutex<Option<Connection>>,
}

impl Snapshot {
    fn load(
        root: &Path,
        loc: &Location,
        storage: GraphStorage,
    ) -> Result<(Snapshot, Arc<StoreMeta>), String> {
        let (seg, mut m) = load_segment(&loc.db, storage)?;
        let coverage = legacy_coverage(&m, seg.counts().global_symbols as usize);
        m.coverage = None;
        Ok((
            Snapshot {
                files: Files::load(&seg)?,
                generation: m.generation,
                index_version: format!("schema-{}.csr-{}", m.schema_version, csr::FORMAT_VERSION),
                coverage,
                root: root.to_path_buf(),
                seg,
                clusters: OnceLock::new(),
                db: loc.db.clone(),
                text: Mutex::new(None),
            },
            Arc::new(m),
        ))
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Run `f` on this snapshot's text connection (the BM25 lanes and chunk rows of
    /// `index.db`), opened read-only on first use and kept with the snapshot: its page
    /// cache (`fts::TEXT_CACHE_KIB`, no mmap) is the resident BM25 cache, and it goes
    /// when a newer generation replaces the snapshot. Readers of one snapshot take turns.
    pub fn with_text<R>(
        &self,
        f: impl FnOnce(&Connection) -> rusqlite::Result<R>,
    ) -> Result<R, String> {
        let mut text = lock(&self.text);
        if text.is_none() {
            *text = Some(super::fts::open(&self.db).map_err(sql)?);
        }
        let conn = text.as_ref().ok_or("index text connection unavailable")?;
        f(conn).map_err(sql)
    }

    /// Files declaring `symbol` (non-local declarations), in path order.
    fn defining_files(&self, symbol: &str) -> Result<BTreeSet<u32>, String> {
        let Some(n) = self.seg.name_id(symbol)? else {
            return Ok(BTreeSet::new());
        };
        let mut out = BTreeSet::new();
        for s in self.seg.symbols_named(n)? {
            let row = self.seg.symbol(s)?;
            if !row.local {
                out.insert(row.file);
            }
        }
        Ok(out)
    }

    /// Distinct neighbours over both directions of the file projection, in path order.
    fn undirected(&self, f: u32) -> Result<BTreeSet<u32>, String> {
        let mut out = BTreeSet::new();
        for adj in [FILE_OUT, FILE_IN] {
            out.extend(
                self.seg
                    .neighbours::<FileEdge>(adj, f)?
                    .iter()
                    .map(|e| e.file),
            );
        }
        Ok(out)
    }

    fn paths(&self, ids: impl IntoIterator<Item = u32>) -> Vec<String> {
        ids.into_iter()
            .map(|f| self.files.path(f).to_string())
            .collect()
    }

    fn communities(&self) -> Result<&Communities, String> {
        self.clusters
            .get_or_init(|| Communities::compute(&self.seg))
            .as_ref()
            .map_err(Clone::clone)
    }

    fn cluster(&self, c: &Communities, rank: usize) -> Result<FileCluster, String> {
        let files = self.paths(c.members[rank].iter().copied());
        Ok(FileCluster {
            cluster_id: rank,
            label: dominant_directory(&files),
            size: files.len(),
            files,
        })
    }
}

/// Label-propagation communities over the undirected, weighted file projection (the
/// legacy `compute_clusters` rules), grouped and ranked by size.
struct Communities {
    /// Rank of each file's cluster.
    rank_of: Vec<u32>,
    /// Members per rank, in path order.
    members: Vec<Vec<u32>>,
}

impl Communities {
    /// Adjacency is read from the segment on every pass rather than held: memory stays
    /// one label per file whatever the edge count.
    fn compute(seg: &Segment) -> Result<Communities, String> {
        let n = seg.counts().files;
        let mut community: Vec<u32> = (0..n).collect();
        for _ in 0..20 {
            let mut changed = false;
            for f in 0..n as usize {
                let mut by_comm: BTreeMap<u32, u64> = BTreeMap::new();
                for dir in [FILE_OUT, FILE_IN] {
                    for e in seg.neighbours::<FileEdge>(dir, f as u32)? {
                        *by_comm.entry(community[e.file as usize]).or_default() += e.weight as u64;
                    }
                }
                // heaviest community; ties go to the smallest id (earliest path)
                if let Some((&best, _)) = by_comm
                    .iter()
                    .max_by(|a, b| a.1.cmp(b.1).then_with(|| b.0.cmp(a.0)))
                    && community[f] != best
                {
                    community[f] = best;
                    changed = true;
                }
            }
            if !changed {
                break;
            }
        }
        let mut groups: BTreeMap<u32, Vec<u32>> = BTreeMap::new();
        for (f, c) in community.iter().enumerate() {
            groups.entry(*c).or_default().push(f as u32);
        }
        let mut members: Vec<Vec<u32>> = groups.into_values().collect();
        members.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a[0].cmp(&b[0])));
        let mut rank_of = vec![0u32; n as usize];
        for (r, m) in members.iter().enumerate() {
            for f in m {
                rank_of[*f as usize] = r as u32;
            }
        }
        Ok(Communities { rank_of, members })
    }
}

fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

impl QueryGraph for Snapshot {
    fn symbol_count(&self) -> usize {
        self.seg.counts().global_symbols as usize
    }

    fn for_each_symbol(&self, f: &mut dyn FnMut(SymbolRef<'_>)) -> Result<(), String> {
        self.seg.for_each_name(&mut |name, syms| {
            for &s in syms {
                let row = self.seg.symbol(s)?;
                if row.local {
                    continue;
                }
                f(SymbolRef {
                    name,
                    path: self.files.path(row.file),
                    line: Some(row.line as usize),
                    file_inbound: self.files.inbound[row.file as usize] as usize,
                });
            }
            Ok(())
        })
    }

    fn for_each_file(&self, f: &mut dyn FnMut(FileRef<'_>)) -> Result<(), String> {
        for (i, inbound) in self.files.inbound.iter().enumerate() {
            f(FileRef {
                path: self.files.path(i as u32),
                inbound: *inbound as usize,
                symbols: self.files.symbols[i] as usize,
            });
        }
        Ok(())
    }

    fn for_each_edge(&self, f: &mut dyn FnMut(&str, &str)) -> Result<(), String> {
        let seg = &self.seg;
        let Some(structure) = seg.footer.layers.iter().position(|l| l == "structure") else {
            return Ok(());
        };
        let files = seg.counts().files;
        let file_of = |n: u32| -> Result<u32, String> {
            match n.checked_sub(files) {
                Some(s) => Ok(seg.symbol(s)?.file),
                None => Ok(n),
            }
        };
        for n in 0..seg.node_count() {
            let from = file_of(n)?;
            for e in seg.neighbours::<csr::Edge>(csr::OUT, n)? {
                let to = file_of(e.node)?;
                if e.layer as usize == structure && from != to {
                    f(self.files.path(from), self.files.path(to));
                }
            }
        }
        Ok(())
    }

    fn hotspots(&self, limit: usize) -> Result<Vec<Hotspot>, String> {
        let fs = &self.files;
        let mut hot: Vec<u32> = (0..fs.inbound.len() as u32)
            .filter(|f| fs.dependents[*f as usize] > 0)
            .collect();
        // ids are path order, so the id breaks weight ties like the legacy path order
        hot.sort_by(|a, b| {
            fs.inbound[*b as usize]
                .cmp(&fs.inbound[*a as usize])
                .then(a.cmp(b))
        });
        hot.truncate(limit);
        Ok(hot
            .into_iter()
            .map(|f| Hotspot {
                path: fs.path(f).to_string(),
                inbound_weight: fs.inbound[f as usize] as usize,
                dependent_count: fs.dependents[f as usize] as usize,
            })
            .collect())
    }

    fn impact(&self, symbol: &str, max_depth: usize) -> Result<SymbolImpact, String> {
        let defined = self.defining_files(symbol)?;
        let mut visited: HashSet<u32> = defined.iter().copied().collect();
        let mut frontier: Vec<u32> = defined.iter().copied().collect();
        let mut impacted = Vec::new();
        let mut depth = 1;
        while !frontier.is_empty() && depth <= max_depth {
            let mut next = BTreeSet::new();
            for f in &frontier {
                for e in self.seg.neighbours::<FileEdge>(FILE_IN, *f)? {
                    if visited.insert(e.file) {
                        next.insert(e.file);
                    }
                }
            }
            for f in &next {
                impacted.push(ImpactedFile {
                    path: self.files.path(*f).to_string(),
                    distance: depth,
                });
            }
            frontier = next.into_iter().collect();
            depth += 1;
        }
        Ok(SymbolImpact {
            symbol: symbol.to_string(),
            defined_in: self.paths(defined),
            impacted_count: impacted.len(),
            impacted,
            max_depth,
            generated_at: now(),
            ..Default::default()
        })
    }

    fn trace(&self, from: &str, to: &str) -> Result<SymbolTrace, String> {
        let from_files = self.defining_files(from)?;
        let to_files = self.defining_files(to)?;
        let mut prev: HashMap<u32, u32> = HashMap::new();
        let mut visited: HashSet<u32> = from_files.iter().copied().collect();
        let mut queue: VecDeque<u32> = from_files.iter().copied().collect();
        let mut hit = None;
        while let Some(f) = queue.pop_front() {
            if to_files.contains(&f) {
                hit = Some(f);
                break;
            }
            for n in self.undirected(f)? {
                if visited.insert(n) {
                    prev.insert(n, f);
                    queue.push_back(n);
                }
            }
        }
        let mut ids = Vec::new();
        let mut cur = hit;
        while let Some(c) = cur {
            ids.push(c);
            cur = prev.get(&c).copied();
        }
        ids.reverse();
        let path = self.paths(ids);
        Ok(SymbolTrace {
            from: from.to_string(),
            to: to.to_string(),
            length: path.len().saturating_sub(1),
            found: !path.is_empty(),
            path,
            generated_at: now(),
            ..Default::default()
        })
    }

    fn clusters(&self) -> Result<Vec<FileCluster>, String> {
        let c = self.communities()?;
        (0..c.members.len()).map(|r| self.cluster(c, r)).collect()
    }

    fn cluster_of(&self, path: &str) -> Result<Option<FileCluster>, String> {
        let Some(f) = self.seg.file_id(path)? else {
            return Ok(None);
        };
        let c = self.communities()?;
        self.cluster(c, c.rank_of[f as usize] as usize).map(Some)
    }

    fn indexed_stat(&self, path: &str) -> Option<(u64, i64)> {
        let f = self.seg.file_id(path).ok()??;
        let row = self.seg.file_row(f).ok()?;
        Some((row.size, row.mtime_ns))
    }
}

/// The index's coverage in the shape search has always reported, plus the envelope.
fn legacy_coverage(m: &StoreMeta, symbols: usize) -> IndexCoverage {
    let c = m.coverage.clone().unwrap_or_default();
    let symbols_truncated = c.loss_counts.get("symbols_truncated").copied().unwrap_or(0);
    let truncated = !c.envelope.exceeded.is_empty() || symbols_truncated > 0;
    let degraded: Vec<String> = c
        .loss_counts
        .iter()
        .map(|(r, n)| format!("{r}={n}"))
        .collect();
    IndexCoverage {
        repo_mode: m.repo_mode.clone(),
        eligible_files: c.eligible_files,
        indexed_files: c.indexed_files,
        truncated,
        max_files: c.envelope.max_files,
        degraded_reason: (!degraded.is_empty())
            .then(|| format!("coverage losses: {}", degraded.join(", "))),
        selected_files: c
            .eligible_files
            .saturating_sub(c.loss_counts.get("envelope_files").copied().unwrap_or(0)),
        complete: c.complete,
        worktree_deleted_files: c.worktree_deleted_files,
        symbols_truncated_files: symbols_truncated,
        losses: c
            .losses
            .iter()
            .map(|l| CoverageLoss {
                path: l.path.clone(),
                reason: l.reason.clone(),
                detail: String::new(),
                content_indexed: l.content_indexed,
            })
            .collect(),
        loss_counts: c.loss_counts.clone(),
        losses_truncated: c.losses_truncated,
        extraction: c.extraction.clone(),
        // per-language support (WS-16): the index's own counts, where `failed` is a
        // lexical fallback past the parse bounds or a parse with errors
        languages: c.language_support.clone(),
        work: IndexWork {
            graph_cache: "hit".into(),
            graph_cache_detail: format!("code index generation {}", m.generation),
            lock: "not_used".into(),
            symbols_indexed: symbols,
            coverage_losses: c.loss_counts.values().sum(),
            ..Default::default()
        },
        envelope: Some(c.envelope),
        ..Default::default()
    }
}

/// A snapshot opened for one read, with the store meta and the commit relation it is
/// judged against.
pub struct Opened {
    pub snapshot: Arc<Snapshot>,
    pub meta: Arc<StoreMeta>,
    pub relation: Relation,
    /// `resident_index` or `index`.
    pub source: &'static str,
}

impl Opened {
    /// The snapshot's coverage with the identity the store was last brought to. The
    /// read itself parses nothing: index work happens in the index worker, and the
    /// orchestrator reports the refresh it ran for the call. `stable` holds when `HEAD`
    /// is still the indexed commit; working-tree edits since the generation are listed
    /// per result path by the freshness envelope.
    pub fn coverage(&self) -> IndexCoverage {
        let mut c = self.snapshot.coverage.clone();
        c.source_identity = SourceIdentitySummary {
            key: self.meta.identity_key.clone(),
            head: self.meta.last_commit.clone(),
            parser_version: self.meta.analyzer_version.clone(),
            identity_complete: !self.meta.identity_key.is_empty(),
            stable: self.relation.status == envelope::Status::Current,
            limitations: 0,
        };
        c
    }

    /// The freshness envelope for a result touching `paths`.
    pub fn freshness<'a>(&self, paths: impl IntoIterator<Item = &'a str>) -> Freshness {
        let s = &self.snapshot;
        let mut f = Freshness::from_relation(
            self.source,
            &s.root,
            &self.relation,
            s.index_version.clone(),
            Some(s.generation),
        );
        f.refresh = self.meta.last_run.clone();
        f.check_paths(&s.root, paths, |p| s.indexed_stat(p));
        f
    }
}

/// The index snapshot for `root`: resident inside `serve`, opened for this call
/// otherwise. None when the root has no usable index (the caller falls back).
pub fn open(root: &Path) -> Option<Opened> {
    let root = fs::canonicalize(root).ok()?;
    match RESIDENT.get() {
        Some(r) => r.open(&root),
        None => {
            let loc = locate(&root)?;
            open_at(&root, &loc.db, GraphStorage::from_env())
                .inspect_err(|e| eprintln!("index: {}: {e}", root.display()))
                .ok()
        }
    }
}

/// Open the store at `db` (the index of `root`) for one read.
pub fn open_at(root: &Path, db: &Path, storage: GraphStorage) -> Result<Opened, String> {
    let loc = Location {
        db: db.to_path_buf(),
    };
    let (snap, meta) = Snapshot::load(root, &loc, storage)?;
    Ok(Opened {
        relation: envelope::relate(root, &meta.last_commit),
        snapshot: Arc::new(snap),
        meta,
        source: "index",
    })
}

// ---------------------------------------------------------------------------
// Resident snapshots
// ---------------------------------------------------------------------------

struct Entry {
    loc: Location,
    stamp: Stamp,
    snapshot: Arc<Snapshot>,
    meta: Arc<StoreMeta>,
    relation: Relation,
}

struct Slot {
    root: PathBuf,
    trust: String,
    state: Mutex<Option<Entry>>,
}

/// Index snapshots kept by `serve`, one per repository root and trust scope,
/// least-recently-used first out past `max_roots`.
pub struct Snapshots {
    max_roots: usize,
    storage: GraphStorage,
    slots: Mutex<Vec<Arc<Slot>>>,
    loads: AtomicU64,
    swaps: AtomicU64,
    hits: AtomicU64,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

impl Snapshots {
    pub fn new(max_roots: usize, storage: GraphStorage) -> Self {
        Snapshots {
            max_roots,
            storage,
            slots: Mutex::new(Vec::new()),
            loads: AtomicU64::new(0),
            swaps: AtomicU64::new(0),
            hits: AtomicU64::new(0),
        }
    }

    fn slot(&self, root: &Path) -> Arc<Slot> {
        let trust = crate::indexcache::trust_scope();
        let mut slots = lock(&self.slots);
        let slot = match slots
            .iter()
            .position(|s| s.root == root && s.trust == trust)
        {
            Some(i) => slots.remove(i),
            None => Arc::new(Slot {
                root: root.to_path_buf(),
                trust,
                state: Mutex::new(None),
            }),
        };
        slots.push(slot.clone());
        let excess = slots.len().saturating_sub(self.max_roots.max(1));
        slots.drain(..excess);
        slot
    }

    /// The snapshot for the canonical `root`, reloaded when its store changed. When the
    /// store changed but its new state cannot be read yet, the previous snapshot keeps
    /// answering and the next read looks again.
    pub fn open(&self, root: &Path) -> Option<Opened> {
        let slot = self.slot(root);
        let mut state = lock(&slot.state);
        let now_stamp = state.as_ref().and_then(|e| stamp(&e.loc.db));
        if let Some(e) = state.as_mut()
            && Some(e.stamp) == now_stamp
        {
            if !e.relation.fresh(root) {
                e.relation = envelope::relate(root, &e.relation.indexed_commit);
            }
            self.hits.fetch_add(1, Ordering::Relaxed);
            return Some(e.opened());
        }
        // the store changed, or is new to this process: reread it
        let loc = match (state.as_ref(), now_stamp) {
            (Some(e), Some(_)) => e.loc.clone(),
            _ => match locate(root) {
                Some(l) => l,
                None => {
                    *state = None;
                    return None;
                }
            },
        };
        let Some(st) = stamp(&loc.db) else {
            *state = None;
            return None;
        };
        let (snapshot, meta) = match (state.as_ref(), read_meta_at(&loc.db)) {
            // same segment (a no-op update rewrote meta): keep the snapshot
            (Some(p), Some(mut m)) if m.graph_segment == p.meta.graph_segment => {
                m.coverage = None;
                (p.snapshot.clone(), Arc::new(m))
            }
            _ => match self.load(root, &loc) {
                Some(loaded) => loaded,
                None => return state.as_ref().map(Entry::opened),
            },
        };
        let prev = state.take();
        if prev
            .as_ref()
            .is_some_and(|p| p.snapshot.generation != snapshot.generation)
        {
            self.swaps.fetch_add(1, Ordering::Relaxed);
        }
        let relation = match prev {
            Some(p) if p.relation.indexed_commit == meta.last_commit && p.relation.fresh(root) => {
                p.relation
            }
            _ => envelope::relate(root, &meta.last_commit),
        };
        let e = Entry {
            loc,
            stamp: st,
            snapshot,
            meta,
            relation,
        };
        let opened = e.opened();
        *state = Some(e);
        Some(opened)
    }

    /// Reload `root`'s snapshot when one is resident, so the next read finds the new
    /// generation loaded (the watcher calls this after its refresh). A root with no
    /// resident snapshot is left alone: loading it could evict another root's.
    pub fn reload_if_resident(&self, root: &Path) {
        let trust = crate::indexcache::trust_scope();
        // the slot list is released before the slot's state is locked: a load in
        // progress holds the state, and other roots' reads must not wait for it
        let slot = lock(&self.slots)
            .iter()
            .find(|s| s.root == root && s.trust == trust)
            .cloned();
        if slot.is_some_and(|s| lock(&s.state).is_some()) {
            self.open(root);
        }
    }

    fn load(&self, root: &Path, loc: &Location) -> Option<(Arc<Snapshot>, Arc<StoreMeta>)> {
        self.loads.fetch_add(1, Ordering::Relaxed);
        Snapshot::load(root, loc, self.storage)
            .inspect_err(|e| eprintln!("index: {}: {e}", root.display()))
            .ok()
            .map(|(s, m)| (Arc::new(s), m))
    }

    /// Drop every snapshot; returns how many roots were resident.
    pub fn clear(&self) -> usize {
        let mut slots = lock(&self.slots);
        let n = slots.len();
        slots.clear();
        n
    }

    pub fn is_empty(&self) -> bool {
        lock(&self.slots).is_empty()
    }

    pub fn stats(&self) -> Value {
        let slots = lock(&self.slots).clone();
        let resident: Vec<Value> = slots
            .iter()
            .filter_map(|s| {
                let st = lock(&s.state);
                st.as_ref().map(|e| {
                    json!({
                        "generation": e.snapshot.generation,
                        "storage": format!("{:?}", e.snapshot.seg.storage).to_lowercase(),
                        "segment_heap_bytes": e.snapshot.seg.resident_bytes(),
                        "counts": e.snapshot.seg.counts(),
                    })
                })
            })
            .collect();
        json!({
            "resident": resident,
            "max_roots": self.max_roots,
            "loads": self.loads.load(Ordering::Relaxed),
            "swaps": self.swaps.load(Ordering::Relaxed),
            "hits": self.hits.load(Ordering::Relaxed),
            "block_cache": csr::block_cache().stats(),
        })
    }
}

fn read_meta_at(db: &Path) -> Option<StoreMeta> {
    read_meta(&open_store(db).ok()?).ok()
}

impl Entry {
    fn opened(&self) -> Opened {
        Opened {
            snapshot: self.snapshot.clone(),
            meta: self.meta.clone(),
            relation: self.relation.clone(),
            source: "resident_index",
        }
    }
}

static RESIDENT: OnceLock<Snapshots> = OnceLock::new();

/// Keep index snapshots resident in this process (`serve` only).
pub fn enable_resident(max_roots: usize) -> &'static Snapshots {
    RESIDENT.get_or_init(|| Snapshots::new(max_roots, GraphStorage::from_env()))
}

pub fn resident() -> Option<&'static Snapshots> {
    RESIDENT.get()
}

#[cfg(test)]
mod tests {
    use super::*;

    // Coverage comes from meta JSON with defaulted fields, so its counts need not agree:
    // more envelope losses than eligible files must not underflow.
    #[test]
    fn legacy_coverage_survives_inconsistent_counts() {
        let mut c = meta::Coverage {
            eligible_files: 3,
            ..Default::default()
        };
        c.loss_counts.insert("envelope_files".into(), 7);
        let m = StoreMeta {
            coverage: Some(c),
            ..Default::default()
        };
        assert_eq!(legacy_coverage(&m, 0).selected_files, 0);
    }

    // Search, explain and impact answered from the index report the index's
    // per-language support counts (WS-16, PAR-SYM-05), as the legacy graph does.
    #[test]
    fn index_coverage_carries_per_language_support() {
        use crate::index::lang::{LanguageCoverage, Support};
        let mut python = LanguageCoverage::default();
        python.add(Support::Supported);
        python.add(Support::Failed);
        let mut c = meta::Coverage::default();
        c.language_support.insert("python".into(), python.clone());
        let m = StoreMeta {
            coverage: Some(c),
            ..Default::default()
        };
        let cov = legacy_coverage(&m, 0);
        assert_eq!(cov.languages.get("python"), Some(&python));
        let summary = crate::symbolgraph::CoverageSummary::from(&cov);
        assert_eq!(summary.languages.get("python"), Some(&python));
    }
}
