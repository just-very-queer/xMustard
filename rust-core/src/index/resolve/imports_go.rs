//! Go import semantics: an import path names a package directory inside a module of
//! the repository (found through the `go.mod` files above the indexed Go files), and
//! its qualifier is the alias or the path's last element (the wildcard leaf). A dot
//! import makes the package's exported names visible unqualified; a blank import binds
//! nothing. Paths outside every module are external (standard library, dependencies).

use std::collections::{BTreeSet, HashMap};
use std::path::{Path, PathBuf};

use rusqlite::Connection;

use super::scope::Import;

pub enum Qualifier<'i> {
    Name(&'i str),
    Dot,
    Blank,
}

/// How an import binds names in the importing file.
pub fn qualifier(imp: &Import) -> Qualifier<'_> {
    match imp.alias.as_deref() {
        Some(".") => Qualifier::Dot,
        Some("_") => Qualifier::Blank,
        Some(a) => Qualifier::Name(a),
        None => Qualifier::Name(leaf(&imp.module)),
    }
}

/// The package name a path's importers use by default: its last element, skipping a
/// major-version suffix (`example.com/m/v2` → `m`).
fn leaf(path: &str) -> &str {
    let mut parts = path.rsplit('/');
    let last = parts.next().unwrap_or(path);
    let is_major =
        last.len() > 1 && last.starts_with('v') && last[1..].bytes().all(|b| b.is_ascii_digit());
    match (is_major, parts.next()) {
        (true, Some(prev)) => prev,
        _ => last,
    }
}

/// Module roots of the repository, discovered once per run.
pub struct Modules {
    root: PathBuf,
    /// module path → directory (repo-relative, "" for the root), longest paths first.
    by_path: Option<Vec<(String, String)>>,
}

impl Modules {
    pub fn new(root: &Path) -> Modules {
        Modules {
            root: root.to_path_buf(),
            by_path: None,
        }
    }

    /// The repo directory an import path names, if it is inside one of the modules.
    pub fn package_dir(
        &mut self,
        conn: &Connection,
        import: &str,
    ) -> rusqlite::Result<Option<String>> {
        if self.by_path.is_none() {
            self.by_path = Some(self.discover(conn)?);
        }
        let found = self.by_path.iter().flatten().find_map(|(module, dir)| {
            let rest = import.strip_prefix(module.as_str())?;
            let rest = match rest {
                "" => "",
                r => r.strip_prefix('/')?,
            };
            Some(match (dir.as_str(), rest) {
                (d, "") => d.to_string(),
                ("", r) => r.to_string(),
                (d, r) => format!("{d}/{r}"),
            })
        });
        Ok(found)
    }

    /// `go.mod` files in the directories of indexed Go files and their ancestors.
    fn discover(&self, conn: &Connection) -> rusqlite::Result<Vec<(String, String)>> {
        let mut dirs: BTreeSet<String> = BTreeSet::new();
        {
            let mut st = conn.prepare("SELECT path FROM files WHERE lang = 'go'")?;
            let mut rows = st.query([])?;
            while let Some(r) = rows.next()? {
                let path: String = r.get(0)?;
                let mut d = path.rsplit_once('/').map_or("", |(d, _)| d).to_string();
                loop {
                    if !dirs.insert(d.clone()) {
                        break; // this directory and its ancestors are known
                    }
                    match d.rsplit_once('/') {
                        Some((parent, _)) => d = parent.to_string(),
                        None if !d.is_empty() => d = String::new(),
                        None => break,
                    }
                }
            }
        }
        let mut seen: HashMap<String, String> = HashMap::new();
        for dir in &dirs {
            let rel = if dir.is_empty() {
                "go.mod".to_string()
            } else {
                format!("{dir}/go.mod")
            };
            if let Ok(text) = crate::symbolgraph::read_repo_file_beneath(&self.root, &rel)
                && let Some(module) = module_path(&text)
            {
                seen.entry(module).or_insert_with(|| dir.clone());
            }
        }
        let mut out: Vec<(String, String)> = seen.into_iter().collect();
        out.sort_by(|a, b| b.0.len().cmp(&a.0.len()).then_with(|| a.0.cmp(&b.0)));
        Ok(out)
    }
}

/// The `module` directive of a go.mod file.
fn module_path(gomod: &str) -> Option<String> {
    gomod.lines().find_map(|l| {
        let rest = l.trim().strip_prefix("module")?;
        let m = rest.trim().trim_matches('"');
        (rest.starts_with(char::is_whitespace) && !m.is_empty()).then(|| m.to_string())
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn leaf_and_module_directive() {
        assert_eq!(leaf("net/http"), "http");
        assert_eq!(leaf("example.com/m/v2"), "m");
        assert_eq!(leaf("fmt"), "fmt");
        assert_eq!(
            module_path("// c\nmodule example.com/x\n\ngo 1.22\n").as_deref(),
            Some("example.com/x")
        );
        assert_eq!(module_path("modulex y\n"), None);
    }
}
