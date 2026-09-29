//! `xmustard-core run -- <argv>`: the command-wrapper reducer (WS-41, PAR-CTX-10).
//!
//! The RTK pattern. A client that cannot replace a shell result rewrites the command
//! to `xmustard-core run -- <command>` (PreToolUse `updatedInput`); the wrapper runs it
//! and prints a projection plus a recovery handle instead of the raw output.
//!
//! * The child's stdout and stderr share ONE pipe, so their interleaving is the
//!   kernel's arrival order, exactly as a terminal would show it.
//! * The wrapper reads the pipe in 64 KiB chunks into an unlinked 0600 spool file
//!   (capped) and a rolling tail, so its memory is O(chunk + tail) whatever the child
//!   prints: 100 MB of output never sits in memory.
//! * The spool is POSTed to the API's `evidence/capture` route. Redaction, reduction,
//!   retention and quota are the server's: the wrapper never reduces or stores output
//!   itself, and every byte a projection or a retained original carries went through the
//!   API's streaming redactor, which refuses the capture when it fails.
//! * It fails open. No workspace, an unreachable API, any refusal (redaction failed,
//!   quota full, too large) or output past the spool cap gives a bounded passthrough:
//!   the native output (the whole of it when it is small, else its tail) plus a notice
//!   line. The command's result is never lost to xMustard being down.
//! * It ends the way the child did: the same exit code, or the same signal.

use std::collections::VecDeque;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::net::{IpAddr, TcpStream, ToSocketAddrs};
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::process::ExitStatusExt;
use std::process::{Command, ExitStatus, Stdio};
use std::time::Duration;

use percent_encoding::{utf8_percent_encode, NON_ALPHANUMERIC};
use sha2::{Digest, Sha256};

use crate::dispatch::{Args, ChildExit, CmdError, CmdResult, Output};

/// The largest spool: the API's own capture cap (`DefaultMaxOriginal`). Past it the
/// output cannot be retained whole, so it is passed through.
pub const SPOOL_CAP: u64 = 16 << 20;
/// The rolling tail kept in memory, and the most a passthrough of a big output prints.
pub const TAIL_CAP: usize = 64 << 10;
/// Output up to this size is passed through whole when the capture fails.
pub const PASSTHROUGH_WHOLE: u64 = 256 << 10;
const CHUNK: usize = 64 << 10;
const REPLY_CAP: u64 = 4 << 20;
const DEFAULT_API: &str = "http://127.0.0.1:8042";
const USAGE: &str = "xmustard-core run [--workspace <id>] [--api <url>] -- <program> [args...]";

/// Where and how the wrapper reaches the API, from flags and the environment.
struct Target {
    workspace: Option<String>,
    api: String,
    token: Option<String>,
}

/// Sizes the wrapper works within (overridable by tests through `run_with`).
#[derive(Clone, Copy)]
pub struct Limits {
    pub spool_cap: u64,
    pub tail_cap: usize,
    pub whole: u64,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            spool_cap: SPOOL_CAP,
            tail_cap: TAIL_CAP,
            whole: PASSTHROUGH_WHOLE,
        }
    }
}

/// Table entry: run with the process environment and the default limits.
pub fn run(args: Args) -> CmdResult {
    run_with(args, &|k| std::env::var(k).ok(), Limits::default())
}

/// `run` with an injectable environment and limits.
pub fn run_with(args: Args, env: &dyn Fn(&str) -> Option<String>, limits: Limits) -> CmdResult {
    let (target, argv) = parse(args, env)?;
    let mut child = Command::new(&argv[0]);
    child.args(&argv[1..]);
    let (read_end, write_end) = rustix::pipe::pipe().map_err(|e| io_failed("pipe")(e.into()))?;
    let dup = write_end.try_clone().map_err(io_failed("pipe"))?;
    child
        .stdout(Stdio::from(write_end))
        .stderr(Stdio::from(dup));
    let mut spawned = match child.spawn() {
        Ok(c) => c,
        // the shell's own answers: 127 not found, 126 not executable
        Err(e) => {
            let code = if e.kind() == io::ErrorKind::NotFound {
                127
            } else {
                126
            };
            let text = format!("xmustard run: {}: {e}\n", argv[0]);
            return Ok(Output::Exit(text, ChildExit::Code(code)));
        }
    };
    drop(child); // the parent's copies of the write end must close for EOF
    let drained = drain(File::from(read_end), limits);
    let status = spawned.wait().map_err(io_failed("wait"))?;
    let exit = exit_of(status);
    let mut drained = drained.map_err(io_failed("read child output"))?;
    let text = present(&target, &argv, exit, &mut drained, limits);
    Ok(Output::Exit(text, exit))
}

