//! Live LSP sessions: spawn a real language server (rust-analyzer, gopls,
//! typescript-language-server, clangd, …), run the initialize/initialized
//! handshake over stdio JSON-RPC with Content-Length framing, open a document,
//! and issue requests (documentSymbol, hover). The raw results feed the existing
//! `lsp.rs` normalizers. Servers that aren't installed degrade gracefully — the
//! same contract ast-grep uses — so this never hard-fails a workspace.

use std::collections::HashSet;
use std::io::{BufRead, BufReader, Write};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{Value, json};

use crate::lsp::{self, RustDocumentSymbolsResult};

#[derive(Debug)]
pub enum LspSessionError {
    /// The language server for this file type is not installed on PATH.
    Unavailable(String),
    /// The session ran but failed (spawn error, protocol error, timeout).
    Failed(String),
}

impl std::fmt::Display for LspSessionError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            LspSessionError::Unavailable(m) => write!(f, "lsp unavailable: {m}"),
            LspSessionError::Failed(m) => write!(f, "lsp failed: {m}"),
        }
    }
}

struct ServerConfig {
    command: &'static str,
    args: &'static [&'static str],
    language_id: &'static str,
}

/// The language-server command that would handle `rel` (e.g. "rust-analyzer"), or
/// None if the file type has no mapped server. Used to group files by server.
pub fn server_command_for(rel: &str) -> Option<&'static str> {
    server_for(rel).map(|c| c.command)
}

fn server_for(rel: &str) -> Option<ServerConfig> {
    let ext = Path::new(rel)
        .extension()
        .and_then(|e| e.to_str())?
        .to_ascii_lowercase();
    let cfg = match ext.as_str() {
        "rs" => ServerConfig {
            command: "rust-analyzer",
            args: &[],
            language_id: "rust",
        },
        "go" => ServerConfig {
            command: "gopls",
            args: &[],
            language_id: "go",
        },
        "ts" => ServerConfig {
            command: "typescript-language-server",
            args: &["--stdio"],
            language_id: "typescript",
        },
        "tsx" => ServerConfig {
            command: "typescript-language-server",
            args: &["--stdio"],
            language_id: "typescriptreact",
        },
        "js" | "mjs" | "cjs" => ServerConfig {
            command: "typescript-language-server",
            args: &["--stdio"],
            language_id: "javascript",
        },
        "jsx" => ServerConfig {
            command: "typescript-language-server",
            args: &["--stdio"],
            language_id: "javascriptreact",
        },
        "c" | "h" => ServerConfig {
            command: "clangd",
            args: &[],
            language_id: "c",
        },
        "cpp" | "cc" | "hpp" | "cxx" => ServerConfig {
            command: "clangd",
            args: &[],
            language_id: "cpp",
        },
        _ => return None,
    };
    Some(cfg)
}

fn binary_on_path(command: &str) -> bool {
    // `command -v` style check via spawning is heavy; probe PATH directly.
    if let Ok(paths) = std::env::var("PATH") {
        for dir in paths.split(':') {
            let candidate = Path::new(dir).join(command);
            if candidate.is_file() {
                return true;
            }
        }
    }
    false
}

fn frame(stdin: &mut impl Write, value: &Value) -> std::io::Result<()> {
    let body = serde_json::to_vec(value)?;
    write!(stdin, "Content-Length: {}\r\n\r\n", body.len())?;
    stdin.write_all(&body)?;
    stdin.flush()
}

fn read_message<R: BufRead>(reader: &mut R) -> std::io::Result<Option<Value>> {
    let mut content_length: Option<usize> = None;
    loop {
        let mut line = String::new();
        let n = reader.read_line(&mut line)?;
        if n == 0 {
            return Ok(None); // EOF
        }
        let trimmed = line.trim_end_matches(['\r', '\n']);
        if trimmed.is_empty() {
            break; // end of header block
        }
        if let Some(rest) = trimmed.strip_prefix("Content-Length:") {
            content_length = rest.trim().parse::<usize>().ok();
        }
    }
    let Some(len) = content_length else {
        return Ok(None);
    };
    // cap the payload so a malformed/hostile Content-Length cannot OOM the process.
    if len > 64 * 1024 * 1024 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("LSP Content-Length {len} exceeds 64 MiB limit"),
        ));
    }
    let mut buf = vec![0u8; len];
    reader.read_exact(&mut buf)?;
    Ok(serde_json::from_slice::<Value>(&buf).ok())
}

