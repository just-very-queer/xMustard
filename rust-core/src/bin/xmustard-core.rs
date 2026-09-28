#![forbid(unsafe_code)]

use std::env;
use std::fs;
use std::path::{Path, PathBuf};

use xmustard_core::dispatch::{
    self, Args, CmdError, CmdResult, Command, Output, Residency, json, need,
};

/// Every subcommand. The one-shot CLI and `xmustard-core serve` dispatch through this
/// table, so a new subcommand is one entry here plus its handler. `residency` says
/// whether the resident worker may run it in-process (see `dispatch::Residency`).
const COMMANDS: &[Command] = &[
    cmd("scan-signals", Residency::Resident, scan_signals),
    cmd("build-repo-map", Residency::Resident, build_repo_map),
    cmd("semantic-impact", Residency::Resident, semantic_impact),
    cmd("path-symbols", Residency::Resident, path_symbols),
    cmd("explain-path", Residency::Resident, explain_path),
    cmd(
        "normalize-diagnostics",
        Residency::Resident,
        normalize_diagnostics,
    ),
    cmd(
        "archive-diagnostics-payload",
        Residency::Resident,
        archive_diagnostics_payload,
    ),
    cmd(
        "link-diagnostic-symbol",
        Residency::Resident,
        link_diagnostic_symbol,
    ),
    cmd(
        "normalize-lsp-definition",
        Residency::Resident,
        normalize_lsp_definition,
    ),
    cmd(
        "normalize-lsp-references",
        Residency::Resident,
        normalize_lsp_references,
    ),
    cmd(
        "normalize-lsp-document-symbols",
        Residency::Resident,
        normalize_lsp_document_symbols,
    ),
    cmd(
        "lsp-document-symbols",
        Residency::OneShot,
        lsp_document_symbols,
    ),
    cmd("lsp-hover", Residency::OneShot, lsp_hover),
    cmd(
        "normalize-lsp-workspace-symbols",
        Residency::Resident,
        normalize_lsp_workspace_symbols,
    ),
    cmd(
        "parse-coverage-lcov",
        Residency::Resident,
        parse_coverage_lcov,
    ),
    cmd("parse-coverage", Residency::Resident, parse_coverage),
    cmd(
        "run-verification-command",
        Residency::OneShot,
        run_verification_command,
    ),
    cmd(
        "run-managed-command",
        Residency::OneShot,
        run_managed_command,
    ),
    cmd(
        "run-verification-profile",
        Residency::OneShot,
        run_verification_profile,
    ),
    cmd("goal", Residency::OneShot, run_goal_command),
    cmd("semantic-search", Residency::OneShot, semantic_search),
    // Whole-repository work runs one-shot so its transient heap leaves with the process
    // (PAR-RT-02): measured on pi-mono, one `changetrack index` left a serve worker at
    // 36.8 MiB instead of the 24.4 MiB query-only plateau. `symbolgraph build`
    // rebuilds and prints the full graph; blast-radius reads every tracked source file
    // on each call and uses no cached graph, so residency saves it nothing. Neither is
    // on a nine-tool query path. Queries over the graph (impact, trace, clusters,
    // hotspots, ownership subsystems) read the shared snapshot and stay resident.
    cmd(
        "changetrack",
        Residency::ResidentExcept(&["index"]),
        run_changetrack_command,
    ),
    // Builds or refreshes the durable per-workspace SQLite index: whole-repository
    // work that writes owner-only state, so it keeps the per-call process boundary.
    cmd("index", Residency::OneShot, xmustard_core::index::run),
    cmd(
        "symbolgraph",
        Residency::ResidentExcept(&["build", "blast-radius"]),
        run_symbolgraph_command,
    ),
    cmd("ownership", Residency::Resident, ownership),
    cmd("search", Residency::Resident, search),
    cmd("repo-key", Residency::Resident, repo_key),
    cmd("wiki", Residency::Resident, wiki),
    // The watcher's orchestrator side (WS-15): start/stop watching a root, take a due
    // refresh batch, report it done. Runs inside serve, where the watchers live.
    cmd(
        "watch",
        Residency::Resident,
        xmustard_core::index::watch::run,
    ),
];

const fn cmd(name: &'static str, residency: Residency, run: fn(Args) -> CmdResult) -> Command {
    Command {
        name,
        residency,
        run,
    }
}

fn main() {
    let mut args = env::args().skip(1);
    let Some(command) = args.next() else {
        eprintln!("usage: xmustard-core <command> [args]");
        std::process::exit(2);
    };
    if command == "serve" {
        std::process::exit(xmustard_core::serve::run_stdio(COMMANDS, args.collect()));
    }
    let Some(entry) = dispatch::find(COMMANDS, &command) else {
        eprintln!("unknown command: {command}");
        std::process::exit(2);
    };
    match (entry.run)(args.collect::<Vec<_>>().into_iter()) {
        Ok(Output::Json(body)) => println!("{body}"),
        Ok(Output::Text(body)) => print!("{body}"),
        Err(err) => {
            eprintln!("{}", err.message);
            std::process::exit(err.code);
        }
    }
}

fn scan_signals(mut args: Args) -> CmdResult {
    let root = need(&mut args, "xmustard-core scan-signals <root_path>")?;
    match xmustard_core::scanner::scan_repo_signals(&PathBuf::from(root)) {
        Ok(signals) => json(&signals),
        Err(err) => Err(CmdError::failed(format!("scan-signals failed: {err}"))),
    }
}

fn build_repo_map(mut args: Args) -> CmdResult {
    let usage = "xmustard-core build-repo-map <workspace_id> <root_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    match xmustard_core::repomap::build_repo_map(&PathBuf::from(root), &workspace_id) {
        Ok(summary) => json(&summary),
        Err(err) => Err(CmdError::failed(format!("build-repo-map failed: {err}"))),
    }
}

