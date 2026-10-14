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

/// One descriptor-hungry test at a time.
///
/// `without_forwarding_the_wrapper_becomes_the_command` runs the wrapper under
/// `ulimit -n 4`, so it passes only when the descriptors it needs are free: measured failing
/// with status 127 and empty output, which is `sh` unable to exec, not the wrapper taking the
/// forwarding path. Any test that holds an fd for the length of an observation window starves
/// it — the pty tests hold a master, a slave and a piped stderr, and the two report tests hold
/// a FILLED stderr pipe while the wrapper runs. Measured on the box: 2 failures in 10
/// full-binary runs with those two unguarded, 0 in 10 once they take the lock, 20 of 20 serial,
/// and 0 in 10 at the merge base. `every_test_that_holds_a_descriptor_takes_the_terminal_lock`
/// enforces that coverage against this file's own source, so a new descriptor-hungry test
/// cannot reintroduce it silently. Same shape as `tests/refresh.rs`'s `serial()`.
///
/// The lock covers only the descriptor-hungry tests; the rest of the binary is unaffected, and
/// nothing here bounds or skips a test — it removes parallel-load contention, which is the
/// first acceptable fix class in goal §3.3.
static TERMINAL: Mutex<()> = Mutex::new(());

/// Every test that holds an fd for the length of an observation window takes `TERMINAL`.
///
/// The lock existed for the `ulimit -n 4` test but covered only the pty tests, while the two
/// report tests hold a FILLED stderr pipe for as long as the wrapper runs - which is the
/// contention the lock was meant to remove. So the coverage is checked here against this file's
/// own source rather than trusted: `report_job`, `report_file_job` and `terminal_job` are the
/// constructors that fill or hold a descriptor, and a new descriptor-hungry test that forgets
/// the lock fails here rather than as a flake on someone else's box.
///
/// This test skips itself, so the hog-name literals above and the `serial_terminal()` call here
/// are not counted as coverage of themselves.
#[test]
fn every_test_that_holds_a_descriptor_takes_the_terminal_lock() {
    const SELF: &str = "every_test_that_holds_a_descriptor_takes_the_terminal_lock";
    let source = include_str!("run_wrapper.rs");
    let hogs = ["report_job(", "report_file_job(", "terminal_job("];
    let mut checked = 0;
    let mut offenders = Vec::new();
    // Split on `#[test]` and scan each SEGMENT: a test's body runs from its own marker to the
    // next one, so the helpers between two tests belong to neither. The first version scanned
    // forward from each marker to the next, which handed the helpers and the following test to
    // whichever test preceded them (codex-spec, round 7).
    let mut segments: Vec<&str> = source.split("#[test]").collect();
    segments.remove(0);
    for body in segments {
        let name = body
            .lines()
            .find_map(|l| l.trim().strip_prefix("fn "))
            .and_then(|l| l.split('(').next())
            .unwrap_or("");
        if name == SELF {
            continue;
        }
        if !hogs.iter().any(|h| body.contains(h)) {
            continue;
        }
        checked += 1;
        if !body.contains("serial_terminal()") {
            offenders.push(name.to_string());
        }
    }
    assert!(
        offenders.is_empty(),
        "these tests hold a descriptor for an observation window but do not take the \
         terminal lock, so they run beside `ulimit -n 4`: {offenders:?}"
    );
    // 2 pty tests (the keystroke and the hangup), the process-sent signal test, and the two
    // report tests: 5 at this head, measured by this scan rather than assumed. A floor below the
    // true count would let a rename hide a contender, which is how `pty_session` - a name no test
    // calls - hid the pty tests while this check still counted itself (codex-spec, round 7).
    assert!(
        checked >= 5,
        "only {checked} descriptor-holding tests were found"
    );
}

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
        let (_sampled, decided) = keystroke_and_count(dir.path(), &mut master, byte);
        // The wrapper's own report of the decision it took, read after the wait above has
        // seen it. For a terminal keystroke with the child in the wrapper's group that
        // decision is to decline, so the line says so: it asserts the wrapper's reason for
        // the count, and it fails when the report is missing or unreadable, which the count
        // alone cannot do (measured: both report mutations survived the count on its own).
        assert_eq!(
            decided,
            format!("SIG{name} not forwarded (the child already has this signal)"),
            "{name}: the wrapper's report of its decision"
        );
        release(dir.path());
        // The wrapper must also END: it declines to relay, not to report. Asserting the exit
        // is what keeps a hung wrapper from passing this test on the count alone.
        let status = wait_within(&mut wrapper, 10).expect("the wrapper never ended");
        // Re-read the count AFTER the wrapper has ended. `keystroke_and_count` sampled it when
        // the report appeared, which proves the terminal's copy landed but says nothing about a
        // duplicate the wrapper might still have been about to relay: the relay happens on the
        // watcher thread and the report is written by the same thread after it, so a relay that
        // raced the report would land after the sample and go uncounted (codex-spec 3). With
        // the wrapper ended, every delivery it was ever going to make has been made.
        let counted = ack_count(dir.path());
        assert_eq!(
            counted, 1,
            "{name}: the child recorded {counted} copies of the operator's keystroke, so the \
             wrapper relayed the terminal's own"
        );
        assert_eq!(status.code(), Some(0), "{name}: wrapper status {status:?}");
    }
}

