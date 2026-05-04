use chrono::Utc;
use percent_encoding::percent_decode_str;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDefinitionLocation {
    pub path: String,
    pub target_line_start: usize,
    pub target_column_start: usize,
    pub target_line_end: usize,
    pub target_column_end: usize,
    pub selection_line_start: Option<usize>,
    pub selection_column_start: Option<usize>,
    pub selection_line_end: Option<usize>,
    pub selection_column_end: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RustDefinitionResult {
    pub workspace_id: String,
    pub path: String,
    pub line: usize,
    pub column: usize,
    pub source_name: String,
    pub evidence_source: String,
    pub selection_reason: String,
    pub definition_count: usize,
    pub definitions: Vec<RustDefinitionLocation>,
    pub warnings: Vec<String>,
    pub generated_at: String,
}

pub fn normalize_definition_file(
    workspace_id: &str,
    root_path: &Path,
    relative_path: &str,
    line: usize,
    column: usize,
    source_name: &str,
    input_path: &Path,
) -> Result<RustDefinitionResult, std::io::Error> {
    let content = fs::read_to_string(input_path)?;
    let payload = serde_json::from_str::<Value>(&content).map_err(|err| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!("decode LSP definition JSON: {err}"),
        )
    })?;
    Ok(normalize_definition_payload(
        workspace_id,
        root_path,
        relative_path,
        line,
        column,
        source_name,
        &payload,
    ))
}

pub fn normalize_definition_payload(
    workspace_id: &str,
    root_path: &Path,
    relative_path: &str,
    line: usize,
    column: usize,
    source_name: &str,
    payload: &Value,
) -> RustDefinitionResult {
    let generated_at = Utc::now().to_rfc3339();
    let source_name = normalize_source_name(source_name);
    let mut warnings = Vec::new();
    let mut definitions = Vec::new();

    match payload {
        Value::Null => warnings.push("LSP server returned no definition locations.".to_string()),
        Value::Array(items) => {
            for item in items {
                if let Some(location) = definition_location(root_path, item, &mut warnings) {
                    definitions.push(location);
                }
            }
        }
        Value::Object(_) => {
            if let Some(location) = definition_location(root_path, payload, &mut warnings) {
                definitions.push(location);
            }
        }
        _ => warnings.push("Skipped unexpected non-object LSP definition payload.".to_string()),
    }

    if definitions.is_empty() {
        warnings.push(
            "No in-workspace definition locations were normalized from the LSP response."
                .to_string(),
        );
    }

    RustDefinitionResult {
        workspace_id: workspace_id.to_string(),
        path: relative_path.trim().to_string(),
        line: normalize_coordinate(line),
        column: normalize_coordinate(column),
        source_name,
        evidence_source: "rust_lsp_definition".to_string(),
        selection_reason:
            "Rust normalized live LSP definition output from a Go-managed workspace session."
                .to_string(),
        definition_count: definitions.len(),
        definitions,
        warnings,
        generated_at,
    }
}

fn definition_location(
    root_path: &Path,
    payload: &Value,
    warnings: &mut Vec<String>,
) -> Option<RustDefinitionLocation> {
    let uri_value = payload
        .get("targetUri")
        .or_else(|| payload.get("uri"))
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|item| !item.is_empty());
    let Some(uri_value) = uri_value else {
        warnings.push("Skipped LSP definition item without a URI.".to_string());
        return None;
    };

    let path = match normalize_file_uri(root_path, uri_value) {
        Ok(Some(path)) => path,
        Ok(None) => {
            warnings.push(format!(
                "Skipped LSP definition outside workspace root: {uri_value}"
            ));
            return None;
        }
        Err(err) => {
            warnings.push(err);
            return None;
        }
    };

    let target_range = payload
        .get("targetRange")
        .or_else(|| payload.get("range"))
        .unwrap_or(&Value::Null);
    let target_start = lsp_position(target_range.get("start"));
    let target_end = lsp_position(target_range.get("end"));
    let selection_range = payload
        .get("targetSelectionRange")
        .or_else(|| payload.get("selectionRange"));

    let selection = selection_range.map(|range| {
        (
            lsp_position(range.get("start")),
            lsp_position(range.get("end")),
        )
    });

    Some(RustDefinitionLocation {
        path,
        target_line_start: target_start.0,
        target_column_start: target_start.1,
        target_line_end: target_end.0,
        target_column_end: target_end.1,
        selection_line_start: selection.map(|item| item.0.0),
        selection_column_start: selection.map(|item| item.0.1),
        selection_line_end: selection.map(|item| item.1.0),
        selection_column_end: selection.map(|item| item.1.1),
    })
}

