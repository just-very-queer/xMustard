#![forbid(unsafe_code)]
//! xmustard-cursor: the Cursor command hook client (WS-40c-b; PAR-ADP-10,
//! PAR-CTX-03). Cursor has no http and no `mcp_tool` hook type, so it spawns this
//! binary once per event; the startup cost is on the hot path of every tool call,
//! which is why it is std-only (no async runtime, no HTTP or JSON crate) and why
//! D-08 budgets it at 2 MiB.
//!
//! ```text
//! xmustard-cursor <event> [--api URL] [--workspace ID] [--allow-insecure-remote]
//! ```
//!
//! It reads the event JSON on stdin, posts it to `/api/hooks/cursor/<event>` over
//! the daemon's TCP address (`--api`, else `XMUSTARD_API_BASE`, else
//! `http://127.0.0.1:8042`), and writes Cursor's documented answer keys to stdout.
//! The bearer token is `XMUSTARD_API_TOKEN` and is sent as a header, never in the
//! URL, and only to a loopback host unless `--allow-insecure-remote` is given.
//! `--workspace` pins the event to a workspace; without it the client binds by
//! `CURSOR_PROJECT_DIR` matched against the event's `workspace_roots`, and posts
//! nothing when no member matches.
//!
//! **It fails open** (the owner's decision, 2026-10-03): every failure path exits
//! non-zero and not 2, so with `failClosed: false` in integrations/cursor/hooks.json
//! the user's tool call proceeds with its own output. The warning goes to stderr,
//! which Cursor logs but the agent never sees.

