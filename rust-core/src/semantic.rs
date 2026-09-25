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

    run_ast_grep_with_binary(
        bin,
        root,
        pattern,
        language,
        path_glob,
        limit,
        AST_GREP_TIMEOUT,
    )
}

const AST_GREP_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(60);

fn run_ast_grep_with_binary(
    bin: &str,
    root: &Path,
    pattern: &str,
    language: Option<&str>,
    path_glob: Option<&str>,
    limit: usize,
    timeout: std::time::Duration,
) -> AstGrepResult {
    use std::io::{BufRead, BufReader, Read};
    use std::process::Stdio;
    use std::sync::mpsc;
    use std::time::Instant;

    let fail = |error: String, matches: Vec<SemanticPatternMatch>| AstGrepResult {
        matches,
        binary: Some(bin.to_string()),
        error: Some(error),
        truncated: false,
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
    // The child stays in this process's group, so a caller killing the group on
    // cancellation (the Go bridge) also ends it.
    let mut child = match command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(c) => c,
        Err(_) => return fail("ast-grep binary is not available.".to_string(), vec![]),
    };

    // stderr: keep the first STDERR_CAP bytes, drain the rest so the child never blocks.
    let mut stderr = child.stderr.take().expect("stderr is piped");
    let stderr_thread = std::thread::spawn(move || {
        let mut kept = Vec::new();
        let mut buf = [0u8; 8192];
        while let Ok(n) = stderr.read(&mut buf) {
            if n == 0 {
                break;
            }
            let room = AST_GREP_STDERR_CAP.saturating_sub(kept.len());
            kept.extend_from_slice(&buf[..n.min(room)]);
        }
        kept
    });

    // stdout: stream bounded lines to this thread; backpressure via the sync channel.
    enum Chunk {
        Line(Vec<u8>),
        Overlong,
        Eof,
    }
    let stdout = child.stdout.take().expect("stdout is piped");
    let (tx, rx) = mpsc::sync_channel::<Chunk>(64);
    std::thread::spawn(move || {
        let mut reader = BufReader::new(stdout);
        loop {
            let mut line = Vec::new();
            let chunk = match reader
                .by_ref()
                .take(AST_GREP_MAX_LINE_BYTES as u64 + 1)
                .read_until(b'\n', &mut line)
            {
                Ok(0) | Err(_) => Chunk::Eof,
                Ok(n) if n > AST_GREP_MAX_LINE_BYTES => Chunk::Overlong,
                Ok(_) => Chunk::Line(line),
            };
            let stop = !matches!(chunk, Chunk::Line(_));
            if tx.send(chunk).is_err() || stop {
                break;
            }
        }
    });

    let deadline = Instant::now() + timeout;
    let mut matches: Vec<SemanticPatternMatch> = Vec::new();
    let mut truncated = false;
    let mut stdout_bytes = 0usize;
    let mut error: Option<String> = None;
    let mut reached_eof = false;
    while error.is_none() && !truncated {
        let remaining = deadline.saturating_duration_since(Instant::now());
        match rx.recv_timeout(remaining) {
            Ok(Chunk::Line(line)) => {
                stdout_bytes += line.len();
                if stdout_bytes > AST_GREP_MAX_OUTPUT_BYTES {
                    error = Some(format!(
                        "ast-grep output exceeded {AST_GREP_MAX_OUTPUT_BYTES} bytes; stopped"
                    ));
                    break;
                }
                let text = String::from_utf8_lossy(&line);
                let raw = text.trim();
                if raw.is_empty() {
                    continue;
                }
                if let Some(m) = parse_ast_grep_line(root, raw) {
                    matches.push(m);
                    if matches.len() >= limit {
                        truncated = true;
                    }
                }
            }
            Ok(Chunk::Overlong) => {
                error = Some(format!(
                    "ast-grep output line exceeded {AST_GREP_MAX_LINE_BYTES} bytes; stopped"
                ));
            }
            Ok(Chunk::Eof) => {
                reached_eof = true;
                break;
            }
            Err(_) => {
                error = Some(format!(
                    "ast-grep timed out after {} ms; child killed",
                    timeout.as_millis()
                ));
            }
        }
    }
    drop(rx);

    // Wait for exit within the same deadline; kill on early stop or overrun.
    let mut status = None;
    if reached_eof {
        while Instant::now() < deadline {
            match child.try_wait() {
                Ok(Some(s)) => {
                    status = Some(s);
                    break;
                }
                Ok(None) => std::thread::sleep(std::time::Duration::from_millis(10)),
                Err(_) => break,
            }
        }
        if status.is_none() {
            error = Some(format!(
                "ast-grep timed out after {} ms; child killed",
                timeout.as_millis()
            ));
        }
    }
    if status.is_none() {
        let _ = child.kill();
        let _ = child.wait();
    }
    let stderr_text = if status.is_some() {
        String::from_utf8_lossy(&stderr_thread.join().unwrap_or_default())
            .trim()
            .to_string()
    } else {
        String::new() // a grandchild may still hold stderr; do not block on it
    };

    if let Some(e) = error {
        return fail(e, matches);
    }
    if let Some(s) = status
        && !s.code().is_some_and(|c| c == 0 || c == 1)
    {
        let msg = if stderr_text.is_empty() {
            format!("ast-grep exited with code {s}")
        } else {
            stderr_text
        };
        return fail(msg, vec![]);
    }
    AstGrepResult {
        matches,
        binary: Some(bin.to_string()),
        error: None,
        truncated,
    }
}

