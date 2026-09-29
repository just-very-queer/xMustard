//! Rust import semantics: `use` paths, `pub use` re-exports and `mod x;` declarations.
//! A path starts at `crate` (the source root of the importing file's crate), `self`,
//! `super`, a workspace crate (found through the `Cargo.toml` files above the indexed
//! Rust files) or a child module of the current module; anything else is `std` or a
//! dependency. A module is a file (`a/b.rs` or `a/b/mod.rs`, `lib.rs`/`main.rs` at the
//! root); what follows the longest path that is a file names an inline module, a type
//! or an enum variant inside it.

use std::collections::{BTreeSet, HashMap};
use std::path::{Path, PathBuf};

use rusqlite::{Connection, Result};

use super::scope::{Bind, Db, FileCtx, Import, Module, Scheme};

/// Module lookups cached per run before the cache is cleared.
const CACHE_MAX: usize = 1024;

/// Path roots that need no import binding.
const ROOTS: &[&str] = &["crate", "self", "super"];

pub struct Rust {
    root: PathBuf,
    /// Workspace crate name (with `_` for `-`) → its source directory, discovered once.
    crates: Option<HashMap<String, String>>,
    cache: HashMap<(String, String), Module>,
}

impl Rust {
    pub fn new(root: &Path) -> Rust {
        Rust {
            root: root.to_path_buf(),
            crates: None,
            cache: HashMap::new(),
        }
    }

    /// `Cargo.toml` files in the directories of indexed Rust files and their ancestors.
    fn discover(&self, conn: &Connection) -> Result<HashMap<String, String>> {
        let mut dirs: BTreeSet<String> = BTreeSet::new();
        let mut st = conn.prepare("SELECT path FROM files WHERE lang = 'rust'")?;
        let mut rows = st.query([])?;
        while let Some(r) = rows.next()? {
            let path: String = r.get(0)?;
            let mut d = path.rsplit_once('/').map_or("", |(d, _)| d).to_string();
            while dirs.insert(d.clone()) {
                match d.rsplit_once('/') {
                    Some((parent, _)) => d = parent.to_string(),
                    None if !d.is_empty() => d = String::new(),
                    None => break,
                }
            }
        }
        let mut out = HashMap::new();
        for dir in &dirs {
            let rel = [dir.as_str(), "Cargo.toml"]
                .into_iter()
                .filter(|s| !s.is_empty())
                .collect::<Vec<_>>()
                .join("/");
            if let Ok(text) = crate::symbolgraph::read_repo_file_beneath(&self.root, &rel)
                && let Some(name) = package_name(&text)
            {
                let src = [dir.as_str(), "src"]
                    .into_iter()
                    .filter(|s| !s.is_empty())
                    .collect::<Vec<_>>()
                    .join("/");
                out.entry(name.replace('-', "_")).or_insert(src);
            }
        }
        Ok(out)
    }
}

impl Scheme for Rust {
    fn module(&mut self, db: &Db<'_>, from: &FileCtx, path: &str) -> Result<Module> {
        let key = (from.path.clone(), path.to_string());
        if let Some(m) = self.cache.get(&key) {
            return Ok(m.clone());
        }
        if self.cache.len() >= CACHE_MAX {
            self.cache.clear();
        }
        if self.crates.is_none() {
            self.crates = Some(self.discover(db.conn)?);
        }
        let crates = self.crates.as_ref().map_or_else(HashMap::new, Clone::clone);
        let m = locate(db, from, path, &crates)?;
        self.cache.insert(key, m.clone());
        Ok(m)
    }

    fn binding(&self, imports: &[Import], local: &str) -> Option<Bind> {
        if ROOTS.contains(&local) {
            return Some(Bind::Module(local.to_string()));
        }
        imports.iter().find_map(|imp| {
            let name = imp.name.as_deref();
            let (bound, bind) = match (imp.kind.as_str(), name, imp.alias.as_deref()) {
                ("mod", _, _) => (imp.module.as_str(), Bind::Module(imp.module.clone())),
                ("use" | "reexport", Some("self"), alias) => (
                    alias.unwrap_or_else(|| imp.module.rsplit("::").next().unwrap_or("")),
                    Bind::Module(imp.module.clone()),
                ),
                ("use" | "reexport", Some(n), alias) if n != "*" => (
                    alias.unwrap_or(n),
                    Bind::Item {
                        module: imp.module.clone(),
                        name: n.to_string(),
                    },
                ),
                _ => return None,
            };
            (bound == local).then_some(bind)
        })
    }

    fn is_wildcard(&self, imp: &Import) -> bool {
        matches!(imp.kind.as_str(), "use" | "reexport") && imp.name.as_deref() == Some("*")
    }

    fn reexports(&self, imp: &Import) -> bool {
        imp.kind == "reexport"
    }

    fn join(&self, base: &str, name: &str) -> String {
        let name = name.replace('.', "::");
        match (base.is_empty(), name.is_empty()) {
            (_, true) => base.to_string(),
            (true, false) => name,
            (false, false) => format!("{base}::{name}"),
        }
    }
}

