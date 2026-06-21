use chrono::Utc;
use serde::Serialize;
use std::collections::HashMap;
use std::path::Path;
use std::process::Command;

use crate::symbolgraph;

#[derive(Debug, Clone, Serialize)]
pub struct Subsystem {
    pub name: String,
    pub file_count: usize,
    pub symbol_count: usize,
    pub internal_edges: usize,
    pub external_edges: usize,
    pub cohesion: f64,
}

#[derive(Debug, Clone, Serialize)]
pub struct OwnerCount {
    pub name: String,
    pub commits: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct OwnerSuggestion {
    pub path: String,
    pub owners: Vec<OwnerCount>,
    pub generated_at: String,
}

fn subsystem_name(path: &str) -> String {
    path.split('/').next().map_or_else(
        || "(root)".to_string(),
        |first| {
            if first.is_empty() {
                "(root)".to_string()
            } else {
                first.to_string()
            }
        },
    )
}

pub fn build_subsystems(root: &Path, workspace_id: &str) -> Vec<Subsystem> {
    let graph = symbolgraph::build_symbol_graph(root, workspace_id);
    let mut file_to_subsystem: HashMap<String, String> = HashMap::new();
    let mut totals: HashMap<String, (usize, usize, usize, usize)> = HashMap::new(); // file_count, symbol_count, internal, external

    for file in graph.files {
        let subsystem = subsystem_name(&file.path);
        file_to_subsystem.insert(file.path.clone(), subsystem.clone());
        let entry = totals.entry(subsystem).or_insert((0, 0, 0, 0));
        entry.0 += 1;
        entry.1 += file.symbol_count;
    }

    for edge in graph.edges {
        let from = file_to_subsystem.get(&edge.from_path);
        let to = file_to_subsystem.get(&edge.to_path);
        match (from, to) {
            (Some(from_sub), Some(to_sub)) => {
                if from_sub == to_sub {
                    if let Some(entry) = totals.get_mut(from_sub) {
                        entry.2 += 1;
                    }
                } else {
                    if let Some(entry) = totals.get_mut(from_sub) {
                        entry.3 += 1;
                    }
                    if let Some(entry) = totals.get_mut(to_sub) {
                        entry.3 += 1;
                    }
                }
            }
            _ => continue,
        }
    }

    let mut subsystems: Vec<Subsystem> = totals
        .into_iter()
        .map(
            |(name, (file_count, symbol_count, internal_edges, external_edges))| {
                let total_edges = internal_edges + external_edges;
                let cohesion = if total_edges == 0 {
                    0.0
                } else {
                    internal_edges as f64 / total_edges as f64
                };
                Subsystem {
                    name,
                    file_count,
                    symbol_count,
                    internal_edges,
                    external_edges,
                    cohesion,
                }
            },
        )
        .collect();

    subsystems.sort_by(|a, b| {
        b.file_count
            .cmp(&a.file_count)
            .then_with(|| a.name.cmp(&b.name))
    });
    subsystems
}

pub fn likely_owners(root: &Path, path: &str) -> OwnerSuggestion {
    let output = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["log", "--format=%an", "--", path])
        .output();

    let owners = match output {
        Ok(output) if output.status.success() => {
            let mut counts: HashMap<String, usize> = HashMap::new();
            let stdout = String::from_utf8_lossy(&output.stdout);
            for author in stdout.lines().filter(|l| !l.trim().is_empty()) {
                *counts.entry(author.to_string()).or_insert(0) += 1;
            }
            let mut owners: Vec<OwnerCount> = counts
                .into_iter()
                .map(|(name, commits)| OwnerCount { name, commits })
                .collect();
            owners.sort_by(|a, b| b.commits.cmp(&a.commits).then_with(|| a.name.cmp(&b.name)));
            owners.truncate(5);
            owners
        }
        _ => Vec::new(),
    };

    OwnerSuggestion {
        path: path.to_string(),
        owners,
        generated_at: Utc::now().to_rfc3339(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    fn temp_repo() -> TempDir {
        let repo = TempDir::new().unwrap();
        std::fs::create_dir_all(repo.path().join("core")).unwrap();
        std::fs::write(
            repo.path().join("core/a.rs"),
            "pub fn core_fn() -> i32 { 1 }\n",
        )
        .unwrap();
        std::fs::write(
            repo.path().join("core/b.rs"),
            "pub fn core_extra() -> i32 { 2 }\n",
        )
        .unwrap();
        std::fs::write(
            repo.path().join("root.rs"),
            "fn root() { let _ = crate::core_fn(); }\n",
        )
        .unwrap();

        let init = Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["init", "-q"])
            .output()
            .unwrap();
        assert!(init.status.success());
        Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["config", "user.email", "t@t"])
            .output()
            .unwrap();
        Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["config", "user.name", "t"])
            .output()
            .unwrap();
        Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["add", "-A"])
            .output()
            .unwrap();
        let commit = Command::new("git")
            .arg("-C")
            .arg(repo.path())
            .args(["commit", "-qm", "add files"])
            .output()
            .unwrap();
        assert!(commit.status.success());
        repo
    }

    #[test]
    fn builds_core_subsystem_from_file_paths() {
        let repo = temp_repo();
        let subsystems = build_subsystems(repo.path(), "ws");
        let core = subsystems
            .iter()
            .find(|s| s.name == "core")
            .expect("core subsystem");
        assert_eq!(core.file_count, 2);
    }

    #[test]
    fn likely_owners_reports_t() {
        let repo = temp_repo();
        let suggestion = likely_owners(repo.path(), "core/a.rs");
        assert!(!suggestion.owners.is_empty());
        assert!(suggestion.owners.iter().any(|owner| owner.name == "t"));
    }
}