fn semantic_impact(mut args: Args) -> CmdResult {
    let usage = "xmustard-core semantic-impact <workspace_id> <root_path> <changes_json_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let changes_json_path = need(&mut args, usage)?;
    let changes_content = fs::read_to_string(&changes_json_path).map_err(|err| {
        CmdError::failed(format!("semantic-impact failed to read changes: {err}"))
    })?;
    let changes =
        serde_json::from_str::<Vec<xmustard_core::repomap::RustRepoChangeRecord>>(&changes_content)
            .map_err(|err| {
                CmdError::failed(format!("semantic-impact failed to decode changes: {err}"))
            })?;
    match xmustard_core::repomap::build_semantic_impact(
        &PathBuf::from(root),
        &workspace_id,
        &changes,
    ) {
        Ok(report) => json(&report),
        Err(err) => Err(CmdError::failed(format!("semantic-impact failed: {err}"))),
    }
}

fn path_symbols(mut args: Args) -> CmdResult {
    let usage = "xmustard-core path-symbols <workspace_id> <root_path> <relative_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let relative_path = need(&mut args, usage)?;
    match xmustard_core::repomap::extract_path_symbols(
        &PathBuf::from(root),
        &workspace_id,
        &relative_path,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!("path-symbols failed: {err}"))),
    }
}

fn explain_path(mut args: Args) -> CmdResult {
    let usage = "xmustard-core explain-path <workspace_id> <root_path> <relative_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let relative_path = need(&mut args, usage)?;
    match xmustard_core::repomap::explain_path(&PathBuf::from(root), &workspace_id, &relative_path)
    {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!("explain-path failed: {err}"))),
    }
}

fn normalize_diagnostics(mut args: Args) -> CmdResult {
    let usage = "xmustard-core normalize-diagnostics <workspace_id> <root_path> <input_json_path> <source_kind> <source_name>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let input_json_path = need(&mut args, usage)?;
    let source_kind = args.next().unwrap_or_else(|| "lsp".to_string());
    let source_name = args.next().unwrap_or_else(|| "unknown".to_string());
    match xmustard_core::diagnostics::normalize_diagnostics_file(
        &workspace_id,
        &PathBuf::from(root),
        &PathBuf::from(input_json_path),
        &source_kind,
        &source_name,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "normalize-diagnostics failed: {err}"
        ))),
    }
}

fn archive_diagnostics_payload(mut args: Args) -> CmdResult {
    let usage = "xmustard-core archive-diagnostics-payload <workspace_id> <input_json_path> <source_kind> <source_name> <server_provenance_json_path>";
    let workspace_id = need(&mut args, usage)?;
    let input_json_path = need(&mut args, usage)?;
    let source_kind = args.next().unwrap_or_else(|| "lsp".to_string());
    let source_name = args.next().unwrap_or_else(|| "unknown".to_string());
    let server_provenance_json_path = need(&mut args, usage)?;
    match xmustard_core::diagnostics::archive_diagnostics_payload_file(
        &workspace_id,
        &PathBuf::from(input_json_path),
        &source_kind,
        &source_name,
        &PathBuf::from(server_provenance_json_path),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "archive-diagnostics-payload failed: {err}"
        ))),
    }
}

/// Parse a numeric argument, failing with `<command> invalid <field>: <err>` (exit 2).
fn parse_arg<T: std::str::FromStr>(raw: &str, command: &str, field: &str) -> Result<T, CmdError>
where
    T::Err: std::fmt::Display,
{
    raw.parse::<T>()
        .map_err(|err| CmdError::new(2, format!("{command} invalid {field}: {err}")))
}

fn link_diagnostic_symbol(mut args: Args) -> CmdResult {
    let usage = "xmustard-core link-diagnostic-symbol <workspace_id> <diagnostic_path> <start_line> <end_line> <diagnostic_fingerprint> <candidates_json_path>";
    let workspace_id = need(&mut args, usage)?;
    let diagnostic_path = need(&mut args, usage)?;
    let start_line_raw = need(&mut args, usage)?;
    let end_line_raw = need(&mut args, usage)?;
    let diagnostic_fingerprint = need(&mut args, usage)?;
    let candidates_json_path = need(&mut args, usage)?;
    let start_line = parse_arg::<usize>(&start_line_raw, "link-diagnostic-symbol", "start_line")?;
    let end_line = parse_arg::<usize>(&end_line_raw, "link-diagnostic-symbol", "end_line")?;
    match xmustard_core::diagnostics::link_diagnostic_symbol_file(
        &workspace_id,
        &diagnostic_path,
        start_line,
        end_line,
        &diagnostic_fingerprint,
        &PathBuf::from(candidates_json_path),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "link-diagnostic-symbol failed: {err}"
        ))),
    }
}

/// The positional arguments shared by `normalize-lsp-definition` and
/// `normalize-lsp-references`.
struct LspLocationArgs {
    workspace_id: String,
    root: PathBuf,
    relative_path: String,
    line: usize,
    column: usize,
    source_name: String,
    input_json_path: PathBuf,
}

