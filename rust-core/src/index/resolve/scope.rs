//! Scope model shared by the Go and TS/JS resolvers: symbol rows, a file's scope
//! (its imports and local bindings), confidence tiers and the symbol queries.

use std::collections::HashMap;
use std::rc::Rc;

use rusqlite::{Connection, OptionalExtension, Row};

use super::counters::Cause;
use crate::index::edges::is_test_file;
use crate::index::facts::ref_kind;

/// Language family with its own scope rules.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Family {
    Go,
    Ts,
}

impl Family {
    pub fn of(lang: &str) -> Option<Family> {
        match lang {
            "go" => Some(Family::Go),
            "typescript" | "tsx" | "javascript" => Some(Family::Ts),
            _ => None,
        }
    }

    /// `files.lang` values in this family (SQL list).
    fn langs(self) -> &'static str {
        match self {
            Family::Go => "'go'",
            Family::Ts => "'typescript','tsx','javascript'",
        }
    }
}

/// Confidence tier of a resolved edge (PAR-IMP-01).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Tier {
    /// Only a unique name match anywhere in the language family.
    Global,
    /// Through an import, a package qualifier or the same Go package.
    ImportScoped,
    /// Declared in the referencing file.
    SameFile,
}

impl Tier {
    pub fn confidence(self) -> f64 {
        match self {
            Tier::SameFile => 0.95,
            Tier::ImportScoped => 0.9,
            Tier::Global => 0.5,
        }
    }

    /// A global-tier edge is a name match, not a scope proof.
    pub fn provenance(self) -> &'static str {
        match self {
            Tier::Global => "lexical",
            _ => "scope",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Tier::SameFile => "same_file",
            Tier::ImportScoped => "import_scoped",
            Tier::Global => "global",
        }
    }
}

/// A non-local symbol row.
#[derive(Debug, Clone)]
pub struct Sym {
    pub id: i64,
    pub file: i64,
    pub name_id: i64,
    pub name: String,
    pub qname: String,
    pub kind: String,
    pub container: Option<i64>,
    pub exported: bool,
    pub local: bool,
}

impl Sym {
    pub fn is_callable(&self) -> bool {
        matches!(self.kind.as_str(), "Function" | "Method" | "Constructor")
    }

    pub fn is_interface(&self) -> bool {
        self.kind == "Interface"
    }
}

const SYM_COLS: &str = "s.id, s.file_id, s.name_id, n.name, s.qualified_name, s.kind, s.container_id, s.exported, s.local";
/// Column of the file path appended after `SYM_COLS`.
const PATH_COL: usize = 9;

fn sym(r: &Row<'_>) -> rusqlite::Result<Sym> {
    Ok(Sym {
        id: r.get(0)?,
        file: r.get(1)?,
        name_id: r.get(2)?,
        name: r.get(3)?,
        qname: r.get(4)?,
        kind: r.get(5)?,
        container: r.get(6)?,
        exported: r.get(7)?,
        local: r.get(8)?,
    })
}

/// Outcome of binding a name.
#[derive(Debug, Clone)]
pub enum Found {
    Hit(Sym, Tier, &'static str),
    /// A module object (namespace import, `require`): no symbol to point at.
    Module,
    Drop(Cause),
}

#[derive(Debug, Clone)]
pub struct Import {
    pub kind: String,
    pub module: String,
    pub name: Option<String>,
    pub alias: Option<String>,
    pub target_key: Option<String>,
    pub resolved: Option<i64>,
}

/// One reference row of the file being resolved (names shared per file).
#[derive(Debug, Clone)]
pub struct Ref {
    pub name_id: i64,
    pub name: Rc<str>,
    pub qual: Option<Rc<str>>,
    pub kind: u8,
    pub symbol: Option<i64>,
}

/// A file's reference rows of the given kinds, in source order; bound to the file id.
pub fn refs_sql(kinds: &[u8]) -> String {
    let list = kinds
        .iter()
        .map(u8::to_string)
        .collect::<Vec<_>>()
        .join(",");
    format!(
        "SELECT r.name_id, n.name, r.qual_id, q.name, r.kind, r.symbol_id FROM refs r
         JOIN names n ON n.id = r.name_id LEFT JOIN names q ON q.id = r.qual_id
         WHERE r.file_id = ?1 AND r.kind IN ({list}) ORDER BY r.start_byte"
    )
}

/// Name strings of one file's references, shared between its `Ref`s.
#[derive(Default)]
pub struct Names(HashMap<i64, Rc<str>>);

impl Names {
    fn get(&mut self, id: Option<i64>, text: Option<String>) -> Option<Rc<str>> {
        let (id, text) = (id?, text?);
        Some(self.0.entry(id).or_insert_with(|| text.into()).clone())
    }