/// A live LSP session bound to one server process. Drop kills the child.
struct Session {
    child: Child,
    rx: mpsc::Receiver<Value>,
    deadline: Instant,
}

impl Session {
    /// Block until the response to `id` arrives, replying null to any
    /// server→client requests so the server doesn't stall.
    fn wait_for(&self, id: i64, stdin: &mut impl Write) -> Result<Value, LspSessionError> {
        loop {
            let now = Instant::now();
            if now >= self.deadline {
                return Err(LspSessionError::Failed(
                    "timed out waiting for response".into(),
                ));
            }
            let budget = (self.deadline - now).min(Duration::from_millis(500));
            match self.rx.recv_timeout(budget) {
                Ok(msg) => {
                    let has_method = msg.get("method").is_some();
                    if let Some(mid) = msg.get("id") {
                        if has_method {
                            // server→client request: reply with null result
                            let reply = json!({"jsonrpc":"2.0","id": mid, "result": null});
                            let _ = frame(stdin, &reply);
                        } else if mid == &json!(id) {
                            if let Some(err) = msg.get("error") {
                                return Err(LspSessionError::Failed(format!("lsp error: {err}")));
                            }
                            return Ok(msg.get("result").cloned().unwrap_or(Value::Null));
                        }
                    }
                    // notifications (method, no id) are ignored
                }
                Err(RecvTimeoutError::Timeout) => continue,
                Err(RecvTimeoutError::Disconnected) => {
                    return Err(LspSessionError::Failed(
                        "lsp server closed the connection".into(),
                    ));
                }
            }
        }
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// Run one request against a freshly-opened document and return its raw result.
fn request_document(
    root: &Path,
    rel: &str,
    method: &str,
    extra_params: Value,
    timeout: Duration,
) -> Result<Value, LspSessionError> {
    let Some(cfg) = server_for(rel) else {
        return Err(LspSessionError::Unavailable(format!(
            "no LSP server mapped for {rel}"
        )));
    };
    if !binary_on_path(cfg.command) {
        return Err(LspSessionError::Unavailable(format!(
            "{} is not installed on this machine",
            cfg.command
        )));
    }
    let abs = root.join(rel);
    let text = std::fs::read_to_string(&abs)
        .map_err(|e| LspSessionError::Failed(format!("read {rel}: {e}")))?;
    let root_uri = format!("file://{}", root.display());
    let doc_uri = format!("file://{}", abs.display());

    // stderr is discarded by default; set XM_LSP_DEBUG=1 to surface server logs.
    let stderr = if std::env::var("XM_LSP_DEBUG").is_ok() {
        Stdio::inherit()
    } else {
        Stdio::null()
    };
    let mut child = Command::new(cfg.command)
        .args(cfg.args)
        .current_dir(root)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(stderr)
        .spawn()
        .map_err(|e| LspSessionError::Failed(format!("spawn {}: {e}", cfg.command)))?;

    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| LspSessionError::Failed("no stdout".into()))?;
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let mut reader = BufReader::new(stdout);
        while let Ok(Some(v)) = read_message(&mut reader) {
            if tx.send(v).is_err() {
                break;
            }
        }
    });
    let mut stdin = child
        .stdin
        .take()
        .ok_or_else(|| LspSessionError::Failed("no stdin".into()))?;

    let session = Session {
        child,
        rx,
        deadline: Instant::now() + timeout,
    };

    // 1) initialize / initialized handshake
    let init = json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "processId": null,
            "rootUri": root_uri,
            "capabilities": {
                "textDocument": {
                    "documentSymbol": { "hierarchicalDocumentSymbolSupport": true },
                    "hover": { "contentFormat": ["plaintext", "markdown"] },
                    "references": {},
                    "definition": { "linkSupport": true },
                    "implementation": { "linkSupport": true },
                    "typeDefinition": { "linkSupport": true },
                    "rename": { "prepareSupport": true }
                }
            }
        }
    });
    frame(&mut stdin, &init).map_err(|e| LspSessionError::Failed(e.to_string()))?;
    session.wait_for(1, &mut stdin)?;
    frame(
        &mut stdin,
        &json!({"jsonrpc":"2.0","method":"initialized","params":{}}),
    )
    .map_err(|e| LspSessionError::Failed(e.to_string()))?;

    // 2) open the document
    let did_open = json!({
        "jsonrpc": "2.0", "method": "textDocument/didOpen",
        "params": { "textDocument": {
            "uri": doc_uri, "languageId": cfg.language_id, "version": 1, "text": text
        }}
    });
    frame(&mut stdin, &did_open).map_err(|e| LspSessionError::Failed(e.to_string()))?;

    // 3) the actual request
    let mut params = json!({ "textDocument": { "uri": doc_uri } });
    if let (Value::Object(p), Value::Object(extra)) = (&mut params, &extra_params) {
        for (k, v) in extra {
            p.insert(k.clone(), v.clone());
        }
    }
    let req = json!({"jsonrpc":"2.0","id":2,"method":method,"params":params});
    frame(&mut stdin, &req).map_err(|e| LspSessionError::Failed(e.to_string()))?;
    let result = session.wait_for(2, &mut stdin)?;

    // 4) best-effort shutdown; Drop kills the child regardless
    let _ = frame(
        &mut stdin,
        &json!({"jsonrpc":"2.0","id":3,"method":"shutdown"}),
    );
    let _ = frame(&mut stdin, &json!({"jsonrpc":"2.0","method":"exit"}));
    Ok(result)
}

