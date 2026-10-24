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
use std::sync::Mutex;
use std::sync::OnceLock;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
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
    wrap_token: Option<String>,
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
        let mut direct = Command::new(&argv[0]);
        direct.args(&argv[1..]);
        remove_wrap_token(&mut direct);
        return not_started(&argv, direct.exec());
    }
    let mut child = Command::new(&argv[0]);
    child.args(&argv[1..]);
    remove_wrap_token(&mut child);
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
        wrap_token: nonblank(env("XMUSTARD_WRAP_TOKEN")),
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

fn remove_wrap_token(command: &mut Command) {
    command.env_remove("XMUSTARD_WRAP_TOKEN");
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
/// **Known limit, owner-decided (WS-FIX-20c-r5-4).** "Skipped only when the child shares the
/// group" is not the same as "skipped only when a terminal sent it". A signal with no sender
/// and a shared group is skipped, and a POSIX timer delivers SIGINT to the wrapper alone with
/// no sender, so a timer-sent SIGINT or SIGQUIT with a grouped child is **not** forwarded. The
/// ruling that fixes this is goal §3.5i, which keeps the WS-FIX-20b rule and rules out the
/// `si_code` table that would be needed to tell a keystroke from a timer; the three options and
/// their costs are in followups.md for the owner. Not fixed here, and the comment no longer
/// claims a timer is never skipped.
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

/// What the watcher decided about one signal, and whether the child actually got it.
///
/// Three outcomes, because a decision to relay is not a delivery and a deliberate decline is not
/// a failed delivery: an operator deciding whether to re-signal the child needs all three apart.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Decision {
    /// The relay rule declined, by design: the child already has this signal.
    Declined,
    /// The rule relayed it and the child received it.
    Forwarded,
    /// The rule relayed it and the delivery failed. The reason is a separate line.
    Undeliverable,
}

impl Decision {
    fn phrase(self) -> &'static str {
        match self {
            Decision::Declined => "not forwarded (the child already has this signal)",
            Decision::Forwarded => "forwarded to the child",
            Decision::Undeliverable => "NOT forwarded (delivery failed)",
        }
    }
}

/// States what the watcher decided about one signal the wrapper handled, AFTER the decision has
/// been acted on. The child cannot be told "I declined to relay that" and a quiet period says
/// nothing, so the decision is stated rather than inferred: an operator reading a wrapper that did
/// not stop its child can see why, and the pty regression waits for this line instead of for a
/// period in which nothing happened (WS-FIX-20c). A signal that ended the wrapper keeps the
/// wrapper's own disposition and needs no line.
///
/// It runs after the delivery (WS-FIX-20c-r1-1), and it only enqueues: there is exactly one
/// watcher thread, so a write that blocked on a stderr nobody reads would park it for good and
/// every later signal would go unforwarded. The write is the `Reporter`'s.
fn report(sig: i32, outcome: Decision) {
    reporter().send(format!(
        "xmustard run: {} {}\n",
        name_of(sig),
        outcome.phrase()
    ));
}

/// The wrapper's reporter, writing to its own stderr as it is. No `dup` and no `O_NONBLOCK`: a
/// `dup` shares the open file description, so a flag set on the copy would change the wrapper's
/// stderr and the child's inherited one too. A thread that may block needs neither.
static REPORTER: OnceLock<Reporter> = OnceLock::new();

fn reporter() -> &'static Reporter {
    REPORTER.get_or_init(|| Reporter::new(io::stderr(), REPORT_QUEUE))
}

/// Waits, up to `REPORT_DRAIN`, for the wrapper's reporter to write what is already queued.
/// Nothing to wait for when no signal was ever handled: the watcher creates the reporter before
/// it signals the child, so a child that died of a forwarded signal implies one exists.
fn drain_reports() {
    if let Some(reporter) = REPORTER.get() {
        reporter.drain(REPORT_DRAIN);
    }
}

/// A thread that owns the diagnostic writes, so the signal watcher only ever enqueues.
///
/// The queue is bounded, so a watcher cannot grow memory against a sink nobody drains, and a full
/// queue drops the line rather than waiting: a lost diagnostic costs an operator one line, while a
/// blocked watcher costs a child its next signal. Every line that is not written, dropped by a full
/// queue or refused by the sink, is counted, and `drain` states the count, so a loss is visible
/// rather than silent.
struct Reporter {
    lines: mpsc::SyncSender<Report>,
    /// Behind a `Mutex` only because a `static` must be `Sync` and `mpsc::Receiver` is not.
    acks: Mutex<mpsc::Receiver<()>>,
    /// Lines a full queue dropped or the sink refused, not yet stated. Shared with the writer.
    lost: Arc<AtomicU64>,
    /// Signals the watcher has started acting on and not yet reported (`Deciding`).
    deciding: AtomicU64,
}

/// One line for the reporter to write and, when `ack` is set, the signal that it has.
struct Report {
    line: String,
    ack: bool,
}

impl Report {
    /// A line whose writing the writer acknowledges.
    fn acked(line: String) -> Report {
        Report { line, ack: true }
    }
}

/// Held by the watcher from before it signals the child until its decision line is queued, so a
/// drain on the exit path waits for a decision whose delivery may be what ended the child.
struct Deciding<'a>(&'a Reporter);

impl Drop for Deciding<'_> {
    fn drop(&mut self) {
        self.0.deciding.fetch_sub(1, Ordering::SeqCst);
    }
}

impl Reporter {
    /// Starts the writer thread over `sink`, with room for `depth` lines while it is behind.
    fn new(mut sink: impl Write + Send + 'static, depth: usize) -> Reporter {
        let (lines, queued) = mpsc::sync_channel::<Report>(depth);
        let (ack_tx, acks) = mpsc::channel();
        let lost = Arc::new(AtomicU64::new(0));
        let refused = Arc::clone(&lost);
        thread::spawn(move || {
            for report in queued {
                // A failed write is counted, not retried: a sink that refused this line will
                // refuse the retry, and nothing the wrapper decides waits on it.
                if sink.write_all(report.line.as_bytes()).is_err() {
                    refused.fetch_add(1, Ordering::SeqCst);
                }
                if report.ack {
                    let _ = ack_tx.send(());
                }
            }
        });
        Reporter {
            lines,
            acks: Mutex::new(acks),
            lost,
            deciding: AtomicU64::new(0),
        }
    }

