//! `xmustard-core serve`: the resident worker.
//!
//! The Go API supervises one long-lived worker (`api-go/internal/rustcore/worker.go`)
//! instead of starting `xmustard-core` for every call. The worker answers JSON-RPC 2.0
//! requests over stdio and runs the subcommand table (`dispatch`) in-process. Symbol
//! graphs stay in memory per source identity ([`GraphSnapshots`]), so a warm query
//! neither spawns a process nor re-reads the graph JSON from disk.
//!
//! # Framing
//!
//! Every message, in both directions, is a header block followed by a JSON body:
//!
//! ```text
//! Content-Length: <body bytes>\r\n
//! Xmustard-Id: <request id>\r\n        (optional)
//! \r\n
//! <body>
//! ```
//!
//! `Xmustard-Id` repeats the JSON-RPC id outside the body. The Go reader uses it to
//! charge a response's bytes to that request's admission scope before allocating them.
//! A header line is at most [`MAX_HEADER_LINE`] bytes and a block at most
//! [`MAX_HEADER_LINES`] lines. A request body is at most [`MAX_REQUEST_BYTES`]; a
//! larger one is skipped and answered with an error, so the stream stays in sync. A
//! response body is at most [`MAX_FRAME_BYTES`] (64 MiB, the one-shot bridge's stdout
//! cap); a larger result is answered with [`OUTPUT_TOO_LARGE`].
//!
//! # Methods
//!
//! - `initialize` returns the protocol version, the pid, `max_inflight` and the
//!   resident subcommand names.
//! - `$/cancelRequest` (a notification, params `{"id": N}`): a queued request never
//!   runs; a running one finishes, but its output is dropped. Either way the request
//!   is answered with [`REQUEST_CANCELLED`], so the client knows when it has ended.
//! - `$/stats` returns counters.
//! - Any resident subcommand, with params `{"args": [...]}`. The subcommand's JSON
//!   output becomes `result` byte for byte: every result frame is written as
//!   `{"jsonrpc":"2.0","id":N,"result":<output>}` in that order, so the client can
//!   slice the result out without decoding it.
//!
//! # Lifecycle
//!
//! Requests run on a fixed pool of `max_inflight` threads (the Go side passes its
//! child limit). End of input on stdin ends the process at once, so a worker never
//! outlives its supervisor's pipe. After `trim_idle` without requests, the resident
//! graph snapshots are dropped. The supervisor owns idle exit: it closes stdin.

use std::collections::{HashMap, VecDeque};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant};

use serde_json::{Value, json};

use crate::dispatch::{self, Command, Output};
use crate::indexcache::SourceIdentity;
use crate::symbolgraph::SymbolGraph;

/// Wire protocol version reported by `initialize`.
pub const PROTOCOL_VERSION: u32 = 1;
/// Largest response body: the one-shot bridge's stdout cap.
pub const MAX_FRAME_BYTES: usize = 64 << 20;
/// Largest request body. Requests carry arguments (paths, queries), never payloads.
pub const MAX_REQUEST_BYTES: usize = 1 << 20;
/// Longest header line, excluding the line terminator.
pub const MAX_HEADER_LINE: usize = 1024;
/// Most header lines in one block, excluding the terminating blank line.
pub const MAX_HEADER_LINES: usize = 16;

pub const PARSE_ERROR: i64 = -32700;
pub const INVALID_REQUEST: i64 = -32600;
pub const METHOD_NOT_FOUND: i64 = -32601;
pub const INVALID_PARAMS: i64 = -32602;
pub const INTERNAL_ERROR: i64 = -32603;
/// The subcommand failed; `data.exit_code` is the CLI exit code it would have used.
pub const COMMAND_FAILED: i64 = -32001;
/// The result exceeded [`MAX_FRAME_BYTES`].
pub const OUTPUT_TOO_LARGE: i64 = -32002;
/// The request was cancelled before its output was sent.
pub const REQUEST_CANCELLED: i64 = -32800;

/// Worker settings, from `serve` arguments.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ServeConfig {
    /// Requests that run at once; later ones queue.
    pub max_inflight: usize,
    /// Drop resident snapshots after this long without requests (zero: never).
    pub trim_idle: Duration,
    /// Repository roots whose graph snapshot stays resident (zero: none).
    pub max_snapshots: usize,
}

impl Default for ServeConfig {
    fn default() -> Self {
        Self {
            max_inflight: 4,
            trim_idle: Duration::from_secs(60),
            max_snapshots: 1,
        }
    }
}

const SERVE_USAGE: &str =
    "xmustard-core serve [--max-inflight=N] [--trim-idle-ms=N] [--max-snapshots=N]";

impl ServeConfig {
    /// Parse `--max-inflight=N`, `--trim-idle-ms=N` and `--max-snapshots=N`.
    pub fn from_args(args: &[String]) -> Result<Self, String> {
        let mut cfg = Self::default();
        for arg in args {
            let (flag, value) = arg
                .split_once('=')
                .ok_or_else(|| format!("usage: {SERVE_USAGE}"))?;
            let n: u64 = value
                .parse()
                .map_err(|_| format!("serve: invalid value for {flag}: {value}"))?;
            match flag {
                "--max-inflight" => cfg.max_inflight = (n as usize).clamp(1, 64),
                "--trim-idle-ms" => cfg.trim_idle = Duration::from_millis(n),
                "--max-snapshots" => cfg.max_snapshots = (n as usize).min(16),
                _ => return Err(format!("usage: {SERVE_USAGE}")),
            }
        }
        Ok(cfg)
    }
}

// ---------------------------------------------------------------------------
// Resident graph snapshots
// ---------------------------------------------------------------------------

struct SnapshotEntry {
    root: String,
    trust: String,
    key: String,
    graph: Arc<SymbolGraph>,
}

