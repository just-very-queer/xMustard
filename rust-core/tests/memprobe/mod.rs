//! Memory probes for the integration tests' RSS guards.
//!
//! A guard bounds two readings of the process under test (WS-16's all-packs test set
//! the precedent, WS-FIX-06 applied it to the other guards):
//!
//! - `dirty`: the process's own writable memory, the memory a budget line protects.
//!   Linux: `RssAnon + RssShmem` from `/proc/<pid>/status`, the file the process itself
//!   reads as `/proc/self/status`. macOS: the physical footprint.
//! - `rss`: the resident set size (ps-RSS). It also counts the clean, file-backed pages
//!   of the binary: its code and grammar tables. A debug binary carries several times
//!   the code of a release one, so a debug run's RSS moves with code size and with how
//!   much of the binary sits in the page cache. On the Linux build box the 5,000-file
//!   index build peaks at 24-25 MiB RSS in a debug build and 16-17 MiB in a release
//!   build; its own anonymous memory is 4.3 and 3.9 MiB.
//!
//! The budget's RSS lines (the 25 MiB heavy slot, the index service's 15 MiB) are lines
//! for the release binary; see [`rss_line`].

// each test crate reads a different part of this module
#![allow(dead_code)]

use std::os::unix::process::ExitStatusExt;
use std::process::{Command, ExitStatus};
use std::time::Duration;

pub const MIB: f64 = (1u64 << 20) as f64;

pub fn mib(bytes: u64) -> f64 {
    bytes as f64 / MIB
}

/// The RSS bound for the binary under test. `cargo test` builds the binary in the
/// test's own profile, so a release run (`cargo test --release`) asserts `release`, the
/// budget's line. A debug run asserts the looser `debug` bound, because debug code pages
/// count in RSS; there the strict bound is the dirty one.
pub const fn rss_line(release: f64, debug: f64) -> f64 {
    if cfg!(debug_assertions) {
        debug
    } else {
        release
    }
}

/// One reading of a live process, in bytes.
#[derive(Clone, Copy, Debug, Default)]
pub struct Mem {
    /// Resident set size now.
    pub rss: u64,
    /// The highest RSS the kernel recorded (Linux VmHWM). macOS keeps none: `rss`.
    pub rss_peak: u64,
    /// Own dirty memory now.
    pub dirty: u64,
    /// The highest dirty memory the kernel recorded (macOS: the lifetime maximum
    /// footprint). Linux keeps none: `dirty`, so a peak is the maximum over samples.
    pub dirty_peak: u64,
}

#[cfg(target_os = "linux")]
pub fn sample(pid: u32) -> Option<Mem> {
    let status = std::fs::read_to_string(format!("/proc/{pid}/status")).ok()?;
    let field = |name: &str| {
        let kib = status.lines().find_map(|l| l.strip_prefix(name))?;
        let kib = kib.trim().strip_suffix("kB")?.trim().parse::<u64>().ok()?;
        Some(kib * 1024)
    };
    let dirty = field("RssAnon:")? + field("RssShmem:")?;
    Some(Mem {
        rss: field("VmRSS:")?,
        rss_peak: field("VmHWM:")?,
        dirty,
        dirty_peak: dirty,
    })
}

#[cfg(target_os = "macos")]
pub fn sample(pid: u32) -> Option<Mem> {
    // SAFETY: a zeroed rusage_info_v4 is a valid out-parameter for RUSAGE_INFO_V4.
    let mut info: libc::rusage_info_v4 = unsafe { std::mem::zeroed() };
    let rc = unsafe {
        libc::proc_pid_rusage(
            pid as libc::pid_t,
            libc::RUSAGE_INFO_V4,
            &mut info as *mut _ as *mut libc::rusage_info_t,
        )
    };
    (rc == 0).then_some(Mem {
        rss: info.ri_resident_size,
        rss_peak: info.ri_resident_size,
        dirty: info.ri_phys_footprint,
        dirty_peak: info.ri_lifetime_max_phys_footprint,
    })
}

#[cfg(not(any(target_os = "linux", target_os = "macos")))]
pub fn sample(_pid: u32) -> Option<Mem> {
    None
}

/// Peaks of one child run, in bytes.
pub struct Peaks {
    /// The child's own dirty peak: exact on macOS, sampled every millisecond on Linux
    /// (a lower bound).
    pub dirty: u64,
    /// The child's own RSS peak: VmHWM on Linux, sampled on macOS.
    pub rss: u64,
    /// The wait4(2) maximum, which also covers the child's own children (the `git
    /// ls-files` an index build streams from).
    pub tree_rss: u64,
    pub status: ExitStatus,
}

/// Run `cmd` to completion, sampling the child while it runs.
pub fn child_peaks(cmd: &mut Command) -> Peaks {
    let child = cmd.spawn().unwrap();
    let pid = child.id();
    let (mut dirty, mut rss) = (0, 0);
    let mut status: libc::c_int = 0;
    // SAFETY: a zeroed rusage is a valid out-parameter; `pid` is our unreaped child until
    // wait4 returns it.
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    loop {
        if let Some(m) = sample(pid) {
            dirty = dirty.max(m.dirty_peak);
            rss = rss.max(m.rss_peak);
        }
        let rc = unsafe { libc::wait4(pid as libc::pid_t, &mut status, libc::WNOHANG, &mut ru) };
        if rc == pid as libc::pid_t {
            break;
        }
        assert_eq!(rc, 0, "wait4 failed");
        std::thread::sleep(Duration::from_millis(1));
    }
    std::mem::forget(child);
    // ru_maxrss is in bytes on macOS and in KiB on Linux
    let unit = if cfg!(target_os = "macos") { 1 } else { 1024 };
    Peaks {
        dirty,
        rss,
        tree_rss: ru.ru_maxrss as u64 * unit,
        status: ExitStatus::from_raw(status),
    }
}
