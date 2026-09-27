//! TS/JS import semantics: named (`import { a as b }`), default, namespace
//! (`import * as ns`) and CommonJS `require` bindings, and export lookup through
//! `export { a } from` and `export * from` re-export chains.

use super::scope::{Db, Import, Sym};

/// What a local import name is bound to.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Binding<'i> {
    /// `import { name }` / `import { name as local }`.
    Named(&'i str),
    /// `import local from`: the target's default export.
    Default,
    /// `import * as local`, `const local = require(...)`: the module object.
    Namespace,
}

/// The binding of `local` among a file's imports, with the import row.
pub fn binding<'i>(imports: &'i [Import], local: &str) -> Option<(Binding<'i>, &'i Import)> {
    imports.iter().find_map(|imp| {
        let b = match (imp.kind.as_str(), imp.name.as_deref()) {
            ("import", Some("default")) => Binding::Default,
            ("import", Some("*")) | ("require", _) => Binding::Namespace,
            ("import", Some(n)) => Binding::Named(n),
            _ => return None,
        };
        let bound = imp.alias.as_deref().or(imp.name.as_deref())?;
        (bound == local).then_some((b, imp))
    })
}

/// Re-export chains followed at most this deep (cycles stop earlier).
const MAX_REEXPORT_DEPTH: usize = 6;

/// The symbol file `fid` exports as `name`, following re-exports; the flag says a
/// re-export was crossed.
pub fn find_export(db: &Db<'_>, fid: i64, name: &str) -> rusqlite::Result<Option<(Sym, bool)>> {
    let mut seen = Vec::new();
    find(db, fid, name, 0, &mut seen)
}

fn find(
    db: &Db<'_>,
    fid: i64,
    name: &str,
    depth: usize,
    seen: &mut Vec<(i64, String)>,
) -> rusqlite::Result<Option<(Sym, bool)>> {
    if depth > MAX_REEXPORT_DEPTH || seen.iter().any(|(f, n)| *f == fid && n == name) {
        return Ok(None);
    }
    seen.push((fid, name.to_string()));
    let own = db.in_file(fid, name, name)?;
    // an exported declaration wins; a CommonJS file (no ES export syntax) exports
    // without the keyword.
    let exported = match own.iter().find(|s| s.exported) {
        Some(s) => Some(s),
        None if !own.is_empty() && !db.has_es_exports(fid)? => own.first(),
        None => None,
    };
    if let Some(s) = exported {
        return Ok(Some((s.clone(), depth > 0)));
    }
    let Some((ctx, _)) = db.file_ctx(fid)? else {
        return Ok(None);
    };
    for imp in &ctx.imports {
        let Some(target) = imp.resolved else {
            continue;
        };
        let next = match (imp.kind.as_str(), imp.alias.as_deref(), imp.name.as_deref()) {
            ("reexport", Some(alias), Some(orig)) if alias == name => orig,
            ("reexport", None, Some(orig)) if orig == name => orig,
            ("reexport_all", None, _) => name,
            _ => continue,
        };
        if let Some((s, _)) = find(db, target, next, depth + 1, seen)? {
            return Ok(Some((s, true)));
        }
    }
    Ok(None)
}
