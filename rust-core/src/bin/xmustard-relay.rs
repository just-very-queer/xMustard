#![forbid(unsafe_code)]
//! xmustard-relay: a stdio MCP server for clients that can only launch a command. It
//! relays newline-delimited JSON-RPC from stdin to the xMustard API's Streamable HTTP
//! endpoint (`/mcp`) and writes every message the API answers with, JSON bodies and
//! server-sent events alike, to stdout. It holds no tool logic: tools/list, the tool
//! calls, workspace resolution and evidence all run in the API, the same as for a
//! client configured with the URL.
//!
//! The relay is std-only (no async runtime, no HTTP or JSON crate) so that one per
//! agent stays within a few MiB of RSS. It therefore speaks plain `http://` only:
//! point it at the loopback API, or at a local TLS-terminating proxy.
//!
//! Usage:
//!
//! ```text
//! xmustard-relay [--url URL] [--workspace ID] [--client NAME] [--mode full|readonly] [--schema lean|full] [--allow-insecure-remote]
//! ```
//!
//! `--url` defaults to `XMUSTARD_MCP_URL`, else `XMUSTARD_API_BASE` + `/mcp` (the Go
//! shim's setting, so the relay is a drop-in for it), else `http://127.0.0.1:8042/mcp`. The bearer
//! token is read from `XMUSTARD_API_TOKEN` (sent as a header, never in the URL, and
//! only to a loopback host unless `--allow-insecure-remote` is given);
//! `--workspace` defaults to `XMUSTARD_WORKSPACE_ID` and is sent as
//! `X-Xmustard-Workspace`. Each request runs on its own thread, so a long tool call
//! never blocks a cancellation or a roots/list answer behind it; notifications and
//! answers are relayed in order on the reading thread. When the API forgets
//! the session (a restart), the relay replays the client's initialize and retries once.
//! While the API is down for a restart (an upgrade or a crash under its service
//! manager), a request waits up to `RESTART_GRACE` for it to listen again instead of
//! failing at once; the connection is refused before anything is sent, so the wait
//! never repeats a request.

use std::env;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream, ToSocketAddrs};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// Largest stdin message relayed (the stdio shim's framing cap).
const MAX_MESSAGE_BYTES: usize = 8 << 20;
const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
/// How long a request waits for a refusing API to come back (a daemon restart).
const RESTART_GRACE: Duration = Duration::from_secs(10);
const THREAD_STACK: usize = 256 << 10;

fn main() {
    let cfg = match Config::from_args(env::args().skip(1), |k| env::var(k).ok()) {
        Ok(cfg) => cfg,
        Err(msg) => {
            eprintln!("xmustard-relay: {msg}");
            std::process::exit(2);
        }
    };
    let out: Output = Arc::new(Mutex::new(Box::new(io::stdout())));
    run(Arc::new(cfg), io::stdin().lock(), out);
}

/// Where and how to reach the endpoint.
#[derive(Debug, Clone)]
struct Config {
    host: String,
    port: u16,
    /// Path and query sent in the request line.
    target: String,
    token: Option<String>,
    workspace: Option<String>,
    /// How long a request waits while the API refuses connections (RESTART_GRACE).
    restart_grace: Duration,
}

impl Config {
    fn from_args(
        args: impl Iterator<Item = String>,
        getenv: impl Fn(&str) -> Option<String>,
    ) -> Result<Config, String> {
        let nonblank =
            |v: Option<String>| v.map(|s| s.trim().to_string()).filter(|s| !s.is_empty());
        // XMUSTARD_API_BASE is the shim's setting: the relay is a drop-in for it
        let mut url = nonblank(getenv("XMUSTARD_MCP_URL"))
            .or_else(|| {
                nonblank(getenv("XMUSTARD_API_BASE"))
                    .map(|b| format!("{}/mcp", b.trim_end_matches('/')))
            })
            .unwrap_or_else(|| "http://127.0.0.1:8042/mcp".to_string());
        let mut workspace = nonblank(getenv("XMUSTARD_WORKSPACE_ID"));
        let mut query: Vec<(String, String)> = Vec::new();
        let mut allow_remote = false;
        let mut args = args;
        while let Some(flag) = args.next() {
            let (name, inline) = match flag.split_once('=') {
                Some((n, v)) => (n.to_string(), Some(v.to_string())),
                None => (flag.clone(), None),
            };
            let key = match name.as_str() {
                "--url" | "--workspace" | "--client" | "--mode" | "--schema" => name,
                "--allow-insecure-remote" => {
                    allow_remote = true;
                    continue;
                }
                "-h" | "--help" => return Err(USAGE.to_string()),
                _ => return Err(format!("unknown argument {flag}\n{USAGE}")),
            };
            let value = inline
                .or_else(|| args.next())
                .ok_or_else(|| format!("{key} needs a value"))?;
            match key.as_str() {
                "--url" => url = value,
                "--workspace" => workspace = Some(value),
                param => query.push((param.trim_start_matches("--").to_string(), value)),
            }
        }
        let (host, port, mut target) = parse_http_url(&url)?;
        let token = nonblank(getenv("XMUSTARD_API_TOKEN"));
        // plain http:// carries the bearer token in cleartext: loopback only, unless the
        // operator opts in (a TLS-terminating proxy on another host is their call)
        if token.is_some() && !allow_remote && !is_loopback(&host) {
            return Err(format!(
                "{url}: refusing to send XMUSTARD_API_TOKEN over plain http to a non-loopback host; \
                 use the loopback API or a local TLS proxy, or pass --allow-insecure-remote"
            ));
        }
        for (k, v) in query {
            target.push(if target.contains('?') { '&' } else { '?' });
            target.push_str(&k);
            target.push('=');
            target.push_str(&percent_encode(&v));
        }
        Ok(Config {
            host,
            port,
            target,
            token,
            workspace,
            restart_grace: RESTART_GRACE,
        })
    }
}