fn normalize_file_uri(root_path: &Path, value: &str) -> Result<Option<String>, String> {
    let Some(raw_path) = value.strip_prefix("file://") else {
        return Err(format!(
            "Unsupported non-file definition URI '{value}' returned by LSP server."
        ));
    };
    let decoded = percent_decode_str(raw_path)
        .decode_utf8()
        .map_err(|err| format!("Failed to decode LSP file URI '{value}': {err}"))?;
    let file_path = PathBuf::from(decoded.as_ref());
    let relative = match file_path.strip_prefix(root_path) {
        Ok(item) => item,
        Err(_) => return Ok(None),
    };
    Ok(Some(relative.to_string_lossy().replace('\\', "/")))
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

fn normalize_coordinate(value: usize) -> usize {
    if value == 0 { 1 } else { value }
}

fn normalize_source_name(value: &str) -> String {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        "unknown".to_string()
    } else {
        trimmed.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::{RustDefinitionLocation, normalize_definition_payload};
    use serde_json::json;
    use std::path::Path;

    #[test]
    fn normalizes_definition_locations_from_lsp_response() {
        let root = Path::new("/repo");
        let payload = json!([
            {
                "uri": "file:///repo/src/app.py",
                "range": {
                    "start": {"line": 3, "character": 4},
                    "end": {"line": 7, "character": 2}
                }
            },
            {
                "targetUri": "file:///repo/src/service.py",
                "targetRange": {
                    "start": {"line": 10, "character": 1},
                    "end": {"line": 10, "character": 18}
                },
                "targetSelectionRange": {
                    "start": {"line": 10, "character": 5},
                    "end": {"line": 10, "character": 18}
                }
            }
        ]);

        let result = normalize_definition_payload(
            "workspace-1",
            root,
            "src/app.py",
            2,
            7,
            "pyright",
            &payload,
        );

        assert_eq!(result.workspace_id, "workspace-1");
        assert_eq!(result.path, "src/app.py");
        assert_eq!(result.line, 2);
        assert_eq!(result.column, 7);
        assert_eq!(result.source_name, "pyright");
        assert_eq!(result.evidence_source, "rust_lsp_definition");
        assert_eq!(result.definition_count, 2);
        assert_eq!(
            result.definitions,
            vec![
                RustDefinitionLocation {
                    path: "src/app.py".to_string(),
                    target_line_start: 4,
                    target_column_start: 5,
                    target_line_end: 8,
                    target_column_end: 3,
                    selection_line_start: None,
                    selection_column_start: None,
                    selection_line_end: None,
                    selection_column_end: None,
                },
                RustDefinitionLocation {
                    path: "src/service.py".to_string(),
                    target_line_start: 11,
                    target_column_start: 2,
                    target_line_end: 11,
                    target_column_end: 19,
                    selection_line_start: Some(11),
                    selection_column_start: Some(6),
                    selection_line_end: Some(11),
                    selection_column_end: Some(19),
                },
            ]
        );
        assert!(result.warnings.is_empty());
    }

    #[test]
    fn definition_contract_rejects_symbol_materialization_payloads() {
        let root = Path::new("/repo");
        let payload = json!([
            {
                "symbol_id": 21,
                "path": "src/app.py",
                "symbol": "ExportService",
                "kind": "class"
            }
        ]);

        let result = normalize_definition_payload(
            "workspace-1",
            root,
            "src/app.py",
            1,
            1,
            "pyright",
            &payload,
        );

        assert_eq!(result.definition_count, 0);
        assert!(
            result
                .warnings
                .iter()
                .any(|item| item.contains("without a URI"))
        );
        assert!(
            result
                .warnings
                .iter()
                .any(|item| item.contains("No in-workspace definition locations"))
        );
    }
}
