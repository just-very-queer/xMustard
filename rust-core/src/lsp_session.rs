//! Live LSP sessions: spawn a real language server (rust-analyzer, gopls,
//! typescript-language-server, clangd, …), run the initialize/initialized
//! handshake over stdio JSON-RPC with Content-Length framing, open a document,
//! and issue requests (documentSymbol, hover). The raw results feed the existing
//! `lsp.rs` normalizers. Servers that aren't installed degrade gracefully — the
//! same contract ast-grep uses — so this never hard-fails a workspace.

use std::collections::HashSet;
use std::io::Read;
use std::io::{BufRead, BufReader, Write};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
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

/// Bounds on one inbound LSP message: header line length, header line count, payload.
const MAX_HEADER_LINE_BYTES: usize = 8 << 10;
const MAX_HEADER_LINES: usize = 32;
const MAX_PAYLOAD_BYTES: usize = 16 << 20;
/// Bounds on messages queued between the reader thread and the requester. Server
/// notifications are dropped before queueing (nothing consumes them).
const MAX_QUEUED_MESSAGES: usize = 16;
const MAX_QUEUED_BYTES: usize = 32 << 20;

fn invalid(msg: String) -> std::io::Error {
    std::io::Error::new(std::io::ErrorKind::InvalidData, msg)
}