const USAGE: &str = "usage: xmustard-relay [--url URL] [--workspace ID] [--client NAME] [--mode full|readonly] [--schema lean|full] [--allow-insecure-remote]";

/// localhost, 127.0.0.0/8 or ::1 (bracketed or not).
fn is_loopback(host: &str) -> bool {
    let bare = host.trim_start_matches('[').trim_end_matches(']');
    bare.eq_ignore_ascii_case("localhost")
        || bare
            .parse::<std::net::IpAddr>()
            .is_ok_and(|ip| ip.is_loopback())
}

/// Splits `http://host[:port]/path[?query]`. Only plain HTTP is spoken.
fn parse_http_url(url: &str) -> Result<(String, u16, String), String> {
    let rest = url.strip_prefix("http://").ok_or_else(|| {
        format!(
            "{url}: only http:// URLs are supported (use the loopback API or a local TLS proxy)"
        )
    })?;
    let (authority, path) = match rest.find('/') {
        Some(i) => (&rest[..i], rest[i..].to_string()),
        None => (rest, "/mcp".to_string()),
    };
    let (host, port) = match authority.rsplit_once(':') {
        Some((h, p)) if !h.ends_with(']') || h.starts_with('[') => (
            h.to_string(),
            p.parse::<u16>().map_err(|_| format!("{url}: bad port"))?,
        ),
        _ => (authority.to_string(), 80),
    };
    if host.is_empty() {
        return Err(format!("{url}: missing host"));
    }
    Ok((host, port, path))
}

fn percent_encode(s: &str) -> String {
    s.bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                (b as char).to_string()
            }
            _ => format!("%{b:02X}"),
        })
        .collect()
}

type Output = Arc<Mutex<Box<dyn Write + Send>>>;

/// Writes one JSON-RPC message as one stdout line.
fn emit(out: &Output, msg: &str) {
    let line = msg.trim().replace(['\r', '\n'], " ");
    if line.is_empty() {
        return;
    }
    let mut w = out.lock().unwrap_or_else(|e| e.into_inner());
    let _ = w.write_all(line.as_bytes());
    let _ = w.write_all(b"\n");
    let _ = w.flush();
}

/// The session the relay holds with the endpoint, and the client's initialize to
/// replay when the endpoint forgets it.
#[derive(Default)]
struct Session {
    id: Option<String>,
    init: Option<String>,
}

type Shared = Arc<Mutex<Session>>;

fn run(cfg: Arc<Config>, input: impl BufRead, out: Output) {
    let session: Shared = Arc::default();
    let mut workers = Vec::new();
    let mut input = input;
    while let Ok(Some((line, oversized))) = read_line(&mut input) {
        if oversized {
            emit(
                &out,
                &rpc_error("null", -32600, "request exceeds max message size"),
            );
            continue;
        }
        let line = line.trim().to_string();
        if line.is_empty() {
            continue;
        }
        let head = scan_top_level(&line);
        let initialize = head.method.as_deref() == Some("initialize");
        if initialize {
            session.lock().unwrap_or_else(|e| e.into_inner()).init = Some(line.clone());
        }
        // initialize, notifications and answers to server requests are relayed in order
        // before the next line is read (so a tool call follows notifications/initialized
        // and carries the session); requests get a thread of their own
        if initialize || head.id.is_none() || head.method.is_none() {
            relay(&cfg, &session, &out, &line, &head, initialize);
            continue;
        }
        workers.retain(|h: &thread::JoinHandle<()>| !h.is_finished());
        let (cfg, session, out) = (Arc::clone(&cfg), Arc::clone(&session), Arc::clone(&out));
        let spawned = thread::Builder::new()
            .stack_size(THREAD_STACK)
            .spawn(move || relay(&cfg, &session, &out, &line, &head, false));
        if let Ok(h) = spawned {
            workers.push(h);
        }
    }
    for h in workers {
        let _ = h.join();
    }
    let id = session.lock().unwrap_or_else(|e| e.into_inner()).id.take();
    if let Some(id) = id {
        let _ = send(&cfg, "DELETE", &[], Some(&id));
    }
}

