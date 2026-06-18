//! Live LSP sessions: spawn a real language server (rust-analyzer, gopls,
//! typescript-language-server, clangd, …), run the initialize/initialized
//! handshake over stdio JSON-RPC with Content-Length framing, open a document,
//! and issue requests (documentSymbol, hover). The raw results feed the existing
//! `lsp.rs` normalizers. Servers that aren't installed degrade gracefully — the
//! same contract ast-grep uses — so this never hard-fails a workspace.

use std::io::{BufRead, BufReader, Write};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

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

fn server_for(rel: &str) -> Option<ServerConfig> {
    let ext = Path::new(rel)
        .extension()
        .and_then(|e| e.to_str())?
        .to_ascii_lowercase();
    let cfg = match ext.as_str() {
        "rs" => ServerConfig { command: "rust-analyzer", args: &[], language_id: "rust" },
        "go" => ServerConfig { command: "gopls", args: &[], language_id: "go" },
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
        "c" | "h" => ServerConfig { command: "clangd", args: &[], language_id: "c" },
        "cpp" | "cc" | "hpp" | "cxx" => {
            ServerConfig { command: "clangd", args: &[], language_id: "cpp" }
        }
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
                return Err(LspSessionError::Failed("timed out waiting for response".into()));
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
                    return Err(LspSessionError::Failed("lsp server closed the connection".into()));
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
        return Err(LspSessionError::Unavailable(format!("no LSP server mapped for {rel}")));
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

    let session = Session { child, rx, deadline: Instant::now() + timeout };

    // 1) initialize / initialized handshake
    let init = json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "processId": null,
            "rootUri": root_uri,
            "capabilities": {
                "textDocument": {
                    "documentSymbol": { "hierarchicalDocumentSymbolSupport": true },
                    "hover": { "contentFormat": ["plaintext", "markdown"] }
                }
            }
        }
    });
    frame(&mut stdin, &init).map_err(|e| LspSessionError::Failed(e.to_string()))?;
    session.wait_for(1, &mut stdin)?;
    frame(&mut stdin, &json!({"jsonrpc":"2.0","method":"initialized","params":{}}))
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
    let _ = frame(&mut stdin, &json!({"jsonrpc":"2.0","id":3,"method":"shutdown"}));
    let _ = frame(&mut stdin, &json!({"jsonrpc":"2.0","method":"exit"}));
    Ok(result)
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
}
