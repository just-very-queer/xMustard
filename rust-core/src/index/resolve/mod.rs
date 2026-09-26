//! Scope and import resolver for Go and TS/JS (WS-17, PAR-IMP-01, PAR-IMP-05).
//!
//! For each Go or TS/JS file it binds the file's references to symbols and writes typed
//! symbol edges (`edges.layer = 'symbol'`): CALLS, USES, ACCESSES (read/write), EXTENDS,
//! IMPLEMENTS, IMPORTS, HAS_METHOD and HAS_PROPERTY. Each edge carries a confidence
//! tier (same-file 0.95, import-scoped 0.9, global 0.5), a provenance (`scope` for
//! scope-proven bindings, `lexical` for the global name-only fallback) and a reason.
//! The lexical file edges (`structure`/`flow` layers) stay as the fallback for every
//! language.
//!
//! Binding order for a plain name: local binding (no edge), same file, then the
//! language's import scope (TS named/default imports and re-export chains; the Go
//! package's other files and dot imports), then, for TS/JS only, a unique top-level
//! definition anywhere in the family (global tier). A member access (`q.name`) is
//! typed through its qualifier: `this`/`super`, a local whose declaration names or
//! constructs its type (receivers, typed parameters, `new T()`, `&T{}`, `NewT()`), a
//! namespace import or Go package qualifier, or a declared type (static access).
//! Members are searched on the type, then its bases first-wins.
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
pub mod imports_ts;
pub mod scope;

use std::collections::{BTreeSet, HashMap};
use std::path::Path;

use rusqlite::{Connection, params};

use counters::{Cause, FileDrops, ResolveCounters};
use emit::{Edges, Target, edge_kind, member_kind};
use imports_go::{Modules, Qualifier};
use imports_ts::Binding;
use scope::{Db, Family, FileCtx, Found, Local, Names, Ref, Sym, Tier};

use super::edges::{STOPWORD_SYMBOLS, is_test_file};
use super::facts::ref_kind;
use super::meta;

/// Bump when resolution output changes for the same facts. Part of the analyzer
/// version, so a change forces a full rebuild.
pub const RESOLVER_REVISION: u32 = 1;

/// Global-tier names shorter than this are too common to bind by name alone.
const MIN_GLOBAL_NAME: usize = 4;
/// Top-level definitions fetched per global-tier name (two files make it ambiguous).
const GLOBAL_CANDIDATES: usize = 8;
/// Global lookups cached per run before the cache is cleared (bounded transient).
const GLOBAL_CACHE_MAX: usize = 4096;
/// Base types searched for a member, first-wins.
const MAX_INHERITANCE_DEPTH: usize = 4;
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
        "Class" | "Struct" | "Interface" | "Enum" | "TypeAlias" | "Namespace" | "Trait"
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
    members: HashMap<(i64, String), Option<Sym>>,
}

pub struct Resolver<'c> {
    db: Db<'c>,
    modules: Modules,
    globals: HashMap<(Family, String), Global>,
    pub counters: ResolveCounters,
}

