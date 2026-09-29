//! Scope and import resolver for Go, TS/JS, Rust, Python and Java (WS-17 and WS-39,
//! PAR-IMP-01, PAR-IMP-05).
//!
//! For each file of those languages it binds the file's references to symbols and writes typed
//! symbol edges (`edges.layer = 'symbol'`): CALLS, USES, ACCESSES (read/write), EXTENDS,
//! IMPLEMENTS, IMPORTS, HAS_METHOD and HAS_PROPERTY. Each edge carries a confidence
//! tier (same-file 0.95, import-scoped 0.9, global 0.5), a provenance (`scope` for
//! scope-proven bindings, `lexical` for the global name-only fallback) and a reason.
//! The lexical file edges (`structure`/`flow` layers) stay as the fallback for every
//! language.
//!
//! Binding order for a plain name: local binding (no edge), same file, then the
//! language's import scope (TS named/default imports and re-export chains; the Go
//! package's other files and dot imports; for Rust, Python and Java the names their
//! `use`/`import` rows bind, wildcard imports, re-exports and Java's own package), then
//! a unique top-level definition anywhere in the family (global tier, every family but
//! Go). A member access (`q.name`) is
//! typed through its qualifier: `this`/`super`, a local whose declaration names or
//! constructs its type (receivers, typed parameters, `new T()`, `&T{}`, `NewT()`), a
//! namespace import or Go package qualifier, or a declared type (static access).
//! Members are searched along the type's method resolution order (`mro`): first-wins
//! depth-first for Go, TS/JS, Rust (own impls, then implemented traits) and Java (class
//! chain, then interfaces), C3 for Python. `self`/`this` name the enclosing type (a Rust
//! `impl` block stands for its type); a Java bare name may be a member of an enclosing
//! class.
//!
//! What cannot be bound exactly is counted per file, name and cause in
//! `resolve_drops` (see `counters`): the epistemic envelope of impact queries.
//!
//! Names inside comments and string literals never reach the resolver: extraction
//! records no references for them.
//!
//! This is a reimplementation from public design descriptions of scope-resolving code
//! graphs; no third-party resolver code is used.

pub mod counters;
pub mod emit;
pub mod imports_go;
pub mod imports_java;
pub mod imports_python;
pub mod imports_rust;
pub mod imports_ts;
pub mod mro;
pub mod scope;

use std::collections::{BTreeSet, HashMap};
use std::path::Path;
use std::rc::Rc;

use rusqlite::{Connection, params};

use counters::{Cause, FileDrops, ResolveCounters};
use emit::{Edges, Target, edge_kind, member_kind};
use imports_go::{Modules, Qualifier};
use imports_ts::Binding;
use scope::{Bind, Db, Family, FileCtx, Found, Local, Module, Names, Ref, Scheme, Sym, Tier};

use super::edges::{STOPWORD_SYMBOLS, is_test_file};
use super::facts::ref_kind;
use super::meta;

/// Bump when resolution output changes for the same facts. Part of the analyzer
/// version, so a change forces a full rebuild.
pub const RESOLVER_REVISION: u32 = 3;

/// Global-tier names shorter than this are too common to bind by name alone.
const MIN_GLOBAL_NAME: usize = 4;
/// Global lookups cached per run before the cache is cleared (bounded transient).
const GLOBAL_CACHE_MAX: usize = 4096;
/// Base-type levels searched for a member, and types in one hierarchy.
const MAX_INHERITANCE_DEPTH: usize = 4;
const MAX_HIERARCHY: usize = 32;
/// Re-export chains followed at most this deep (Rust `pub use`, Python `__init__`).
const MAX_REEXPORT_DEPTH: usize = 6;
/// Hierarchies cached per run before the cache is cleared.
/// Same-named impl methods of other types read before those of the type are picked out.
const IMPL_CANDIDATES: usize = 16;
const HIERARCHY_CACHE_MAX: usize = 256;
/// Files of one imported Go package that get a file-level IMPORTS edge.
const MAX_PACKAGE_IMPORT_EDGES: usize = 64;

/// Reference kinds that can bind to a symbol (bindings, import and export names and
/// lexical-scanner words cannot).
const RESOLVED_KINDS: &[u8] = &[
    ref_kind::CALL,
    ref_kind::MEMBER_CALL,
    ref_kind::READ,
    ref_kind::MEMBER,
    ref_kind::TYPE,
    ref_kind::WRITE,
    ref_kind::EXTENDS,
    ref_kind::IMPLEMENTS,
];

/// Languages the resolver owns (`files.lang`).
pub fn resolves(lang: &str) -> bool {
    Family::of(lang).is_some()
}

fn is_type(s: &Sym) -> bool {
    matches!(
        s.kind.as_str(),
        "Class" | "Struct" | "Interface" | "Enum" | "TypeAlias" | "Namespace" | "Trait" | "Impl"
    )
}

fn retag(found: Found, reason: &'static str) -> Found {
    match found {
        Found::Hit(s, tier, _) => Found::Hit(s, tier, reason),
        other => other,
    }
}

/// Per-file state of one resolution.
struct FileRun {
    ctx: FileCtx,
    local: Local,
    edges: Edges,
    drops: FileDrops,
    /// Plain-name lookups from this file's scope.
    lookups: HashMap<String, Found>,
    /// (type id, member name) → member.
    members: HashMap<(i64, String), Option<Member>>,
}

/// A member found on a type, and whether the search was ambiguous (overloads, an
/// inconsistent C3 order).
#[derive(Clone)]
struct Member {
    sym: Sym,
    ambiguous: bool,
}

/// A type's search order: itself, then its bases as the family's rules order them.
struct Hierarchy {
    order: Vec<Sym>,
    consistent: bool,
}

/// The module systems of the families that have module files.
type Schemes = HashMap<Family, Box<dyn Scheme>>;

pub struct Resolver<'c> {
    db: Db<'c>,
    modules: Modules,
    schemes: Schemes,
    globals: HashMap<(Family, String), Global>,
    hierarchies: HashMap<i64, Rc<Hierarchy>>,
    pub counters: ResolveCounters,
}

