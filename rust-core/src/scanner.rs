use regex::Regex;
use serde::{Deserialize, Serialize};
use sha1::{Digest, Sha1};
use std::collections::BTreeMap;
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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ScannerMilestone {
    pub name: &'static str,
    pub outcome: &'static str,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct RustEvidenceRef {
    pub path: String,
    pub line: usize,
    pub excerpt: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct RustDiscoverySignal {
    pub signal_id: String,
    pub kind: String,
    pub severity: String,
    pub title: String,
    pub summary: String,
    pub file_path: String,
    pub line: usize,
    pub evidence: Vec<RustEvidenceRef>,
    pub tags: Vec<String>,
    pub fingerprint: String,
}

#[derive(Debug, Clone)]
struct SignalRule {
    kind: &'static str,
    severity: &'static str,
    title: &'static str,
    patterns: Vec<Regex>,
}

pub fn initial_scanner_plan() -> Vec<ScannerMilestone> {
    vec![
        ScannerMilestone {
            name: "ledger_ingestion",
            outcome: "Mirror Bugs_*.md parsing and verdict merge behavior from the current Python scanner.",
        },
        ScannerMilestone {
            name: "signal_detection",
            outcome: "Port low-noise signal detection with the same excluded directory strategy.",
        },
        ScannerMilestone {
            name: "parity_fixtures",
            outcome: "Compare Python and Rust scanner outputs against backend test fixtures before cutover.",
        },
    ]
}

pub fn scan_repo_signals(root_path: &Path) -> Result<Vec<RustDiscoverySignal>, std::io::Error> {
    let mut deduped: BTreeMap<String, RustDiscoverySignal> = BTreeMap::new();
    let rules = signal_rules();

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
        if !should_scan_file(&relative) {
            continue;
        }
        let content = match fs::read_to_string(entry.path()) {
            Ok(content) => content,
            Err(_) => continue,
        };
        for (index, line) in content.lines().enumerate() {
            let line_number = index + 1;
            for rule in &rules {
                for pattern in &rule.patterns {
                    if !content_matches_signal(rule.kind, pattern, line) {
                        continue;
                    }
                    let trimmed = line.trim();
                    let fingerprint = fingerprint_for_signal(rule.kind, &relative, line_number, trimmed);
                    deduped.insert(
                        fingerprint.clone(),
                        RustDiscoverySignal {
                            signal_id: format!("sig_{}", &fingerprint[..12]),
                            kind: rule.kind.to_string(),
                            severity: rule.severity.to_string(),
                            title: rule.title.to_string(),
                            summary: trimmed.chars().take(280).collect(),
                            file_path: relative.clone(),
                            line: line_number,
                            evidence: vec![RustEvidenceRef {
                                path: relative.clone(),
                                line: line_number,
                                excerpt: trimmed.to_string(),
                            }],
                            tags: vec![rule.kind.to_string(), "auto-discovery".to_string()],
                            fingerprint,
                        },
                    );
                }
            }
        }
    }

    let mut signals: Vec<RustDiscoverySignal> = deduped.into_values().collect();
    signals.sort_by(|left, right| {
        left.severity
            .cmp(&right.severity)
            .then(left.file_path.cmp(&right.file_path))
            .then(left.line.cmp(&right.line))
    });
    Ok(signals)
}