    /// Queues `line` without ever blocking the caller; a full queue drops it and counts it.
    fn send(&self, line: String) {
        if self.lines.try_send(Report { line, ack: false }).is_err() {
            self.lost.fetch_add(1, Ordering::SeqCst);
        }
    }

    /// Marks one signal as being acted on until the returned guard drops.
    fn deciding(&self) -> Deciding<'_> {
        self.deciding.fetch_add(1, Ordering::SeqCst);
        Deciding(self)
    }

    /// Waits up to `limit` for every decision in progress to be queued and every queued line to
    /// reach the sink, then states how many lines were lost.
    ///
    /// A spawned thread is detached, so a forwarded signal that promptly ends the child could end
    /// the wrapper before the watcher queued its line or the writer ran, and the decision would
    /// go missing exactly when it matters most. The loss is counted only after the writer has
    /// caught up, so a write the sink refused is in the count, and the count is cleared only once
    /// its notice is queued, so a drain that runs out of time leaves it for a later one. All of it
    /// is bounded by `limit`: the writer may be parked on a sink nobody drains, and the wrapper's
    /// own outcome must not wait on that.
    fn drain(&self, limit: Duration) {
        let deadline = std::time::Instant::now() + limit;
        while self.deciding.load(Ordering::SeqCst) > 0 && std::time::Instant::now() < deadline {
            thread::sleep(DRAIN_POLL);
        }
        if !self.flush(deadline) {
            return;
        }
        self.state_loss(deadline);
    }

    /// States the accumulated loss only after its notice is queued.
    fn state_loss(&self, deadline: std::time::Instant) {
        let lost = self.lost.load(Ordering::SeqCst);
        if lost == 0 {
            return;
        }
        let notice = format!("xmustard run: {lost} decision report(s) not written to stderr\n");
        if self.enqueue(Report::acked(notice), deadline) {
            self.lost.fetch_sub(lost, Ordering::SeqCst);
            self.await_ack(deadline);
        }
    }

    /// Queues an acknowledged empty line and waits for its ack: once it arrives, every line queued
    /// before it has been handed to the sink. `false` when the deadline passed first.
    fn flush(&self, deadline: std::time::Instant) -> bool {
        self.enqueue(Report::acked(String::new()), deadline) && self.await_ack(deadline)
    }

    /// Queues `report`, waiting for room until `deadline`: the queue is fullest exactly when lines
    /// were lost, so giving up at the first full queue would lose the count with them.
    fn enqueue(&self, mut report: Report, deadline: std::time::Instant) -> bool {
        loop {
            match self.lines.try_send(report) {
                Ok(()) => return true,
                Err(mpsc::TrySendError::Full(r)) if std::time::Instant::now() < deadline => {
                    report = r;
                    thread::sleep(DRAIN_POLL);
                }
                Err(_) => return false,
            }
        }
    }

    fn await_ack(&self, deadline: std::time::Instant) -> bool {
        let Ok(acks) = self.acks.lock() else {
            return false;
        };
        acks.recv_timeout(deadline.saturating_duration_since(std::time::Instant::now()))
            .is_ok()
    }
}

/// How many reports may be waiting before the queue drops the newest. Deep enough that a burst of
/// signals all get their line.
const REPORT_QUEUE: usize = 32;

/// How long the wrapper waits for queued reports on its way out. Long enough for a write to a
/// live stderr, short enough that a stuck stderr cannot hold the wrapper's exit.
const REPORT_DRAIN: Duration = Duration::from_millis(50);

/// How often a drain looks for room in a full queue or for a decision still in progress.
const DRAIN_POLL: Duration = Duration::from_millis(1);

/// Signals the child and says whether it got the signal.
///
/// Only the child, never its group: signalling the PROCESS GROUP would need the child to lead its
/// own group (`CommandExt::process_group(0)`), which changes where an interactive child sits
/// relative to the terminal. That is WS-21B's isolation policy, an owner decision (G§5.11).
///
/// ESRCH is the normal race at exit: the child is already gone and was not signalled. Any other
/// error means the child will NOT be signalled (EPERM after it changed its credentials is the case
/// that matters), and its reason goes through the reporter like every other line this thread
/// writes, never `eprintln!`: a synchronous write to a full stderr would park the sole watcher.
fn deliver(child: rustix::process::Pid, sig: rustix::process::Signal) -> Decision {
    match kill_retrying_interrupt(child, sig) {
        Ok(()) => Decision::Forwarded,
        Err(rustix::io::Errno::SRCH) => Decision::Undeliverable,
        Err(e) => {
            reporter().send(format!(
                "xmustard run: could not signal the child ({}): {e}\n",
                name_of(sig.as_raw())
            ));
            Decision::Undeliverable
        }
    }
}

/// `kill(2)` to the child, retrying a signal that arrived before the syscall ran.
///
/// The watcher thread has every forwarded signal's handler installed while it makes this call, so
/// `kill_process` can return `EINTR` with the child untouched. Treating that as a failed delivery
/// would report a child as running that has merely not been signalled yet, which is the wrong
/// diagnostic in the direction that matters (OCR, round 5).
///
/// Bounded, because an unbounded retry here is a spin on a signal that keeps arriving. A signal
/// every millisecond for this long is not a client timeout.
fn kill_retrying_interrupt(
    child: rustix::process::Pid,
    sig: rustix::process::Signal,
) -> rustix::io::Result<()> {
    let give_up = std::time::Instant::now() + KILL_RETRY_WINDOW;
    loop {
        match rustix::process::kill_process(child, sig) {
            Err(rustix::io::Errno::INTR) if std::time::Instant::now() < give_up => {
                thread::sleep(KILL_RETRY_INTERVAL);
            }
            other => return other,
        }
    }
}

/// How long `kill_retrying_interrupt` keeps retrying `EINTR`, and how long it waits between
/// attempts.
const KILL_RETRY_WINDOW: Duration = Duration::from_millis(200);
const KILL_RETRY_INTERVAL: Duration = Duration::from_millis(5);

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
        >::new(forwarded())
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
                // Hold the reporter before any early return: the exit-path drain must wait even
                // when reaping races a signal that was already received.
                let _deciding = reporter().deciding();
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
                let sender = origin.process.map(|p| p.pid);
                let outcome = if relay(raw, sender, same_process_group(child)) {
                    deliver(child, sig)
                } else {
                    Decision::Declined
                };
                // AFTER the delivery, always: the delivery is the contract and the line is a
                // diagnostic, so a stderr whose reader has stopped may cost the line but never
                // the signal (WS-FIX-20c-r1-1).
                report(raw, outcome);
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
    // Before anything that ends the process, so a queued decision report is written before the
    // wrapper's outcome is decided (codex-spec, round 4). Bounded inside.
    drain_reports();
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
    project_with_token(&reply, target.wrap_token.as_deref())
}