use std::io::{self, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::time::{Duration, Instant};

use xmustard_core::cursorhook::{self, FAIL_OPEN_EXIT};

/// Largest event read from stdin; a larger event is not posted.
const MAX_BODY: usize = 8 << 20;
/// Largest answer read back (the daemon's hook reply carries a projection plus a
/// handle, both of which cap far below this).
const MAX_ANSWER: usize = 1 << 20;
/// The whole budget: connect, post and read together.
const BUDGET: Duration = Duration::from_millis(200);

fn main() {
    let code = run(
        std::env::args().skip(1),
        &mut io::stdin().lock(),
        &mut io::stdout().lock(),
    );
    std::process::exit(code);
}

/// One hook run. Every failure return is a fail-open exit, never Cursor's blocking
/// code 2, so a failure can never stop the user's agent.
fn run(args: impl Iterator<Item = String>, stdin: &mut dyn Read, stdout: &mut dyn Write) -> i32 {
    let cfg = match Config::from_args(args, cursorhook::process_env()) {
        Ok(cfg) => cfg,
        Err(msg) => {
            warn(&msg);
            return FAIL_OPEN_EXIT;
        }
    };
    let started = Instant::now();
    let mut event = String::new();
    if let Err(msg) = read_event(stdin, &mut event) {
        warn(&msg);
        return FAIL_OPEN_EXIT;
    }
    let env = cursorhook::read_env(cursorhook::process_env());
    let pin = match cursorhook::workspace_pin(&env, &event, cfg.workspace.as_deref()) {
        Some(pin) => pin,
        // Fail-closed on binding, fail-open on the user's action: nothing is posted
        // and Cursor keeps its own output.
        None => {
            warn(
                "the workspace roots do not agree with CURSOR_PROJECT_DIR, or no workspace id is \
                 configured; sending nothing",
            );
            return 0;
        }
    };
    let reply = match post(&cfg, &pin, &event, started) {
        Ok(Some(reply)) => reply,
        // The daemon was reached and declined to answer: not a failure. Cursor keeps
        // its own output whether the client wrote nothing or wrote an answer.
        Ok(None) => return 0,
        Err(msg) => {
            warn(&msg);
            return FAIL_OPEN_EXIT;
        }
    };
    let context = cursorhook::json_member(&reply, "additional_context");
    let replacement = cursorhook::replacement_for(&cfg.event, &reply);
    let cursorhook::Outcome::Answered(answer) =
        cursorhook::event_answer(&cfg.event, context.as_deref(), replacement.as_deref())
    else {
        return 0;
    };
    let Some(rendered) = cursorhook::render(&answer) else {
        return 0;
    };
    if let Err(e) = stdout.write_all(rendered.as_bytes()) {
        warn(&format!("writing the answer failed: {e}"));
        return FAIL_OPEN_EXIT;
    }
    0
}

/// A warning on stderr. Cursor logs it and the agent never sees it, and it names no
/// secret: the address and the reason, never the token or the event body.
fn warn(msg: &str) {
    let _ = writeln!(io::stderr(), "xmustard-cursor: {msg}");
}

/// Which event this is, and where and how to reach the daemon.
struct Config {
    event: String,
    host: String,
    port: u16,
    token: Option<String>,
    /// The operator's pin, which wins over a root resolved from the environment.
    workspace: Option<String>,
}

impl Config {
    fn from_args(
        args: impl Iterator<Item = String>,
        getenv: impl Fn(&str) -> Option<String>,
    ) -> Result<Config, String> {
        let nonblank =
            |v: Option<String>| v.map(|s| s.trim().to_string()).filter(|s| !s.is_empty());
        let mut event: Option<String> = None;
        let mut url = nonblank(getenv("XMUSTARD_API_BASE"))
            .unwrap_or_else(|| cursorhook::DEFAULT_API_BASE.to_string());
        let mut workspace = nonblank(getenv("XMUSTARD_WORKSPACE_ID"));
        let mut allow_remote = false;
        let mut args = args;
        while let Some(arg) = args.next() {
            match arg.as_str() {
                "--allow-insecure-remote" => {
                    allow_remote = true;
                    continue;
                }
                "-h" | "--help" => return Err(cursorhook::USAGE.to_string()),
                _ => {}
            }
            let (name, inline) = match arg.split_once('=') {
                Some((n, v)) => (n.to_string(), Some(v.to_string())),
                None => (arg.clone(), None),
            };
            if name.starts_with('-') {
                let value = inline
                    .or_else(|| args.next())
                    .ok_or_else(|| format!("{name} needs a value"))?;
                match name.as_str() {
                    "--api" => url = value,
                    "--workspace" => workspace = Some(value),
                    _ => return Err(format!("unknown argument {arg}\n{}", cursorhook::USAGE)),
                }
                continue;
            }
            if event.is_some() {
                return Err(format!("one event per run\n{}", cursorhook::USAGE));
            }
            event = Some(name);
        }
        let event = event.ok_or_else(|| cursorhook::USAGE.to_string())?;
        if !cursorhook::routable(&event) {
            return Err(format!(
                "{event} is not a routed Cursor event ({})\n{}",
                cursorhook::ROUTED_EVENTS.join(", "),
                cursorhook::USAGE
            ));
        }
        let (host, port) = parse_base(&url)?;
        let token = nonblank(getenv("XMUSTARD_API_TOKEN"));
        // A token and a pin both go into a header, and a value with a line break in
        // it would end that header and start another (WS-40c-b r1-4). The token is
        // checked here, before it can reach the request; the pin is a workspace id,
        // whose own rule admits no such character.
        if let Some(t) = &token
            && !valid_header_value(t)
        {
            return Err("XMUSTARD_API_TOKEN holds a character that cannot go in a header".into());
        }
        if let Some(w) = &workspace
            && !cursorhook::valid_workspace_id(w)
        {
            return Err(format!("{w} is not a workspace id"));
        }
        // plain http:// carries the bearer token in cleartext: loopback only, unless
        // the operator opts in (a TLS-terminating proxy on another host is their call)
        if token.is_some() && !allow_remote && !is_loopback(&host) {
            return Err(format!(
                "{url}: refusing to send XMUSTARD_API_TOKEN over plain http to a non-loopback host; \
                 use the loopback API or a local TLS proxy, or pass --allow-insecure-remote"
            ));
        }
        Ok(Config {
            event,
            host,
            port,
            token,
            workspace,
        })
    }
}

/// Whether a value can go into an HTTP header: no control character, which includes
/// the line break that would end the header.
fn valid_header_value(v: &str) -> bool {
    !v.is_empty() && !v.bytes().any(|b| b < 0x20 || b == 0x7f)
}

/// `localhost`, 127.0.0.0/8 or ::1 (bracketed or not).
fn is_loopback(host: &str) -> bool {
    let bare = host.trim_start_matches('[').trim_end_matches(']');
    bare.eq_ignore_ascii_case("localhost")
        || bare
            .parse::<std::net::IpAddr>()
            .is_ok_and(|ip| ip.is_loopback())
}

/// Splits `http://host[:port]` off a base address. Only plain HTTP is spoken: the
/// client carries no TLS stack for its startup budget.
fn parse_base(url: &str) -> Result<(String, u16), String> {
    let rest = url
        .strip_prefix("http://")
        .ok_or_else(|| format!("{url}: only http:// URLs are supported"))?;
    let authority = rest.split('/').next().unwrap_or(rest);
    if authority.is_empty() {
        return Err(format!("{url}: missing host"));
    }
    match authority.rsplit_once(':') {
        Some((h, p)) if !h.ends_with(']') => Ok((
            h.to_string(),
            p.parse::<u16>().map_err(|_| format!("{url}: bad port"))?,
        )),
        _ => Ok((authority.to_string(), 80)),
    }
}

/// Reads the event from stdin, refusing an empty or oversized one.
fn read_event(stdin: &mut dyn Read, out: &mut String) -> Result<(), String> {
    let mut raw = Vec::with_capacity(4096);
    let mut chunk = [0u8; 8192];
    loop {
        match stdin
            .read(&mut chunk)
            .map_err(|e| format!("reading the event failed: {e}"))?
        {
            0 => break,
            n => {
                if raw.len() + n > MAX_BODY {
                    return Err("the event is larger than this client posts".into());
                }
                raw.extend_from_slice(&chunk[..n]);
            }
        }
    }
    match String::from_utf8(raw) {
        Ok(s) if !s.trim().is_empty() => {
            *out = s;
            Ok(())
        }
        _ => Err("the event was empty or not valid UTF-8".into()),
    }
}

/// Posts the event and returns the daemon's reply body.
///
/// Ok(None) means the daemon was reached and declined to answer, which is not an
/// error: Cursor keeps its own output either way.
fn post(cfg: &Config, pin: &str, event: &str, started: Instant) -> Result<Option<String>, String> {
    let budget = started
        .checked_add(BUDGET)
        .map(|d| d.saturating_duration_since(Instant::now()))
        .unwrap_or(BUDGET);
    let addr = (cfg.host.as_str(), cfg.port)
        .to_socket_addrs()
        .map_err(|e| format!("resolving {}:{} failed: {e}", cfg.host, cfg.port))?
        .next()
        .ok_or_else(|| format!("{}:{} resolved to no address", cfg.host, cfg.port))?;
    let mut stream = TcpStream::connect_timeout(&addr, budget).map_err(|e| {
        format!(
            "the daemon at {}:{} is unreachable: {e}",
            cfg.host, cfg.port
        )
    })?;
    stream
        .set_read_timeout(Some(budget))
        .and_then(|()| stream.set_write_timeout(Some(budget)))
        .map_err(|e| format!("setting the deadline failed: {e}"))?;
    let mut request = String::with_capacity(event.len() + 256);
    request.push_str(&format!(
        "POST {} HTTP/1.1\r\nHost: {}:{}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n",
        cursorhook::route(&cfg.event),
        cfg.host,
        cfg.port,
        event.len()
    ));
    request.push_str("Connection: close\r\n");
    if let Some(token) = &cfg.token {
        request.push_str(&format!("Authorization: Bearer {token}\r\n"));
    }
    if !pin.is_empty() {
        request.push_str(&format!("X-Xmustard-Workspace: {pin}\r\n"));
    }
    request.push_str("\r\n");
    stream
        .write_all(request.as_bytes())
        .and_then(|()| stream.write_all(event.as_bytes()))
        .map_err(|e| format!("posting the event failed: {e}"))?;
    parse_reply(&mut stream)
}

/// Reads the whole reply and applies the status and framing rules.
fn parse_reply(stream: &mut TcpStream) -> Result<Option<String>, String> {
    let mut raw = Vec::with_capacity(4096);
    let mut chunk = [0u8; 8192];
    loop {
        let n = stream
            .read(&mut chunk)
            .map_err(|e| format!("reading the reply failed: {e}"))?;
        if n == 0 {
            break;
        }
        if raw.len() + n > MAX_ANSWER {
            return Err("the reply is larger than this client reads".into());
        }
        raw.extend_from_slice(&chunk[..n]);
    }
    // The reply is read as bytes and decoded strictly: a lossy decode would replace
    // a byte that is not UTF-8 with U+FFFD, and the client would hand Cursor an
    // answer that differs from the daemon's (WS-40c-b r1-9). A reply that is not
    // valid UTF-8 is no answer.
    let Ok(text) = String::from_utf8(raw) else {
        return Err("the reply was not valid UTF-8".into());
    };
    Ok(answer_of(&text))
}

/// The reply's answer body, or None when the response is not one.
///
/// Only a 200 with a Content-Length body is an answer. A refusal, an empty 200 and a
/// chunked response all read as "no answer", because the daemon always sends a
/// length and a response it did not write in that shape is not one to guess at.
fn answer_of(text: &str) -> Option<String> {
    let (status, rest) = text.split_once("\r\n")?;
    if status.split_whitespace().nth(1)? != "200" {
        return None;
    }
    let split = rest.find("\r\n\r\n")?;
    let (headers, body) = rest.split_at(split);
    let mut length: Option<usize> = None;
    for line in headers.split("\r\n") {
        let Some((name, value)) = line.split_once(':') else {
            continue;
        };
        match name.trim().to_ascii_lowercase().as_str() {
            "content-length" => length = value.trim().parse().ok(),
            "transfer-encoding" => return None,
            _ => {}
        }
    }
    let body = &body[4..];
    let n = length?;
    if n <= MAX_ANSWER && body.len() >= n {
        Some(body[..n].to_string())
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cfg_for(args: &[&str], env: &[(&str, &str)]) -> Result<Config, String> {
        let owned: Vec<(String, String)> = env
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        Config::from_args(args.iter().map(|s| s.to_string()), |k| {
            owned
                .iter()
                .find(|(key, _)| key == k)
                .map(|(_, v)| v.clone())
        })
    }

    #[test]
    fn an_unrouted_event_is_refused() {
        assert!(
            cfg_for(&["afterShellExecution"], &[]).is_err(),
            "only the four routed events are accepted, so a typos hooks.json fails open"
        );
    }

    #[test]
    fn a_routed_event_with_the_default_address_is_accepted() {
        let cfg = cfg_for(&["postToolUse"], &[]).expect("postToolUse routes");
        assert_eq!((cfg.host.as_str(), cfg.port), ("127.0.0.1", 8042));
    }

    #[test]
    fn a_base_address_with_a_port_is_parsed() {
        let cfg = cfg_for(&["postToolUse", "--api=http://127.0.0.1:9999"], &[]).expect("parses");
        assert_eq!(cfg.port, 9999);
    }

    #[test]
    fn a_token_to_a_remote_host_is_refused_without_the_opt_in() {
        assert!(
            cfg_for(
                &["postToolUse", "--api=http://example.invalid:8042"],
                &[("XMUSTARD_API_TOKEN", "secret")]
            )
            .is_err(),
            "plain http to a remote host carries the token in cleartext"
        );
    }

    #[test]
    fn the_remote_opt_in_allows_the_same_address() {
        assert!(
            cfg_for(
                &[
                    "postToolUse",
                    "--api=http://example.invalid:8042",
                    "--allow-insecure-remote",
                ],
                &[("XMUSTARD_API_TOKEN", "secret")]
            )
            .is_ok()
        );
    }

    #[test]
    fn an_https_base_is_refused_because_the_client_speaks_plain_http() {
        assert!(cfg_for(&["postToolUse", "--api=https://127.0.0.1:8042"], &[]).is_err());
    }

    #[test]
    fn no_event_is_a_usage_error() {
        assert!(cfg_for(&[], &[]).is_err());
    }

    #[test]
    fn two_events_are_refused() {
        assert!(cfg_for(&["postToolUse", "sessionStart"], &[]).is_err());
    }

    #[test]
    fn a_flag_without_its_value_is_refused() {
        assert!(cfg_for(&["postToolUse", "--api"], &[]).is_err());
    }

    #[test]
    fn an_unknown_flag_is_refused() {
        assert!(cfg_for(&["postToolUse", "--nope=1"], &[]).is_err());
    }

    #[test]
    fn the_operator_pin_is_read_from_the_environment() {
        let cfg = cfg_for(&["sessionStart"], &[("XMUSTARD_WORKSPACE_ID", "abc")]).expect("routes");
        assert_eq!(cfg.workspace.as_deref(), Some("abc"));
    }

    #[test]
    fn a_200_with_a_length_is_the_answer() {
        assert_eq!(
            answer_of("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}").as_deref(),
            Some("{}")
        );
    }

    #[test]
    fn a_refusal_is_no_answer_rather_than_an_error() {
        assert_eq!(
            answer_of("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"),
            None
        );
        assert_eq!(answer_of("HTTP/1.1 204 No Content\r\n\r\n"), None);
    }

    #[test]
    fn garbage_is_no_answer() {
        assert_eq!(answer_of(""), None);
        assert_eq!(answer_of("garbage"), None);
    }

    #[test]
    fn a_chunked_reply_is_refused_because_the_daemon_never_chunks() {
        assert_eq!(
            answer_of("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\n\r\n"),
            None
        );
        // A response carrying BOTH is refused on the transfer-encoding alone. Without
        // this case the header is redundant (a chunked reply has no length, so the
        // length rule already returns None) and mutation M7 survived.
        assert_eq!(
            answer_of(
                "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nTransfer-Encoding: chunked\r\n\r\n{}"
            ),
            None,
            "a body whose framing is ambiguous is not an answer, whatever its length says"
        );
    }

    #[test]
    fn a_200_without_a_length_is_not_guessed_at() {
        assert_eq!(answer_of("HTTP/1.1 200 OK\r\n\r\n{}"), None);
    }

    #[test]
    fn a_body_shorter_than_its_length_is_no_answer() {
        assert_eq!(
            answer_of("HTTP/1.1 200 OK\r\nContent-Length: 99\r\n\r\n{}"),
            None
        );
    }

    #[test]
    fn an_empty_event_is_not_posted() {
        let mut out = String::new();
        let mut input = io::Cursor::new(b"   ".to_vec());
        assert!(
            read_event(&mut input, &mut out).is_err(),
            "Cursor sends JSON, never blanks"
        );
    }

    #[test]
    fn an_event_is_read_whole() {
        let mut out = String::new();
        let mut input = io::Cursor::new(b"{\"a\":1}".to_vec());
        assert!(read_event(&mut input, &mut out).is_ok());
        assert_eq!(out, "{\"a\":1}");
    }

    #[test]
    fn an_event_that_is_not_utf8_is_refused() {
        let mut out = String::new();
        let mut input = io::Cursor::new(vec![0xff, 0xfe]);
        assert!(read_event(&mut input, &mut out).is_err());
    }

    #[test]
    fn no_matching_root_and_no_pin_sends_nothing_and_fails_open() {
        let mut out: Vec<u8> = Vec::new();
        let mut input = io::Cursor::new(
            br#"{"hook_event_name":"postToolUse","workspace_roots":["/a","/b"]}"#.to_vec(),
        );
        // No CURSOR_PROJECT_DIR, no XMUSTARD_WORKSPACE_ID: nothing to bind.
        let code = run(
            ["postToolUse".to_string()].into_iter(),
            &mut input,
            &mut out,
        );
        assert_eq!(
            code, 0,
            "a hook that cannot name its repository sends nothing"
        );
        assert!(
            out.is_empty(),
            "and writes nothing, so Cursor keeps its own output"
        );
    }

    #[test]
    fn an_unrouted_event_exits_fail_open_and_never_blocks() {
        let mut out: Vec<u8> = Vec::new();
        let mut input = io::Cursor::new(b"{}".to_vec());
        let code = run(
            ["beforeReadFile".to_string()].into_iter(),
            &mut input,
            &mut out,
        );
        assert_eq!(code, FAIL_OPEN_EXIT);
        assert_ne!(code, 2, "exit 2 is Cursor's block code");
        assert!(out.is_empty());
    }

    #[test]
    fn an_unreadable_event_exits_fail_open_and_never_blocks() {
        let mut out: Vec<u8> = Vec::new();
        let mut input = io::Cursor::new(Vec::new());
        let code = run(
            ["postToolUse".to_string()].into_iter(),
            &mut input,
            &mut out,
        );
        assert_eq!(code, FAIL_OPEN_EXIT);
        assert_ne!(code, 2);
        assert!(out.is_empty());
    }
}