    /// One row of `refs_sql`.
    pub fn read(&mut self, r: &Row<'_>) -> rusqlite::Result<Ref> {
        let name_id: i64 = r.get(0)?;
        Ok(Ref {
            name_id,
            name: self
                .get(Some(name_id), r.get(1)?)
                .unwrap_or_else(|| "".into()),
            qual: self.get(r.get(2)?, r.get(3)?),
            kind: r.get(4)?,
            symbol: r.get(5)?,
        })
    }
}

/// What any file contributes to name lookup: its identity and imports.
#[derive(Debug, Clone)]
pub struct FileCtx {
    pub id: i64,
    pub path: String,
    pub family: Family,
    pub imports: Vec<Import>,
}

impl FileCtx {
    pub fn dir(&self) -> &str {
        self.path.rsplit_once('/').map_or("", |(d, _)| d)
    }

    pub fn is_test(&self) -> bool {
        is_test_file(&self.path)
    }
}

/// The file being resolved: its own symbols and the local bindings of each scope.
pub struct Local {
    pub symbols: HashMap<i64, Sym>,
    /// name → (enclosing symbol, type hint) of each binding of that name.
    binds: HashMap<Rc<str>, Vec<Binding>>,
}

/// A local binding: its enclosing symbol and type hint.
type Binding = (Option<i64>, Option<Rc<str>>);

impl Local {
    /// `binds`: the file's binding references (`ref_kind::BIND`).
    pub fn new(symbols: Vec<Sym>, binds_refs: Vec<Ref>) -> Local {
        let mut binds: HashMap<Rc<str>, Vec<Binding>> = HashMap::new();
        for r in binds_refs {
            binds.entry(r.name).or_default().push((r.symbol, r.qual));
        }
        Local {
            symbols: symbols.into_iter().map(|s| (s.id, s)).collect(),
            binds,
        }
    }

    /// Enclosing symbols from `at` outwards (the file scope, `None`, last).
    fn chain(&self, at: Option<i64>) -> impl Iterator<Item = Option<i64>> + '_ {
        let mut cur = Some(at);
        std::iter::from_fn(move || {
            let here = cur?;
            cur = here.map(|id| self.symbols.get(&id).and_then(|s| s.container));
            Some(here)
        })
    }

    /// The local binding `name` visible at `at`, with its type hint.
    pub fn binding(&self, at: Option<i64>, name: &str) -> Option<Option<Rc<str>>> {
        let binds = self.binds.get(name)?;
        self.chain(at)
            .find_map(|s| binds.iter().find(|(b, _)| *b == s))
            .map(|(_, hint)| hint.clone())
    }

    /// Nearest enclosing symbol visible outside its body: the edge source.
    pub fn source(&self, at: Option<i64>) -> Option<i64> {
        self.chain(at)
            .flatten()
            .find(|id| self.symbols.get(id).is_some_and(|s| !s.local))
    }

    /// Nearest enclosing class, for `this`/`super`.
    pub fn class(&self, at: Option<i64>) -> Option<&Sym> {
        self.chain(at)
            .flatten()
            .filter_map(|id| self.symbols.get(&id))
            .find(|s| s.kind == "Class")
    }
}

