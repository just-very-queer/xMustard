//! `xmustard-core run -- <argv>` (WS-41): exit codes, signals, ordering, bounded memory
//! and fail-open behavior, against the real binary and a fake capture API.

mod memprobe;

use std::io::{Read, Write};
use std::net::TcpListener;
use std::os::unix::process::ExitStatusExt;
use std::process::{Command, Output, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

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

/// A child that records each signal it is sent, one line each, and outlives a wrapper that
/// is gone. It traps every signal it watches, so a recorded line proves the wrapper
/// delivered the signal rather than that the child noticed its pipe close; and `stop` lets a
/// test end it cleanly instead of leaving it to time out.
const WATCHDOG: &str = r#"WM_DIR="$1"
note() { printf '%s\n' "$1" >> "$WM_DIR/log"; }
trap 'note TERM' TERM
trap 'note INT' INT
trap 'note QUIT' QUIT
trap 'note HUP' HUP
trap 'note USR1' USR1
printf '%s' "$$" > "$WM_DIR/pid"
printf 'ready\n' > "$WM_DIR/ready"
i=0
while [ "$i" -lt 900 ]; do
  i=$((i+1))
  [ -f "$WM_DIR/stop" ] && exit 0
  sleep 0.05
done
printf 'timeout\n' >> "$WM_DIR/log"
exit 9"#;

/// Starts the wrapper over a signal-watching child and waits for the child to be ready.
fn watchdog(dir: &std::path::Path) -> std::process::Child {
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .arg("sh")
        .arg("-c")
        .arg(WATCHDOG)
        .arg("sh")
        .arg(dir.to_str().unwrap());
    c.env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN");
    let child = c.spawn().unwrap();
    await_file(&dir.join("ready"));
    child
}

/// Blocks until `path` exists, so a test never races the child's first write.
fn await_file(path: &std::path::Path) {
    for _ in 0..900 {
        if path.exists() {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!("{} never appeared", path.display());
}

fn seen(dir: &std::path::Path) -> String {
    std::fs::read_to_string(dir.join("log")).unwrap_or_default()
}

/// Blocks until `text` shows in the child's log, so a test asserts what the child saw and
/// not only where the wrapper ended.
fn await_seen(dir: &std::path::Path, text: &str) -> String {
    for _ in 0..900 {
        let log = seen(dir);
        if log.contains(text) {
            return log;
        }
        thread::sleep(Duration::from_millis(20));
    }
    seen(dir)
}

/// The child's own pid, the one the wrapper signals.
fn child_pid(dir: &std::path::Path) -> String {
    std::fs::read_to_string(dir.join("pid"))
        .unwrap()
        .trim()
        .to_string()
}

/// The wrapper's pid: the child recorded its own `$$`, so its parent is the wrapper.
fn wrapper_pid(dir: &std::path::Path) -> u32 {
    let ppid = Command::new("ps")
        .args(["-o", "ppid=", "-p", &child_pid(dir)])
        .output()
        .unwrap();
    String::from_utf8_lossy(&ppid.stdout)
        .trim()
        .parse()
        .expect("the wrapper's pid")
}

/// Signals the wrapper by its own pid, which is the case WS-41's deviation (2) missed: a
/// terminal reaches the child through the process group, a client timeout does not.
fn signal_wrapper(dir: &std::path::Path, sig: &str) {
    let status = Command::new("kill")
        .args([format!("-{sig}"), wrapper_pid(dir).to_string()])
        .status()
        .unwrap();
    assert!(status.success(), "kill -{sig} on the wrapper");
}

/// Ends the watchdog child, so a test never leaves one running past itself.
fn release(dir: &std::path::Path) {
    let _ = std::fs::write(dir.join("stop"), "");
}

#[test]
fn a_term_sent_to_the_wrapper_reaches_the_child() {
    let dir = tempfile::tempdir().unwrap();
    let mut wrapper = watchdog(dir.path());
    signal_wrapper(dir.path(), "TERM");
    let log = await_seen(dir.path(), "TERM");
    assert!(log.contains("TERM"), "the child was never told: {log:?}");
    release(dir.path());
    let status = wrapper.wait().unwrap();
    assert!(
        !seen(dir.path()).contains("outlived"),
        "the wrapper ended before the child did"
    );
    let _ = status;
}

#[test]
fn a_child_killed_by_a_forwarded_signal_kills_the_wrapper_the_same_way() {
    // a child that does NOT trap, so the forwarded SIGTERM ends it; the wrapper must then
    // end with that signal, the rule the self-signalled child already had
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .args(["sleep", "30"])
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN");
    let mut wrapper = c.spawn().unwrap();
    await_sleeping(wrapper.id());
    Command::new("kill")
        .args(["-TERM", &wrapper.id().to_string()])
        .status()
        .unwrap();
    let status = wrapper.wait().unwrap();
    assert_eq!(status.signal(), Some(15), "wrapper status {status:?}");
}

/// Blocks until the wrapper's own child is running, so the signal is not sent into a window
/// where the wrapper has not spawned yet.
fn await_sleeping(wrapper: u32) {
    for _ in 0..900 {
        let out = Command::new("pgrep")
            .args(["-P", &wrapper.to_string()])
            .output()
            .unwrap();
        if out.status.success() && !out.stdout.is_empty() {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!("the wrapper never spawned its child");
}

#[test]
fn the_wrapper_does_not_swallow_a_signal_it_does_not_forward() {
    let dir = tempfile::tempdir().unwrap();
    let mut wrapper = watchdog(dir.path());
    let child = child_pid(dir.path());
    // USR1 is not in FORWARDED, so the wrapper's own disposition is untouched: it dies, and
    // the child is not told.
    Command::new("kill")
        .args(["-USR1", &wrapper_pid(dir.path()).to_string()])
        .status()
        .unwrap();
    let status = wrapper.wait().unwrap();
    assert_eq!(status.signal(), Some(10), "wrapper status {status:?}");
    release(dir.path());
    let _ = std::process::Command::new("kill")
        .args(["-TERM", &child])
        .status();
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
    eprintln!(
        "wrapper dirty peak on 100 MB: {dirty:.1} MiB (rss {:.1} MiB)",
        memprobe::mib(peaks.rss)
    );
    assert!(
        dirty < 24.0,
        "the wrapper held {dirty:.1} MiB while streaming 100 MB"
    );
}