fn lsp_location_args(args: &mut Args, command: &str) -> Result<LspLocationArgs, CmdError> {
    let usage = format!(
        "xmustard-core {command} <workspace_id> <root_path> <relative_path> <line> <column> <source_name> <input_json_path>"
    );
    let workspace_id = need(args, &usage)?;
    let root = need(args, &usage)?;
    let relative_path = need(args, &usage)?;
    let line_raw = need(args, &usage)?;
    let column_raw = need(args, &usage)?;
    let source_name = need(args, &usage)?;
    let input_json_path = need(args, &usage)?;
    Ok(LspLocationArgs {
        workspace_id,
        root: PathBuf::from(root),
        relative_path,
        line: parse_arg(&line_raw, command, "line")?,
        column: parse_arg(&column_raw, command, "column")?,
        source_name,
        input_json_path: PathBuf::from(input_json_path),
    })
}

fn normalize_lsp_definition(mut args: Args) -> CmdResult {
    let a = lsp_location_args(&mut args, "normalize-lsp-definition")?;
    match xmustard_core::lsp::normalize_definition_file(
        &a.workspace_id,
        &a.root,
        &a.relative_path,
        a.line,
        a.column,
        &a.source_name,
        &a.input_json_path,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "normalize-lsp-definition failed: {err}"
        ))),
    }
}

fn normalize_lsp_references(mut args: Args) -> CmdResult {
    let a = lsp_location_args(&mut args, "normalize-lsp-references")?;
    match xmustard_core::lsp::normalize_references_file(
        &a.workspace_id,
        &a.root,
        &a.relative_path,
        a.line,
        a.column,
        &a.source_name,
        &a.input_json_path,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "normalize-lsp-references failed: {err}"
        ))),
    }
}

fn normalize_lsp_document_symbols(mut args: Args) -> CmdResult {
    let usage = "xmustard-core normalize-lsp-document-symbols <workspace_id> <root_path> <relative_path> <source_name> <input_json_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let relative_path = need(&mut args, usage)?;
    let source_name = need(&mut args, usage)?;
    let input_json_path = need(&mut args, usage)?;
    match xmustard_core::lsp::normalize_document_symbols_file(
        &workspace_id,
        &PathBuf::from(root),
        &relative_path,
        &source_name,
        &PathBuf::from(input_json_path),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "normalize-lsp-document-symbols failed: {err}"
        ))),
    }
}

fn lsp_document_symbols(mut args: Args) -> CmdResult {
    let usage = "xmustard-core lsp-document-symbols <workspace_id> <root_path> <relative_path> [timeout_secs]";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let relative_path = need(&mut args, usage)?;
    let timeout = args
        .next()
        .and_then(|v| v.parse::<u64>().ok())
        .unwrap_or(45);
    match xmustard_core::lsp_session::live_document_symbols(
        &workspace_id,
        &PathBuf::from(root),
        &relative_path,
        timeout,
    ) {
        Ok(result) => json(&result),
        // graceful degradation (like ast-grep): not installed -> 200 with reason
        Err(xmustard_core::lsp_session::LspSessionError::Unavailable(msg)) => {
            json(&serde_json::json!({"available": false, "reason": msg, "symbols": []}))
        }
        Err(err) => Err(CmdError::failed(format!(
            "lsp-document-symbols failed: {err}"
        ))),
    }
}

fn lsp_hover(mut args: Args) -> CmdResult {
    let usage =
        "xmustard-core lsp-hover <root_path> <relative_path> <line> <character> [timeout_secs]";
    let root = need(&mut args, usage)?;
    let relative_path = need(&mut args, usage)?;
    let line = args.next().and_then(|v| v.parse::<u32>().ok());
    let character = args.next().and_then(|v| v.parse::<u32>().ok());
    let (Some(line), Some(character)) = (line, character) else {
        return Err(CmdError::usage(usage));
    };
    let timeout = args
        .next()
        .and_then(|v| v.parse::<u64>().ok())
        .unwrap_or(45);
    match xmustard_core::lsp_session::live_hover(
        &PathBuf::from(root),
        &relative_path,
        line,
        character,
        timeout,
    ) {
        Ok(result) => json(&result),
        Err(xmustard_core::lsp_session::LspSessionError::Unavailable(msg)) => {
            json(&serde_json::json!({"available": false, "reason": msg}))
        }
        Err(err) => Err(CmdError::failed(format!("lsp-hover failed: {err}"))),
    }
}

fn normalize_lsp_workspace_symbols(mut args: Args) -> CmdResult {
    let usage = "xmustard-core normalize-lsp-workspace-symbols <workspace_id> <root_path> <query> <limit> <source_name> <input_json_path>";
    let workspace_id = need(&mut args, usage)?;
    let root = need(&mut args, usage)?;
    let query = need(&mut args, usage)?;
    let limit_raw = need(&mut args, usage)?;
    let source_name = need(&mut args, usage)?;
    let input_json_path = need(&mut args, usage)?;
    let limit = parse_arg::<usize>(&limit_raw, "normalize-lsp-workspace-symbols", "limit")?;
    match xmustard_core::lsp::normalize_workspace_symbols_file(
        &workspace_id,
        &PathBuf::from(root),
        &query,
        limit,
        &source_name,
        &PathBuf::from(input_json_path),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "normalize-lsp-workspace-symbols failed: {err}"
        ))),
    }
}

fn parse_coverage_lcov(mut args: Args) -> CmdResult {
    let usage =
        "xmustard-core parse-coverage-lcov <workspace_id> <report_path> [run_id] [issue_id]";
    let workspace_id = need(&mut args, usage)?;
    let report_path = need(&mut args, usage)?;
    let run_id = args.next();
    let issue_id = args.next();
    match xmustard_core::verification::parse_lcov_file(
        &PathBuf::from(&report_path),
        &workspace_id,
        run_id.as_deref(),
        issue_id.as_deref(),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "parse-coverage-lcov failed: {err}"
        ))),
    }
}