fn parse(
    mut args: Args,
    env: &dyn Fn(&str) -> Option<String>,
) -> Result<(Target, Vec<String>), CmdError> {
    let mut target = Target {
        workspace: nonblank(env("XMUSTARD_WORKSPACE_ID")),
        api: nonblank(env("XMUSTARD_API_BASE")).unwrap_or_else(|| DEFAULT_API.to_string()),
        token: nonblank(env("XMUSTARD_API_TOKEN")),
    };
    loop {
        match args.next().as_deref() {
            Some("--workspace") => target.workspace = args.next(),
            Some("--api") => target.api = args.next().ok_or_else(|| CmdError::usage(USAGE))?,
            Some("--") => break,
            _ => return Err(CmdError::usage(USAGE)),
        }
    }
    let argv: Vec<String> = args.collect();
    if argv.is_empty() {
        return Err(CmdError::usage(USAGE));
    }
    Ok((target, argv))
}

fn nonblank(v: Option<String>) -> Option<String> {
    v.map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
}

fn io_failed(what: &'static str) -> impl Fn(io::Error) -> CmdError {
    move |e| CmdError::failed(format!("run: {what}: {e}"))
}

/// What the child printed: the spool (`None` once dropped, with `lost` saying why),
/// the byte total and the last `tail_cap` bytes.
struct Drained {
    spool: Option<File>,
    lost: String,
    total: u64,
    tail: Vec<u8>,
}

/// Reads the child's output to EOF in fixed chunks. Memory is one chunk plus the tail.
fn drain(mut pipe: File, limits: Limits) -> io::Result<Drained> {
    let mut spool = Some(private_spool()?);
    let mut lost = String::new();
    let (mut total, mut buf) = (0u64, vec![0u8; CHUNK]);
    let mut tail: VecDeque<u8> = VecDeque::with_capacity(limits.tail_cap + CHUNK);
    loop {
        let n = match pipe.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => n,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        };
        total += n as u64;
        tail.extend(&buf[..n]);
        tail.drain(..tail.len().saturating_sub(limits.tail_cap));
        if total > limits.spool_cap {
            // the original can no longer be kept whole; keep draining
            spool = None;
            lost = format!(
                "its output exceeds the {}-byte capture limit",
                limits.spool_cap
            );
        } else if spool
            .as_mut()
            .is_some_and(|f| f.write_all(&buf[..n]).is_err())
        {
            // a full disk must not stall the child: degrade to the tail
            spool = None;
            lost = "the output spool could not be written".into();
        }
    }
    Ok(Drained {
        spool,
        lost,
        total,
        tail: tail.into_iter().collect(),
    })
}

/// An owner-only file that is unlinked at once, so it never outlives the process and
/// no other user can open it by name.
fn private_spool() -> io::Result<File> {
    let dir = std::env::temp_dir();
    let unique = format!(
        ".xmustard-run-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos())
    );
    let path = dir.join(unique);
    let f = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)?;
    fs::remove_file(&path)?;
    Ok(f)
}

fn exit_of(status: ExitStatus) -> ChildExit {
    status.signal().map_or_else(
        || ChildExit::Code(status.code().unwrap_or(1)),
        ChildExit::Signal,
    )
}