/// A terminal disconnect hangs up the session leader ALONE, which is the wrapper here, so the
/// child in the wrapper's group gets SIGHUP only if the wrapper relays it (goal §3.5i). The
/// counter child has no SIGHUP handler and dies of one, so a relayed hangup ends the child and
/// then the wrapper, the same way. A wrapper that declined the kernel's hangup would sit in
/// `drain` behind a live child until that child's own 60 s deadline; the bounded wait below
/// fails first, and `Job` stops the child and kills the wrapper on the way out.
///
/// The disconnect is real: `terminal_job` opens the master close-on-exec, so dropping the test's
/// copy closes the last one and the kernel hangs the terminal up. The kernel's SIGHUP names no
/// sender, so this is the case a sender test cannot reach. Linux only, like the keystroke test,
/// because that is where the delivery was measured.
#[cfg(target_os = "linux")]
#[test]
fn a_terminal_hangup_reaches_the_child_and_ends_the_wrapper() {
    let _g = serial_terminal();
    let dir = tempfile::tempdir().unwrap();
    let (wrapper, master) = terminal_job(dir.path());
    let mut job = Job {
        wrapper,
        dir: dir.path().to_path_buf(),
        _stderr_read: None,
    };
    await_child(&dir.path().join("ready"), dir.path());
    drop(master);
    let status = wait_within(&mut job.wrapper, 20).expect(
        "the wrapper sat in drain after the terminal hung up, so the child never got SIGHUP",
    );
    assert_eq!(
        decision(dir.path()),
        "SIGHUP forwarded to the child",
        "the wrapper's report of the hangup"
    );
    assert_eq!(
        status.signal(),
        Some(libc::SIGHUP),
        "the wrapper must end the way the child did: {status:?}"
    );
}

/// How many deliveries the pty child has acknowledged, from its `ack` file. One line per
/// handled signal, `fsync`ed by the child, so a line means the child has run its handler.
fn ack_count(dir: &std::path::Path) -> usize {
    std::fs::read_to_string(dir.join("ack"))
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count()
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
    let ack = || ack_count(dir);
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
        // A per-signal decision line, not a delivery error and not a drop notice. Keyed on the
        // signal name rather than on a phrase: the three decision phrasings changed at round 7,
        // and a filter pinned to one of them silently returns nothing when they do
        // (codex-spec, round 7).
        .filter(|l| l.starts_with("SIG") || l.starts_with("xmustard-core"))
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
    // Close-on-exec, so the test holds the only copy of the master: a wrapper or child that
    // inherited one would keep the terminal alive after the test drops its own, and dropping it
    // is how the hangup test disconnects the terminal. Set after the open because `posix_openpt`
    // takes no `O_CLOEXEC` outside Linux.
    rustix::io::fcntl_setfd(&master, rustix::io::FdFlags::CLOEXEC).unwrap();
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
    /// The read end of the wrapper's stderr pipe, held so it stays open for the whole test.
    /// A dropped read end would give the wrapper's blocked report an EPIPE instead of a wait,
    /// and EPIPE is not the hazard WS-FIX-20c-r1-1 is about.
    _stderr_read: Option<std::fs::File>,
}

