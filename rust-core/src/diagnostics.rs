use chrono::Utc;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha1::{Digest, Sha1};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustNormalizedDiagnostic {
    pub workspace_id: String,
    pub path: String,
    pub range_start_line: usize,
    pub range_start_column: usize,
    pub range_end_line: usize,
    pub range_end_column: usize,
    pub severity: String,
    pub message: String,
    pub source_kind: String,
    pub source_name: String,
    pub rule_code: Option<String>,
    pub fingerprint: String,
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDiagnosticsBatch {
    pub workspace_id: String,
    pub source_kind: String,
    pub source_name: String,
    pub diagnostic_count: usize,
    pub diagnostics: Vec<RustNormalizedDiagnostic>,
    pub severity_counts: BTreeMap<String, usize>,
    pub warnings: Vec<String>,
    pub generated_at: String,
}

pub fn normalize_diagnostics_file(
    workspace_id: &str,
    root_path: &Path,
    input_path: &Path,
    source_kind: &str,
    source_name: &str,
) -> Result<RustDiagnosticsBatch, std::io::Error> {
    let content = fs::read_to_string(input_path)?;
    let payload = serde_json::from_str::<Value>(&content).map_err(|err| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!("decode diagnostics JSON: {err}"),
        )
    })?;
    Ok(normalize_diagnostics_payload(
        workspace_id,
        root_path,
        &payload,
        source_kind,
        source_name,
    ))
}

pub fn normalize_diagnostics_payload(
    workspace_id: &str,
    root_path: &Path,
    payload: &Value,
    source_kind: &str,
    source_name: &str,
) -> RustDiagnosticsBatch {
    let generated_at = Utc::now().to_rfc3339();
    let source_kind = normalize_source_kind(source_kind);
    let source_name = if source_name.trim().is_empty() {
        "unknown".to_string()
    } else {
        source_name.trim().to_string()
    };
    let mut warnings = Vec::new();
    let mut out = Vec::new();

    for item in diagnostic_items(payload) {
        let path = diagnostic_path(root_path, item, payload);
        let Some(path) = path else {
            warnings.push("Skipped diagnostic without a path or URI.".to_string());
            continue;
        };
        let message = item
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string();
        if message.is_empty() {
            warnings.push(format!("Skipped diagnostic for {path} without a message."));
            continue;
        }
        let range = item.get("range").unwrap_or(&Value::Null);
        let (start_line, start_column) = lsp_position(range.get("start"));
        let (end_line, end_column) = lsp_position(range.get("end"));
        let severity = normalize_severity(item.get("severity"));
        let rule_code = diagnostic_code(item.get("code"));
        let fingerprint = diagnostic_fingerprint(
            workspace_id,
            &path,
            start_line,
            start_column,
            end_line,
            end_column,
            &severity,
            &message,
            &source_kind,
            &source_name,
            rule_code.as_deref(),
        );
        out.push(RustNormalizedDiagnostic {
            workspace_id: workspace_id.to_string(),
            path,
            range_start_line: start_line,
            range_start_column: start_column,
            range_end_line: end_line,
            range_end_column: end_column,
            severity,
            message,
            source_kind: source_kind.clone(),
            source_name: source_name.clone(),
            rule_code,
            fingerprint,
            generated_at: generated_at.clone(),
        });
    }

    let mut severity_counts = BTreeMap::new();
    for item in &out {
        *severity_counts.entry(item.severity.clone()).or_insert(0) += 1;
    }
    if out.is_empty() {
        warnings.push("No diagnostics were normalized from the input payload.".to_string());
    }

    RustDiagnosticsBatch {
        workspace_id: workspace_id.to_string(),
        source_kind,
        source_name,
        diagnostic_count: out.len(),
        diagnostics: out,
        severity_counts,
        warnings,
        generated_at,
    }
}

fn diagnostic_items(payload: &Value) -> Vec<&Value> {
    if let Some(items) = payload.get("diagnostics").and_then(Value::as_array) {
        return items.iter().collect();
    }
    if let Some(items) = payload.as_array() {
        return items.iter().collect();
    }
    Vec::new()
}