/// The process exit code that repeats `exit`. A signal ends this process with the same
/// signal (default disposition), so a shell's WIFSIGNALED sees what the child did;
/// when that cannot end it, 128 + n, the shell convention, is returned.
pub fn end_like(exit: ChildExit) -> i32 {
    match exit {
        ChildExit::Code(c) => c,
        ChildExit::Signal(n) => {
            if let Some(sig) = rustix::process::Signal::from_named_raw(n) {
                let _ = rustix::process::kill_process(rustix::process::getpid(), sig);
            }
            128 + n
        }
    }
}

/// The text to print: the API's projection and recovery footer, or on any failure a
/// bounded passthrough with a notice.
fn present(
    target: &Target,
    argv: &[String],
    exit: ChildExit,
    out: &mut Drained,
    limits: Limits,
) -> String {
    if out.total == 0 {
        return String::new();
    }
    let attempt = match out.spool.as_mut() {
        None => Err(format!("{} ({} bytes)", out.lost, out.total)),
        Some(spool) => capture(target, argv, exit, spool, out.total),
    };
    attempt.unwrap_or_else(|why| passthrough(out, limits, &why))
}

/// Native output, bounded, then the notice saying why it is not a projection.
fn passthrough(out: &mut Drained, limits: Limits, why: &str) -> String {
    let whole = match out.spool.as_mut() {
        Some(f) if out.total <= limits.whole => {
            let mut all = Vec::with_capacity(out.total as usize);
            f.seek(SeekFrom::Start(0))
                .and_then(|_| f.read_to_end(&mut all))
                .ok()
                .map(|_| all)
        }
        _ => None,
    };
    let (body, note) = match whole {
        Some(all) => (all, format!("[xmustard: passed through unreduced: {why}]")),
        None => (
            out.tail.clone(),
            format!(
                "[xmustard: showing the last {} of {} bytes: {why}]",
                out.tail.len(),
                out.total
            ),
        ),
    };
    let mut text = String::from_utf8_lossy(&body).into_owned();
    if !text.ends_with('\n') {
        text.push('\n');
    }
    text.push_str(&note);
    text.push('\n');
    text
}

/// Posts the spool to the capture route and returns the projection and footer.
fn capture(
    target: &Target,
    argv: &[String],
    exit: ChildExit,
    spool: &mut File,
    total: u64,
) -> Result<String, String> {
    let ws = target
        .workspace
        .as_deref()
        .ok_or("no workspace (XMUSTARD_WORKSPACE_ID)")?;
    let (host, port) = split_api(&target.api)?;
    // the upload is plain http and unredacted until the server redacts it: only loopback
    if !is_loopback(&host) {
        return Err("refusing to upload output over plain http to a non-loopback host".into());
    }
    let exit_code = match exit {
        ChildExit::Code(c) => c,
        ChildExit::Signal(n) => 128 + n,
    };
    let query = format!(
        "format=raw&tool=Bash&command={}&exit_code={exit_code}&args_digest={}&content_type={}",
        enc(&command_hint(argv)),
        digest(argv),
        enc("text/plain; charset=utf-8"),
    );
    let mut head = format!(
        "POST /api/workspaces/{}/evidence/capture?{query} HTTP/1.0\r\nHost: {host}:{port}\r\n\
         Content-Type: text/plain\r\nContent-Length: {total}\r\n",
        enc(ws)
    );
    if let Some(token) = &target.token {
        head.push_str(&format!("Authorization: Bearer {token}\r\n"));
    }
    head.push_str("\r\n");
    let addr = (host.as_str(), port)
        .to_socket_addrs()
        .ok()
        .and_then(|mut a| a.next())
        .ok_or("the API address does not resolve")?;
    let mut conn = TcpStream::connect_timeout(&addr, Duration::from_secs(2))
        .map_err(|e| format!("the API is unreachable ({e})"))?;
    let _ = conn.set_read_timeout(Some(Duration::from_secs(30)));
    let _ = conn.set_write_timeout(Some(Duration::from_secs(30)));
    spool.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
    conn.write_all(head.as_bytes())
        .and_then(|_| io::copy(&mut *spool, &mut conn).map(|_| ()))
        .map_err(|e| format!("the API refused the upload ({e})"))?;
    let mut reply = Vec::new();
    conn.take(REPLY_CAP)
        .read_to_end(&mut reply)
        .map_err(|e| format!("the API reply failed ({e})"))?;
    project(&reply)
}