impl Drop for Job {
    fn drop(&mut self) {
        let _ = std::fs::write(self.dir.join("stop"), "");
        let _ = self.wrapper.kill();
        let _ = self.wrapper.wait();
    }
}

/// The child of `report_job`, and the difference from `WATCHDOG` that makes the blocking
/// harness work: it reopens its own stderr on `/dev/null` before anything else.
///
/// The wrapper hands its stderr to the child, so a stderr that blocks also blocks the child —
/// which is why the three measured harnesses (a closed read end, a full blocking pipe, a full
/// non-blocking pipe) each left the wrapper alive with the child's directory empty. Closing the
/// child's stderr separates the two: the wrapper keeps a pipe that is full and undrained, and
/// the child runs its script normally.
const BLOCKING_STDERR_CHILD: &str = r#"exec 2>/dev/null
WM_DIR="$1"
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
exit 9"#;

/// Starts `xmustard-core run` over `BLOCKING_STDERR_CHILD` with its stderr on a pipe that is
/// already at its buffer limit and that nobody ever drains, so the watcher's report write
/// blocks for as long as the pipe stays full.
///
/// A blocking report, not a sampled one: a test that reads the child's log when it sees the
/// report races the CHILD's own trap scheduling, which is the child's business and not the
/// wrapper's (measured: green solo, 0 of 10 under box load). Blocking the report removes that
/// race in the direction that matters. On the correct order the signal has already been
/// delivered when the watcher reaches `report`, so the child has it while the watcher is
/// stuck. On `report`-before-`kill_process` (the mutation that survived round 1) the watcher
/// blocks BEFORE the delivery, so nothing ever reaches the child and its log stays empty.
///
/// The child must not inherit the blocked descriptor, or it would block on its first write and
/// never reach its script: `BLOCKING_STDERR_CHILD` reopens its own stderr on `/dev/null` as its
/// first statement, which is why this harness works where the three measured ones did not.
fn report_job(dir: &std::path::Path) -> Job {
    let (read_end, write_end) = pipe().expect("a pipe for the wrapper's stderr");
    // Fill the pipe to its limit and keep the read end OPEN, so the wrapper's write blocks
    // rather than seeing EPIPE: a diagnostic that fails fast would not exercise the ordering.
    fill_pipe(&write_end);
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .arg("sh")
        .arg("-c")
        .arg(BLOCKING_STDERR_CHILD)
        .arg("sh")
        .arg(dir.to_str().unwrap());
    c.env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stderr(write_end);
    let wrapper = c.spawn().expect("the wrapper could not start");
    Job {
        wrapper,
        dir: dir.to_path_buf(),
        // Held so the descriptor stays open for the whole test: closing it would turn the
        // wrapper's blocked write into EPIPE and hand this test a pass on the wrong code.
        _stderr_read: Some(read_end),
    }
}

/// A pipe, on the two raw descriptors the wrapper needs. `libc::pipe` rather than
/// `std::os::unix::net` because a pipe is what a blocking write needs and a socket pair is
/// not what the wrapper writes to.
fn pipe() -> Option<(std::fs::File, std::fs::File)> {
    let mut fds = [0i32; 2];
    // SAFETY: `fds` is two valid, writable ints; `pipe` writes exactly two descriptors.
    if unsafe { libc::pipe(fds.as_mut_ptr()) } != 0 {
        return None;
    }
    Some(unsafe {
        (
            std::fs::File::from_raw_fd(fds[0]),
            std::fs::File::from_raw_fd(fds[1]),
        )
    })
}

