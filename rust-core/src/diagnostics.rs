use chrono::Utc;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha1::{Digest, Sha1};
use sha2::Sha256;
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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDiagnosticsReplayArchive {
    pub workspace_id: String,
    pub source_kind: String,
    pub source_name: String,
    pub raw_payload: Value,
    pub raw_payload_sha256: String,
    pub raw_payload_bytes: usize,
    pub server_provenance: Value,
    pub replay_readiness: String,
    pub warnings: Vec<String>,
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDiagnosticSymbolCandidate {
    pub symbol_id: i64,
    pub path: String,
    pub symbol: String,
    pub kind: String,
    pub language: Option<String>,
    pub line_start: Option<usize>,
    pub line_end: Option<usize>,
    pub enclosing_scope: Option<String>,
    pub signature_text: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDiagnosticLinkedSymbol {
    pub symbol_id: i64,
    pub path: String,
    pub symbol: String,
    pub kind: String,
    pub language: Option<String>,
    pub line_start: Option<usize>,
    pub line_end: Option<usize>,
    pub enclosing_scope: Option<String>,
    pub signature_text: Option<String>,
    pub link_strategy: String,
    pub evidence_source: String,
    pub selection_reason: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDiagnosticSymbolLinkResult {
    pub workspace_id: String,
    pub path: String,
    pub diagnostic_fingerprint: String,
    pub linked_symbol: Option<RustDiagnosticLinkedSymbol>,
    pub candidate_count: usize,
    pub evidence_source: String,
    pub selection_reason: String,
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

pub fn archive_diagnostics_payload_file(
    workspace_id: &str,
    input_path: &Path,
    source_kind: &str,
    source_name: &str,
    server_provenance_path: &Path,
) -> Result<RustDiagnosticsReplayArchive, std::io::Error> {
    let content = fs::read_to_string(input_path)?;
    let payload = serde_json::from_str::<Value>(&content).map_err(|err| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!("decode diagnostics JSON for replay archive: {err}"),
        )
    })?;
    let provenance_content = fs::read_to_string(server_provenance_path)?;
    let server_provenance = serde_json::from_str::<Value>(&provenance_content).map_err(|err| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!("decode diagnostics server provenance JSON: {err}"),
        )
    })?;
    Ok(archive_diagnostics_payload(
        workspace_id,
        content.as_bytes(),
        payload,
        source_kind,
        source_name,
        server_provenance,
    ))
}

pub fn archive_diagnostics_payload(
    workspace_id: &str,
    raw_payload_bytes: &[u8],
    payload: Value,
    source_kind: &str,
    source_name: &str,
    server_provenance: Value,
) -> RustDiagnosticsReplayArchive {
    let generated_at = Utc::now().to_rfc3339();
    let source_kind = normalize_source_kind(source_kind);
    let source_name = if source_name.trim().is_empty() {
        "unknown".to_string()
    } else {
        source_name.trim().to_string()
    };
    let mut hasher = Sha256::new();
    hasher.update(raw_payload_bytes);
    let mut warnings = Vec::new();
    if payload.is_null() {
        warnings.push("Archived diagnostics payload is null; replay cannot explain historical diagnostics content.".to_string());
    }
    if !server_provenance.is_object() {
        warnings.push("Server provenance is not an object; replay cannot explain the diagnostic capture source cleanly.".to_string());
    } else if source_kind == "lsp"
        && server_provenance
            .get("server_id")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .is_empty()
    {
        warnings.push("LSP server provenance is missing server_id; replay can show the payload but not the exact server identity.".to_string());
    }
    let replay_readiness = if warnings.is_empty() {
        "raw_payload_and_server_provenance_archived"
    } else {
        "raw_payload_archived_with_provenance_warnings"
    };
    RustDiagnosticsReplayArchive {
        workspace_id: workspace_id.to_string(),
        source_kind,
        source_name,
        raw_payload: payload,
        raw_payload_sha256: format!("{:x}", hasher.finalize()),
        raw_payload_bytes: raw_payload_bytes.len(),
        server_provenance,
        replay_readiness: replay_readiness.to_string(),
        warnings,
        generated_at,
    }
}

pub fn link_diagnostic_symbol_file(
    workspace_id: &str,
    diagnostic_path: &str,
    start_line: usize,
    end_line: usize,
    diagnostic_fingerprint: &str,
    candidates_path: &Path,
) -> Result<RustDiagnosticSymbolLinkResult, std::io::Error> {
    let content = fs::read_to_string(candidates_path)?;
    let payload = serde_json::from_str::<Value>(&content).map_err(|err| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!("decode diagnostic symbol candidates JSON: {err}"),
        )
    })?;
    Ok(link_diagnostic_symbol_payload(
        workspace_id,
        diagnostic_path,
        start_line,
        end_line,
        diagnostic_fingerprint,
        &payload,
    ))
}