/// Symbol graphs kept in memory inside `serve`, one per repository root. An entry is
/// the graph the shared disk cache holds for the root's current source identity, so
/// serving it is the disk cache without the per-call JSON parse. A root's entry is
/// replaced when its identity changes; the least recently used root is evicted past
/// `max_roots`; the idle trim drops them all.
pub struct GraphSnapshots {
    max_roots: usize,
    entries: Mutex<Vec<SnapshotEntry>>,
    hits: AtomicU64,
    stores: AtomicU64,
}

impl GraphSnapshots {
    pub fn new(max_roots: usize) -> Self {
        Self {
            max_roots,
            entries: Mutex::new(Vec::new()),
            hits: AtomicU64::new(0),
            stores: AtomicU64::new(0),
        }
    }

    fn lock(&self) -> MutexGuard<'_, Vec<SnapshotEntry>> {
        self.entries.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// The snapshot for exactly this identity (root, trust scope and key), if resident.
    pub fn get(&self, id: &SourceIdentity) -> Option<Arc<SymbolGraph>> {
        let trust = crate::indexcache::trust_scope();
        let mut entries = self.lock();
        let at = entries
            .iter()
            .position(|e| e.root == id.root && e.trust == trust && e.key == id.key)?;
        let entry = entries.remove(at);
        let graph = entry.graph.clone();
        entries.push(entry);
        self.hits.fetch_add(1, Ordering::Relaxed);
        Some(graph)
    }

    /// Keep `graph` as the root's snapshot for `id`, replacing any older one.
    pub fn put(&self, id: &SourceIdentity, graph: Arc<SymbolGraph>) {
        if self.max_roots == 0 {
            return;
        }
        let trust = crate::indexcache::trust_scope();
        let mut entries = self.lock();
        entries.retain(|e| !(e.root == id.root && e.trust == trust));
        entries.push(SnapshotEntry {
            root: id.root.clone(),
            trust,
            key: id.key.clone(),
            graph,
        });
        let excess = entries.len().saturating_sub(self.max_roots);
        entries.drain(..excess);
        self.stores.fetch_add(1, Ordering::Relaxed);
    }

    /// Drop every snapshot; returns how many were resident.
    pub fn clear(&self) -> usize {
        let mut entries = self.lock();
        let n = entries.len();
        entries.clear();
        n
    }

    pub fn len(&self) -> usize {
        self.lock().len()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    fn stats(&self) -> Value {
        json!({
            "resident": self.len(),
            "max_roots": self.max_roots,
            "hits": self.hits.load(Ordering::Relaxed),
            "stores": self.stores.load(Ordering::Relaxed),
        })
    }
}

static RESIDENT_GRAPHS: OnceLock<GraphSnapshots> = OnceLock::new();

/// The process's resident graph snapshots: set only by `serve`, so the one-shot CLI
/// keeps no graph between calls.
pub fn resident_graphs() -> Option<&'static GraphSnapshots> {
    RESIDENT_GRAPHS.get()
}

// ---------------------------------------------------------------------------
// Framing
// ---------------------------------------------------------------------------

/// Why a header block could not be read. Every variant except `Eof` leaves the stream
/// out of sync.
#[derive(Debug)]
pub enum FrameError {
    /// Clean end of input before a new message began.
    Eof,
    HeaderLineTooLong,
    TooManyHeaders,
    BadHeader(String),
    MissingLength,
    Io(io::Error),
}

impl std::fmt::Display for FrameError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            FrameError::Eof => write!(f, "end of input"),
            FrameError::HeaderLineTooLong => {
                write!(f, "header line longer than {MAX_HEADER_LINE} bytes")
            }
            FrameError::TooManyHeaders => write!(f, "more than {MAX_HEADER_LINES} header lines"),
            FrameError::BadHeader(line) => write!(f, "malformed header line: {line}"),
            FrameError::MissingLength => write!(f, "header block without Content-Length"),
            FrameError::Io(err) => write!(f, "read failed: {err}"),
        }
    }
}

/// A parsed header block.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FrameHeader {
    pub len: usize,
    pub id: Option<i64>,
}

/// Read one header line of at most `MAX_HEADER_LINE` bytes, without its terminator.
/// Returns `Ok(None)` at a clean end of input.
fn read_header_line<R: BufRead>(r: &mut R) -> Result<Option<Vec<u8>>, FrameError> {
    let mut line = Vec::new();
    // one byte of slack for the '\r' of "\r\n"; the '\n' ends the read.
    let limit = (MAX_HEADER_LINE + 2) as u64;
    let n = r
        .by_ref()
        .take(limit)
        .read_until(b'\n', &mut line)
        .map_err(FrameError::Io)?;
    if n == 0 {
        return Ok(None);
    }
    if line.last() != Some(&b'\n') {
        return Err(if n as u64 >= limit {
            FrameError::HeaderLineTooLong
        } else {
            FrameError::Io(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "end of input inside a header",
            ))
        });
    }
    line.pop();
    if line.last() == Some(&b'\r') {
        line.pop();
    }
    if line.len() > MAX_HEADER_LINE {
        return Err(FrameError::HeaderLineTooLong);
    }
    Ok(Some(line))
}