/// Writes into the pipe until it is full, then leaves the descriptor BLOCKING.
///
/// The fill must not block THIS process (the test would hang before spawning anything) and the
/// wrapper's descriptor must stay BLOCKING (a non-blocking full pipe answers `EAGAIN`, which is
/// a different hazard than the one r1-1 is about). One `dup` cannot give both: it shares the
/// open file description, so `F_SETFL(O_NONBLOCK)` on the copy sets the flag for the wrapper too
/// — three review channels found that independently (codex-review P2, codex-spec, OCR), and it
/// would have made this test pass on the ordering it exists to condemn.
///
/// So the flag is set on a copy for the fill and then **cleared on the descriptor the wrapper
/// inherits**. Dropping the copy is not enough, and that is measured rather than assumed: the
/// flag lives on the shared open file description, which survives while any descriptor refers to
/// it, so a closed filler leaves `write_end` non-blocking and the report `EAGAIN`s (the
/// assertion below caught it, logs/ws-fix-20c-run-20261004T163930Z.log). `F_GETPIPE_SZ` is
/// deliberately not used: it is Linux-only and the repository's checks run on macOS too
/// (codex-review P1, round 3). The loop stops on `EAGAIN`, which is the kernel saying there is no
/// room at all, and a one-byte probe afterwards proves it rather than assuming it (OCR, round 5).
fn fill_pipe(write_end: &std::fs::File) {
    {
        // SAFETY: `dup` of a live descriptor; `fcntl(F_SETFL)` on this copy only.
        let filler = unsafe {
            let copy = libc::dup(std::os::fd::AsRawFd::as_raw_fd(write_end));
            assert!(copy >= 0, "the fill descriptor could not be duplicated");
            assert_eq!(
                libc::fcntl(copy, libc::F_SETFL, libc::O_NONBLOCK),
                0,
                "the fill descriptor could not be made non-blocking"
            );
            std::fs::File::from_raw_fd(copy)
        };
        let chunk = vec![b'x'; 4096];
        // Loop until `EAGAIN`, NOT until a short write. For a write larger than `PIPE_BUF` the
        // kernel may take only the space that is free and return short while room remains, so a
        // short write does not mean the pipe is full — and a partly-full pipe may still hold the
        // decision line, which would let the wrapper's report through and the witness pass on the
        // code it exists to condemn (OCR, round 5). Only `EAGAIN` says there is no room at all.
        loop {
            // SAFETY: a plain write of a live buffer to a valid descriptor.
            let written = unsafe {
                libc::write(
                    std::os::fd::AsRawFd::as_raw_fd(&filler),
                    chunk.as_ptr().cast(),
                    chunk.len(),
                )
            };
            if written < 0 {
                // `last_os_error` rather than a compared constant: a non-blocking pipe with no
                // room answers EAGAIN or EWOULDBLOCK depending on the platform, and this harness
                // has to know which rather than assume it.
                let e = std::io::Error::last_os_error();
                assert_eq!(
                    e.kind(),
                    std::io::ErrorKind::WouldBlock,
                    "the fill failed for a reason other than a full pipe: {e}"
                );
                break;
            }
            assert!(
                written > 0,
                "a full pipe must refuse the write, not accept zero bytes"
            );
        }
        // Proof, not an assumption: the filler is non-blocking and the pipe is still full, so a
        // one-byte probe from this same descriptor must be refused. Without this the harness
        // could be exercising a pipe with room in it and the test would not mean anything.
        let probe = b".";
        // SAFETY: a one-byte write from the same live non-blocking descriptor.
        let refused = unsafe {
            libc::write(
                std::os::fd::AsRawFd::as_raw_fd(&filler),
                probe.as_ptr().cast(),
                1,
            )
        };
        assert!(
            refused < 0,
            "the pipe still accepted a byte, so the wrapper's report would not block"
        );
        assert_eq!(
            std::io::Error::last_os_error().kind(),
            std::io::ErrorKind::WouldBlock,
            "the pipe refused the probe for a reason other than being full"
        );
    }
    // Clear the flag the fill set on the shared description, so the wrapper inherits a blocking
    // descriptor. SAFETY: `write_end` is live and the only file-status flags it carries are the
    // ones this function just set.
    assert_eq!(
        unsafe { libc::fcntl(std::os::fd::AsRawFd::as_raw_fd(write_end), libc::F_SETFL, 0,) },
        0,
        "the fill could not restore the wrapper's descriptor to blocking"
    );
    assert!(
        !is_nonblocking(write_end),
        "the fill left the wrapper's descriptor non-blocking"
    );
}