/// A persistent LSP session: spawns one server, runs the handshake once, and
/// answers MANY requests over the same connection (opening each document lazily).
/// This is what makes batched reference resolution feasible — the one-shot
/// `request_document` spawns a fresh server per call, which cannot scale to
/// thousands of symbols. Bound to a single server command (one language family).
pub struct LspWorkspaceSession {
    session: Session,
    stdin: std::process::ChildStdin,
    root: std::path::PathBuf,
    language_id: &'static str,
    opened: HashSet<String>,
    next_id: i64,
    per_request: Duration,
}

impl LspWorkspaceSession {
    /// Start a persistent session for the server that handles `sample_rel`'s
    /// language. Unavailable if no server is mapped or installed.
    pub fn start(
        root: &Path,
        sample_rel: &str,
        per_request_secs: u64,
    ) -> Result<Self, LspSessionError> {
        let Some(cfg) = server_for(sample_rel) else {
            return Err(LspSessionError::Unavailable(format!(
                "no LSP server mapped for {sample_rel}"
            )));
        };
        if !binary_on_path(cfg.command) {
            return Err(LspSessionError::Unavailable(format!(
                "{} is not installed",
                cfg.command
            )));
        }
        let stderr = if std::env::var("XM_LSP_DEBUG").is_ok() {
            Stdio::inherit()
        } else {
            Stdio::null()
        };
        let mut child = Command::new(cfg.command)
            .args(cfg.args)
            .current_dir(root)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(stderr)
            .spawn()
            .map_err(|e| LspSessionError::Failed(format!("spawn {}: {e}", cfg.command)))?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| LspSessionError::Failed("no stdout".into()))?;
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            let mut reader = BufReader::new(stdout);
            while let Ok(Some(v)) = read_message(&mut reader) {
                if tx.send(v).is_err() {
                    break;
                }
            }
        });
        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(|| LspSessionError::Failed("no stdin".into()))?;
        let per_request = Duration::from_secs(per_request_secs.clamp(2, 60));
        // initialize takes longer than a normal request (server indexes the repo).
        let session = Session {
            child,
            rx,
            deadline: Instant::now() + Duration::from_secs(90),
        };
        // canonicalize so the rootUri matches the (canonical) URIs the server returns
        // — otherwise /var vs /private/var on macOS breaks reference-path stripping.
        let root = std::fs::canonicalize(root).unwrap_or_else(|_| root.to_path_buf());
        let root_uri = format!("file://{}", root.display());
        let init = json!({
            "jsonrpc":"2.0","id":1,"method":"initialize",
            "params":{"processId":null,"rootUri":root_uri,
                "capabilities":{"textDocument":{"references":{},"definition":{"linkSupport":true}}}}
        });
        frame(&mut stdin, &init).map_err(|e| LspSessionError::Failed(e.to_string()))?;
        session.wait_for(1, &mut stdin)?;
        frame(
            &mut stdin,
            &json!({"jsonrpc":"2.0","method":"initialized","params":{}}),
        )
        .map_err(|e| LspSessionError::Failed(e.to_string()))?;
        Ok(Self {
            session,
            stdin,
            root: root.to_path_buf(),
            language_id: cfg.language_id,
            opened: HashSet::new(),
            next_id: 1,
            per_request,
        })
    }

    /// Pre-open a document so the server includes it in its project view. Cross-file
    /// references (esp. tsserver's inferred projects without a tsconfig) only resolve
    /// once the referencing files are open.
    pub fn open(&mut self, rel: &str) {
        let _ = self.ensure_open(rel);
    }

    fn ensure_open(&mut self, rel: &str) -> Result<String, LspSessionError> {
        let abs = self.root.join(rel);
        let doc_uri = format!("file://{}", abs.display());
        if !self.opened.contains(rel) {
            let text = std::fs::read_to_string(&abs)
                .map_err(|e| LspSessionError::Failed(format!("read {rel}: {e}")))?;
            let did_open = json!({
                "jsonrpc":"2.0","method":"textDocument/didOpen",
                "params":{"textDocument":{"uri":doc_uri,"languageId":self.language_id,"version":1,"text":text}}
            });
            frame(&mut self.stdin, &did_open)
                .map_err(|e| LspSessionError::Failed(e.to_string()))?;
            // only mark open AFTER the read + send succeed, so a failure can retry.
            self.opened.insert(rel.to_string());
        }
        Ok(doc_uri)
    }

    /// References to the symbol at (line, character) in `rel`, as (path, line)
    /// pairs relative to the workspace root. Excludes the declaration itself.
    pub fn references(
        &mut self,
        rel: &str,
        line: u32,
        character: u32,
    ) -> Result<Vec<(String, u32)>, LspSessionError> {
        let doc_uri = self.ensure_open(rel)?;
        self.next_id += 1;
        let id = self.next_id;
        let req = json!({
            "jsonrpc":"2.0","id":id,"method":"textDocument/references",
            "params":{"textDocument":{"uri":doc_uri},"position":{"line":line,"character":character},
                "context":{"includeDeclaration":false}}
        });
        self.session.deadline = Instant::now() + self.per_request;
        frame(&mut self.stdin, &req).map_err(|e| LspSessionError::Failed(e.to_string()))?;
        let result = self.session.wait_for(id, &mut self.stdin)?;
        let root_prefix = format!("file://{}/", self.root.display());
        let mut out = Vec::new();
        if let Value::Array(items) = result {
            for it in items {
                let uri = it.get("uri").and_then(|u| u.as_str()).unwrap_or("");
                let l = it
                    .get("range")
                    .and_then(|r| r.get("start"))
                    .and_then(|s| s.get("line"))
                    .and_then(|n| n.as_u64());
                if let (Some(rel_path), Some(l)) = (uri.strip_prefix(&root_prefix), l) {
                    out.push((rel_path.to_string(), l as u32));
                }
            }
        }
        Ok(out)
    }
}

