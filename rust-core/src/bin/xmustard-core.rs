use std::env;
use std::fs;
use std::path::{Path, PathBuf};

fn main() {
    let mut args = env::args().skip(1);
    let Some(command) = args.next() else {
        eprintln!("usage: xmustard-core <command> [args]");
        std::process::exit(2);
    };

    match command.as_str() {
        "scan-signals" => {
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core scan-signals <root_path>");
                std::process::exit(2);
            };
            let root_path = PathBuf::from(root);
            match xmustard_core::scanner::scan_repo_signals(&root_path) {
                Ok(signals) => {
                    println!(
                        "{}",
                        serde_json::to_string(&signals).expect("scanner result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("scan-signals failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "build-repo-map" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core build-repo-map <workspace_id> <root_path>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core build-repo-map <workspace_id> <root_path>");
                std::process::exit(2);
            };
            let root_path = PathBuf::from(root);
            match xmustard_core::repomap::build_repo_map(&root_path, &workspace_id) {
                Ok(summary) => {
                    println!(
                        "{}",
                        serde_json::to_string(&summary).expect("repo-map result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("build-repo-map failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "semantic-impact" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core semantic-impact <workspace_id> <root_path> <changes_json_path>"
                );
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core semantic-impact <workspace_id> <root_path> <changes_json_path>"
                );
                std::process::exit(2);
            };
            let Some(changes_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core semantic-impact <workspace_id> <root_path> <changes_json_path>"
                );
                std::process::exit(2);
            };
            let changes_content = match fs::read_to_string(&changes_json_path) {
                Ok(content) => content,
                Err(err) => {
                    eprintln!("semantic-impact failed to read changes: {err}");
                    std::process::exit(1);
                }
            };
            let changes = match serde_json::from_str::<Vec<xmustard_core::repomap::RustRepoChangeRecord>>(&changes_content) {
                Ok(changes) => changes,
                Err(err) => {
                    eprintln!("semantic-impact failed to decode changes: {err}");
                    std::process::exit(1);
                }
            };
            let root_path = PathBuf::from(root);
            match xmustard_core::repomap::build_semantic_impact(
                &root_path,
                &workspace_id,
                &changes,
            ) {
                Ok(report) => {
                    println!(
                        "{}",
                        serde_json::to_string(&report)
                            .expect("semantic impact result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("semantic-impact failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "path-symbols" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core path-symbols <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core path-symbols <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            let Some(relative_path) = args.next() else {
                eprintln!("usage: xmustard-core path-symbols <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            match xmustard_core::repomap::extract_path_symbols(
                &PathBuf::from(root),
                &workspace_id,
                &relative_path,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("path symbols result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("path-symbols failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "explain-path" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core explain-path <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core explain-path <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            let Some(relative_path) = args.next() else {
                eprintln!("usage: xmustard-core explain-path <workspace_id> <root_path> <relative_path>");
                std::process::exit(2);
            };
            match xmustard_core::repomap::explain_path(
                &PathBuf::from(root),
                &workspace_id,
                &relative_path,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("path explainer result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("explain-path failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "normalize-diagnostics" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core normalize-diagnostics <workspace_id> <root_path> <input_json_path> <source_kind> <source_name>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core normalize-diagnostics <workspace_id> <root_path> <input_json_path> <source_kind> <source_name>");
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!("usage: xmustard-core normalize-diagnostics <workspace_id> <root_path> <input_json_path> <source_kind> <source_name>");
                std::process::exit(2);
            };
            let source_kind = args.next().unwrap_or_else(|| "lsp".to_string());
            let source_name = args.next().unwrap_or_else(|| "unknown".to_string());
            match xmustard_core::diagnostics::normalize_diagnostics_file(
                &workspace_id,
                &PathBuf::from(root),
                &PathBuf::from(input_json_path),
                &source_kind,
                &source_name,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("diagnostics result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("normalize-diagnostics failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "archive-diagnostics-payload" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core archive-diagnostics-payload <workspace_id> <input_json_path> <source_kind> <source_name> <server_provenance_json_path>");
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!("usage: xmustard-core archive-diagnostics-payload <workspace_id> <input_json_path> <source_kind> <source_name> <server_provenance_json_path>");
                std::process::exit(2);
            };
            let source_kind = args.next().unwrap_or_else(|| "lsp".to_string());
            let source_name = args.next().unwrap_or_else(|| "unknown".to_string());
            let Some(server_provenance_json_path) = args.next() else {
                eprintln!("usage: xmustard-core archive-diagnostics-payload <workspace_id> <input_json_path> <source_kind> <source_name> <server_provenance_json_path>");
                std::process::exit(2);
            };
            match xmustard_core::diagnostics::archive_diagnostics_payload_file(
                &workspace_id,
                &PathBuf::from(input_json_path),
                &source_kind,
                &source_name,
                &PathBuf::from(server_provenance_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("diagnostics replay archive should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("archive-diagnostics-payload failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "link-diagnostic-symbol" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let Some(diagnostic_path) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let Some(start_line_raw) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let Some(end_line_raw) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let Some(diagnostic_fingerprint) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let Some(candidates_json_path) = args.next() else {
                eprintln!("usage: xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>");
                std::process::exit(2);
            };
            let start_line = match start_line_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("link-diagnostic-symbol invalid start_line: {err}");
                    std::process::exit(2);
                }
            };
            let end_line = match end_line_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("link-diagnostic-symbol invalid end_line: {err}");
                    std::process::exit(2);
                }
            };
            match xmustard_core::diagnostics::link_diagnostic_symbol_file(
                &workspace_id,
                &diagnostic_path,
                start_line,
                end_line,
                &diagnostic_fingerprint,
                &PathBuf::from(candidates_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("diagnostic symbol link result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("link-diagnostic-symbol failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "normalize-lsp-definition" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(relative_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(line_raw) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(column_raw) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(source_name) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-definition <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let line = match line_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("normalize-lsp-definition invalid line: {err}");
                    std::process::exit(2);
                }
            };
            let column = match column_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("normalize-lsp-definition invalid column: {err}");
                    std::process::exit(2);
                }
            };
            match xmustard_core::lsp::normalize_definition_file(
                &workspace_id,
                &PathBuf::from(root),
                &relative_path,
                line,
                column,
                &source_name,
                &PathBuf::from(input_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("LSP definition result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("normalize-lsp-definition failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "normalize-lsp-references" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(relative_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(line_raw) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(column_raw) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(source_name) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-references <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let line = match line_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("normalize-lsp-references invalid line: {err}");
                    std::process::exit(2);
                }
            };
            let column = match column_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("normalize-lsp-references invalid column: {err}");
                    std::process::exit(2);
                }
            };
            match xmustard_core::lsp::normalize_references_file(
                &workspace_id,
                &PathBuf::from(root),
                &relative_path,
                line,
                column,
                &source_name,
                &PathBuf::from(input_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("LSP references result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("normalize-lsp-references failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "normalize-lsp-document-symbols" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(relative_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(source_name) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>"
                );
                std::process::exit(2);
            };
            match xmustard_core::lsp::normalize_document_symbols_file(
                &workspace_id,
                &PathBuf::from(root),
                &relative_path,
                &source_name,
                &PathBuf::from(input_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("LSP document-symbols result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("normalize-lsp-document-symbols failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "normalize-lsp-workspace-symbols" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let Some(root) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let Some(query) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let Some(limit_raw) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let Some(source_name) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let Some(input_json_path) = args.next() else {
                eprintln!("usage: xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>");
                std::process::exit(2);
            };
            let limit = match limit_raw.parse::<usize>() {
                Ok(value) => value,
                Err(err) => {
                    eprintln!("normalize-lsp-workspace-symbols invalid limit: {err}");
                    std::process::exit(2);
                }
            };
            match xmustard_core::lsp::normalize_workspace_symbols_file(
                &workspace_id,
                &PathBuf::from(root),
                &query,
                limit,
                &source_name,
                &PathBuf::from(input_json_path),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result).expect("LSP workspace-symbols result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("normalize-lsp-workspace-symbols failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "parse-coverage-lcov" => {
            let Some(workspace_id) = args.next() else {
                eprintln!(
                    "usage: xmustard-core parse-coverage-lcov <workspace_id> <report_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let Some(report_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core parse-coverage-lcov <workspace_id> <report_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            match xmustard_core::verification::parse_lcov_file(
                &PathBuf::from(&report_path),
                &workspace_id,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result).expect("coverage result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("parse-coverage-lcov failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "parse-coverage" => {
            let Some(workspace_id) = args.next() else {
                eprintln!("usage: xmustard-core parse-coverage <workspace_id> <report_path> [run_id] [issue_id]");
                std::process::exit(2);
            };
            let Some(report_path) = args.next() else {
                eprintln!("usage: xmustard-core parse-coverage <workspace_id> <report_path> [run_id] [issue_id]");
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            match xmustard_core::verification::parse_coverage_file(
                &PathBuf::from(&report_path),
                &workspace_id,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result).expect("coverage result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("parse-coverage failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "run-verification-command" => {
            let Some(workspace_root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let Some(timeout_seconds) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let Some(command_text) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>"
                );
                std::process::exit(2);
            };
            let timeout_seconds = timeout_seconds.parse::<u64>().unwrap_or(30);
            match xmustard_core::verification::run_verification_command(
                &PathBuf::from(&workspace_root),
                &command_text,
                timeout_seconds,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("verification command result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("run-verification-command failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "run-managed-command" => {
            let Some(workspace_root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-managed-command <workspace_root> <timeout_seconds> <program> [args...]"
                );
                std::process::exit(2);
            };
            let Some(timeout_seconds) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-managed-command <workspace_root> <timeout_seconds> <program> [args...]"
                );
                std::process::exit(2);
            };
            let Some(program) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-managed-command <workspace_root> <timeout_seconds> <program> [args...]"
                );
                std::process::exit(2);
            };
            let timeout_seconds = timeout_seconds.parse::<u64>().unwrap_or(30);
            let mut command_args = vec![program];
            command_args.extend(args);
            match xmustard_core::verification::run_managed_command(
                &PathBuf::from(&workspace_root),
                &command_args,
                timeout_seconds,
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("managed command result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("run-managed-command failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "run-verification-profile" => {
            let Some(workspace_root) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-profile <workspace_root> <profile_json_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let Some(profile_json_path) = args.next() else {
                eprintln!(
                    "usage: xmustard-core run-verification-profile <workspace_root> <profile_json_path> [run_id] [issue_id]"
                );
                std::process::exit(2);
            };
            let run_id = args.next();
            let issue_id = args.next();
            let profile_content = match fs::read_to_string(&profile_json_path) {
                Ok(content) => content,
                Err(err) => {
                    eprintln!("run-verification-profile failed to read profile: {err}");
                    std::process::exit(1);
                }
            };
            let profile = match serde_json::from_str::<xmustard_core::verification::RustVerificationProfileInput>(&profile_content) {
                Ok(profile) => profile,
                Err(err) => {
                    eprintln!("run-verification-profile failed to decode profile: {err}");
                    std::process::exit(1);
                }
            };
            match xmustard_core::verification::run_verification_profile(
                &PathBuf::from(&workspace_root),
                &profile,
                run_id.as_deref(),
                issue_id.as_deref(),
            ) {
                Ok(result) => {
                    println!(
                        "{}",
                        serde_json::to_string(&result)
                            .expect("verification profile result should serialize")
                    );
                }
                Err(err) => {
                    eprintln!("run-verification-profile failed: {err}");
                    std::process::exit(1);
                }
            }
        }
        "describe-architecture" => {
            println!(
                "{}",
                serde_json::to_string(&xmustard_core::contracts::no_python_architecture_contract())
                    .expect("architecture contract should serialize")
            );
        }
        "goal" => {
            run_goal_command(args);
        }
        _ => {
            eprintln!("unknown command: {command}");
            std::process::exit(2);
        }
    }
}

/// Dispatch the `goal` subcommand family over the durable goal runtime.
/// All read JSON output goes to stdout; markdown projections print verbatim.
fn run_goal_command(mut args: impl Iterator<Item = String>) {
    use xmustard_core::goalruntime as goal;

    fn next_or_exit(value: Option<String>, usage: &str) -> String {
        match value {
            Some(value) => value,
            None => {
                eprintln!("usage: {usage}");
                std::process::exit(2);
            }
        }
    }

    fn read_json_request<T: serde::de::DeserializeOwned>(path: &str) -> T {
        let content = match fs::read_to_string(path) {
            Ok(content) => content,
            Err(err) => {
                eprintln!("goal: failed to read request {path}: {err}");
                std::process::exit(1);
            }
        };
        match serde_json::from_str::<T>(&content) {
            Ok(value) => value,
            Err(err) => {
                eprintln!("goal: failed to decode request {path}: {err}");
                std::process::exit(1);
            }
        }
    }

    fn fail(err: xmustard_core::goalruntime::GoalError) -> ! {
        eprintln!("goal: {err}");
        // Exit code 3 marks a guardrail/gate refusal so callers can branch on it.
        let code = match err {
            xmustard_core::goalruntime::GoalError::CompletionBlocked(_)
            | xmustard_core::goalruntime::GoalError::SlopBlocked(_) => 3,
            xmustard_core::goalruntime::GoalError::NotFound(_) => 4,
            _ => 1,
        };
        std::process::exit(code);
    }

    fn print_json<T: serde::Serialize>(value: &T) {
        println!(
            "{}",
            serde_json::to_string(value).expect("goal result should serialize")
        );
    }

    let Some(sub) = args.next() else {
        eprintln!(
            "usage: xmustard-core goal <list|get|create|iterate|status|ledger|context|lint> ..."
        );
        std::process::exit(2);
    };

    match sub.as_str() {
        "list" => {
            let data_dir = next_or_exit(args.next(), "xmustard-core goal list <data_dir> <workspace_id>");
            let workspace_id =
                next_or_exit(args.next(), "xmustard-core goal list <data_dir> <workspace_id>");
            match goal::list_goals(Path::new(&data_dir), &workspace_id) {
                Ok(goals) => print_json(&goals),
                Err(err) => fail(err),
            }
        }
        "get" => {
            let data_dir =
                next_or_exit(args.next(), "xmustard-core goal get <data_dir> <workspace_id> <goal_id>");
            let workspace_id =
                next_or_exit(args.next(), "xmustard-core goal get <data_dir> <workspace_id> <goal_id>");
            let goal_id =
                next_or_exit(args.next(), "xmustard-core goal get <data_dir> <workspace_id> <goal_id>");
            match goal::get_goal(Path::new(&data_dir), &workspace_id, &goal_id) {
                Ok(record) => print_json(&record),
                Err(err) => fail(err),
            }
        }
        "create" => {
            let usage = "xmustard-core goal create <data_dir> <workspace_id> <request_json_path>";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let request_path = next_or_exit(args.next(), usage);
            let request: goal::GoalCreateRequest = read_json_request(&request_path);
            match goal::create_goal(Path::new(&data_dir), &workspace_id, &request) {
                Ok((record, report)) => print_json(&serde_json::json!({
                    "goal": record,
                    "slop": report,
                })),
                Err(err) => fail(err),
            }
        }
        "iterate" => {
            let usage =
                "xmustard-core goal iterate <data_dir> <workspace_id> <goal_id> <request_json_path>";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let goal_id = next_or_exit(args.next(), usage);
            let request_path = next_or_exit(args.next(), usage);
            let request: goal::GoalIterationAppendRequest = read_json_request(&request_path);
            match goal::append_iteration(Path::new(&data_dir), &workspace_id, &goal_id, &request) {
                Ok((record, report)) => print_json(&serde_json::json!({
                    "iteration": record,
                    "slop": report,
                })),
                Err(err) => fail(err),
            }
        }
        "status" => {
            let usage =
                "xmustard-core goal status <data_dir> <workspace_id> <goal_id> <status> [skip_reason]";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let goal_id = next_or_exit(args.next(), usage);
            let status_raw = next_or_exit(args.next(), usage);
            let skip_reason = args.next().unwrap_or_default();
            let status = match goal::GoalStatus::parse(&status_raw) {
                Ok(status) => status,
                Err(err) => fail(err),
            };
            match goal::update_status(
                Path::new(&data_dir),
                &workspace_id,
                &goal_id,
                status,
                &skip_reason,
            ) {
                Ok(record) => print_json(&record),
                Err(err) => fail(err),
            }
        }
        "ledger" => {
            let usage = "xmustard-core goal ledger <data_dir> <workspace_id> <goal_id>";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let goal_id = next_or_exit(args.next(), usage);
            match goal::read_ledger(Path::new(&data_dir), &workspace_id, &goal_id) {
                Ok(markdown) => print!("{markdown}"),
                Err(err) => fail(err),
            }
        }
        "context" => {
            let usage = "xmustard-core goal context <data_dir> <workspace_id> <goal_id>";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let goal_id = next_or_exit(args.next(), usage);
            match goal::build_context_packet(Path::new(&data_dir), &workspace_id, &goal_id) {
                Ok(packet) => print!("{packet}"),
                Err(err) => fail(err),
            }
        }
        "lint" => {
            let usage = "xmustard-core goal lint <data_dir> <workspace_id> <goal_id>";
            let data_dir = next_or_exit(args.next(), usage);
            let workspace_id = next_or_exit(args.next(), usage);
            let goal_id = next_or_exit(args.next(), usage);
            match goal::lint_goal(Path::new(&data_dir), &workspace_id, &goal_id) {
                Ok(report) => print_json(&report),
                Err(err) => fail(err),
            }
        }
        other => {
            eprintln!("unknown goal subcommand: {other}");
            std::process::exit(2);
        }
    }
}