/// Read a header block. Header names are case-insensitive; unknown headers are
/// ignored. `Content-Length` is required.
pub fn read_header<R: BufRead>(r: &mut R) -> Result<FrameHeader, FrameError> {
    let mut len: Option<usize> = None;
    let mut id: Option<i64> = None;
    let mut lines = 0usize;
    loop {
        let Some(line) = read_header_line(r)? else {
            return Err(if lines == 0 {
                FrameError::Eof
            } else {
                FrameError::Io(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "end of input inside a header block",
                ))
            });
        };
        if line.is_empty() {
            if lines == 0 {
                // tolerate blank lines between messages.
                continue;
            }
            break;
        }
        lines += 1;
        if lines > MAX_HEADER_LINES {
            return Err(FrameError::TooManyHeaders);
        }
        let text = String::from_utf8_lossy(&line);
        let (name, value) = text
            .split_once(':')
            .ok_or_else(|| FrameError::BadHeader(text.chars().take(80).collect()))?;
        let value = value.trim();
        if name.eq_ignore_ascii_case("content-length") {
            len = Some(
                value
                    .parse()
                    .map_err(|_| FrameError::BadHeader(text.chars().take(80).collect()))?,
            );
        } else if name.eq_ignore_ascii_case("xmustard-id") {
            id = Some(
                value
                    .parse()
                    .map_err(|_| FrameError::BadHeader(text.chars().take(80).collect()))?,
            );
        }
    }
    Ok(FrameHeader {
        len: len.ok_or(FrameError::MissingLength)?,
        id,
    })
}

/// Write one message whose body is the concatenation of `parts`.
pub fn write_frame<W: Write + ?Sized>(
    w: &mut W,
    id: Option<i64>,
    parts: &[&[u8]],
) -> io::Result<()> {
    let len: usize = parts.iter().map(|p| p.len()).sum();
    let mut header = format!("Content-Length: {len}\r\n");
    if let Some(id) = id {
        header.push_str(&format!("Xmustard-Id: {id}\r\n"));
    }
    header.push_str("\r\n");
    w.write_all(header.as_bytes())?;
    for part in parts {
        w.write_all(part)?;
    }
    w.flush()
}

fn result_prefix(id: i64) -> String {
    format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":")
}