/// The projection text of a 200 reply; any other status is the refusal reason.
#[cfg(test)]
fn project(reply: &[u8]) -> Result<String, String> {
    project_with_token(reply, None)
}

fn project_with_token(reply: &[u8], wrap_token: Option<&str>) -> Result<String, String> {
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
        if let Some(token) = wrap_token.filter(|_| footer.starts_with("[xmustard evidence] ")) {
            text.push_str(&format!(" [xmustard-wrap-token:{token}]"));
        }
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

fn basename(word: &str) -> &str {
    word.rsplit('/').next().unwrap_or(word)
}

/// What the server may learn of the command line to pick a reducer: the program's name
/// and, when it is a plain word and the program is one whose subcommand selects a
/// family, that argument (`git diff`). The rest of the line can hold tokens or
/// secrets, so it never leaves the process.
///
/// A client that cannot replace a shell result rewrites the command into
/// `<shell> -c <command>` (the Pi adapter's WS-44e wrap). The program is then the
/// shell, which every command shares, so the hint has to come from the string the
/// shell will run. `sh -c "git diff"` must still hint `git diff`, or the whole
/// command-keyed reducer registry falls through to the generic shell reducer.
fn command_hint(argv: &[String]) -> String {
    let name = basename(&argv[0]);
    // A shell invocation's words come from the string the shell will run, so the
    // hint describes the command and not the shell that every command shares. Only
    // a program the Go side treats as a shell (registry.go: bash, sh, zsh, dash,
    // ksh) is read that way: `python -c <script>` is a script, and none of its
    // words may leave the process.
    let script = SHELLS
        .contains(&name)
        .then(|| argv.get(1).filter(|f| *f == "-c").and(argv.get(2)))
        .flatten();
    match script {
        // The registry classifies every simple command of the line
        // (`commandFamily`: split on `;`, `&&`, `||`, `|`, most specific family
        // wins), so `cd sub && go test ./...` is a test run there. Reading only the
        // line's first word sent `cd`, which the registry also classifies — as the
        // generic shell family — and the capture lost the test reduction entirely.
        Some(line) => shell_line_hint(line).unwrap_or_else(|| name.to_string()),
        None => match argv.first().map(|a| basename(a)).filter(|a| plain(a)) {
            Some(_) => family_hint(argv),
            None => name.to_string(),
        },
    }
}

// The hint over a shell line's simple commands, by the registry's own rule
// (`commandFamily`, registry.go:299): every simple command is classified and the most
// specific wins, compared with a STRICT `>`, so the first of two equally specific
// commands is the one kept. Each command's hint is ranked like registry.go's argv
// family, including the family derived from a package script.
fn shell_line_hint(line: &str) -> Option<String> {
    let mut best: Option<(u8, String)> = None;
    for simple in split_simple_commands(line) {
        let words = command_words(simple);
        let Some(_) = words
            .first()
            .map(|word| basename(word))
            .filter(|w| plain(w))
        else {
            continue;
        };
        let Some((rank, hint)) = family_selection(&words) else {
            continue;
        };
        if best.as_ref().is_none_or(|(best_rank, _)| rank > *best_rank) {
            best = Some((rank, hint));
        }
    }
    best.map(|(_, hint)| hint)
}

#[derive(Clone, Copy)]
enum FamilyMatch {
    Any,
    Subcommand(&'static [&'static str]),
    Contains(&'static [&'static str]),
    Script(&'static str),
    NodeTest,
    Git,
    Container,
    Tail,
    RgFiles,
}

#[derive(Clone, Copy)]
enum FamilyHint {
    Program,
    Keyword,
    Word(&'static str),
}

struct FamilyRule {
    programs: &'static [&'static str],
    matcher: FamilyMatch,
    rank: u8,
    hint: FamilyHint,
}

