//! Lexical file-to-file edges in `index.db` (provenance `lexical`), ported from the
//! legacy builder in `symbolgraph.rs` so the resident service (WS-14) can reach legacy
//! parity before the scope resolver (WS-17) adds typed symbol edges.
//!
//! The algorithm is the legacy one: a name defined by exactly one file (≥4 bytes, not a
//! stopword) anchors an edge from every other file that mentions it; relative imports
//! resolve to files; supertype names become `inherits` edges; test files get `tests`
//! edges; flow context gives the separate `returns`/`branches`/`writes` layer. Weight is
//! the number of distinct contributing names (flow: occurrences) and `via` keeps the
//! first eight in name order.
//!
//! Intentional differences from the legacy graph:
//! - the mentions are the file's references, so words inside comments and string
//!   literals no longer create edges (the known lexical false edges);
//! - definers are non-local declarations only (a closure or helper declared inside a
//!   function cannot be referenced from another file), top-level function-valued
//!   constants count as functions, and an `impl Foo` block does not make `Foo` ambiguous;
//! - ESM `./x.js` specifiers resolve to `x.ts` sources.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use rusqlite::{Connection, params};

use super::facts::{flow, ref_kind};

const MIN_NAME_LEN: usize = 4;
/// Names this common produce noisy edges; they never anchor one (legacy list).
const STOPWORD_SYMBOLS: &[&str] = &[
    "main", "test", "tests", "init", "new", "build", "run", "string", "error", "result", "value",
    "data", "name", "path", "self", "this", "type", "node", "item", "list",
];
/// Symbol kinds that define a referenceable name (the legacy extractor's kinds).
const DEFINER_KINDS: &str =
    "'Function','Method','Class','Struct','Enum','Interface','Trait','TypeAlias','Union'";

pub const LEXICAL_CONFIDENCE: f64 = 0.5;
pub const RELATIVE_IMPORT_CONFIDENCE: f64 = 0.9;

