use chrono::Utc;
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::fs;
use std::path::{Path, PathBuf};
use walkdir::{DirEntry, WalkDir};

const SCANNER_EXCLUDED_DIR_NAMES: &[&str] = &[
    ".git",
    ".hg",
    ".svn",
    ".venv",
    "venv",
    "__pycache__",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".turbo",
    ".next",
    "target",
    "node_modules",
    "dist",
    "build",
    "coverage",
    ".coverage",
    "tmp",
    "vendor",
    "third_party",
    "research",
];

const SCANNER_EXCLUDED_RELATIVE_DIRS: &[&str] = &["backend/data", "frontend/dist"];

const SCANNABLE_SOURCE_EXTENSIONS: &[&str] = &[
    ".bash", ".c", ".cc", ".cpp", ".cjs", ".cs", ".css", ".go", ".h", ".hpp", ".html", ".java",
    ".js", ".jsx", ".kt", ".kts", ".mjs", ".php", ".py", ".rb", ".rs", ".scala", ".sh", ".sql",
    ".swift", ".ts", ".tsx", ".yaml", ".yml", ".zsh",
];

const SCANNABLE_SOURCE_FILENAMES: &[&str] = &["Dockerfile", "Justfile", "Makefile", "Procfile"];

const REPO_MAP_KEY_FILE_PATTERNS: &[(&str, &str)] = &[
    ("AGENTS.md", "guide"),
    ("CONVENTIONS.md", "guide"),
    ("README.md", "guide"),
    ("pyproject.toml", "config"),
    ("package.json", "config"),
    ("tsconfig.json", "config"),
    ("vite.config.ts", "config"),
    ("vite.config.js", "config"),
    ("backend/app/main.py", "entry"),
    ("backend/app/service.py", "entry"),
    ("frontend/src/App.tsx", "entry"),
];