// Mirrors api-go/internal/evidence/registry.go:354-356 and argvFamily at 455-555. Keep each
// predicate, its specificity rank and the safe family hint together so their
// selection cannot drift. Arguments are lowercased once and argv0 is excluded
// from the registry's lowercased `rest` matching.
const FAMILY_RULES: &[FamilyRule] = &[
    FamilyRule {
        programs: &[
            "pytest",
            "py.test",
            "jest",
            "vitest",
            "mocha",
            "rspec",
            "phpunit",
            "ctest",
            "tox",
            "nox",
            "ava",
            "tap",
            "karma",
            "unittest",
            "nosetests",
            "playwright",
            "cypress",
        ],
        matcher: FamilyMatch::Any,
        rank: 10,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &[
            "eslint",
            "golangci-lint",
            "ruff",
            "flake8",
            "pylint",
            "mypy",
            "pyright",
            "staticcheck",
            "shellcheck",
            "rubocop",
            "stylelint",
            "biome",
            "oxlint",
            "hadolint",
            "markdownlint",
            "revive",
            "golint",
            "tflint",
            "yamllint",
            "pycodestyle",
            "pydocstyle",
            "bandit",
            "semgrep",
        ],
        matcher: FamilyMatch::Any,
        rank: 8,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &[
            "tsc",
            "gcc",
            "g++",
            "clang",
            "clang++",
            "javac",
            "rustc",
            "webpack",
            "esbuild",
            "rollup",
            "cmake",
            "ninja",
            "bazel",
            "msbuild",
            "xcodebuild",
            "swiftc",
            "kotlinc",
            "scalac",
            "turbo",
        ],
        matcher: FamilyMatch::Any,
        rank: 9,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["go"],
        matcher: FamilyMatch::Subcommand(&["test"]),
        rank: 10,
        hint: FamilyHint::Word("test"),
    },
    FamilyRule {
        programs: &["go"],
        matcher: FamilyMatch::Subcommand(&["vet"]),
        rank: 8,
        hint: FamilyHint::Word("vet"),
    },
    FamilyRule {
        programs: &["go"],
        matcher: FamilyMatch::Subcommand(&["build", "install", "run", "generate", "mod"]),
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &["cargo"],
        matcher: FamilyMatch::Subcommand(&["test", "nextest"]),
        rank: 10,
        hint: FamilyHint::Word("test"),
    },
    FamilyRule {
        programs: &["cargo"],
        matcher: FamilyMatch::Subcommand(&["clippy"]),
        rank: 9,
        hint: FamilyHint::Word("clippy"),
    },
    FamilyRule {
        programs: &["cargo"],
        matcher: FamilyMatch::Subcommand(&["build", "check", "run", "install", "doc", "fmt"]),
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &["npm", "yarn", "pnpm", "bun", "deno"],
        matcher: FamilyMatch::Script("test"),
        rank: 10,
        hint: FamilyHint::Word("test"),
    },
    FamilyRule {
        programs: &["npm", "yarn", "pnpm", "bun", "deno"],
        matcher: FamilyMatch::Script("lint"),
        rank: 8,
        hint: FamilyHint::Word("lint"),
    },
    FamilyRule {
        programs: &["npm", "yarn", "pnpm", "bun", "deno"],
        matcher: FamilyMatch::Script("build"),
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &["npm", "yarn", "pnpm", "bun", "deno"],
        matcher: FamilyMatch::Subcommand(&["run"]),
        rank: 0,
        hint: FamilyHint::Word("run"),
    },
    FamilyRule {
        programs: &["node"],
        matcher: FamilyMatch::NodeTest,
        rank: 10,
        hint: FamilyHint::Word("--test"),
    },
    FamilyRule {
        programs: &["make", "just", "task", "rake"],
        matcher: FamilyMatch::Contains(&["test", "check"]),
        rank: 10,
        hint: FamilyHint::Keyword,
    },
    FamilyRule {
        programs: &["make", "just", "task", "rake"],
        matcher: FamilyMatch::Contains(&["lint"]),
        rank: 8,
        hint: FamilyHint::Word("lint"),
    },
    FamilyRule {
        programs: &["make", "just", "task", "rake"],
        matcher: FamilyMatch::Any,
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &[
            "mvn",
            "gradle",
            "gradlew",
            "./gradlew",
            "sbt",
            "dotnet",
            "swift",
            "zig",
            "mix",
        ],
        matcher: FamilyMatch::Contains(&["test"]),
        rank: 10,
        hint: FamilyHint::Word("test"),
    },
    FamilyRule {
        programs: &[
            "mvn",
            "gradle",
            "gradlew",
            "./gradlew",
            "sbt",
            "dotnet",
            "swift",
            "zig",
            "mix",
        ],
        matcher: FamilyMatch::Any,
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &["git"],
        matcher: FamilyMatch::Git,
        rank: 7,
        hint: FamilyHint::Word("diff"),
    },
    FamilyRule {
        programs: &["git"],
        matcher: FamilyMatch::Subcommand(&["grep"]),
        rank: 6,
        hint: FamilyHint::Word("grep"),
    },
    FamilyRule {
        programs: &["git"],
        matcher: FamilyMatch::Subcommand(&["ls-files"]),
        rank: 4,
        hint: FamilyHint::Word("ls-files"),
    },
    FamilyRule {
        programs: &["git"],
        matcher: FamilyMatch::Any,
        rank: 2,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["diff", "colordiff"],
        matcher: FamilyMatch::Any,
        rank: 7,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["docker", "kubectl", "podman", "oc"],
        matcher: FamilyMatch::Container,
        rank: 5,
        hint: FamilyHint::Word("logs"),
    },
    FamilyRule {
        programs: &["docker", "kubectl", "podman", "oc"],
        matcher: FamilyMatch::Subcommand(&["build"]),
        rank: 9,
        hint: FamilyHint::Word("build"),
    },
    FamilyRule {
        programs: &["tail"],
        matcher: FamilyMatch::Tail,
        rank: 5,
        hint: FamilyHint::Word("--follow"),
    },
    FamilyRule {
        programs: &["tail"],
        matcher: FamilyMatch::Any,
        rank: 1,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["rg"],
        matcher: FamilyMatch::RgFiles,
        rank: 4,
        hint: FamilyHint::Word("--files"),
    },
    FamilyRule {
        programs: &["grep", "egrep", "fgrep", "rg", "ag", "ack", "ugrep"],
        matcher: FamilyMatch::Any,
        rank: 6,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["journalctl", "dmesg", "logcat", "stern"],
        matcher: FamilyMatch::Any,
        rank: 5,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["find", "fd", "fdfind", "locate"],
        matcher: FamilyMatch::Any,
        rank: 4,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["ls", "tree", "exa", "eza", "dir", "lsd"],
        matcher: FamilyMatch::Any,
        rank: 3,
        hint: FamilyHint::Program,
    },
    FamilyRule {
        programs: &["cat", "head", "nl", "bat", "less", "more", "sed", "awk"],
        matcher: FamilyMatch::Any,
        rank: 1,
        hint: FamilyHint::Program,
    },
];

#[cfg(test)]
fn argv_family_rank(words: &[String]) -> u8 {
    family_selection(words).map_or(0, |(rank, _)| rank)
}

fn family_selection(words: &[String]) -> Option<(u8, String)> {
    let program = basename(words.first()?).to_ascii_lowercase();
    let args = words
        .iter()
        .skip(1)
        .map(|word| word.to_ascii_lowercase())
        .collect::<Vec<_>>();
    let rest = args.join(" ");
    let sub = args.first().map(String::as_str).unwrap_or_default();
    let matches = |matcher: FamilyMatch| match matcher {
        FamilyMatch::Any => true,
        FamilyMatch::Subcommand(choices) => choices.contains(&sub),
        FamilyMatch::Contains(words) => words.iter().any(|word| rest.contains(word)),
        FamilyMatch::Script(family) => {
            let script = if sub == "run" {
                args.get(1).map(String::as_str).unwrap_or_default()
            } else {
                sub
            };
            family_of(script) == Some(family)
        }
        FamilyMatch::NodeTest => rest.contains("--test"),
        FamilyMatch::Git => {
            matches!(sub, "diff" | "show" | "format-patch")
                || (sub == "log" && (rest.contains(" -p ") || rest.contains("--patch")))
        }
        FamilyMatch::Container => rest.contains(" logs ") || sub == "logs",
        FamilyMatch::Tail => {
            rest.contains(" -f ") || rest.contains("--follow") || rest.contains(".log")
        }
        FamilyMatch::RgFiles => rest.contains(" --files ") || rest.ends_with("--files"),
    };
    FAMILY_RULES
        .iter()
        .find(|rule| rule.programs.contains(&program.as_str()) && matches(rule.matcher))
        .map(|rule| {
            let hint = match rule.hint {
                FamilyHint::Program => program.clone(),
                FamilyHint::Keyword => {
                    let word = ["test", "check", "lint"]
                        .into_iter()
                        .find(|word| rest.contains(word))
                        .unwrap_or("build");
                    format!("{program} {word}")
                }
                FamilyHint::Word(word) => {
                    if matches!(rule.matcher, FamilyMatch::Script(_)) && sub == "run" {
                        format!("{program} run {word}")
                    } else {
                        format!("{program} {word}")
                    }
                }
            };
            (rule.rank, hint)
        })
        .or(Some((0, program)))
}

