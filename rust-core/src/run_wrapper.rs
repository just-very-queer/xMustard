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
//! * A signal sent to the *wrapper* is forwarded to the child. The wrapper is the process a
//!   client times out on and an operator `kill`s, and the child is the work: without the
//!   forward the wrapper dies on the default disposition and leaves the child running,
//!   holding whatever it took. The wrapper then ends with the child's own outcome, so a
//!   shell's WIFSIGNALED still sees what really happened.
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
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::process::{Command, ExitStatus, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use percent_encoding::{NON_ALPHANUMERIC, utf8_percent_encode};
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
    // Armed BEFORE the spawn, so no signal can reach the wrapper while it still has the
    // default disposition and a child it could orphan; one that lands before `arm` waits for
    // the pid instead (WS-FIX-20-r1-10).
    let mut forwarder = Forwarder::start();
    if !forwarder.is_armed() {
        // Without forwarding, a wrapper over the child could die on a timeout and orphan it.
        // Fail open without that risk: become the command, so every signal reaches it
        // directly and its output is native (WS-FIX-20-r4-2).
        eprintln!("xmustard run: running {} directly, unreduced", argv[0]);
        return not_started(&argv, Command::new(&argv[0]).args(&argv[1..]).exec());
    }
    let mut child = Command::new(&argv[0]);
    child.args(&argv[1..]);
    let (read_end, write_end) = rustix::pipe::pipe().map_err(|e| io_failed("pipe")(e.into()))?;
    let dup = write_end.try_clone().map_err(io_failed("pipe"))?;
    child
        .stdout(Stdio::from(write_end))
        .stderr(Stdio::from(dup));
    let mut spawned = match child.spawn() {
        Ok(c) => c,
        Err(e) => return not_started(&argv, e),
    };
    forwarder.arm(spawned.id());
    drop(child); // the parent's copies of the write end must close for EOF
    let drained = drain(File::from(read_end), limits);
    let status = spawned.wait().map_err(io_failed("wait"))?;
    // From here a forwarded signal has nobody left to reach, so it ends the wrapper itself.
    forwarder.mark_reaped();
    // The forwarder stays armed through `present`: the capture below can take seconds, and
    // a client that timed out must still be able to stop the wrapper during it.
    let exit = exit_of(status);
    let mut drained = drained.map_err(io_failed("read child output"))?;
    let text = present(&target, &argv, exit, &mut drained, limits);
    // Stays armed until the process exits: the caller still prints `text`, which blocks on a
    // full stdout pipe, and a timeout then must still end the wrapper (WS-FIX-20 r3). A
    // stopped watcher would leave the handler installed with no action, swallowing it.
    // `end_like` re-raises through `emulate_default_handler`, which no handler can swallow.
    forwarder.hold_until_exit();
    Ok(Output::Exit(text, exit))
}

/// A command that could not be started answers as a shell would: 127 not found, 126 not
/// executable.
fn not_started(argv: &[String], e: io::Error) -> CmdResult {
    let code = if e.kind() == io::ErrorKind::NotFound {
        127
    } else {
        126
    };
    let text = format!("xmustard run: {}: {e}\n", argv[0]);
    Ok(Output::Exit(text, ChildExit::Code(code)))
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

/// How one forwarded signal reaches the child. One row per forwarded signal, so a signal
/// is added by a row rather than by another branch in a decision.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Delivery {
    /// Relayed whatever its origin and whatever the groups. The child cannot be shown to have
    /// it already: the kernel delivers a terminal's hangup to the **session leader alone**, so
    /// a child in that leader's group does not receive it from anywhere else (WS-FIX-20c,
    /// which replaced WS-FIX-20b's group test here — a shared group is not evidence of a
    /// delivery the kernel does not make). A duplicate costs the child a re-run of its
    /// cleanup; a swallowed signal costs an orphan.
    Always,
    /// Relayed unless the child's own group has already delivered it. A terminal sends SIGINT
    /// and SIGQUIT to **every** process in its foreground group, so the child has the
    /// operator's keystroke and a relayed copy is the second one (WS-FIX-20-r4-1, WS-FIX-20b).
    UnlessGroup,
}