impl<'c> Resolver<'c> {
    pub fn new(conn: &'c Connection, root: &Path) -> Resolver<'c> {
        Resolver {
            db: Db { conn },
            modules: Modules::new(root),
            globals: HashMap::new(),
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
        let n = f.edges.write(conn, fid, &mut self.counters)?;
        f.drops.write(conn, fid, &mut self.counters)?;
        self.counters.files += 1;
        Ok(n)
    }

    // ---- declarations: HAS_METHOD / HAS_PROPERTY, Go interface boundaries ----

    fn declarations(&mut self, f: &mut FileRun) -> rusqlite::Result<()> {
        let mut own: Vec<&Sym> = f.local.symbols.values().collect();
        own.sort_by_key(|s| s.id);
        for s in own {
            if let Some(c) = s.container.and_then(|c| f.local.symbols.get(&c))
                && is_type(c)
                && let Some(kind) = member_kind(s)
            {
                let t = Target {
                    file: s.file,
                    symbol: Some(s.id),
                    tier: Tier::SameFile,
                    reason: "declaration",
                    via: &s.name,
                };
                f.edges.add(Some(c.id), kind, None, t);
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
        let reason = match (&found, imp.kind.as_str()) {
            (Some((_, true)), _) | (_, "reexport" | "reexport_all") => "re-export",
            _ => "import",
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
            (None, _) => self.lookup_here(f, &r.name)?,
        };
        match found {
            Found::Hit(target, tier, reason) => self.bind(f, r, &target, tier, reason),
            Found::Module => {}
            Found::Drop(cause) => f.drops.note(r.name_id, cause),
        }
        Ok(())
    }

    fn bind(&mut self, f: &mut FileRun, r: &Ref, target: &Sym, tier: Tier, reason: &'static str) {
        let Some((kind, access)) = edge_kind(r.kind, target) else {
            return;
        };
        if target.is_callable() && !matches!(r.kind, ref_kind::CALL | ref_kind::MEMBER_CALL) {
            f.drops.note(r.name_id, Cause::CallableValue);
        }
        let src = f.local.source(r.symbol);
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
    }

    /// `q.name`: type the qualifier, then find the member.
    fn qualified(&mut self, f: &mut FileRun, r: &Ref, q: &str) -> rusqlite::Result<Found> {
        let recv = match (f.ctx.family, q) {
            (Family::Ts, "this") => match f.local.class(r.symbol) {
                Some(c) => Found::Hit(c.clone(), Tier::SameFile, "this"),
                None => Found::Drop(Cause::ReceiverUntyped),
            },
            (Family::Ts, "super") => match f.local.class(r.symbol).cloned() {
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
        if let Some(found) = self.import_member(f, q, &r.name)? {
            return Ok(found);
        }
        let recv = retag(self.lookup_here(f, q)?, "static-member");
        self.member_found(f, r, recv)
    }

    /// `q.name` where `q` is a Go package qualifier or a TS namespace import; None when
    /// `q` is neither.
    fn import_member(
        &mut self,
        f: &FileRun,
        q: &str,
        name: &str,
    ) -> rusqlite::Result<Option<Found>> {
        let found = match f.ctx.family {
            Family::Go => {
                let Some(imp) = f
                    .ctx
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
                let Some((Binding::Namespace, imp)) = imports_ts::binding(&f.ctx.imports, q) else {
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

    /// A binding's type hint: `T` from the file scope, `q.T` through a package or
    /// namespace import or as a member of a declared `q`.
    fn lookup_hint(&mut self, f: &mut FileRun, hint: &str) -> rusqlite::Result<Found> {
        let Some((q, t)) = hint.split_once('.') else {
            return self.lookup_here(f, hint);
        };
        if let Some(found) = self.import_member(f, q, t)? {
            return Ok(found);
        }
        Ok(match self.lookup_here(f, q)? {
            Found::Hit(ty, tier, reason) => match self.member_of(f.ctx.family, &ty, t, 0)? {
                Some(m) => Found::Hit(m, tier, reason),
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
                let m = self.member_of(f.ctx.family, &ty, &r.name, 0)?;
                f.members.insert(key, m.clone());
                m
            }
        };
        Ok(match member {
            Some(m) => {
                if ty.is_interface() {
                    f.drops.note(r.name_id, Cause::DispatchBoundary);
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

    /// Member `name` of `ty`: declared on it (for Go, in any file of its package),
    /// else on its bases in declaration order (first wins).
    fn member_of(
        &mut self,
        family: Family,
        ty: &Sym,
        name: &str,
        depth: usize,
    ) -> rusqlite::Result<Option<Sym>> {
        let qname = format!("{}.{name}", ty.qname);
        let Some((ctx, _)) = self.db.file_ctx(ty.file)? else {
            return Ok(None);
        };
        let own = match family {
            Family::Ts => self.db.in_file(ty.file, name, &qname)?.into_iter().next(),
            Family::Go => self
                .db
                .in_go_dir(ctx.dir(), name, &qname)?
                .into_iter()
                .next()
                .map(|(s, _)| s),
        };
        if own.is_some() || depth >= MAX_INHERITANCE_DEPTH {
            return Ok(own);
        }
        for base in self.db.bases(ty)? {
            if let Found::Hit(b, ..) = self.lookup(&ctx, &base)?
                && let Some(m) = self.member_of(family, &b, name, depth + 1)?
            {
                return Ok(Some(m));
            }
        }
        Ok(None)
    }

    /// The first base type of a class (`super`).
    fn first_base(&mut self, class: &Sym) -> rusqlite::Result<Found> {
        let Some((ctx, _)) = self.db.file_ctx(class.file)? else {
            return Ok(Found::Drop(Cause::Unresolved));
        };
        let Some(base) = self.db.bases(class)?.into_iter().next() else {
            return Ok(Found::Drop(Cause::Unresolved));
        };
        Ok(retag(self.lookup(&ctx, &base)?, "super"))
    }

    // ---- plain-name lookup ----

    fn lookup_here(&mut self, f: &mut FileRun, name: &str) -> rusqlite::Result<Found> {
        if let Some(found) = f.lookups.get(name) {
            return Ok(found.clone());
        }
        let found = self.lookup(&f.ctx, name)?;
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
            // a relative specifier naming no indexed file is a broken in-repo binding
            let cause = if imp.target_key.is_some() {
                Cause::Unresolved
            } else {
                Cause::External
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
        let key = (ctx.family, name.to_string());
        let unique = match self.globals.get(&key) {
            Some(u) => *u,
            None => {
                if self.globals.len() >= GLOBAL_CACHE_MAX {
                    self.globals.clear();
                }
                let defs = self.db.top_level(ctx.family, name, GLOBAL_CANDIDATES)?;
                let files: BTreeSet<i64> = defs.iter().map(|(s, _)| s.file).collect();
                let u = match (files.len(), defs.first()) {
                    (1, Some((s, _))) => Global::One {
                        id: s.id,
                        file: s.file,
                    },
                    (0, _) => Global::None,
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
         WHERE file_id = ?1 AND kind IN ('reexport', 'reexport_all')",
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
        let mut reexports_changed = false;
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
                Export::Reexport { name, alias, .. } => {
                    reexports_changed = true;
                    for n in [name, alias].into_iter().flatten() {
                        if let Some(id) = (Db { conn }).name_id(n)? {
                            self.names.insert(id);
                        }
                    }
                }
            }
        }
        if reexports_changed {
            // anything bound through this file's re-exports: its importers
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

/// Files in `path`'s Go package that declare the top-level type `ty`.
fn go_type_files(conn: &Connection, path: &str, ty: &str) -> rusqlite::Result<Vec<i64>> {
    let dir = path.rsplit_once('/').map_or("", |(d, _)| d);
    Ok(Db { conn }
        .in_go_dir(dir, ty, ty)?
        .into_iter()
        .map(|(s, _)| s.file)
        .collect())
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