fn command_words(simple: &str) -> Vec<String> {
    let mut words: Vec<String> = simple
        .split_whitespace()
        .map(|word| word.trim_matches(['\'', '"', '(', ')']).to_string())
        .filter(|word| {
            !word.is_empty()
                && !word.starts_with(['>', '<'])
                && !word.starts_with("2>")
                && !word.starts_with("&>")
        })
        .collect();
    loop {
        let Some(word) = words.first().map(String::as_str) else {
            break;
        };
        let skip = match word {
            word if word.contains('=') && !word.starts_with('-') && !word.starts_with('=') => 1,
            "sudo" | "time" | "env" | "nice" | "command" | "exec" | "npx" | "bunx" | "pnpx"
            | "xargs" | "stdbuf" | "unbuffer" => 1,
            "timeout" if words.len() > 1 => 2,
            "uv" | "poetry" | "pipenv" | "rye" | "hatch" | "pdm"
                if words.get(1).is_some_and(|arg| arg == "run") =>
            {
                2
            }
            word if (word == "python" || word == "python3" || word.starts_with("python3."))
                && words.len() > 2
                && words.get(1).is_some_and(|arg| arg == "-m") =>
            {
                2
            }
            _ => break,
        };
        words.drain(..skip);
    }
    words
}

fn family_hint(words: &[String]) -> String {
    family_selection(words).map_or_else(
        || {
            words
                .first()
                .map(|word| basename(word).to_ascii_lowercase())
                .unwrap_or_else(|| "bash".to_string())
        },
        |(_, hint)| hint,
    )
}

// Split a shell line the way `splitCommand` does, outside quotes: on `;`, `&&`,
// `||`, `|` and newline. A quote, a redirect or a substitution is left alone, so a `&&`
// inside a string does not split the line and no word that fails `plain` reaches
// the hint.
fn split_simple_commands(line: &str) -> Vec<&str> {
    let mut parts = Vec::new();
    let (mut start, mut quote, mut escaped) = (0usize, 0u8, false);
    let bytes = line.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        let c = bytes[i];
        if quote != 0 {
            if escaped {
                escaped = false;
            } else if c == b'\\' && quote == b'"' {
                escaped = true;
            } else if c == quote {
                quote = 0;
            }
            i += 1;
            continue;
        }
        match c {
            b'\'' | b'"' => quote = c,
            b'|' => {
                parts.push(&line[start..i]);
                start = i + 1;
                if bytes.get(i + 1) == Some(&b'|') {
                    i += 1;
                    start = i + 1;
                }
            }
            b'&' if bytes.get(i + 1) == Some(&b'&') => {
                parts.push(&line[start..i]);
                start = i + 2;
                i += 1;
            }
            b'\n' => {
                parts.push(&line[start..i]);
                start = i + 1;
            }
            b';' => {
                parts.push(&line[start..i]);
                start = i + 1;
            }
            _ => {}
        }
        i += 1;
    }
    parts.push(&line[start..]);
    parts
        .into_iter()
        .map(str::trim)
        .filter(|p| !p.is_empty())
        .collect()
}

fn plain(s: &str) -> bool {
    !s.is_empty()
        && !s.starts_with('-')
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-+".contains(&b))
}

/// Programs the Go registry reads as a shell wrapping a `-c` string.
const SHELLS: [&str; 5] = ["bash", "sh", "zsh", "dash", "ksh"];