/// The decision table. One row per forwarded signal, and `FORWARDED` is derived from it, so a
/// signal cannot be forwarded without a delivery decision to make about it.
///
/// `SIGHUP` and `SIGTERM` are `Always` because neither has a keystroke: a terminal delivers
/// SIGINT and SIGQUIT to its foreground group and nothing else, so for those two a shared group
/// means the child already has the operator's copy (WS-FIX-20-r4-1, WS-FIX-20b). A terminal
/// disconnect hangs up the session leader alone (WS-FIX-20c) and a POSIX timer reaches the
/// wrapper alone with no sender at all, so a group says nothing about either and skipping there
/// is what leaves a live child unstopped behind a wrapper stuck in `drain`.
const DELIVERIES: &[(i32, Delivery)] = &[
    (signal_hook::consts::signal::SIGHUP, Delivery::Always),
    (signal_hook::consts::signal::SIGTERM, Delivery::Always),
    (signal_hook::consts::signal::SIGINT, Delivery::UnlessGroup),
    (signal_hook::consts::signal::SIGQUIT, Delivery::UnlessGroup),
];

/// The signals the wrapper forwards to its child, as the signal numbers `signal-hook`
/// delivers, derived from `DELIVERIES` so the two cannot drift. Only these: a signal the
/// wrapper was not asked to forward keeps the disposition it had, so the wrapper neither
/// swallows it nor dies from one.
fn forwarded() -> Vec<i32> {
    DELIVERIES.iter().map(|(s, _)| *s).collect()
}

/// How `sig` is delivered. A signal with no row is relayed: this table is consulted only for a
/// signal `forwarded` returned, which is derived from it, so the arm exists for a caller naming
/// a signal directly rather than to paper over a missing row.
fn delivery_of(sig: i32) -> Delivery {
    DELIVERIES
        .iter()
        .find(|(s, _)| *s == sig)
        .map_or(Delivery::Always, |(_, d)| *d)
}

/// Whether a signal the wrapper caught should be relayed to the child.
///
/// `sender` is the process the signal names (`None` when it names none), and `shares_group`
/// is whether the child is in the wrapper's own process group, which is what makes a
/// terminal's group-wide delivery reach the child as well.
///
/// A signal a **process** sent is always relayed: `kill(2)` and `sigqueue(3)` fill the sender,
/// and a client timeout or an operator's `kill <pid>` arrives this way with the wrapper as
/// its only target (WS-41 deviation (2)). That holds for every signal, including the ones a
/// group would have covered.
///
/// A signal the **terminal or the kernel** generated is relayed according to `DELIVERIES`:
/// SIGINT and SIGQUIT are skipped only when the child shares the wrapper's group, because
/// those two are what a terminal sends to its whole foreground group and nothing else; SIGHUP
/// and SIGTERM are never skipped, because a terminal disconnect hangs up the session leader
/// alone and a POSIX timer reaches the wrapper alone, so neither reaches the group and a
/// skipped one orphans a live child (WS-FIX-20c, WS-FIX-20c-r1-4).
///
/// The skip is never taken on the absence of a sender alone: an unfilled or zero-valued field
/// is not evidence that the child already has the signal, and treating it as such is what
/// would drop a timeout. A sender of 0 is a filled field naming pid 0, which no `kill(2)` can
/// come from, and it is relayed for the same reason.
pub fn relay(sig: i32, sender: Option<i32>, shares_group: bool) -> bool {
    match delivery_of(sig) {
        Delivery::Always => true,
        Delivery::UnlessGroup => sender.is_some() || !shares_group,
    }
}

/// The signal's name as an operator sees it, for the watcher's report.
fn name_of(sig: i32) -> &'static str {
    match sig {
        signal_hook::consts::signal::SIGHUP => "SIGHUP",
        signal_hook::consts::signal::SIGINT => "SIGINT",
        signal_hook::consts::signal::SIGQUIT => "SIGQUIT",
        signal_hook::consts::signal::SIGTERM => "SIGTERM",
        _ => "a signal",
    }
}

/// Writes what the watcher decided about one signal the wrapper handled, on the wrapper's own
/// stderr, AFTER the decision has been acted on. The child cannot be told "I declined to relay
/// that" and a quiet period says nothing, so the decision is stated rather than inferred: an
/// operator reading a wrapper that did not stop its child can see why, and the pty regression
/// waits for this line instead of for a period in which nothing happened (WS-FIX-20c). A signal
/// that ended the wrapper keeps the wrapper's own disposition and needs no line.
///
/// It runs after `kill_process` on purpose (WS-FIX-20c-r1-1): a write to a blocked stderr must
/// not cost the relay, and once the signal has gone out a blocked write costs only the line.
fn report(sig: i32, relayed: bool) {
    let decision = match relayed {
        true => "forwarded",
        false => "not forwarded",
    };
    eprintln!("xmustard run: {} {decision} to the child", name_of(sig));
}

