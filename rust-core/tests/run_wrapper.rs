//! `xmustard-core run -- <argv>` (WS-41): exit codes, signals, ordering, bounded memory
//! and fail-open behavior, against the real binary and a fake capture API.

mod memprobe;

use std::io::{Read, Write};
use std::net::TcpListener;
use std::os::fd::{FromRawFd, IntoRawFd};
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::process::{Command, Output, Stdio};
use std::sync::Mutex;
use std::sync::MutexGuard;
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// One terminal-driven test at a time.
///
/// `without_forwarding_the_wrapper_becomes_the_command` runs the wrapper under
/// `ulimit -n 4`, so it passes only when the descriptors it needs are free. The pty tests
/// hold a pty master, a slave and a piped stderr for the whole of a several-second
/// observation window, and this binary runs its tests in parallel by default, so a pty test
/// beside it starves the low-limit one: measured on the box, 2 failures in 10 full-binary
/// runs with the pty tests in the same binary and 0 in 10 at the merge base, which has no pty
/// test. Serialized, it is 6 of 6. The same shape as `tests/refresh.rs`'s `serial()`.
///
/// The lock covers only the terminal-driven tests; the rest of the binary is unaffected, and
/// nothing here bounds or skips a test — it removes parallel-load contention, which is the
/// first acceptable fix class in goal §3.3.
static TERMINAL: Mutex<()> = Mutex::new(());

fn serial_terminal() -> MutexGuard<'static, ()> {
    TERMINAL.lock().unwrap_or_else(|e| e.into_inner())
}

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

