//! `xmustard-core run -- <argv>` (WS-41): exit codes, signals, ordering, bounded memory
//! and fail-open behavior, against the real binary and a fake capture API.

mod memprobe;

use std::io::{Read, Write};
use std::net::TcpListener;
use std::os::unix::process::ExitStatusExt;
use std::process::{Command, Output, Stdio};
use std::sync::mpsc;
use std::thread;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// A run with no workspace configured: the API is never contacted.
fn run(argv: &[&str]) -> Output {
    let mut c = Command::new(BIN);
    c.arg("run").arg("--").args(argv);
    c.env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN");
    c.output().unwrap()
}

fn text(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).into_owned()
}

#[test]
fn exit_code_is_preserved() {
    assert_eq!(run(&["sh", "-c", "echo hi; exit 7"]).status.code(), Some(7));
    assert_eq!(run(&["sh", "-c", "exit 0"]).status.code(), Some(0));
}

#[test]
fn a_missing_program_answers_127_like_a_shell() {
    let o = run(&["xmustard-no-such-program"]);
    assert_eq!(o.status.code(), Some(127));
    assert!(text(&o).contains("xmustard-no-such-program"));
}

#[test]
fn a_child_killed_by_a_signal_kills_the_wrapper_the_same_way() {
    let o = run(&["sh", "-c", "echo before; kill -TERM $$"]);
    assert_eq!(o.status.signal(), Some(15), "status {:?}", o.status);
    assert!(
        text(&o).contains("before"),
        "output before the signal is kept"
    );
}

#[test]
fn stdout_and_stderr_keep_their_order() {
    let o = run(&[
        "sh",
        "-c",
        "echo one; echo two >&2; echo three; echo four >&2",
    ]);
    let out = text(&o);
    let body: Vec<&str> = out.lines().take(4).collect();
    assert_eq!(body, ["one", "two", "three", "four"], "{out}");
}

#[test]
fn an_unreachable_api_fails_open_with_the_native_output_and_a_notice() {
    let o = Command::new(BIN)
        .args([
            "run",
            "--workspace",
            "ws",
            "--api",
            "http://127.0.0.1:1",
            "--",
            "sh",
            "-c",
            "echo native; exit 3",
        ])
        .output()
        .unwrap();
    let out = text(&o);
    assert_eq!(o.status.code(), Some(3));
    assert!(out.starts_with("native\n"), "{out}");
    assert!(
        out.contains("[xmustard: passed through unreduced: the API is unreachable"),
        "{out}"
    );
}

/// A one-request capture API that answers `status` with `body`, and reports the
/// request head and the number of body bytes it received.
fn fake_api(status: &'static str, body: &'static str) -> (String, mpsc::Receiver<(String, usize)>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let (mut s, _) = listener.accept().unwrap();
        let mut got = Vec::new();
        let mut buf = [0u8; 8192];
        let (mut head_end, mut len) = (None, 0usize);
        loop {
            let n = s.read(&mut buf).unwrap();
            if n == 0 {
                break;
            }
            got.extend_from_slice(&buf[..n]);
            if head_end.is_none() {
                head_end = got.windows(4).position(|w| w == b"\r\n\r\n").map(|p| p + 4);
                if let Some(h) = head_end {
                    let head = String::from_utf8_lossy(&got[..h]).to_lowercase();
                    len = head
                        .lines()
                        .find_map(|l| l.strip_prefix("content-length: "))
                        .and_then(|v| v.trim().parse().ok())
                        .unwrap();
                }
            }
            if head_end.is_some_and(|h| got.len() >= h + len) {
                break;
            }
        }
        let h = head_end.unwrap();
        tx.send((
            String::from_utf8_lossy(&got[..h]).into_owned(),
            got.len() - h,
        ))
        .unwrap();
        write!(
            s,
            "HTTP/1.0 {status}\r\nContent-Type: application/json\r\n\r\n{body}"
        )
        .unwrap();
    });
    (base, rx)
}