/// Read one Content-Length-framed message, returning it with its payload size.
fn read_framed<R: BufRead>(reader: &mut R) -> std::io::Result<Option<(Value, usize)>> {
    let mut content_length: Option<usize> = None;
    let mut line = Vec::new();
    let mut lines = 0;
    loop {
        line.clear();
        let n = reader
            .by_ref()
            .take(MAX_HEADER_LINE_BYTES as u64 + 1)
            .read_until(b'\n', &mut line)?;
        if n == 0 {
            return Ok(None); // EOF
        }
        if n > MAX_HEADER_LINE_BYTES {
            return Err(invalid(format!(
                "LSP header line exceeds {MAX_HEADER_LINE_BYTES} bytes"
            )));
        }
        lines += 1;
        if lines > MAX_HEADER_LINES {
            return Err(invalid(format!(
                "LSP header exceeds {MAX_HEADER_LINES} lines"
            )));
        }
        let text = String::from_utf8_lossy(&line);
        let trimmed = text.trim_end_matches(['\r', '\n']);
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
    if len > MAX_PAYLOAD_BYTES {
        return Err(invalid(format!(
            "LSP Content-Length {len} exceeds the {MAX_PAYLOAD_BYTES}-byte limit"
        )));
    }
    let mut buf = vec![0u8; len];
    reader.read_exact(&mut buf)?;
    Ok(serde_json::from_slice::<Value>(&buf).ok().map(|v| (v, len)))
}

/// Inbound message queue shared by the reader thread and the session.
struct Inbox {
    rx: mpsc::Receiver<(Value, usize)>,
    queued_bytes: Arc<AtomicUsize>,
    overflow: Arc<AtomicBool>,
}

/// Spawn the stdout reader: frames are bounded by read_framed; notifications are
/// dropped; responses and server requests are queued up to MAX_QUEUED_MESSAGES and
/// MAX_QUEUED_BYTES. Past either bound the message is dropped and `overflow` is set,
/// which fails the waiting request instead of growing memory or blocking the server.
fn spawn_reader(stdout: std::process::ChildStdout) -> Inbox {
    let (tx, rx) = mpsc::sync_channel::<(Value, usize)>(MAX_QUEUED_MESSAGES);
    let queued_bytes = Arc::new(AtomicUsize::new(0));
    let overflow = Arc::new(AtomicBool::new(false));
    let (q, o) = (queued_bytes.clone(), overflow.clone());
    thread::spawn(move || {
        let mut reader = BufReader::new(stdout);
        loop {
            let (msg, len) = match read_framed(&mut reader) {
                Ok(Some(m)) => m,
                Ok(None) => break,
                Err(_) => {
                    o.store(true, Ordering::SeqCst);
                    break;
                }
            };
            if msg.get("method").is_some() && msg.get("id").is_none() {
                continue; // notification: never consumed, never queued
            }
            if q.load(Ordering::SeqCst) + len > MAX_QUEUED_BYTES {
                o.store(true, Ordering::SeqCst);
                continue;
            }
            q.fetch_add(len, Ordering::SeqCst);
            match tx.try_send((msg, len)) {
                Ok(()) => {}
                Err(mpsc::TrySendError::Full(_)) => {
                    q.fetch_sub(len, Ordering::SeqCst);
                    o.store(true, Ordering::SeqCst);
                }
                Err(mpsc::TrySendError::Disconnected(_)) => break,
            }
        }
    });
    Inbox {
        rx,
        queued_bytes,
        overflow,
    }
}

/// Source text for a didOpen notification, through the shared bounded, no-follow,
/// regular-file-checked repo reader. The text is sent and then dropped, not retained.
fn read_document_text(root: &Path, rel: &str) -> Result<String, LspSessionError> {
    crate::symbolgraph::read_repo_file_beneath(root, rel)
        .map_err(|e| LspSessionError::Failed(format!("read {rel}: {e}")))
}

/// A live LSP session bound to one server process. Drop kills the child.
struct Session {
    child: Child,
    inbox: Inbox,
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
            if self.inbox.overflow.load(Ordering::SeqCst) {
                return Err(LspSessionError::Failed(
                    "lsp inbound message bound exceeded (oversized frame or queue overflow)".into(),
                ));
            }
            let budget = (self.deadline - now).min(Duration::from_millis(500));
            match self.inbox.rx.recv_timeout(budget) {
                Ok((msg, len)) => {
                    self.inbox.queued_bytes.fetch_sub(len, Ordering::SeqCst);
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
    let text = read_document_text(root, rel)?;
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
    let inbox = spawn_reader(stdout);
    let mut stdin = child
        .stdin
        .take()
        .ok_or_else(|| LspSessionError::Failed("no stdin".into()))?;

    let session = Session {
        child,
        inbox,
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
        let inbox = spawn_reader(stdout);
        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(|| LspSessionError::Failed("no stdin".into()))?;
        let per_request = Duration::from_secs(per_request_secs.clamp(2, 60));
        // initialize takes longer than a normal request (server indexes the repo).
        let session = Session {
            child,
            inbox,
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
            let text = read_document_text(&self.root, rel)?;
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
        assert!(matches!(err, LspSessionError::Unavailable(_)));
    }

    // Audit finding 5: header lines were read with an unbounded read_line.
    #[test]
    fn read_message_rejects_oversized_header() {
        let mut input = vec![b'X'; 1 << 20];
        input.extend_from_slice(b"\r\n\r\n");
        let mut reader = std::io::Cursor::new(input);
        assert!(
            read_framed(&mut reader).is_err(),
            "a 1 MiB header line must be refused, not buffered"
        );
        let many: String = (0..10_000).map(|i| format!("X-Junk-{i}: 1\r\n")).collect();
        let mut reader = std::io::Cursor::new(format!("{many}\r\n").into_bytes());
        assert!(
            read_framed(&mut reader).is_err(),
            "unbounded header count accepted"
        );
        let huge = format!("Content-Length: {}\r\n\r\n", MAX_PAYLOAD_BYTES + 1);
        let mut reader = std::io::Cursor::new(huge.into_bytes());
        assert!(
            read_framed(&mut reader).is_err(),
            "payload over the cap accepted"
        );
        let ok = "Content-Length: 2\r\n\r\n{}";
        let mut reader = std::io::Cursor::new(ok.as_bytes().to_vec());
        assert_eq!(read_framed(&mut reader).unwrap().unwrap().1, 2);
    }

    fn frames(messages: &[Value]) -> Vec<u8> {
        let mut out = Vec::new();
        for m in messages {
            frame(&mut out, m).unwrap();
        }
        out
    }

    fn inbox_over(bytes: Vec<u8>) -> (Inbox, std::process::Child) {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("frames");
        std::fs::write(&path, bytes).unwrap();
        let mut child = Command::new("cat")
            .arg(&path)
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        let inbox = spawn_reader(child.stdout.take().unwrap());
        let _ = child.wait();
        std::thread::sleep(Duration::from_millis(200));
        (inbox, child)
    }

    // Audit finding 5: the reader queued every message on an unbounded channel.
    #[cfg(unix)]
    #[test]
    fn inbound_queue_drops_notifications_and_bounds_responses() {
        // a notification flood is never queued: only the one response arrives.
        let mut msgs: Vec<Value> = (0..2_000)
            .map(|i| json!({"jsonrpc":"2.0","method":"$/progress","params":{"i":i}}))
            .collect();
        msgs.push(json!({"jsonrpc":"2.0","id":7,"result":"ok"}));
        let (inbox, _c) = inbox_over(frames(&msgs));
        let (first, _) = inbox.rx.recv_timeout(Duration::from_secs(2)).unwrap();
        assert_eq!(first["id"], 7);
        assert!(inbox.rx.try_recv().is_err());
        assert!(!inbox.overflow.load(Ordering::SeqCst));

        // more unconsumed responses than the queue holds: bounded and flagged.
        let msgs: Vec<Value> = (0..(MAX_QUEUED_MESSAGES as i64 + 10))
            .map(|i| json!({"jsonrpc":"2.0","id":i,"result":null}))
            .collect();
        let (inbox, _c) = inbox_over(frames(&msgs));
        assert!(
            inbox.overflow.load(Ordering::SeqCst),
            "queue overflow not flagged"
        );
        let queued = std::iter::from_fn(|| inbox.rx.try_recv().ok()).count();
        assert!(queued <= MAX_QUEUED_MESSAGES, "queued {queued} messages");
    }

    // Audit finding 5: didOpen text used a raw, symlink-following, unbounded read.
    #[cfg(unix)]
    #[test]
    fn document_text_uses_bounded_no_follow_reader() {
        let root = tempfile::TempDir::new().unwrap();
        let outside = tempfile::TempDir::new().unwrap();
        std::fs::write(outside.path().join("secret.rs"), "SECRET").unwrap();
        std::os::unix::fs::symlink(
            outside.path().join("secret.rs"),
            root.path().join("link.rs"),
        )
        .unwrap();
        assert!(
            read_document_text(root.path(), "link.rs").is_err(),
            "symlink followed"
        );
        let big = vec![b'a'; (crate::symbolgraph::MAX_REPO_FILE_BYTES as usize) + 1];
        std::fs::write(root.path().join("big.rs"), big).unwrap();
        assert!(
            read_document_text(root.path(), "big.rs").is_err(),
            "oversized read"
        );
        std::fs::write(root.path().join("ok.rs"), "fn ok() {}").unwrap();
        assert_eq!(
            read_document_text(root.path(), "ok.rs").unwrap(),
            "fn ok() {}"
        );
        // Fable F4: exact path, no trimming onto a decoy; a whitespace-named symlink is
        // still refused.
        std::fs::write(root.path().join(" lead.rs"), "REAL").unwrap();
        std::fs::write(root.path().join("lead.rs"), "DECOY").unwrap();
        assert_eq!(read_document_text(root.path(), " lead.rs").unwrap(), "REAL");
        std::os::unix::fs::symlink(outside.path().join("secret.rs"), root.path().join(" s.rs"))
            .unwrap();
        assert!(read_document_text(root.path(), " s.rs").is_err());
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