const AST_GREP_MAX_LINE_BYTES: usize = 1 << 20;
const AST_GREP_MAX_OUTPUT_BYTES: usize = 64 << 20;
const AST_GREP_STDERR_CAP: usize = 64 << 10;

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

    #[cfg(unix)]
    fn fake_sg(dir: &Path, body: &str) -> String {
        use std::os::unix::fs::PermissionsExt;
        let path = dir.join("sg");
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path.to_string_lossy().into_owned()
    }

    #[cfg(unix)]
    fn run_bounded(bin: String, root: std::path::PathBuf, limit: usize) -> Option<AstGrepResult> {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let r = run_ast_grep_with_binary(
                &bin,
                &root,
                "fn $A() {}",
                None,
                None,
                limit,
                std::time::Duration::from_secs(2),
            );
            let _ = tx.send(r);
        });
        rx.recv_timeout(std::time::Duration::from_secs(15)).ok()
    }

    // Audit finding 5: Command::output had no child timeout.
    #[cfg(unix)]
    #[test]
    fn ast_grep_child_is_killed_at_timeout() {
        let dir = tempfile::TempDir::new().unwrap();
        let pid_file = dir.path().join("pid");
        // like the real single-process binary, the child itself blocks (exec).
        let bin = fake_sg(
            dir.path(),
            &format!("echo $$ > '{}'; exec sleep 600", pid_file.display()),
        );
        let r = run_bounded(bin, dir.path().to_path_buf(), 5)
            .expect("ast-grep run did not return within 15s of a 2s timeout");
        assert!(r.error.unwrap_or_default().contains("timed out"));
        let pid = std::fs::read_to_string(&pid_file).unwrap();
        let alive = std::process::Command::new("kill")
            .args(["-0", pid.trim()])
            .status()
            .unwrap()
            .success();
        assert!(!alive, "timed-out ast-grep child {pid} is still running");
    }

    // Audit finding 5: output was buffered in full before the result limit applied.
    #[cfg(unix)]
    #[test]
    fn ast_grep_output_is_bounded_while_streaming() {
        let dir = tempfile::TempDir::new().unwrap();
        let line = r#"{"file":"a.rs","text":"fn x() {}","range":{"start":{"line":0,"column":0},"end":{"line":0,"column":9}}}"#;
        let bin = fake_sg(dir.path(), &format!("while :; do echo '{line}'; done"));
        let r = run_bounded(bin, dir.path().to_path_buf(), 3)
            .expect("endless ast-grep output was not cut off at the result limit");
        assert_eq!(r.matches.len(), 3);
        assert!(r.truncated);
    }

    #[test]
    fn parse_ast_grep_line_missing_file_returns_none() {
        let json = r#"{"text":"fn x(){}","language":"Rust"}"#;
        let root = Path::new(".");
        let result = parse_ast_grep_line(root, json);
        assert!(result.is_none());
    }
}