/// The registry's own script families, in its precedence order (test before lint
/// before build). A script matching none is not sent: the registry's fallback for
/// an unknown npm script is the generic package-manager reducer, not a guess.
///
/// registry.go:491-495 verbatim, and the order is the registry's, because the
/// classification has to agree with it exactly: a hint that disagrees routes the
/// capture to a different reducer than the same command would have had unwrapped.
/// Only `test`, `lint`, `build` and `e2e` are prefix tests there; `t`, `vitest`,
/// `jest`, `eslint`, `compile`, `typecheck`, `tsc`, `install` and `ci` are exact
/// matches, and `starts_with` on any of them would claim a family the registry does
/// not (`tsc` is a build there, `jest` a test).
fn family_of(script: &str) -> Option<&'static str> {
    let s = script;
    const FAMILIES: [(&str, &[&str], &[&str]); 3] = [
        ("test", &["test", "e2e"], &["t", "vitest", "jest"]),
        ("lint", &["lint"], &["eslint"]),
        (
            "build",
            &["build"],
            &["compile", "typecheck", "tsc", "install", "ci"],
        ),
    ];
    FAMILIES
        .iter()
        .find(|(_, prefixes, exact)| {
            prefixes.iter().any(|prefix| s.starts_with(prefix)) || exact.contains(&s)
        })
        .map(|(family, _, _)| *family)
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
    fn wrapper_token_is_removed_from_the_child_environment() {
        let token = "0123456789abcdef0123456789abcdef";
        let mut child = Command::new("env");
        child.env("XMUSTARD_WRAP_TOKEN", token);
        remove_wrap_token(&mut child);
        let output = child.output().unwrap();
        assert!(output.status.success());
        let text = String::from_utf8(output.stdout).unwrap();
        assert!(!text.contains(token), "private token reached child output");
        assert!(
            !text.contains("XMUSTARD_WRAP_TOKEN="),
            "private token reached child's environment"
        );
    }

    #[test]
    fn per_call_token_is_added_only_to_the_accepted_evidence_footer() {
        let token = "0123456789abcdef0123456789abcdef";
        let accepted = concat!(
            "HTTP/1.0 200 OK\r\n\r\n",
            "{\"projection\":\"p\",",
            "\"footer\":\"[xmustard evidence] {\\\"handle\\\":\\\"xm1.H1\\\"}\"}"
        )
        .as_bytes();
        assert_eq!(
            project_with_token(accepted, Some(token)).unwrap(),
            format!(
                "p\n[xmustard evidence] {{\"handle\":\"xm1.H1\"}} [xmustard-wrap-token:{token}]\n"
            )
        );
        let native_lookalike = concat!(
            "HTTP/1.0 200 OK\r\n\r\n",
            "{\"projection\":\"[xmustard evidence] {\\\"handle\\\":\\\"xm1.FAKE\\\"}\",",
            "\"footer\":\"\"}"
        )
        .as_bytes();
        assert!(
            !project_with_token(native_lookalike, Some(token))
                .unwrap()
                .contains(token)
        );
        let refused = b"HTTP/1.0 503 Unavailable\r\n\r\n{\"reason\":\"redaction_failed\"}";
        assert!(
            !project_with_token(refused, Some(token))
                .unwrap_err()
                .contains(token)
        );
    }

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

    /// A sink whose first write waits until `release` is sent or dropped, so a test can hold the
    /// reporter's writer in one write while it fills the queue behind it. `entered` fires when
    /// that first write starts; everything written lands in `out`.
    struct GatedSink {
        entered: Option<mpsc::Sender<()>>,
        gate: mpsc::Receiver<()>,
        out: Arc<Mutex<Vec<u8>>>,
    }

    impl Write for GatedSink {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            if let Some(entered) = self.entered.take() {
                let _ = entered.send(());
                let _ = self.gate.recv();
            }
            self.out.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// The sink, the `entered` receiver, the `release` sender and the shared output.
    fn gated_sink() -> (
        GatedSink,
        mpsc::Receiver<()>,
        mpsc::Sender<()>,
        Arc<Mutex<Vec<u8>>>,
    ) {
        let (entered_tx, entered) = mpsc::channel();
        let (release, gate) = mpsc::channel();
        let out = Arc::new(Mutex::new(Vec::new()));
        let sink = GatedSink {
            entered: Some(entered_tx),
            gate,
            out: Arc::clone(&out),
        };
        (sink, entered, release, out)
    }

    /// The watcher's side of the reporter never waits on the sink: with the writer held in a
    /// write and the queue full, further lines are dropped at once, counted, and the count is
    /// stated by the drain. The sends run on their own thread and are awaited with a deadline, so
    /// a send that blocks fails the test instead of hanging it (goal §1.7 rule 11).
    #[test]
    fn a_full_report_queue_drops_lines_without_blocking_and_the_drain_counts_them() {
        let (sink, entered, release, out) = gated_sink();
        let reporter = Arc::new(Reporter::new(sink, 2));
        reporter.send("first\n".into());
        entered
            .recv_timeout(Duration::from_secs(10))
            .expect("the writer never started the first line");
        let (done, sent) = mpsc::channel();
        let sender = Arc::clone(&reporter);
        thread::spawn(move || {
            for k in 0..5 {
                sender.send(format!("line {k}\n"));
            }
            let _ = done.send(());
        });
        sent.recv_timeout(Duration::from_secs(10))
            .expect("a send blocked behind a stuck writer");
        release.send(()).unwrap();
        reporter.drain(Duration::from_secs(10));
        let text = String::from_utf8(out.lock().unwrap().clone()).unwrap();
        assert_eq!(
            text,
            "first\nline 0\nline 1\n\
             xmustard run: 3 decision report(s) not written to stderr\n"
        );
    }

    /// The drain is bounded: a writer stuck on a sink nobody reads cannot hold the wrapper's exit.
    /// Awaited with a deadline on another thread, so an unbounded drain fails instead of hanging.
    #[test]
    fn a_drain_returns_while_the_writer_is_stuck() {
        let (sink, entered, release, _out) = gated_sink();
        let reporter = Arc::new(Reporter::new(sink, 2));
        reporter.send("stuck\n".into());
        entered
            .recv_timeout(Duration::from_secs(10))
            .expect("the writer never started the first line");
        let (done, drained) = mpsc::channel();
        let draining = Arc::clone(&reporter);
        thread::spawn(move || {
            draining.drain(Duration::from_millis(100));
            let _ = done.send(());
        });
        let waited = drained.recv_timeout(Duration::from_secs(10));
        drop(release);
        waited.expect("the drain waited on a stuck writer past its limit");
    }

    /// A drain on the exit path waits for a decision the watcher is still acting on: the signal it
    /// just delivered may be what ended the child, and its line is not queued yet. The drain runs
    /// on its own thread with a generous limit, so it cannot have timed out within the 200 ms
    /// window; the test then queues the line, releases the decision, and expects exactly it.
    #[test]
    fn a_drain_waits_for_a_decision_still_being_acted_on() {
        let (sink, _entered, release, out) = gated_sink();
        drop(release);
        let reporter = Arc::new(Reporter::new(sink, 2));
        let deciding = reporter.deciding();
        let (done, drained) = mpsc::channel();
        let draining = Arc::clone(&reporter);
        thread::spawn(move || {
            draining.drain(Duration::from_secs(10));
            let _ = done.send(());
        });
        assert_eq!(
            drained.recv_timeout(Duration::from_millis(200)),
            Err(mpsc::RecvTimeoutError::Timeout),
            "the drain returned while a decision was still being acted on"
        );
        reporter.send("SIGTERM forwarded to the child\n".into());
        drop(deciding);
        drained
            .recv_timeout(Duration::from_secs(10))
            .expect("the drain never returned after the decision was queued");
        let text = String::from_utf8(out.lock().unwrap().clone()).unwrap();
        assert_eq!(text, "SIGTERM forwarded to the child\n");
    }

    /// A sink that refuses every line starting with `refused` and keeps the rest.
    struct RefusingSink(Arc<Mutex<Vec<u8>>>);

    impl Write for RefusingSink {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            if buf.starts_with(b"refused") {
                return Err(io::ErrorKind::BrokenPipe.into());
            }
            self.0.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// A line the sink refuses is lost as surely as one a full queue dropped, so it is counted
    /// and the drain states it.
    #[test]
    fn a_line_the_sink_refuses_is_counted_and_stated() {
        let out = Arc::new(Mutex::new(Vec::new()));
        let reporter = Reporter::new(RefusingSink(Arc::clone(&out)), 4);
        reporter.send("refused\n".into());
        reporter.send("kept\n".into());
        reporter.drain(Duration::from_secs(10));
        let text = String::from_utf8(out.lock().unwrap().clone()).unwrap();
        assert_eq!(
            text,
            "kept\nxmustard run: 1 decision report(s) not written to stderr\n"
        );
    }

    /// A full queue prevents the loss notice from being queued; the bounded worker wait keeps a
    /// broken enqueue from hanging the test thread.
    #[test]
    fn a_drain_keeps_loss_until_its_notice_is_queued() {
        let (sink, entered, release, out) = gated_sink();
        let reporter = Arc::new(Reporter::new(sink, 1));
        reporter.send("writer-held\n".into());
        entered
            .recv_timeout(Duration::from_secs(10))
            .expect("the writer never started the held line");
        reporter.send("queue-full\n".into());
        reporter.send("lost\n".into());
        assert_eq!(reporter.lost.load(Ordering::SeqCst), 1);
        assert!(
            matches!(
                reporter.lines.try_send(Report {
                    line: "probe\n".into(),
                    ack: false,
                }),
                Err(mpsc::TrySendError::Full(_))
            ),
            "the writer queue was not full while its first write was held"
        );

        let (done, finished) = mpsc::channel();
        let stating = Arc::clone(&reporter);
        thread::spawn(move || {
            stating.state_loss(std::time::Instant::now() + Duration::from_millis(100));
            let _ = done.send(());
        });
        let waited = finished.recv_timeout(Duration::from_secs(10));
        release.send(()).unwrap();
        waited.expect("state_loss blocked past its deadline");
        assert_eq!(reporter.lost.load(Ordering::SeqCst), 1);

        reporter.drain(Duration::from_secs(10));
        let text = String::from_utf8(out.lock().unwrap().clone()).unwrap();
        assert!(
            text.ends_with("xmustard run: 1 decision report(s) not written to stderr\n"),
            "the later drain did not state the retained loss count: {text:?}"
        );
    }

    /// A drain that runs out of time before it can queue the loss notice keeps the count, so a
    /// later drain still states it. The first drain runs on its own thread and is awaited with a
    /// deadline, so a drain that waited on the stuck writer fails instead of hanging.
    #[test]
    fn a_drain_out_of_time_keeps_the_count_for_the_next_one() {
        let (sink, entered, release, out) = gated_sink();
        let reporter = Arc::new(Reporter::new(sink, 1));
        reporter.send("first\n".into());
        entered
            .recv_timeout(Duration::from_secs(10))
            .expect("the writer never started the first line");
        reporter.send("queued\n".into());
        reporter.send("dropped\n".into());
        let (done, drained) = mpsc::channel();
        let draining = Arc::clone(&reporter);
        thread::spawn(move || {
            draining.drain(Duration::from_millis(100));
            let _ = done.send(());
        });
        let waited = drained.recv_timeout(Duration::from_secs(10));
        release.send(()).unwrap();
        waited.expect("the drain waited on a stuck writer past its limit");
        reporter.drain(Duration::from_secs(10));
        let text = String::from_utf8(out.lock().unwrap().clone()).unwrap();
        assert_eq!(
            text,
            "first\nqueued\nxmustard run: 1 decision report(s) not written to stderr\n"
        );
    }

    /// The wrapper's exit drains the reporter before it ends: with a decision held in progress,
    /// `end_like` cannot return before the drain's limit has passed. A lower bound only, so load
    /// can never make it fail; an exit that skipped the drain returns at once.
    #[test]
    fn ending_like_the_child_drains_the_reporter_first() {
        let deciding = reporter().deciding();
        let started = std::time::Instant::now();
        let code = end_like(ChildExit::Code(5));
        let waited = started.elapsed();
        drop(deciding);
        assert_eq!(code, 5);
        assert!(
            waited >= REPORT_DRAIN,
            "end_like returned after {waited:?} with a decision in progress, so it did not drain"
        );
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
    fn command_hint_of_a_shell_invocation_names_the_command_not_the_shell() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(command_hint(&a(&["bash", "-c", "git diff"])), "git diff");
        assert_eq!(command_hint(&a(&["sh", "-c", "go test ./..."])), "go test");
        assert_eq!(
            command_hint(&a(&["/bin/bash", "-c", "cargo clippy --all"])),
            "cargo clippy"
        );
        // Nothing past the second word is taken, so a token later in the line never
        // reaches the hint.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "git diff --stat"])),
            "git diff"
        );
        // The registry classifies EVERY simple command of the line
        // (`commandFamily`: split on `;`, `&&`, `||`, `|`, most specific family
        // wins), so a leading `cd` does not decide the hint. Reading only the first
        // word sent `cd`, which has no family of its own, and the capture lost the
        // reduction this whole path exists to enable.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "cd sub && git diff"])),
            "git diff"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "cd sub && go test ./..."])),
            "go test"
        );
        // Every separator the registry splits on, and the precedence between them.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "cd a; npm run test"])),
            "npm run test"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "ls || go build ./..."])),
            "go build"
        );
        // The registry's familyRank decides the winner, with its strict `>` keeping the
        // first equal-family command. The selected hint must classify to the same family.
        for (line, expected_rank) in [
            ("go test && npm run build", 10),
            ("npm run build && go test", 10),
            ("git diff && go test", 10),
            ("cat main.go | grep func", 6),
            ("cd sub\ngo test ./...", 10),
            ("FOO=bar sudo timeout 5s python -m pytest -q", 10),
            ("python3.11 -m pytest -q", 10),
            ("/usr/bin/pytest -q", 10),
            ("git log --patch", 7),
            ("git status --short", 2),
            ("git grep pattern", 6),
            ("rg --files", 4),
            ("tail -f service.log", 5),
            ("node --test tests/*.js", 10),
            ("docker compose logs -f api", 5),
            ("podman logs api", 5),
            ("oc logs pod", 5),
            ("kubectl -n ns logs pod", 5),
            ("g++ main.cpp", 9),
            ("clang++ main.cpp", 9),
        ] {
            let hint = command_hint(&a(&["bash", "-c", line]));
            let want_rank = split_simple_commands(line)
                .into_iter()
                .map(|simple| argv_family_rank(&command_words(simple)))
                .max()
                .unwrap_or(0);
            assert_eq!(want_rank, expected_rank, "registry family rank for {line}");
            assert_eq!(
                argv_family_rank(&command_words(&hint)),
                want_rank,
                "{line} -> {hint}"
            );
        }
        for (line, want) in [
            ("node --test tests/*.js", "node --test"),
            ("docker compose logs -f api", "docker logs"),
            ("podman logs api", "podman logs"),
            ("oc logs pod", "oc logs"),
            ("kubectl -n ns logs pod", "kubectl logs"),
            ("g++ main.cpp", "g++"),
            ("clang++ main.cpp", "clang++"),
        ] {
            assert_eq!(command_hint(&a(&["bash", "-c", line])), want, "{line}");
        }
        // The registry keeps the first command when both have the same family.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "go test && git diff"])),
            "go test"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "git diff && go test"])),
            "go test"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "cat main.go | grep func"])),
            "grep",
            "a single pipe splits commands and grep outranks read"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "cd sub\ngo test ./..."])),
            "go test",
            "newline splits commands"
        );
        assert_eq!(
            command_hint(&a(&[
                "bash",
                "-c",
                "FOO=bar sudo timeout 5s python -m pytest -q"
            ])),
            "pytest",
            "leading assignments and command wrappers are removed"
        );
        assert_eq!(
            command_hint(&a(&["bash", "-c", "echo hi && cd x"])),
            "echo",
            "a line of only rule-less commands names the first one, which is the \
             same answer a single such command gets"
        );
        // A separator inside quotes is not a separator, so the line stays one
        // command and no word that fails `plain` reaches the hint.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "echo 'a && go test'"])),
            "echo",
            "a quoted && is text, not a separator"
        );
        assert_eq!(command_hint(&a(&["bash", "-c", "ls"])), "ls");
        assert_eq!(command_hint(&a(&["bash", "-c", ""])), "bash");
        assert_eq!(command_hint(&a(&["bash", "-c"])), "bash");
        assert_eq!(command_hint(&a(&["bash"])), "bash");
    }

    #[test]
    fn command_hint_sends_only_words_needed_to_preserve_the_registry_family() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        // Unknown subcommands are omitted; only the registry's recognized family is
        // carried in the hint, not a possible target or secret argument.
        for (program, want) in [
            ("go", "go"),
            ("git", "git"),
            ("cargo", "cargo"),
            ("docker", "docker"),
        ] {
            assert_eq!(
                command_hint(&a(&["bash", "-c", &format!("{program} sub")])),
                want,
                "{program} sub"
            );
        }
        assert_eq!(
            command_hint(&a(&["bash", "-c", "npm sub"])),
            "npm",
            "an unknown package-manager command must not expose its argument"
        );
        // Everything else: the first word is the whole hint. An argument is not a
        // family selector, and `echo hunter2` must not put hunter2 in the query.
        for (command, want) in [
            ("echo hunter2", "echo"),
            ("curl https://h/?k=secret", "curl"),
            ("pytest -k secret", "pytest"),
            ("ls -la /home/user", "ls"),
            ("sed -i s/a/b/ f", "sed"),
        ] {
            assert_eq!(
                command_hint(&a(&["bash", "-c", command])),
                want,
                "{command}"
            );
        }
        // A word holding a redirect, a glob or a substitution is never a plain word.
        assert_eq!(
            command_hint(&a(&["bash", "-c", "go test ./..."])),
            "go test"
        );
        assert_eq!(command_hint(&a(&["bash", "-c", "go >out"])), "go");
    }

    #[test]
    fn command_hint_reads_a_dash_c_script_only_for_a_shell() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(command_hint(&a(&["python", "-c", "hunter2 = 1"])), "python");
        assert_eq!(
            command_hint(&a(&["/usr/bin/node", "-c", "git diff"])),
            "node"
        );
        assert_eq!(command_hint(&a(&["zsh", "-c", "git diff"])), "git diff");
    }

    #[test]
    fn command_hint_keeps_what_the_registry_needs_for_scripts_and_targets() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        for (command, want) in [
            ("npm run test", "npm run test"),
            ("pnpm run lint --fix", "pnpm run lint"),
            ("npm run", "npm run"),
            ("npm test", "npm test"),
            ("yarn test:unit", "yarn test"),
            ("pnpm lint:fix", "pnpm lint"),
            ("bun build:prod", "bun build"),
            ("yarn deploy-hunter2", "yarn"),
            ("make test", "make test"),
            ("make -j4 check-backend", "make check"),
            ("make lint", "make lint"),
            ("make deploy-hunter2", "make build"),
            ("gradle build", "gradle build"),
        ] {
            assert_eq!(
                command_hint(&a(&["bash", "-c", command])),
                want,
                "{command}"
            );
        }
    }

    #[test]
    fn a_run_script_hint_carries_its_family_and_never_the_script_name() {
        // r6 (codex-spec medium): the registry reads the script only to classify it
        // (registry.go `HasPrefix(script, "test")`, and the same for lint and build), so
        // the family word is all it needs. A script named after a secret must not reach
        // the capture URL, which is what sending the name verbatim allowed.
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let hint = |command: &str| command_hint(&a(&["bash", "-c", command]));
        for (command, want) in [
            ("npm run test", "npm run test"),
            ("npm run test:unit", "npm run test"),
            ("npm run e2e", "npm run test"),
            ("npm run vitest", "npm run test"),
            ("npm run lint:fix", "npm run lint"),
            ("npm run eslint", "npm run lint"),
            ("npm run build", "npm run build"),
            ("npm run tsc", "npm run build"),
            // r7: only `test`, `e2e`, `lint` and `build` are prefix tests in the
            // registry; the rest are exact, so a prefix match would claim a family the
            // registry does not — `tsc` is a build there and `t` is not a prefix of it,
            // while `jest` is a test there.
            ("npm run jest", "npm run test"),
            ("npm run tscheck", "npm run"),
            // npm's own variant naming uses a colon, and the registry classifies these
            // by prefix (`registry.go`: `HasPrefix(script, "test")`). Reading the script
            // through `plain`, which refuses a colon, sent a bare `npm run` for all
            // three — the generic package-manager reducer, so the capture landed in no
            // family at all.
            ("npm run build:prod", "npm run build"),
            ("npm run lint:fix", "npm run lint"),
            // A script in none of the registry's families sends nothing, because the
            // registry's own fallback for an unknown script is the generic reducer.
            ("npm run deploy", "npm run"),
            ("npm run hunter2", "npm run"),
            ("npm run TEST", "npm run test"),
        ] {
            assert_eq!(hint(command), want, "{command}");
        }
        // The shape a secret takes: a plain word that is not a family name.
        assert!(!hint("npm run hunter2").contains("hunter2"));
        assert!(!hint("npm run s3cret-token").contains("s3cret"));
    }

    #[test]
    fn the_wrapper_default_api_is_the_one_the_adapter_refuses_against() {
        // commandwrap.ts WRAPPER_DEFAULT_API is a second constant in another language.
        // If the two drift, the adapter compares against an endpoint the wrapper does
        // not use and wraps a call the wrapper sends somewhere else.
        assert_eq!(DEFAULT_API, "http://127.0.0.1:8042");
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