impl Drop for LspWorkspaceSession {
    fn drop(&mut self) {
        let _ = frame(
            &mut self.stdin,
            &json!({"jsonrpc":"2.0","id":999999,"method":"shutdown"}),
        );
        let _ = frame(&mut self.stdin, &json!({"jsonrpc":"2.0","method":"exit"}));
        // Session's Drop kills the child.
    }
}

/// Run a live `textDocument/documentSymbol` request and normalize the result
/// through the existing lsp normalizer.
pub fn live_document_symbols(
    workspace_id: &str,
    root: &Path,
    rel: &str,
    timeout_secs: u64,
) -> Result<RustDocumentSymbolsResult, LspSessionError> {
    let payload = request_document(
        root,
        rel,
        "textDocument/documentSymbol",
        json!({}),
        Duration::from_secs(timeout_secs.clamp(2, 120)),
    )?;
    Ok(lsp::normalize_document_symbols_payload(
        workspace_id,
        root,
        rel,
        "live-lsp",
        &payload,
    ))
}

/// Run a live `textDocument/hover` request at a position; returns the raw hover
/// payload (contents + range) — small enough to pass through unnormalized.
pub fn live_hover(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    request_document(
        root,
        rel,
        "textDocument/hover",
        json!({ "position": { "line": line, "character": character } }),
        Duration::from_secs(timeout_secs.clamp(2, 120)),
    )
}