fn parse_coverage(mut args: Args) -> CmdResult {
    let usage = "xmustard-core parse-coverage <workspace_id> <report_path> [run_id] [issue_id]";
    let workspace_id = need(&mut args, usage)?;
    let report_path = need(&mut args, usage)?;
    let run_id = args.next();
    let issue_id = args.next();
    match xmustard_core::verification::parse_coverage_file(
        &PathBuf::from(&report_path),
        &workspace_id,
        run_id.as_deref(),
        issue_id.as_deref(),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!("parse-coverage failed: {err}"))),
    }
}

fn run_verification_command(mut args: Args) -> CmdResult {
    let usage =
        "xmustard-core run-verification-command <workspace_root> <timeout_seconds> <command>";
    let workspace_root = need(&mut args, usage)?;
    let timeout_seconds = need(&mut args, usage)?;
    let command_text = need(&mut args, usage)?;
    let timeout_seconds = timeout_seconds.parse::<u64>().unwrap_or(30);
    match xmustard_core::verification::run_verification_command(
        &PathBuf::from(&workspace_root),
        &command_text,
        timeout_seconds,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "run-verification-command failed: {err}"
        ))),
    }
}

fn run_managed_command(mut args: Args) -> CmdResult {
    let usage =
        "xmustard-core run-managed-command <workspace_root> <timeout_seconds> <program> [args...]";
    let workspace_root = need(&mut args, usage)?;
    let timeout_seconds = need(&mut args, usage)?;
    let program = need(&mut args, usage)?;
    let timeout_seconds = timeout_seconds.parse::<u64>().unwrap_or(30);
    let mut command_args = vec![program];
    command_args.extend(args);
    match xmustard_core::verification::run_managed_command(
        &PathBuf::from(&workspace_root),
        &command_args,
        timeout_seconds,
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "run-managed-command failed: {err}"
        ))),
    }
}

fn run_verification_profile(mut args: Args) -> CmdResult {
    let usage = "xmustard-core run-verification-profile <workspace_root> <profile_json_path> [run_id] [issue_id]";
    let workspace_root = need(&mut args, usage)?;
    let profile_json_path = need(&mut args, usage)?;
    let run_id = args.next();
    let issue_id = args.next();
    let profile_content = fs::read_to_string(&profile_json_path).map_err(|err| {
        CmdError::failed(format!(
            "run-verification-profile failed to read profile: {err}"
        ))
    })?;
    let profile =
        serde_json::from_str::<xmustard_core::verification::RustVerificationProfileInput>(
            &profile_content,
        )
        .map_err(|err| {
            CmdError::failed(format!(
                "run-verification-profile failed to decode profile: {err}"
            ))
        })?;
    match xmustard_core::verification::run_verification_profile(
        &PathBuf::from(&workspace_root),
        &profile,
        run_id.as_deref(),
        issue_id.as_deref(),
    ) {
        Ok(result) => json(&result),
        Err(err) => Err(CmdError::failed(format!(
            "run-verification-profile failed: {err}"
        ))),
    }
}

fn semantic_search(mut args: Args) -> CmdResult {
    let usage = "xmustard-core semantic-search <root> <pattern> [language] [path_glob] [limit]";
    let root = need(&mut args, usage)?;
    let pattern = need(&mut args, usage)?;
    let language = args.next();
    let path_glob = args.next();
    let limit = args
        .next()
        .and_then(|v| v.parse::<usize>().ok())
        .unwrap_or(50);
    json(&xmustard_core::semantic::run_ast_grep_query(
        &PathBuf::from(root),
        &pattern,
        language.as_deref(),
        path_glob.as_deref(),
        limit,
    ))
}

fn ownership(mut args: Args) -> CmdResult {
    let sub = need(&mut args, "xmustard-core ownership <subsystems|owners> ...")?;
    match sub.as_str() {
        "subsystems" => {
            let usage = "xmustard-core ownership subsystems <root> <workspace_id>";
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            json(
                &xmustard_core::ownership::build_subsystems(Path::new(&root), &ws)
                    .map_err(|e| CmdError::failed(format!("ownership subsystems: {e}")))?,
            )
        }
        "owners" => {
            let usage = "xmustard-core ownership owners <root> <path>";
            let root = need(&mut args, usage)?;
            let path = need(&mut args, usage)?;
            json(&xmustard_core::ownership::likely_owners(
                Path::new(&root),
                &path,
            ))
        }
        other => Err(CmdError::new(
            2,
            format!("unknown ownership subcommand: {other}"),
        )),
    }
}

/// A leading `--identity-key=K`: the repository identity the caller observed for this
/// graph read. The code index answers only when it was brought to that identity; else
/// the legacy graph for the current tree does (`symbolgraph::query_source_for`). Only
/// the first argument is looked at, so a query or symbol is never taken for the flag.
fn identity_flag(args: Args) -> (Option<String>, Args) {
    let mut v: Vec<String> = args.collect();
    let key = v
        .first()
        .and_then(|a| a.strip_prefix("--identity-key="))
        .map(str::to_string);
    if key.is_some() {
        v.remove(0);
    }
    (key.filter(|k| !k.is_empty()), v.into_iter())
}