/// Whether `fd` carries `O_NONBLOCK`, read back from the descriptor rather than assumed.
fn is_nonblocking(fd: &std::fs::File) -> bool {
    // SAFETY: `F_GETFL` takes no argument and returns the descriptor's flags.
    let flags = unsafe { libc::fcntl(std::os::fd::AsRawFd::as_raw_fd(fd), libc::F_GETFL) };
    flags & libc::O_NONBLOCK != 0
}

/// WS-FIX-20c-r1-1: the signal reaches the child even when the wrapper cannot report it.
///
/// The report is a diagnostic, so it must not be able to cost the relay. Two hazards, and
/// `report_job` makes the second one real: the wrapper's stderr is a pipe already at its buffer
/// limit that nobody ever drains, so **any** write the watcher makes there would wait.
///
/// 1. **A report that blocks must never sit between the decision and the delivery.** The
///    delivery is `kill_process`; the report only describes it. So the delivery goes out first.
/// 2. **A report must not block at all** (codex-review P1), because there is one watcher
///    thread: a blocked write parks it and every LATER signal goes unforwarded. So `report`
///    writes through its own non-blocking descriptor.
///
/// A third hazard the earlier version of this test had: it read the child's log at the instant
/// it saw the report, which races the CHILD's trap scheduling rather than the wrapper's
/// ordering — measured on the box, green solo and 0 of 10 under load. This one waits for the
/// child's log, so the wrapper's ordering decides the outcome and the child only records it
/// afterwards. Both hazards are witnessed: the child is signalled with the pipe full, and a
/// SECOND signal still arrives afterwards, which a blocked watcher could not deliver.
#[test]
fn the_child_has_the_signal_before_the_wrapper_reports_it_did() {
    let _g = serial_terminal();
    let dir = tempfile::tempdir().unwrap();
    let mut job = report_job(dir.path());
    await_file(&dir.path().join("ready"));
    let sent = Command::new("kill")
        .args(["-TERM", &job.wrapper.id().to_string()])
        .status()
        .unwrap();
    assert!(sent.success(), "kill -TERM on the wrapper");
    // The child's log is awaited, not sampled. Correct order delivers the signal before the
    // watcher reports, so the line is already there or arrives immediately; report-first
    // blocks in the watcher before the delivery, so nothing ever arrives.
    let log = await_seen(dir.path(), "TERM");
    assert!(
        log.contains("TERM"),
        "the wrapper's report cost the relay: the child was never signalled, so the diagnostic \
         could report before the delivery it describes: {log:?}"
    );
    // The watcher is still running, so a second signal must still be relayed. With one watcher
    // thread a report that blocked on this full pipe would park it forever and this line would
    // never arrive: this is what codex-review P1 found, and the pipe being full is what makes
    // it observable rather than theoretical.
    let again = Command::new("kill")
        .args(["-INT", &job.wrapper.id().to_string()])
        .status()
        .unwrap();
    assert!(again.success(), "kill -INT on the wrapper");
    let log = await_seen(dir.path(), "INT");
    assert!(
        log.contains("INT"),
        "the watcher was still blocked on the first report, so a later signal went unforwarded \
         and the child kept running: {log:?}"
    );
    release(dir.path());
    let ended = wait_within(&mut job.wrapper, 10).expect("the wrapper never ended");
    assert_eq!(ended.code(), Some(0), "wrapper status {ended:?}");
    // No assertion on the report line here, and that is deliberate: this job's stderr is full
    // and undrained, so a correct wrapper CANNOT get the line out, and asserting that it did
    // would condemn the code for working. That this pipe is full is also why the harness is
    // about the relay rather than about the report.
    //
    // Reporting has its own witness, and it needs a stderr that is merely SLOW rather than
    // permanently full: `the_wrapper_states_what_it_decided_for_each_signal` gives the wrapper a
    // file for stderr and reads the decision back. Between them the two claims are covered,
    // which codex-spec (round 4) was right that neither test managed alone.
}