impl<'c> Resolver<'c> {
    pub fn new(conn: &'c Connection, root: &Path) -> Resolver<'c> {
        Resolver {
            db: Db { conn },
            modules: Modules::new(root),
            schemes: HashMap::from([
                (
                    Family::Rust,
                    Box::new(imports_rust::Rust::new(root)) as Box<dyn Scheme>,
                ),
                (Family::Python, Box::new(imports_python::Python::default())),
                (Family::Java, Box::new(imports_java::Java::default())),
            ]),
            globals: HashMap::new(),
            hierarchies: HashMap::new(),
            counters: ResolveCounters::default(),
        }
    }

    /// Recompute file `fid`'s symbol edges and drops. Returns edges written (0 for
    /// languages the resolver does not own and for files without facts).
    pub fn resolve_file(&mut self, fid: i64) -> rusqlite::Result<usize> {
        let conn = self.db.conn;
        conn.prepare_cached("DELETE FROM edges WHERE src_file = ?1 AND layer = 'symbol'")?
            .execute([fid])?;
        conn.prepare_cached("DELETE FROM resolve_drops WHERE file_id = ?1")?
            .execute([fid])?;
        let Some((ctx, status)) = self.db.file_ctx(fid)? else {
            return Ok(0);
        };
        if !meta::INDEXED_STATUSES.contains(&status.as_str()) {
            return Ok(0);
        }
        let mut names = Names::default();
        let binds = self.db.file_refs(fid, &[ref_kind::BIND], &mut names)?;
        let mut f = FileRun {
            ctx,
            local: Local::new(self.db.file_symbols(fid)?, binds),
            edges: Edges::default(),
            drops: FileDrops::default(),
            lookups: HashMap::new(),
            members: HashMap::new(),
        };
        self.declarations(&mut f)?;
        self.imports(&mut f)?;
        // streamed: a file's references are never all in memory at once
        let mut st = conn.prepare_cached(&scope::refs_sql(RESOLVED_KINDS))?;
        let mut rows = st.query([fid])?;
        while let Some(row) = rows.next()? {
            let r = names.read(row)?;
            self.reference(&mut f, &r)?;
        }
        drop(rows);
        drop(st);
        let family = f.ctx.family.rules().name;
        let n = f.edges.write(conn, fid, &mut self.counters)?;
        f.drops.write(conn, fid, &mut self.counters, family)?;
        let language = self.counters.languages.entry(family).or_default();
        language.files += 1;
        language.edges += n;
        self.counters.files += 1;
        Ok(n)
    }

    // ---- declarations: HAS_METHOD / HAS_PROPERTY, Go interface boundaries ----

    fn declarations(&mut self, f: &mut FileRun) -> rusqlite::Result<()> {
        let mut ids: Vec<i64> = f.local.symbols.keys().copied().collect();
        ids.sort_unstable();
        for id in ids {
            let s = &f.local.symbols[&id].clone();
            if let Some(c) = s.container.and_then(|c| f.local.symbols.get(&c)).cloned()
                && is_type(&c)
                && let Some(kind) = member_kind(s)
            {
                // an `impl` block's methods belong to the type it names
                let owner = self.impl_type(f, &c)?.map_or(c.id, |t| t.id);
                let t = Target {
                    file: s.file,
                    symbol: Some(s.id),
                    tier: Tier::SameFile,
                    reason: "declaration",
                    via: &s.name,
                };
                f.edges.add(Some(owner), kind, None, t);
            }
            if f.ctx.family != Family::Go || s.container.is_some() || s.qname != s.name {
                continue;
            }
            if s.is_interface() {
                f.drops.note(s.name_id, Cause::UndecidedInterface);
            }
            if is_type(s) {
                // methods declared on this type in the package's other files
                for m in self.db.go_methods_elsewhere(f.ctx.dir(), &s.name, s.file)? {
                    let t = Target {
                        file: m.file,
                        symbol: Some(m.id),
                        tier: Tier::ImportScoped,
                        reason: "same-package",
                        via: &m.name,
                    };
                    f.edges.add(Some(s.id), "HAS_METHOD", None, t);
                }
            }
        }
        Ok(())
    }

    // ---- import rows: IMPORTS edges ----

    fn imports(&mut self, f: &mut FileRun) -> rusqlite::Result<()> {
        let imports = std::mem::take(&mut f.ctx.imports);
        for imp in &imports {
            match f.ctx.family {
                Family::Ts => self.ts_import(f, imp)?,
                Family::Rust | Family::Python | Family::Java => self.module_import(f, imp)?,
                Family::Go => {
                    let Some(dir) = self.modules.package_dir(self.db.conn, &imp.module)? else {
                        continue; // standard library or a dependency
                    };
                    let files = self.db.go_files_in(&dir)?;
                    let targets = files
                        .iter()
                        .filter(|(_, p)| !is_test_file(p))
                        .take(MAX_PACKAGE_IMPORT_EDGES);
                    for (id, _) in targets {
                        f.edges.add(
                            None,
                            "IMPORTS",
                            None,
                            Target {
                                file: *id,
                                symbol: None,
                                tier: Tier::ImportScoped,
                                reason: "package",
                                via: &imp.module,
                            },
                        );
                    }
                }
            }
        }
        f.ctx.imports = imports;
        Ok(())
    }

    fn ts_import(&mut self, f: &mut FileRun, imp: &scope::Import) -> rusqlite::Result<()> {
        // the export this row names: `import { a }`, `export { a } from`, a default
        // import by its local name; None for namespace, side-effect and `export *`.
        let named = match (imp.kind.as_str(), imp.name.as_deref()) {
            ("import", Some("default")) => imp.alias.as_deref(),
            ("import" | "reexport", Some(n)) if n != "*" => Some(n),
            _ => None,
        };
        let Some(target) = imp.resolved else {
            if imp.target_key.is_some()
                && let Some(n) = named
                && let Some(id) = self.db.name_id(n)?
            {
                f.drops.note(id, Cause::Unresolved);
            }
            return Ok(());
        };
        let found = match named {
            Some(n) => imports_ts::find_export(&self.db, target, n)?,
            None => None,
        };
        if found.is_none()
            && let Some(n) = named
            && let Some(id) = self.db.name_id(n)?
        {
            f.drops.note(id, Cause::Unresolved);
        }
        // an import bound through a re-export chain forces the chain's modules too
        let reason = match (&found, imp.reason) {
            (Some((_, true)), "import") => "re-export",
            (_, reason) => reason,
        };
        f.edges.add(
            None,
            "IMPORTS",
            None,
            Target {
                file: found.as_ref().map_or(target, |(s, _)| s.file),
                symbol: found.as_ref().map(|(s, _)| s.id),
                tier: Tier::ImportScoped,
                reason,
                via: &imp.module,
            },
        );
        Ok(())
    }

    // ---- module files: Rust, Python, Java ----

    /// The files a module path names, read from `from`.
    fn module_of(&mut self, from: &FileCtx, path: &str) -> rusqlite::Result<Module> {
        let db = Db { conn: self.db.conn };
        match self.schemes.get_mut(&from.family) {
            Some(scheme) => scheme.module(&db, from, path),
            None => Ok(Module::External),
        }
    }

    fn scheme(&self, family: Family) -> Option<&dyn Scheme> {
        self.schemes.get(&family).map(|s| s.as_ref())
    }

    /// The top-level (or `prefix`-nested) symbol `name` exported by `files`: declared in
    /// one of them, else re-exported by an import or a wildcard import of theirs.
    fn export_of(
        &mut self,
        family: Family,
        files: &[i64],
        prefix: &str,
        name: &str,
        depth: usize,
    ) -> rusqlite::Result<Option<Sym>> {
        self.find_export(family, files, prefix, name, depth, &mut BTreeSet::new())
    }

    /// `export_of` with the files whose re-exports were already searched (a chain of
    /// wildcard re-exports may be cyclic, and would otherwise be walked once per path).
    fn find_export(
        &mut self,
        family: Family,
        files: &[i64],
        prefix: &str,
        name: &str,
        depth: usize,
        seen: &mut BTreeSet<i64>,
    ) -> rusqlite::Result<Option<Sym>> {
        let qname = match prefix.is_empty() {
            true => name.to_string(),
            false => format!("{prefix}.{name}"),
        };
        for &fid in files {
            if let Some(s) = self.db.in_file(fid, name, &qname)?.into_iter().next() {
                return Ok(Some(s));
            }
        }
        if depth >= MAX_REEXPORT_DEPTH || !prefix.is_empty() {
            return Ok(None);
        }
        for &fid in files {
            if !seen.insert(fid) {
                continue;
            }
            let Some((ctx, _)) = self.db.file_ctx(fid)? else {
                continue;
            };
            let Some(scheme) = self.scheme(family) else {
                continue;
            };
            // only imports the file re-exports: named ones by their bound name
            let reexported: Vec<scope::Import> = ctx
                .imports
                .iter()
                .filter(|i| scheme.reexports(i))
                .cloned()
                .collect();
            let via: Vec<(String, Option<String>)> = match scheme.binding(&reexported, name) {
                Some(Bind::Item { module, name: orig }) => vec![(module, Some(orig))],
                Some(Bind::Module(_)) => continue,
                None => reexported
                    .iter()
                    .filter(|i| scheme.is_wildcard(i))
                    .map(|i| (i.module.clone(), None))
                    .collect(),
            };
            for (module, orig) in via {
                let Module::Files(next, p) = self.module_of(&ctx, &module)? else {
                    continue;
                };
                let wanted = orig.as_deref().unwrap_or(name);
                if let Some(s) = self.find_export(family, &next, &p, wanted, depth + 1, seen)? {
                    return Ok(Some(s));
                }
            }
        }
        Ok(None)
    }

    /// `item` of module `module`, as an import row or a qualifier names it: a symbol, or
    /// a submodule (`Found::Module`).
    fn module_item(
        &mut self,
        ctx: &FileCtx,
        module: &str,
        item: &str,
        reason: &'static str,
    ) -> rusqlite::Result<Found> {
        let (files, prefix) = match self.module_of(ctx, module)? {
            Module::Files(files, prefix) => (files, prefix),
            Module::External => return Ok(Found::Drop(Cause::External)),
            Module::Missing => return Ok(Found::Drop(Cause::Unresolved)),
        };
        // a `mod` declaration names a module, which is bound as a module below
        if let Some(s) = self
            .export_of(ctx.family, &files, &prefix, item, 0)?
            .filter(|s| s.kind != "Module")
        {
            return Ok(Found::Hit(s, Tier::ImportScoped, reason));
        }
        let sub = self.scheme(ctx.family).map(|sc| sc.join(module, item));
        Ok(match sub {
            Some(path) if matches!(self.module_of(ctx, &path)?, Module::Files(..)) => Found::Module,
            _ => Found::Drop(Cause::Unresolved),
        })
    }

    /// IMPORTS edges of one `use`/`import` row: to the symbol it names, else to the
    /// module's files.
    fn module_import(&mut self, f: &mut FileRun, imp: &scope::Import) -> rusqlite::Result<()> {
        let family = f.ctx.family;
        let Some(scheme) = self.scheme(family) else {
            return Ok(());
        };
        let reason = family.rules().import_reason;
        // the item this row names; none for a module import or a wildcard
        let item = match (
            imp.name.as_deref(),
            scheme.is_wildcard(imp),
            imp.kind.as_str(),
        ) {
            (_, true, _) | (Some("self"), ..) | (_, _, "mod" | "extern_crate") => None,
            (name, ..) => name,
        };
        if imp.kind == "extern_crate" {
            return Ok(());
        }
        let module = self.module_of(&f.ctx, &imp.module)?;
        let files = match (&module, item) {
            (Module::Files(files, _), _) => files.clone(),
            (Module::Missing, Some(n)) => {
                self.note_unresolved(f, n)?;
                return Ok(());
            }
            _ => return Ok(()),
        };
        let mut found = None;
        if let Some(n) = item {
            found = match self.module_item(&f.ctx, &imp.module, n, reason)? {
                Found::Hit(s, ..) => Some(vec![(s.file, Some(s.id))]),
                Found::Module => {
                    let sub = self.scheme(family).map(|sc| sc.join(&imp.module, n));
                    match sub.map(|p| self.module_of(&f.ctx, &p)).transpose()? {
                        Some(Module::Files(ids, _)) => {
                            Some(ids.into_iter().map(|id| (id, None)).collect())
                        }
                        _ => None,
                    }
                }
                _ => {
                    self.note_unresolved(f, n)?;
                    None
                }
            };
        }
        let targets = found.unwrap_or_else(|| files.iter().map(|id| (*id, None)).collect());
        for (file, symbol) in targets.into_iter().take(MAX_PACKAGE_IMPORT_EDGES) {
            if symbol.is_none() && file == f.ctx.id {
                continue;
            }
            f.edges.add(
                None,
                "IMPORTS",
                None,
                Target {
                    file,
                    symbol,
                    tier: Tier::ImportScoped,
                    reason,
                    via: &imp.module,
                },
            );
        }
        Ok(())
    }

    fn note_unresolved(&mut self, f: &mut FileRun, name: &str) -> rusqlite::Result<()> {
        if let Some(id) = self.db.name_id(name)? {
            f.drops.note(id, Cause::Unresolved);
        }
        Ok(())
    }

    /// The module path a qualifier `q` (`a`, `a.b`) names through the file's imports;
    /// None when it names no module (a type, a local, an unbound name).
    fn qualifier_path(&mut self, ctx: &FileCtx, q: &str) -> rusqlite::Result<Option<String>> {
        let (head, rest) = q.split_once('.').unwrap_or((q, ""));
        let Some(scheme) = self.scheme(ctx.family) else {
            return Ok(None);
        };
        Ok(match scheme.binding(&ctx.imports, head) {
            Some(Bind::Module(p)) => Some(scheme.join(&p, rest)),
            Some(Bind::Item { module, name }) => {
                // an item that is a symbol is a type or a value: the type route serves it
                let sub = scheme.join(&scheme.join(&module, &name), rest);
                match self.module_item(ctx, &module, &name, "import")? {
                    Found::Module => Some(sub),
                    _ => None,
                }
            }
            None => None,
        })
    }

    /// `q.name` where `q` names a module of the file's imports; None when it does not.
    fn scheme_member(
        &mut self,
        ctx: &FileCtx,
        q: &str,
        name: &str,
    ) -> rusqlite::Result<Option<Found>> {
        let Some(path) = self.qualifier_path(ctx, q)? else {
            return Ok(None);
        };
        Ok(Some(self.module_item(ctx, &path, name, "module-import")?))
    }

    /// A name bound by an import row or a wildcard import of a Rust, Python or Java
    /// file, else the file's own Java package.
    fn lookup_scheme(&mut self, ctx: &FileCtx, name: &str) -> rusqlite::Result<Found> {
        let family = ctx.family;
        let Some(scheme) = self.scheme(family) else {
            return Ok(Found::Drop(Cause::External));
        };
        // explicit imports, then the file's own package, then wildcard imports
        match scheme.binding(&ctx.imports, name) {
            Some(Bind::Item { module, name: item }) => {
                return self.module_item(ctx, &module, &item, "import");
            }
            Some(Bind::Module(_)) => return Ok(Found::Module),
            None => {}
        }
        if let Some(s) = scheme.package_scope(&self.db, ctx, name)? {
            return Ok(Found::Hit(s, Tier::ImportScoped, "same-package"));
        }
        let wildcards: Vec<String> = ctx
            .imports
            .iter()
            .filter(|i| scheme.is_wildcard(i))
            .map(|i| i.module.clone())
            .collect();
        for module in wildcards {
            if let Module::Files(files, prefix) = self.module_of(ctx, &module)?
                && let Some(s) = self.export_of(family, &files, &prefix, name, 0)?
            {
                return Ok(Found::Hit(s, Tier::ImportScoped, "wildcard-import"));
            }
        }
        self.global(ctx, name)
    }

    // ---- type hierarchy ----

    /// The type an `impl` block names, resolved from `f`'s scope; None for any other
    /// symbol, or when the type is not found.
    fn impl_type(&mut self, f: &mut FileRun, sym: &Sym) -> rusqlite::Result<Option<Sym>> {
        if sym.kind != "Impl" {
            return Ok(None);
        }
        Ok(match self.lookup_here(f, &sym.name)? {
            Found::Hit(t, ..) if t.kind != "Impl" && is_type(&t) => Some(t),
            _ => None,
        })
    }

    /// The impl block `block` is `ty`'s: its type name resolves, from the block's own file,
    /// to `ty` (an unresolved or ambiguous name is not decided: not the type's).
    fn impl_block_of(&mut self, block: i64, ty: &Sym) -> rusqlite::Result<bool> {
        let Some(imp) = self.db.symbol(block)? else {
            return Ok(false);
        };
        let Some((ctx, _)) = self.db.file_ctx(imp.file)? else {
            return Ok(false);
        };
        Ok(matches!(self.lookup(&ctx, &imp.name)?, Found::Hit(t, ..) if t.id == ty.id))
    }

    /// Where a Rust type's impl blocks are searched: its crate's source directory.
    fn impl_scope(family: Family, path: &str) -> Option<String> {
        family
            .rules()
            .crate_impls
            .then(|| imports_rust::src_root(path).to_string())
    }

    /// The order a member is searched in on `ty`: its bases resolved through the scope
    /// of the file that declares each, arranged by the family's rules (cached).
    fn hierarchy(&mut self, family: Family, ty: &Sym) -> rusqlite::Result<Rc<Hierarchy>> {
        if let Some(h) = self.hierarchies.get(&ty.id) {
            return Ok(h.clone());
        }
        if self.hierarchies.len() >= HIERARCHY_CACHE_MAX {
            self.hierarchies.clear();
        }
        let mut syms: HashMap<i64, Sym> = HashMap::from([(ty.id, ty.clone())]);
        let mut parents: HashMap<i64, Vec<i64>> = HashMap::new();
        let mut level = vec![ty.clone()];
        for _ in 0..MAX_INHERITANCE_DEPTH {
            let mut next = Vec::new();
            for t in level {
                if parents.contains_key(&t.id) || syms.len() >= MAX_HIERARCHY {
                    continue;
                }
                let Some((tctx, _)) = self.db.file_ctx(t.file)? else {
                    continue;
                };
                let src = Self::impl_scope(family, &tctx.path);
                let mut ids = Vec::new();
                for (hint, file, block) in self.db.base_hints(&t, src.as_deref())? {
                    // an impl block found by the type's simple name may be another type's
                    if let Some(block) = block
                        && !self.impl_block_of(block, &t)?
                    {
                        continue;
                    }
                    let ctx = match file == tctx.id {
                        true => tctx.clone(),
                        false => match self.db.file_ctx(file)? {
                            Some((c, _)) => c,
                            None => continue,
                        },
                    };
                    if let Found::Hit(b, ..) = self.hint_in(&ctx, &hint)?
                        && b.id != t.id
                        && !ids.contains(&b.id)
                    {
                        ids.push(b.id);
                        if syms.insert(b.id, b.clone()).is_none() {
                            next.push(b);
                        }
                    }
                }
                parents.insert(t.id, ids);
            }
            level = next;
        }
        let (ids, consistent) = mro::linearize(ty.id, family.rules().order, &parents);
        let order = ids.iter().filter_map(|id| syms.remove(id)).collect();
        let h = Rc::new(Hierarchy { order, consistent });
        self.hierarchies.insert(ty.id, h.clone());
        Ok(h)
    }

    /// Members `name` declared on `ty` itself.
    fn own_members(&mut self, family: Family, ty: &Sym, name: &str) -> rusqlite::Result<Vec<Sym>> {
        let qname = format!("{}.{name}", ty.qname);
        let Some((ctx, _)) = self.db.file_ctx(ty.file)? else {
            return Ok(Vec::new());
        };
        Ok(match family {
            Family::Go => self
                .db
                .in_go_dir(ctx.dir(), name, &qname)?
                .into_iter()
                .map(|(s, _)| s)
                .collect(),
            Family::Rust => {
                // impl blocks of the type in other files of the crate: one is the member,
                // several are ambiguous (a rebinding by module is not decided)
                let own = self.db.in_file(ty.file, name, &qname)?;
                if !own.is_empty() {
                    return Ok(own);
                }
                let mut found = Vec::new();
                for m in self.db.impl_methods(
                    imports_rust::src_root(&ctx.path),
                    &ty.name,
                    name,
                    ty.file,
                    IMPL_CANDIDATES,
                )? {
                    // the query matches by the type's simple name: keep the blocks of `ty`
                    if let Some(block) = m.container
                        && self.impl_block_of(block, ty)?
                    {
                        found.push(m);
                    }
                }
                found.truncate(2);
                found
            }
            Family::Ts | Family::Python | Family::Java => self.db.in_file(ty.file, name, &qname)?,
        })
    }

    // ---- references ----

    fn reference(&mut self, f: &mut FileRun, r: &Ref) -> rusqlite::Result<()> {
        use ref_kind::*;
        let found = match (&r.qual, r.kind) {
            (Some(q), _) => self.qualified(f, r, q)?,
            (None, MEMBER | MEMBER_CALL) => Found::Drop(Cause::ReceiverUntyped),
            (None, _) if f.local.binding(r.symbol, &r.name).is_some() => {
                self.counters.locals += 1;
                return Ok(());
            }
            (None, _) => match self.implicit_member(f, r)? {
                Some(found) => found,
                None => self.lookup_here(f, &r.name)?,
            },
        };
        match found {
            Found::Hit(target, tier, reason) => self.bind(f, r, &target, tier, reason)?,
            Found::Module => {}
            Found::Drop(cause) => f.drops.note(r.name_id, cause),
        }
        Ok(())
    }

    fn bind(
        &mut self,
        f: &mut FileRun,
        r: &Ref,
        target: &Sym,
        tier: Tier,
        reason: &'static str,
    ) -> rusqlite::Result<()> {
        let Some((kind, access)) = edge_kind(r.kind, target) else {
            return Ok(());
        };
        if target.is_callable() && !matches!(r.kind, ref_kind::CALL | ref_kind::MEMBER_CALL) {
            f.drops.note(r.name_id, Cause::CallableValue);
        }
        let mut src = f.local.source(r.symbol);
        // a reference in an `impl` header (its trait) belongs to the type
        if let Some(header) = src.and_then(|id| f.local.symbols.get(&id)).cloned() {
            src = self.impl_type(f, &header)?.map_or(src, |t| Some(t.id));
        }
        f.edges.add(
            src,
            kind,
            access,
            Target {
                file: target.file,
                symbol: Some(target.id),
                tier,
                reason,
                via: &r.name,
            },
        );
        Ok(())
    }

    /// A bare name inside a type that is one of its members (Java): the members of the
    /// enclosing types, innermost first.
    fn implicit_member(&mut self, f: &mut FileRun, r: &Ref) -> rusqlite::Result<Option<Found>> {
        let rules = f.ctx.family.rules();
        if !rules.implicit_this {
            return Ok(None);
        }
        let enclosing: Vec<Sym> = f
            .local
            .classes(r.symbol, rules.self_kinds)
            .cloned()
            .collect();
        for ty in enclosing {
            let recv = Found::Hit(ty, Tier::SameFile, "implicit-this");
            if let found @ Found::Hit(..) = self.member_found(f, r, recv)? {
                return Ok(Some(found));
            }
        }
        Ok(None)
    }

    /// `q.name`: type the qualifier, then find the member.
    fn qualified(&mut self, f: &mut FileRun, r: &Ref, q: &str) -> rusqlite::Result<Found> {
        let rules = f.ctx.family.rules();
        let flags = (
            rules.self_names.contains(&q),
            rules.super_names.contains(&q),
        );
        let enclosing = match flags {
            (false, false) => None,
            _ => f.local.class(r.symbol, rules.self_kinds).cloned(),
        };
        let recv = match flags {
            (true, _) => match enclosing {
                // an `impl` block stands for the type it names
                Some(c) => match self.impl_type(f, &c)? {
                    Some(t) => Found::Hit(t, Tier::SameFile, "this"),
                    None => Found::Hit(c, Tier::SameFile, "this"),
                },
                None => Found::Drop(Cause::ReceiverUntyped),
            },
            (_, true) => match enclosing {
                Some(c) => self.first_base(&c)?,
                None => Found::Drop(Cause::ReceiverUntyped),
            },
            _ => match f.local.binding(r.symbol, q) {
                Some(Some(hint)) => retag(self.lookup_hint(f, &hint)?, "receiver-typed"),
                Some(None) => Found::Drop(Cause::ReceiverUntyped),
                None => return self.through_qualifier(f, r, q),
            },
        };
        self.member_found(f, r, recv)
    }

    /// A qualifier that is no local binding: a package or namespace import, or a
    /// declared type or object (static access).
    fn through_qualifier(&mut self, f: &mut FileRun, r: &Ref, q: &str) -> rusqlite::Result<Found> {
        if let Some(found) = self.import_member(&f.ctx, q, &r.name)? {
            return Ok(found);
        }
        let recv = retag(self.lookup_here(f, q)?, "static-member");
        self.member_found(f, r, recv)
    }

    /// `q.name` where `q` is a Go package qualifier or a TS namespace import; None when
    /// `q` is neither.
    fn import_member(
        &mut self,
        ctx: &FileCtx,
        q: &str,
        name: &str,
    ) -> rusqlite::Result<Option<Found>> {
        let found = match ctx.family {
            Family::Rust | Family::Python | Family::Java => {
                return self.scheme_member(ctx, q, name);
            }
            Family::Go => {
                let Some(imp) = ctx
                    .imports
                    .iter()
                    .find(|i| matches!(imports_go::qualifier(i), Qualifier::Name(n) if n == q))
                else {
                    return Ok(None);
                };
                match self.modules.package_dir(self.db.conn, &imp.module)? {
                    Some(dir) => match self.package_export(&dir, name)? {
                        Some(s) => Found::Hit(s, Tier::ImportScoped, "package"),
                        None => Found::Drop(Cause::Unresolved),
                    },
                    None => Found::Drop(Cause::External),
                }
            }
            Family::Ts => {
                let Some((Binding::Namespace, imp)) = imports_ts::binding(&ctx.imports, q) else {
                    return Ok(None);
                };
                match imp.resolved {
                    Some(t) => match imports_ts::find_export(&self.db, t, name)? {
                        Some((s, _)) => Found::Hit(s, Tier::ImportScoped, "namespace-import"),
                        None => Found::Drop(Cause::Unresolved),
                    },
                    None if imp.target_key.is_some() => Found::Drop(Cause::Unresolved),
                    None => Found::Drop(Cause::External),
                }
            }
        };
        Ok(Some(found))
    }

    /// A binding's type hint: `T` from the file scope, `q.T` through a package, module
    /// or namespace import or as a member of a declared `q`.
    fn lookup_hint(&mut self, f: &mut FileRun, hint: &str) -> rusqlite::Result<Found> {
        match hint.contains('.') {
            true => self.hint_in(&f.ctx, hint),
            false => self.lookup_here(f, hint),
        }
    }

    /// `hint` resolved from the scope of the file `ctx`.
    fn hint_in(&mut self, ctx: &FileCtx, hint: &str) -> rusqlite::Result<Found> {
        let Some((q, t)) = hint.rsplit_once('.') else {
            return self.lookup(ctx, hint);
        };
        if let Some(found) = self.import_member(ctx, q, t)? {
            return Ok(found);
        }
        Ok(match self.lookup(ctx, q)? {
            Found::Hit(ty, tier, reason) => match self.member_of(ctx.family, &ty, t)? {
                Some(m) => Found::Hit(m.sym, tier, reason),
                None => Found::Drop(Cause::Unresolved),
            },
            other => other,
        })
    }

    /// The member `r.name` of a typed receiver.
    fn member_found(&mut self, f: &mut FileRun, r: &Ref, recv: Found) -> rusqlite::Result<Found> {
        let (ty, tier, reason) = match recv {
            Found::Hit(t, tier, reason) => (t, tier, reason),
            Found::Module => return Ok(Found::Drop(Cause::ReceiverUntyped)),
            drop => return Ok(drop),
        };
        let key = (ty.id, r.name.to_string());
        let member = match f.members.get(&key) {
            Some(m) => m.clone(),
            None => {
                let m = self.member_of(f.ctx.family, &ty, &r.name)?;
                f.members.insert(key, m.clone());
                m
            }
        };
        Ok(match member {
            Some(Member { sym: m, ambiguous }) => {
                if ty.is_interface() {
                    f.drops.note(r.name_id, Cause::DispatchBoundary);
                }
                if ambiguous {
                    f.drops.note(r.name_id, Cause::Ambiguous);
                }
                let tier = if m.file == f.ctx.id {
                    tier
                } else {
                    tier.min(Tier::ImportScoped)
                };
                Found::Hit(m, tier, reason)
            }
            None if is_type(&ty) => Found::Drop(Cause::Unresolved),
            None => Found::Drop(Cause::ReceiverUntyped),
        })
    }

    /// Member `name` of `ty`: declared on it (for Go, in any file of its package; for
    /// Rust, in any of its impl blocks), else on its bases in the family's search order.
    /// Several declarations of the name on one type (overloads) or an inconsistent C3
    /// order make the answer ambiguous.
    fn member_of(
        &mut self,
        family: Family,
        ty: &Sym,
        name: &str,
    ) -> rusqlite::Result<Option<Member>> {
        let h = self.hierarchy(family, ty)?;
        for t in &h.order {
            let mut own = self.own_members(family, t, name)?.into_iter();
            if let Some(sym) = own.next() {
                let ambiguous = own.next().is_some() || !h.consistent;
                return Ok(Some(Member { sym, ambiguous }));
            }
        }
        Ok(None)
    }

    /// The first base type of a class (`super`).
    fn first_base(&mut self, class: &Sym) -> rusqlite::Result<Found> {
        let Some((ctx, _)) = self.db.file_ctx(class.file)? else {
            return Ok(Found::Drop(Cause::Unresolved));
        };
        let Some((hint, ..)) = self.db.base_hints(class, None)?.into_iter().next() else {
            return Ok(Found::Drop(Cause::Unresolved));
        };
        Ok(retag(self.hint_in(&ctx, &hint)?, "super"))
    }

    // ---- plain-name lookup ----

    fn lookup_here(&mut self, f: &mut FileRun, name: &str) -> rusqlite::Result<Found> {
        if let Some(found) = f.lookups.get(name) {
            return Ok(found.clone());
        }
        // this file's own declarations are already in memory
        let found = match f.local.top_level(name) {
            Some(s) => Found::Hit(s.clone(), Tier::SameFile, "same-file"),
            None => self.lookup(&f.ctx, name)?,
        };
        f.lookups.insert(name.to_string(), found.clone());
        Ok(found)
    }

    /// Bind a plain name from `ctx`'s file scope (locals are the caller's concern).
    fn lookup(&mut self, ctx: &FileCtx, name: &str) -> rusqlite::Result<Found> {
        if let Some(s) = self.db.in_file(ctx.id, name, name)?.into_iter().next() {
            return Ok(Found::Hit(s, Tier::SameFile, "same-file"));
        }
        match ctx.family {
            Family::Go => self.lookup_go(ctx, name),
            Family::Ts => match imports_ts::binding(&ctx.imports, name) {
                Some((b, imp)) => self.through_import(b, imp, name),
                None => self.global(ctx, name),
            },
            Family::Rust | Family::Python | Family::Java => self.lookup_scheme(ctx, name),
        }
    }

    fn lookup_go(&mut self, ctx: &FileCtx, name: &str) -> rusqlite::Result<Found> {
        // the package's other files; a test file also sees the package's test files
        let same_package = self
            .db
            .in_go_dir(ctx.dir(), name, name)?
            .into_iter()
            .find(|(s, p)| s.file != ctx.id && (ctx.is_test() || !is_test_file(p)));
        if let Some((s, _)) = same_package {
            return Ok(Found::Hit(s, Tier::ImportScoped, "same-package"));
        }
        for imp in &ctx.imports {
            if matches!(imports_go::qualifier(imp), Qualifier::Dot)
                && let Some(dir) = self.modules.package_dir(self.db.conn, &imp.module)?
                && let Some(s) = self.package_export(&dir, name)?
            {
                return Ok(Found::Hit(s, Tier::ImportScoped, "dot-import"));
            }
        }
        // a package qualifier names the package itself
        if ctx
            .imports
            .iter()
            .any(|i| matches!(imports_go::qualifier(i), Qualifier::Name(n) if n == name))
        {
            return Ok(Found::Module);
        }
        // predeclared identifiers, or a name no scope declares
        Ok(Found::Drop(Cause::External))
    }

    /// An exported top-level name of the (non-test) files of a Go package.
    fn package_export(&self, dir: &str, name: &str) -> rusqlite::Result<Option<Sym>> {
        Ok(self
            .db
            .in_go_dir(dir, name, name)?
            .into_iter()
            .find(|(s, p)| s.exported && !is_test_file(p))
            .map(|(s, _)| s))
    }

    fn through_import(
        &self,
        b: Binding<'_>,
        imp: &scope::Import,
        local: &str,
    ) -> rusqlite::Result<Found> {
        let Some(target) = imp.resolved else {
            // a relative specifier naming no indexed file is a broken in-repo binding; a
            // non-relative one is a package or a path alias the resolver does not map
            let cause = match imp.target_key {
                Some(_) => Cause::Unresolved,
                None => Cause::UnresolvedModule,
            };
            return Ok(Found::Drop(cause));
        };
        let wanted = match b {
            Binding::Namespace => return Ok(Found::Module),
            Binding::Named(n) => n,
            Binding::Default => local,
        };
        Ok(match imports_ts::find_export(&self.db, target, wanted)? {
            Some((s, crossed)) => {
                let reason = if crossed { "re-export" } else { "import" };
                Found::Hit(s, Tier::ImportScoped, reason)
            }
            None => Found::Drop(Cause::Unresolved),
        })
    }

    /// TS/JS global tier: a name defined at top level by exactly one file. (The
    /// referencing file defines no top-level `name`: the same-file tier came first.)
    fn global(&mut self, ctx: &FileCtx, name: &str) -> rusqlite::Result<Found> {
        let common = name.len() < MIN_GLOBAL_NAME
            || STOPWORD_SYMBOLS.contains(&name.to_lowercase().as_str());
        if !ctx.family.rules().global {
            return Ok(Found::Drop(Cause::External));
        }
        let key = (ctx.family, name.to_string());
        let unique = match self.globals.get(&key) {
            Some(u) => *u,
            None => {
                if self.globals.len() >= GLOBAL_CACHE_MAX {
                    self.globals.clear();
                }
                // two definer files make the name ambiguous
                let u = match self.db.top_level_files(ctx.family, name, 2)?[..] {
                    [] => Global::None,
                    [file] => match self.db.in_file(file, name, name)?.first() {
                        Some(s) => Global::One { id: s.id, file },
                        None => Global::None,
                    },
                    _ => Global::Many,
                };
                self.globals.insert(key, u);
                u
            }
        };
        Ok(match (unique, common) {
            (Global::None, _) => Found::Drop(Cause::External),
            (Global::One { id, file }, false) if file != ctx.id => match self.db.symbol(id)? {
                Some(s) => Found::Hit(s, Tier::Global, "global-unique-name"),
                None => Found::Drop(Cause::Unresolved),
            },
            _ => Found::Drop(Cause::Ambiguous),
        })
    }
}

/// Global-tier definers of a name (ids only: the cache outlives many files).
#[derive(Clone, Copy)]
enum Global {
    None,
    One { id: i64, file: i64 },
    Many,
}

// ---- incremental dependencies ----

/// One entry of a file's export signature: what other files' resolution reads.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub enum Export {
    Symbol {
        name_id: i64,
        qname: String,
        kind: String,
        exported: bool,
    },
    Reexport {
        module: String,
        name: Option<String>,
        alias: Option<String>,
    },
    /// The base names (`extends`/`implements`, Go embedding) of a type: members bind
    /// through them.
    Bases { qname: String, bases: Vec<String> },
    /// The file uses ES export syntax, so its unexported declarations are not exports.
    EsModule,
}