fn signal_rules() -> Vec<SignalRule> {
    vec![
        SignalRule {
            kind: "annotation",
            severity: "P2",
            title: "Backlog annotation in code",
            patterns: vec![
                Regex::new(r"(?:#|//|/\*|\*)\s*(?:TODO|FIXME|BUG|HACK|XXX)\b").unwrap(),
                Regex::new(r"<!--\s*(?:TODO|FIXME|BUG|HACK|XXX)\b").unwrap(),
            ],
        },
        SignalRule {
            kind: "exception_swallow",
            severity: "P1",
            title: "Swallowed generic exception",
            patterns: vec![Regex::new(r"except Exception:\s*(pass|continue|return None|return)").unwrap()],
        },
        SignalRule {
            kind: "not_implemented",
            severity: "P2",
            title: "Not implemented code path",
            patterns: vec![
                Regex::new(r"^\s*raise\s+NotImplemented(?:Error)?(?:\(|$)").unwrap(),
                Regex::new(r":\s*raise\s+NotImplemented(?:Error)?(?:\(|$)").unwrap(),
            ],
        },
        SignalRule {
            kind: "test_marker",
            severity: "P3",
            title: "Deferred or skipped test coverage",
            patterns: vec![
                Regex::new(r"^\s*@?pytest\.mark\.(?:xfail|skip)\b").unwrap(),
                Regex::new(r"^\s*@unittest\.skip\b").unwrap(),
                Regex::new(r"\b(?:it|test)\.skip\(").unwrap(),
            ],
        },
    ]
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

fn content_matches_signal(kind: &str, pattern: &Regex, content: &str) -> bool {
    let matches: Vec<_> = pattern.find_iter(content).collect();
    if matches.is_empty() {
        return false;
    }
    if kind != "annotation" {
        return true;
    }
    matches.into_iter().any(|capture| {
        capture.start() == 0
            || content
                .chars()
                .nth(capture.start().saturating_sub(1))
                .map(char::is_whitespace)
                .unwrap_or(false)
    })
}

fn fingerprint_for_signal(kind: &str, file_path: &str, line: usize, content: &str) -> String {
    let mut hasher = Sha1::new();
    hasher.update(format!("{kind}:{file_path}:{line}:{content}"));
    format!("{:x}", hasher.finalize())
}

#[cfg(test)]
mod tests {
    use super::scan_repo_signals;
    use std::fs;
    use std::path::Path;

    fn write(path: &Path, content: &str) {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(path, content).unwrap();
    }

    #[test]
    fn scanner_ignores_excluded_dirs_and_docs() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();
        fs::create_dir_all(root.join("src")).unwrap();
        fs::create_dir_all(root.join("backend/data")).unwrap();
        fs::create_dir_all(root.join("research")).unwrap();
        fs::create_dir_all(root.join("docs")).unwrap();

        write(&root.join("src/app.py"), "# TODO: real signal\n");
        write(&root.join("backend/data/generated.py"), "# TODO: generated signal\n");
        write(&root.join("research/notes.py"), "# TODO: research signal\n");
        write(&root.join("docs/ARCHITECTURE.md"), "- TODO: documentation note\n");

        let signals = scan_repo_signals(root).unwrap();
        assert_eq!(signals.len(), 1);
        assert_eq!(signals[0].file_path, "src/app.py");
        assert_eq!(signals[0].kind, "annotation");
    }

    #[test]
    fn scanner_finds_actionable_matches_not_pattern_literals() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();
        fs::create_dir_all(root.join("src")).unwrap();
        fs::create_dir_all(root.join("tests")).unwrap();

        write(
            &root.join("src/sample.py"),
            concat!(
                "BUG_HEADER_RE = re.compile(r\"^###\\\\s+(P\\\\\\\\d...)$\")\n",
                "patterns = [r\"NotImplementedError|raise NotImplemented\", r\"xfail|skip\\\\\\\\(|todo\"]\n",
                "# TODO: real backlog item\n",
                "def pending_feature():\n",
                "    raise NotImplementedError()\n"
            ),
        );
        write(
            &root.join("tests/test_sample.py"),
            concat!(
                "import pytest\n",
                "@pytest.mark.skip(reason=\"later\")\n",
                "def test_pending():\n",
                "    assert True\n"
            ),
        );

        let signals = scan_repo_signals(root).unwrap();
        let observed: Vec<(String, String, usize)> = signals
            .into_iter()
            .map(|signal| (signal.kind, signal.file_path, signal.line))
            .collect();
        assert_eq!(
            observed,
            vec![
                ("annotation".to_string(), "src/sample.py".to_string(), 3),
                ("not_implemented".to_string(), "src/sample.py".to_string(), 5),
                ("test_marker".to_string(), "tests/test_sample.py".to_string(), 2),
            ]
        );
    }
}