fn repo_map_file_role(relative_path: &str) -> Option<&'static str> {
    if let Some((_, role)) = REPO_MAP_KEY_FILE_PATTERNS
        .iter()
        .find(|(pattern, _)| relative_path == *pattern)
    {
        return Some(role);
    }
    let lowered = relative_path.to_lowercase();
    let is_test = lowered.contains("/tests/")
        || lowered.starts_with("tests/")
        || PathBuf::from(&lowered)
            .file_name()
            .and_then(|name| name.to_str())
            .map(|name| name.starts_with("test_"))
            .unwrap_or(false);
    if is_test {
        return Some("test");
    }
    if should_scan_file(relative_path) {
        return Some("source");
    }
    None
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RepoMapMilestone {
    pub name: &'static str,
    pub outcome: &'static str,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct RustRepoMapDirectoryRecord {
    pub path: String,
    pub file_count: usize,
    pub source_file_count: usize,
    pub test_file_count: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct RustRepoMapFileRecord {
    pub path: String,
    pub role: String,
    pub size_bytes: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustRepoMapSummary {
    pub workspace_id: String,
    pub root_path: String,
    pub total_files: usize,
    pub source_files: usize,
    pub test_files: usize,
    pub top_extensions: BTreeMap<String, usize>,
    pub top_directories: Vec<RustRepoMapDirectoryRecord>,
    pub key_files: Vec<RustRepoMapFileRecord>,
    pub generated_at: String,
}

pub fn initial_repomap_plan() -> Vec<RepoMapMilestone> {
    vec![
        RepoMapMilestone {
            name: "directory_summary",
            outcome: "Port top-directory and key-file summary generation from the Python repo-map logic.",
        },
        RepoMapMilestone {
            name: "path_ranking",
            outcome: "Port issue-aware related-path ranking as a standalone Rust service boundary.",
        },
        RepoMapMilestone {
            name: "symbol_expansion",
            outcome: "Add tree-sitter based symbol summaries inspired by aider's repo map design.",
        },
    ]
}

pub fn build_repo_map(
    root_path: &Path,
    workspace_id: &str,
) -> Result<RustRepoMapSummary, std::io::Error> {
    let mut extension_counts: HashMap<String, usize> = HashMap::new();
    let mut top_level: HashMap<String, RustRepoMapDirectoryRecord> = HashMap::new();
    let mut key_files: Vec<RustRepoMapFileRecord> = Vec::new();
    let mut discovered_key_paths: HashSet<String> = HashSet::new();

    let mut total_files = 0usize;
    let mut source_files = 0usize;
    let mut test_files = 0usize;

    let walker = WalkDir::new(root_path)
        .into_iter()
        .filter_entry(|entry| !should_skip_entry(root_path, entry));

    for entry in walker {
        let entry = match entry {
            Ok(entry) => entry,
            Err(_) => continue,
        };
        if !entry.file_type().is_file() {
            continue;
        }
        let relative = match relative_path(root_path, entry.path()) {
            Some(value) => value,
            None => continue,
        };
        if should_ignore_relative_path(&relative) {
            continue;
        }

        let Some(role) = repo_map_file_role(&relative) else {
            continue;
        };
        total_files += 1;
        let top_dir = relative
            .split('/')
            .next()
            .filter(|_| relative.contains('/'))
            .unwrap_or(".")
            .to_string();

        let is_source = should_scan_file(&relative);
        let is_test = role == "test";

        {
            let entry = top_level.entry(top_dir).or_insert(RustRepoMapDirectoryRecord {
                path: String::new(),
                file_count: 0,
                source_file_count: 0,
                test_file_count: 0,
            });
            if entry.path.is_empty() {
                entry.path = if relative.contains('/') {
                    relative.split('/').next().unwrap_or(".").to_string()
                } else {
                    ".".to_string()
                };
            }
            entry.file_count += 1;
            if is_source {
                entry.source_file_count += 1;
            }
            if is_test {
                entry.test_file_count += 1;
            }
        }

        if is_source {
            source_files += 1;
            let ext_key = PathBuf::from(&relative)
                .extension()
                .and_then(|value| value.to_str())
                .map(|ext| format!(".{ext}"))
                .unwrap_or_else(|| {
                    PathBuf::from(&relative)
                        .file_name()
                        .and_then(|name| name.to_str())
                        .unwrap_or_default()
                        .to_string()
                });
            *extension_counts.entry(ext_key).or_insert(0) += 1;
        }
        if is_test {
            test_files += 1;
        }

        let size_bytes = fs::metadata(entry.path()).ok().map(|meta| meta.len());
        for (pattern, role) in REPO_MAP_KEY_FILE_PATTERNS {
            if relative == *pattern && !discovered_key_paths.contains(&relative) {
                discovered_key_paths.insert(relative.clone());
                key_files.push(RustRepoMapFileRecord {
                    path: relative.clone(),
                    role: (*role).to_string(),
                    size_bytes,
                });
            }
        }
        if is_test && !discovered_key_paths.contains(&relative) && key_files.len() < 12 {
            discovered_key_paths.insert(relative.clone());
            key_files.push(RustRepoMapFileRecord {
                path: relative.clone(),
                role: "test".to_string(),
                size_bytes,
            });
        }
    }

    let mut top_directories: Vec<RustRepoMapDirectoryRecord> = top_level.into_values().collect();
    top_directories.sort_by(|left, right| {
        right
            .source_file_count
            .cmp(&left.source_file_count)
            .then(right.file_count.cmp(&left.file_count))
            .then(left.path.cmp(&right.path))
    });
    top_directories.truncate(8);

    let mut extension_items: Vec<(String, usize)> = extension_counts.into_iter().collect();
    extension_items.sort_by(|left, right| right.1.cmp(&left.1).then(left.0.cmp(&right.0)));
    extension_items.truncate(8);
    let top_extensions = extension_items.into_iter().collect();

    Ok(RustRepoMapSummary {
        workspace_id: workspace_id.to_string(),
        root_path: root_path.to_string_lossy().to_string(),
        total_files,
        source_files,
        test_files,
        top_extensions,
        top_directories,
        key_files: key_files.into_iter().take(12).collect(),
        generated_at: Utc::now().to_rfc3339(),
    })
}

fn should_skip_entry(root_path: &Path, entry: &DirEntry) -> bool {
    if entry.depth() == 0 {
        return false;
    }
    let Some(relative) = relative_path(root_path, entry.path()) else {
        return true;
    };
    should_ignore_relative_path(&relative)
}

fn relative_path(root_path: &Path, path: &Path) -> Option<String> {
    let relative = path.strip_prefix(root_path).ok()?;
    Some(relative.to_string_lossy().replace('\\', "/"))
}

fn should_ignore_relative_path(relative_path: &str) -> bool {
    let normalized_parts: Vec<&str> = relative_path
        .split('/')
        .filter(|part| !part.is_empty() && *part != ".")
        .collect();
    if normalized_parts.is_empty() {
        return false;
    }
    if normalized_parts
        .iter()
        .any(|part| SCANNER_EXCLUDED_DIR_NAMES.contains(part))
    {
        return true;
    }
    let normalized = normalized_parts.join("/");
    SCANNER_EXCLUDED_RELATIVE_DIRS
        .iter()
        .any(|excluded| normalized == *excluded || normalized.starts_with(&format!("{excluded}/")))
}

fn should_scan_file(relative_path: &str) -> bool {
    if should_ignore_relative_path(relative_path) {
        return false;
    }
    let path = PathBuf::from(relative_path);
    let file_name = match path.file_name().and_then(|name| name.to_str()) {
        Some(value) => value,
        None => return false,
    };
    if SCANNABLE_SOURCE_FILENAMES.contains(&file_name) {
        return true;
    }
    let ext = path.extension().and_then(|value| value.to_str()).unwrap_or_default();
    SCANNABLE_SOURCE_EXTENSIONS.contains(&format!(".{ext}").as_str())
}

#[cfg(test)]
mod tests {
    use super::build_repo_map;
    use std::fs;
    use std::path::Path;

    fn write(path: &Path, content: &str) {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(path, content).unwrap();
    }

    #[test]
    fn repomap_ignores_excluded_dirs_and_collects_summary() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();

        write(&root.join("AGENTS.md"), "# instructions\n");
        write(&root.join("src/app.py"), "print('ok')\n");
        write(&root.join("tests/test_app.py"), "def test_ok():\n    assert True\n");
        write(&root.join("backend/data/generated.py"), "print('ignore')\n");
        write(&root.join("research/notes.py"), "print('ignore')\n");

        let summary = build_repo_map(root, "workspace-1").unwrap();

        assert_eq!(summary.total_files, 3);
        assert_eq!(summary.source_files, 2);
        assert_eq!(summary.test_files, 1);
        assert!(summary.key_files.iter().any(|item| item.path == "AGENTS.md"));
        assert!(summary.top_directories.iter().any(|item| item.path == "src"));
    }

    #[test]
    fn repomap_counts_only_repo_understanding_relevant_files() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();

        write(&root.join("README.md"), "# readme\n");
        write(&root.join("src/app.py"), "print('ok')\n");
        write(&root.join("tests/test_app.py"), "def test_ok():\n    assert True\n");
        write(&root.join("docs/notes.txt"), "not indexed\n");
        write(&root.join("assets/logo.png"), "png");
        write(&root.join("target/debug.bin"), "bin");

        let summary = build_repo_map(root, "workspace-2").unwrap();

        assert_eq!(summary.total_files, 3);
        assert_eq!(summary.source_files, 2);
        assert_eq!(summary.test_files, 1);
        assert!(summary.key_files.iter().any(|item| item.path == "README.md"));
        assert!(summary.key_files.iter().any(|item| item.path == "tests/test_app.py"));
    }
}