/// Reads one line, capped at MAX_MESSAGE_BYTES (an oversized line is drained, not
/// buffered). None at end of input.
fn read_line(r: &mut impl BufRead) -> io::Result<Option<(String, bool)>> {
    let mut buf = Vec::new();
    let mut oversized = false;
    loop {
        let chunk = r.fill_buf()?;
        if chunk.is_empty() {
            return Ok(if buf.is_empty() && !oversized {
                None
            } else {
                Some((finish(buf), oversized))
            });
        }
        let (take, done) = match chunk.iter().position(|&b| b == b'\n') {
            Some(i) => (i + 1, true),
            None => (chunk.len(), false),
        };
        if buf.len() + take > MAX_MESSAGE_BYTES {
            oversized = true;
            buf.clear();
        } else if !oversized {
            buf.extend_from_slice(&chunk[..take]);
        }
        r.consume(take);
        if done {
            return Ok(Some((finish(buf), oversized)));
        }
    }
}

fn finish(buf: Vec<u8>) -> String {
    String::from_utf8_lossy(&buf).into_owned()
}

/// Posts one message and writes the endpoint's answer to stdout. A request the
/// endpoint cannot answer gets a JSON-RPC error, so the client never waits forever.
fn relay(cfg: &Config, session: &Shared, out: &Output, line: &str, head: &Head, initialize: bool) {
    let sid = if initialize {
        None
    } else {
        session.lock().unwrap_or_else(|e| e.into_inner()).id.clone()
    };
    let mut resp = send(cfg, "POST", line.as_bytes(), sid.as_deref());
    if !initialize
        && sid.is_some()
        && matches!(&resp, Ok(r) if r.status == 404)
        && reinitialize(cfg, session, sid.as_deref().unwrap_or_default())
    {
        let sid = session.lock().unwrap_or_else(|e| e.into_inner()).id.clone();
        resp = send(cfg, "POST", line.as_bytes(), sid.as_deref());
    }
    let id = head.id.as_deref();
    match resp {
        Err(e) => {
            if let Some(id) = id {
                emit(
                    out,
                    &rpc_error(
                        id,
                        -32603,
                        &format!(
                            "xmustard API unreachable at http://{}:{} ({e})",
                            cfg.host, cfg.port
                        ),
                    ),
                );
            }
        }
        Ok(mut r) => {
            if initialize {
                session.lock().unwrap_or_else(|e| e.into_inner()).id = r.header("mcp-session-id");
            }
            deliver(&mut r, out, id);
        }
    }
}

/// Replays the client's initialize (and initialized notification) to open a new
/// session after the endpoint forgot the old one. The answer is not relayed: the
/// client already has one. Only the first of several requests that got a 404 for the
/// same `stale` id replays it; the others retry with the session it opened.
fn reinitialize(cfg: &Config, session: &Shared, stale: &str) -> bool {
    let mut s = session.lock().unwrap_or_else(|e| e.into_inner());
    if s.id.as_deref() != Some(stale) {
        return s.id.is_some();
    }
    let Some(init) = s.init.clone() else {
        return false;
    };
    let Ok(mut r) = send(cfg, "POST", init.as_bytes(), None) else {
        return false;
    };
    let _ = io::copy(&mut r.body, &mut io::sink());
    s.id = r.header("mcp-session-id");
    let Some(id) = s.id.clone() else { return false };
    drop(s);
    let note = br#"{"jsonrpc":"2.0","method":"notifications/initialized"}"#;
    let _ = send(cfg, "POST", note, Some(&id));
    true
}

/// Writes a response to stdout: every event of a stream, or a JSON body. A request
/// whose answer carries no JSON-RPC message gets an error naming the HTTP status.
fn deliver(r: &mut Response, out: &Output, id: Option<&str>) {
    if r.header("content-type")
        .is_some_and(|t| t.starts_with("text/event-stream"))
    {
        // a stream that ends without a response is a cancelled call: nothing is owed
        for_each_event(&mut r.body, |data| emit(out, data));
        return;
    }
    let mut body = String::new();
    let _ = r.body.read_to_string(&mut body);
    if body.trim_start().starts_with('{') && body.contains("\"jsonrpc\"") {
        emit(out, &body);
    } else if let (Some(id), true) = (id, r.status >= 300) {
        emit(
            out,
            &rpc_error(
                id,
                -32603,
                &format!("xmustard MCP endpoint answered HTTP {}", r.status),
            ),
        );
    }
}

fn rpc_error(id: &str, code: i32, message: &str) -> String {
    format!(
        r#"{{"jsonrpc":"2.0","id":{id},"error":{{"code":{code},"message":{}}}}}"#,
        json_string(message)
    )
}