#[test]
fn a_capture_prints_the_projection_and_handle_and_sends_only_a_command_hint() {
    let (base, seen) = fake_api(
        "200 OK",
        r#"{"projection":"PROJECTED","footer":"handle xm1.abc"}"#,
    );
    let o = Command::new(BIN)
        .args([
            "run",
            "--workspace",
            "ws",
            "--api",
            &base,
            "--",
            "sh",
            "-c",
            "echo out; echo err >&2; exit 2",
            "--token=hunter2",
        ])
        .env("XMUSTARD_API_TOKEN", "tok")
        .output()
        .unwrap();
    assert_eq!(o.status.code(), Some(2));
    assert_eq!(text(&o), "PROJECTED\nhandle xm1.abc\n");
    let (head, body_len) = seen.recv().unwrap();
    assert_eq!(
        body_len,
        "out\nerr\n".len(),
        "the whole output was uploaded, in order"
    );
    assert!(
        head.starts_with("POST /api/workspaces/ws/evidence/capture?"),
        "{head}"
    );
    assert!(head.contains("exit_code=2"), "{head}");
    assert!(
        head.to_lowercase().contains("authorization: bearer tok"),
        "{head}"
    );
    assert!(head.contains("command=sh"), "{head}");
    assert!(
        !head.contains("hunter2") && !head.contains("echo"),
        "the command line leaked: {head}"
    );
}

#[test]
fn a_refusal_from_the_api_fails_open_with_the_reason() {
    let (base, _seen) = fake_api(
        "503 Service Unavailable",
        r#"{"reason":"redaction_failed"}"#,
    );
    let o = Command::new(BIN)
        .args([
            "run",
            "--workspace",
            "ws",
            "--api",
            &base,
            "--",
            "sh",
            "-c",
            "echo secret-looking",
        ])
        .output()
        .unwrap();
    let out = text(&o);
    assert!(out.starts_with("secret-looking\n"), "{out}");
    assert!(out.contains("503 redaction_failed"), "{out}");
}

#[test]
fn a_token_is_never_sent_to_a_non_loopback_host() {
    let o = Command::new(BIN)
        .args([
            "run",
            "--workspace",
            "ws",
            "--api",
            "http://example.invalid:9",
            "--",
            "echo",
            "x",
        ])
        .env("XMUSTARD_API_TOKEN", "tok")
        .output()
        .unwrap();
    assert!(text(&o).contains("non-loopback"), "{}", text(&o));
}

#[test]
fn output_is_never_uploaded_to_a_non_loopback_host_even_without_a_token() {
    let o = Command::new(BIN)
        .args([
            "run",
            "--workspace",
            "ws",
            "--api",
            "http://example.invalid:9",
            "--",
            "echo",
            "x",
        ])
        .env_remove("XMUSTARD_API_TOKEN")
        .output()
        .unwrap();
    assert!(text(&o).contains("non-loopback"), "{}", text(&o));
}

#[test]
fn one_hundred_megabytes_stream_through_in_bounded_memory() {
    let out = tempfile::tempfile().unwrap();
    let mut cmd = Command::new(BIN);
    cmd.args([
        "run",
        "--",
        "sh",
        "-c",
        "yes 0123456789abcdef0123456789abcdef | head -c 104857600; echo TAIL-MARK >&2",
    ])
    .env_remove("XMUSTARD_WORKSPACE_ID")
    .stdout(Stdio::from(out.try_clone().unwrap()));
    let peaks = memprobe::child_peaks(&mut cmd);
    assert_eq!(peaks.status.code(), Some(0));
    let mut printed = String::new();
    let mut f = out;
    std::io::Seek::rewind(&mut f).unwrap();
    f.read_to_string(&mut printed).unwrap();
    assert!(printed.contains("TAIL-MARK"), "the tail is kept");
    assert!(
        printed.contains("of 104857"),
        "the notice names the total: {}",
        &printed[printed.len().saturating_sub(200)..]
    );
    assert!(
        printed.len() < 128 << 10,
        "a passthrough is bounded: {} bytes",
        printed.len()
    );
    let dirty = memprobe::mib(peaks.dirty);
    peaks.assert_sampled_dirty("wrapper on 100 MB");
    eprintln!(
        "wrapper dirty peak on 100 MB: {dirty:.1} MiB (rss {:.1} MiB)",
        memprobe::mib(peaks.rss)
    );
    assert!(
        dirty < 24.0,
        "the wrapper held {dirty:.1} MiB while streaming 100 MB"
    );
}