/// The other half of the report's contract, on its own harness: the wrapper SAYS what it decided
/// about each signal it handled.
///
/// `the_child_has_the_signal_before_the_wrapper_reports_it_did` gives the wrapper a stderr that
/// is permanently full, which is the right shape for proving the relay is not delayed by a
/// diagnostic and the wrong shape for reading the diagnostic back — so it witnesses the relay
/// and this witnesses the report. Neither test covers both, which is what codex-spec (round 4)
/// found.
///
/// A file for stderr, so the reporter's write always completes and `drain_reports` has something
/// to wait for. That is the part the reporter thread exists for and the part a detached thread
/// got wrong: a forwarded signal that promptly ends the child could otherwise end the wrapper
/// before the line was ever written.
///
/// The child's exit is delayed until after the wrapper's own outcome is decided, so the wrapper's
/// exit races the reporter rather than following it. A child that ended instantly would let the
/// reporter usually win by luck, and `drain_reports` would be untested — measured: with an
/// instant child, removing the drain from the exit path leaves all 24 tests green
/// (logs/ws-fix-20c-run-20261004T172918Z.log). `LATE_CHILD` makes the reporter lose by
/// construction, so the drain is what decides whether the line lands.
#[test]
fn the_wrapper_states_what_it_decided_for_each_signal() {
    let _g = serial_terminal();
    let dir = tempfile::tempdir().unwrap();
    let mut job = report_file_job(dir.path(), LATE_CHILD);
    await_file(&dir.path().join("ready"));
    let wrapper = job.wrapper.id().to_string();
    for sig in ["-TERM", "-INT"] {
        let sent = Command::new("kill").args([sig, &wrapper]).status().unwrap();
        assert!(sent.success(), "kill {sig} on the wrapper");
        let name = sig.trim_start_matches('-');
        let log = await_seen(dir.path(), name);
        assert!(log.contains(name), "the child never got {name}: {log:?}");
    }
    release(dir.path());
    let ended = wait_within(&mut job.wrapper, 10).expect("the wrapper never ended");
    assert_eq!(ended.code(), Some(0), "wrapper status {ended:?}");
    // Read AFTER the wrapper ended, so `drain_reports` has had its chance and the line is there
    // whether the reporter ran early or was flushed on the way out.
    let stated = std::fs::read_to_string(dir.path().join("wrapper-err")).unwrap_or_default();
    for name in ["TERM", "INT"] {
        assert!(
            stated.contains(&format!("SIG{name} forwarded to the child")),
            "the wrapper handled SIG{name} without stating the decision: {stated:?}"
        );
    }
}

/// The child `the_wrapper_states_what_it_decided_for_each_signal` runs: `WATCHDOG`, but it
/// stays alive until `stop` appears instead of only sleeping, so the wrapper's own exit cannot
/// overtake it and the race the drain exists to win is decided by the drain rather than by luck.
const LATE_CHILD: &str = r#"WM_DIR="$1"
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
  [ -f "$WM_DIR/stop" ] && sleep 0.5 && exit 0
  sleep 0.05
done
exit 9"#;

/// `watchdog_with_file_stderr` over a named child script, so the two harnesses differ only in
/// the child and not in how the wrapper is started.
fn report_file_job(dir: &std::path::Path, child: &str) -> Job {
    let mut c = Command::new(BIN);
    c.arg("run")
        .arg("--")
        .arg("sh")
        .arg("-c")
        .arg(child)
        .arg("sh")
        .arg(dir.to_str().unwrap());
    c.env_remove("XMUSTARD_WORKSPACE_ID")
        .env_remove("XMUSTARD_API_TOKEN")
        .stderr(std::fs::File::create(dir.join("wrapper-err")).unwrap());
    Job {
        wrapper: c.spawn().expect("the wrapper could not start"),
        dir: dir.to_path_buf(),
        _stderr_read: None,
    }
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