fn search(args: Args) -> CmdResult {
    let usage = "xmustard-core search [--identity-key=K] [--offset=N] [--path-glob=GLOB] \
                 <root> <workspace_id> <query> [limit] [seed]";
    let (key, args) = identity_flag(args);
    // `--name=value` flags lead; the first other argument ends them, so a query or seed
    // symbol that looks like a flag is never read as one.
    let mut flags: Vec<String> = args.collect();
    let lead = flags.iter().take_while(|a| a.starts_with("--")).count();
    let mut args = flags.split_off(lead).into_iter();
    let (mut offset, mut path_glob) = (0usize, None::<String>);
    for flag in flags {
        match flag.split_once('=') {
            Some(("--offset", n)) => {
                offset = n
                    .parse()
                    .map_err(|_| CmdError::new(2, format!("search: bad --offset {n}\n{usage}")))?
            }
            Some(("--path-glob", g)) => path_glob = Some(g.to_string()),
            _ => {
                return Err(CmdError::new(
                    2,
                    format!("search: unknown flag {flag}\n{usage}"),
                ));
            }
        }
    }
    let root = need(&mut args, usage)?;
    let ws = need(&mut args, usage)?;
    let query = need(&mut args, usage)?;
    // then the limit and an optional seed symbol for the graph-proximity lane
    let limit = args
        .next()
        .and_then(|v| v.parse::<usize>().ok())
        .unwrap_or(25);
    let seed = args.next().filter(|s| !s.trim().is_empty());
    json(&xmustard_core::search::search(
        &PathBuf::from(root),
        &ws,
        key.as_deref(),
        &query,
        &xmustard_core::search::SearchOptions {
            limit,
            offset,
            seed: seed.as_deref(),
            path_glob: path_glob.as_deref(),
        },
    ))
}

fn repo_key(mut args: Args) -> CmdResult {
    // The repaired source identity (same key as graph-cache invalidation), plus the
    // directories git ignores as a whole (`ignored_dirs`) for the Go identity cache's
    // fingerprint. Always succeeds with JSON; failures are `identity_complete=false`
    // plus `limitations`, never a partial key presented as complete.
    let root = need(&mut args, "xmustard-core repo-key <root>")?;
    json(&xmustard_core::indexcache::repo_key_identity(
        &PathBuf::from(root),
    ))
}

fn wiki(mut args: Args) -> CmdResult {
    let usage = "xmustard-core wiki <root> <workspace_id>";
    let root = need(&mut args, usage)?;
    let ws = need(&mut args, usage)?;
    json(
        &xmustard_core::wiki::generate_wiki(&PathBuf::from(root), &ws)
            .map_err(|e| CmdError::failed(format!("wiki: {e}")))?,
    )
}

/// Dispatch the `goal` subcommand family over the durable goal runtime.
/// JSON reads are returned as JSON; Markdown projections are returned verbatim.
fn run_goal_command(mut args: Args) -> CmdResult {
    use xmustard_core::goalruntime as goal;

    fn read_json_request<T: serde::de::DeserializeOwned>(path: &str) -> Result<T, CmdError> {
        let content = fs::read_to_string(path).map_err(|err| {
            CmdError::failed(format!("goal: failed to read request {path}: {err}"))
        })?;
        serde_json::from_str::<T>(&content).map_err(|err| {
            CmdError::failed(format!("goal: failed to decode request {path}: {err}"))
        })
    }

    let sub = need(
        &mut args,
        "xmustard-core goal <list|get|create|iterate|status|ledger|context|lint> ...",
    )?;
    match sub.as_str() {
        "list" => {
            let usage = "xmustard-core goal list <data_dir> <workspace_id>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goals = goal::list_goals(Path::new(&data_dir), &workspace_id).map_err(goal_fail)?;
            json(&goals)
        }
        "get" => {
            let usage = "xmustard-core goal get <data_dir> <workspace_id> <goal_id>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let record =
                goal::get_goal(Path::new(&data_dir), &workspace_id, &goal_id).map_err(goal_fail)?;
            json(&record)
        }
        "create" => {
            let usage = "xmustard-core goal create <data_dir> <workspace_id> <request_json_path>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let request_path = need(&mut args, usage)?;
            let request: goal::GoalCreateRequest = read_json_request(&request_path)?;
            let (record, report) = goal::create_goal(Path::new(&data_dir), &workspace_id, &request)
                .map_err(goal_fail)?;
            json(&serde_json::json!({
                "goal": record,
                "slop": report,
            }))
        }
        "iterate" => {
            let usage = "xmustard-core goal iterate <data_dir> <workspace_id> <goal_id> <request_json_path>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let request_path = need(&mut args, usage)?;
            let request: goal::GoalIterationAppendRequest = read_json_request(&request_path)?;
            let (record, report) =
                goal::append_iteration(Path::new(&data_dir), &workspace_id, &goal_id, &request)
                    .map_err(goal_fail)?;
            json(&serde_json::json!({
                "iteration": record,
                "slop": report,
            }))
        }
        "status" => {
            let usage = "xmustard-core goal status <data_dir> <workspace_id> <goal_id> <status> [skip_reason]";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let status_raw = need(&mut args, usage)?;
            let skip_reason = args.next().unwrap_or_default();
            let status = goal::GoalStatus::parse(&status_raw).map_err(goal_fail)?;
            let record = goal::update_status(
                Path::new(&data_dir),
                &workspace_id,
                &goal_id,
                status,
                &skip_reason,
            )
            .map_err(goal_fail)?;
            json(&record)
        }
        "ledger" => {
            let usage = "xmustard-core goal ledger <data_dir> <workspace_id> <goal_id>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let markdown = goal::read_ledger(Path::new(&data_dir), &workspace_id, &goal_id)
                .map_err(goal_fail)?;
            Ok(Output::Text(markdown))
        }
        "context" => {
            let usage = "xmustard-core goal context <data_dir> <workspace_id> <goal_id>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let packet = goal::build_context_packet(Path::new(&data_dir), &workspace_id, &goal_id)
                .map_err(goal_fail)?;
            Ok(Output::Text(packet))
        }
        "lint" => {
            let usage = "xmustard-core goal lint <data_dir> <workspace_id> <goal_id>";
            let data_dir = need(&mut args, usage)?;
            let workspace_id = need(&mut args, usage)?;
            let goal_id = need(&mut args, usage)?;
            let report = goal::lint_goal(Path::new(&data_dir), &workspace_id, &goal_id)
                .map_err(goal_fail)?;
            json(&report)
        }
        other => Err(CmdError::new(
            2,
            format!("unknown goal subcommand: {other}"),
        )),
    }
}