/// The `name` of a Cargo manifest's `[package]` table.
fn package_name(manifest: &str) -> Option<String> {
    let mut in_package = false;
    for line in manifest.lines().map(str::trim) {
        match (line.starts_with('['), line) {
            (true, l) => in_package = l == "[package]",
            (false, l) if in_package => {
                if let Some(rest) = l.strip_prefix("name")
                    && let Some(v) = rest.trim_start().strip_prefix('=')
                {
                    return Some(v.trim().trim_matches('"').to_string());
                }
            }
            _ => {}
        }
    }
    None
}

/// The source directory of the crate `path` belongs to: up to its first `src` directory,
/// else its own directory (tests, examples and build scripts are crates of their own).
pub fn src_root(path: &str) -> &str {
    let dir = path.rsplit_once('/').map_or("", |(d, _)| d);
    match path
        .match_indices("src/")
        .find(|(i, _)| *i == 0 || path.as_bytes()[i - 1] == b'/')
    {
        Some((i, _)) => path[..i + 3].trim_end_matches('/'),
        None => dir,
    }
}

/// The module path of a file below its crate's source root (`a/b.rs`, `a/b/mod.rs` →
/// `a::b`; `lib.rs`, `main.rs` → the root).
fn module_of(path: &str) -> Vec<String> {
    let root = src_root(path);
    let rel = path
        .strip_prefix(root)
        .unwrap_or(path)
        .trim_start_matches('/');
    let stem = rel.strip_suffix(".rs").unwrap_or(rel);
    let mut segs: Vec<String> = stem.split('/').map(str::to_string).collect();
    if matches!(segs.last().map(String::as_str), Some("mod")) {
        segs.pop();
    }
    if segs.len() == 1 && matches!(segs[0].as_str(), "lib" | "main") {
        segs.clear();
    }
    segs
}

fn locate(
    db: &Db<'_>,
    from: &FileCtx,
    path: &str,
    crates: &HashMap<String, String>,
) -> Result<Module> {
    let mut segs = path.split("::").filter(|s| !s.is_empty()).peekable();
    let mut src = src_root(&from.path).to_string();
    let mut module = module_of(&from.path);
    // a path is anchored at a root or a workspace crate; else it names a child module
    let anchored = match segs.peek().copied() {
        Some("crate") => {
            module.clear();
            true
        }
        Some("self" | "super") => true,
        Some(name) if crates.contains_key(name) => {
            src = crates[name].clone();
            module.clear();
            true
        }
        _ => false,
    };
    if anchored {
        segs.next();
    }
    while segs.next_if_eq(&"super").is_some() {
        module.pop();
    }
    let start = module.len();
    module.extend(segs.map(str::to_string));
    // the longest path that is a file; a child module needs at least its own segment
    let floor = start + usize::from(!anchored);
    for k in (floor..=module.len()).rev() {
        let at = |leaf: &str| {
            [src.as_str(), &module[..k].join("/"), leaf]
                .into_iter()
                .filter(|s| !s.is_empty())
                .collect::<Vec<_>>()
                .join("/")
        };
        let files = match k {
            0 => vec![at("lib.rs"), at("main.rs")],
            _ => vec![at("").to_string() + ".rs", at("mod.rs")],
        };
        for f in files {
            if let Some(id) = db.file_at(&f)? {
                return Ok(Module::Files(vec![id], module[k..].join(".")));
            }
        }
    }
    Ok(match anchored {
        true => Module::Missing,
        false => Module::External,
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
    fn module_paths_and_roots() {
        assert_eq!(src_root("rust-core/src/index/mod.rs"), "rust-core/src");
        assert_eq!(src_root("src/lib.rs"), "src");
        assert_eq!(src_root("tests/x.rs"), "tests");
        assert_eq!(module_of("src/a/b.rs"), ["a", "b"]);
        assert_eq!(module_of("src/a/b/mod.rs"), ["a", "b"]);
        assert!(module_of("src/lib.rs").is_empty());
        assert_eq!(
            package_name("[package]\nname = \"my-crate\"\nversion = \"1\"\n").as_deref(),
            Some("my-crate")
        );
        assert_eq!(package_name("[dependencies]\nname = \"x\"\n"), None);
    }

    #[test]
    fn use_forms_bind_names() {
        let imports = [
            imp("use", "crate::a", Some("Item"), Some("I")),
            imp("use", "crate::b", Some("self"), None),
            imp("use", "crate::c", Some("*"), None),
            imp("mod", "child", None, None),
        ];
        let r = Rust::new(Path::new("."));
        assert_eq!(
            r.binding(&imports, "I"),
            Some(Bind::Item {
                module: "crate::a".into(),
                name: "Item".into()
            })
        );
        assert_eq!(
            r.binding(&imports, "b"),
            Some(Bind::Module("crate::b".into()))
        );
        assert_eq!(
            r.binding(&imports, "child"),
            Some(Bind::Module("child".into()))
        );
        assert_eq!(
            r.binding(&imports, "crate"),
            Some(Bind::Module("crate".into()))
        );
        assert_eq!(r.binding(&imports, "c"), None);
        assert!(r.is_wildcard(&imports[2]));
        assert_eq!(r.join("crate::a", "b.C"), "crate::a::b::C");
    }
}