pub type Signature = BTreeSet<Export>;

/// The export signature of a file: its non-local declarations and re-exports.
pub fn signature(conn: &Connection, fid: i64) -> rusqlite::Result<Signature> {
    let mut out = Signature::new();
    let mut st = conn.prepare_cached(
        "SELECT name_id, qualified_name, kind, exported FROM symbols WHERE file_id = ?1 AND local = 0",
    )?;
    for row in st.query_map([fid], |r| {
        Ok(Export::Symbol {
            name_id: r.get(0)?,
            qname: r.get(1)?,
            kind: r.get(2)?,
            exported: r.get(3)?,
        })
    })? {
        out.insert(row?);
    }
    let mut st = conn.prepare_cached(
        "SELECT module, name, alias FROM imports
         WHERE file_id = ?1 AND (kind IN ('reexport', 'reexport_type', 'reexport_all')
               OR (kind IN ('import', 'wildcard')
                   AND EXISTS (SELECT 1 FROM files WHERE id = ?1 AND lang = 'python')))",
    )?;
    for row in st.query_map([fid], |r| {
        Ok(Export::Reexport {
            module: r.get(0)?,
            name: r.get(1)?,
            alias: r.get(2)?,
        })
    })? {
        out.insert(row?);
    }
    let mut st = conn.prepare_cached(
        "SELECT s.qualified_name, n.name FROM refs r
         JOIN symbols s ON s.id = r.symbol_id JOIN names n ON n.id = r.name_id
         WHERE r.file_id = ?1 AND r.kind IN (?2, ?3) AND s.local = 0
         ORDER BY s.qualified_name, r.start_byte",
    )?;
    let mut bases: HashMap<String, Vec<String>> = HashMap::new();
    for row in st.query_map(params![fid, ref_kind::EXTENDS, ref_kind::IMPLEMENTS], |r| {
        Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
    })? {
        let (qname, base) = row?;
        bases.entry(qname).or_default().push(base);
    }
    out.extend(
        bases
            .into_iter()
            .map(|(qname, bases)| Export::Bases { qname, bases }),
    );
    if (Db { conn }).has_es_exports(fid)? {
        out.insert(Export::EsModule);
    }
    Ok(out)
}