/// Exit code 3 marks a guardrail/gate refusal and 4 a missing goal, so callers can
/// branch on them.
fn goal_exit_code(err: &xmustard_core::goalruntime::GoalError) -> i32 {
    use xmustard_core::goalruntime::GoalError;
    match err {
        GoalError::CompletionBlocked(_) | GoalError::SlopBlocked(_) => 3,
        GoalError::NotFound(_) => 4,
        _ => 1,
    }
}

fn goal_fail(err: xmustard_core::goalruntime::GoalError) -> CmdError {
    CmdError::new(goal_exit_code(&err), format!("goal: {err}"))
}

/// `changetrack <fingerprint|index|drift|changed-since|working-changes> ...` —
/// gitnexus-style repo change tracking.
fn run_changetrack_command(mut args: Args) -> CmdResult {
    use xmustard_core::changetrack as ct;

    let sub = need(
        &mut args,
        "xmustard-core changetrack <fingerprint|index|drift|changed-since|working-changes> ...",
    )?;
    match sub.as_str() {
        "fingerprint" => {
            let root = need(&mut args, "xmustard-core changetrack fingerprint <root>")?;
            match ct::compute_fingerprint(Path::new(&root)) {
                Ok(fingerprint) => json(&fingerprint),
                Err(err) => Err(CmdError::failed(format!(
                    "changetrack fingerprint failed: {err}"
                ))),
            }
        }
        "index" => {
            let usage = "xmustard-core changetrack index <data_dir> <root> <workspace_id> [--reason=registration|first_ground|head_changed|admin] [--stage]";
            let data_dir = need(&mut args, usage)?;
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            // an explicit rebaseline unless the caller names the automatic trigger;
            // --stage leaves it beside the baseline for the caller to record and publish
            let (mut reason, mut stage) = (ct::BaselineReason::Admin, false);
            for flag in args {
                match flag.as_str() {
                    "--stage" => stage = true,
                    other => {
                        reason = other
                            .strip_prefix("--reason=")
                            .and_then(ct::BaselineReason::parse)
                            .ok_or_else(|| CmdError::usage(usage))?
                    }
                }
            }
            match ct::rebaseline_summary(Path::new(&data_dir), Path::new(&root), &ws, reason, stage)
            {
                Ok(summary) => json(&summary),
                Err(err) => Err(CmdError::failed(format!("changetrack index failed: {err}"))),
            }
        }
        "drift" => {
            let usage = "xmustard-core changetrack drift <data_dir> <root> <workspace_id>";
            let data_dir = need(&mut args, usage)?;
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            json(&ct::detect_drift(
                Path::new(&data_dir),
                Path::new(&root),
                &ws,
            ))
        }
        "changed-since" => {
            let usage = "xmustard-core changetrack changed-since <data_dir> <root> <workspace_id>";
            let data_dir = need(&mut args, usage)?;
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            json(&ct::changed_since_baseline(
                Path::new(&data_dir),
                Path::new(&root),
                &ws,
            ))
        }
        "working-changes" => {
            let usage =
                "xmustard-core changetrack working-changes <data_dir> <root> <workspace_id>";
            let data_dir = need(&mut args, usage)?;
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            json(&ct::working_tree_changes(
                Path::new(&data_dir),
                Path::new(&root),
                &ws,
            ))
        }
        "incorporate" => {
            let usage = "xmustard-core changetrack incorporate <data_dir> <root> <workspace_id>";
            let data_dir = need(&mut args, usage)?;
            let root = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            match ct::record_incorporation(Path::new(&data_dir), Path::new(&root), &ws) {
                Ok(events) => json(&events),
                Err(err) => Err(CmdError::failed(format!(
                    "changetrack incorporate failed: {err}"
                ))),
            }
        }
        "lineage" => {
            let usage = "xmustard-core changetrack lineage <data_dir> <workspace_id> <path>";
            let data_dir = need(&mut args, usage)?;
            let ws = need(&mut args, usage)?;
            let path = need(&mut args, usage)?;
            json(&ct::file_lineage(Path::new(&data_dir), &ws, &path))
        }
        other => Err(CmdError::new(
            2,
            format!("unknown changetrack subcommand: {other}"),
        )),
    }
}