fn error_body(id: Option<i64>, code: i64, message: &str, data: Option<Value>) -> Vec<u8> {
    let mut error = json!({"code": code, "message": message});
    if let Some(data) = data {
        error["data"] = data;
    }
    serde_json::to_vec(&json!({"jsonrpc": "2.0", "id": id, "error": error}))
        .expect("error frame should serialize")
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

struct Job {
    id: i64,
    command: &'static Command,
    args: Vec<String>,
    cancelled: Arc<AtomicBool>,
}

#[derive(Default)]
struct Counters {
    requests: AtomicU64,
    completed: AtomicU64,
    failed: AtomicU64,
    cancelled: AtomicU64,
    panicked: AtomicU64,
    trims: AtomicU64,
}

struct Shared {
    writer: Mutex<Box<dyn Write + Send>>,
    queue: Mutex<VecDeque<Job>>,
    ready: Condvar,
    inflight: Mutex<HashMap<i64, Arc<AtomicBool>>>,
    last_activity: Mutex<Instant>,
    shutdown: AtomicBool,
    counters: Counters,
    snapshots: &'static GraphSnapshots,
    cfg: ServeConfig,
    started: Instant,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

impl Shared {
    fn send(&self, id: Option<i64>, parts: &[&[u8]]) {
        // A failed write means the supervisor is gone; stdin reaches EOF next.
        let _ = write_frame(&mut **lock(&self.writer), id, parts);
    }

    fn send_error(&self, id: Option<i64>, code: i64, message: &str, data: Option<Value>) {
        self.send(id, &[&error_body(id, code, message, data)]);
    }

    fn touch(&self) {
        *lock(&self.last_activity) = Instant::now();
    }

    fn stats(&self) -> Value {
        let c = &self.counters;
        json!({
            "pid": std::process::id(),
            "uptime_ms": self.started.elapsed().as_millis() as u64,
            "requests": c.requests.load(Ordering::Relaxed),
            "completed": c.completed.load(Ordering::Relaxed),
            "failed": c.failed.load(Ordering::Relaxed),
            "cancelled": c.cancelled.load(Ordering::Relaxed),
            "panicked": c.panicked.load(Ordering::Relaxed),
            "trims": c.trims.load(Ordering::Relaxed),
            "inflight": lock(&self.inflight).len(),
            "queued": lock(&self.queue).len(),
            "snapshots": self.snapshots.stats(),
        })
    }
}

/// Serve on stdin/stdout until stdin closes; returns the process exit code. The
/// protocol stream moves to a duplicate of stdout and fd 1 is pointed at stderr, so
/// stray output from library code cannot corrupt a frame.
pub fn run_stdio(table: &'static [Command], args: Vec<String>) -> i32 {
    let cfg = match ServeConfig::from_args(&args) {
        Ok(cfg) => cfg,
        Err(msg) => {
            eprintln!("{msg}");
            return 2;
        }
    };
    let writer: Box<dyn Write + Send> = match protocol_stdout() {
        Ok(w) => w,
        Err(err) => {
            eprintln!("serve: cannot set up the protocol stream: {err}");
            return 1;
        }
    };
    let reader = BufReader::with_capacity(64 << 10, io::stdin());
    serve(table, cfg, reader, writer)
}

#[cfg(unix)]
fn protocol_stdout() -> io::Result<Box<dyn Write + Send>> {
    use std::os::fd::AsFd;
    let proto = rustix::io::dup(io::stdout().as_fd())?;
    rustix::stdio::dup2_stdout(io::stderr().as_fd())?;
    Ok(Box::new(std::fs::File::from(proto)))
}

#[cfg(not(unix))]
fn protocol_stdout() -> io::Result<Box<dyn Write + Send>> {
    Ok(Box::new(io::stdout()))
}

/// Serve requests from `reader` until it ends, writing responses to `writer`.
/// Returns 0 at a clean end of input and 3 when the input stream is malformed.
pub fn serve<R: BufRead>(
    table: &'static [Command],
    cfg: ServeConfig,
    mut reader: R,
    writer: Box<dyn Write + Send>,
) -> i32 {
    let snapshots = RESIDENT_GRAPHS.get_or_init(|| GraphSnapshots::new(cfg.max_snapshots));
    let shared = Arc::new(Shared {
        writer: Mutex::new(writer),
        queue: Mutex::new(VecDeque::new()),
        ready: Condvar::new(),
        inflight: Mutex::new(HashMap::new()),
        last_activity: Mutex::new(Instant::now()),
        shutdown: AtomicBool::new(false),
        counters: Counters::default(),
        snapshots,
        cfg: cfg.clone(),
        started: Instant::now(),
    });
    for n in 0..cfg.max_inflight {
        let shared = shared.clone();
        // Tree-sitter walks recurse; match the main thread's stack, not the 2 MiB
        // default. Unused stack is address space, not resident memory.
        std::thread::Builder::new()
            .name(format!("xm-serve-{n}"))
            .stack_size(8 << 20)
            .spawn(move || pool_thread(&shared))
            .expect("serve: spawn a pool thread");
    }
    if !cfg.trim_idle.is_zero() {
        let shared = shared.clone();
        std::thread::Builder::new()
            .name("xm-serve-trim".into())
            .spawn(move || trim_thread(&shared))
            .expect("serve: spawn the trim thread");
    }
    let code = read_loop(table, &shared, &mut reader);
    shared.shutdown.store(true, Ordering::SeqCst);
    shared.ready.notify_all();
    code
}

fn read_loop<R: BufRead>(table: &'static [Command], shared: &Shared, reader: &mut R) -> i32 {
    loop {
        let header = match read_header(reader) {
            Ok(h) => h,
            Err(FrameError::Eof) => return 0,
            Err(err) => {
                shared.send_error(None, PARSE_ERROR, &format!("bad frame: {err}"), None);
                eprintln!("serve: bad frame, stopping: {err}");
                return 3;
            }
        };
        if header.len > MAX_REQUEST_BYTES {
            // Skip the body so the stream stays in sync, then refuse the request.
            match io::copy(
                &mut reader.by_ref().take(header.len as u64),
                &mut io::sink(),
            ) {
                Ok(n) if n == header.len as u64 => {}
                _ => return 0,
            }
            shared.send_error(
                header.id,
                INVALID_REQUEST,
                &format!("request body exceeds {MAX_REQUEST_BYTES} bytes"),
                None,
            );
            continue;
        }
        let mut body = vec![0u8; header.len];
        if reader.read_exact(&mut body).is_err() {
            return 0;
        }
        handle_message(table, shared, header.id, &body);
    }
}

fn handle_message(table: &'static [Command], shared: &Shared, header_id: Option<i64>, body: &[u8]) {
    let msg: Value = match serde_json::from_slice(body) {
        Ok(v) => v,
        Err(err) => {
            shared.send_error(
                header_id,
                PARSE_ERROR,
                &format!("invalid JSON: {err}"),
                None,
            );
            return;
        }
    };
    let id = match msg.get("id") {
        None | Some(Value::Null) => None,
        Some(v) => match v.as_i64() {
            Some(id) => Some(id),
            None => {
                shared.send_error(None, INVALID_REQUEST, "request id must be an integer", None);
                return;
            }
        },
    };
    let Some(method) = msg.get("method").and_then(Value::as_str) else {
        shared.send_error(id, INVALID_REQUEST, "missing method", None);
        return;
    };
    shared.touch();
    match method {
        "$/cancelRequest" => {
            if let Some(target) = msg.pointer("/params/id").and_then(Value::as_i64)
                && let Some(flag) = lock(&shared.inflight).get(&target)
            {
                flag.store(true, Ordering::SeqCst);
            }
        }
        "initialize" => {
            let methods: Vec<&str> = table
                .iter()
                .filter(|c| c.is_resident())
                .map(|c| c.name)
                .collect();
            // first arguments that keep a resident family one-shot, so the client
            // routes them without a round trip.
            let one_shot: serde_json::Map<String, Value> = table
                .iter()
                .filter_map(|c| match c.residency {
                    dispatch::Residency::ResidentExcept(subs) => {
                        Some((c.name.to_string(), json!(subs)))
                    }
                    _ => None,
                })
                .collect();
            let result = json!({
                "protocol": PROTOCOL_VERSION,
                "server": "xmustard-core",
                "version": env!("CARGO_PKG_VERSION"),
                "pid": std::process::id(),
                "max_inflight": shared.cfg.max_inflight,
                "max_frame_bytes": MAX_FRAME_BYTES,
                "methods": methods,
                "one_shot_subcommands": one_shot,
            });
            reply(shared, id, &result);
        }
        "$/stats" => reply(shared, id, &shared.stats()),
        name => {
            // Unknown notifications are ignored, as JSON-RPC requires.
            let Some(id) = id else { return };
            let Some(command) = dispatch::find(table, name) else {
                shared.send_error(
                    Some(id),
                    METHOD_NOT_FOUND,
                    &format!("unknown command: {name}"),
                    None,
                );
                return;
            };
            let args = match parse_args(&msg) {
                Ok(args) => args,
                Err(why) => {
                    shared.send_error(Some(id), INVALID_PARAMS, &why, None);
                    return;
                }
            };
            if !command.resident_for(&args) {
                shared.send_error(
                    Some(id),
                    METHOD_NOT_FOUND,
                    &format!("{name} runs one-shot only"),
                    Some(json!({"reason": "not_resident"})),
                );
                return;
            }
            let cancelled = Arc::new(AtomicBool::new(false));
            {
                let mut inflight = lock(&shared.inflight);
                if inflight.contains_key(&id) {
                    drop(inflight);
                    shared.send_error(Some(id), INVALID_REQUEST, "duplicate request id", None);
                    return;
                }
                inflight.insert(id, cancelled.clone());
            }
            shared.counters.requests.fetch_add(1, Ordering::Relaxed);
            lock(&shared.queue).push_back(Job {
                id,
                command,
                args,
                cancelled,
            });
            shared.ready.notify_one();
        }
    }
}

fn reply(shared: &Shared, id: Option<i64>, result: &Value) {
    // Notifications get no response.
    let Some(id) = id else { return };
    let body = serde_json::to_vec(result).expect("result should serialize");
    shared.send(Some(id), &[result_prefix(id).as_bytes(), &body, b"}"]);
}

fn parse_args(msg: &Value) -> Result<Vec<String>, String> {
    match msg.pointer("/params/args") {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(items)) => items
            .iter()
            .map(|v| {
                v.as_str()
                    .map(str::to_string)
                    .ok_or_else(|| "params.args must be an array of strings".to_string())
            })
            .collect(),
        Some(_) => Err("params.args must be an array of strings".to_string()),
    }
}

fn pool_thread(shared: &Shared) {
    loop {
        let job = {
            let mut queue = lock(&shared.queue);
            loop {
                if let Some(job) = queue.pop_front() {
                    break job;
                }
                if shared.shutdown.load(Ordering::SeqCst) {
                    return;
                }
                queue = shared.ready.wait(queue).unwrap_or_else(|e| e.into_inner());
            }
        };
        run_job(shared, job);
        shared.touch();
    }
}

fn run_job(shared: &Shared, job: Job) {
    let id = job.id;
    let outcome = if job.cancelled.load(Ordering::SeqCst) {
        None
    } else {
        let run = job.command.run;
        let args = job.args;
        Some(catch_unwind(AssertUnwindSafe(
            move || run(args.into_iter()),
        )))
    };
    lock(&shared.inflight).remove(&id);
    let c = &shared.counters;
    if outcome.is_none() || job.cancelled.load(Ordering::SeqCst) {
        c.cancelled.fetch_add(1, Ordering::Relaxed);
        shared.send_error(Some(id), REQUEST_CANCELLED, "request cancelled", None);
        return;
    }
    match outcome.expect("checked above") {
        Ok(Ok(Output::Json(body))) => {
            let prefix = result_prefix(id);
            if prefix.len() + body.len() + 1 > MAX_FRAME_BYTES {
                c.failed.fetch_add(1, Ordering::Relaxed);
                shared.send_error(
                    Some(id),
                    OUTPUT_TOO_LARGE,
                    &format!(
                        "{} output of {} bytes exceeds {MAX_FRAME_BYTES}",
                        job.command.name,
                        body.len()
                    ),
                    None,
                );
                return;
            }
            c.completed.fetch_add(1, Ordering::Relaxed);
            shared.send(Some(id), &[prefix.as_bytes(), body.as_bytes(), b"}"]);
        }
        Ok(Ok(Output::Text(_))) => {
            c.failed.fetch_add(1, Ordering::Relaxed);
            shared.send_error(
                Some(id),
                INTERNAL_ERROR,
                &format!(
                    "{} produced text, which serve does not return",
                    job.command.name
                ),
                None,
            );
        }
        Ok(Err(err)) => {
            c.failed.fetch_add(1, Ordering::Relaxed);
            shared.send_error(
                Some(id),
                COMMAND_FAILED,
                &err.message,
                Some(json!({"exit_code": err.code})),
            );
        }
        Err(panic) => {
            c.panicked.fetch_add(1, Ordering::Relaxed);
            let what = panic
                .downcast_ref::<&str>()
                .map(|s| s.to_string())
                .or_else(|| panic.downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "unknown panic".to_string());
            shared.send_error(
                Some(id),
                INTERNAL_ERROR,
                &format!("{} panicked: {what}", job.command.name),
                None,
            );
        }
    }
}

fn trim_thread(shared: &Shared) {
    let every = (shared.cfg.trim_idle / 4).clamp(Duration::from_millis(20), Duration::from_secs(5));
    while !shared.shutdown.load(Ordering::SeqCst) {
        std::thread::sleep(every);
        let idle = lock(&shared.last_activity).elapsed() >= shared.cfg.trim_idle;
        let busy = !lock(&shared.inflight).is_empty() || !lock(&shared.queue).is_empty();
        if idle && !busy && !shared.snapshots.is_empty() {
            shared.snapshots.clear();
            shared.counters.trims.fetch_add(1, Ordering::Relaxed);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::dispatch::{Args, CmdError, CmdResult, Residency, json as json_out};
    use std::io::{Cursor, PipeReader, PipeWriter};

    // ---- framing ----

    fn frame(id: Option<i64>, body: &str) -> Vec<u8> {
        let mut out = Vec::new();
        write_frame(&mut out, id, &[body.as_bytes()]).unwrap();
        out
    }

    #[test]
    fn frame_round_trip_carries_length_and_id() {
        let bytes = frame(Some(42), r#"{"a":1}"#);
        assert_eq!(
            bytes,
            b"Content-Length: 7\r\nXmustard-Id: 42\r\n\r\n{\"a\":1}".to_vec()
        );
        let mut r = Cursor::new(bytes);
        let h = read_header(&mut r).unwrap();
        assert_eq!(
            h,
            FrameHeader {
                len: 7,
                id: Some(42)
            }
        );
        let mut body = vec![0; h.len];
        r.read_exact(&mut body).unwrap();
        assert_eq!(body, br#"{"a":1}"#);
        assert!(matches!(read_header(&mut r), Err(FrameError::Eof)));
    }

    #[test]
    fn header_names_are_case_insensitive_and_unknown_headers_ignored() {
        let raw = b"content-type: x\r\nCONTENT-LENGTH: 3\n\r\n{} ";
        let h = read_header(&mut Cursor::new(raw.to_vec())).unwrap();
        assert_eq!(h, FrameHeader { len: 3, id: None });
    }

    #[test]
    fn header_line_cap_is_enforced() {
        let ok = format!(
            "X-Pad: {}\r\nContent-Length: 0\r\n\r\n",
            "a".repeat(MAX_HEADER_LINE - 7)
        );
        assert!(read_header(&mut Cursor::new(ok.into_bytes())).is_ok());
        let long = format!(
            "X-Pad: {}\r\nContent-Length: 0\r\n\r\n",
            "a".repeat(MAX_HEADER_LINE)
        );
        assert!(matches!(
            read_header(&mut Cursor::new(long.into_bytes())),
            Err(FrameError::HeaderLineTooLong)
        ));
        // an unterminated flood never buffers more than the cap.
        let flood = vec![b'a'; 4 << 20];
        assert!(matches!(
            read_header(&mut Cursor::new(flood)),
            Err(FrameError::HeaderLineTooLong)
        ));
    }

    #[test]
    fn header_count_cap_and_malformed_headers_are_errors() {
        let many = "X: 1\r\n".repeat(MAX_HEADER_LINES + 1) + "Content-Length: 0\r\n\r\n";
        assert!(matches!(
            read_header(&mut Cursor::new(many.into_bytes())),
            Err(FrameError::TooManyHeaders)
        ));
        for bad in [
            "no colon here\r\n\r\n",
            "Content-Length: -1\r\n\r\n",
            "Content-Length: 1x\r\n\r\n",
            "Content-Length: 1\r\nXmustard-Id: abc\r\n\r\n",
        ] {
            assert!(
                matches!(
                    read_header(&mut Cursor::new(bad.as_bytes().to_vec())),
                    Err(FrameError::BadHeader(_))
                ),
                "{bad:?}"
            );
        }
        assert!(matches!(
            read_header(&mut Cursor::new(b"X: 1\r\n\r\n".to_vec())),
            Err(FrameError::MissingLength)
        ));
        assert!(matches!(
            read_header(&mut Cursor::new(b"Content-Length: 1\r\n".to_vec())),
            Err(FrameError::Io(_))
        ));
    }

    // ---- server ----

    fn echo(args: Args) -> CmdResult {
        json_out(&args.collect::<Vec<_>>())
    }
    fn fail(_: Args) -> CmdResult {
        Err(CmdError::new(4, "not found: thing"))
    }
    fn boom(_: Args) -> CmdResult {
        panic!("boom")
    }
    fn text(_: Args) -> CmdResult {
        Ok(Output::Text("# md".into()))
    }
    fn huge(_: Args) -> CmdResult {
        Ok(Output::Json(format!("\"{}\"", "a".repeat(MAX_FRAME_BYTES))))
    }
    /// Blocks until the gate file named by its argument exists.
    fn wait_for(mut args: Args) -> CmdResult {
        let gate = args.next().unwrap_or_default();
        let deadline = Instant::now() + Duration::from_secs(10);
        while !std::path::Path::new(&gate).exists() && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(5));
        }
        json_out(&"released")
    }

    const TABLE: &[Command] = &[
        Command {
            name: "echo",
            residency: Residency::Resident,
            run: echo,
        },
        Command {
            name: "fail",
            residency: Residency::Resident,
            run: fail,
        },
        Command {
            name: "boom",
            residency: Residency::Resident,
            run: boom,
        },
        Command {
            name: "text",
            residency: Residency::Resident,
            run: text,
        },
        Command {
            name: "huge",
            residency: Residency::Resident,
            run: huge,
        },
        Command {
            name: "wait",
            residency: Residency::Resident,
            run: wait_for,
        },
        Command {
            name: "oneshot",
            residency: Residency::OneShot,
            run: echo,
        },
        Command {
            name: "family",
            residency: Residency::ResidentExcept(&["spawn"]),
            run: echo,
        },
    ];

    struct Harness {
        to_server: Option<PipeWriter>,
        from_server: BufReader<PipeReader>,
        server: Option<std::thread::JoinHandle<i32>>,
    }

    impl Harness {
        fn start(cfg: ServeConfig) -> Self {
            let (req_r, req_w) = std::io::pipe().unwrap();
            let (resp_r, resp_w) = std::io::pipe().unwrap();
            let server = std::thread::spawn(move || {
                serve(TABLE, cfg, BufReader::new(req_r), Box::new(resp_w))
            });
            Harness {
                to_server: Some(req_w),
                from_server: BufReader::new(resp_r),
                server: Some(server),
            }
        }

        fn send(&mut self, id: Option<i64>, body: &Value) {
            let w = self.to_server.as_mut().unwrap();
            write_frame(w, id, &[serde_json::to_string(body).unwrap().as_bytes()]).unwrap();
        }

        fn request(&mut self, id: i64, method: &str, args: &[&str]) {
            self.send(
                Some(id),
                &json!({"jsonrpc": "2.0", "id": id, "method": method, "params": {"args": args}}),
            );
        }

        /// Next response: (header id, raw body, parsed body).
        fn recv(&mut self) -> (Option<i64>, Vec<u8>, Value) {
            let h = read_header(&mut self.from_server).unwrap();
            let mut body = vec![0; h.len];
            self.from_server.read_exact(&mut body).unwrap();
            let v = serde_json::from_slice(&body).unwrap();
            (h.id, body, v)
        }

        fn close(mut self) -> i32 {
            drop(self.to_server.take());
            self.server.take().unwrap().join().unwrap()
        }
    }

    #[test]
    fn initialize_lists_resident_methods_only() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(1, "initialize", &[]);
        let (hid, _, v) = h.recv();
        assert_eq!(hid, Some(1));
        assert_eq!(v["result"]["protocol"], PROTOCOL_VERSION);
        assert_eq!(v["result"]["max_inflight"], 4);
        let methods = v["result"]["methods"].as_array().unwrap();
        assert!(methods.contains(&json!("echo")));
        assert!(methods.contains(&json!("family")));
        assert!(!methods.contains(&json!("oneshot")));
        assert_eq!(
            v["result"]["one_shot_subcommands"],
            json!({"family": ["spawn"]})
        );
        assert_eq!(h.close(), 0);
    }

    #[test]
    fn result_is_embedded_verbatim_in_canonical_order() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(7, "echo", &["a b", "-x"]);
        let (hid, raw, _) = h.recv();
        assert_eq!(hid, Some(7));
        assert_eq!(
            String::from_utf8(raw).unwrap(),
            r#"{"jsonrpc":"2.0","id":7,"result":["a b","-x"]}"#
        );
        h.close();
    }

    #[test]
    fn command_errors_carry_the_cli_exit_code() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(2, "fail", &[]);
        let (_, _, v) = h.recv();
        assert_eq!(v["id"], 2);
        assert_eq!(v["error"]["code"], COMMAND_FAILED);
        assert_eq!(v["error"]["message"], "not found: thing");
        assert_eq!(v["error"]["data"]["exit_code"], 4);
        h.close();
    }

    #[test]
    fn a_panicking_command_is_an_error_and_the_server_survives() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(3, "boom", &[]);
        let (_, _, v) = h.recv();
        assert_eq!(v["error"]["code"], INTERNAL_ERROR);
        assert!(v["error"]["message"].as_str().unwrap().contains("boom"));
        h.request(4, "echo", &["still up"]);
        let (_, _, v) = h.recv();
        assert_eq!(v["result"], json!(["still up"]));
        h.close();
    }

    #[test]
    fn unknown_one_shot_and_bad_requests_are_refused() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(1, "nope", &[]);
        assert_eq!(h.recv().2["error"]["code"], METHOD_NOT_FOUND);
        h.request(2, "oneshot", &[]);
        let v = h.recv().2;
        assert_eq!(v["error"]["code"], METHOD_NOT_FOUND);
        assert_eq!(v["error"]["data"]["reason"], "not_resident");
        h.request(3, "family", &["spawn"]);
        assert_eq!(h.recv().2["error"]["data"]["reason"], "not_resident");
        h.request(4, "family", &["read"]);
        assert_eq!(h.recv().2["result"], json!(["read"]));
        h.send(
            Some(5),
            &json!({"jsonrpc": "2.0", "id": 5, "method": "echo", "params": {"args": [1]}}),
        );
        assert_eq!(h.recv().2["error"]["code"], INVALID_PARAMS);
        h.send(
            Some(6),
            &json!({"jsonrpc": "2.0", "id": "six", "method": "echo"}),
        );
        assert_eq!(h.recv().2["error"]["code"], INVALID_REQUEST);
        h.request(7, "text", &[]);
        assert_eq!(h.recv().2["error"]["code"], INTERNAL_ERROR);
        // malformed JSON body, correctly framed: the stream stays usable.
        write_frame(h.to_server.as_mut().unwrap(), Some(8), &[b"{nope"]).unwrap();
        let (hid, _, v) = h.recv();
        assert_eq!(
            (hid, v["error"]["code"].as_i64()),
            (Some(8), Some(PARSE_ERROR))
        );
        h.request(9, "echo", &["ok"]);
        assert_eq!(h.recv().2["result"], json!(["ok"]));
        assert_eq!(h.close(), 0);
    }

    #[test]
    fn oversized_request_is_skipped_and_refused_in_sync() {
        let mut h = Harness::start(ServeConfig::default());
        let pad = "a".repeat(MAX_REQUEST_BYTES);
        h.send(
            Some(1),
            &json!({"jsonrpc": "2.0", "id": 1, "method": "echo", "params": {"args": [pad]}}),
        );
        let (hid, _, v) = h.recv();
        assert_eq!(hid, Some(1));
        assert_eq!(v["error"]["code"], INVALID_REQUEST);
        h.request(2, "echo", &["after"]);
        assert_eq!(h.recv().2["result"], json!(["after"]));
        h.close();
    }

    #[test]
    fn output_over_the_frame_limit_is_refused() {
        let mut h = Harness::start(ServeConfig::default());
        h.request(1, "huge", &[]);
        let (_, raw, v) = h.recv();
        assert!(raw.len() < 1024);
        assert_eq!(v["error"]["code"], OUTPUT_TOO_LARGE);
        h.close();
    }

    #[test]
    fn a_bad_header_stops_the_server_with_a_parse_error() {
        let mut h = Harness::start(ServeConfig::default());
        // The server stops reading after the first capped header line, so the rest of
        // this write fails with a broken pipe; only the response matters.
        let _ = h
            .to_server
            .as_mut()
            .unwrap()
            .write_all(&vec![b'z'; 4 << 20]);
        let (hid, _, v) = h.recv();
        assert_eq!(hid, None);
        assert_eq!(v["error"]["code"], PARSE_ERROR);
        assert_eq!(h.server.take().unwrap().join().unwrap(), 3);
    }

    #[test]
    fn cancel_drops_a_running_request_and_answers_cancelled() {
        let dir = tempfile::tempdir().unwrap();
        let gate = dir.path().join("gate");
        let mut h = Harness::start(ServeConfig::default());
        h.request(1, "wait", &[gate.to_str().unwrap()]);
        // make sure it is running before cancelling.
        std::thread::sleep(Duration::from_millis(50));
        h.send(
            None,
            &json!({"jsonrpc": "2.0", "method": "$/cancelRequest", "params": {"id": 1}}),
        );
        std::thread::sleep(Duration::from_millis(20));
        std::fs::write(&gate, b"").unwrap();
        let (hid, _, v) = h.recv();
        assert_eq!(hid, Some(1));
        assert_eq!(v["error"]["code"], REQUEST_CANCELLED);
        h.request(2, "$/stats", &[]);
        let stats = h.recv().2;
        assert_eq!(stats["result"]["cancelled"], 1);
        h.close();
    }

    #[test]
    fn a_queued_request_cancelled_before_it_starts_never_runs() {
        let dir = tempfile::tempdir().unwrap();
        let gate = dir.path().join("gate");
        let cfg = ServeConfig {
            max_inflight: 1,
            ..ServeConfig::default()
        };
        let mut h = Harness::start(cfg);
        h.request(1, "wait", &[gate.to_str().unwrap()]);
        h.request(2, "echo", &["queued"]);
        h.send(
            None,
            &json!({"jsonrpc": "2.0", "method": "$/cancelRequest", "params": {"id": 2}}),
        );
        std::thread::sleep(Duration::from_millis(30));
        std::fs::write(&gate, b"").unwrap();
        let mut seen = HashMap::new();
        for _ in 0..2 {
            let (hid, _, v) = h.recv();
            seen.insert(hid.unwrap(), v);
        }
        assert_eq!(seen[&1]["result"], "released");
        assert_eq!(seen[&2]["error"]["code"], REQUEST_CANCELLED);
        h.close();
    }

    #[test]
    fn requests_run_concurrently_up_to_max_inflight() {
        let dir = tempfile::tempdir().unwrap();
        let gate = dir.path().join("gate");
        let mut h = Harness::start(ServeConfig {
            max_inflight: 2,
            ..ServeConfig::default()
        });
        for id in 1..=3 {
            h.request(id, "wait", &[gate.to_str().unwrap()]);
        }
        std::thread::sleep(Duration::from_millis(50));
        h.request(9, "$/stats", &[]);
        let stats = h.recv().2;
        // two run on the pool, one waits in the queue.
        assert_eq!(stats["result"]["queued"], 1);
        assert_eq!(stats["result"]["inflight"], 3);
        std::fs::write(&gate, b"").unwrap();
        for _ in 0..3 {
            assert_eq!(h.recv().2["result"], "released");
        }
        h.close();
    }

    #[test]
    fn duplicate_inflight_ids_are_refused() {
        let dir = tempfile::tempdir().unwrap();
        let gate = dir.path().join("gate");
        let mut h = Harness::start(ServeConfig::default());
        h.request(1, "wait", &[gate.to_str().unwrap()]);
        h.request(1, "echo", &["dup"]);
        let v = h.recv().2;
        assert_eq!(v["error"]["code"], INVALID_REQUEST);
        std::fs::write(&gate, b"").unwrap();
        assert_eq!(h.recv().2["result"], "released");
        h.close();
    }

    #[test]
    fn serve_config_parses_flags_and_rejects_unknown_ones() {
        let args = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let cfg = ServeConfig::from_args(&args(&[
            "--max-inflight=3",
            "--trim-idle-ms=250",
            "--max-snapshots=2",
        ]))
        .unwrap();
        assert_eq!(
            cfg,
            ServeConfig {
                max_inflight: 3,
                trim_idle: Duration::from_millis(250),
                max_snapshots: 2
            }
        );
        assert_eq!(
            ServeConfig::from_args(&args(&["--max-inflight=0"]))
                .unwrap()
                .max_inflight,
            1
        );
        assert!(ServeConfig::from_args(&args(&["--bogus=1"])).is_err());
        assert!(ServeConfig::from_args(&args(&["--max-inflight"])).is_err());
        assert!(ServeConfig::from_args(&args(&["--max-inflight=x"])).is_err());
    }

    // ---- snapshots ----

    fn identity(root: &str, key: &str) -> SourceIdentity {
        let mut id = crate::indexcache::source_identity(std::path::Path::new("/nonexistent-xm"));
        id.root = root.into();
        id.key = key.into();
        id
    }

    fn graph(tag: &str) -> Arc<SymbolGraph> {
        let mut g: SymbolGraph = serde_json::from_value(
            json!({"workspace_id": tag, "file_count": 0, "symbol_count": 0,
                "edge_count": 0, "files": [], "symbols": [], "edges": [], "generated_at": ""}),
        )
        .unwrap();
        g.workspace_id = tag.into();
        Arc::new(g)
    }

    #[test]
    fn snapshots_are_per_root_identity_and_lru_bounded() {
        let s = GraphSnapshots::new(2);
        assert!(s.get(&identity("/a", "k1")).is_none());
        s.put(&identity("/a", "k1"), graph("a1"));
        assert_eq!(s.get(&identity("/a", "k1")).unwrap().workspace_id, "a1");
        // a new identity for the same root replaces, never serves, the old one.
        s.put(&identity("/a", "k2"), graph("a2"));
        assert!(s.get(&identity("/a", "k1")).is_none());
        assert_eq!(s.len(), 1);
        s.put(&identity("/b", "k"), graph("b"));
        // touch /a so /b is least recently used.
        assert!(s.get(&identity("/a", "k2")).is_some());
        s.put(&identity("/c", "k"), graph("c"));
        assert_eq!(s.len(), 2);
        assert!(s.get(&identity("/b", "k")).is_none());
        assert!(s.get(&identity("/a", "k2")).is_some());
        assert_eq!(s.clear(), 2);
        assert!(s.is_empty());
        let off = GraphSnapshots::new(0);
        off.put(&identity("/a", "k"), graph("a"));
        assert!(off.is_empty());
    }
}