/// What an update must re-resolve beyond the changed files themselves.
#[derive(Debug, Default)]
pub struct Dependents {
    /// Names whose binding may have changed: files referencing them re-resolve.
    pub names: BTreeSet<i64>,
    /// Files that depend on a changed export without naming it.
    pub files: BTreeSet<i64>,
}

impl Dependents {
    /// Record the difference between a file's signature before and after an update
    /// (`path` is the file's path; a deleted file has an empty `after`).
    pub fn note_change(
        &mut self,
        conn: &Connection,
        fid: i64,
        path: &str,
        before: &Signature,
        after: &Signature,
    ) -> rusqlite::Result<()> {
        // exports bound without being named here: re-exports, CommonJS declarations
        let mut importers_changed = false;
        for e in before.symmetric_difference(after) {
            match e {
                Export::Symbol {
                    name_id,
                    qname,
                    kind,
                    ..
                } => {
                    self.names.insert(*name_id);
                    // a Go method declared away from its type: the type's file owns
                    // the HAS_METHOD edge.
                    if kind == "Method"
                        && path.ends_with(".go")
                        && let Some((ty, _)) = qname.split_once('.')
                    {
                        self.files.extend(go_type_files(conn, path, ty)?);
                    }
                }
                // members bound through the old or the new bases may rebind: every
                // member name of either hierarchy
                Export::Bases { bases, .. } => {
                    self.names.extend(hierarchy_member_names(conn, bases)?);
                }
                Export::EsModule => importers_changed = true,
                Export::Reexport { name, alias, .. } => {
                    importers_changed = true;
                    // a module-file language names the module by path, not by resolved id
                    self.files.extend(importers_of(conn, path)?);
                    for n in [name, alias].into_iter().flatten() {
                        if let Some(id) = (Db { conn }).name_id(n)? {
                            self.names.insert(id);
                        }
                    }
                }
            }
        }
        if importers_changed {
            // anything bound through this file's exports without naming them: its importers
            let mut st = conn.prepare_cached(
                "SELECT DISTINCT file_id FROM imports WHERE resolved_file_id = ?1",
            )?;
            for f in st.query_map([fid], |r| r.get::<_, i64>(0))? {
                self.files.insert(f?);
            }
        }
        Ok(())
    }
}

