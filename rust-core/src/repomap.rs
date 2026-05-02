use chrono::Utc;
use regex::Regex;
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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustRepoChangeRecord {
    pub path: String,
    pub status: String,
    pub scope: String,
    pub previous_path: Option<String>,
    #[serde(default)]
    pub staged: bool,
    #[serde(default)]
    pub unstaged: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustChangedSymbolRecord {
    pub path: String,
    pub symbol: String,
    pub kind: String,
    pub line_start: Option<usize>,
    pub line_end: Option<usize>,
    pub evidence_source: String,
    pub semantic_status: Option<String>,
    pub selection_reason: String,
    pub change_scopes: Vec<String>,
    pub change_statuses: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustImpactPathRecord {
    pub path: String,
    pub reason: String,
    pub derivation_source: String,
    pub score: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustSemanticImpactReport {
    pub workspace_id: String,
    pub changed_symbols: Vec<RustChangedSymbolRecord>,
    pub likely_affected_files: Vec<RustImpactPathRecord>,
    pub likely_affected_tests: Vec<RustImpactPathRecord>,
    pub derivation_source: String,
    pub warnings: Vec<String>,
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

pub fn build_semantic_impact(
    root_path: &Path,
    workspace_id: &str,
    changes: &[RustRepoChangeRecord],
) -> Result<RustSemanticImpactReport, std::io::Error> {
    let symbols = derive_changed_symbols(root_path, changes);
    let changed_paths = changes
        .iter()
        .filter(|item| item.status != "deleted")
        .map(|item| item.path.clone())
        .collect::<Vec<_>>();
    let (affected_files, affected_tests) =
        rank_affected_paths(root_path, &changed_paths, &symbols)?;
    let mut warnings = Vec::new();
    if !changed_paths.is_empty() && symbols.is_empty() {
        warnings.push(
            "Rust semantic core did not derive symbol-level context for the changed paths."
                .to_string(),
        );
    }
    Ok(RustSemanticImpactReport {
        workspace_id: workspace_id.to_string(),
        changed_symbols: symbols,
        likely_affected_files: affected_files,
        likely_affected_tests: affected_tests,
        derivation_source: "rust_semantic_core".to_string(),
        warnings,
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

fn derive_changed_symbols(
    root_path: &Path,
    changes: &[RustRepoChangeRecord],
) -> Vec<RustChangedSymbolRecord> {
    let mut symbols: Vec<RustChangedSymbolRecord> = Vec::new();
    let mut seen: HashMap<String, usize> = HashMap::new();
    for change in changes {
        if change.status == "deleted" {
            continue;
        }
        for mut symbol in extract_symbols_from_file(root_path, &change.path) {
            let key = format!("{}\0{}\0{}", symbol.path, symbol.symbol, symbol.kind);
            if let Some(index) = seen.get(&key).copied() {
                push_unique(&mut symbols[index].change_scopes, change.scope.clone(), 8);
                push_unique(
                    &mut symbols[index].change_statuses,
                    change.status.clone(),
                    8,
                );
                continue;
            }
            symbol.change_scopes = vec![change.scope.clone()];
            symbol.change_statuses = vec![change.status.clone()];
            seen.insert(key, symbols.len());
            symbols.push(symbol);
        }
    }
    symbols
}

fn extract_symbols_from_file(root_path: &Path, relative_path: &str) -> Vec<RustChangedSymbolRecord> {
    if !should_scan_file(relative_path) {
        return Vec::new();
    }
    let content = match fs::read_to_string(root_path.join(relative_path)) {
        Ok(value) => value,
        Err(_) => return Vec::new(),
    };
    let patterns = [
        (Regex::new(r"^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(").unwrap(), "function"),
        (Regex::new(r"^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b").unwrap(), "class"),
        (
            Regex::new(r"^\s*func\s+(?:\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(")
                .unwrap(),
            "function",
        ),
        (
            Regex::new(r"^\s*(?:export\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\b").unwrap(),
            "function",
        ),
        (
            Regex::new(r"^\s*(?:export\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)\b").unwrap(),
            "class",
        ),
        (
            Regex::new(r"^\s*(?:export\s+)?(?:interface|type)\s+([A-Za-z_][A-Za-z0-9_]*)\b")
                .unwrap(),
            "type",
        ),
        (
            Regex::new(r"^\s*(?:pub\s+)?(?:struct|enum|trait)\s+([A-Za-z_][A-Za-z0-9_]*)\b")
                .unwrap(),
            "type",
        ),
        (
            Regex::new(r"^\s*(?:pub\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(").unwrap(),
            "function",
        ),
    ];
    let mut out = Vec::new();
    for (index, line) in content.lines().enumerate() {
        for (pattern, kind) in &patterns {
            let Some(captures) = pattern.captures(line) else {
                continue;
            };
            let Some(symbol) = captures.get(1) else {
                continue;
            };
            let line_no = index + 1;
            out.push(RustChangedSymbolRecord {
                path: relative_path.replace('\\', "/"),
                symbol: symbol.as_str().to_string(),
                kind: (*kind).to_string(),
                line_start: Some(line_no),
                line_end: Some(line_no),
                evidence_source: "rust_semantic_core".to_string(),
                semantic_status: Some("on_demand".to_string()),
                selection_reason:
                    "Rust semantic core extracted this symbol from a changed path.".to_string(),
                change_scopes: Vec::new(),
                change_statuses: Vec::new(),
            });
            break;
        }
        if out.len() >= 48 {
            break;
        }
    }
    out
}

fn rank_affected_paths(
    root_path: &Path,
    changed_paths: &[String],
    symbols: &[RustChangedSymbolRecord],
) -> Result<(Vec<RustImpactPathRecord>, Vec<RustImpactPathRecord>), std::io::Error> {
    let mut tokens = Vec::new();
    for path in changed_paths {
        tokens.extend(path_tokens(path));
    }
    for symbol in symbols {
        push_unique(&mut tokens, symbol.symbol.to_lowercase(), 64);
    }
    let mut files = Vec::new();
    let mut tests = Vec::new();
    let changed: HashSet<&str> = changed_paths.iter().map(|item| item.as_str()).collect();
    for path in candidate_source_paths(root_path, 180) {
        if changed.contains(path.as_str()) {
            continue;
        }
        let score = path_score(&path, &tokens);
        if score == 0 {
            continue;
        }
        let is_test = repo_map_file_role(&path) == Some("test")
            || path.to_lowercase().contains("test")
            || path.to_lowercase().ends_with(".spec.ts");
        let record = RustImpactPathRecord {
            path,
            reason: "Rust semantic core matched path terms and changed-symbol terms.".to_string(),
            derivation_source: "rust_semantic_core".to_string(),
            score,
        };
        if is_test {
            tests.push(record);
        } else {
            files.push(record);
        }
    }
    sort_impact_paths(&mut files);
    sort_impact_paths(&mut tests);
    files.truncate(10);
    tests.truncate(10);
    Ok((files, tests))
}

fn candidate_source_paths(root_path: &Path, limit: usize) -> Vec<String> {
    let mut out = Vec::new();
    let walker = WalkDir::new(root_path)
        .into_iter()
        .filter_entry(|entry| !should_skip_entry(root_path, entry));
    for entry in walker {
        if out.len() >= limit {
            break;
        }
        let Ok(entry) = entry else {
            continue;
        };
        if !entry.file_type().is_file() {
            continue;
        }
        let Some(relative) = relative_path(root_path, entry.path()) else {
            continue;
        };
        if should_scan_file(&relative) {
            out.push(relative);
        }
    }
    out
}

fn path_tokens(path: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut current = String::new();
    for ch in path.chars() {
        if ch.is_ascii_alphanumeric() || ch == '_' {
            current.push(ch.to_ascii_lowercase());
        } else {
            if current.len() >= 3 {
                push_unique(&mut out, current.clone(), 16);
            }
            current.clear();
        }
    }
    if current.len() >= 3 {
        push_unique(&mut out, current, 16);
    }
    out
}

fn path_score(path: &str, tokens: &[String]) -> i32 {
    let lower = path.to_lowercase();
    let mut score = 0;
    for token in tokens {
        if token.len() >= 3 && lower.contains(token) {
            score += 2;
        }
    }
    if repo_map_file_role(path) == Some("test") {
        score += 1;
    }
    score
}

fn sort_impact_paths(items: &mut [RustImpactPathRecord]) {
    items.sort_by(|left, right| {
        right
            .score
            .cmp(&left.score)
            .then(left.path.cmp(&right.path))
    });
}

fn push_unique(items: &mut Vec<String>, value: String, limit: usize) {
    if value.trim().is_empty() || items.iter().any(|item| item == &value) {
        return;
    }
    if items.len() < limit {
        items.push(value);
    }
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

    #[test]
    fn semantic_impact_extracts_symbols_and_ranks_related_paths() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();

        write(
            &root.join("src/app.py"),
            "class ExportService:\n    def export_summary(self):\n        return 'ok'\n",
        );
        write(
            &root.join("tests/test_export.py"),
            "def test_export_summary():\n    assert True\n",
        );

        let report = super::build_semantic_impact(
            root,
            "workspace-3",
            &[super::RustRepoChangeRecord {
                path: "src/app.py".to_string(),
                status: "modified".to_string(),
                scope: "working_tree".to_string(),
                previous_path: None,
                staged: false,
                unstaged: true,
            }],
        )
        .unwrap();

        assert!(report
            .changed_symbols
            .iter()
            .any(|item| item.symbol == "ExportService"));
        assert!(report
            .likely_affected_tests
            .iter()
            .any(|item| item.path == "tests/test_export.py"));
        assert_eq!(report.derivation_source, "rust_semantic_core");
    }
}