/// The projection text of a 200 reply; any other status is the refusal reason.
fn project(reply: &[u8]) -> Result<String, String> {
    let split = reply
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .ok_or("the API reply was malformed")?;
    let head = String::from_utf8_lossy(&reply[..split]);
    let status: u16 = head
        .split_whitespace()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .ok_or("the API reply had no status")?;
    let body: serde_json::Value = serde_json::from_slice(&reply[split + 4..])
        .map_err(|_| format!("the API answered {status}"))?;
    if status != 200 {
        let reason = body["reason"].as_str().unwrap_or("refused");
        return Err(format!("the API answered {status} {reason}"));
    }
    let field = |k: &str| body[k].as_str().unwrap_or("").to_string();
    let mut text = field("projection");
    if !text.is_empty() && !text.ends_with('\n') {
        text.push('\n');
    }
    let footer = field("footer");
    if !footer.is_empty() {
        text.push_str(&footer);
        text.push('\n');
    }
    Ok(text)
}

/// Splits `http://host[:port]` (https is refused: the wrapper speaks plain loopback).
fn split_api(api: &str) -> Result<(String, u16), String> {
    let rest = api
        .strip_prefix("http://")
        .ok_or("the API base must be an http:// URL")?;
    let authority = rest.split('/').next().unwrap_or("");
    let (host, port) = match authority.rsplit_once(':') {
        Some((h, p)) if !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()) => (
            h.trim_matches(['[', ']']),
            p.parse().map_err(|_| "the API port is invalid")?,
        ),
        _ => (authority.trim_matches(['[', ']']), 80),
    };
    if host.is_empty() {
        return Err("the API base names no host".into());
    }
    Ok((host.to_string(), port))
}

fn is_loopback(host: &str) -> bool {
    host == "localhost" || host.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

fn enc(s: &str) -> String {
    utf8_percent_encode(s, NON_ALPHANUMERIC).to_string()
}

/// What the server may learn of the command line to pick a reducer: the program's name
/// and, when it is a plain word, its first argument (`git diff`). The rest of the line
/// can hold tokens or secrets, so it never leaves the process.
fn command_hint(argv: &[String]) -> String {
    let name = argv[0].rsplit('/').next().unwrap_or(&argv[0]);
    let plain = |s: &String| {
        !s.is_empty()
            && s.bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
            && !s.starts_with('-')
    };
    match argv.get(1).filter(|a| plain(a)) {
        Some(sub) => format!("{name} {sub}"),
        None => name.to_string(),
    }
}

fn digest(argv: &[String]) -> String {
    let mut h = Sha256::new();
    for a in argv {
        h.update((a.len() as u64).to_le_bytes());
        h.update(a.as_bytes());
    }
    h.finalize().iter().map(|b| format!("{b:02x}")).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn command_hint_keeps_program_and_plain_subcommand_only() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(
            command_hint(&a(&["/usr/bin/git", "diff", "--stat"])),
            "git diff"
        );
        assert_eq!(
            command_hint(&a(&["curl", "-H", "Authorization: x"])),
            "curl"
        );
        assert_eq!(command_hint(&a(&["curl", "https://h/?k=secret"])), "curl");
    }

    #[test]
    fn split_api_reads_host_and_port() {
        assert_eq!(
            split_api("http://127.0.0.1:8042").unwrap(),
            ("127.0.0.1".into(), 8042)
        );
        assert_eq!(split_api("http://[::1]:9/x").unwrap(), ("::1".into(), 9));
        assert!(split_api("https://x").is_err());
    }

    #[test]
    fn project_reads_status_and_reason() {
        let ok = b"HTTP/1.0 200 OK\r\n\r\n{\"projection\":\"p\",\"footer\":\"f\"}";
        assert_eq!(project(ok).unwrap(), "p\nf\n");
        let no = b"HTTP/1.0 503 X\r\n\r\n{\"reason\":\"redaction_failed\"}";
        assert!(project(no).unwrap_err().contains("redaction_failed"));
    }
}