fn json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Server-sent events: calls f with the data of each event (multi-line data joined).
fn for_each_event(r: &mut impl BufRead, mut f: impl FnMut(&str)) {
    let mut data = String::new();
    let mut line = String::new();
    loop {
        line.clear();
        match r.read_line(&mut line) {
            Ok(0) | Err(_) => break,
            Ok(_) => {}
        }
        let l = line.trim_end_matches(['\r', '\n']);
        if l.is_empty() {
            if !data.is_empty() {
                f(&data);
                data.clear();
            }
        } else if let Some(v) = l.strip_prefix("data:") {
            if !data.is_empty() {
                data.push('\n');
            }
            data.push_str(v.strip_prefix(' ').unwrap_or(v));
        }
    }
    if !data.is_empty() {
        f(&data);
    }
}

/// The top-level members of a message the relay routes on.
#[derive(Debug, Default, PartialEq)]
struct Head {
    method: Option<String>,
    /// The raw JSON of the id (a string keeps its quotes).
    id: Option<String>,
}

/// Reads the top-level "method" and "id" of a JSON object without a JSON library:
/// it walks the members, skipping nested values and strings with their escapes.
fn scan_top_level(s: &str) -> Head {
    let b = s.as_bytes();
    let mut head = Head::default();
    let mut i = skip_ws(b, 0);
    if b.get(i) != Some(&b'{') {
        return head;
    }
    i += 1;
    loop {
        i = skip_ws(b, i);
        let Some(key_end) = string_end(b, i) else {
            return head;
        };
        let key = &s[i + 1..key_end - 1];
        i = skip_ws(b, key_end);
        if b.get(i) != Some(&b':') {
            return head;
        }
        i = skip_ws(b, i + 1);
        let Some(val_end) = value_end(b, i) else {
            return head;
        };
        let raw = &s[i..val_end];
        match key {
            "method" => {
                head.method = raw
                    .strip_prefix('"')
                    .and_then(|v| v.strip_suffix('"'))
                    .map(str::to_string)
            }
            "id" if raw != "null" => head.id = Some(raw.to_string()),
            _ => {}
        }
        i = skip_ws(b, val_end);
        match b.get(i) {
            Some(b',') => i += 1,
            _ => return head,
        }
    }
}

fn skip_ws(b: &[u8], mut i: usize) -> usize {
    while b.get(i).is_some_and(|c| c.is_ascii_whitespace()) {
        i += 1;
    }
    i
}

/// End (exclusive) of the string starting at b[i] == '"'.
fn string_end(b: &[u8], i: usize) -> Option<usize> {
    if b.get(i) != Some(&b'"') {
        return None;
    }
    let mut j = i + 1;
    while j < b.len() {
        match b[j] {
            b'\\' => j += 2,
            b'"' => return Some(j + 1),
            _ => j += 1,
        }
    }
    None
}

/// End (exclusive) of the JSON value starting at b[i].
fn value_end(b: &[u8], i: usize) -> Option<usize> {
    match b.get(i)? {
        b'"' => string_end(b, i),
        b'{' | b'[' => {
            let mut depth = 0usize;
            let mut j = i;
            while j < b.len() {
                match b[j] {
                    b'"' => {
                        j = string_end(b, j)?;
                        continue;
                    }
                    b'{' | b'[' => depth += 1,
                    b'}' | b']' => {
                        depth -= 1;
                        if depth == 0 {
                            return Some(j + 1);
                        }
                    }
                    _ => {}
                }
                j += 1;
            }
            None
        }
        _ => Some(
            (i..b.len())
                .find(|&j| matches!(b[j], b',' | b'}' | b']') || b[j].is_ascii_whitespace())
                .unwrap_or(b.len()),
        ),
    }
}

/// One HTTP response: status, headers and a body reader (de-chunked).
struct Response {
    status: u16,
    headers: Vec<(String, String)>,
    body: Box<dyn BufRead + Send>,
}

impl Response {
    fn header(&self, name: &str) -> Option<String> {
        self.headers
            .iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(name))
            .map(|(_, v)| v.clone())
    }
}

/// Sends one request on a fresh connection (Connection: close). A POST waits out a
/// restarting API; the session DELETE at exit does not.
fn send(cfg: &Config, method: &str, body: &[u8], session: Option<&str>) -> io::Result<Response> {
    let addr = (cfg.host.as_str(), cfg.port)
        .to_socket_addrs()?
        .next()
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no address"))?;
    let grace = if method == "POST" {
        cfg.restart_grace
    } else {
        Duration::ZERO
    };
    let mut stream = connect(&addr, grace)?;
    let _ = stream.set_nodelay(true);
    let mut req = format!(
        "{method} {} HTTP/1.1\r\nHost: {}:{}\r\nAccept: application/json, text/event-stream\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n",
        cfg.target,
        cfg.host,
        cfg.port,
        body.len()
    );
    let headers = [
        (
            "Authorization",
            cfg.token.as_ref().map(|t| format!("Bearer {t}")),
        ),
        ("Mcp-Session-Id", session.map(str::to_string)),
        ("X-Xmustard-Workspace", cfg.workspace.clone()),
    ];
    for (k, v) in headers {
        if let Some(v) = v {
            req.push_str(&format!("{k}: {v}\r\n"));
        }
    }
    req.push_str("\r\n");
    stream.write_all(req.as_bytes())?;
    stream.write_all(body)?;
    stream.flush()?;
    read_response(BufReader::new(stream))
}