/// Whether the child is in the wrapper's own process group, so a terminal's group-wide signal
/// reaches it too. `false` on any error (the child is gone, or its group cannot be read):
/// an unknown group is treated as "not shared", which relays rather than skips.
fn same_process_group(child: rustix::process::Pid) -> bool {
    match (
        rustix::process::getpgid(Some(child)),
        rustix::process::getpgid(None),
    ) {
        (Ok(child_group), Ok(own_group)) => child_group == own_group,
        _ => false,
    }
}

/// Forwards a termination signal to the child while the wrapper runs.
///
/// The wrapper is the process a client times out on and an operator `kill`s, so without
/// this the wrapper dies on the default disposition and the child keeps running: it is
/// orphaned and keeps whatever it took (a lock, a temp file, a port) until it ends on its
/// own. A terminal signal reaches the child through the process group, which is what
/// WS-41's deviation (2) recorded; a signal to the wrapper's own pid does not, and that is
/// the case this closes. A terminal signal that also arrives here is not relayed on, so the
/// child sees one copy and not two, where the terminal delivers to a whole group (`relay`).
///
/// The child's outcome is still what the wrapper reports: `wait` reaps the real status, so
/// a child that survives the signal and exits 0 makes the wrapper exit 0, and one the
/// signal killed makes the wrapper end with that signal as before.
struct Forwarder {
    handle: Option<signal_hook::iterator::Handle>,
    watcher: Option<thread::JoinHandle<()>>,
    /// Set once `wait` has reaped the child. From that point there is nobody left to forward
    /// to, so a forwarded signal ends the WRAPPER instead — see `install`.
    reaped: Arc<AtomicBool>,
    /// Hands the child's pid to the watcher once it exists. Dropping it unsent (the spawn
    /// failed) tells a watcher holding a signal that there will be no child.
    pid: Option<mpsc::Sender<u32>>,
}

impl Forwarder {
    /// Starts forwarding to the child. A failure to install is not fatal: the wrapper then
    /// behaves as it did before, which is worse for the child but never worse than not
    /// running the command at all, so the refusal is a note, not an error.
    fn start() -> Self {
        let reaped = Arc::new(AtomicBool::new(false));
        let (tx, rx) = mpsc::channel();
        let (handle, watcher) = Self::install(rx, Arc::clone(&reaped)).unzip();
        Self {
            handle,
            watcher,
            reaped,
            pid: Some(tx),
        }
    }

    /// Whether the signals are registered, so a signal to the wrapper reaches the child.
    fn is_armed(&self) -> bool {
        self.handle.is_some()
    }

    /// Names the child the watcher forwards to; a signal held since `start` goes now.
    fn arm(&mut self, pid: u32) {
        if let Some(tx) = self.pid.take() {
            let _ = tx.send(pid);
        }
    }

    /// Records that the child is gone, so a signal arriving from now on ends the wrapper.
    fn mark_reaped(&self) {
        self.reaped.store(true, Ordering::SeqCst);
    }

    /// Registers the forwarded signals and starts the thread that relays them to the
    /// child. `None` when the platform refuses the registration.
    fn install(
        pid: mpsc::Receiver<u32>,
        reaped: Arc<AtomicBool>,
    ) -> Option<(signal_hook::iterator::Handle, thread::JoinHandle<()>)> {
        let mut signals = signal_hook::iterator::SignalsInfo::<
            signal_hook::iterator::exfiltrator::WithOrigin,
        >::new(&forwarded())
        .map_err(|e| io::Error::other(e.to_string()))
        .inspect_err(|e| {
            eprintln!("xmustard run: signals are not forwarded to the child: {e}");
        })
        .ok()?;
        let handle = signals.handle();
        let watcher = thread::spawn(move || {
            let mut child = None;
            for origin in signals.forever() {
                let raw = origin.signal;
                if child.is_none() {
                    // Before `arm`: wait for the spawn. No pid ever coming means the spawn
                    // failed, and the signal ends the wrapper as it would have unforwarded.
                    match pid.recv() {
                        Ok(p) => child = rustix::process::Pid::from_raw(p as i32),
                        Err(_) => {
                            let _ = signal_hook::low_level::emulate_default_handler(raw);
                            return;
                        }
                    }
                }
                let (Some(sig), Some(child)) =
                    (rustix::process::Signal::from_named_raw(raw), child)
                else {
                    continue;
                };
                if reaped.load(Ordering::SeqCst) || has_ended(child) {
                    // The child is reaped and there is nothing left to tell it, so the
                    // signal means what it would have meant without any forwarding at all:
                    // end this process the same way. Without this the handler would consume
                    // the signal and do nothing, leaving a client that timed out unable to
                    // stop the wrapper for as long as the capture takes.
                    let _ = signal_hook::low_level::emulate_default_handler(raw);
                    return;
                }
                // Whether to relay is `DELIVERIES`' decision, not this loop's: a signal the
                // terminal or the kernel generated is skipped only where a group delivery
                // would already have reached the child, and a hangup or a timer is never
                // skipped because neither reaches the group.
                let shares_group = same_process_group(child);
                let relayed = relay(raw, origin.process.map(|p| p.pid), shares_group);
                if relayed {
                    // ESRCH: the child is already gone, which is the normal race at exit.
                    // Signalling the PROCESS GROUP instead would need the child to lead its
                    // own group (`CommandExt::process_group(0)`), which changes where an
                    // interactive child sits relative to the terminal: that is WS-21B's
                    // isolation policy, an owner decision (G§5.11), not this row's to take.
                    //
                    // BEFORE the report, always. The delivery is the contract and the line is a
                    // diagnostic, so a stderr whose reader has stopped may cost the diagnostic
                    // but never the signal: reporting first would let a full pipe swallow a
                    // client's timeout and orphan the child (WS-FIX-20c-r1-1).
                    let _ = rustix::process::kill_process(child, sig);
                }
                report(raw, relayed);
            }
        });
        Some((handle, watcher))
    }