fn diagnostic_path(root_path: &Path, item: &Value, payload: &Value) -> Option<String> {
    for candidate in [
        item.get("path").and_then(Value::as_str),
        payload.get("path").and_then(Value::as_str),
        item.get("uri").and_then(Value::as_str),
        payload.get("uri").and_then(Value::as_str),
    ]
    .into_iter()
    .flatten()
    {
        let value = candidate.trim();
        if value.is_empty() {
            continue;
        }
        let path = if let Some(stripped) = value.strip_prefix("file://") {
            PathBuf::from(stripped)
        } else {
            PathBuf::from(value)
        };
        let relative = if path.is_absolute() {
            path.strip_prefix(root_path)
                .map(|item| item.to_path_buf())
                .unwrap_or(path)
        } else {
            path
        };
        return Some(relative.to_string_lossy().replace('\\', "/"));
    }
    None
}

fn lsp_position(value: Option<&Value>) -> (usize, usize) {
    let Some(value) = value else {
        return (1, 1);
    };
    let line = value
        .get("line")
        .and_then(Value::as_u64)
        .map(|item| item as usize + 1)
        .unwrap_or(1);
    let column = value
        .get("character")
        .or_else(|| value.get("column"))
        .and_then(Value::as_u64)
        .map(|item| item as usize + 1)
        .unwrap_or(1);
    (line, column)
}

fn normalize_source_kind(value: &str) -> String {
    match value.trim().to_lowercase().as_str() {
        "lsp" | "compiler" | "test" | "scanner" | "manual" => value.trim().to_lowercase(),
        _ => "lsp".to_string(),
    }
}

fn normalize_severity(value: Option<&Value>) -> String {
    match value {
        Some(Value::Number(number)) => match number.as_u64().unwrap_or(0) {
            1 => "error".to_string(),
            2 => "warning".to_string(),
            3 => "info".to_string(),
            4 => "hint".to_string(),
            _ => "info".to_string(),
        },
        Some(Value::String(text)) => match text.trim().to_lowercase().as_str() {
            "error" | "warning" | "info" | "hint" => text.trim().to_lowercase(),
            _ => "info".to_string(),
        },
        _ => "info".to_string(),
    }
}

fn diagnostic_code(value: Option<&Value>) -> Option<String> {
    match value {
        Some(Value::String(text)) if !text.trim().is_empty() => Some(text.trim().to_string()),
        Some(Value::Number(number)) => Some(number.to_string()),
        Some(Value::Object(map)) => map
            .get("value")
            .and_then(Value::as_str)
            .map(|item| item.trim().to_string())
            .filter(|item| !item.is_empty()),
        _ => None,
    }
}

fn diagnostic_fingerprint(
    workspace_id: &str,
    path: &str,
    start_line: usize,
    start_column: usize,
    end_line: usize,
    end_column: usize,
    severity: &str,
    message: &str,
    source_kind: &str,
    source_name: &str,
    rule_code: Option<&str>,
) -> String {
    let mut hasher = Sha1::new();
    hasher.update(workspace_id.as_bytes());
    hasher.update(b"\0");
    hasher.update(path.as_bytes());
    hasher.update(b"\0");
    hasher.update(format!("{start_line}:{start_column}:{end_line}:{end_column}").as_bytes());
    hasher.update(b"\0");
    hasher.update(severity.as_bytes());
    hasher.update(b"\0");
    hasher.update(message.as_bytes());
    hasher.update(b"\0");
    hasher.update(source_kind.as_bytes());
    hasher.update(b"\0");
    hasher.update(source_name.as_bytes());
    hasher.update(b"\0");
    hasher.update(rule_code.unwrap_or("").as_bytes());
    format!("{:x}", hasher.finalize())
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    #[test]
    fn normalizes_lsp_publish_diagnostics_payload() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path();
        let payload = json!({
            "uri": "file:///tmp/project/src/app.ts",
            "diagnostics": [{
                "range": {
                    "start": {"line": 1, "character": 4},
                    "end": {"line": 1, "character": 12}
                },
                "severity": 1,
                "code": "TS2304",
                "source": "typescript",
                "message": "Cannot find name 'thing'."
            }]
        });

        let batch = super::normalize_diagnostics_payload(
            "workspace-1",
            root,
            &payload,
            "lsp",
            "typescript-language-server",
        );

        assert_eq!(batch.diagnostic_count, 1);
        let item = &batch.diagnostics[0];
        assert_eq!(item.severity, "error");
        assert_eq!(item.range_start_line, 2);
        assert_eq!(item.range_start_column, 5);
        assert_eq!(item.rule_code.as_deref(), Some("TS2304"));
        assert_eq!(item.source_kind, "lsp");
        assert_eq!(item.source_name, "typescript-language-server");
        assert!(!item.fingerprint.is_empty());
    }
}
