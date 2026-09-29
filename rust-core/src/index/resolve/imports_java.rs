//! Java import semantics: an import names a class by its fully qualified name
//! (`import a.b.C;` binds `C`), a static or nested member (`import a.b.C.m;`), or a whole
//! package (`import a.b.*;`). A package is a directory below a source root, so a name is
//! matched against the tail of a file's path (`a/b/C.java`); the classes of a file's own
//! directory are in scope without an import; anything else is the JDK or a dependency.

use std::collections::HashMap;

use rusqlite::Result;

use super::scope::{Bind, Db, FileCtx, Import, Module, Scheme, Sym};

/// Module lookups cached per run before the cache is cleared.
const CACHE_MAX: usize = 1024;

#[derive(Default)]
pub struct Java {
    cache: HashMap<String, Module>,
}

impl Scheme for Java {
    fn module(&mut self, db: &Db<'_>, _from: &FileCtx, path: &str) -> Result<Module> {
        if let Some(m) = self.cache.get(path) {
            return Ok(m.clone());
        }
        if self.cache.len() >= CACHE_MAX {
            self.cache.clear();
        }
        let m = locate(db, path)?;
        self.cache.insert(path.to_string(), m.clone());
        Ok(m)
    }

    fn binding(&self, imports: &[Import], local: &str) -> Option<Bind> {
        imports.iter().find_map(|imp| {
            let name = imp.name.as_deref().filter(|n| *n == local)?;
            Some(Bind::Item {
                module: imp.module.clone(),
                name: name.to_string(),
            })
        })
    }

    fn is_wildcard(&self, imp: &Import) -> bool {
        imp.kind == "wildcard"
    }

    fn reexports(&self, _imp: &Import) -> bool {
        false
    }

    fn package_scope(&self, db: &Db<'_>, from: &FileCtx, name: &str) -> Result<Option<Sym>> {
        Ok(db
            .in_lang_dir("java", from.dir(), name, name)?
            .into_iter()
            .find(|(s, _)| s.file != from.id)
            .map(|(s, _)| s))
    }

    fn join(&self, base: &str, name: &str) -> String {
        match name.is_empty() {
            true => base.to_string(),
            false => format!("{base}.{name}"),
        }
    }
}

/// A package (its directory's files), else the class a path names with its nested
/// segments as the qualified prefix; a path outside the repository is external.
fn locate(db: &Db<'_>, path: &str) -> Result<Module> {
    let segments: Vec<&str> = path.split('.').collect();
    let package = db.files_in_tail("java", &segments.join("/"))?;
    if !package.is_empty() {
        return Ok(Module::Files(
            package.into_iter().map(|(id, _)| id).collect(),
            String::new(),
        ));
    }
    // the longest leading run of segments that is a class's file
    for k in (1..=segments.len()).rev() {
        let tail = format!("{}.java", segments[..k].join("/"));
        if let Some(id) = db.file_ending("java", &tail)? {
            return Ok(Module::Files(vec![id], segments[k - 1..].join(".")));
        }
    }
    Ok(Module::External)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn import_binds_the_class_name() {
        let imp = Import {
            kind: "import".into(),
            reason: "import",
            module: "com.acme".into(),
            name: Some("Widget".into()),
            alias: None,
            target_key: None,
            resolved: None,
        };
        let j = Java::default();
        assert_eq!(
            j.binding(&[imp], "Widget"),
            Some(Bind::Item {
                module: "com.acme".into(),
                name: "Widget".into()
            })
        );
        assert_eq!(j.join("com.acme", "Widget"), "com.acme.Widget");
    }
}