/// Connects to addr, retrying a refused connection (nothing listens: the API is
/// restarting) with backoff until grace has passed.
fn connect(addr: &SocketAddr, grace: Duration) -> io::Result<TcpStream> {
    let deadline = Instant::now() + grace;
    let mut wait = Duration::from_millis(50);
    loop {
        match TcpStream::connect_timeout(addr, CONNECT_TIMEOUT) {
            Err(e)
                if e.kind() == io::ErrorKind::ConnectionRefused
                    && Instant::now() + wait < deadline =>
            {
                thread::sleep(wait);
                wait = (wait * 2).min(Duration::from_millis(500));
            }
            result => return result,
        }
    }
}

fn read_response<R: BufRead + Send + 'static>(mut r: R) -> io::Result<Response> {
    let bad = |m: &str| io::Error::new(io::ErrorKind::InvalidData, m.to_string());
    let mut line = String::new();
    r.read_line(&mut line)?;
    let status = line
        .split_whitespace()
        .nth(1)
        .and_then(|s| s.parse::<u16>().ok())
        .ok_or_else(|| bad("malformed status line"))?;
    let mut headers = Vec::new();
    loop {
        line.clear();
        if r.read_line(&mut line)? == 0 {
            return Err(bad("headers cut off"));
        }
        let l = line.trim_end();
        if l.is_empty() {
            break;
        }
        if let Some((k, v)) = l.split_once(':') {
            headers.push((k.trim().to_string(), v.trim().to_string()));
        }
    }
    let mut resp = Response {
        status,
        headers,
        body: Box::new(io::empty()),
    };
    resp.body = if resp
        .header("transfer-encoding")
        .is_some_and(|v| v.eq_ignore_ascii_case("chunked"))
    {
        Box::new(BufReader::new(Chunked {
            inner: r,
            left: 0,
            done: false,
        }))
    } else if let Some(n) = resp
        .header("content-length")
        .and_then(|v| v.parse::<u64>().ok())
    {
        Box::new(r.take(n))
    } else {
        Box::new(r)
    };
    Ok(resp)
}

/// Decodes a chunked transfer-encoded body.
struct Chunked<R> {
    inner: R,
    left: usize,
    done: bool,
}