/// Symbol and scope queries over `index.db`.
pub struct Db<'c> {
    pub conn: &'c Connection,
}

impl Db<'_> {
    pub fn file_ctx(&self, fid: i64) -> rusqlite::Result<Option<(FileCtx, String)>> {
        let head = self
            .conn
            .prepare_cached("SELECT path, lang, parse_status FROM files WHERE id = ?1")?
            .query_row([fid], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                ))
            })
            .optional()?;
        let Some((path, lang, status)) = head else {
            return Ok(None);
        };
        let Some(family) = Family::of(&lang) else {
            return Ok(None);
        };
        let imports = self
            .conn
            .prepare_cached(
                "SELECT kind, module, name, alias, target_key, resolved_file_id FROM imports
                 WHERE file_id = ?1 ORDER BY ord",
            )?
            .query_map([fid], |r| {
                Ok(Import {
                    kind: r.get(0)?,
                    module: r.get(1)?,
                    name: r.get(2)?,
                    alias: r.get(3)?,
                    target_key: r.get(4)?,
                    resolved: r.get(5)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(Some((
            FileCtx {
                id: fid,
                path,
                family,
                imports,
            },
            status,
        )))
    }

    pub fn file_symbols(&self, fid: i64) -> rusqlite::Result<Vec<Sym>> {
        self.conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS} FROM symbols s JOIN names n ON n.id = s.name_id
                 WHERE s.file_id = ?1 ORDER BY s.ord"
            ))?
            .query_map([fid], sym)?
            .collect()
    }

    /// A file's references of the given kinds, in source order (read eagerly: for the
    /// few binding references).
    pub fn file_refs(
        &self,
        fid: i64,
        kinds: &[u8],
        names: &mut Names,
    ) -> rusqlite::Result<Vec<Ref>> {
        let mut st = self.conn.prepare_cached(&refs_sql(kinds))?;
        let mut rows = st.query([fid])?;
        let mut out = Vec::new();
        while let Some(r) = rows.next()? {
            out.push(names.read(r)?);
        }
        Ok(out)
    }

    pub fn name_id(&self, name: &str) -> rusqlite::Result<Option<i64>> {
        self.conn
            .prepare_cached("SELECT id FROM names WHERE name = ?1")?
            .query_row([name], |r| r.get(0))
            .optional()
    }

    pub fn symbol(&self, id: i64) -> rusqlite::Result<Option<Sym>> {
        self.conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS} FROM symbols s JOIN names n ON n.id = s.name_id WHERE s.id = ?1"
            ))?
            .query_row([id], sym)
            .optional()
    }

    /// Non-local symbols of one file with this qualified name, in declaration order.
    pub fn in_file(&self, fid: i64, name: &str, qname: &str) -> rusqlite::Result<Vec<Sym>> {
        self.conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS} FROM symbols s JOIN names n ON n.id = s.name_id
                 WHERE s.file_id = ?1 AND n.name = ?2 AND s.qualified_name = ?3 AND s.local = 0
                 ORDER BY s.ord"
            ))?
            .query_map(rusqlite::params![fid, name, qname], sym)?
            .collect()
    }

    /// Non-local symbols with this qualified name in the Go files directly inside
    /// `dir`, with each file's path.
    pub fn in_go_dir(
        &self,
        dir: &str,
        name: &str,
        qname: &str,
    ) -> rusqlite::Result<Vec<(Sym, String)>> {
        let (lo, hi) = dir_range(dir);
        let rows = self
            .conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS}, f.path FROM symbols s JOIN names n ON n.id = s.name_id
                 JOIN files f ON f.id = s.file_id
                 WHERE n.name = ?1 AND s.qualified_name = ?2 AND s.local = 0 AND f.lang = 'go'
                   AND f.path >= ?3 AND f.path < ?4
                 ORDER BY f.path, s.ord"
            ))?
            .query_map(rusqlite::params![name, qname, lo, hi], |r| {
                Ok((sym(r)?, r.get::<_, String>(PATH_COL)?))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows.into_iter().filter(|(_, p)| in_dir(p, dir)).collect())
    }

    /// Go methods declared on `ty` (qualified `ty.M`) in the files directly inside `dir`
    /// other than `except`: receivers declared away from their type.
    pub fn go_methods_elsewhere(
        &self,
        dir: &str,
        ty: &str,
        except: i64,
    ) -> rusqlite::Result<Vec<Sym>> {
        let (lo, hi) = dir_range(dir);
        let rows = self
            .conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS}, f.path FROM symbols s JOIN names n ON n.id = s.name_id
                 JOIN files f ON f.id = s.file_id
                 WHERE s.kind = 'Method' AND s.local = 0 AND s.qualified_name = ?1 || '.' || n.name
                   AND f.lang = 'go' AND f.path >= ?2 AND f.path < ?3 AND s.file_id != ?4
                 ORDER BY f.path, s.ord"
            ))?
            .query_map(rusqlite::params![ty, lo, hi, except], |r| {
                Ok((sym(r)?, r.get::<_, String>(PATH_COL)?))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows
            .into_iter()
            .filter(|(_, p)| in_dir(p, dir))
            .map(|(s, _)| s)
            .collect())
    }

    /// Top-level definitions of `name` in the family's files, up to `limit`, with
    /// each file's path.
    pub fn top_level(
        &self,
        family: Family,
        name: &str,
        limit: usize,
    ) -> rusqlite::Result<Vec<(Sym, String)>> {
        let sql = format!(
            "SELECT {SYM_COLS}, f.path FROM symbols s JOIN names n ON n.id = s.name_id
             JOIN files f ON f.id = s.file_id
             WHERE n.name = ?1 AND s.qualified_name = ?1 AND s.local = 0
               AND f.lang IN ({}) ORDER BY f.path, s.ord LIMIT ?2",
            family.langs()
        );
        self.conn
            .prepare_cached(&sql)?
            .query_map(rusqlite::params![name, limit as i64], |r| {
                Ok((sym(r)?, r.get::<_, String>(PATH_COL)?))
            })?
            .collect()
    }

    /// Base-type names a type declares (`extends`/`implements` references it encloses).
    pub fn bases(&self, ty: &Sym) -> rusqlite::Result<Vec<String>> {
        self.conn
            .prepare_cached(
                "SELECT n.name FROM refs r JOIN names n ON n.id = r.name_id
                 WHERE r.file_id = ?1 AND r.symbol_id = ?2 AND r.kind IN (?3, ?4)
                 ORDER BY r.start_byte",
            )?
            .query_map(
                rusqlite::params![ty.file, ty.id, ref_kind::EXTENDS, ref_kind::IMPLEMENTS],
                |r| r.get(0),
            )?
            .collect()
    }

    /// Paths of the Go files directly inside `dir`, with their ids.
    pub fn go_files_in(&self, dir: &str) -> rusqlite::Result<Vec<(i64, String)>> {
        let (lo, hi) = dir_range(dir);
        let rows = self
            .conn
            .prepare_cached(
                "SELECT id, path FROM files WHERE lang = 'go' AND path >= ?1 AND path < ?2
                 ORDER BY path",
            )?
            .query_map(rusqlite::params![lo, hi], |r| Ok((r.get(0)?, r.get(1)?)))?
            .collect::<rusqlite::Result<Vec<(i64, String)>>>()?;
        Ok(rows.into_iter().filter(|(_, p)| in_dir(p, dir)).collect())
    }
}

/// Path bounds of the files under `dir` ('0' follows '/').
fn dir_range(dir: &str) -> (String, String) {
    if dir.is_empty() {
        (String::new(), "\u{10FFFF}".to_string())
    } else {
        (format!("{dir}/"), format!("{dir}0"))
    }
}

/// `path` is a file directly inside `dir`.
pub fn in_dir(path: &str, dir: &str) -> bool {
    path.rsplit_once('/').map_or("", |(d, _)| d) == dir
}