/// `await_file` for a child that is an interpreter rather than a shell: starting one on a
/// loaded box costs seconds, which the shell's 18 s budget does not always cover, and a
/// timeout here would read as a broken wrapper rather than as a slow child.
fn await_child(path: &std::path::Path, dir: &std::path::Path) {
    for _ in 0..3000 {
        if path.exists() {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!(
        "{} never appeared; the wrapper said: {:?}; the dir holds {:?}",
        path.display(),
        std::fs::read_to_string(dir.join("wrapper-err")).unwrap_or_default(),
        std::fs::read_dir(dir)
            .map(|r| {
                r.filter_map(|e| e.ok().map(|e| e.file_name()))
                    .collect::<Vec<_>>()
            })
            .unwrap_or_default()
    );
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
    // The child is still alive here (it traps TERM), so a wrapper that ended now would have
    // left it orphaned: the wrapper must still be waiting before the release.
    thread::sleep(Duration::from_millis(200));
    assert!(
        wrapper.try_wait().unwrap().is_none(),
        "the wrapper ended while its child was still running"
    );
    release(dir.path());
    let status = wrapper.wait().unwrap();
    // The child exits 0 on release, so a wrapper that ends on its own reports 0. Asserting
    // the number rather than discarding it is what separates "the wrapper waited for the
    // child" from "the wrapper died and left the child running" — on the base this status
    // is a signal death, never a code.
    assert_eq!(status.code(), Some(0), "wrapper status {status:?}");
    assert!(
        !seen(dir.path()).contains("timeout"),
        "the child outlived the test"
    );
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
    // The platform's own SIGUSR1 number: 10 on Linux, 30 on macOS. Both are checked
    // locally per AGENTS.md, so a hard-coded 10 reddens every macOS run.
    assert_eq!(
        status.signal(),
        Some(signal_hook::consts::signal::SIGUSR1),
        "wrapper status {status:?}"
    );
    release(dir.path());
    let _ = std::process::Command::new("kill")
        .args(["-TERM", &child])
        .status();
}

/// Every signal in `FORWARDED` reaches the child, so the table is not one entry wide.
#[test]
fn every_forwarded_signal_reaches_the_child() {
    for sig in ["INT", "QUIT", "HUP"] {
        let dir = tempfile::tempdir().unwrap();
        let mut wrapper = watchdog(dir.path());
        signal_wrapper(dir.path(), sig);
        let log = await_seen(dir.path(), sig);
        assert!(log.contains(sig), "{sig} never reached the child: {log:?}");
        release(dir.path());
        let _ = wrapper.wait().unwrap();
    }
}

/// A signal that arrives when the child has already exited is not a crash and does not leave
/// the wrapper unkillable. Round 1 called `Forwarder::stop` immediately after `wait`, so a
/// signal sent while the wrapper was still uploading the child's output found a handler with
/// no action behind it and was swallowed; the wrapper then ran on regardless. The contract is
/// that the signal still ends the wrapper, whether the child is reaped or not.
#[test]
fn a_signal_that_arrives_after_the_child_ended_still_ends_the_wrapper() {
    // The child ends at once, so the wrapper is inside `present` — the capture — when the
    // signal lands. A loopback listener that accepts and never answers holds the wrapper in
    // its reply read; a refused capture would return instantly, the wrapper would finish
    // before the signal could arrive, and this test would pass on the broken code too
    // (measured: a first version survived its mutation, reporting the child's code).
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let api = format!("http://{}", listener.local_addr().unwrap());
    let wrapper = Command::new(BIN)
        .arg("run")
        .arg("--api")
        .arg(&api)
        .arg("--")
        .args(["sh", "-c", "echo done; exit 4"])
        .env("XMUSTARD_WORKSPACE_ID", "ws-signal-race")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let pid = wrapper.id();
    // Wait until the wrapper has actually reached its capture: the listener has accepted the
    // upload. Polling `pgrep -P` cannot do this — the child is reaped within microseconds of
    // being spawned, so a poll either misses it or reports "no child" before the spawn
    // (measured: the first version of this test passed on the broken code for exactly that
    // reason). Accepting the upload is the one event that means the child ran, was reaped,
    // and the wrapper is now in `present`.
    let (uploaded, seen) = mpsc::channel();
    let _server = thread::spawn(move || {
        if let Ok((s, _)) = listener.accept() {
            let _ = uploaded.send(());
            thread::sleep(Duration::from_secs(30));
            drop(s);
        }
    });
    let _ = seen
        .recv_timeout(Duration::from_secs(10))
        .expect("the upload never arrived");
    // Give the wrapper a moment to finish writing the request body into the stalled socket.
    thread::sleep(Duration::from_millis(100));
    let _ = Command::new("kill")
        .args(["-TERM", &pid.to_string()])
        .status();
    let out = wrapper.wait_with_output().unwrap();
    // The signal ended the wrapper. Under round 1's code the handler was still installed with
    // its action removed, so this signal was swallowed and the wrapper outlived the wait.
    assert_eq!(
        out.status.signal(),
        Some(15),
        "a signal after the reap must still end the wrapper, got {:?}",
        out.status
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

/// Polls `child` until it ends or `secs` pass; on timeout kills it and returns `None`.
fn wait_within(child: &mut std::process::Child, secs: u64) -> Option<std::process::ExitStatus> {
    for _ in 0..secs * 50 {
        if let Some(s) = child.try_wait().unwrap() {
            return Some(s);
        }
        thread::sleep(Duration::from_millis(20));
    }
    let _ = child.kill();
    let _ = child.wait();
    None
}

/// WS-FIX-20 r3 (codex-review P1): the direct child has exited but a grandchild still
/// holds the output pipe, so the wrapper is inside `drain`, before `wait`. A TERM then must
/// end the wrapper, not be forwarded to the exited child and swallowed.
#[test]
fn a_term_while_a_grandchild_holds_the_pipe_ends_the_wrapper() {
    let dir = tempfile::tempdir().unwrap();
    let script =
        r#"sleep 20 & printf '%s' "$!" > "$1/grandchild"; printf '%s' "$$" > "$1/pid"; exit 0"#;
    let mut wrapper = Command::new(BIN)
        .args([
            "run",
            "--",
            "sh",
            "-c",
            script,
            "sh",
            dir.path().to_str().unwrap(),
        ])
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdout(Stdio::null())
        .spawn()
        .unwrap();
    await_file(&dir.path().join("pid"));
    let pid = child_pid(dir.path());
    // the child is a zombie (exited, not yet reaped) once ps reports state Z
    for _ in 0..500 {
        let st = Command::new("ps")
            .args(["-o", "stat=", "-p", &pid])
            .output()
            .unwrap();
        if String::from_utf8_lossy(&st.stdout)
            .trim_start()
            .starts_with('Z')
        {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    let killed = Command::new("kill")
        .args(["-TERM", &wrapper.id().to_string()])
        .status()
        .unwrap();
    assert!(killed.success());
    let status = wait_within(&mut wrapper, 5);
    let grandchild = std::fs::read_to_string(dir.path().join("grandchild")).unwrap();
    let _ = Command::new("kill").arg(grandchild.trim()).status();
    let status = status.expect("the TERM was swallowed: the wrapper kept draining");
    assert_eq!(status.signal(), Some(15), "wrapper status {status:?}");
}

/// WS-FIX-20 r3 (codex-spec 1): a TERM while the wrapper is blocked printing its output to
/// a stdout nobody reads must still end it. The first byte read proves the wrapper is in
/// the print, which comes only after the child is reaped and the capture is done.
#[test]
fn a_term_while_the_wrapper_prints_ends_it() {
    let mut wrapper = Command::new(BIN)
        .args([
            "run",
            "--",
            "sh",
            "-c",
            "head -c 200000 /dev/zero | tr '\\0' a",
        ])
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    let mut out = wrapper.stdout.take().unwrap();
    let mut first = [0u8; 1];
    out.read_exact(&mut first).unwrap();
    thread::sleep(Duration::from_millis(200)); // let the unread pipe fill
    let killed = Command::new("kill")
        .args(["-TERM", &wrapper.id().to_string()])
        .status()
        .unwrap();
    assert!(killed.success());
    let status = wait_within(&mut wrapper, 5);
    drop(out);
    let status = status.expect("the TERM was swallowed while the wrapper printed");
    assert_eq!(status.signal(), Some(15), "wrapper status {status:?}");
}

/// WS-FIX-20 r3 (codex-spec 4): a child whose TERM trap exits 0 makes the wrapper exit 0.
/// Without forwarding the wrapper dies of the TERM itself.
#[test]
fn a_child_that_exits_zero_on_term_makes_the_wrapper_exit_zero() {
    let dir = tempfile::tempdir().unwrap();
    let script = r#"trap 'echo trapped; exit 0' TERM; echo started; printf '%s' "$$" > "$1/pid"; printf 'ready\n' > "$1/ready"; i=0; while [ "$i" -lt 600 ]; do i=$((i+1)); sleep 0.05; done; exit 9"#;
    let mut wrapper = Command::new(BIN)
        .args([
            "run",
            "--",
            "sh",
            "-c",
            script,
            "sh",
            dir.path().to_str().unwrap(),
        ])
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    await_file(&dir.path().join("ready"));
    signal_wrapper(dir.path(), "TERM");
    let mut out = String::new();
    wrapper
        .stdout
        .take()
        .unwrap()
        .read_to_string(&mut out)
        .unwrap();
    let status = wait_within(&mut wrapper, 10).expect("the wrapper never ended");
    assert_eq!(status.code(), Some(0), "wrapper status {status:?}");
    assert!(
        out.contains("started") && out.contains("trapped"),
        "the child's output was not kept: {out:?}"
    );
}

/// WS-FIX-20-r4-2: when the signal handlers cannot be registered (here `ulimit -n 4` leaves
/// only fd 3, and signal-hook's self-pipe needs two), the wrapper must not run the child
/// behind a wrapper that cannot forward a timeout. It becomes the command instead, so the
/// child's `$$` is the very pid the caller started, and its output is native.
#[test]
fn without_forwarding_the_wrapper_becomes_the_command() {
    let _g = serial_terminal();
    let run = format!("ulimit -n 4 && exec '{BIN}' run -- sh -c 'echo \"pid=$$\"'");
    let mut wrapper = Command::new("sh")
        .args(["-c", &run])
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    let mut out = String::new();
    wrapper
        .stdout
        .take()
        .unwrap()
        .read_to_string(&mut out)
        .unwrap();
    let status = wait_within(&mut wrapper, 10).expect("the wrapper never ended");
    assert_eq!(status.code(), Some(0), "status {status:?}, output {out:?}");
    assert_eq!(
        out.trim(),
        format!("pid={}", wrapper.id()),
        "not exec'd in place: {out:?}"
    );
}

/// WS-FIX-20b: one Ctrl-C is one signal.
///
/// A terminal delivers SIGINT to every process in its foreground process group, so the child
/// already receives the operator's keystroke. WS-FIX-20's watcher then relayed the wrapper's
/// own copy on to the child, and the child ran its handler twice per keystroke
/// (WS-FIX-20-r4-1). The wrapper must not relay a signal the terminal or the kernel
/// generated for the whole group.
///
/// The terminal here is a real pty and the wrapper's group is its foreground group, which is
/// what a shell does with a job: measured on the box, `fg == pgid == child's pgid`. Writing
/// `\x03` to the master is then what the line discipline turns into SIGINT for that group,
/// so the child gets the terminal's copy directly and the wrapper gets its own to relay. No
/// `kill(2)` is involved, so a relayed copy is distinguishable from the terminal's.
///
/// The child is `python3`, not `sh`, and that is load-bearing rather than stylistic: `dash`
/// coalesces back-to-back SIGINTs, so the two copies arrived microseconds apart and its trap
/// ran **once** (measured on the box: three SIGINTs in a tight loop, one trap call). A
/// shell-based version of this test passes on the broken code, which is how the first
/// version of it did.
///
/// Linux only. The test asserts the wrapper does NOT relay, which holds when the terminal's
/// own group delivery reached the child — the case `#[cfg]` below is the one measured
/// (`AGENTS.md` runs `cargo test` on macOS, so the gate is an attribute rather than a skip).
/// On macOS `Origin::extract` reports a terminal-generated signal as no sender as well (pid
/// and uid both 0), so the same rule applies there; the test is gated because its discriminator
/// was measured on Linux, not because the behaviour is known to differ.
#[cfg(target_os = "linux")]
#[test]
fn one_terminal_keystroke_reaches_the_child_once() {
    let _g = serial_terminal();
    // The keystrokes a terminal turns into SIGINT (INTR) and SIGQUIT (QUIT) for its
    // foreground group. Each keystroke gets its own wrapper, because a run is what leaves a
    // child and its log behind and the first run's child is released before the next starts.
    for (name, byte) in [("INT", b"\x03"), ("QUIT", b"\x1c")] {
        let dir = tempfile::tempdir().unwrap();
        let (mut wrapper, mut master) = terminal_job(dir.path());
        await_child(&dir.path().join("ready"), dir.path());
        let (counted, decided) = keystroke_and_count(dir.path(), &mut master, byte);
        // The wrapper's own report of the decision it took, read after the wait above has
        // seen it. For a terminal keystroke with the child in the wrapper's group that
        // decision is to decline, so the line says so: it asserts the wrapper's reason for
        // the count, and it fails when the report is missing or unreadable, which the count
        // alone cannot do (measured: both report mutations survived the count on its own).
        assert_eq!(
            decided,
            format!("SIG{name} not forwarded to the child"),
            "{name}: the wrapper's report of its decision"
        );
        release(dir.path());
        // The wrapper must also END: it declines to relay, not to report. Asserting the exit
        // is what keeps a hung wrapper from passing this test on the count alone.
        let status = wait_within(&mut wrapper, 10).expect("the wrapper never ended");
        assert_eq!(
            counted, 1,
            "{name}: the child recorded {counted} copies of the operator's keystroke, so the \
             wrapper relayed the terminal's own"
        );
        assert_eq!(status.code(), Some(0), "{name}: wrapper status {status:?}");
    }
}

/// Writes `byte` to the pty master and returns how many deliveries the child acknowledged,
/// with what the wrapper reported it decided about that signal.
///
/// The wait has two stages and both are synchronised, neither on a timer.
///
/// Stage 1 is the terminal's own copy, which the line discipline delivers to the foreground
/// group. The boundary is the child's `ack` file, not a quiet period: the child appends and
/// `fsync`s one line per handled signal, so a line in `ack` means the child has already run
/// its handler for that delivery. A timer cannot stand in for this — the wrapper's watcher is
/// an ordinary thread and can be descheduled for longer than any fixed interval, which is
/// what made the first two versions of this helper able to return a false 1 (r1 codex-spec 2,
/// r1 OCR 1, r2 codex-spec 3, r2 OCR 1).
///
/// Stage 2 is the wrapper's watcher finishing with the signal, and it is a wait on the
/// watcher's own report of what it decided (`run_wrapper::report` writes one line on the
/// wrapper's stderr, `dir/wrapper-err`). Acknowledging the terminal's delivery is not enough
/// on its own, because the duplicate this test exists to catch arrives second: returning
/// there would race the very relay under test. A count of unchanged polls after the
/// acknowledgement was the previous answer and it is gone: the watcher forwards or declines,
/// and either way it has then reported, so the wait is on that report and it means the same
/// thing in both cases — which is what four rounds asked for (r1 codex-spec 2, r2 codex-spec 3,
/// r3 codex-spec 1, r4 codex-spec 2).
///
/// The bound is a deadline; on expiry it returns the count the child last acknowledged, the
/// honest reading at that point. The assertion is `== 1`, so a late duplicate still fails the
/// test rather than passing it.
const SETTLE_INTERVAL: Duration = Duration::from_millis(40);
const ACK_TIMEOUT: Duration = Duration::from_secs(20);

fn keystroke_and_count(
    dir: &std::path::Path,
    master: &mut std::fs::File,
    byte: &[u8],
) -> (usize, String) {
    master.write_all(byte).unwrap();
    master.flush().unwrap();
    let ack = || {
        std::fs::read_to_string(dir.join("ack"))
            .unwrap_or_default()
            .lines()
            .filter(|l| !l.trim().is_empty())
            .count()
    };
    let deadline = std::time::Instant::now() + ACK_TIMEOUT;
    // Stage 1: the terminal's own copy, which the line discipline delivers to the group.
    while std::time::Instant::now() < deadline && ack() == 0 {
        thread::sleep(SETTLE_INTERVAL);
    }
    // Stage 2: the watcher's report of what it decided about this signal. The report is the
    // decision, so a delayed watcher extends the wait rather than letting it pass early.
    await_report(dir, deadline);
    let decided = decision(dir);
    (ack(), decided)
}

/// Blocks until the wrapper's watcher has reported the keystroke's signal. `dir/wrapper-err`
/// is the wrapper's stderr, which `terminal_job` points at a file, so the report is a line
/// in a file this test can read.
fn await_report(dir: &std::path::Path, deadline: std::time::Instant) {
    while std::time::Instant::now() < deadline && decision(dir).is_empty() {
        thread::sleep(SETTLE_INTERVAL);
    }
}

/// What the wrapper reported about the signals it handled, in order, as the operator reads
/// them. Reading the lines rather than a boolean is what lets the caller assert the decision
/// and not only its existence: a wait that recognised nothing and a wait that recognised a
/// report both return, and only the lines tell them apart.
fn decisions(dir: &std::path::Path) -> Vec<String> {
    std::fs::read_to_string(dir.join("wrapper-err"))
        .unwrap_or_default()
        .lines()
        .filter_map(|l| l.strip_prefix("xmustard run: "))
        .filter(|l| l.ends_with("to the child"))
        .map(str::to_string)
        .collect()
}

/// The one decision the wrapper reported, which is the one this keystroke caused. Empty when
/// the report was never written or could not be read, which is the case the caller asserts.
fn decision(dir: &std::path::Path) -> String {
    decisions(dir).last().cloned().unwrap_or_default()
}

/// Starts `xmustard-core run` over `COUNTER_CHILD` in a new session whose controlling
/// terminal is a fresh pty, and returns the wrapper and the pty master. The child inherits
/// the wrapper's process group, so wrapper and child are the pty's one foreground group,
/// exactly as a shell runs a job. The slave is closed here and not returned: a caller that
/// held its number would be holding an fd that is no longer open and could be reused.
fn terminal_job(dir: &std::path::Path) -> (std::process::Child, std::fs::File) {
    let master =
        rustix::pty::openpt(rustix::pty::OpenptFlags::RDWR | rustix::pty::OpenptFlags::NOCTTY)
            .unwrap();
    rustix::pty::grantpt(&master).unwrap();
    rustix::pty::unlockpt(&master).unwrap();
    let master = std::fs::File::from(master);
    let slave = {
        let name = rustix::pty::ptsname(&master, Vec::new()).unwrap();
        rustix::fs::open(name, rustix::fs::OFlags::RDWR, rustix::fs::Mode::empty())
            .unwrap()
            .into_raw_fd()
    };
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .arg("python3")
        .arg(COUNTER_CHILD)
        .arg(dir.to_str().unwrap())
        .env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stdin(owned(slave))
        .stdout(owned(slave))
        .stderr(std::fs::File::create(dir.join("wrapper-err")).unwrap());
    // A controlling terminal is what makes the line discipline signal the group, and only a
    // session leader may acquire one, so `setsid` runs between the fork and the exec. The
    // wrapper then leads its own session and group, and the child it spawns inherits that
    // group: the terminal job a shell would set up. Both calls are safe wrappers and nothing
    // between them allocates, so the post-fork window is the one `spawn` already requires
    // its caller to keep minimal.
    unsafe {
        c.pre_exec(|| {
            rustix::process::setsid()?;
            rustix::process::ioctl_tiocsctty(std::io::stdin())?;
            Ok(())
        });
    }
    let wrapper = c.spawn().unwrap_or_else(|e| {
        panic!(
            "the wrapper could not start in its own session: {e}; setsid on a process group \
             leader answers EPERM, and the pty cannot signal a group it does not own"
        );
    });
    // The parent's copy of the slave must close, or the master never sees EOF.
    drop(unsafe { std::fs::File::from_raw_fd(slave) });
    (wrapper, master)
}

/// `Stdio` for an inherited copy of `fd`. The dup is deliberate: three stdio slots need three
/// descriptors and the pty has one.
fn owned(fd: i32) -> Stdio {
    let copy = unsafe { libc::dup(fd) };
    assert!(copy >= 0, "the pty could not be duplicated");
    unsafe { Stdio::from_raw_fd(copy) }
}

/// The child the pty tests run: it appends one line per signal it is handed, so two
/// deliveries are two lines. A file rather than an inline script because `dash`'s trap
/// coalesces the two copies into one call and cannot witness the defect. It is a path,
/// because the wrapper runs an interpreter over it, and `CARGO_MANIFEST_DIR` is how a test
/// names a file in its own crate without guessing the working directory.
const COUNTER_CHILD: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/tests/testdata/counter_child.py"
);

/// The other half of the contract, on the same harness: a signal a **process** sent is still
/// relayed, which is WS-41 deviation (2) proper — a client timeout and `kill <pid>` reach
/// the wrapper alone, and the pty test above proves a terminal one no longer is.
#[test]
fn a_signal_a_process_sent_still_reaches_the_child() {
    let _g = serial_terminal();
    let dir = tempfile::tempdir().unwrap();
    let (mut wrapper, _master) = terminal_job(dir.path());
    await_child(&dir.path().join("ready"), dir.path());
    Command::new("kill")
        .args(["-TERM", &wrapper.id().to_string()])
        .status()
        .unwrap();
    let log = await_seen(dir.path(), "TERM");
    assert!(
        log.contains("TERM"),
        "a sent TERM was not relayed to the child: {log:?}"
    );
    release(dir.path());
    let status = wait_within(&mut wrapper, 10).expect("the wrapper never ended");
    assert_eq!(status.code(), Some(0), "wrapper status {status:?}");
}

/// Kills the wrapper and releases the child when a test fails, so a failed assertion never
/// leaves a process behind on a box four other lanes share.
struct Job {
    wrapper: std::process::Child,
    dir: std::path::PathBuf,
}

impl Drop for Job {
    fn drop(&mut self) {
        let _ = std::fs::write(self.dir.join("stop"), "");
        let _ = self.wrapper.kill();
        let _ = self.wrapper.wait();
    }
}

/// Starts `xmustard-core run` over `WATCHDOG` with its stderr in `dir/wrapper-err`.
///
/// A file, not a pipe: every attempt to make the wrapper's stderr unwritable also makes the
/// CHILD's stderr unwritable, because the wrapper hands its own stderr to the child, and a
/// `dash` that cannot write to stderr from its first instruction never reaches the script —
/// measured three ways on the box (a closed read end, a full blocking pipe, a full
/// non-blocking pipe all left the wrapper alive with the child's dir empty). So a
/// stderr-as-hazard harness cannot isolate the wrapper at all. The report is therefore
/// observed by ORDER against the signal instead, on two files the test can read.
fn report_job(dir: &std::path::Path) -> Job {
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .arg("sh")
        .arg("-c")
        .arg(WATCHDOG)
        .arg("sh")
        .arg(dir.to_str().unwrap());
    c.env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stderr(std::fs::File::create(dir.join("wrapper-err")).unwrap());
    Job {
        wrapper: c.spawn().expect("the wrapper could not start"),
        dir: dir.to_path_buf(),
    }
}

/// WS-FIX-20c-r1-1: by the time the wrapper reports what it decided, the child has the signal.
///
/// The report is a diagnostic, so it must not be able to cost the relay. Ordering is the
/// whole contract, and it is observable without a stderr hazard: the watcher writes its
/// report to `dir/wrapper-err` and the child records its own handling in `dir/log`, and this
/// test waits for the REPORT and then reads the child's log **without waiting for it**. On
/// the correct order the signal has already gone out, so the line is there the moment the
/// report lands. On `report`-before-`kill_process` (the mutation that survived round 1) the
/// report lands first and the child's log is still empty, which is the whole defect: the
/// diagnostic can be read before the delivery it describes.
///
/// A file, not a pipe, for the wrapper's stderr: every attempt to make that stderr
/// unwritable also makes the CHILD's stderr unwritable, because the wrapper hands its own
/// stderr to the child, and a `dash` that cannot write to stderr from its first instruction
/// never reaches the script. Measured three ways on the box — a closed read end, a full
/// blocking pipe and a full non-blocking pipe each left the wrapper alive with the child's
/// directory empty — so a stderr-as-hazard harness cannot isolate the wrapper at all, and
/// the two versions that tried it are what hung the shared box (goal §1.7 rule 11).
#[test]
fn the_child_has_the_signal_before_the_wrapper_reports_it_did() {
    let dir = tempfile::tempdir().unwrap();
    let mut job = report_job(dir.path());
    await_file(&dir.path().join("ready"));
    let sent = Command::new("kill")
        .args(["-TERM", &job.wrapper.id().to_string()])
        .status()
        .unwrap();
    assert!(sent.success(), "kill -TERM on the wrapper");
    // Both files are read in the SAME loop iteration, so the child's log is sampled in the
    // instant the report is first seen and not one poll later: a separate wait for the log
    // would let a report-first watcher fill it in between and the test would pass on the
    // broken code. The loop exits the moment the report appears, which is the boundary this
    // test is about.
    let mut sampled: Option<String> = None;
    let deadline = std::time::Instant::now() + Duration::from_secs(20);
    while std::time::Instant::now() < deadline {
        if !decision(dir.path()).is_empty() {
            sampled = Some(seen(dir.path()));
            break;
        }
        thread::sleep(SETTLE_INTERVAL);
    }
    let decided = decision(dir.path());
    assert!(
        decided.contains("forwarded to the child"),
        "the wrapper never reported forwarding: {decided:?}"
    );
    let log = sampled.unwrap_or_default();
    assert!(
        log.contains("TERM"),
        "the wrapper reported forwarding before the child had the signal, so the diagnostic \
         came first and the delivery could still be pending: {log:?}"
    );
    release(dir.path());
    let ended = wait_within(&mut job.wrapper, 10).expect("the wrapper never ended");
    assert_eq!(ended.code(), Some(0), "wrapper status {ended:?}");
}

/// The rule that decides the tests above, as one decision with a refusal case on each side
/// and no process or timing in it, so it is auditable without a terminal.
///
/// Three axes decide it: which signal, the `siginfo` sender, and whether the child is in the
/// wrapper's own process group. All three are exercised, because all three are load-bearing:
/// the sender alone decides the client's timeout, the group alone decides a terminal's
/// keystroke, and the signal alone decides the hangup that reaches the session leader alone.
#[test]
fn the_relay_rule_has_a_refusal_case_on_each_side() {
    use signal_hook::consts::signal::{SIGHUP, SIGINT, SIGQUIT, SIGTERM};
    let relayed = xmustard_core::run_wrapper::relay;
    let shared = true;
    let apart = false;
    let sender = Some(4321);
    let none = None;

    // A signal a process sent is relayed whatever the signal and whatever the group: that is
    // the client's timeout and an operator's `kill`, and WS-41 deviation (2) proper.
    for sig in [SIGHUP, SIGINT, SIGQUIT, SIGTERM] {
        for group in [shared, apart] {
            assert!(
                relayed(sig, sender, group),
                "signal {sig} sent by a process must be relayed (group shared: {group})"
            );
        }
    }
    // A terminal-generated SIGINT or SIGQUIT is skipped ONLY when the child is in the wrapper's
    // group, so the terminal's own group-wide delivery is the one the child already has.
    for sig in [SIGINT, SIGQUIT] {
        assert!(
            !relayed(sig, none, shared),
            "a terminal-generated {sig} must not be relayed to a child already in the group"
        );
        assert!(
            relayed(sig, none, apart),
            "a child outside the wrapper's group never got the terminal's {sig}, so relaying \
             is what keeps the wrapper from orphaning it"
        );
    }
    // SIGHUP is the case the kernel delivers to the session leader ALONE, and SIGTERM the case a
    // POSIX timer delivers to the wrapper alone with no sender at all: neither reaches the
    // group, so a shared group says nothing about whether the child has it. Skipping either is
    // what left a live child unstopped behind a wrapper stuck in drain (WS-FIX-20c).
    for sig in [SIGHUP, SIGTERM] {
        for group in [shared, apart] {
            for from in [sender, none, Some(0)] {
                assert!(
                    relayed(sig, from, group),
                    "{sig} reaches the wrapper alone, so it must be relayed whatever the sender \
                     ({from:?}) and whatever the group (shared: {group})"
                );
            }
        }
    }
    // A filled field naming pid 0 cannot come from `kill(2)`, so it is relayed rather than
    // treated as a sender-less terminal signal: dropping it would orphan the child.
    assert!(
        relayed(SIGINT, Some(0), shared),
        "an unfilled sender is not evidence the child already has the signal"
    );
}