impl<R: BufRead> Read for Chunked<R> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if self.done || buf.is_empty() {
            return Ok(0);
        }
        if self.left == 0 {
            let mut line = String::new();
            self.inner.read_line(&mut line)?;
            let size = line.trim().split(';').next().unwrap_or("");
            self.left = usize::from_str_radix(size, 16)
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "bad chunk size"))?;
            if self.left == 0 {
                self.done = true;
                return Ok(0);
            }
        }
        let want = buf.len().min(self.left);
        let n = self.inner.read(&mut buf[..want])?;
        if n == 0 {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "chunk cut off",
            ));
        }
        self.left -= n;
        if self.left == 0 {
            let mut crlf = String::new();
            self.inner.read_line(&mut crlf)?;
        }
        Ok(n)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::TcpListener;
    use std::sync::mpsc;

    #[test]
    fn scans_top_level_members_only() {
        let h = scan_top_level(
            r#"{"params":{"id":9,"method":"x","s":"a\"}"},"id":"c-1","method":"tools/call"}"#,
        );
        assert_eq!(
            h,
            Head {
                method: Some("tools/call".into()),
                id: Some("\"c-1\"".into())
            }
        );
        let n = scan_top_level(
            r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}"#,
        );
        assert_eq!(n.id, None);
        assert_eq!(
            scan_top_level(r#"{"id":42,"result":[1,{"a":[]}]}"#)
                .id
                .as_deref(),
            Some("42")
        );
        assert_eq!(scan_top_level("not json"), Head::default());
    }

    #[test]
    fn parses_events_and_chunks() {
        let mut seen = Vec::new();
        let sse = "event: message\ndata: {\"a\":1}\n\n: comment\ndata: {\"b\":\ndata: 2}\n\ndata:{\"c\":3}";
        for_each_event(&mut sse.as_bytes(), |d| seen.push(d.to_string()));
        assert_eq!(seen, vec!["{\"a\":1}", "{\"b\":\n2}", "{\"c\":3}"]);
        let raw = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n7;x=y\r\n, world\r\n0\r\n\r\n";
        let mut r = read_response(io::Cursor::new(raw.as_bytes().to_vec())).unwrap();
        let mut body = String::new();
        r.body.read_to_string(&mut body).unwrap();
        assert_eq!((r.status, body.as_str()), (200, "hello, world"));
    }

    #[test]
    fn config_from_flags_and_env() {
        let env = |k: &str| match k {
            "XMUSTARD_API_TOKEN" => Some("tok".to_string()),
            "XMUSTARD_WORKSPACE_ID" => Some("ws-env".to_string()),
            _ => None,
        };
        let args = [
            "--url=http://localhost:9000/mcp",
            "--client",
            "codex",
            "--mode",
            "readonly",
            "--schema=full",
        ];
        let c = Config::from_args(args.iter().map(|s| s.to_string()), env).unwrap();
        assert_eq!((c.host.as_str(), c.port), ("localhost", 9000));
        assert_eq!(c.target, "/mcp?client=codex&mode=readonly&schema=full");
        assert_eq!(
            (c.token.as_deref(), c.workspace.as_deref()),
            (Some("tok"), Some("ws-env"))
        );
        assert!(
            Config::from_args(
                ["--url", "https://x/mcp"].iter().map(|s| s.to_string()),
                |_| None
            )
            .is_err()
        );
        assert!(Config::from_args(["--bogus"].iter().map(|s| s.to_string()), |_| None).is_err());
        // the token never goes to a non-loopback host over plain http without opt-in
        let remote = |extra: &[&str]| {
            let args = ["--url", "http://api.example.com:8042/mcp"]
                .iter()
                .chain(extra)
                .map(|s| s.to_string());
            Config::from_args(args, env)
        };
        assert!(remote(&[]).is_err());
        assert!(remote(&["--allow-insecure-remote"]).is_ok());
        for url in [
            "http://127.0.0.2:1/mcp",
            "http://[::1]:1/mcp",
            "http://LOCALHOST/mcp",
        ] {
            assert!(
                Config::from_args(["--url", url].iter().map(|s| s.to_string()), env).is_ok(),
                "{url}"
            );
        }
        assert!(
            Config::from_args(
                ["--url", "http://api.example.com/mcp"]
                    .iter()
                    .map(|s| s.to_string()),
                |_| None
            )
            .is_ok()
        );
        let base = |k: &str| (k == "XMUSTARD_API_BASE").then(|| "http://127.0.0.1:9/".to_string());
        let b = Config::from_args(std::iter::empty(), base).unwrap();
        assert_eq!((b.port, b.target.as_str()), (9, "/mcp"));
        let d = Config::from_args(std::iter::empty(), |_| None).unwrap();
        assert_eq!(
            (d.host.as_str(), d.port, d.target.as_str()),
            ("127.0.0.1", 8042, "/mcp")
        );
    }

    /// One request as the fake endpoint saw it.
    #[derive(Debug, Clone)]
    struct Seen {
        method: String,
        session: Option<String>,
        auth: Option<String>,
        body: String,
    }

    fn read_request(stream: &TcpStream) -> Seen {
        let mut r = BufReader::new(stream.try_clone().unwrap());
        let mut line = String::new();
        r.read_line(&mut line).unwrap();
        let method = line.split_whitespace().next().unwrap_or("").to_string();
        let (mut len, mut session, mut auth) = (0usize, None, None);
        loop {
            line.clear();
            r.read_line(&mut line).unwrap();
            let l = line.trim_end();
            if l.is_empty() {
                break;
            }
            let (k, v) = l.split_once(':').unwrap();
            match k.to_ascii_lowercase().as_str() {
                "content-length" => len = v.trim().parse().unwrap(),
                "mcp-session-id" => session = Some(v.trim().to_string()),
                "authorization" => auth = Some(v.trim().to_string()),
                _ => {}
            }
        }
        let mut body = vec![0; len];
        r.read_exact(&mut body).unwrap();
        Seen {
            method,
            session,
            auth,
            body: String::from_utf8(body).unwrap(),
        }
    }

    fn respond(mut s: &TcpStream, status: &str, headers: &str, body: &str) {
        let _ = write!(
            s,
            "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\n\r\n{body}",
            body.len()
        );
    }

    /// A fake endpoint: initialize opens session "s1" (then "s2" after it forgets s1);
    /// tools/call "slow" streams a progress event and holds the stream until cancelled;
    /// tools/call "fast" streams a progress event and its result in chunks.
    fn fake_endpoint() -> (u16, mpsc::Receiver<Seen>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let (tx, rx) = mpsc::channel();
        let held: Arc<Mutex<Option<TcpStream>>> = Arc::default();
        let sessions = Arc::new(Mutex::new(0u32));
        thread::spawn(move || {
            for stream in listener.incoming() {
                let stream = stream.unwrap();
                let seen = read_request(&stream);
                tx.send(seen.clone()).unwrap();
                let head = scan_top_level(&seen.body);
                let method = head.method.clone().unwrap_or_default();
                let live = format!("s{}", sessions.lock().unwrap());
                match (seen.method.as_str(), method.as_str()) {
                    ("DELETE", _) => respond(&stream, "204 No Content", "", ""),
                    (_, "initialize") => {
                        let mut n = sessions.lock().unwrap();
                        *n += 1;
                        let body = format!(
                            r#"{{"jsonrpc":"2.0","id":{},"result":{{"protocolVersion":"2025-06-18"}}}}"#,
                            head.id.unwrap()
                        );
                        respond(
                            &stream,
                            "200 OK",
                            &format!("Mcp-Session-Id: s{n}\r\nContent-Type: application/json\r\n"),
                            &body,
                        );
                    }
                    _ if seen.session.as_deref() != Some(live.as_str()) => {
                        respond(&stream, "404 Not Found", "", "")
                    }
                    (_, "notifications/cancelled") => {
                        respond(&stream, "202 Accepted", "", "");
                        if let Some(s) = held.lock().unwrap().take() {
                            let _ = s.shutdown(std::net::Shutdown::Both);
                        }
                    }
                    (_, m) if m.starts_with("notifications/") || head.method.is_none() => {
                        respond(&stream, "202 Accepted", "", "")
                    }
                    (_, "tools/call") => {
                        let mut s = &stream;
                        let id = head.id.unwrap();
                        let progress = r#"{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":1}}"#;
                        let _ = write!(
                            s,
                            "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"
                        );
                        let chunk = |s: &mut &TcpStream, d: &str| {
                            let _ = write!(s, "{:x}\r\n{d}\r\n", d.len());
                        };
                        chunk(&mut s, &format!("event: message\ndata: {progress}\n\n"));
                        if seen.body.contains("slow") {
                            *held.lock().unwrap() = Some(stream.try_clone().unwrap());
                            continue;
                        }
                        let result = format!(
                            r#"event: message{}data: {{"jsonrpc":"2.0","id":{id},"result":{{"ok":true}}}}{}"#,
                            "\n", "\n\n"
                        );
                        let (a, b) = result.split_at(20);
                        chunk(&mut s, a);
                        chunk(&mut s, b);
                        let _ = write!(s, "0\r\n\r\n");
                    }
                    _ => {
                        let body = format!(
                            r#"{{"jsonrpc":"2.0","id":{},"result":{{}}}}"#,
                            head.id.unwrap_or("null".into())
                        );
                        respond(
                            &stream,
                            "200 OK",
                            "Content-Type: application/json\r\n",
                            &body,
                        );
                    }
                }
            }
        });
        (port, rx)
    }

    #[derive(Clone, Default)]
    struct Sink(Arc<Mutex<Vec<u8>>>);
    impl Write for Sink {
        fn write(&mut self, b: &[u8]) -> io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(b);
            Ok(b.len())
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    fn lines(sink: &Sink) -> Vec<String> {
        String::from_utf8(sink.0.lock().unwrap().clone())
            .unwrap()
            .lines()
            .map(str::to_string)
            .collect()
    }

    fn config(port: u16) -> Arc<Config> {
        Arc::new(Config {
            host: "127.0.0.1".into(),
            port,
            target: "/mcp".into(),
            token: Some("tok".into()),
            workspace: None,
            restart_grace: Duration::ZERO,
        })
    }

    #[test]
    fn a_request_waits_for_a_restarting_api() {
        // the API is down: its port refuses connections
        let port = TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        let restarted = thread::spawn(move || {
            thread::sleep(Duration::from_millis(300));
            let listener = TcpListener::bind(("127.0.0.1", port)).unwrap();
            let (stream, _) = listener.accept().unwrap();
            let seen = read_request(&stream);
            let body = r#"{"jsonrpc":"2.0","id":9,"result":{}}"#;
            respond(
                &stream,
                "200 OK",
                "Content-Type: application/json\r\n",
                body,
            );
            seen
        });
        let cfg = Config {
            restart_grace: Duration::from_secs(5),
            ..(*config(port)).clone()
        };
        let sink = Sink::default();
        let out: Output = Arc::new(Mutex::new(Box::new(sink.clone())));
        let ping = r#"{"jsonrpc":"2.0","id":9,"method":"ping"}"#;
        relay(
            &cfg,
            &Shared::default(),
            &out,
            ping,
            &scan_top_level(ping),
            false,
        );
        assert_eq!(restarted.join().unwrap().body, ping);
        assert_eq!(
            lines(&sink),
            vec![r#"{"jsonrpc":"2.0","id":9,"result":{}}"#.to_string()]
        );
        // with no grace a refused connection fails the request at once
        let started = Instant::now();
        relay(
            &config(port),
            &Shared::default(),
            &out,
            ping,
            &scan_top_level(ping),
            false,
        );
        assert!(lines(&sink).last().unwrap().contains("unreachable"));
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    #[test]
    fn relays_streams_progress_cancellation_and_replays_initialize() {
        let (port, seen) = fake_endpoint();
        let sink = Sink::default();
        let (tx, rx) = mpsc::channel::<String>();
        // stdin fed line by line, so the cancellation follows the slow call's start
        struct Feed(mpsc::Receiver<String>, Vec<u8>);
        impl Read for Feed {
            fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
                if self.1.is_empty() {
                    match self.0.recv() {
                        Ok(l) => self.1 = l.into_bytes(),
                        Err(_) => return Ok(0),
                    }
                }
                let n = buf.len().min(self.1.len());
                buf[..n].copy_from_slice(&self.1[..n]);
                self.1.drain(..n);
                Ok(n)
            }
        }
        let out: Output = Arc::new(Mutex::new(Box::new(sink.clone())));
        let relay = thread::spawn({
            let cfg = config(port);
            move || run(cfg, BufReader::new(Feed(rx, Vec::new())), out)
        });
        let next = |want: &str| loop {
            let s = seen
                .recv_timeout(Duration::from_secs(5))
                .expect("request not relayed");
            if s.body.contains(want) || s.method == want {
                return s;
            }
        };
        tx.send(r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}"#.to_string() + "\n")
            .unwrap();
        let init = next("initialize");
        assert_eq!(init.auth.as_deref(), Some("Bearer tok"));
        tx.send(
            r#"{"jsonrpc":"2.0","id":"slow-1","method":"tools/call","params":{"name":"slow"}}"#
                .to_string()
                + "\n",
        )
        .unwrap();
        assert_eq!(next("slow").session.as_deref(), Some("s1"));
        tx.send(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow-1"}}"#.to_string() + "\n").unwrap();
        next("notifications/cancelled");
        tx.send(
            r#"{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fast"}}"#
                .to_string()
                + "\n",
        )
        .unwrap();
        next("fast");
        // wait for the fast result before ending input
        for _ in 0..200 {
            if lines(&sink).iter().any(|l| l.contains(r#""id":2"#)) {
                break;
            }
            thread::sleep(Duration::from_millis(10));
        }
        drop(tx);
        relay.join().unwrap();
        assert_eq!(next("DELETE").session.as_deref(), Some("s1"));
        let out = lines(&sink);
        assert!(
            out[0].contains(r#""protocolVersion":"2025-06-18""#),
            "{out:?}"
        );
        assert_eq!(
            out.iter()
                .filter(|l| l.contains("notifications/progress"))
                .count(),
            2,
            "{out:?}"
        );
        assert!(
            out.iter()
                .any(|l| l == r#"{"jsonrpc":"2.0","id":2,"result":{"ok":true}}"#),
            "{out:?}"
        );
        assert!(
            !out.iter().any(|l| l.contains("slow-1")),
            "a cancelled call was answered: {out:?}"
        );
    }

    #[test]
    fn a_session_already_replaced_is_not_reinitialized_again() {
        // a dead endpoint: any replay would fail and return false
        let cfg = config(1);
        let session: Shared = Arc::default();
        {
            let mut s = session.lock().unwrap();
            s.init = Some(r#"{"jsonrpc":"2.0","id":0,"method":"initialize"}"#.into());
            s.id = Some("s2".into());
        }
        assert!(reinitialize(&cfg, &session, "s1"));
        assert_eq!(session.lock().unwrap().id.as_deref(), Some("s2"));
        assert!(!reinitialize(&cfg, &session, "s2"));
    }

    #[test]
    fn replays_initialize_when_the_session_is_forgotten() {
        let (port, seen) = fake_endpoint();
        let cfg = config(port);
        let sink = Sink::default();
        let out: Output = Arc::new(Mutex::new(Box::new(sink.clone())));
        let session: Shared = Arc::default();
        let init = r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}"#;
        session.lock().unwrap().init = Some(init.to_string());
        relay(&cfg, &session, &out, init, &scan_top_level(init), true);
        // the endpoint restarts: s1 is gone, the relay replays initialize and retries
        session.lock().unwrap().id = Some("stale".into());
        let ping = r#"{"jsonrpc":"2.0","id":7,"method":"ping"}"#;
        relay(&cfg, &session, &out, ping, &scan_top_level(ping), false);
        let got: Vec<Seen> = seen.try_iter().collect();
        assert_eq!(
            got.iter()
                .filter(|s| s.body.contains(r#""method":"initialize""#))
                .count(),
            2
        );
        assert_eq!(session.lock().unwrap().id.as_deref(), Some("s2"));
        assert!(
            lines(&sink).iter().any(|l| l.contains(r#""id":7"#)),
            "{:?}",
            lines(&sink)
        );
        // an unreachable endpoint answers the request with an error
        let dead = config(1);
        relay(&dead, &session, &out, ping, &scan_top_level(ping), false);
        assert!(lines(&sink).last().unwrap().contains("unreachable"));
    }
}