/// Name ids of the members of the types named `bases` and of their own bases, up to
/// `MAX_INHERITANCE_DEPTH`. Types are matched by name across the repository (and an
/// aliased named import by its original name): an over-approximation, so a rebinding
/// through a base is never missed.
fn hierarchy_member_names(conn: &Connection, bases: &[String]) -> rusqlite::Result<Vec<i64>> {
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut level: Vec<String> = bases.to_vec();
    let mut out = Vec::new();
    let mut original = conn.prepare_cached(
        "SELECT DISTINCT name FROM imports WHERE alias = ?1 AND name IS NOT NULL",
    )?;
    let mut types = conn.prepare_cached(&format!(
        "SELECT {} FROM symbols s JOIN names n ON n.id = s.name_id
         WHERE s.qualified_name = ?1 AND s.local = 0",
        scope::SYM_COLS
    ))?;
    let mut members = conn.prepare_cached(
        "SELECT DISTINCT name_id FROM symbols
         WHERE local = 0 AND substr(qualified_name, 1, length(?1) + 1) = ?1 || '.'",
    )?;
    let db = Db { conn };
    for _ in 0..=MAX_INHERITANCE_DEPTH {
        let mut next = Vec::new();
        for name in std::mem::take(&mut level) {
            let aliased = original
                .query_map([&name], |r| r.get::<_, String>(0))?
                .collect::<rusqlite::Result<Vec<_>>>()?;
            for ty in std::iter::once(name).chain(aliased) {
                if !seen.insert(ty.clone()) {
                    continue;
                }
                for m in members.query_map([&ty], |r| r.get::<_, i64>(0))? {
                    out.push(m?);
                }
                for s in types.query_map([&ty], scope::sym)? {
                    next.extend(db.bases(&s?)?);
                }
            }
        }
        level = next;
    }
    Ok(out)
}

