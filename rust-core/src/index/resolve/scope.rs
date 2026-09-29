//! Scope model shared by the Go and TS/JS resolvers: symbol rows, a file's scope
//! (its imports and local bindings), confidence tiers and the symbol queries.

use std::collections::HashMap;
use std::rc::Rc;

use rusqlite::{Connection, OptionalExtension, Row};

use super::counters::Cause;
use super::mro::Order;
use crate::index::edges::is_test_file;
use crate::index::facts::ref_kind;

/// Language family with its own scope rules.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Family {
    Go,
    Ts,
    Rust,
    Python,
    Java,
}

/// What differs between families in the binding rules the resolver shares.
pub struct Rules {
    /// The family's name in coverage reports.
    pub name: &'static str,
    /// `files.lang` values of the family (SQL list).
    pub langs: &'static str,
    /// How a member is searched across a type's bases.
    pub order: Order,
    /// Receiver names that stand for the enclosing type, and the kinds of symbol that
    /// enclose them.
    pub self_names: &'static [&'static str],
    pub self_kinds: &'static [&'static str],
    /// Receiver names that stand for the first base of the enclosing type.
    pub super_names: &'static [&'static str],
    /// A bare name inside a type may name one of its members (Java).
    pub implicit_this: bool,
    /// A name defined by one file of the family binds anywhere, at the global tier.
    pub global: bool,
    /// The `IMPORTS` edge reason of the family's import rows (Go, TS/JS read theirs
    /// from the row).
    pub import_reason: &'static str,
}

impl Family {
    pub fn of(lang: &str) -> Option<Family> {
        FAMILIES
            .iter()
            .find(|(langs, _)| langs.contains(&lang))
            .map(|(_, f)| *f)
    }

    pub fn rules(self) -> &'static Rules {
        &RULES[self as usize]
    }
}

/// `files.lang` values per family.
const FAMILIES: &[(&[&str], Family)] = &[
    (&["go"], Family::Go),
    (&["typescript", "tsx", "javascript"], Family::Ts),
    (&["rust"], Family::Rust),
    (&["python"], Family::Python),
    (&["java"], Family::Java),
];

/// Rules by family, in `Family` declaration order.
const RULES: [Rules; 5] = [
    Rules {
        name: "go",
        langs: "'go'",
        order: Order::FirstWins,
        self_names: &[],
        self_kinds: &[],
        super_names: &[],
        implicit_this: false,
        global: false,
        import_reason: "package",
    },
    Rules {
        name: "ts",
        langs: "'typescript','tsx','javascript'",
        order: Order::FirstWins,
        self_names: &["this"],
        self_kinds: &["Class"],
        super_names: &["super"],
        implicit_this: false,
        global: true,
        import_reason: "import",
    },
    Rules {
        name: "rust",
        langs: "'rust'",
        order: Order::FirstWins,
        self_names: &["self", "Self"],
        self_kinds: &["Impl", "Trait"],
        super_names: &[],
        implicit_this: false,
        global: true,
        import_reason: "use",
    },
    Rules {
        name: "python",
        langs: "'python'",
        order: Order::C3,
        self_names: &["self", "cls"],
        self_kinds: &["Class"],
        super_names: &[],
        implicit_this: false,
        global: true,
        import_reason: "py-import",
    },
    Rules {
        name: "java",
        langs: "'java'",
        order: Order::FirstWins,
        self_names: &["this"],
        self_kinds: &["Class", "Enum", "Interface"],
        super_names: &["super"],
        implicit_this: true,
        global: true,
        import_reason: "java-import",
    },
];

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

/// What a module path names in a language with module files (Rust, Python, Java).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Module {
    /// The files that hold the module (none for a namespace package or a directory
    /// without files of its own) and the qualified prefix of the module inside them
    /// (`""` when the module is the file; `Outer` for `pkg.Outer` naming a class).
    Files(Vec<i64>, String),
    /// Outside the repository: the standard library or a dependency.
    External,
    /// Inside the repository's layout, but no file holds it.
    Missing,
}

