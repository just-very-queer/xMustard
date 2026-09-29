//! Python import semantics: `import a.b` binds `a` (`import a.b as x` binds `x` to
//! `a.b`), `from m import n as x` binds `x` to the item `n` of `m` (or to the submodule
//! `m.n`), a leading dot names a package relative to the importing file, and a
//! directory of modules without `__init__.py` is a namespace package. Absolute module
//! paths are looked up under the repository root, `src` and the importing file's
//! ancestor directories (its script or package root); a path found nowhere is external.

use std::collections::HashMap;

use rusqlite::Result;

use super::scope::{Bind, Db, FileCtx, Import, Module, Scheme};

/// Module lookups cached per run before the cache is cleared.
const CACHE_MAX: usize = 1024;

#[derive(Default)]
pub struct Python {
    /// (importing directory, module path) → module.
    cache: HashMap<(String, String), Module>,
}

impl Scheme for Python {
    fn module(&mut self, db: &Db<'_>, from: &FileCtx, path: &str) -> Result<Module> {
        let key = (from.dir().to_string(), path.to_string());
        if let Some(m) = self.cache.get(&key) {
            return Ok(m.clone());
        }
        if self.cache.len() >= CACHE_MAX {
            self.cache.clear();
        }
        let m = locate(db, from, path)?;
        self.cache.insert(key, m.clone());
        Ok(m)
    }

    fn binding(&self, imports: &[Import], local: &str) -> Option<Bind> {
        // a later import rebinds the name
        imports.iter().rev().find_map(|imp| {
            let (bound, bind) = match (imp.kind.as_str(), imp.name.as_deref(), imp.alias.as_deref())
            {
                ("import", None, Some(alias)) => (alias, Bind::Module(imp.module.clone())),
                ("import", None, None) => {
                    let root = imp.module.split('.').next()?;
                    (root, Bind::Module(root.to_string()))
                }
                ("import", Some(name), alias) => (
                    alias.unwrap_or(name),
                    Bind::Item {
                        module: imp.module.clone(),
                        name: name.to_string(),
                    },
                ),
                _ => return None,
            };
            (bound == local).then_some(bind)
        })
    }

    fn is_wildcard(&self, imp: &Import) -> bool {
        imp.kind == "wildcard"
    }

    fn reexports(&self, _imp: &Import) -> bool {
        true
    }

    fn join(&self, base: &str, name: &str) -> String {
        match (base.ends_with('.') || base.is_empty(), name.is_empty()) {
            (_, true) => base.to_string(),
            (true, false) => format!("{base}{name}"),
            (false, false) => format!("{base}.{name}"),
        }
    }
}

/// The directories an absolute module path may be rooted at: the repository root, `src`,
/// then (for a script, not a package member) the importing file's ancestors. A package
/// member imports through the interpreter's path, so an ancestor directory is no root:
/// `import json` in package `app` is the standard library, not `app/json.py`.
fn roots(db: &Db<'_>, from: &FileCtx) -> Result<Vec<String>> {
    let mut out = vec![String::new(), "src".to_string()];
    let init = [from.dir(), "__init__.py"]
        .into_iter()
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>()
        .join("/");
    let member = !from.dir().is_empty() && db.file_at(&init)?.is_some();
    let mut dir = from.dir();
    while !member && !dir.is_empty() {
        out.push(dir.to_string());
        dir = dir.rsplit_once('/').map_or("", |(d, _)| d);
    }
    out.dedup();
    Ok(out)
}

fn locate(db: &Db<'_>, from: &FileCtx, path: &str) -> Result<Module> {
    let dots = path.bytes().take_while(|b| *b == b'.').count();
    let rest = &path[dots..];
    let segments: Vec<&str> = rest.split('.').filter(|s| !s.is_empty()).collect();
    let candidates: Vec<String> = match dots {
        0 => roots(db, from)?,
        n => {
            // one dot is the importing file's package, each more its parent
            let mut dir = from.dir();
            for _ in 1..n {
                dir = dir.rsplit_once('/').map_or("", |(d, _)| d);
            }
            vec![dir.to_string()]
        }
    };
    for root in &candidates {
        let base = [root.as_str(), &segments.join("/")]
            .into_iter()
            .filter(|s| !s.is_empty())
            .collect::<Vec<_>>()
            .join("/");
        let files = [format!("{base}.py"), format!("{base}/__init__.py")];
        let file = match base.is_empty() {
            true => db.file_at("__init__.py")?,
            false => files
                .iter()
                .find_map(|f| db.file_at(f).transpose())
                .transpose()?,
        };
        if let Some(id) = file {
            return Ok(Module::Files(vec![id], String::new()));
        }
        // a namespace package: a directory holding modules, without `__init__.py`
        if !base.is_empty() && !db.files_in("python", &base)?.is_empty() {
            return Ok(Module::Files(Vec::new(), String::new()));
        }
    }
    Ok(match dots {
        0 => Module::External,
        _ => Module::Missing,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn imp(kind: &str, module: &str, name: Option<&str>, alias: Option<&str>) -> Import {
        Import {
            kind: kind.into(),
            reason: "import",
            module: module.into(),
            name: name.map(Into::into),
            alias: alias.map(Into::into),
            target_key: None,
            resolved: None,
        }
    }

    #[test]
    fn import_forms_bind_names() {
        let imports = [
            imp("import", "a.b.c", None, None),
            imp("import", "x.y", None, Some("z")),
            imp("import", ".pkg", Some("helper"), Some("h")),
            imp("import", "m", Some("n"), None),
            imp("wildcard", "w", None, None),
        ];
        let p = Python::default();
        assert_eq!(p.binding(&imports, "a"), Some(Bind::Module("a".into())));
        assert_eq!(p.binding(&imports, "z"), Some(Bind::Module("x.y".into())));
        assert_eq!(
            p.binding(&imports, "h"),
            Some(Bind::Item {
                module: ".pkg".into(),
                name: "helper".into()
            })
        );
        assert!(p.binding(&imports, "n").is_some());
        assert_eq!(p.binding(&imports, "w"), None);
        assert_eq!(p.join(".", "x"), ".x");
        assert_eq!(p.join("a", "b"), "a.b");
    }
}