// position_request runs any position-based LSP request and returns the raw result.
fn position_request(
    method: &str,
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    extra: Value,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    let mut params = json!({ "position": { "line": line, "character": character } });
    if let (Value::Object(p), Value::Object(ex)) = (&mut params, &extra) {
        for (k, v) in ex {
            p.insert(k.clone(), v.clone());
        }
    }
    request_document(
        root,
        rel,
        method,
        params,
        Duration::from_secs(timeout_secs.clamp(2, 120)),
    )
}

/// `textDocument/references` — all references to the symbol at a position
/// (includeDeclaration controls whether the definition itself is included). This
/// is the request the scope-resolved CALLS edge builder (S2) batches per symbol.
pub fn live_references(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    include_declaration: bool,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    position_request(
        "textDocument/references",
        root,
        rel,
        line,
        character,
        json!({ "context": { "includeDeclaration": include_declaration } }),
        timeout_secs,
    )
}

/// `textDocument/definition` — where the symbol at a position is defined.
pub fn live_definition(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    position_request(
        "textDocument/definition",
        root,
        rel,
        line,
        character,
        json!({}),
        timeout_secs,
    )
}

/// `textDocument/implementation` — concrete implementations of an interface/trait
/// member at a position.
pub fn live_implementation(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    position_request(
        "textDocument/implementation",
        root,
        rel,
        line,
        character,
        json!({}),
        timeout_secs,
    )
}

/// `textDocument/typeDefinition` — the type of the symbol at a position.
pub fn live_type_definition(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    position_request(
        "textDocument/typeDefinition",
        root,
        rel,
        line,
        character,
        json!({}),
        timeout_secs,
    )
}

/// `textDocument/rename` — the WorkspaceEdit that renames the symbol at a position
/// to `new_name` (the edit is returned, not applied).
pub fn live_rename(
    root: &Path,
    rel: &str,
    line: u32,
    character: u32,
    new_name: &str,
    timeout_secs: u64,
) -> Result<Value, LspSessionError> {
    position_request(
        "textDocument/rename",
        root,
        rel,
        line,
        character,
        json!({ "newName": new_name }),
        timeout_secs,
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unmapped_extension_is_unavailable() {
        let err = live_document_symbols("ws", Path::new("/tmp"), "notes.txt", 5).unwrap_err();
        matches!(err, LspSessionError::Unavailable(_));
    }

    #[test]
    fn server_mapping_covers_core_languages() {
        assert_eq!(server_for("a.rs").unwrap().command, "rust-analyzer");
        assert_eq!(server_for("a.tsx").unwrap().language_id, "typescriptreact");
        assert_eq!(server_for("a.go").unwrap().command, "gopls");
        assert!(server_for("a.txt").is_none());
    }

    #[test]
    fn position_requests_degrade_on_unmapped_extension() {
        // every new position request reports Unavailable for an unsupported file,
        // so a caller without a language server degrades gracefully.
        let r = Path::new("/tmp");
        assert!(matches!(
            live_references(r, "notes.txt", 0, 0, true, 5).unwrap_err(),
            LspSessionError::Unavailable(_)
        ));
        assert!(matches!(
            live_definition(r, "notes.txt", 0, 0, 5).unwrap_err(),
            LspSessionError::Unavailable(_)
        ));
        assert!(matches!(
            live_implementation(r, "notes.txt", 0, 0, 5).unwrap_err(),
            LspSessionError::Unavailable(_)
        ));
        assert!(matches!(
            live_type_definition(r, "notes.txt", 0, 0, 5).unwrap_err(),
            LspSessionError::Unavailable(_)
        ));
        assert!(matches!(
            live_rename(r, "notes.txt", 0, 0, "x", 5).unwrap_err(),
            LspSessionError::Unavailable(_)
        ));
    }
}