/// What a local name introduced by an import stands for.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Bind {
    /// `from m import name`, `use m::name`, `import m.Name`: an item of module `module`
    /// (or, failing that item, the submodule `module` + `name`).
    Item { module: String, name: String },
    /// `import m`, `use m::{self}`, `mod m`: the module itself.
    Module(String),
}

/// The module system of one language: how import rows bind names and how a module path
/// maps to files. Paths are spelled as the language does (`a.b`, `..x`, `crate::a::b`).
pub trait Scheme {
    /// The files a module path names, read from the file `from`.
    fn module(&mut self, db: &Db<'_>, from: &FileCtx, path: &str) -> rusqlite::Result<Module>;
    /// What `local` is bound to by the file's imports.
    fn binding(&self, imports: &[Import], local: &str) -> Option<Bind>;
    /// The import brings every exported name of its module into scope.
    fn is_wildcard(&self, imp: &Import) -> bool;
    /// The import makes the names it binds visible to the file's importers (Rust
    /// `pub use`, Python's `from .x import y` in a package).
    fn reexports(&self, imp: &Import) -> bool;
    /// The submodule `name` of module `base`.
    fn join(&self, base: &str, name: &str) -> String;
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
        matches!(self.kind.as_str(), "Interface" | "Trait")
    }
}

pub(super) const SYM_COLS: &str = "s.id, s.file_id, s.name_id, n.name, s.qualified_name, s.kind, s.container_id, s.exported, s.local";
/// Column of the file path appended after `SYM_COLS`.
const PATH_COL: usize = 9;

pub(super) fn sym(r: &Row<'_>) -> rusqlite::Result<Sym> {
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
    /// The kind the binding rules read (`import`, `reexport`, `reexport_all`,
    /// `require`, `dynamic`); see [`IMPORT_KINDS`].
    pub kind: String,
    /// The reason an IMPORTS edge from this import carries.
    pub reason: &'static str,
    pub module: String,
    pub name: Option<String>,
    pub alias: Option<String>,
    pub target_key: Option<String>,
    pub resolved: Option<i64>,
}

/// Import kinds as the extractor stores them: the kind the binding rules read, and the
/// reason of the IMPORTS edge. Type-only, in-function and dynamic imports bind names
/// like their eager forms but never force the imported module's initialization, so
/// the import-cycle check (PAR-IMP-08) skips edges with those reasons.
pub const IMPORT_KINDS: &[(&str, &str, &str)] = &[
    ("import", "import", "import"),
    ("import_type", "import", "type-import"),
    ("reexport", "reexport", "re-export"),
    ("reexport_type", "reexport", "type-re-export"),
    ("reexport_all", "reexport_all", "re-export"),
    ("require", "require", "require"),
    ("require_local", "require", "lazy-require"),
    ("dynamic", "dynamic", "dynamic-import"),
];

/// IMPORTS edge reasons that run the imported module when the importer loads (Go's
/// `package` imports included): the edges an initialization cycle is made of.
pub const FORCING_IMPORT_REASONS: &[&str] = &["import", "re-export", "require", "package"];