/// `symbolgraph <build|hotspots|blast-radius|...> ...` — the semantic symbol graph.
/// The read-only queries run on the code index's snapshot when the root has an index
/// (resident when serving) and on the legacy graph otherwise (see
/// `symbolgraph::query_source`); impact, impact-file, trace and cluster-of carry
/// freshness and coverage.
fn run_symbolgraph_command(mut args: Args) -> CmdResult {
    use xmustard_core::symbolgraph as sg;

    let sub = need(
        &mut args,
        "xmustard-core symbolgraph <build|hotspots|clusters|cluster-of|coverage|impact|impact-file|trace|blast-radius> [--identity-key=K] ...",
    )?;
    let (key, mut args) = identity_flag(args);
    let source = |root: &str, ws: &str| sg::query_source_for(Path::new(root), ws, key.as_deref());
    let usage = |rest: &str| {
        format!("xmustard-core symbolgraph {sub} [--identity-key=K] <root> <workspace_id>{rest}")
    };
    let failed = |e: String| CmdError::failed(format!("symbolgraph {sub}: {e}"));
    match sub.as_str() {
        "build" => {
            let u = usage("");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            json(&sg::build_symbol_graph(Path::new(&root), &ws))
        }
        "coverage" => {
            // ground's coverage block: the per-language support counts search, explain
            // and impact report, without the loss list, from the same source they read
            // (the code index's snapshot when the root has one, else the legacy graph)
            let u = usage("");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            // with the freshness of the source it came from: the snapshot generation,
            // the watcher state and the last index run's counters (WS-15)
            let src = source(&root, &ws);
            let freshness = src.freshness(std::iter::empty());
            let cov = src.coverage;
            json(&serde_json::json!({
                "complete": cov.complete,
                "languages": cov.languages,
                "freshness": freshness,
            }))
        }
        "clusters" => {
            let u = usage("");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let src = source(&root, &ws);
            json(&src.graph.clusters().map_err(failed)?)
        }
        "cluster-of" => {
            let u = usage(" <path>");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let path = need(&mut args, &u)?;
            let src = source(&root, &ws);
            let cluster = src.graph.cluster_of(&path).map_err(failed)?;
            let (freshness, coverage) = src.annotate([path.as_str()]);
            json(&serde_json::json!({
                "path": path,
                "cluster": cluster,
                "freshness": freshness,
                "coverage": coverage,
            }))
        }
        "impact" => {
            let u = usage(" <symbol> [max_depth]");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let symbol = need(&mut args, &u)?;
            let depth = args
                .next()
                .and_then(|v| v.parse::<usize>().ok())
                .unwrap_or(4);
            let src = source(&root, &ws);
            let mut out = src.graph.impact(&symbol, depth).map_err(failed)?;
            let paths = out
                .defined_in
                .iter()
                .chain(out.impacted.iter().map(|i| &i.path));
            (out.freshness, out.coverage) = src.annotate(paths.map(String::as_str));
            json(&out)
        }
        "impact-file" => {
            // impact(path=): the file-level walk; the caller confines the path to the
            // root, and it is only looked up in the graph
            let u = usage(" <path> [max_depth]");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let path = need(&mut args, &u)?;
            let depth = args
                .next()
                .and_then(|v| v.parse::<usize>().ok())
                .unwrap_or(4);
            let src = source(&root, &ws);
            let mut out = src.graph.file_impact(&path, depth).map_err(failed)?;
            let start = out.found.then_some(&out.path);
            let paths = start
                .into_iter()
                .chain(out.impacted.iter().map(|i| &i.path));
            (out.freshness, out.coverage) = src.annotate(paths.map(String::as_str));
            json(&out)
        }
        "trace" => {
            let u = usage(" <from_symbol> <to_symbol>");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let from = need(&mut args, &u)?;
            let to = need(&mut args, &u)?;
            let src = source(&root, &ws);
            let mut out = src.graph.trace(&from, &to).map_err(failed)?;
            (out.freshness, out.coverage) = src.annotate(out.path.iter().map(String::as_str));
            json(&out)
        }
        "hotspots" => {
            let u = usage(" [limit]");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let limit = args
                .next()
                .and_then(|v| v.parse::<usize>().ok())
                .unwrap_or(20);
            let src = source(&root, &ws);
            json(&src.graph.hotspots(limit).map_err(failed)?)
        }
        "blast-radius" => {
            let u = usage(" <symbol>");
            let root = need(&mut args, &u)?;
            let ws = need(&mut args, &u)?;
            let symbol = need(&mut args, &u)?;
            json(&sg::blast_radius(Path::new(&root), &ws, &symbol))
        }
        other => Err(CmdError::new(
            2,
            format!("unknown symbolgraph subcommand: {other}"),
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| s.to_string()).collect()
    }

    /// Every resident invocation the Go bridge sends through `runCoreCtx`, including
    /// all nine-tool query paths, with the first argument for command families.
    const GO_WORKER_CALLS: &[(&str, &[&str])] = &[
        ("scan-signals", &[]),
        ("build-repo-map", &[]),
        ("semantic-impact", &[]),
        ("path-symbols", &[]),
        ("explain-path", &[]),
        ("normalize-diagnostics", &[]),
        ("archive-diagnostics-payload", &[]),
        ("link-diagnostic-symbol", &[]),
        ("normalize-lsp-definition", &[]),
        ("normalize-lsp-references", &[]),
        ("normalize-lsp-document-symbols", &[]),
        ("normalize-lsp-workspace-symbols", &[]),
        ("parse-coverage-lcov", &[]),
        ("parse-coverage", &[]),
        ("symbolgraph", &["hotspots"]),
        ("symbolgraph", &["impact"]),
        ("symbolgraph", &["impact-file"]),
        ("symbolgraph", &["trace"]),
        ("symbolgraph", &["clusters"]),
        ("symbolgraph", &["cluster-of"]),
        ("symbolgraph", &["coverage"]),
        ("changetrack", &["fingerprint"]),
        ("changetrack", &["drift"]),
        ("changetrack", &["changed-since"]),
        ("changetrack", &["working-changes"]),
        ("changetrack", &["incorporate"]),
        ("changetrack", &["lineage"]),
        ("ownership", &["subsystems"]),
        ("ownership", &["owners"]),
        ("search", &[]),
        ("repo-key", &[]),
        ("wiki", &[]),
    ];

    /// rust-core/go-calls.txt: every call code reachable from an api-go main makes
    /// (`called`) and the commands kept without one (`kept ... -- reason`).
    /// TestCoreCallManifestMatchesGoSources (api-go/internal/rustcore) holds the file
    /// to the Go sources, so this list cannot drift from what Go calls.
    const GO_CALL_MANIFEST: &str = include_str!("../../go-calls.txt");

    /// (kind, subcommand, family member) for each line of the manifest.
    fn go_call_manifest() -> Vec<(&'static str, &'static str, Option<&'static str>)> {
        GO_CALL_MANIFEST
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty() && !line.starts_with('#'))
            .map(|line| {
                let (spec, reason) = match line.split_once(" -- ") {
                    Some((spec, reason)) => (spec, Some(reason.trim())),
                    None => (line, None),
                };
                let words: Vec<&str> = spec.split_whitespace().collect();
                let ok = match (words.first().copied(), reason) {
                    (Some("called"), None) => true,
                    (Some("kept"), Some(reason)) => !reason.is_empty(),
                    _ => false,
                };
                assert!(ok && (2..=3).contains(&words.len()), "go-calls.txt: {line}");
                (words[0], words[1], words.get(2).copied())
            })
            .collect()
    }

    #[test]
    fn go_called_commands_exist() {
        let manifest = go_call_manifest();
        assert!(
            manifest
                .iter()
                .filter(|(kind, ..)| *kind == "called")
                .count()
                >= 30,
            "go-calls.txt lost its calls"
        );
        for (kind, name, member) in manifest {
            let entry = dispatch::find(COMMANDS, name).unwrap_or_else(|| {
                panic!("go-calls.txt lists `{kind} {name}`, but COMMANDS has no {name}")
            });
            // A member alone never has the rest of its arguments, so a handler that
            // knows it answers with its usage line instead of running.
            if let Some(member) = member
                && let Err(err) = (entry.run)(strings(&[member]).into_iter())
            {
                assert_ne!(
                    err.message,
                    format!("unknown {name} subcommand: {member}"),
                    "go-calls.txt lists `{kind} {name} {member}`"
                );
            }
        }
    }

    #[test]
    fn subcommands_without_a_caller_stay_removed() {
        // WS-25 removed these: no Go caller, no script caller.
        for name in [
            "swarm",
            "bench",
            "lsp-references",
            "lsp-definition",
            "lsp-implementation",
            "lsp-type-definition",
            "lsp-rename",
        ] {
            assert!(dispatch::find(COMMANDS, name).is_none(), "{name}");
        }
        let sg = dispatch::find(COMMANDS, "symbolgraph").unwrap();
        for sub in ["build-lsp", "flow"] {
            let err = (sg.run)(strings(&[sub, "/r", "ws"]).into_iter()).unwrap_err();
            assert_eq!(
                err.message,
                format!("unknown symbolgraph subcommand: {sub}")
            );
        }
    }

    #[test]
    fn every_go_worker_call_is_resident() {
        for (name, args) in GO_WORKER_CALLS {
            let entry = dispatch::find(COMMANDS, name)
                .unwrap_or_else(|| panic!("{name} missing from COMMANDS"));
            assert!(
                entry.resident_for(&strings(args)),
                "{name} {args:?} must be resident: the Go worker path sends it"
            );
        }
    }

    #[test]
    fn process_spawning_commands_stay_one_shot() {
        for name in [
            "lsp-document-symbols",
            "lsp-hover",
            "run-verification-command",
            "run-managed-command",
            "run-verification-profile",
            "goal",
            "semantic-search",
        ] {
            let entry = dispatch::find(COMMANDS, name).unwrap();
            assert!(!entry.is_resident(), "{name} must stay one-shot");
        }
        let sg = dispatch::find(COMMANDS, "symbolgraph").unwrap();
        for sub in ["build", "blast-radius"] {
            assert!(!sg.resident_for(&strings(&[sub, "/r", "ws"])), "{sub}");
        }
        let ct = dispatch::find(COMMANDS, "changetrack").unwrap();
        assert!(!ct.resident_for(&strings(&["index", "/d", "/r", "ws"])));
    }

    #[test]
    fn table_names_are_unique() {
        let mut names: Vec<&str> = COMMANDS.iter().map(|c| c.name).collect();
        names.sort_unstable();
        let before = names.len();
        names.dedup();
        assert_eq!(before, names.len(), "duplicate subcommand in COMMANDS");
        assert!(
            dispatch::find(COMMANDS, "serve").is_none(),
            "serve is the worker entry point, not a table command"
        );
    }

    #[test]
    fn handlers_report_usage_instead_of_exiting() {
        // Every handler returns a usage error (never exits the process) when its
        // required arguments are missing.
        for entry in COMMANDS {
            let err = (entry.run)(Vec::new().into_iter())
                .expect_err("a handler with no arguments must fail");
            assert_eq!(err.code, 2, "{}: {}", entry.name, err.message);
            assert!(
                err.message.starts_with("usage: "),
                "{}: {}",
                entry.name,
                err.message
            );
        }
    }

    #[test]
    fn unknown_family_subcommands_fail_with_code_two() {
        for name in ["symbolgraph", "changetrack", "ownership", "goal"] {
            let entry = dispatch::find(COMMANDS, name).unwrap();
            let err = (entry.run)(strings(&["nope"]).into_iter()).unwrap_err();
            assert_eq!(err.code, 2);
            assert_eq!(err.message, format!("unknown {name} subcommand: nope"));
        }
    }

    #[test]
    fn numeric_argument_errors_keep_their_messages() {
        let entry = dispatch::find(COMMANDS, "link-diagnostic-symbol").unwrap();
        let err = (entry.run)(strings(&["ws", "a.rs", "x", "2", "fp", "c.json"]).into_iter())
            .unwrap_err();
        assert_eq!(err.code, 2);
        assert!(
            err.message
                .starts_with("link-diagnostic-symbol invalid start_line: "),
            "{}",
            err.message
        );
    }
}