    /// Leaves the watcher running for the rest of the process, so any forwarded signal
    /// from here on ends the wrapper (the child is reaped) instead of being swallowed.
    fn hold_until_exit(self) {
        std::mem::forget(self);
    }

    /// Stops forwarding and joins the watcher. `signal-hook-registry` does NOT restore the
    /// previous disposition when its last handler is dropped, so this only removes the
    /// watcher's actions; `end_like` restores SIG_DFL itself before re-raising the child's
    /// signal, and no forwarded signal can reach the wrapper after this point.
    fn stop(&mut self) {
        self.pid = None;
        if let Some(handle) = self.handle.take() {
            handle.close();
        }
        if let Some(watcher) = self.watcher.take() {
            let _ = watcher.join();
        }
    }
}

impl Drop for Forwarder {
    fn drop(&mut self) {
        self.stop();
    }
}

/// Whether the child has exited, reaped or not. `drain` blocks BEFORE `wait` while a
/// grandchild holds the output pipe open, so `reaped` alone would forward a timeout to an
/// exited child and swallow it (WS-FIX-20 r3). `NOWAIT` leaves the status for `wait`; an
/// error (ECHILD once `wait` reaped it) also means the child is gone, so this fails toward
/// ending the wrapper rather than toward swallowing the signal.
fn has_ended(child: rustix::process::Pid) -> bool {
    use rustix::process::{WaitId, WaitIdOptions, waitid};
    let probe = WaitIdOptions::EXITED | WaitIdOptions::NOHANG | WaitIdOptions::NOWAIT;
    !matches!(waitid(WaitId::Pid(child), probe), Ok(None))
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
/// signal, so a shell's WIFSIGNALED sees what the child did; when that cannot end it,
/// 128 + n, the shell convention, is returned.
///
/// The re-raise goes through `emulate_default_handler`, which restores the default
/// disposition before raising. A plain `kill_process(getpid(), sig)` would be swallowed if a
/// handler were still installed: `signal-hook-registry`'s `unregister` documents that it does
/// not restore the previous disposition, so a signal sent here after `Forwarder::stop` would
/// find this process's handler still in place and do nothing.
pub fn end_like(exit: ChildExit) -> i32 {
    match exit {
        ChildExit::Code(c) => c,
        ChildExit::Signal(n) => {
            let _ = signal_hook::low_level::emulate_default_handler(n);
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

    /// WS-FIX-20-r1-10: a TERM that reaches the wrapper before the child's pid is known
    /// must not take the default action (ending the wrapper and orphaning the child); it is
    /// held and forwarded once the child exists. Without the pre-spawn arm this test
    /// process dies of SIGTERM.
    #[test]
    fn a_term_before_the_child_exists_is_forwarded_once_it_does() {
        let mut forwarder = Forwarder::start();
        let me = rustix::process::getpid();
        rustix::process::kill_process(me, rustix::process::Signal::TERM).unwrap();
        thread::sleep(Duration::from_millis(200));
        let mut child = Command::new("sleep").arg("30").spawn().unwrap();
        forwarder.arm(child.id());
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        let status = loop {
            if let Some(s) = child.try_wait().unwrap() {
                break Some(s);
            }
            if std::time::Instant::now() > deadline {
                let _ = child.kill();
                break None;
            }
            thread::sleep(Duration::from_millis(50));
        };
        forwarder.stop();
        let status = status.expect("the held TERM never reached the child");
        assert_eq!(status.signal(), Some(signal_hook::consts::signal::SIGTERM));
    }

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