fn import_kind(stored: &str) -> (String, &'static str) {
    IMPORT_KINDS
        .iter()
        .find(|(k, _, _)| *k == stored)
        .map_or((stored.to_string(), "import"), |(_, kind, reason)| {
            (kind.to_string(), *reason)
        })
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

    /// The first non-local symbol declared at the file's top level as `name`.
    pub fn top_level(&self, name: &str) -> Option<&Sym> {
        self.symbols
            .values()
            .filter(|s| !s.local && s.qname == name)
            .min_by_key(|s| s.id)
    }

    /// Enclosing symbols of one of `kinds`, innermost first.
    pub fn classes<'a>(
        &'a self,
        at: Option<i64>,
        kinds: &'a [&str],
    ) -> impl Iterator<Item = &'a Sym> + 'a {
        self.chain(at)
            .flatten()
            .filter_map(|id| self.symbols.get(&id))
            .filter(|s| kinds.contains(&s.kind.as_str()))
    }

    /// Nearest enclosing symbol of one of `kinds`, for `this`/`self`/`super`.
    pub fn class<'a>(&'a self, at: Option<i64>, kinds: &'a [&str]) -> Option<&'a Sym> {
        self.classes(at, kinds).next()
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
                let (kind, reason) = import_kind(&r.get::<_, String>(0)?);
                Ok(Import {
                    kind,
                    reason,
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
                // a file holds few symbols; a common name (`new`) has thousands
                "SELECT {SYM_COLS} FROM symbols s INDEXED BY symbols_file
                 JOIN names n ON n.id = s.name_id
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
        self.in_lang_dir("go", dir, name, qname)
    }

    /// `in_go_dir` for the files of any one language.
    pub fn in_lang_dir(
        &self,
        lang: &str,
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
                 WHERE n.name = ?1 AND s.qualified_name = ?2 AND s.local = 0 AND f.lang = ?5
                   AND f.path >= ?3 AND f.path < ?4
                 ORDER BY f.path, s.ord"
            ))?
            .query_map(rusqlite::params![name, qname, lo, hi, lang], |r| {
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

    /// Files of the family defining `name` at top level, in path order, at most
    /// `limit` (two decide uniqueness however many rows one file holds).
    pub fn top_level_files(
        &self,
        family: Family,
        name: &str,
        limit: usize,
    ) -> rusqlite::Result<Vec<i64>> {
        let sql = format!(
            "SELECT DISTINCT s.file_id FROM symbols s JOIN names n ON n.id = s.name_id
             JOIN files f ON f.id = s.file_id
             WHERE n.name = ?1 AND s.qualified_name = ?1 AND s.local = 0
               AND f.lang IN ({}) ORDER BY f.path LIMIT ?2",
            family.rules().langs
        );
        self.conn
            .prepare_cached(&sql)?
            .query_map(rusqlite::params![name, limit as i64], |r| r.get(0))?
            .collect()
    }

    /// The file uses ES export syntax: an exported declaration, an export specifier
    /// or a re-export.
    pub fn has_es_exports(&self, fid: i64) -> rusqlite::Result<bool> {
        self.conn
            .prepare_cached(
                "SELECT EXISTS (SELECT 1 FROM symbols WHERE file_id = ?1 AND exported = 1)
                     OR EXISTS (SELECT 1 FROM refs WHERE file_id = ?1 AND kind = ?2)
                     OR EXISTS (SELECT 1 FROM imports WHERE file_id = ?1
                                AND kind IN ('reexport', 'reexport_type', 'reexport_all'))",
            )?
            .query_row(rusqlite::params![fid, ref_kind::EXPORT], |r| r.get(0))
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
        self.files_in("go", dir)
    }

    /// Paths of the `lang` files directly inside `dir`, with their ids.
    pub fn files_in(&self, lang: &str, dir: &str) -> rusqlite::Result<Vec<(i64, String)>> {
        let (lo, hi) = dir_range(dir);
        let rows = self
            .conn
            .prepare_cached(
                "SELECT id, path FROM files WHERE lang = ?3 AND path >= ?1 AND path < ?2
                 ORDER BY path",
            )?
            .query_map(rusqlite::params![lo, hi, lang], |r| {
                Ok((r.get(0)?, r.get(1)?))
            })?
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

/// Hierarchy and module-layout queries of the Rust, Python and Java resolvers.
impl Db<'_> {
    /// The id of the file at exactly `path`.
    pub fn file_at(&self, path: &str) -> rusqlite::Result<Option<i64>> {
        self.conn
            .prepare_cached("SELECT id FROM files WHERE path = ?1")?
            .query_row([path], |r| r.get(0))
            .optional()
    }

    /// The `lang` file whose path is `tail` or ends with `/tail` (a source root, such as
    /// Java's `src/main/java`, is not part of a class's package path).
    pub fn file_ending(&self, lang: &str, tail: &str) -> rusqlite::Result<Option<i64>> {
        self.conn
            .prepare_cached(
                "SELECT id FROM files WHERE lang = ?1
                 AND (path = ?2 OR substr(path, -length(?2) - 1) = '/' || ?2) ORDER BY path LIMIT 1",
            )?
            .query_row(rusqlite::params![lang, tail], |r| r.get(0))
            .optional()
    }

    /// The `lang` files directly inside a directory whose path is `tail` or ends with
    /// `/tail` (a package's directory below its source root).
    pub fn files_in_tail(&self, lang: &str, tail: &str) -> rusqlite::Result<Vec<(i64, String)>> {
        let rows = self
            .conn
            .prepare_cached(
                "SELECT id, path FROM files WHERE lang = ?1
                 AND (path LIKE ?2 || '/%' OR path LIKE '%/' || ?2 || '/%') ORDER BY path",
            )?
            .query_map(rusqlite::params![lang, tail], |r| {
                Ok((r.get(0)?, r.get(1)?))
            })?
            .collect::<rusqlite::Result<Vec<(i64, String)>>>()?;
        let direct = |p: &str| {
            let dir = p.rsplit_once('/').map_or("", |(d, _)| d);
            dir == tail || dir.strip_suffix(tail).is_some_and(|d| d.ends_with('/'))
        };
        Ok(rows.into_iter().filter(|(_, p)| direct(p)).collect())
    }

    /// Base-type hints of a type as written (`B`, or `q.B` through a qualifier), with
    /// the file whose scope they are read in: its `extends`/`implements` references
    /// and, for a Rust type, the traits of the `impl Trait for T` blocks under `src`.
    pub fn base_hints(
        &self,
        ty: &Sym,
        rust_src: Option<&str>,
    ) -> rusqlite::Result<Vec<(String, i64)>> {
        const HINT: &str = "CASE WHEN q.name IS NULL THEN n.name ELSE q.name || '.' || n.name END";
        let own = format!(
            "SELECT {HINT}, r.file_id FROM refs r JOIN names n ON n.id = r.name_id
             LEFT JOIN names q ON q.id = r.qual_id
             WHERE r.file_id = ?1 AND r.symbol_id = ?2 AND r.kind IN (?3, ?4) ORDER BY r.start_byte"
        );
        let mut out = self
            .conn
            .prepare_cached(&own)?
            .query_map(
                rusqlite::params![ty.file, ty.id, ref_kind::EXTENDS, ref_kind::IMPLEMENTS],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?
            .collect::<rusqlite::Result<Vec<(String, i64)>>>()?;
        if let Some(src) = rust_src {
            let (lo, hi) = dir_range(src);
            let impls = format!(
                "SELECT {HINT}, r.file_id FROM symbols s JOIN files f ON f.id = s.file_id
                 JOIN refs r ON r.file_id = s.file_id AND r.start_byte >= s.start_byte
                              AND r.start_byte < s.end_byte AND r.symbol_id = s.id
                 JOIN names n ON n.id = r.name_id LEFT JOIN names q ON q.id = r.qual_id
                 WHERE s.kind = 'Impl' AND s.name_id = ?1 AND r.kind = ?2
                   AND f.path >= ?3 AND f.path < ?4 ORDER BY f.path, r.start_byte"
            );
            let rows = self
                .conn
                .prepare_cached(&impls)?
                .query_map(
                    rusqlite::params![ty.name_id, ref_kind::IMPLEMENTS, lo, hi],
                    |r| Ok((r.get(0)?, r.get(1)?)),
                )?
                .collect::<rusqlite::Result<Vec<(String, i64)>>>()?;
            out.extend(rows);
        }
        Ok(out)
    }

    /// Methods `name` of the Rust type `ty` declared by impl blocks in files under
    /// `src` other than `except`, at most `limit`.
    pub fn impl_methods(
        &self,
        src: &str,
        ty: &str,
        name: &str,
        except: i64,
        limit: usize,
    ) -> rusqlite::Result<Vec<Sym>> {
        let (lo, hi) = dir_range(src);
        self.conn
            .prepare_cached(&format!(
                "SELECT {SYM_COLS} FROM symbols s JOIN names n ON n.id = s.name_id
                 JOIN files f ON f.id = s.file_id
                 WHERE n.name = ?1 AND s.qualified_name = ?2 || '.' || ?1 AND s.local = 0
                   AND f.lang = 'rust' AND f.path >= ?3 AND f.path < ?4 AND s.file_id != ?5
                 ORDER BY f.path, s.ord LIMIT ?6"
            ))?
            .query_map(
                rusqlite::params![name, ty, lo, hi, except, limit as i64],
                sym,
            )?
            .collect()
    }
}