/// Files in `path`'s Go package that declare the top-level type `ty`.
fn go_type_files(conn: &Connection, path: &str, ty: &str) -> rusqlite::Result<Vec<i64>> {
    let dir = path.rsplit_once('/').map_or("", |(d, _)| d);
    Ok(Db { conn }
        .in_go_dir(dir, ty, ty)?
        .into_iter()
        .map(|(s, _)| s.file)
        .collect())
}

/// Files of the module-file languages (Rust, Python, Java) with an import that may name
/// the file at `path`, for when it is added or removed: a module is named by its stem
/// (the directory for `__init__.py` and `mod.rs`) as the last segment of an import's
/// module or as its imported name, and a Java package by its directory.
pub fn importers_of(conn: &Connection, path: &str) -> rusqlite::Result<BTreeSet<i64>> {
    let (dir, file) = path.rsplit_once('/').unwrap_or(("", path));
    let stem = file.rsplit_once('.').map_or(file, |(s, _)| s);
    let stem = match stem {
        "__init__" | "mod" => dir.rsplit('/').next().unwrap_or(dir),
        s => s,
    };
    let mut st = conn.prepare_cached(
        "SELECT DISTINCT i.file_id FROM imports i JOIN files f ON f.id = i.file_id
         WHERE f.lang IN ('rust', 'python', 'java')
           AND (i.name = ?1 OR i.module = ?1 OR i.module LIKE '%.' || ?1
                OR i.module LIKE '%::' || ?1 OR i.module LIKE '%.' || ?1 || '.%'
                OR i.module LIKE '%::' || ?1 || '::%'
                OR (i.kind = 'wildcard' AND ?2 LIKE '%' || replace(i.module, '.', '/')))",
    )?;
    let rows = st.query_map(params![stem, dir], |r| r.get::<_, i64>(0))?;
    rows.collect()
}