/// Test files reference code under test; they get `tests` edges (legacy rule).
pub fn is_test_file(path: &str) -> bool {
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Def {
    One { file: i64, callable: bool },
    Ambiguous,
}

/// name id → its unique defining file (or ambiguity).
pub struct Definers {
    map: HashMap<i64, Def>,
}

impl Definers {
    pub fn load(conn: &Connection) -> rusqlite::Result<Definers> {
        let sql = format!(
            "SELECT s.name_id, n.name, s.file_id, s.kind FROM symbols s
             JOIN names n ON n.id = s.name_id JOIN files f ON f.id = s.file_id
             WHERE s.local = 0 AND s.kind IN ({DEFINER_KINDS})
             ORDER BY f.path, s.ord"
        );
        let mut st = conn.prepare(&sql)?;
        let mut rows = st.query([])?;
        let mut map: HashMap<i64, Def> = HashMap::new();
        while let Some(r) = rows.next()? {
            let name_id: i64 = r.get(0)?;
            let name: String = r.get(1)?;
            if name.len() < MIN_NAME_LEN || STOPWORD_SYMBOLS.contains(&name.to_lowercase().as_str())
            {
                continue;
            }
            let file: i64 = r.get(2)?;
            let kind: String = r.get(3)?;
            let callable = matches!(kind.as_str(), "Function" | "Method");
            map.entry(name_id)
                .and_modify(|d| {
                    // a second defining FILE makes the name ambiguous; a repeat in the
                    // same file keeps the first definition.
                    if let Def::One { file: f, .. } = d
                        && *f != file
                    {
                        *d = Def::Ambiguous;
                    }
                })
                .or_insert(Def::One { file, callable });
        }
        Ok(Definers { map })
    }

    fn unique(&self, name_id: i64) -> Option<(i64, bool)> {
        match self.map.get(&name_id) {
            Some(Def::One { file, callable }) => Some((*file, *callable)),
            _ => None,
        }
    }

    /// Definer status of the given names, for detecting which names an update changed.
    pub fn snapshot(&self, names: &BTreeSet<i64>) -> BTreeMap<i64, Option<(i64, bool)>> {
        names
            .iter()
            .map(|n| {
                (
                    *n,
                    self.map.get(n).map(|d| match d {
                        Def::One { file, callable } => (*file, *callable),
                        Def::Ambiguous => (-1, false),
                    }),
                )
            })
            .collect()
    }
}

#[derive(Default)]
struct Agg {
    weight: i64,
    via: Vec<String>,
    confidence: f64,
}

fn add(
    agg: &mut BTreeMap<(i64, &'static str), Agg>,
    to: i64,
    kind: &'static str,
    via: &str,
    n: i64,
    conf: f64,
) {
    let e = agg.entry((to, kind)).or_default();
    e.weight += n;
    if e.via.len() < 8 && !e.via.iter().any(|v| v == via) {
        e.via.push(via.to_string());
    }
    if conf > e.confidence {
        e.confidence = conf;
    }
}

/// Recompute the outgoing lexical edges of one file. Returns edges written.
pub fn rebuild_file_edges(
    conn: &Connection,
    defs: &Definers,
    fid: i64,
    path: &str,
) -> rusqlite::Result<usize> {
    conn.prepare_cached("DELETE FROM edges WHERE src_file = ?1")?
        .execute([fid])?;
    let test = is_test_file(path);
    let mut structure: BTreeMap<(i64, &'static str), Agg> = BTreeMap::new();
    let mut flows: BTreeMap<(i64, &'static str), Agg> = BTreeMap::new();

    // relative imports resolved to files, in source order
    {
        let mut st = conn.prepare_cached(
            "SELECT module, resolved_file_id FROM imports
             WHERE file_id = ?1 AND resolved_file_id IS NOT NULL ORDER BY ord",
        )?;
        let rows = st.query_map([fid], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)))?;
        for row in rows {
            let (spec, to) = row?;
            if to != fid {
                add(
                    &mut structure,
                    to,
                    "imports",
                    &spec,
                    1,
                    RELATIVE_IMPORT_CONFIDENCE,
                );
            }
        }
    }

    // reference names by kind and flow, in name order
    let mut inherits: BTreeMap<String, i64> = BTreeMap::new();
    let mut imports: BTreeMap<String, i64> = BTreeMap::new();
    let mut words: BTreeMap<String, i64> = BTreeMap::new();
    let mut flow_counts: BTreeMap<(String, &'static str), (i64, i64)> = BTreeMap::new();
    {
        let mut st = conn.prepare_cached(
            "SELECT n.name, r.name_id, r.kind, r.flow, count(*) FROM refs r
             JOIN names n ON n.id = r.name_id WHERE r.file_id = ?1
             GROUP BY r.name_id, r.kind, r.flow",
        )?;
        let rows = st.query_map([fid], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, i64>(1)?,
                r.get::<_, u8>(2)?,
                r.get::<_, u8>(3)?,
                r.get::<_, i64>(4)?,
            ))
        })?;
        for row in rows {
            let (name, id, kind, fl, n) = row?;
            if defs.unique(id).is_none() {
                continue;
            }
            match kind {
                ref_kind::EXTENDS | ref_kind::IMPLEMENTS => {
                    inherits.insert(name.clone(), id);
                }
                ref_kind::IMPORT => {
                    imports.insert(name.clone(), id);
                }
                _ => {}
            }
            words.insert(name.clone(), id);
            let label = match fl {
                flow::RETURNS => Some("returns"),
                flow::BRANCHES => Some("branches"),
                flow::WRITES => Some("writes"),
                _ => None,
            };
            if let Some(label) = label {
                let e = flow_counts.entry((name, label)).or_insert((id, 0));
                e.1 += n;
            }
        }
    }
    for (name, id) in &inherits {
        if let Some((to, _)) = defs.unique(*id)
            && to != fid
        {
            add(&mut structure, to, "inherits", name, 1, LEXICAL_CONFIDENCE);
        }
    }
    for (name, id) in &imports {
        if let Some((to, _)) = defs.unique(*id)
            && to != fid
        {
            add(&mut structure, to, "imports", name, 1, LEXICAL_CONFIDENCE);
        }
    }
    for (name, id) in &words {
        if inherits.contains_key(name) {
            continue; // already a typed inheritance edge
        }
        if let Some((to, callable)) = defs.unique(*id)
            && to != fid
        {
            let kind = if test {
                "tests"
            } else if callable {
                "calls"
            } else {
                "references"
            };
            add(&mut structure, to, kind, name, 1, LEXICAL_CONFIDENCE);
        }
    }
    for ((name, label), (id, n)) in &flow_counts {
        if let Some((to, _)) = defs.unique(*id)
            && to != fid
        {
            add(&mut flows, to, label, name, *n, LEXICAL_CONFIDENCE);
        }
    }

    let mut ins = conn.prepare_cached(
        "INSERT INTO edges(src_file, dst_file, src_symbol, dst_symbol, kind, layer, weight,
         confidence, provenance, via) VALUES (?1, ?2, NULL, NULL, ?3, ?4, ?5, ?6, 'lexical', ?7)",
    )?;
    let mut written = 0;
    for (layer, agg) in [("structure", structure), ("flow", flows)] {
        for ((to, kind), mut a) in agg {
            a.via.sort();
            let via = serde_json::to_string(&a.via).unwrap_or_else(|_| "[]".into());
            ins.execute(params![fid, to, kind, layer, a.weight, a.confidence, via])?;
            written += 1;
        }
    }
    Ok(written)
}

/// Files whose references name any of `name_ids` (their edges may change when those
/// names gain, lose or change a definer).
pub fn files_referencing(
    conn: &Connection,
    name_ids: &BTreeSet<i64>,
) -> rusqlite::Result<BTreeSet<i64>> {
    let mut st = conn.prepare_cached("SELECT DISTINCT file_id FROM refs WHERE name_id = ?1")?;
    let mut out = BTreeSet::new();
    for id in name_ids {
        for f in st.query_map([id], |r| r.get::<_, i64>(0))? {
            out.insert(f?);
        }
    }
    Ok(out)
}

/// Definer-kind name ids declared (non-locally) by a file.
pub fn defined_names(conn: &Connection, fid: i64) -> rusqlite::Result<BTreeSet<i64>> {
    let sql = format!(
        "SELECT DISTINCT name_id FROM symbols WHERE file_id = ?1 AND local = 0 AND kind IN ({DEFINER_KINDS})"
    );
    let mut st = conn.prepare_cached(&sql)?;
    let rows = st.query_map([fid], |r| r.get::<_, i64>(0))?;
    rows.collect()
}