pub fn link_diagnostic_symbol_payload(
    workspace_id: &str,
    diagnostic_path: &str,
    start_line: usize,
    end_line: usize,
    diagnostic_fingerprint: &str,
    payload: &Value,
) -> RustDiagnosticSymbolLinkResult {
    let generated_at = Utc::now().to_rfc3339();
    let mut warnings = Vec::new();
    let candidates = diagnostic_symbol_candidates(payload, &mut warnings);
    let linked_symbol =
        choose_diagnostic_symbol_link(diagnostic_path, start_line, end_line, &candidates);
    let selection_reason = if linked_symbol.is_some() {
        "Rust selected a conservative diagnostic-to-symbol link from durable diagnostic and symbol rows.".to_string()
    } else {
        "Rust left the diagnostic unlinked because durable symbol candidates did not produce one unambiguous conservative match.".to_string()
    };

    RustDiagnosticSymbolLinkResult {
        workspace_id: workspace_id.to_string(),
        path: diagnostic_path.trim().to_string(),
        diagnostic_fingerprint: diagnostic_fingerprint.trim().to_string(),
        linked_symbol,
        candidate_count: candidates.len(),
        evidence_source: "rust_diagnostic_symbol_link".to_string(),
        selection_reason,
        warnings,
        generated_at,
    }
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

fn diagnostic_symbol_candidates(
    payload: &Value,
    warnings: &mut Vec<String>,
) -> Vec<RustDiagnosticSymbolCandidate> {
    let items = if let Some(items) = payload.get("candidates").and_then(Value::as_array) {
        items
    } else if let Some(items) = payload.as_array() {
        items
    } else {
        warnings.push("No diagnostic symbol candidate array was provided.".to_string());
        return Vec::new();
    };
    let mut out = Vec::new();
    for item in items {
        match serde_json::from_value::<RustDiagnosticSymbolCandidate>(item.clone()) {
            Ok(candidate) => out.push(candidate),
            Err(err) => warnings.push(format!(
                "Skipped invalid diagnostic symbol candidate: {err}"
            )),
        }
    }
    out
}

fn choose_diagnostic_symbol_link(
    diagnostic_path: &str,
    start_line: usize,
    end_line: usize,
    candidates: &[RustDiagnosticSymbolCandidate],
) -> Option<RustDiagnosticLinkedSymbol> {
    let path = diagnostic_path.trim();
    let exact: Vec<&RustDiagnosticSymbolCandidate> = candidates
        .iter()
        .filter(|item| item.path == path && item.line_start == Some(start_line))
        .collect();
    if exact.len() == 1 {
        return Some(candidate_link(
            exact[0],
            "diagnostic_start_line_exact_symbol_anchor",
            "The diagnostic starts on exactly one durable symbol anchor line.",
        ));
    }
    if exact.len() > 1 {
        return None;
    }

    let normalized_end = end_line.max(start_line);
    let mut enclosing: Vec<&RustDiagnosticSymbolCandidate> = candidates
        .iter()
        .filter(|item| {
            item.path == path
                && item
                    .line_start
                    .zip(item.line_end)
                    .map(|(symbol_start, symbol_end)| {
                        symbol_start <= start_line && symbol_end >= normalized_end
                    })
                    .unwrap_or(false)
        })
        .collect();
    if enclosing.is_empty() {
        return None;
    }
    enclosing.sort_by_key(|item| {
        let start = item.line_start.unwrap_or(usize::MAX);
        let end = item.line_end.unwrap_or(usize::MAX);
        end.saturating_sub(start)
    });
    let best_span = enclosing[0]
        .line_start
        .zip(enclosing[0].line_end)
        .map(|(start, end)| end.saturating_sub(start));
    let narrowest: Vec<&RustDiagnosticSymbolCandidate> = enclosing
        .into_iter()
        .filter(|item| {
            item.line_start
                .zip(item.line_end)
                .map(|(start, end)| Some(end.saturating_sub(start)) == best_span)
                .unwrap_or(false)
        })
        .collect();
    if narrowest.len() == 1 {
        return Some(candidate_link(
            narrowest[0],
            "diagnostic_range_unique_narrowest_symbol",
            "The diagnostic range is enclosed by exactly one narrowest durable symbol range.",
        ));
    }
    None
}

fn candidate_link(
    candidate: &RustDiagnosticSymbolCandidate,
    strategy: &str,
    reason: &str,
) -> RustDiagnosticLinkedSymbol {
    RustDiagnosticLinkedSymbol {
        symbol_id: candidate.symbol_id,
        path: candidate.path.clone(),
        symbol: candidate.symbol.clone(),
        kind: candidate.kind.clone(),
        language: candidate.language.clone(),
        line_start: candidate.line_start,
        line_end: candidate.line_end,
        enclosing_scope: candidate.enclosing_scope.clone(),
        signature_text: candidate.signature_text.clone(),
        link_strategy: strategy.to_string(),
        evidence_source: "rust_diagnostic_symbol_link".to_string(),
        selection_reason: reason.to_string(),
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

    #[test]
    fn archives_raw_diagnostics_payload_with_server_provenance() {
        let payload = json!({
            "uri": "file:///tmp/project/src/app.ts",
            "diagnostics": [{
                "range": {
                    "start": {"line": 1, "character": 4},
                    "end": {"line": 1, "character": 12}
                },
                "severity": 1,
                "message": "Cannot find name 'thing'."
            }]
        });
        let raw = serde_json::to_vec(&payload).unwrap();
        let archive = super::archive_diagnostics_payload(
            "workspace-1",
            &raw,
            payload.clone(),
            "lsp",
            "typescript-language-server",
            json!({
                "source_mode": "input_file",
                "server_id": "typescript-language-server",
                "language_id": "typescript",
                "input_path": "diagnostics.json"
            }),
        );

        assert_eq!(archive.workspace_id, "workspace-1");
        assert_eq!(archive.source_kind, "lsp");
        assert_eq!(archive.source_name, "typescript-language-server");
        assert_eq!(archive.raw_payload, payload);
        assert_eq!(archive.raw_payload_bytes, raw.len());
        assert_eq!(archive.raw_payload_sha256.len(), 64);
        assert_eq!(
            archive.replay_readiness,
            "raw_payload_and_server_provenance_archived"
        );
        assert!(archive.warnings.is_empty());
    }

    #[test]
    fn links_diagnostic_to_unique_exact_symbol_anchor() {
        let payload = json!([
            {
                "symbol_id": 21,
                "path": "src/app.py",
                "symbol": "ExportService",
                "kind": "class",
                "language": "python",
                "line_start": 1,
                "line_end": 12,
                "enclosing_scope": null,
                "signature_text": "class ExportService:"
            }
        ]);

        let result = super::link_diagnostic_symbol_payload(
            "workspace-1",
            "src/app.py",
            1,
            1,
            "diagfp",
            &payload,
        );

        let link = result.linked_symbol.expect("expected linked symbol");
        assert_eq!(link.symbol, "ExportService");
        assert_eq!(
            link.link_strategy,
            "diagnostic_start_line_exact_symbol_anchor"
        );
        assert_eq!(link.evidence_source, "rust_diagnostic_symbol_link");
    }

    #[test]
    fn links_diagnostic_to_unique_narrowest_enclosing_symbol() {
        let payload = json!([
            {
                "symbol_id": 21,
                "path": "src/app.py",
                "symbol": "ExportService",
                "kind": "class",
                "language": "python",
                "line_start": 1,
                "line_end": 20,
                "enclosing_scope": null,
                "signature_text": "class ExportService:"
            },
            {
                "symbol_id": 22,
                "path": "src/app.py",
                "symbol": "run",
                "kind": "method",
                "language": "python",
                "line_start": 5,
                "line_end": 8,
                "enclosing_scope": "ExportService",
                "signature_text": "def run(self):"
            }
        ]);

        let result = super::link_diagnostic_symbol_payload(
            "workspace-1",
            "src/app.py",
            6,
            6,
            "diagfp",
            &payload,
        );

        let link = result.linked_symbol.expect("expected linked symbol");
        assert_eq!(link.symbol, "run");
        assert_eq!(
            link.link_strategy,
            "diagnostic_range_unique_narrowest_symbol"
        );
    }

    #[test]
    fn refuses_ambiguous_symbol_links() {
        let payload = json!([
            {
                "symbol_id": 21,
                "path": "src/app.py",
                "symbol": "ExportService",
                "kind": "class",
                "language": "python",
                "line_start": 1,
                "line_end": 5,
                "enclosing_scope": null,
                "signature_text": null
            },
            {
                "symbol_id": 22,
                "path": "src/app.py",
                "symbol": "ExportFactory",
                "kind": "class",
                "language": "python",
                "line_start": 1,
                "line_end": 5,
                "enclosing_scope": null,
                "signature_text": null
            }
        ]);

        let result = super::link_diagnostic_symbol_payload(
            "workspace-1",
            "src/app.py",
            1,
            1,
            "diagfp",
            &payload,
        );

        assert!(result.linked_symbol.is_none());
        assert_eq!(result.candidate_count, 2);
        assert!(result.selection_reason.contains("unambiguous"));
    }
}