/// Symbol ids by UID of a file, taken before its rows are rewritten.
pub fn symbol_ids(conn: &Connection, fid: i64) -> rusqlite::Result<Vec<(String, i64)>> {
    conn.prepare_cached("SELECT uid, id FROM symbols WHERE file_id = ?1")?
        .query_map([fid], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect()
}

/// After a file's rows were rewritten, point other files' symbol edges at the new
/// ids of the same UIDs. Edges to a UID that is gone are deleted; their source files
/// are returned (they must re-resolve).
pub fn remap_symbol_ids(
    conn: &Connection,
    fid: i64,
    old: &[(String, i64)],
) -> rusqlite::Result<BTreeSet<i64>> {
    let new: HashMap<String, i64> = symbol_ids(conn, fid)?.into_iter().collect();
    let mut owners = BTreeSet::new();
    let mut src_of =
        conn.prepare_cached("SELECT DISTINCT src_file FROM edges WHERE dst_symbol = ?1")?;
    let mut upd = conn.prepare_cached("UPDATE edges SET dst_symbol = ?2 WHERE dst_symbol = ?1")?;
    let mut del = conn.prepare_cached("DELETE FROM edges WHERE dst_symbol = ?1")?;
    for (uid, old_id) in old {
        match new.get(uid) {
            Some(n) if n == old_id => {}
            Some(n) => {
                upd.execute(params![old_id, n])?;
            }
            None => {
                for f in src_of.query_map([old_id], |r| r.get::<_, i64>(0))? {
                    owners.insert(f?);
                }
                del.execute([old_id])?;
            }
        }
    }
    owners.remove(&fid);
    Ok(owners)
}
