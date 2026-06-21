use serde::{Deserialize, Serialize};
use std::path::Path;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticPatternMatch {
    pub path: String,
    pub language: Option<String>,
    pub line_start: Option<usize>,
    pub line_end: Option<usize>,
    pub column_start: Option<usize>,
    pub column_end: Option<usize>,
    pub matched_text: String,
    pub context_lines: Option<String>,
    pub meta_variables: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct AstGrepResult {
    pub matches: Vec<SemanticPatternMatch>,
    pub binary: Option<String>,
    pub error: Option<String>,
    pub truncated: bool,
}

fn find_binary(name: &str) -> Option<String> {
    let output = std::process::Command::new("which")
        .arg(name)
        .output()
        .ok()?;
    if output.status.success() {
        let path = String::from_utf8_lossy(&output.stdout).trim().to_string();
        if path.is_empty() { None } else { Some(path) }
    } else {
        None
    }
}

fn relative_match_path(root: &Path, file_path: &str) -> String {
    let candidate = Path::new(file_path);
    if candidate.is_absolute() {
        let root_resolved = root.canonicalize();
        let candidate_resolved = candidate.canonicalize();
        if let (Ok(root_r), Ok(cand_r)) = (root_resolved, candidate_resolved) {
            if let Ok(rel) = cand_r.strip_prefix(&root_r) {
                return rel.to_string_lossy().replace('\\', "/");
            }
        }
        candidate.to_string_lossy().replace('\\', "/")
    } else {
        let s = file_path.strip_prefix("./").unwrap_or(file_path);
        s.to_string().replace('\\', "/")
    }
}

fn one_based_index(payload: &serde_json::Value, key: &str) -> Option<usize> {
    payload.get(key)?.as_u64().map(|v| v as usize + 1)
}

fn normalize_ast_grep_language(value: &serde_json::Value) -> Option<String> {
    let s = value.as_str()?;
    if s.is_empty() {
        None
    } else {
        Some(s.to_lowercase())
    }
}

pub fn parse_ast_grep_line(root: &Path, line: &str) -> Option<SemanticPatternMatch> {
    let item: serde_json::Value = serde_json::from_str(line).ok()?;
    let file_path = item.get("file")?.as_str()?;
    let text = item.get("text")?.as_str()?;

    let relative_path = relative_match_path(root, file_path);

    let range = item.get("range").and_then(|v| v.as_object());
    let start = range.and_then(|r| r.get("start"));
    let end = range.and_then(|r| r.get("end"));

    let line_start = start.and_then(|s| one_based_index(s, "line"));
    let line_end = end.and_then(|e| one_based_index(e, "line"));
    let column_start = start.and_then(|s| one_based_index(s, "column"));
    let column_end = end.and_then(|e| one_based_index(e, "column"));

    let language = item.get("language").and_then(normalize_ast_grep_language);

    let context_lines = item
        .get("lines")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());

    let meta_variables = item
        .get("metaVariables")
        .and_then(|v| v.get("single"))
        .and_then(|v| v.as_object())
        .map(|obj| {
            let mut keys: Vec<String> = obj.keys().cloned().collect();
            keys.sort();
            keys
        })
        .unwrap_or_default();

    Some(SemanticPatternMatch {
        path: relative_path,
        language,
        line_start,
        line_end,
        column_start,
        column_end,
        matched_text: text.to_string(),
        context_lines,
        meta_variables,
    })
}

pub fn run_ast_grep_query(
    root: &Path,
    pattern: &str,
    language: Option<&str>,
    path_glob: Option<&str>,
    limit: usize,
) -> AstGrepResult {
    let binary = find_binary("sg").or_else(|| find_binary("ast-grep"));

    let Some(ref bin) = binary else {
        return AstGrepResult {
            matches: vec![],
            binary: None,
            error: Some("ast-grep binary is not installed on this machine.".to_string()),
            truncated: false,
        };
    };

    let mut command = std::process::Command::new(bin);
    command
        .arg("run")
        .arg("--pattern")
        .arg(pattern)
        .arg("--json=stream");

    if let Some(lang) = language {
        command.arg("--lang").arg(lang);
    }
    if let Some(glob) = path_glob {
        command.arg("--globs").arg(glob);
    }
    command.arg(root);

    let output = match command.output() {
        Ok(o) => o,
        Err(_) => {
            return AstGrepResult {
                matches: vec![],
                binary: Some(bin.clone()),
                error: Some("ast-grep binary is not available.".to_string()),
                truncated: false,
            };
        }
    };

    let exit_ok = output
        .status
        .code()
        .map(|c| c == 0 || c == 1)
        .unwrap_or(false);
    if !exit_ok {
        let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();
        let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
        let error_msg = if stderr.is_empty() {
            if stdout.is_empty() {
                format!("ast-grep exited with code {}", output.status)
            } else {
                stdout
            }
        } else {
            stderr
        };
        return AstGrepResult {
            matches: vec![],
            binary: Some(bin.clone()),
            error: Some(error_msg),
            truncated: false,
        };
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut matches: Vec<SemanticPatternMatch> = Vec::new();
    let mut truncated = false;

    for raw_line in stdout.lines() {
        let raw_line = raw_line.trim();
        if raw_line.is_empty() {
            continue;
        }
        if let Some(m) = parse_ast_grep_line(root, raw_line) {
            matches.push(m);
            if matches.len() >= limit {
                truncated = true;
                break;
            }
        }
    }

    AstGrepResult {
        matches,
        binary: Some(bin.clone()),
        error: None,
        truncated,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_ast_grep_line_parses_valid_json() {
        let json = r#"{"file":"src/lib.rs","text":"fn x(){}","language":"Rust","range":{"start":{"line":0,"column":2},"end":{"line":4,"column":7}},"lines":"fn x(){}","metaVariables":{"single":{"B":{},"A":{}}}}"#;
        let root = Path::new(".");
        let result = parse_ast_grep_line(root, json).expect("should parse valid JSON line");

        assert_eq!(result.path, "src/lib.rs");
        assert_eq!(result.line_start, Some(1));
        assert_eq!(result.column_start, Some(3));
        assert_eq!(result.line_end, Some(5));
        assert_eq!(result.language, Some("rust".to_string()));
        assert_eq!(result.meta_variables, vec!["A", "B"]);
    }

    #[test]
    fn parse_ast_grep_line_missing_file_returns_none() {
        let json = r#"{"text":"fn x(){}","language":"Rust"}"#;
        let root = Path::new(".");
        let result = parse_ast_grep_line(root, json);
        assert!(result.is_none());
    }
}
