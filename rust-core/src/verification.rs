use chrono::Utc;
use roxmltree::Document;
use serde::{Deserialize, Serialize};
use sha1::{Digest, Sha1};
use std::fs;
use std::path::Path;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct VerificationMilestone {
    pub name: &'static str,
    pub outcome: &'static str,
}

pub fn initial_verification_plan() -> Vec<VerificationMilestone> {
    vec![
        VerificationMilestone {
            name: "process_runner",
            outcome: "Move long-running verification and runtime-safe process control into the Rust core.",
        },
        VerificationMilestone {
            name: "artifact_parsers",
            outcome: "Port coverage and verification artifact parsing behind stable contract structs.",
        },
        VerificationMilestone {
            name: "replay_hooks",
            outcome: "Expose replayable verification entry points for later eval harnesses.",
        },
    ]
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RustCoverageResult {
    pub result_id: String,
    pub workspace_id: String,
    pub run_id: Option<String>,
    pub issue_id: Option<String>,
    pub line_coverage: f64,
    pub branch_coverage: Option<f64>,
    pub function_coverage: Option<f64>,
    pub lines_covered: usize,
    pub lines_total: usize,
    pub branches_covered: Option<usize>,
    pub branches_total: Option<usize>,
    pub files_covered: usize,
    pub files_total: usize,
    pub uncovered_files: Vec<String>,
    pub format: String,
    pub raw_report_path: Option<String>,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustVerificationCommandResult {
    pub command: String,
    pub cwd: String,
    pub exit_code: Option<i32>,
    pub success: bool,
    pub timed_out: bool,
    pub duration_ms: u64,
    pub stdout_excerpt: String,
    pub stderr_excerpt: String,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustVerificationProfileInput {
    pub profile_id: String,
    pub workspace_id: String,
    pub name: String,
    pub description: String,
    pub test_command: String,
    pub coverage_command: Option<String>,
    pub coverage_report_path: Option<String>,
    pub coverage_format: String,
    pub max_runtime_seconds: u64,
    pub retry_count: u64,
    pub source_paths: Vec<String>,
    pub built_in: bool,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RustVerificationProfileResult {
    pub profile_id: String,
    pub workspace_id: String,
    pub attempts: Vec<RustVerificationCommandResult>,
    pub attempt_count: usize,
    pub success: bool,
    pub coverage_command_result: Option<RustVerificationCommandResult>,
    pub coverage_result: Option<RustCoverageResult>,
    pub coverage_report_path: Option<String>,
    pub created_at: String,
}

pub fn run_verification_command(
    workspace_root: &Path,
    command: &str,
    timeout_seconds: u64,
) -> Result<RustVerificationCommandResult, std::io::Error> {
    let mut shell_command = if cfg!(target_os = "windows") {
        let mut cmd = Command::new("cmd");
        cmd.arg("/C").arg(command);
        cmd
    } else {
        let mut cmd = Command::new("sh");
        cmd.arg("-lc").arg(command);
        cmd
    };

    let resolved_cwd = workspace_root
        .canonicalize()
        .unwrap_or_else(|_| workspace_root.to_path_buf());
    let started_at = Instant::now();
    let timeout = Duration::from_secs(timeout_seconds.max(1));

    let mut child = shell_command
        .current_dir(&resolved_cwd)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;

    loop {
        if child.try_wait()?.is_some() {
            let output = child.wait_with_output()?;
            return Ok(build_verification_result(
                command,
                &resolved_cwd,
                output.status.code(),
                output.status.success(),
                false,
                started_at.elapsed(),
                String::from_utf8_lossy(&output.stdout).as_ref(),
                String::from_utf8_lossy(&output.stderr).as_ref(),
            ));
        }

        if started_at.elapsed() >= timeout {
            let _ = child.kill();
            let output = child.wait_with_output()?;
            return Ok(build_verification_result(
                command,
                &resolved_cwd,
                output.status.code(),
                false,
                true,
                started_at.elapsed(),
                String::from_utf8_lossy(&output.stdout).as_ref(),
                String::from_utf8_lossy(&output.stderr).as_ref(),
            ));
        }

        thread::sleep(Duration::from_millis(50));
    }
}

pub fn run_verification_profile(
    workspace_root: &Path,
    profile: &RustVerificationProfileInput,
    run_id: Option<&str>,
    issue_id: Option<&str>,
) -> Result<RustVerificationProfileResult, std::io::Error> {
    let mut attempts = Vec::new();
    let max_attempts = (profile.retry_count as usize).saturating_add(1).max(1);
    for _ in 0..max_attempts {
        let attempt = run_verification_command(
            workspace_root,
            &profile.test_command,
            profile.max_runtime_seconds.max(1),
        )?;
        let attempt_success = attempt.success;
        attempts.push(attempt);
        if attempt_success {
            break;
        }
    }

    let mut coverage_command_result = None;
    let mut coverage_result = None;
    let resolved_report_path = resolve_report_path(workspace_root, profile.coverage_report_path.as_deref());

    let test_success = attempts.last().map(|item| item.success).unwrap_or(false);
    if test_success {
        if let Some(command) = profile.coverage_command.as_deref() {
            coverage_command_result = Some(run_verification_command(
                workspace_root,
                command,
                profile.max_runtime_seconds.max(1),
            )?);
        }

        let coverage_ok = coverage_command_result
            .as_ref()
            .map(|item| item.success)
            .unwrap_or(true);
        if coverage_ok {
            if let Some(report_path) = resolved_report_path.as_ref() {
                if report_path.exists() {
                    coverage_result = Some(parse_coverage_file(
                        report_path,
                        &profile.workspace_id,
                        run_id,
                        issue_id,
                    )?);
                }
            }
        }
    }

    let overall_success = test_success
        && coverage_command_result
            .as_ref()
            .map(|item| item.success)
            .unwrap_or(true);

    Ok(RustVerificationProfileResult {
        profile_id: profile.profile_id.clone(),
        workspace_id: profile.workspace_id.clone(),
        attempt_count: attempts.len(),
        attempts,
        success: overall_success,
        coverage_command_result,
        coverage_result,
        coverage_report_path: resolved_report_path.map(|path| path.to_string_lossy().to_string()),
        created_at: Utc::now().to_rfc3339(),
    })
}

pub fn parse_lcov_file(
    report_path: &Path,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
) -> Result<RustCoverageResult, std::io::Error> {
    let content = fs::read_to_string(report_path)?;
    Ok(parse_lcov_content(
        &content,
        workspace_id,
        run_id,
        issue_id,
        report_path.to_string_lossy().as_ref(),
    ))
}

pub fn parse_cobertura_file(
    report_path: &Path,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
) -> Result<RustCoverageResult, std::io::Error> {
    let content = fs::read_to_string(report_path)?;
    Ok(parse_cobertura_content(
        &content,
        workspace_id,
        run_id,
        issue_id,
        report_path.to_string_lossy().as_ref(),
    ))
}

pub fn parse_istanbul_file(
    report_path: &Path,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
) -> Result<RustCoverageResult, std::io::Error> {
    let content = fs::read_to_string(report_path)?;
    Ok(parse_istanbul_content(
        &content,
        workspace_id,
        run_id,
        issue_id,
        report_path.to_string_lossy().as_ref(),
    ))
}

pub fn parse_coverage_file(
    report_path: &Path,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
) -> Result<RustCoverageResult, std::io::Error> {
    let extension = report_path
        .extension()
        .and_then(|ext| ext.to_str())
        .unwrap_or_default();
    match extension {
        "xml" => parse_cobertura_file(report_path, workspace_id, run_id, issue_id),
        "json" => parse_istanbul_file(report_path, workspace_id, run_id, issue_id),
        "csv" | "txt" | "info" | "" => parse_lcov_file(report_path, workspace_id, run_id, issue_id),
        _ => Ok(empty_coverage_result(
            workspace_id,
            run_id,
            issue_id,
            "unknown",
            report_path.to_string_lossy().as_ref(),
        )),
    }
}

pub fn parse_lcov_content(
    content: &str,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
    report_path: &str,
) -> RustCoverageResult {
    let mut lines_covered = 0usize;
    let mut lines_total = 0usize;
    let mut files_covered = 0usize;
    let mut files_total = 0usize;
    let mut uncovered: Vec<String> = Vec::new();

    let mut current_file = String::new();
    let mut file_hit = 0usize;
    let mut file_total = 0usize;

    for raw_line in content.lines() {
        let line = raw_line.trim();
        if let Some(rest) = line.strip_prefix("SF:") {
            finalize_lcov_file(
                &mut current_file,
                &mut file_hit,
                &mut file_total,
                &mut files_total,
                &mut lines_covered,
                &mut lines_total,
                &mut files_covered,
                &mut uncovered,
            );
            current_file = rest.to_string();
        } else if let Some(rest) = line.strip_prefix("DA:") {
            let parts: Vec<&str> = rest.split(',').collect();
            if parts.len() >= 2 {
                file_total += 1;
                if parts[1].parse::<i64>().ok().unwrap_or(0) > 0 {
                    file_hit += 1;
                }
            }
        } else if line == "end_of_record" {
            finalize_lcov_file(
                &mut current_file,
                &mut file_hit,
                &mut file_total,
                &mut files_total,
                &mut lines_covered,
                &mut lines_total,
                &mut files_covered,
                &mut uncovered,
            );
        }
    }

    let line_coverage = if lines_total > 0 {
        ((lines_covered as f64 / lines_total as f64) * 10000.0).round() / 100.0
    } else {
        0.0
    };

    RustCoverageResult {
        result_id: coverage_result_id(workspace_id, run_id, issue_id, report_path),
        workspace_id: workspace_id.to_string(),
        run_id: run_id.map(|value| value.to_string()),
        issue_id: issue_id.map(|value| value.to_string()),
        line_coverage,
        branch_coverage: None,
        function_coverage: None,
        lines_covered,
        lines_total,
        branches_covered: None,
        branches_total: None,
        files_covered,
        files_total,
        uncovered_files: uncovered.into_iter().take(50).collect(),
        format: "lcov".to_string(),
        raw_report_path: Some(report_path.to_string()),
        created_at: Utc::now().to_rfc3339(),
    }
}

pub fn parse_cobertura_content(
    content: &str,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
    report_path: &str,
) -> RustCoverageResult {
    let document = match Document::parse(content) {
        Ok(document) => document,
        Err(_) => {
            return empty_coverage_result(workspace_id, run_id, issue_id, "cobertura", report_path);
        }
    };

    let root = document.root_element();
    let line_rate = root
        .attribute("line-rate")
        .and_then(|value| value.parse::<f64>().ok())
        .unwrap_or(0.0);
    let branch_rate = root
        .attribute("branch-rate")
        .and_then(|value| value.parse::<f64>().ok());

    let mut lines_covered = 0usize;
    let mut lines_total = 0usize;
    let mut files_covered = 0usize;
    let mut files_total = 0usize;
    let mut uncovered: Vec<String> = Vec::new();

    for class_node in root.descendants().filter(|node| node.has_tag_name("class")) {
        files_total += 1;
        let file_name = class_node.attribute("filename").unwrap_or_default().to_string();
        let mut class_lines = 0usize;
        let mut class_total = 0usize;
        for line_node in class_node.descendants().filter(|node| node.has_tag_name("line")) {
            let hits = line_node
                .attribute("hits")
                .and_then(|value| value.parse::<i64>().ok())
                .unwrap_or(0);
            class_total += 1;
            if hits > 0 {
                class_lines += 1;
            }
        }
        lines_covered += class_lines;
        lines_total += class_total;
        if class_lines > 0 {
            files_covered += 1;
        } else if !file_name.is_empty() {
            uncovered.push(file_name);
        }
    }

    RustCoverageResult {
        result_id: coverage_result_id(workspace_id, run_id, issue_id, report_path),
        workspace_id: workspace_id.to_string(),
        run_id: run_id.map(|value| value.to_string()),
        issue_id: issue_id.map(|value| value.to_string()),
        line_coverage: round_percent(line_rate * 100.0),
        branch_coverage: branch_rate.map(|value| round_percent(value * 100.0)),
        function_coverage: None,
        lines_covered,
        lines_total,
        branches_covered: None,
        branches_total: None,
        files_covered,
        files_total,
        uncovered_files: uncovered.into_iter().take(50).collect(),
        format: "cobertura".to_string(),
        raw_report_path: Some(report_path.to_string()),
        created_at: Utc::now().to_rfc3339(),
    }
}

pub fn parse_istanbul_content(
    content: &str,
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
    report_path: &str,
) -> RustCoverageResult {
    let payload: serde_json::Value = match serde_json::from_str(content) {
        Ok(payload) => payload,
        Err(_) => {
            return empty_coverage_result(workspace_id, run_id, issue_id, "istanbul", report_path);
        }
    };

    let source_files = payload
        .get("source_files")
        .or_else(|| payload.get("coverage"))
        .cloned()
        .unwrap_or(serde_json::Value::Null);

    let normalized_source_files: Vec<serde_json::Value> = match source_files {
        serde_json::Value::Array(items) => items,
        serde_json::Value::Object(map) => map
            .into_iter()
            .map(|(path, mut value)| {
                if let serde_json::Value::Object(ref mut inner) = value {
                    inner.insert("path".to_string(), serde_json::Value::String(path));
                }
                value
            })
            .collect(),
        _ => Vec::new(),
    };

    let mut total_lines = 0usize;
    let mut covered_lines = 0usize;
    let mut files_covered = 0usize;
    let mut files_total = 0usize;
    let mut uncovered: Vec<String> = Vec::new();

    for source_file in normalized_source_files {
        let Some(file_obj) = source_file.as_object() else {
            continue;
        };
        files_total += 1;
        let file_path = file_obj
            .get("path")
            .or_else(|| file_obj.get("file"))
            .and_then(|value| value.as_str())
            .unwrap_or_default()
            .to_string();
        let statement_data = file_obj
            .get("s")
            .or_else(|| file_obj.get("statementMap"))
            .and_then(|value| value.as_object());
        let Some(statement_data) = statement_data else {
            continue;
        };

        let total_count = statement_data.len();
        let hit_count = statement_data
            .values()
            .filter(|value| match value {
                serde_json::Value::Number(number) => number.as_i64().unwrap_or(0) > 0,
                serde_json::Value::Object(inner) => inner
                    .get("executed")
                    .and_then(|flag| flag.as_bool())
                    .unwrap_or(false),
                _ => false,
            })
            .count();

        if total_count > 0 {
            total_lines += total_count;
            covered_lines += hit_count;
            if hit_count > 0 {
                files_covered += 1;
            } else {
                uncovered.push(file_path);
            }
        }
    }

    RustCoverageResult {
        result_id: coverage_result_id(workspace_id, run_id, issue_id, report_path),
        workspace_id: workspace_id.to_string(),
        run_id: run_id.map(|value| value.to_string()),
        issue_id: issue_id.map(|value| value.to_string()),
        line_coverage: if total_lines > 0 {
            round_percent((covered_lines as f64 / total_lines as f64) * 100.0)
        } else {
            0.0
        },
        branch_coverage: None,
        function_coverage: None,
        lines_covered: covered_lines,
        lines_total: total_lines,
        branches_covered: None,
        branches_total: None,
        files_covered,
        files_total,
        uncovered_files: uncovered.into_iter().take(50).collect(),
        format: "istanbul".to_string(),
        raw_report_path: Some(report_path.to_string()),
        created_at: Utc::now().to_rfc3339(),
    }
}

fn finalize_lcov_file(
    current_file: &mut String,
    file_hit: &mut usize,
    file_total: &mut usize,
    files_total: &mut usize,
    lines_covered: &mut usize,
    lines_total: &mut usize,
    files_covered: &mut usize,
    uncovered: &mut Vec<String>,
) {
    if current_file.is_empty() {
        return;
    }
    *files_total += 1;
    *lines_covered += *file_hit;
    *lines_total += *file_total;
    if *file_hit > 0 {
        *files_covered += 1;
    } else {
        uncovered.push(current_file.clone());
    }
    current_file.clear();
    *file_hit = 0;
    *file_total = 0;
}

fn coverage_result_id(
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
    report_path: &str,
) -> String {
    let mut hasher = Sha1::new();
    hasher.update(workspace_id);
    hasher.update(run_id.unwrap_or_default());
    hasher.update(issue_id.unwrap_or_default());
    hasher.update(report_path);
    hasher.update(Utc::now().to_rfc3339());
    format!("cov_{:x}", hasher.finalize())[0..16].to_string()
}

fn round_percent(value: f64) -> f64 {
    (value * 100.0).round() / 100.0
}

fn build_verification_result(
    command: &str,
    cwd: &Path,
    exit_code: Option<i32>,
    success: bool,
    timed_out: bool,
    duration: Duration,
    stdout: &str,
    stderr: &str,
) -> RustVerificationCommandResult {
    RustVerificationCommandResult {
        command: command.to_string(),
        cwd: cwd.to_string_lossy().to_string(),
        exit_code,
        success,
        timed_out,
        duration_ms: duration.as_millis().min(u128::from(u64::MAX)) as u64,
        stdout_excerpt: truncate_excerpt(stdout, 4000),
        stderr_excerpt: truncate_excerpt(stderr, 4000),
        created_at: Utc::now().to_rfc3339(),
    }
}

fn truncate_excerpt(text: &str, limit: usize) -> String {
    if text.len() <= limit {
        return text.to_string();
    }
    let omitted = text.len() - limit;
    format!("{}\n...[truncated {} chars]", &text[..limit], omitted)
}

fn resolve_report_path(workspace_root: &Path, report_path: Option<&str>) -> Option<std::path::PathBuf> {
    let report_path = report_path?;
    let candidate = Path::new(report_path);
    if candidate.is_absolute() {
        return Some(candidate.to_path_buf());
    }
    Some(workspace_root.join(candidate))
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MigrationVerificationResult {
    pub risks: Vec<String>,
    pub recommended_contract: String,
    pub unix_constraints: Vec<String>,
}

pub fn run_migration_verification(
    workspace_root: &Path,
    command: &str,
    timeout_seconds: u64,
) -> Result<MigrationVerificationResult, std::io::Error> {
    let mut shell_cmd = if cfg!(target_os = "windows") {
        let mut cmd = Command::new("cmd");
        cmd.arg("/C").arg(command);
        cmd
    } else {
        let mut cmd = Command::new("sh");
        cmd.arg("-lc").arg(command);
        cmd
    };

    let resolved_cwd = workspace_root.canonicalize().unwrap_or_else(|_| workspace_root.to_path_buf());
    let started_at = Instant::now();
    let timeout = Duration::from_secs(timeout_seconds.max(1));

    let mut child = shell_cmd
        .current_dir(&resolved_cwd)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;

    loop {
        if let Some(status) = child.try_wait()? {
            let output = child.wait_with_output()?;
            let exit_code = status.code();
            let success = status.success();
            let elapsed = started_at.elapsed();
            return Ok(build_migration_result(
                command,
                &resolved_cwd,
                exit_code,
                success,
                false,
                elapsed,
                String::from_utf8_lossy(&output.stdout).as_ref(),
                String::from_utf8_lossy(&output.stderr).as_ref(),
            ));
        }

        if started_at.elapsed() >= timeout {
            let _ = child.kill();
            let output = child.wait_with_output()?;
            let elapsed = started_at.elapsed();
            return Ok(build_migration_result(
                command,
                &resolved_cwd,
                output.status.code(),
                false,
                true,
                elapsed,
                String::from_utf8_lossy(&output.stdout).as_ref(),
                String::from_utf8_lossy(&output.stderr).as_ref(),
            ));
        }

        thread::sleep(Duration::from_millis(50));
    }
}

fn build_migration_result(
    command: &str,
    cwd: &Path,
    exit_code: Option<i32>,
    success: bool,
    timed_out: bool,
    duration: Duration,
    stdout: &str,
    stderr: &str,
) -> MigrationVerificationResult {
    let mut risks = Vec::new();

    if timed_out {
        risks.push("verification_timed_out".to_string());
    }

    if !success && !timed_out {
        if let Some(code) = exit_code {
            risks.push(format!("exit_code_{}", code));
        }
    }

    let stderr_lower = stderr.to_lowercase();
    if stderr_lower.contains("error") || stderr_lower.contains("failed") {
        risks.push("stderr_indicates_failure".to_string());
    }

    let stdout_trunc = truncate_excerpt(stdout, 2000);
    let stderr_trunc = truncate_excerpt(stderr, 2000);

    if stdout_trunc.len() > 1800 || stderr_trunc.len() > 1800 {
        risks.push("excessive_output_may_mask_issues".to_string());
    }

    let recommended_contract = format!(
        r#"{{"command":"{}","cwd":"{}","exit_code":{:?},"success":{},"timed_out":{},"duration_ms":{},"stdout_excerpt":"{}","stderr_excerpt":"{}"}}"#,
        command,
        cwd.to_string_lossy(),
        exit_code,
        success,
        timed_out,
        duration.as_millis().min(u128::from(u64::MAX)) as u64,
        escape_json(&stdout_trunc),
        escape_excerpt(&stderr_trunc),
    );

    let mut unix_constraints = Vec::new();
    unix_constraints.push("timeout_seconds_required".to_string());
    unix_constraints.push("cwd_must_exist".to_string());
    unix_constraints.push("command_string_passed_to_shell".to_string());

    if cfg!(target_os = "windows") {
        unix_constraints.push("windows_cmd_shell_syntax".to_string());
    } else {
        unix_constraints.push("posix_shell_syntax".to_string());
    }

    MigrationVerificationResult {
        risks,
        recommended_contract,
        unix_constraints,
    }
}

fn escape_json(s: &str) -> String {
    s.replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
        .replace('\r', "\\r")
        .replace('\t', "\\t")
}

fn escape_excerpt(s: &str) -> String {
    escape_json(s)
}

fn empty_coverage_result(
    workspace_id: &str,
    run_id: Option<&str>,
    issue_id: Option<&str>,
    format: &str,
    report_path: &str,
) -> RustCoverageResult {
    RustCoverageResult {
        result_id: coverage_result_id(workspace_id, run_id, issue_id, report_path),
        workspace_id: workspace_id.to_string(),
        run_id: run_id.map(|value| value.to_string()),
        issue_id: issue_id.map(|value| value.to_string()),
        line_coverage: 0.0,
        branch_coverage: None,
        function_coverage: None,
        lines_covered: 0,
        lines_total: 0,
        branches_covered: None,
        branches_total: None,
        files_covered: 0,
        files_total: 0,
        uncovered_files: Vec::new(),
        format: format.to_string(),
        raw_report_path: Some(report_path.to_string()),
        created_at: Utc::now().to_rfc3339(),
    }
}

#[cfg(test)]
mod tests {
    use super::{
        parse_cobertura_content, parse_istanbul_content, parse_lcov_content, run_migration_verification,
        run_verification_command, run_verification_profile, RustVerificationProfileInput,
    };
    use chrono::Utc;
    use tempfile::TempDir;

    #[test]
    fn parses_lcov_content() {
        let result = parse_lcov_content(
            "SF:src/app.py\nDA:1,1\nDA:2,0\nend_of_record\nSF:src/empty.py\nDA:1,0\nend_of_record\n",
            "workspace-1",
            Some("run-1"),
            Some("issue-1"),
            "coverage.info",
        );

        assert_eq!(result.format, "lcov");
        assert_eq!(result.lines_covered, 1);
        assert_eq!(result.lines_total, 3);
        assert_eq!(result.files_covered, 1);
        assert_eq!(result.files_total, 2);
        assert_eq!(result.uncovered_files, vec!["src/empty.py".to_string()]);
        assert_eq!(result.line_coverage, 33.33);
    }

    #[test]
    fn parses_cobertura_content() {
        let result = parse_cobertura_content(
            r#"<coverage line-rate="0.5" branch-rate="0.25"><packages><package><classes><class filename="src/app.py"><lines><line number="1" hits="1"/><line number="2" hits="0"/></lines></class><class filename="src/empty.py"><lines><line number="1" hits="0"/></lines></class></classes></package></packages></coverage>"#,
            "workspace-1",
            Some("run-1"),
            Some("issue-1"),
            "coverage.xml",
        );

        assert_eq!(result.format, "cobertura");
        assert_eq!(result.line_coverage, 50.0);
        assert_eq!(result.branch_coverage, Some(25.0));
        assert_eq!(result.lines_covered, 1);
        assert_eq!(result.lines_total, 3);
        assert_eq!(result.files_covered, 1);
        assert_eq!(result.files_total, 2);
        assert_eq!(result.uncovered_files, vec!["src/empty.py".to_string()]);
    }

    #[test]
    fn parses_istanbul_content() {
        let result = parse_istanbul_content(
            r#"{"coverage":{"src/app.ts":{"s":{"1":1,"2":0}},"src/empty.ts":{"s":{"1":0}}}}"#,
            "workspace-1",
            Some("run-1"),
            Some("issue-1"),
            "coverage.json",
        );

        assert_eq!(result.format, "istanbul");
        assert_eq!(result.line_coverage, 33.33);
        assert_eq!(result.lines_covered, 1);
        assert_eq!(result.lines_total, 3);
        assert_eq!(result.files_covered, 1);
        assert_eq!(result.files_total, 2);
        assert_eq!(result.uncovered_files, vec!["src/empty.ts".to_string()]);
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn runs_verification_command_successfully() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_verification_command(temp_dir.path(), "printf 'ok\\n'", 2).expect("command should run");

        assert!(result.success);
        assert!(!result.timed_out);
        assert_eq!(result.exit_code, Some(0));
        assert_eq!(result.stdout_excerpt, "ok\n");
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn captures_verification_command_failure() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_verification_command(temp_dir.path(), "printf 'boom\\n' 1>&2; exit 3", 2)
            .expect("command should run");

        assert!(!result.success);
        assert!(!result.timed_out);
        assert_eq!(result.exit_code, Some(3));
        assert_eq!(result.stderr_excerpt, "boom\n");
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn marks_timeout_for_long_running_verification_command() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_verification_command(temp_dir.path(), "sleep 2", 1).expect("command should run");

        assert!(!result.success);
        assert!(result.timed_out);
        assert!(result.duration_ms >= 1000);
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn runs_verification_profile_with_retry_and_coverage() {
        let temp_dir = TempDir::new().expect("temp dir");
        let profile = RustVerificationProfileInput {
            profile_id: "backend-pytest".to_string(),
            workspace_id: "workspace-1".to_string(),
            name: "Backend pytest".to_string(),
            description: "Verification profile".to_string(),
            test_command: "if [ ! -f .attempt ]; then touch .attempt; exit 1; fi; printf 'tests ok\\n'".to_string(),
            coverage_command: Some(
                "printf 'SF:src/app.py\\nDA:1,1\\nDA:2,0\\nend_of_record\\n' > coverage.info".to_string(),
            ),
            coverage_report_path: Some("coverage.info".to_string()),
            coverage_format: "lcov".to_string(),
            max_runtime_seconds: 2,
            retry_count: 1,
            source_paths: vec!["AGENTS.md".to_string()],
            built_in: false,
            created_at: Utc::now().to_rfc3339(),
            updated_at: Utc::now().to_rfc3339(),
        };

        let result = run_verification_profile(temp_dir.path(), &profile, Some("run-1"), Some("issue-1"))
            .expect("profile should run");

        assert!(result.success);
        assert_eq!(result.attempt_count, 2);
        assert_eq!(result.attempts.len(), 2);
        assert!(!result.attempts[0].success);
        assert!(result.attempts[1].success);
        assert!(result.coverage_command_result.as_ref().is_some_and(|item| item.success));
        assert_eq!(result.coverage_result.as_ref().map(|item| item.format.as_str()), Some("lcov"));
        assert_eq!(result.coverage_result.as_ref().map(|item| item.lines_covered), Some(1));
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn runs_migration_verification_and_returns_contract() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_migration_verification(temp_dir.path(), "printf 'ok\\n'", 2).expect("should run");

        assert!(result.risks.is_empty());
        assert!(result.recommended_contract.contains("\"success\":true"));
        assert!(result.recommended_contract.contains("exit_code\":"));
        assert!(result.unix_constraints.contains(&"timeout_seconds_required".to_string()));
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn migration_verification_captures_failure_risks() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_migration_verification(temp_dir.path(), "printf 'error\\n' 1>&2; exit 1", 2).expect("should run");

        assert!(result.risks.contains(&"exit_code_1".to_string()));
        assert!(result.risks.contains(&"stderr_indicates_failure".to_string()));
        assert!(result.recommended_contract.contains("\"success\":false"));
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn migration_verification_marks_timeout_risk() {
        let temp_dir = TempDir::new().expect("temp dir");
        let result = run_migration_verification(temp_dir.path(), "sleep 3", 1).expect("should run");

        assert!(result.risks.contains(&"verification_timed_out".to_string()));
        assert!(result.recommended_contract.contains("\"timed_out\":true"));
    }
}
