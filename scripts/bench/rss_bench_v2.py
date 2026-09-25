"""Parity-scale budget gate v2 (PAR-EVAL-05, PAR-EVAL-08; bench side of PAR-EVAL-04).

`rss_bench.py` (v1) is frozen at 6411f391. This module is its successor; it measures and
reports, and it leaves v1 untouched.

Gate. ps-RSS stays the gate for continuity with v1: one `ps -axo pid=,ppid=,rss=,comm=`
snapshot every 100 ms, summed over the xMustard-owned process tree. The limit is
100,000,000 bytes (95.4 MiB). A run counts only when the sampler took samples, every ps
call succeeded, every registered root was present in every snapshot, and the whole
workload completed with its mandatory checks. A low number from a broken run is INVALID,
never PASS.

Alongside the gate, per process:
  * footprint: phys_footprint on macOS (proc_pid_rusage), PSS and USS on Linux
    (/proc/<pid>/smaps_rollup);
  * anon versus file-backed resident memory: RssAnon/RssFile/RssShmem on Linux; on macOS a
    VM region walk (proc_pidinfo PROC_PIDREGIONPATHINFO) at each new tree peak;
  * the unsampled per-process peak (macOS ri_lifetime_max_phys_footprint, Linux VmHWM);
  * storage bytes read and written (macOS ri_diskio_*, Linux /proc/<pid>/io).

Attribution. Every process is assigned a component through the process-role registry in
budget_ledger.json: registered roots carry their role; descendants are classified by
executable name. Processes that match an external rule (LSP servers, compilers, tests,
shells run for the runner) and everything below them go on separate external lines and
are never counted in the gate. Unknown descendants of owned processes count as owned
(`unclassified:<exe>`), so nothing is excluded silently. Agent roots (here the bench
harness) are external; only xMustard binaries launched below them are pulled in as owned.

Scenarios. `v1-workload` runs the frozen v1 workload verbatim (v1's own main()) with the
v2 sampler running beside v1's sampler, so one run yields both numbers. Parity-scale
scenarios run over pinned Apache/MIT fixtures (parity_fixtures.json): 1, 2 and 4 agents,
queries during a reindex, a snapshot swap under load, captures during indexing, two hot
repos, four agents in four worktrees, and the watcher. A scenario whose product feature
is absent at this HEAD is reported as not run, with the missing requirement named; it is
never reported as passed.

Tool accounting (PAR-EVAL-04, bench side). Every MCP call the harness makes is recorded:
tool, argument bytes, request and response wire bytes, result content bytes, estimated
tokens (bytes/4 heuristic, not a tokenizer), latency, and the work counters the result
reports. The API's own counters (/api/health budget.counters, when present) are recorded
as before/after deltas.

Nothing here talks to a model provider.
"""

import argparse
import collections
import ctypes
import ctypes.util
import datetime
import hashlib
import http.client
import json
import math
import os
import platform
import shutil
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "e2e"))
sys.path.insert(0, HERE)
from harness import REPO_ROOT, Api, Checks, Mcp, run, sha_file  # noqa: E402

GATE_BYTES = 100_000_000
GATE_MIB = round(GATE_BYTES / 2**20, 1)
INTERVAL_S = 0.1
MIB = 2**20
LEDGER_PATH = os.path.join(HERE, "budget_ledger.json")
FIXTURES_PATH = os.path.join(HERE, "parity_fixtures.json")
PS_CMD = ["ps", "-axo", "pid=,ppid=,rss=,comm="]  # identical to v1
TOKEN_BASIS = "ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded"


def mib(n_bytes, nd=1):
    return None if n_bytes is None else round(n_bytes / MIB, nd)


# --------------------------------------------------------------------------------------
# Process table and attribution (pure; unit-tested on synthetic snapshots)
# --------------------------------------------------------------------------------------

Proc = collections.namedtuple("Proc", "pid ppid rss_kib comm")
Root = collections.namedtuple("Root", "label pid component klass")  # klass: owned | agent


def parse_ps(text):
    """Parse `ps -axo pid=,ppid=,rss=,comm=` exactly as v1 does.

    Returns (procs, children). Raises ValueError on any unparsable line (v1 records that
    as a sampler error and drops the snapshot).
    """
    procs, children = {}, {}
    for line in text.splitlines():
        f = line.split(None, 3)
        try:
            pid, ppid, rss = int(f[0]), int(f[1]), int(f[2])
        except (ValueError, IndexError):
            raise ValueError(f"unparsable ps line {line[:120]!r}")
        procs[pid] = Proc(pid, ppid, rss, os.path.basename(f[3]) if len(f) > 3 else "?")
        children.setdefault(ppid, []).append(pid)
    return procs, children


def exe_name(comm):
    """Executable name used for classification: ps shows exited-but-unreaped macOS
    processes as `(name)`, and ones whose name is gone as `<defunct>`."""
    if len(comm) > 2 and comm.startswith("(") and comm.endswith(")"):
        return comm[1:-1]
    return comm


class RoleRegistry:
    """Maps processes to components from the ledger's `process_roles` section."""

    def __init__(self, spec):
        self.rules = spec["rules"]
        self.owned_binaries = list(spec["owned_binaries"])
        for r in self.rules:
            if r["class"] not in ("owned", "external"):
                raise ValueError(f"rule {r} has class {r['class']!r}; want owned or external")

    @staticmethod
    def _matches(names, comm):
        # Linux truncates the task comm to 15 bytes (typescript-language-server -> typescript-lang)
        return comm in names or (len(comm) == 15 and any(n.startswith(comm) for n in names))

    def needs_argv(self, comm):
        name = exe_name(comm)
        return any(r.get("argv1") and self._matches(r["exe"], name) for r in self.rules)

    def classify(self, comm, argv1=None):
        name = exe_name(comm)
        if name == "<defunct>":  # an exited child awaiting its parent's wait(): no memory left
            return "defunct", "owned"
        for r in self.rules:
            if not self._matches(r["exe"], name):
                continue
            if r.get("argv1") and argv1 not in r["argv1"]:
                continue
            return r["component"], r["class"]
        return f"unclassified:{name}", "owned"

    def is_owned_binary(self, comm):
        return self._matches(self.owned_binaries, exe_name(comm))


def attribute(procs, children, roots, registry, argv1_of=lambda pid: None):
    """Classify every process under the registered roots.

    Returns (missing_root_labels, rows) with rows = {pid: (component, klass, root_label)}
    and klass in owned | external | agent.
      * An owned root and its descendants are owned, except a descendant that matches an
        external rule: it and everything below it are external.
      * An agent root is reported on its own line. Below it, only xMustard binaries are
        pulled in (as owned sub-roots, for example a hook client or shim the agent
        launched); the rest of an agent's tree is the agent's own and is not attributed.
    """
    missing = [r.label for r in roots if r.pid not in procs]
    rows = {}

    def argv1(pid):
        return argv1_of(pid) if registry.needs_argv(procs[pid].comm) else None

    def walk(start, comp, klass, label):
        stack = [(start, comp, klass)]
        while stack:
            pid, c, k = stack.pop()
            if pid in rows:
                continue
            rows[pid] = (c, k, label)
            for ch in children.get(pid, ()):
                if ch in rows or ch not in procs:
                    continue
                cc, ck = registry.classify(procs[ch].comm, argv1(ch))
                if k == "external":  # externals stay external; keep a more specific external name
                    stack.append((ch, cc if ck == "external" else c, "external"))
                elif ck == "owned" and exe_name(procs[ch].comm) == exe_name(procs[pid].comm):
                    # a child still running its parent's image: forked, not yet exec'd. It stays
                    # in the gate (ps counts its copy-on-write pages again) under its own name.
                    stack.append((ch, "fork_pre_exec", "owned"))
                else:
                    stack.append((ch, cc, ck))

    for r in roots:
        if r.klass == "owned" and r.pid in procs:
            walk(r.pid, r.component, "owned", r.label)
    for r in roots:
        if r.klass != "agent" or r.pid not in procs:
            continue
        stack, seen = list(children.get(r.pid, ())), set()
        while stack:
            q = stack.pop()
            if q in seen or q in rows or q not in procs:
                continue
            seen.add(q)
            if registry.is_owned_binary(procs[q].comm):
                comp, _ = registry.classify(procs[q].comm, argv1(q))
                walk(q, comp, "owned", f"{r.label}>{exe_name(procs[q].comm)}")
            else:
                stack.extend(children.get(q, ()))
        rows.setdefault(r.pid, (r.component, "agent", r.label))
    return missing, rows


def snapshot_totals(procs, rows):
    """Synchronized sums for one snapshot: gate (owned), per owned component, per
    external component, per agent line, and owned+external (the v1-equivalent tree)."""
    gate = 0
    comp, ext, agents = collections.Counter(), collections.Counter(), collections.Counter()
    for pid, (c, k, label) in rows.items():
        rss = procs[pid].rss_kib
        if k == "owned":
            gate += rss
            comp[c] += rss
        elif k == "external":
            ext[c] += rss
        else:
            agents[label] += rss
    return {"gate_kib": gate, "tree_all_kib": gate + sum(ext.values()), "components_kib": dict(comp),
            "externals_kib": dict(ext), "agents_kib": dict(agents)}


# --------------------------------------------------------------------------------------
# Per-process memory probes
# --------------------------------------------------------------------------------------

RUSAGE_INFO_V4 = 4
RUSAGE_V4_FIELDS = 35  # uint64 fields after the 16-byte uuid
TASK_VM_INFO = 22
TASK_VM_INFO_STRUCT = "<QiiQQQQQQQQQQQQQQQQQQQq"  # struct task_vm_info up to ledger_phys_footprint_peak (pack 4)
TASK_VM_INFO_BYTES = struct.calcsize(TASK_VM_INFO_STRUCT)


def unpack_rusage_v4(buf):
    """Decode struct rusage_info_v4 (sys/resource.h) into the fields v2 reports."""
    v = struct.unpack_from(f"<{RUSAGE_V4_FIELDS}Q", buf, 16)
    return {"resident_bytes": v[6], "footprint_bytes": v[7], "io_read_bytes": v[16],
            "io_written_bytes": v[17], "lifetime_max_footprint_bytes": v[28]}


def unpack_task_vm_info(buf):
    """Decode struct task_vm_info (mach/task_info.h, TASK_VM_INFO). internal is anonymous
    resident memory and external is file-backed resident memory; together they are the
    task's resident size, which is what ps reports as RSS."""
    v = struct.unpack_from(TASK_VM_INFO_STRUCT, buf, 0)
    return {"resident_bytes": v[3], "hwm_bytes": v[4], "anon_bytes": v[7], "anon_peak_bytes": v[8],
            "file_bytes": v[9], "file_peak_bytes": v[10], "compressed_bytes": v[16], "footprint_bytes": v[19]}


def parse_kv_kib(text, keys=None):
    """`Key:   123 kB` lines (/proc/<pid>/status, smaps_rollup) -> {Key: bytes}."""
    out = {}
    for line in text.splitlines():
        k, _, rest = line.partition(":")
        parts = rest.split()
        if len(parts) == 2 and parts[1] == "kB" and (keys is None or k in keys):
            try:
                out[k] = int(parts[0]) * 1024
            except ValueError:
                pass
    return out


def parse_proc_io(text):
    out = {}
    for line in text.splitlines():
        k, _, v = line.partition(":")
        try:
            out[k.strip()] = int(v)
        except ValueError:
            pass
    return out


def linux_stats(status_text, rollup_text, io_text):
    """Linux per-process metrics from /proc/<pid>/{status,smaps_rollup,io} text."""
    st = parse_kv_kib(status_text, {"VmRSS", "RssAnon", "RssFile", "RssShmem", "VmHWM"})
    out = {"resident_bytes": st.get("VmRSS"), "anon_bytes": st.get("RssAnon"), "file_bytes": st.get("RssFile"),
           "shmem_bytes": st.get("RssShmem"), "hwm_bytes": st.get("VmHWM")}
    if rollup_text:
        ro = parse_kv_kib(rollup_text)
        out["footprint_bytes"] = ro.get("Pss")
        out["pss_anon_bytes"], out["pss_file_bytes"] = ro.get("Pss_Anon"), ro.get("Pss_File")
        if "Private_Clean" in ro or "Private_Dirty" in ro:
            out["uss_bytes"] = ro.get("Private_Clean", 0) + ro.get("Private_Dirty", 0)
        out["swap_bytes"] = ro.get("Swap")
    if io_text:
        io = parse_proc_io(io_text)
        out["io_read_bytes"], out["io_written_bytes"] = io.get("read_bytes"), io.get("write_bytes")
        out["io_rchar"], out["io_wchar"] = io.get("rchar"), io.get("wchar")
    return out


class DarwinProbe:
    name = "darwin"
    basis = {
        "footprint": "phys_footprint (task_info TASK_VM_INFO through task_name_for_pid)",
        "split": "TASK_VM_INFO internal (anonymous) and external (file-backed) resident bytes, every sample; they sum to the RSS ps reports",
        "process_peak": "TASK_VM_INFO resident_size_peak (RSS) and rusage lifetime max phys_footprint, both unsampled per process",
        "io": "ri_diskio_bytesread / ri_diskio_byteswritten (cumulative per process, last observation)",
    }

    def __init__(self):
        self.lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        self.libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
        self.self_task = ctypes.c_uint.in_dll(self.libc, "mach_task_self_").value
        self.rbuf = ctypes.create_string_buffer(512)
        self.vbuf = (ctypes.c_uint * 128)()
        self.argmax = 1 << 20
        self.abuf = ctypes.create_string_buffer(self.argmax)

    def _vm_info(self, pid):
        port = ctypes.c_uint(0)
        if self.libc.task_name_for_pid(self.self_task, ctypes.c_int(pid), ctypes.byref(port)) != 0:
            return None
        try:
            count = ctypes.c_uint(len(self.vbuf))
            if self.libc.task_info(port, TASK_VM_INFO, self.vbuf, ctypes.byref(count)) != 0 or count.value * 4 < TASK_VM_INFO_BYTES:
                return None
            return unpack_task_vm_info(ctypes.string_at(self.vbuf, TASK_VM_INFO_BYTES))
        finally:
            self.libc.mach_port_deallocate(self.self_task, port)

    def stats(self, pid):
        vm = self._vm_info(pid)
        ok = self.lib.proc_pid_rusage(ctypes.c_int(pid), ctypes.c_int(RUSAGE_INFO_V4), self.rbuf) == 0
        ru = unpack_rusage_v4(self.rbuf.raw) if ok else None
        if vm is None and ru is None:
            return None
        out = dict(ru or {})
        out.update(vm or {})
        return out

    def argv1(self, pid):
        """argv[1] via sysctl KERN_PROCARGS2 (int argc, exec path, NUL padding, argv...)."""
        mib_ = (ctypes.c_int * 3)(1, 49, pid)  # CTL_KERN, KERN_PROCARGS2
        size = ctypes.c_size_t(self.argmax)
        if self.libc.sysctl(mib_, 3, self.abuf, ctypes.byref(size), None, ctypes.c_size_t(0)) != 0:
            return None
        return parse_procargs2(ctypes.string_at(self.abuf, min(size.value, 4096)))


def parse_procargs2(raw):
    if len(raw) < 4:
        return None
    argc = struct.unpack_from("<i", raw, 0)[0]
    rest = raw[4:]
    i = rest.find(b"\0")  # end of exec path
    if i < 0:
        return None
    while i < len(rest) and rest[i] == 0:
        i += 1
    args = rest[i:].split(b"\0")[:argc]
    return args[1].decode(errors="replace") if len(args) > 1 else None


class LinuxProbe:
    name = "linux"
    basis = {
        "footprint": "PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty",
        "split": "RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample",
        "process_peak": "VmHWM (unsampled, per process)",
        "io": "/proc/<pid>/io read_bytes / write_bytes (cumulative per process, last observation)",
    }

    @staticmethod
    def _read(path):
        try:
            with open(path) as f:
                return f.read()
        except OSError:
            return None

    def stats(self, pid):
        status = self._read(f"/proc/{pid}/status")
        if status is None:
            return None
        return linux_stats(status, self._read(f"/proc/{pid}/smaps_rollup"), self._read(f"/proc/{pid}/io"))

    def argv1(self, pid):
        raw = self._read(f"/proc/{pid}/cmdline")
        parts = (raw or "").split("\0")
        return parts[1] if len(parts) > 1 and parts[1] else None


class NullProbe:
    name = "none"
    basis = {"footprint": "unavailable on this platform", "split": "unavailable", "process_peak": "unavailable",
             "io": "unavailable"}

    def stats(self, pid):
        return None

    def argv1(self, pid):
        return None


def default_probe():
    try:
        if sys.platform == "darwin":
            return DarwinProbe()
        if sys.platform.startswith("linux"):
            return LinuxProbe()
    except OSError:
        pass
    return NullProbe()


# --------------------------------------------------------------------------------------
# Sampler
# --------------------------------------------------------------------------------------

def _pct(values, q):
    if not values:
        return None
    s = sorted(values)
    return s[min(len(s) - 1, int(len(s) * q))]


class SamplerV2(threading.Thread):
    """Every 100 ms: one ps snapshot -> attribution -> per-process probes -> peaks.

    `ingest()` holds all per-sample logic so unit tests can feed synthetic snapshots and
    a fake probe; `run()` only adds the ps call and the pacing.
    """

    def __init__(self, roots, registry, probe=None, step_ref=None, interval=INTERVAL_S, storage_dir=None):
        super().__init__(daemon=True)
        self.roots = list(roots)
        self.registry = registry
        self.probe = probe or NullProbe()
        self.step_ref = step_ref if step_ref is not None else ["setup"]
        self.interval = interval
        self.storage_dir = storage_dir
        self.stop_ev = threading.Event()
        self.t0 = time.time()
        self.samples, self.errors, self.lost_roots, self.gaps, self.last_t = 0, [], [], [], None
        self.sample_cost_ms = []
        self.peak = {"gate_kib": 0}
        self.tree_all_peak_kib = 0
        self.step_peak_kib = {}
        self.comp_peak_kib, self.comp_values = {}, collections.defaultdict(list)
        self.comp_fp_peak = {}
        self.ext_peak_kib, self.ext_fp_peak, self.agent_peak_kib = {}, {}, {}
        self.fp = {"peak_bytes": 0, "step": None, "t_s": None, "partial_samples": 0}
        self.proc_last = {}  # (pid, exe) -> last stats + component/class
        self.seen = collections.defaultdict(set)  # component -> {(pid, exe)}
        self.max_concurrent = collections.Counter()
        self.pid_peak_kib = {}  # (pid, exe) -> max sampled rss
        self.series = []
        self.argv_cache = {}
        self.storage_peak, self.storage_step = (0, 0, 0), None

    # -- helpers --
    def _argv1(self, pid, comm):
        key = (pid, comm)
        if key not in self.argv_cache:
            self.argv_cache[key] = self.probe.argv1(pid)
        return self.argv_cache[key]

    def _stats(self, pid):
        try:
            return self.probe.stats(pid)
        except (OSError, ValueError, struct.error):
            return None

    # -- per sample --
    def ingest(self, t, step, ps_text):
        try:
            procs, children = parse_ps(ps_text)
        except ValueError as e:
            self.errors.append({"t": round(t - self.t0, 2), "step": step, "error": str(e)})
            return
        missing, rows = attribute(procs, children, self.roots, self.registry,
                                  lambda pid: self._argv1(pid, procs[pid].comm))
        if missing:
            self.lost_roots.append({"t": round(t - self.t0, 2), "step": step, "missing": missing})
            return
        self.samples += 1
        if self.last_t is not None:
            self.gaps.append(t - self.last_t)
        self.last_t = t
        tot = snapshot_totals(procs, rows)
        stats, fp_total, fp_partial = {}, 0, False
        comp_fp, ext_fp = collections.Counter(), collections.Counter()
        concurrent = collections.Counter()
        for pid, (c, k, label) in rows.items():
            exe = exe_name(procs[pid].comm)
            key = (pid, exe)
            st = self._stats(pid)
            stats[pid] = st
            self.seen[c].add(key)
            concurrent[c] += 1
            if procs[pid].rss_kib > self.pid_peak_kib.get(key, -1):
                self.pid_peak_kib[key] = procs[pid].rss_kib
            if st is not None:
                self.proc_last[key] = dict(st, component=c, klass=k)
            fp = (st or {}).get("footprint_bytes")
            if k == "owned":
                if fp is None:
                    fp_partial = True
                else:
                    fp_total += fp
                    comp_fp[c] += fp
            elif k == "external" and fp is not None:
                ext_fp[c] += fp
        for c, n in concurrent.items():
            self.max_concurrent[c] = max(self.max_concurrent[c], n)
        for c, kib in tot["components_kib"].items():
            self.comp_peak_kib[c] = max(self.comp_peak_kib.get(c, 0), kib)
            if c not in self.comp_values:  # absent in every earlier sample
                self.comp_values[c] = [0] * (self.samples - 1)
            self.comp_values[c].append(kib)
        for c in self.comp_values:
            if c not in tot["components_kib"]:
                self.comp_values[c].append(0)
        for c, b in comp_fp.items():
            self.comp_fp_peak[c] = max(self.comp_fp_peak.get(c, 0), b)
        for c, kib in tot["externals_kib"].items():
            self.ext_peak_kib[c] = max(self.ext_peak_kib.get(c, 0), kib)
        for c, b in ext_fp.items():
            self.ext_fp_peak[c] = max(self.ext_fp_peak.get(c, 0), b)
        for label, kib in tot["agents_kib"].items():
            self.agent_peak_kib[label] = max(self.agent_peak_kib.get(label, 0), kib)
        if fp_partial:
            self.fp["partial_samples"] += 1
        if fp_total > self.fp["peak_bytes"]:
            self.fp.update(peak_bytes=fp_total, step=step, t_s=round(t - self.t0, 2), partial=fp_partial)
        self.tree_all_peak_kib = max(self.tree_all_peak_kib, tot["tree_all_kib"])
        self.step_peak_kib[step] = max(self.step_peak_kib.get(step, 0), tot["gate_kib"])
        if tot["gate_kib"] > self.peak["gate_kib"]:
            self._record_peak(t, step, procs, rows, stats, tot, fp_total, fp_partial)
        self.series.append([round(t - self.t0, 2), step, tot["gate_kib"], fp_total // 1024, sum(tot["externals_kib"].values())])
        if self.storage_dir and self.samples % 10 == 0:
            s = dir_bytes(self.storage_dir)
            if s[0] > self.storage_peak[0]:
                self.storage_peak, self.storage_step = s, step

    def _record_peak(self, t, step, procs, rows, stats, tot, fp_total, fp_partial):
        """Keep the per-process detail of a new gate peak: RSS, footprint and the resident
        split (anon + file [+ shmem on Linux] = RSS), read in the same sample."""
        rows_out, split, split_ok = [], collections.Counter(), True
        for pid, (c, k, label) in rows.items():
            if k == "agent":
                continue
            st = stats.get(pid) or {}
            row = {"pid": pid, "ppid": procs[pid].ppid, "comm": procs[pid].comm, "component": c, "class": k,
                   "rss_kib": procs[pid].rss_kib}
            for key in ("footprint_bytes", "anon_bytes", "file_bytes", "shmem_bytes", "compressed_bytes", "uss_bytes",
                        "hwm_bytes", "lifetime_max_footprint_bytes"):
                if st.get(key) is not None:
                    row[key] = st[key]
            if k == "owned":
                if st.get("anon_bytes") is None or st.get("file_bytes") is None:
                    split_ok = False
                    split["unsplit_bytes"] += procs[pid].rss_kib * 1024  # exited or exiting: ps still shows RSS
                else:
                    for key in ("anon_bytes", "file_bytes", "shmem_bytes", "compressed_bytes"):
                        split[key] += st.get(key) or 0
            rows_out.append(row)
        rows_out.sort(key=lambda r: -r["rss_kib"])
        self.peak = {"gate_kib": tot["gate_kib"], "step": step, "t_s": round(t - self.t0, 2), "processes": rows_out,
                     "components_kib": tot["components_kib"], "externals_kib": tot["externals_kib"],
                     "footprint_bytes": fp_total, "footprint_partial": fp_partial,
                     "split": dict(split, complete=split_ok)}

    def sample_once(self):
        t, step = time.time(), self.step_ref[0]
        try:
            p = subprocess.run(PS_CMD, capture_output=True, text=True, timeout=5)
        except (OSError, subprocess.TimeoutExpired) as e:
            self.errors.append({"t": round(t - self.t0, 2), "step": step, "error": repr(e)})
            return
        if p.returncode != 0 or not p.stdout.strip():
            self.errors.append({"t": round(t - self.t0, 2), "step": step, "error": f"ps exit {p.returncode}: {p.stderr.strip()[:200]}"})
            return
        self.ingest(t, step, p.stdout)
        self.sample_cost_ms.append((time.time() - t) * 1000)

    def run(self):
        while not self.stop_ev.is_set():
            t0 = time.time()
            self.sample_once()
            self.stop_ev.wait(max(0.0, self.interval - (time.time() - t0)))

    def stop(self):
        self.stop_ev.set()
        if self.is_alive():
            self.join(10)

    def validity(self):
        problems = []
        if self.samples == 0:
            problems.append("zero samples")
        if self.errors:
            problems.append(f"{len(self.errors)} ps errors")
        if self.lost_roots:
            problems.append(f"required roots missing in {len(self.lost_roots)} snapshots")
        return problems

    def report(self):
        gaps = sorted(self.gaps)
        peak_bytes = self.peak["gate_kib"] * 1024
        comps = {}
        for c in sorted(self.comp_peak_kib):  # owned components only
            vals = self.comp_values.get(c, [])
            keys = self.seen.get(c, set())
            lifetime = [self.proc_last[k].get("lifetime_max_footprint_bytes") or 0 for k in keys if k in self.proc_last]
            hwm = [self.proc_last[k].get("hwm_bytes") or 0 for k in keys if k in self.proc_last]
            io_r = [self.proc_last[k].get("io_read_bytes") for k in keys if k in self.proc_last]
            io_w = [self.proc_last[k].get("io_written_bytes") for k in keys if k in self.proc_last]
            comps[c] = {
                "peak_mib": mib(self.comp_peak_kib.get(c, 0) * 1024), "p50_mib": mib((_pct(vals, 0.5) or 0) * 1024),
                "at_gate_peak_mib": mib(self.peak.get("components_kib", {}).get(c, 0) * 1024),
                "footprint_peak_mib": mib(self.comp_fp_peak.get(c)),
                "process_rss_peak_mib": mib(max(hwm) or None) if hwm else None,
                "process_footprint_peak_mib": mib(max(lifetime) or None) if lifetime else None,
                "processes_seen": len(keys), "max_concurrent": self.max_concurrent.get(c, 0),
                "io_read_bytes": sum(v for v in io_r if v), "io_written_bytes": sum(v for v in io_w if v),
            }
        externals = {c: {"peak_mib": mib(kib * 1024), "footprint_peak_mib": mib(self.ext_fp_peak.get(c)),
                         "processes_seen": len(self.seen.get(c, ()))}
                     for c, kib in sorted(self.ext_peak_kib.items())}
        split = self.peak.get("split") or {}
        return {
            "gate": {"metric": "ps-RSS summed over the xMustard-owned tree in one snapshot (externals excluded)",
                     "peak_bytes": peak_bytes, "peak_mib": mib(peak_bytes), "peak_mb": round(peak_bytes / 1e6, 1),
                     "peak_step": self.peak.get("step"), "peak_t_s": self.peak.get("t_s")},
            "tree_all_peak_mib": mib(self.tree_all_peak_kib * 1024),
            "footprint": {"basis": self.probe.basis["footprint"], "tree_peak_mib": mib(self.fp["peak_bytes"]) if self.fp["peak_bytes"] else None,
                          "tree_peak_step": self.fp["step"], "at_gate_peak_mib": mib(self.peak.get("footprint_bytes")),
                          "at_gate_peak_partial": self.peak.get("footprint_partial"), "partial_samples": self.fp["partial_samples"]},
            "split_at_gate_peak": {"basis": self.probe.basis["split"], "anon_mib": mib(split.get("anon_bytes")),
                                   "file_mib": mib(split.get("file_bytes")), "shmem_mib": mib(split.get("shmem_bytes")),
                                   "compressed_mib": mib(split.get("compressed_bytes")),
                                   "unsplit_mib": mib(split.get("unsplit_bytes", 0)), "complete": split.get("complete"),
                                   "note": "unsplit = RSS of owned processes that exited between ps and the probe"},
            "process_peak_basis": self.probe.basis["process_peak"], "io_basis": self.probe.basis["io"],
            "components": comps,
            "externals": externals,
            "agents": {k: {"peak_mib": mib(v * 1024)} for k, v in sorted(self.agent_peak_kib.items())},
            "processes_at_gate_peak": self.peak.get("processes", []),
            "step_peak_mib": {k: mib(v * 1024) for k, v in self.step_peak_kib.items()},
            "samples": self.samples, "interval_ms_target": int(self.interval * 1000),
            "interval_ms_observed": {"median": round(gaps[len(gaps) // 2] * 1000, 1) if gaps else None,
                                     "max": round(gaps[-1] * 1000, 1) if gaps else None,
                                     "over_150ms": sum(1 for g in gaps if g > 0.15)},
            "sample_cost_ms": {"p50": round(_pct(self.sample_cost_ms, 0.5), 1) if self.sample_cost_ms else None,
                               "max": round(max(self.sample_cost_ms), 1) if self.sample_cost_ms else None},
            "ps_errors": self.errors[:20], "ps_error_count": len(self.errors),
            "lost_root_snapshots": self.lost_roots[:20], "lost_root_count": len(self.lost_roots),
            "roots": [r._asdict() for r in self.roots],
            "probe": self.probe.name,
            "storage_sampled_peak": {"apparent": self.storage_peak[0], "allocated": self.storage_peak[1],
                                     "files": self.storage_peak[2], "step": self.storage_step},
            "spawn_note": "processes_seen counts distinct processes observed at 100 ms; short-lived children can be missed (lower bound)",
            "units": "*_mib = 2^20 bytes, *_mb = 10^6 bytes; ps rss is KiB on darwin and linux",
            "series_t_step_gate_kib_fp_kib_ext_kib": self.series,
        }


def dir_bytes(path):
    """(apparent bytes, allocated bytes, files) under path; missing path is zero."""
    size = alloc = files = 0
    for d, _, names in os.walk(path):
        for n in names:
            try:
                st = os.lstat(os.path.join(d, n))
            except OSError:
                continue
            size += st.st_size
            alloc += st.st_blocks * 512
            files += 1
    return size, alloc, files


def evaluate_gate(sampler_report, invalid_reasons, limit_bytes=GATE_BYTES):
    """The gate verdict: PASS only for a valid run at or under the limit."""
    peak = sampler_report["gate"]["peak_bytes"]
    valid = not invalid_reasons
    verdict = "INVALID" if not valid else ("PASS" if peak <= limit_bytes else "FAIL")
    return {"limit_bytes": limit_bytes, "limit_mib": round(limit_bytes / MIB, 1), "peak_bytes": peak,
            "peak_mib": mib(peak), "valid": valid, "invalid_reasons": list(invalid_reasons),
            "passed": verdict == "PASS", "verdict": verdict}


# --------------------------------------------------------------------------------------
# MCP clients with per-call accounting (PAR-EVAL-04, bench side)
# --------------------------------------------------------------------------------------

class UsageLog:
    """Thread-safe per-call records; `summary()` aggregates per tool."""

    WORK_KEYS = ("files_read", "files_parsed", "files_reused", "bytes_parsed", "symbols_indexed")

    def __init__(self):
        self.lock = threading.Lock()
        self.calls = []

    def record(self, **rec):
        with self.lock:
            self.calls.append(rec)

    def summary(self):
        by = collections.OrderedDict()
        for c in self.calls:
            by.setdefault(c["name"], []).append(c)
        out = {}
        for name, cs in by.items():
            lat = [c["latency_ms"] for c in cs]
            content = sum(c.get("content_bytes") or 0 for c in cs)
            s = {"calls": len(cs), "errors": sum(1 for c in cs if c.get("error")),
                 "arg_bytes": sum(c.get("arg_bytes") or 0 for c in cs),
                 "request_bytes": sum(c.get("request_bytes") or 0 for c in cs),
                 "response_bytes": sum(c.get("response_bytes") or 0 for c in cs),
                 "content_bytes": content, "est_tokens": sum(c.get("est_tokens") or 0 for c in cs),
                 "latency_ms": {"p50": round(_pct(lat, 0.5), 1), "p95": round(_pct(lat, 0.95), 1), "max": round(max(lat), 1)},
                 "transports": sorted({c.get("transport") for c in cs if c.get("transport")})}
            work = collections.Counter()
            for c in cs:
                for k, v in (c.get("work") or {}).items():
                    if isinstance(v, (int, float)) and not isinstance(v, bool):
                        work[k] += v
            if work:
                s["work"] = dict(work)
            meta = [c["meta_usage"] for c in cs if c.get("meta_usage")]
            if meta:
                s["meta_usage_calls"] = len(meta)
            out[name] = s
        return {"token_basis": TOKEN_BASIS, "per_tool": out, "calls_total": len(self.calls),
                "errors_total": sum(1 for c in self.calls if c.get("error"))}


def call_name(method, params):
    if method == "tools/call":
        return (params or {}).get("name", "tools/call")
    return method


def result_accounting(reply):
    """Content bytes, token estimate, work counters and error state of one JSON-RPC reply."""
    out = {"content_bytes": 0, "est_tokens": 0, "work": None, "meta_usage": None, "error": None}
    if not isinstance(reply, dict):
        out["error"] = "no reply"
        return out
    if "error" in reply:
        out["error"] = f"rpc {reply['error'].get('code')}: {str(reply['error'].get('message'))[:160]}"
        return out
    res = reply.get("result") or {}
    texts = [c.get("text") or "" for c in (res.get("content") or []) if isinstance(c, dict)]
    # resources/read returns contents[] with text or a base64 blob; count what is delivered
    resources = [c.get("text") or c.get("blob") or "" for c in (res.get("contents") or []) if isinstance(c, dict)]
    n = sum(len(t.encode()) for t in texts + resources)
    out["content_bytes"] = n
    out["est_tokens"] = math.ceil(n / 4)
    if res.get("isError"):
        out["error"] = f"isError: {(texts[0] if texts else '')[:160]}"
    meta = res.get("_meta") or {}
    if isinstance(meta, dict) and meta.get("usage"):
        out["meta_usage"] = meta["usage"]
    if texts:
        try:
            body = json.loads(texts[0])
        except ValueError:
            body = None
        if isinstance(body, dict):
            work = (body.get("coverage") or {}).get("work") if isinstance(body.get("coverage"), dict) else None
            if isinstance(work, dict):
                out["work"] = {k: work.get(k) for k in UsageLog.WORK_KEYS if k in work}
    return out


class AccountingMcp(Mcp):
    """The harness's stdio MCP client plus exact wire-byte accounting per call."""

    usage = None  # a UsageLog; set by accounting_mcp_class()
    agent = "stdio"

    def __init__(self, *a, **kw):
        super().__init__(*a, **kw)
        self.reply_bytes, self.request_bytes = {}, {}

    def _read(self):
        for line in self.proc.stdout:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            with self.cv:
                key = json.dumps(msg.get("id"))
                self.replies[key] = msg
                self.reply_bytes[key] = len(line)
                self.cv.notify_all()

    def send(self, method, params=None, id_=None):
        if id_ is None:
            id_ = self.next_id
            self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": id_, "method": method}
        if params is not None:
            msg["params"] = params
        line = (json.dumps(msg) + "\n").encode()
        self.request_bytes[json.dumps(id_)] = len(line)
        self.proc.stdin.write(line)
        self.proc.stdin.flush()
        return id_

    def call(self, method, params=None, timeout=120):
        t0 = time.time()
        id_ = self.send(method, params)
        key = json.dumps(id_)
        reply, err = None, None
        try:
            reply = self.wait(id_, timeout)
        except TimeoutError as e:
            err = f"timeout: {e}"
            raise
        finally:
            acc = result_accounting(reply) if reply is not None else {"error": err}
            if self.usage is not None:
                args = (params or {}).get("arguments") if method == "tools/call" else params
                self.usage.record(name=call_name(method, params), transport="stdio", agent=self.agent,
                                  arg_bytes=len(json.dumps(args, separators=(",", ":")).encode()) if args is not None else 0,
                                  request_bytes=self.request_bytes.pop(key, None), response_bytes=self.reply_bytes.pop(key, None),
                                  latency_ms=(time.time() - t0) * 1000, **acc)
        return reply


def accounting_mcp_class(usage, agent="stdio"):
    return type("AccountingMcpBound", (AccountingMcp,), {"usage": usage, "agent": agent})


def parse_sse_messages(body):
    """JSON-RPC messages from a text/event-stream body (data: lines joined per event)."""
    msgs, data = [], []
    for line in body.splitlines() + [""]:
        if line == "":
            if data:
                try:
                    msgs.append(json.loads("\n".join(data)))
                except ValueError:
                    pass
                data = []
        elif line.startswith("data:"):
            data.append(line[5:].lstrip(" "))
    return msgs


class HttpMcp:
    """Minimal MCP Streamable HTTP client (one session per agent) with the same
    accounting as AccountingMcp. Used when the API serves /mcp (PAR-RT-03, WS-13)."""

    def __init__(self, base, path="/mcp", usage=None, agent="http", headers=None):
        u = urllib.parse.urlparse(base)
        self.host, self.port, self.path = u.hostname, u.port, path
        self.usage, self.agent = usage, agent
        self.headers = dict(headers or {})
        self.session = None
        self.protocol = None
        self.next_id = 1
        self.lock = threading.Lock()
        self.proc = None  # no process of its own

    def start(self):
        return self

    def _post(self, payload, timeout):
        body = json.dumps(payload).encode()
        conn = http.client.HTTPConnection(self.host, self.port, timeout=timeout)
        hdrs = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream", **self.headers}
        if self.session:
            hdrs["Mcp-Session-Id"] = self.session
        if self.protocol:
            hdrs["MCP-Protocol-Version"] = self.protocol
        try:
            conn.request("POST", self.path, body=body, headers=hdrs)
            resp = conn.getresponse()
            raw = resp.read()
            return resp.status, resp.getheader("Content-Type") or "", resp.getheader("Mcp-Session-Id"), raw, len(body)
        finally:
            conn.close()

    def call(self, method, params=None, timeout=120):
        with self.lock:
            id_ = self.next_id
            self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": id_, "method": method}
        if params is not None:
            msg["params"] = params
        t0, reply, err, raw, sent = time.time(), None, None, b"", 0
        try:
            status, ctype, sid, raw, sent = self._post(msg, timeout)
            if sid and not self.session:
                self.session = sid
            if status != 200:
                err = f"http {status}: {raw[:160]!r}"
            elif "text/event-stream" in ctype:
                reply = next((m for m in parse_sse_messages(raw.decode(errors="replace")) if m.get("id") == id_), None)
            else:
                reply = json.loads(raw or b"null")
            if reply is None and err is None:
                err = "no JSON-RPC reply for the request id"
            if method == "initialize" and isinstance(reply, dict):
                self.protocol = (reply.get("result") or {}).get("protocolVersion")
        except (OSError, ValueError) as e:
            err = f"transport: {e!r}"
        finally:
            acc = result_accounting(reply) if reply is not None else {"error": err}
            if self.usage is not None:
                args = (params or {}).get("arguments") if method == "tools/call" else params
                self.usage.record(name=call_name(method, params), transport="http", agent=self.agent,
                                  arg_bytes=len(json.dumps(args, separators=(",", ":")).encode()) if args is not None else 0,
                                  request_bytes=sent, response_bytes=len(raw), latency_ms=(time.time() - t0) * 1000, **acc)
        if reply is None:
            return {"error": {"code": -32099, "message": err}}
        return reply

    def notify(self, method, params):
        try:
            self._post({"jsonrpc": "2.0", "method": method, "params": params}, 10)
        except OSError:
            pass

    def tool(self, name, args, timeout=120):
        return self.call("tools/call", {"name": name, "arguments": args}, timeout)

    def stop(self):
        if not self.session:
            return
        try:
            conn = http.client.HTTPConnection(self.host, self.port, timeout=5)
            conn.request("DELETE", self.path, headers={"Mcp-Session-Id": self.session})
            conn.getresponse().read()
            conn.close()
        except OSError:
            pass


INIT_PARAMS = {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "rss-bench-v2", "version": "2"}}


def probe_http_mcp(base):
    """(available, detail): does the API answer MCP initialize over Streamable HTTP at /mcp?"""
    c = HttpMcp(base)
    try:
        r = c.call("initialize", INIT_PARAMS, timeout=10)
    except Exception as e:  # noqa: BLE001 - a failed probe is simply "absent"
        return False, repr(e)
    ok = isinstance(r, dict) and isinstance(r.get("result"), dict) and "protocolVersion" in r["result"]
    c.stop()
    return ok, (r.get("error") or {}).get("message") if not ok else "initialize answered"


# --------------------------------------------------------------------------------------
# Pinned parity fixtures (cloned to scratch, never vendored, verified by hash)
# --------------------------------------------------------------------------------------

class FixtureError(RuntimeError):
    pass


GIT_NO_FILTERS = ["-c", "filter.lfs.smudge=", "-c", "filter.lfs.clean=", "-c", "filter.lfs.process=",
                  "-c", "filter.lfs.required=false", "-c", "core.autocrlf=false", "-c", "advice.detachedHead=false",
                  "-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always"]
FIXED_IDENTITY = {"GIT_AUTHOR_NAME": "xmustard-bench", "GIT_AUTHOR_EMAIL": "bench@xmustard.invalid",
                  "GIT_COMMITTER_NAME": "xmustard-bench", "GIT_COMMITTER_EMAIL": "bench@xmustard.invalid",
                  "GIT_AUTHOR_DATE": "2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2026-01-01T00:00:00Z",
                  "GIT_LFS_SKIP_SMUDGE": "1", "GIT_TERMINAL_PROMPT": "0"}


def git(args, cwd, check=True, env_extra=None):
    env = dict(os.environ)
    env.update(FIXED_IDENTITY)
    env.update(env_extra or {})
    p = subprocess.run(["git", *GIT_NO_FILTERS, *args], cwd=cwd, env=env, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise FixtureError(f"git {' '.join(args[:4])} failed in {cwd}: {p.stderr.strip()[:300]}")
    return p


def content_digest(root):
    """sha256 over sorted `path NUL sha256(content) LF` of the tracked entries as they sit
    on disk (a symlink hashes its target string; a submodule gitlink, which has no
    checkout, hashes its pinned commit id). Identifies the exact bytes indexed."""
    entries = {}
    for rec in git(["ls-files", "-s", "-z"], root).stdout.split("\0"):
        if rec:
            meta, _, path = rec.partition("\t")
            mode, obj = meta.split()[:2]
            entries[path] = (mode, obj)
    h = hashlib.sha256()
    n = 0
    for p in sorted(entries):
        mode, obj = entries[p]
        full = os.path.join(root, p)
        if mode == "160000":
            d = hashlib.sha256(f"gitlink:{obj}".encode()).hexdigest()
        elif os.path.islink(full):
            d = hashlib.sha256(os.readlink(full).encode()).hexdigest()
        elif os.path.isfile(full):
            d = sha_file(full)
        else:
            raise FixtureError(f"tracked path missing on disk: {p}")
        h.update(p.encode() + b"\0" + d.encode() + b"\n")
        n += 1
    return h.hexdigest(), n


def mirror_roots():
    """Places a local mirror of research/ may live: this checkout and, for a git worktree,
    the main checkout (research/ is git-ignored, so it exists only there)."""
    roots = [REPO_ROOT]
    p = run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], cwd=REPO_ROOT, check=False)
    common = p.stdout.strip()
    if p.returncode == 0 and common:
        main = os.path.dirname(common)
        if main not in roots:
            roots.append(main)
    extra = os.environ.get("XMUSTARD_FIXTURE_MIRRORS")
    if extra:
        roots = extra.split(os.pathsep) + roots
    return roots


def fetch_commit(dest, commit, sources):
    """Shallow-fetch one exact commit into the repo at dest from the first source that has
    it. Local mirrors are tried first (no network); upstream last."""
    tried = []
    for src in sources:
        p = git(["fetch", "-q", "--depth", "1", "--no-tags", src, commit], dest, check=False)
        if p.returncode == 0 and git(["rev-parse", "FETCH_HEAD"], dest).stdout.strip() == commit:
            return src
        tried.append(f"{src}: {p.stderr.strip()[:160] or 'fetched a different commit'}")
    raise FixtureError(f"commit {commit} not available from any source: {tried}")


def ensure_source(name, spec, cache):
    """A verified checkout of the pinned upstream commit (HEAD and tree id checked)."""
    dest = os.path.join(cache, "src", f"{name}-{spec['commit'][:12]}")
    if os.path.isdir(os.path.join(dest, ".git")):
        head = git(["rev-parse", "HEAD"], dest, check=False).stdout.strip()
        tree = git(["rev-parse", "HEAD^{tree}"], dest, check=False).stdout.strip()
        if head == spec["commit"] and tree == spec["tree"]:
            return dest, "cache"
    shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest)
    git(["init", "-q"], dest)
    sources = []
    for rel in spec.get("mirrors", []):
        for root in mirror_roots():
            cand = rel if os.path.isabs(rel) else os.path.join(root, rel)
            if os.path.isdir(cand) and cand not in sources:
                sources.append(cand)
    if spec.get("url"):
        sources.append(spec["url"])
    used = fetch_commit(dest, spec["commit"], sources)
    git(["checkout", "-q", "--detach", spec["commit"]], dest)
    head = git(["rev-parse", "HEAD"], dest).stdout.strip()
    tree = git(["rev-parse", "HEAD^{tree}"], dest).stdout.strip()
    if head != spec["commit"] or tree != spec["tree"]:
        raise FixtureError(f"{name}: fetched {head} tree {tree}; pinned {spec['commit']} tree {spec['tree']}")
    return dest, used


def build_workspace_repo(dest, parts):
    """A fresh repo whose single root commit has exactly the given trees.

    parts: list of (source_repo, commit, prefix, excludes). Trees are assembled from git
    objects (read-tree), so no attribute, eol or LFS filter touches the content, and the
    resulting tree id is a pure function of the pinned inputs."""
    shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest)
    git(["init", "-q"], dest)
    for src, commit, _, _ in parts:
        git(["fetch", "-q", "--depth", "1", "--no-tags", src, commit], dest)
    idx_env = {"GIT_INDEX_FILE": os.path.join(dest, ".git", "bench-index")}
    for src, commit, prefix, excludes in parts:
        if prefix:
            git(["read-tree", f"--prefix={prefix}", commit], dest, env_extra=idx_env)
        else:
            git(["read-tree", commit], dest, env_extra=idx_env)
        for ex in excludes:
            git(["rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", prefix + ex], dest, env_extra=idx_env)
    tree = git(["write-tree"], dest, env_extra=idx_env).stdout.strip()
    os.remove(idx_env["GIT_INDEX_FILE"])
    commit = git(["commit-tree", tree, "-m", "xmustard bench fixture"], dest).stdout.strip()
    git(["update-ref", "refs/heads/main", commit], dest)
    git(["symbolic-ref", "HEAD", "refs/heads/main"], dest)
    git(["reset", "-q", "--hard", commit], dest)
    return tree


def verify_workspace(path, expect, label):
    tree = git(["rev-parse", "HEAD^{tree}"], path).stdout.strip()
    digest, n = content_digest(path)
    got = {"tree": tree, "content_sha256": digest, "files": n}
    bad = [k for k in ("tree", "content_sha256", "files") if expect.get(k) is not None and expect[k] != got[k]]
    if bad:
        raise FixtureError(f"{label}: {', '.join(f'{k} {got[k]} != pinned {expect[k]}' for k in bad)}")
    return got


def ensure_workspace(name, fixtures, cache):
    """The verified workspace repo for a fixture or composite name (cached, re-verified)."""
    fx, comp = fixtures["fixtures"], fixtures.get("composites", {})
    spec = fx.get(name) or comp.get(name)
    if spec is None:
        raise FixtureError(f"unknown fixture {name!r}")
    ws = spec["workspace"]
    pinned = bool(ws.get("content_sha256"))
    dest = os.path.join(cache, "ws", f"{name}-{ws['content_sha256'][:12] if pinned else 'unpinned'}")
    info = {"name": name, "path": dest, "pinned": ws}
    if pinned and os.path.isdir(os.path.join(dest, ".git")):
        try:
            info["verified"] = verify_workspace(dest, ws, name)
            info["source"] = "cache"
            return info
        except FixtureError:
            pass
    if name in fx:
        src, used = ensure_source(name, spec, cache)
        parts = [(src, spec["commit"], "", spec.get("exclude", []))]
        info["upstream"] = {"commit": spec["commit"], "tree": spec["tree"], "fetched_from": used}
    else:
        parts = []
        for member in spec["members"]:
            m = ensure_workspace(member, fixtures, cache)
            head = git(["rev-parse", "HEAD"], m["path"]).stdout.strip()
            parts.append((m["path"], head, member.rstrip("/") + "/", []))
        info["members"] = spec["members"]
    build_workspace_repo(dest, parts)
    info["verified"] = verify_workspace(dest, ws, name)
    info["source"] = "built"
    missing = [p["explain"] for p in spec.get("probes", []) if not os.path.isfile(os.path.join(dest, p["explain"]))]
    if missing:
        raise FixtureError(f"{name}: probe paths missing at the pinned commit: {missing}")
    return info


def clone_workspace(src, dest):
    """A private, writable copy of a verified workspace for one scenario."""
    git(["clone", "-q", "--local", src, dest], os.path.dirname(dest))
    return dest


def load_json(path):
    with open(path) as f:
        return json.load(f)


# --------------------------------------------------------------------------------------
# Ledger: per-component budget lines, workstream lines, reconciliation, delta check
# --------------------------------------------------------------------------------------

def ledger_reconcile(ledger):
    """Reconcile the §7.2 component lines with the per-workstream lines (§12.5, WS-10)."""
    comps = ledger["components"]
    steady = sum(c["steady_mib"] for c in comps.values())
    heavy = comps["heavy_slot"]["peak_mib"] + comps["git_child"]["peak_mib"]
    overlap = sum(max(0, c["peak_mib"] - c["steady_mib"]) for n, c in comps.items()
                  if n not in ("heavy_slot", "git_child", "margin") and not c.get("peak_in_heavy_slot"))
    design_by_proc = collections.defaultdict(lambda: [0.0, 0.0])
    for c in comps.values():
        if c.get("process"):
            design_by_proc[c["process"]][0] += c["steady_mib"]
            design_by_proc[c["process"]][1] += c["peak_mib"]
    ws_by_proc = collections.defaultdict(float)
    unassigned = []
    for wid, w in ledger["workstreams"].items():
        if w.get("line_mib") is None:
            unassigned.append(wid)
            continue
        if w.get("process"):
            ws_by_proc[w["process"]] += w["line_mib"]
    ref = (ledger.get("reference_measurement") or {}).get("component_p50_mib") or {}
    rows = []
    for proc in sorted(set(design_by_proc) | set(ws_by_proc)):
        d_steady, d_peak = design_by_proc.get(proc, [0.0, 0.0])
        cur = ref.get(proc)
        projected = None if cur is None else round(cur + ws_by_proc.get(proc, 0.0), 1)
        rows.append({"process": proc, "design_steady_mib": round(d_steady, 1), "design_peak_mib": round(d_peak, 1),
                     "workstream_lines_mib": round(ws_by_proc.get(proc, 0.0), 1), "reference_p50_mib": cur,
                     "projected_steady_mib": projected,
                     "over_design_steady": None if projected is None else projected > d_steady})
    limit = ledger["gate"]["limit_bytes"] / MIB
    return {
        "design_steady_mib": round(steady, 1),
        "design_peak_mib_7_2_method": round(steady + heavy, 1),
        "design_peak_mib_with_query_overlap": round(steady + heavy + overlap, 1),
        "gate_mib": round(limit, 1),
        "fits_7_2_method": steady + heavy <= limit,
        "fits_with_query_overlap": steady + heavy + overlap <= limit,
        "per_process": rows,
        "workstreams_without_a_line": unassigned,
        "note": ("7.2 method: steady subtotal + heavy slot + git child. With query overlap: resident components "
                 "also at their peak while the heavy slot runs (critic §12.4). Projected = reference measurement "
                 "p50 + the sum of workstream lines placed on that process."),
    }


def ledger_check(ledger, workstream, head, base):
    """Fail when a workstream's measured delta exceeds its ledger line (plus tolerance).

    head/base: {scenario: {"gate_peak_mib": x, "components_p50_mib": {...}}} measured on
    the same machine. The tree peak delta and the p50 delta of the workstream's process
    are both checked; a negative line (a saving) must actually be delivered."""
    tol = ledger["tolerance"]
    w = ledger["workstreams"].get(workstream)
    if w is None:
        line, proc, why = ledger.get("default_line_mib", 0.0), None, "not in the ledger: default line"
    else:
        line, proc, why = w.get("line_mib"), w.get("process"), w.get("title", "")
        if line is None:
            line, why = 0.0, "no line declared in the ledger (treated as 0 until one is set)"
    results, ok = [], True
    for sc in sorted(set(head) & set(base)):
        h, b = head[sc], base[sc]
        d_tree = round(h["gate_peak_mib"] - b["gate_peak_mib"], 2)
        row = {"scenario": sc, "line_mib": line, "tree_peak_delta_mib": d_tree,
               "tree_allowed_mib": round(line + tol["tree_peak_mib"], 2)}
        row["tree_ok"] = d_tree <= line + tol["tree_peak_mib"]
        if proc:
            hc = h.get("components_p50_mib", {}).get(proc, 0.0) or 0.0
            bc = b.get("components_p50_mib", {}).get(proc, 0.0) or 0.0
            row.update(process=proc, process_p50_delta_mib=round(hc - bc, 2),
                       process_allowed_mib=round(line + tol["component_p50_mib"], 2))
            row["process_ok"] = hc - bc <= line + tol["component_p50_mib"]
        row["ok"] = row["tree_ok"] and row.get("process_ok", True)
        ok = ok and row["ok"]
        results.append(row)
    if not results:
        ok = False
    return {"workstream": workstream, "line_mib": line, "process": proc, "why": why, "tolerance": tol,
            "rows": results, "passed": ok,
            "note": "no common scenario between head and base" if not results else None}


def ledger_view(report):
    """{scenario: {gate_peak_mib, components_p50_mib}} from a v2 report, for ledger_check."""
    out = {}
    for sc, r in report.get("scenarios", {}).items():
        if r.get("status") != "ran" or not (r.get("gate") or {}).get("valid"):
            continue
        comps = r["sampler"]["components"]
        out[sc] = {"gate_peak_mib": r["gate"]["peak_mib"],
                   "components_p50_mib": {c: v["p50_mib"] for c, v in comps.items()}}
    return out


# --------------------------------------------------------------------------------------
# Binaries and provenance
# --------------------------------------------------------------------------------------

def build_binaries_from(source_root, out_dir, core_bin=None):
    """Build the Go API and shim from source_root; resolve or build the Rust core."""
    os.makedirs(out_dir, exist_ok=True)
    api_go = os.path.join(source_root, "api-go")
    for name in ("xmustard-api", "xmustard-mcp"):
        run(["go", "build", "-o", os.path.join(out_dir, name), f"./cmd/{name}"], cwd=api_go)
    core = core_bin or os.environ.get("XMUSTARD_CORE_BIN")
    if not core:
        rust = os.path.join(source_root, "rust-core")
        run(["cargo", "build", "--release", "--quiet", "--bin", "xmustard-core"], cwd=rust)
        core = os.path.join(rust, "target", "release", "xmustard-core")
    return os.path.join(out_dir, "xmustard-api"), os.path.join(out_dir, "xmustard-mcp"), os.path.abspath(core)


def provenance_for(source_root, api_bin, mcp_bin, core, scripts):
    def g(*a):
        return run(["git", *a], cwd=source_root, check=False).stdout.strip()
    diff = run(["git", "diff", "HEAD", "--", "api-go", "rust-core/src", "rust-core/Cargo.toml", "rust-core/Cargo.lock"],
               cwd=source_root, check=False).stdout
    untracked = g("ls-files", "--others", "--exclude-standard", "--", "api-go", "rust-core/src").split()
    uh = hashlib.sha256()
    for p in sorted(untracked):
        uh.update(p.encode() + b"\0" + sha_file(os.path.join(source_root, p)).encode() + b"\n")
    return {
        "source_root": source_root, "head": g("rev-parse", "HEAD"),
        "source_diff_sha256": hashlib.sha256(diff.encode()).hexdigest(),
        "untracked_source_files": len(untracked), "untracked_source_sha256": uh.hexdigest(),
        "binaries_sha256": {"xmustard-api": sha_file(api_bin), "xmustard-mcp": sha_file(mcp_bin), "xmustard-core": sha_file(core)},
        "core_bin": core, "core_bin_prebuilt": bool(os.environ.get("XMUSTARD_CORE_BIN")),
        "script_sha256": {s: sha_file(os.path.join(REPO_ROOT, s)) for s in scripts},
        "env": {"os": platform.platform(), "machine": platform.machine(), "python": platform.python_version(),
                "go": run(["go", "version"], check=False).stdout.strip(), "cpus": os.cpu_count(),
                "load_avg": [round(x, 2) for x in os.getloadavg()] if hasattr(os, "getloadavg") else None},
    }


SCRIPTS = ["scripts/bench/rss_bench_v2.py", "scripts/bench/rss_bench.py", "scripts/bench/budget_ledger.json",
           "scripts/bench/parity_fixtures.json", "scripts/e2e/harness.py"]


# --------------------------------------------------------------------------------------
# Scenario: the frozen v1 workload, measured by v1's and v2's samplers in the same run
# --------------------------------------------------------------------------------------

V1_GATE_CHECKS = ("sampler valid", "sampled xMustard-owned tree peak")  # v1's own verdict lines, not workload checks
V1_HELPER_CHECK = "helper-child limit held"


def v1_workload_validity(v1gate, checks, kids, srep):
    """Invalid reasons for the v1 workload as judged by v2: v1's own validity (its
    sampler, completeness) and its mandatory workload checks, with one known v1
    miscount re-judged. v1 counts every process whose name contains "xmustard-core",
    including a core's forked-not-yet-exec'd children (RSS 0); v2 attributes those to
    fork_pre_exec, so the admission cap is judged on actual cores only."""
    invalid = [r for r in v1gate.get("invalid_reasons", []) if not r.startswith("mandatory workload checks failed")]
    failed = [c["check"] for c in checks if not c["ok"] and not c["check"].startswith(V1_GATE_CHECKS)]
    helper = None
    if V1_HELPER_CHECK in failed:
        comps = srep["components"]
        cores = sum(comps.get(c, {}).get("max_concurrent", 0) for c in ("rust_core_per_call", "rust_index_service"))
        ok = kids.get("peak", 1 << 30) <= kids.get("cap", -1) and cores <= kids.get("cap", -1)
        helper = {"v1_check_failed": True, "admission": kids, "v2_max_concurrent_cores": cores,
                  "v2_fork_pre_exec_max_concurrent": comps.get("fork_pre_exec", {}).get("max_concurrent", 0), "held": ok}
        if ok:
            failed.remove(V1_HELPER_CHECK)
    if failed:
        invalid.append(f"mandatory workload checks failed: {failed}")
    return invalid, helper


def run_v1_workload(ctx):
    """Run rss_bench.py's own main() with four substitutions, restored afterwards:
      * Sampler: v1's sampler class extended to run the v2 sampler beside it;
      * Mcp: the harness client plus exact wire-byte accounting;
      * build_binaries: the binaries this invocation already built (from --source-root);
      * provenance: computed for --source-root.
    The workload itself (fixture, steps, checks, gate inputs) is v1's code."""
    import rss_bench as v1

    usage = UsageLog()
    holder = {}
    registry, probe = ctx["registry"], ctx["probe"]
    role_of = {"xmustard-api": "go_daemon", "xmustard-mcp": "mcp_access"}

    class DualSampler(v1.Sampler):
        def __init__(self, roots, storage_dir=None):
            super().__init__(roots, storage_dir=storage_dir)
            v2roots = [Root(lbl, pid, role_of.get(lbl.split("#")[0], "unclassified:" + lbl), "owned") for lbl, pid in roots.items()]
            v2roots.append(Root("bench-harness", os.getpid(), "agent:bench-harness", "agent"))
            self.v2 = SamplerV2(v2roots, registry, probe, step_ref=v1.CURRENT_STEP, storage_dir=storage_dir)
            holder["v2"] = self.v2
            holder["api_pid"] = roots.get("xmustard-api")

        def start(self):
            self.v2.start()
            super().start()

        def stop(self):
            super().stop()
            self.v2.stop()

    saved = {k: getattr(v1, k) for k in ("Sampler", "Mcp", "build_binaries", "provenance")}
    tmp_report = os.path.join(ctx["work"], "v1-workload-v1report.json")
    argv = sys.argv
    exit_code = None
    try:
        v1.Sampler = DualSampler
        v1.Mcp = accounting_mcp_class(usage, "v1-shim")
        v1.build_binaries = lambda out_dir: (ctx["api_bin"], ctx["mcp_bin"], ctx["core"])
        v1.provenance = lambda api_bin, mcp_bin, core: ctx["provenance"]
        sys.argv = ["rss_bench.py", "--report", tmp_report]
        try:
            v1.main()
        except SystemExit as e:
            exit_code = e.code
        except Exception as e:  # noqa: BLE001 - a crashed workload is INVALID below, never a pass
            exit_code = repr(e)
    finally:
        for k, v in saved.items():
            setattr(v1, k, v)
        sys.argv = argv
    if not os.path.exists(tmp_report) or "v2" not in holder:
        return {"status": "ran", "gate": evaluate_gate({"gate": {"peak_bytes": 0}}, ["v1 workload did not produce a report"]),
                "exit_code": exit_code}
    v1rep = load_json(tmp_report)
    res = v1rep["results"]
    sampler = holder["v2"]
    srep = sampler.report()
    v1gate = res.get("gate", {})
    checks = list(v1rep.get("checks", []))
    invalid, helper = v1_workload_validity(v1gate, checks, (res.get("admission") or {}).get("children") or {}, srep)
    invalid += [f"v2 sampler: {p}" for p in sampler.validity()]
    v1_peak = (res.get("sampled_tree") or {}).get("peak_bytes")
    tol = ctx["ledger"]["tolerance"]["v1_crosscheck_mib"]
    cross = None
    if v1_peak is not None:
        d = round((srep["tree_all_peak_mib"] or 0) - v1_peak / MIB, 2)
        cross = {"v1_sampler_peak_mib": mib(v1_peak), "v1_sampler_peak_mb": round(v1_peak / 1e6, 1),
                 "v2_tree_all_peak_mib": srep["tree_all_peak_mib"], "v2_gate_peak_mib": srep["gate"]["peak_mib"],
                 "difference_mib": d, "tolerance_mib": tol, "within_noise": abs(d) <= tol,
                 "v1_samples": (res.get("sampled_tree") or {}).get("samples"), "v2_samples": srep["samples"],
                 "v1_interval_ms_median": ((res.get("sampled_tree") or {}).get("interval_ms_observed") or {}).get("median"),
                 "v2_interval_ms_median": srep["interval_ms_observed"]["median"],
                 "note": "two independent 100 ms samplers over the same run; the difference is sampling phase noise"}
        if not cross["within_noise"]:
            invalid.append(f"v2 disagrees with the frozen v1 sampler by {d} MiB (tolerance {tol})")
    else:
        invalid.append("the frozen v1 sampler reported no peak")
    gate = evaluate_gate(srep, invalid)
    return {"status": "ran", "description": "frozen v1 workload (501 generated files), v1's own code",
            "transport": "stdio-shim", "gate": gate, "sampler": srep, "v1": {"gate": v1gate, "sampled_tree_peak_mib": mib(v1_peak),
            "steps": res.get("steps"), "fixture": res.get("fixture"),
            "admission": {k: v for k, v in (res.get("admission") or {}).items() if k != "capture_attempts"}},
            "v1_crosscheck": cross, "helper_child_recheck": helper, "checks": checks,
            "checks_failed": [c["check"] for c in checks if not c["ok"]],
            "usage": usage.summary(), "exit_code": exit_code}


# --------------------------------------------------------------------------------------
# Parity-scale scenarios
# --------------------------------------------------------------------------------------

SCENARIOS = collections.OrderedDict([
    ("v1-workload", {"kind": "v1", "suites": ["ci", "parity"],
                     "about": "frozen v1 workload: 501 generated files, 2 stdio agents, captures and expansion"}),
    ("agents-1", {"kind": "agents", "agents": 1, "fixture": "parity-composite", "suites": ["parity"],
                  "about": "1 agent querying the composite fixture"}),
    ("agents-2", {"kind": "agents", "agents": 2, "fixture": "parity-composite", "suites": ["parity"],
                  "about": "2 agents querying the composite fixture"}),
    ("agents-4", {"kind": "agents", "agents": 4, "fixture": "parity-composite", "suites": ["parity"],
                  "about": "4 agents querying the composite fixture"}),
    ("reindex-during-queries", {"kind": "agents", "agents": 2, "fixture": "parity-composite", "disturb": "reindex",
                                "suites": ["parity"], "about": "2 agents query while edited files are reindexed (overlapping peaks)"}),
    ("snapshot-swap-under-load", {"kind": "agents", "agents": 2, "fixture": "parity-composite", "disturb": "edits",
                                  "suites": ["parity"], "about": "2 agents query while edits force index refresh and swap"}),
    ("captures-during-index", {"kind": "agents", "agents": 1, "fixture": "parity-composite", "disturb": "captures",
                               "cold": True, "suites": ["parity"], "about": "cold index build with 5 x 16 MiB captures in flight"}),
    ("two-hot-repos", {"kind": "agents", "agents": 2, "fixtures": ["cline", "pi-mono"], "suites": ["parity"],
                       "about": "2 agents alternating between two indexed repos"}),
    ("four-agents-four-worktrees", {"kind": "agents", "agents": 4, "fixture": "cline", "worktrees": 4, "cold": True,
                                    "suites": ["parity"], "about": "4 agents, each in its own git worktree of one repo, cold"}),
    ("watcher-on", {"kind": "agents", "agents": 2, "fixture": "parity-composite", "disturb": "edits",
                    "requires": ["watcher"], "suites": ["parity"], "about": "2 agents with the watcher running while files change"}),
])


def find_key(obj, key, depth=0):
    """First value of `key` anywhere in a JSON value (bounded depth and breadth)."""
    if depth > 8:
        return None
    if isinstance(obj, dict):
        if key in obj:
            return obj[key]
        for v in obj.values():
            r = find_key(v, key, depth + 1)
            if r is not None:
                return r
    elif isinstance(obj, list):
        for v in obj[:50]:
            r = find_key(v, key, depth + 1)
            if r is not None:
                return r
    return None


def tool_body(reply):
    try:
        return json.loads(reply["result"]["content"][0]["text"])
    except (KeyError, ValueError, IndexError, TypeError):
        return None


def feature_present(feat, v):
    """Whether an observed feature value means the feature is on."""
    if feat == "uncapped_index":
        return isinstance(v, dict) and v.get("truncated") is False
    if isinstance(v, (dict, list)):
        return bool(v)
    if isinstance(v, str):
        v = v.lower()
    return v not in (None, False, "", "off", "disabled", "absent", "none")


class FeatureState:
    """Product features the parity claim needs, detected from live responses using the
    detection keys declared in parity_fixtures.json `feature_probes`."""

    def __init__(self, spec):
        self.spec = spec
        self.values = {}
        self.lock = threading.Lock()

    def observe(self, body):
        if not isinstance(body, dict):
            return
        with self.lock:
            for feat, s in self.spec.items():
                for key in s.get("keys", []):
                    v = find_key(body, key)
                    if v is not None and feat not in self.values:
                        self.values[feat] = v
            cov = body.get("coverage") if isinstance(body.get("coverage"), dict) else None
            if cov and "truncated" in cov:
                prev = self.values.get("uncapped_index")
                cur = {"truncated": cov.get("truncated"), "indexed_files": cov.get("indexed_files"),
                       "eligible_files": cov.get("eligible_files"), "max_files": cov.get("max_files")}
                if prev is None or cur.get("truncated"):
                    self.values["uncapped_index"] = cur


def agent_loop(agent, name, targets, rounds, features, errors, calls):
    """One agent: ground every target once, remember once, then `rounds` rounds of
    search / explain / impact / recall. targets: [(workspace_id, probes)]; round i uses
    targets[i % len(targets)]. Every failure is recorded in `errors`."""

    def call(tool, args, ws):
        calls.append(1)
        r = agent.tool(tool, dict(args, workspace_id=ws), timeout=900)
        body = tool_body(r)
        features.observe(body)
        if "error" in r or (r.get("result") or {}).get("isError"):
            errors.append({"agent": name, "tool": tool, "detail": json.dumps(r.get("error") or body or r)[:300]})
        return body

    try:
        for ws, _ in targets:
            call("ground", {}, ws)
        ws0, probes0 = targets[0]
        call("remember", {"content": f"bench fact from {name}: search results carry coverage",
                          "title": f"{name} fact", "paths": probes0[0]["explain"]}, ws0)
        for i in range(rounds):
            ws, probes = targets[i % len(targets)]
            p = probes[i % len(probes)]
            call("search", {"query": p["search"]}, ws)
            call("explain", {"path": p["explain"]}, ws)
            call("impact", {"symbol": p["impact"]}, ws)
            call("recall", {"query": p["search"], "paths": p["explain"]}, ws)
    except Exception as e:  # noqa: BLE001 - a dead agent is a failed workload, never a quiet one
        errors.append({"agent": name, "tool": "loop", "detail": repr(e)[:300]})


def fixture_probes(fixtures, name, agent_idx=0):
    """The probe list for a fixture or composite (composite paths get the member prefix),
    rotated per agent so agents do not all ask the same question at once."""
    fx, comp = fixtures["fixtures"], fixtures.get("composites", {})
    if name in fx:
        probes = list(fx[name]["probes"])
    else:
        probes = []
        for m in comp[name]["members"]:
            for p in fx[m]["probes"]:
                probes.append(dict(p, explain=f"{m}/{p['explain']}"))
    shift = agent_idx % len(probes)
    return probes[shift:] + probes[:shift]


def edit_file(path, token):
    with open(path, "a") as f:
        f.write(f"\nexport function {token}(): number {{ return 1; }}\n")


def pick_edit_targets(candidates, n):
    """n deterministic edit targets spread across the sorted candidates (repeating when
    there are fewer candidates than edits)."""
    files = sorted(set(candidates))
    if not files:
        return []
    step = max(1, len(files) // n)
    picked = files[::step][:n]
    return (picked * (n // len(picked) + 1))[:n]


def indexed_ts_files(api, ws, queries=("function", "export", "return", "const", "class")):
    """TypeScript files the index content-indexes, taken from search hits. At this HEAD
    the index covers only the first files up to its cap, and coverage.losses lists at
    most 200 of the rest, so hits are the reliable way to find files a query can see."""
    found = set()
    for q in queries:
        st, body, _ = api.get(f"/api/workspaces/{ws}/search?q={q}&limit=100")
        for h in ((body or {}).get("hits") or []) if st == 200 and isinstance(body, dict) else []:
            path = h.get("path") or ""
            if path.endswith(".ts") and not path.endswith(".d.ts"):
                found.add(path)
    return sorted(found)


def counter_delta(h0, h1):
    """Before/after deltas of the API's data-movement counters (WS-06), when exposed."""
    c0 = ((h0 or {}).get("budget") or {}).get("counters")
    c1 = ((h1 or {}).get("budget") or {}).get("counters")
    if not isinstance(c1, dict):
        return {"available": False, "note": "/api/health has no budget.counters block at this HEAD (the API-side counters land with WS-06)"}
    c0 = c0 or {}
    out = {"available": True, "scope": c1.get("scope")}
    for k, v in c1.items():
        if isinstance(v, (int, float)) and not isinstance(v, bool):
            out[k] = v - (c0.get(k) or 0)
        elif isinstance(v, dict):
            out[k] = {kk: vv - ((c0.get(k) or {}).get(kk) or 0) for kk, vv in v.items() if isinstance(vv, (int, float))}
    return out


def run_agents_scenario(ctx, name, sc):
    fixtures, rounds = ctx["fixtures"], ctx["rounds"]
    feats = FeatureState(fixtures["feature_probes"])
    usage = UsageLog()
    checks = Checks(f"scenario:{name}")
    work = tempfile.mkdtemp(prefix=f"{name}-", dir=ctx["work"])
    step = ["setup"]
    api = sampler = None
    agents, steps, errors, calls = [], [], [], []
    result = {"status": "ran", "about": sc["about"], "rounds": rounds}
    completed = False

    def timed(label, fn):
        step[0] = label
        t0 = time.time()
        try:
            return fn()
        finally:
            steps.append({"step": label, "seconds": round(time.time() - t0, 3)})
            step[0] = "between steps"

    try:
        # fixtures: verified workspace -> private clone for this scenario, re-verified
        names = sc.get("fixtures") or [sc["fixture"]]
        repos, fixture_info = [], {}
        for fx_name in names:
            info = ensure_workspace(fx_name, fixtures, ctx["cache"])
            fixture_info[fx_name] = {k: info.get(k) for k in ("verified", "source", "upstream", "members")}
            dest = clone_workspace(info["path"], os.path.join(work, fx_name))
            got = verify_workspace(dest, info["pinned"], f"{fx_name} (scenario copy)")
            checks.check(f"fixture {fx_name} matches its pinned tree and content hash", True,
                         f"{got['files']} files, tree {got['tree'][:12]}, sha256 {got['content_sha256'][:12]}")
            repos.append((fx_name, fx_name, dest))  # (label, probe fixture, path)
        if sc.get("worktrees"):
            label0, fx0, base = repos[0]
            for i in range(1, sc["worktrees"]):
                wt = os.path.join(work, f"{label0}-wt{i}")
                git(["worktree", "add", "-q", "-b", f"bench-wt{i}", wt, "HEAD"], base)
                repos.append((f"{label0}-wt{i}", fx0, wt))
        result["fixtures"] = fixture_info
        data = os.path.join(work, "data")
        os.makedirs(data)
        with open(os.path.join(data, "settings.json"), "w") as f:
            json.dump({"require_multi_agent_verification": False}, f)
        api = Api(ctx["api_bin"], data, {"XMUSTARD_CORE_BIN": ctx["core"]}).start()
        http_ok, http_detail = probe_http_mcp(api.base)
        feats.values["http_mcp"] = http_ok
        result["transport"] = "http" if http_ok else "stdio-shim"
        result["transport_detail"] = http_detail
        n_agents = sc["agents"]
        for i in range(n_agents):
            if http_ok:
                agents.append(HttpMcp(api.base, usage=usage, agent=f"agent{i + 1}"))
            else:
                agents.append(accounting_mcp_class(usage, f"agent{i + 1}")(ctx["mcp_bin"], api.base).start())
        roots = [Root("xmustard-api", api.proc.pid, "go_daemon", "owned")]
        roots += [Root(f"xmustard-mcp#{i + 1}", a.proc.pid, "mcp_access", "owned") for i, a in enumerate(agents) if a.proc]
        roots.append(Root("bench-harness", os.getpid(), "agent:bench-harness", "agent"))
        sampler = SamplerV2(roots, ctx["registry"], ctx["probe"], step_ref=step, storage_dir=os.path.join(data, "evidence"))
        sampler.start()
        for a in agents:
            a.call("initialize", INIT_PARAMS)
            a.notify("notifications/initialized", {})
            a.call("tools/list", {})
        _, h0, _ = api.get("/api/health")
        wsids = []
        for label, _, path in repos:
            st, snap, _ = timed(f"load {label}", lambda: api.request("POST", "/api/workspaces/load", {"root_path": path, "auto_scan": True}, timeout=900))
            checks.check(f"workspace {label} loads", st == 200, str(st))
            wsids.append(snap["workspace"]["workspace_id"])
        if not sc.get("cold"):
            for (label, _, _), ws in zip(repos, wsids):
                st, _, _ = timed(f"index baseline {label}", lambda: api.request("POST", f"/api/workspaces/{ws}/index", timeout=1800))
                checks.check(f"index baseline {label}", st == 200, str(st))
        targets_per_agent = []
        for i in range(n_agents):
            if sc.get("worktrees"):
                j = i % len(repos)
                targets_per_agent.append([(wsids[j], fixture_probes(fixtures, repos[j][1], i))])
            else:
                targets_per_agent.append([(ws, fixture_probes(fixtures, fx, i)) for (_, fx, _), ws in zip(repos, wsids)])
        planned = sum(len(t) + 1 + 4 * rounds for t in targets_per_agent)
        threads = [threading.Thread(target=agent_loop, args=(a, f"agent{i + 1}", targets_per_agent[i], rounds, feats, errors, calls))
                   for i, a in enumerate(agents)]
        disturb = sc.get("disturb")
        edit_candidates = indexed_ts_files(api, wsids[0]) if disturb in ("reindex", "edits") else []
        label = f"{n_agents} agent(s) querying" + (f" + {disturb}" if disturb else "")
        step[0] = label
        t0 = time.time()
        if disturb == "captures":
            # a cold index build with five exactly-16 MiB captures in flight (admission decided on headers)
            idx_status = []
            idx = threading.Thread(target=lambda: idx_status.append(api.request("POST", f"/api/workspaces/{wsids[0]}/index", timeout=1800)[0]))
            idx.start()
            time.sleep(0.1)
            payload = (b"PASS suite case ok\n" * (v1mod().CAPTURE_BYTES // 19 + 1))[:v1mod().CAPTURE_BYTES]
            release, caps = threading.Event(), [None] * 5
            decided = [threading.Event() for _ in range(5)]
            cth = [threading.Thread(target=v1mod().capture_attempt, args=(api.port, wsids[0], payload, decided[j], release, caps, j)) for j in range(5)]
            [t.start() for t in cth]
            all_decided = all(e.wait(60) for e in decided)
            index_running = idx.is_alive()
            release.set()
            [t.start() for t in threads]
            [t.join() for t in cth]
            idx.join()
            [t.join() for t in threads]
            outcomes = [c["outcome"] for c in caps]
            sha = hashlib.sha256(payload).hexdigest()
            ok_caps = [c for c in caps if c["outcome"] == "captured"]
            checks.check("capture attempts were decided on headers", all_decided, str(outcomes))
            checks.check("no capture ended in a transport error", "transport_error" not in outcomes, str(outcomes))
            checks.check("admitted captures succeed with exact size and SHA-256",
                         bool(ok_caps) and all((c["json"] or {}).get("raw_sha256") == sha for c in ok_caps), str(outcomes))
            checks.check("cold index build answered 200", idx_status == [200], str(idx_status))
            checks.check("capture admission was decided while the index build was in flight", index_running,
                         "the index finished before every capture was decided: no overlap was measured")
            result["captures"] = {"outcomes": outcomes, "index_still_running_when_decided": index_running}
        else:
            [t.start() for t in threads]
            if disturb == "reindex":
                # edits, then a full reindex while the agents keep querying (overlapping peaks)
                time.sleep(1.0)
                edits = pick_edit_targets(edit_candidates, 40)
                for j, rel in enumerate(edits):
                    edit_file(os.path.join(repos[0][2], rel), f"xmBenchReindex{j}")
                alive = sum(t.is_alive() for t in threads)
                st, _, _ = api.request("POST", f"/api/workspaces/{wsids[0]}/index", timeout=1800)
                checks.check(f"reindex after {len(edits)} edits answered 200 during queries", st == 200 and bool(edits), str(st))
                checks.check("agents were querying when the reindex started (overlap)", alive > 0, f"{alive} agents alive")
                result["edits"], result["agents_alive_at_reindex"] = len(edits), alive
            elif disturb == "edits":
                # one edit every 1.5 s while the agents query: each forces a refresh and swap
                edits = pick_edit_targets(edit_candidates, 12)
                last, under_load = None, 0
                for j, rel in enumerate(edits):
                    if not any(t.is_alive() for t in threads):
                        break
                    last = (f"xmBenchSwap{j}", rel)
                    edit_file(os.path.join(repos[0][2], rel), last[0])
                    under_load += 1
                    time.sleep(1.5)
                checks.check("edits landed while agents were querying (overlap)", under_load > 0, f"{under_load} edits under load")
                result["edits_under_load"] = under_load
                [t.join() for t in threads]
                if last:
                    body = tool_body(agents[0].tool("search", {"workspace_id": wsids[0], "query": last[0]}, timeout=900)) or {}
                    fresh = any(h.get("path") == last[1] for h in (body.get("hits") or [])[:10])
                    checks.check("the last edit is searchable after the swap (freshness)", fresh, f"{last[0]} in {last[1]}")
                result["edits"] = len(edits)
            [t.join() for t in threads]
        steps.append({"step": label, "seconds": round(time.time() - t0, 3)})
        step[0] = "between steps"
        checks.check("every agent tool call answered without error", not errors, json.dumps(errors[:5]))
        checks.check("every planned agent call was made", len(calls) >= planned, f"{len(calls)} of {planned}")
        _, h1, _ = api.get("/api/health")
        result["health_counters"] = counter_delta(h0, h1)
        completed = True
    except Exception as e:  # noqa: BLE001 - recorded as an invalid run, never as a pass
        checks.check("scenario completed without exception", False, repr(e))
    finally:
        if sampler:
            sampler.stop()
        for a in agents:
            a.stop()
        if api:
            api.stop()
    result["steps"] = steps
    result["features"] = dict(feats.values)
    result["usage"] = usage.summary()
    result["errors"] = errors[:20]
    result["checks"] = checks.results
    result["checks_failed"] = [c["check"] for c in checks.failed()]
    invalid = []
    if sampler:
        result["sampler"] = srep = sampler.report()
        invalid += sampler.validity()
    else:
        srep = {"gate": {"peak_bytes": 0}}
        invalid.append("sampler never started")
    if not completed:
        invalid.append("workload incomplete")
    if result["checks_failed"]:
        invalid.append(f"mandatory workload checks failed: {result['checks_failed']}")
    result["gate"] = evaluate_gate(srep, invalid)
    if not ctx["keep"]:
        shutil.rmtree(work, ignore_errors=True)
    return result


def v1mod():
    import rss_bench
    return rss_bench


GRAMMAR_SAMPLES = collections.OrderedDict([
    ("go", ("sample.go", "package sample\n\nfunc Probe() int { return 1 }\n")),
    ("rust", ("sample.rs", "pub fn probe() -> i32 { 1 }\n")),
    ("typescript", ("sample.ts", "export function probe(): number { return 1; }\n")),
    ("tsx", ("sample.tsx", "export function Probe() { return <div />; }\n")),
    ("javascript", ("sample.js", "export function probe() { return 1; }\n")),
    ("python", ("sample.py", "def probe():\n    return 1\n")),
    ("java", ("Sample.java", "class Sample {\n  int probe() { return 1; }\n}\n")),
    ("c", ("sample.c", "int probe(void) { return 1; }\n")),
    ("cpp", ("sample.cpp", "int probe() { return 1; }\n")),
    ("csharp", ("Sample.cs", "class Sample {\n  int Probe() { return 1; }\n}\n")),
    ("ruby", ("sample.rb", "def probe\n  1\nend\n")),
    ("php", ("sample.php", "<?php\nfunction probe() { return 1; }\n")),
    ("kotlin", ("Sample.kt", "fun probe(): Int = 1\n")),
    ("swift", ("Sample.swift", "func probe() -> Int { return 1 }\n")),
    ("bash", ("sample.sh", "probe() {\n  echo 1\n}\n")),
    ("scala", ("Sample.scala", "object Sample {\n  def probe(): Int = 1\n}\n")),
    ("lua", ("sample.lua", "function probe()\n  return 1\nend\n")),
])


def probe_features(ctx):
    """Invocation-level feature probe against the same binaries, on a tiny scratch repo
    of original sample files: which of the 17 parity languages parse with a grammar
    (explain -> symbol_source/parser_language), whether /mcp serves MCP over HTTP, and
    which feature markers a search result carries."""
    work = tempfile.mkdtemp(prefix="features-", dir=ctx["work"])
    repo = os.path.join(work, "repo")
    os.makedirs(repo)
    for _, (fname, body) in GRAMMAR_SAMPLES.items():
        with open(os.path.join(repo, fname), "w") as f:
            f.write(body)
    git(["init", "-q"], repo)
    git(["add", "-A"], repo)
    git(["commit", "-qm", "grammar probe"], repo)
    data = os.path.join(work, "data")
    os.makedirs(data)
    api = None
    langs = {}
    feats = FeatureState(ctx["fixtures"]["feature_probes"])
    try:
        api = Api(ctx["api_bin"], data, {"XMUSTARD_CORE_BIN": ctx["core"]}).start()
        feats.values["http_mcp"] = probe_http_mcp(api.base)[0]
        st, snap, _ = api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True}, timeout=300)
        ws = snap["workspace"]["workspace_id"]
        for lang, (fname, _) in GRAMMAR_SAMPLES.items():
            st, body, _ = api.get(f"/api/workspaces/{ws}/explain-path?path={urllib.parse.quote(fname)}")
            body = body if isinstance(body, dict) else {}
            exp = body.get("explanation") if isinstance(body.get("explanation"), dict) else body  # with or without a cluster
            langs[lang] = {"status": st, "symbol_source": exp.get("symbol_source"), "parser_language": exp.get("parser_language"),
                           "symbols": len(exp.get("detected_symbols") or [])}
        st, body, _ = api.get(f"/api/workspaces/{ws}/search?q=probe")
        feats.observe(body if isinstance(body, dict) else None)
    except Exception as e:  # noqa: BLE001 - reported, and every dependent claim stays unestablished
        langs["_error"] = repr(e)
    finally:
        if api:
            api.stop()
        if not ctx["keep"]:
            shutil.rmtree(work, ignore_errors=True)
    parsed = [k for k, v in langs.items() if isinstance(v, dict) and v.get("symbol_source") == "tree_sitter" and v.get("symbols")]
    return {"grammars": {"languages": langs, "with_grammar": parsed, "count": len(parsed), "required": len(GRAMMAR_SAMPLES),
                         "basis": "explain on one original sample file per language; tree_sitter with >=1 symbol counts"},
            "features": dict(feats.values)}


# --------------------------------------------------------------------------------------
# Report
# --------------------------------------------------------------------------------------

def parity_claim(report, fixtures):
    """The parity-scale claim (PAR-EVAL-05) holds only when every parity scenario ran,
    passed, and every required product feature was present."""
    missing = []
    scen = report["scenarios"]
    for name, sc in SCENARIOS.items():
        if "parity" not in sc["suites"]:
            continue
        r = scen.get(name)
        if r is None:
            missing.append(f"{name}: not run in this invocation")
        elif r.get("status") != "ran":
            missing.append(f"{name}: {r.get('status')} ({r.get('reason')})")
        elif not r["gate"]["passed"]:
            missing.append(f"{name}: gate {r['gate']['verdict']}")
    ran = [n for n, r in scen.items() if r.get("status") == "ran" and SCENARIOS.get(n, {}).get("kind") == "agents"]
    for f, spec in fixtures["feature_probes"].items():
        absent = [n for n in ran if not feature_present(f, scen[n].get("features", {}).get(f))]
        if absent:
            missing.append(f"{f}: absent in {len(absent)} of {len(ran)} parity scenarios ({spec['requirement']})")
    g = report.get("grammars")
    if g is None:
        missing.append("grammars: not probed")
    elif g["count"] < g["required"]:
        missing.append(f"grammars: {g['count']} of {g['required']} parity languages parse with a grammar (PAR-SYM-05, WS-16)")
    return {"established": not missing, "missing": missing}


def overall_verdict(report):
    ran = [r for r in report["scenarios"].values() if r.get("status") == "ran"]
    if not ran:
        return "NO-RUN"
    if any(r["gate"]["verdict"] == "FAIL" for r in ran):
        return "FAIL"
    if any(r["gate"]["verdict"] == "INVALID" for r in ran):
        return "INVALID"
    return "PASS"


def fmt(v, suffix=""):
    return "–" if v is None else f"{v}{suffix}"


def render_markdown(report):
    L = []
    prov = report.get("provenance") or {}
    env = prov.get("env") or {}
    L.append(f"# Budget gate v2 report ({report['generated_at']})")
    L.append("")
    L.append(f"**Verdict: {report['verdict']}** (gate: ps-RSS of the xMustard-owned tree <= {GATE_MIB} MiB, "
             f"{GATE_BYTES:,} bytes). Parity-scale claim: "
             + ("**established**." if report["parity_claim"]["established"] else "**not established**."))
    if not report["parity_claim"]["established"] and report["parity_claim"]["missing"]:
        L.append("")
        L.append("Missing for the parity-scale claim:")
        for m in report["parity_claim"]["missing"]:
            L.append(f"- {m}")
    L.append("")
    L.append(f"Source `{(prov.get('head') or '')[:12]}` · core `{(prov.get('binaries_sha256') or {}).get('xmustard-core', '')[:12]}` · "
             f"{env.get('os')} {env.get('machine')} · {env.get('cpus')} CPUs · load {env.get('load_avg')} · probe `{report.get('probe')}`")
    L.append("")
    L.append("## Scenarios")
    L.append("")
    L.append("| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |")
    L.append("|---|---|---|---|---|---|---|---|")
    for name, r in report["scenarios"].items():
        if r.get("status") != "ran":
            L.append(f"| {name} | {r.get('status')}: {r.get('reason')} | – | – | – | – | – | – |")
            continue
        g, s = r["gate"], r.get("sampler") or {}
        sp = s.get("split_at_gate_peak") or {}
        unsplit = f" (+{sp['unsplit_mib']} exited)" if sp.get("unsplit_mib") else ""
        ext = sum((v.get("peak_mib") or 0) for v in (s.get("externals") or {}).values())
        L.append(f"| {name} | {g['verdict']} | {fmt(g.get('peak_mib'))} ({fmt(round(g['peak_bytes'] / 1e6, 1) if g.get('peak_bytes') else None)}) | "
                 f"{fmt((s.get('footprint') or {}).get('tree_peak_mib'))} | {fmt(sp.get('anon_mib'))} / {fmt(sp.get('file_mib'))}{unsplit} | "
                 f"{fmt(round(ext, 1) if s.get('externals') else 0)} | {fmt((s.get('gate') or {}).get('peak_step'))} | {r.get('transport', '–')} |")
    for name, r in report["scenarios"].items():
        if r.get("status") != "ran":
            continue
        s = r.get("sampler")
        L.append("")
        L.append(f"### {name}")
        L.append("")
        L.append(r.get("about") or r.get("description") or "")
        if r["gate"]["invalid_reasons"]:
            L.append("")
            L.append("Invalid because: " + "; ".join(r["gate"]["invalid_reasons"]))
        if not s:
            continue
        L.append("")
        L.append(f"Samples {s['samples']} (median interval {s['interval_ms_observed']['median']} ms, max {s['interval_ms_observed']['max']} ms). "
                 f"Footprint basis: {s['footprint']['basis']}. Split basis: {s['split_at_gate_peak']['basis']}.")
        L.append("")
        L.append("| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Largest process RSS peak MiB (unsampled) | "
                 "Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |")
        L.append("|---|---|---|---|---|---|---|---|---|")
        for c, v in s["components"].items():
            L.append(f"| {c} | {fmt(v['peak_mib'])} | {fmt(v['p50_mib'])} | {fmt(v['at_gate_peak_mib'])} | {fmt(v['footprint_peak_mib'])} | "
                     f"{fmt(v.get('process_rss_peak_mib'))} | {fmt(v.get('process_footprint_peak_mib'))} | {v['processes_seen']} | {v['max_concurrent']} |")
        L.append("")
        if s["externals"]:
            L.append("External processes (not counted in the gate):")
            L.append("")
            for c, v in s["externals"].items():
                L.append(f"- {c}: peak {v['peak_mib']} MiB, footprint peak {fmt(v['footprint_peak_mib'])} MiB, {v['processes_seen']} processes")
        else:
            L.append("External processes: none observed under the xMustard roots.")
        for lbl, v in (s.get("agents") or {}).items():
            L.append(f"- agent line `{lbl}` (the bench harness, external): peak {v['peak_mib']} MiB")
        L.append("")
        L.append("Processes at the gate peak:")
        L.append("")
        L.append("| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |")
        L.append("|---|---|---|---|---|---|---|")
        for p in s["processes_at_gate_peak"]:
            L.append(f"| {p['pid']} | {p['comm']} | {p['component']} ({p['class']}) | {mib(p['rss_kib'] * 1024)} | "
                     f"{fmt(mib(p.get('footprint_bytes')))} | {fmt(mib(p.get('anon_bytes')))} | {fmt(mib(p.get('file_bytes')))} |")
        cross = r.get("v1_crosscheck")
        if cross:
            L.append("")
            L.append(f"v1 cross-check (same run): v1 sampler {cross['v1_sampler_peak_mib']} MiB ({cross['v1_sampler_peak_mb']} MB), "
                     f"v2 owned+external {cross['v2_tree_all_peak_mib']} MiB, difference {cross['difference_mib']} MiB "
                     f"(tolerance {cross['tolerance_mib']}): {'within noise' if cross['within_noise'] else 'OUTSIDE noise'}. "
                     f"Median sample interval v1 {cross.get('v1_interval_ms_median')} ms, v2 {cross.get('v2_interval_ms_median')} ms.")
        hr = r.get("helper_child_recheck")
        if hr:
            L.append("")
            L.append(f"v1's helper-child check failed on its name count; v2 re-judged it on actual cores: "
                     f"{hr['v2_max_concurrent_cores']} concurrent cores against cap {hr['admission'].get('cap')} "
                     f"(forked-before-exec children seen at once: {hr['v2_fork_pre_exec_max_concurrent']}): "
                     f"{'held' if hr['held'] else 'NOT held'}.")
        u = r.get("usage")
        if u and u.get("per_tool"):
            L.append("")
            L.append(f"Tool usage (PAR-EVAL-04, harness side; tokens: {u['token_basis']}):")
            L.append("")
            L.append("| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |")
            L.append("|---|---|---|---|---|---|---|---|---|---|---|")
            for t, v in u["per_tool"].items():
                w = v.get("work") or {}
                L.append(f"| {t} | {v['calls']} | {v['errors']} | {v['arg_bytes']} | {v['response_bytes']} | {v['content_bytes']} | {v['est_tokens']} | "
                         f"{v['latency_ms']['p50']} | {v['latency_ms']['p95']} | {fmt(w.get('files_parsed'))} | {fmt(w.get('bytes_parsed'))} |")
        hc = r.get("health_counters")
        if hc:
            L.append("")
            if hc.get("available"):
                L.append("API data-movement counters (delta over the scenario): "
                         + ", ".join(f"{k}={v}" for k, v in hc.items() if k not in ("available", "scope")))
            else:
                L.append(hc["note"])
        feats = r.get("features")
        if feats is not None:
            L.append("")
            L.append("Features observed: " + (", ".join(f"{k}={json.dumps(v)}" for k, v in feats.items()) or "none"))
    g = report.get("grammars")
    if g:
        L.append("")
        L.append(f"## Grammars\n\n{g['count']} of {g['required']} parity languages parse with a grammar: {', '.join(g['with_grammar']) or 'none'}.")
    lc = report.get("ledger_check")
    L.append("")
    L.append("## Budget ledger")
    if lc:
        L.append("")
        L.append(f"Workstream {lc['workstream']} (line {lc['line_mib']} MiB on `{lc['process']}`): **{'PASS' if lc['passed'] else 'FAIL'}**.")
        L.append("")
        L.append("| Scenario | Tree peak delta MiB | Allowed | Process p50 delta MiB | Allowed |")
        L.append("|---|---|---|---|---|")
        for row in lc["rows"]:
            L.append(f"| {row['scenario']} | {row['tree_peak_delta_mib']} | {row['tree_allowed_mib']} | {fmt(row.get('process_p50_delta_mib'))} | {fmt(row.get('process_allowed_mib'))} |")
    rec = report.get("ledger_reconcile")
    if rec:
        L.append("")
        L.append(f"Design lines (§7.2): steady {rec['design_steady_mib']} MiB; peak {rec['design_peak_mib_7_2_method']} MiB by the §7.2 method, "
                 f"{rec['design_peak_mib_with_query_overlap']} MiB with query peaks overlapping the heavy slot; gate {rec['gate_mib']} MiB.")
        L.append("")
        L.append("| Process | Design steady | Design peak | Workstream lines | Reference p50 | Projected steady |")
        L.append("|---|---|---|---|---|---|")
        for row in rec["per_process"]:
            L.append(f"| {row['process']} | {row['design_steady_mib']} | {row['design_peak_mib']} | {row['workstream_lines_mib']} | "
                     f"{fmt(row['reference_p50_mib'])} | {fmt(row['projected_steady_mib'])} |")
    L.append("")
    L.append("## Not measured")
    L.append("")
    for u in report.get("unmeasured", []):
        L.append(f"- {u}")
    L.append("")
    return "\n".join(L)


UNMEASURED = [
    "hardware memory bandwidth",
    "allocations inside the Rust core and the MCP shim (only process totals are measured)",
    "byte copies per stage",
    "short peaks between 100 ms samples (the gate is a SAMPLED peak; the per-process lifetime maximum is reported beside it)",
    "exact tokens (a bytes/4 estimate is reported; no tokenizer is loaded)",
    "work inside short-lived children that exit between samples (processes_seen is a lower bound)",
]


# --------------------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------------------

def select_scenarios(args):
    if args.scenarios:
        names = [s.strip() for s in args.scenarios.split(",") if s.strip()]
        unknown = [n for n in names if n not in SCENARIOS]
        if unknown:
            raise SystemExit(f"unknown scenarios {unknown}; known: {list(SCENARIOS)}")
        return names
    return [n for n, sc in SCENARIOS.items() if args.suite in sc["suites"]]


def median_report(runs):
    """Fold repeated runs of one scenario: the run with the median gate peak is kept,
    and every run's peak is listed."""
    ok = [r for r in runs if r.get("status") == "ran"]
    if len(ok) <= 1:
        return runs[0]
    ok.sort(key=lambda r: r["gate"]["peak_bytes"])
    mid = ok[len(ok) // 2]
    mid = dict(mid)
    mid["repeats"] = {"runs": len(runs), "gate_peak_mib": [r["gate"]["peak_mib"] for r in ok],
                      "verdicts": [r["gate"]["verdict"] for r in ok],
                      "detail": [{"gate_peak_mib": r["gate"]["peak_mib"], "verdict": r["gate"]["verdict"],
                                  "v1_difference_mib": (r.get("v1_crosscheck") or {}).get("difference_mib"),
                                  "components_p50_mib": {c: v["p50_mib"] for c, v in ((r.get("sampler") or {}).get("components") or {}).items()},
                                  "components_peak_mib": {c: v["peak_mib"] for c, v in ((r.get("sampler") or {}).get("components") or {}).items()}}
                                 for r in ok]}
    if any(r["gate"]["verdict"] != "PASS" for r in ok):
        worst = next(r for r in ok if r["gate"]["verdict"] != "PASS")
        mid["gate"] = dict(mid["gate"], verdict=worst["gate"]["verdict"], passed=False,
                           invalid_reasons=mid["gate"]["invalid_reasons"] + [f"a repeat was {worst['gate']['verdict']}"])
    return mid


def cmd_run(args):
    if args.workstream and not args.baseline:
        raise SystemExit("--workstream needs --baseline <report.json> measured on the same machine")
    base = load_json(args.baseline) if args.baseline else None
    ledger = load_json(args.ledger)
    fixtures = load_json(FIXTURES_PATH)
    source_root = os.path.abspath(args.source_root or REPO_ROOT)
    out_dir = os.path.abspath(args.out)
    os.makedirs(out_dir, exist_ok=True)
    work = tempfile.mkdtemp(prefix="xm-gate-v2-")
    cache = os.path.abspath(args.fixture_cache or os.environ.get("XMUSTARD_FIXTURE_CACHE")
                            or os.path.join(tempfile.gettempdir(), "xmustard-parity-fixtures"))
    names = select_scenarios(args)
    api_bin, mcp_bin, core = build_binaries_from(source_root, os.path.join(work, "bin"), args.core_bin)
    ctx = {"ledger": ledger, "fixtures": fixtures, "registry": RoleRegistry(ledger["process_roles"]),
           "probe": default_probe(), "api_bin": api_bin, "mcp_bin": mcp_bin, "core": core, "work": work,
           "cache": cache, "keep": args.keep, "rounds": args.rounds}
    ctx["provenance"] = provenance_for(source_root, api_bin, mcp_bin, core, SCRIPTS)
    report = {"schema": 2, "generated_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
              "gate_limit_bytes": GATE_BYTES, "gate_limit_mib": GATE_MIB, "provenance": ctx["provenance"],
              "probe": ctx["probe"].name, "fixture_cache": cache, "scenarios": collections.OrderedDict(),
              "unmeasured": UNMEASURED}
    try:
        present = {}
        if any(SCENARIOS[n]["kind"] == "agents" for n in names):
            probe = probe_features(ctx)
            report["grammars"], report["features_probed"] = probe["grammars"], probe["features"]
            present = probe["features"]
        for n in names:
            sc = SCENARIOS[n]
            absent = [f for f in sc.get("requires", []) if not feature_present(f, present.get(f))]
            if absent:
                reqs = ", ".join(f"{f} ({fixtures['feature_probes'][f]['requirement']})" for f in absent)
                report["scenarios"][n] = {"status": "skipped", "reason": f"feature absent at this HEAD: {reqs}", "about": sc["about"]}
                print(f"== scenario {n}: skipped, {report['scenarios'][n]['reason']}", flush=True)
                continue
            print(f"== scenario {n}: {sc['about']}", flush=True)
            runs = []
            for i in range(max(1, args.repeat)):
                r = run_v1_workload(ctx) if sc["kind"] == "v1" else run_agents_scenario(ctx, n, sc)
                runs.append(r)
                g = r.get("gate") or {}
                cross = (r.get("v1_crosscheck") or {}).get("difference_mib")
                print(f"   run {i + 1}: {r.get('status')} {g.get('verdict', '')} peak {g.get('peak_mib')} MiB"
                      + (f" (v2 - v1 sampler: {cross} MiB)" if cross is not None else ""), flush=True)
                if r.get("status") != "ran":
                    break
            report["scenarios"][n] = median_report(runs)
    finally:
        if not args.keep:
            shutil.rmtree(work, ignore_errors=True)
    report["verdict"] = overall_verdict(report)
    report["parity_claim"] = parity_claim(report, fixtures)
    report["ledger_reconcile"] = ledger_reconcile(ledger)
    if args.workstream:
        report["ledger_check"] = ledger_check(ledger, args.workstream, ledger_view(report), ledger_view(base))
        report["ledger_check"]["baseline_head"] = (base.get("provenance") or {}).get("head")
    write_reports(report, out_dir)
    code = 0 if report["verdict"] == "PASS" else 1
    if report.get("ledger_check") and not report["ledger_check"]["passed"]:
        code = 1
    if args.require_parity_claim and not report["parity_claim"]["established"]:
        code = 1
    print(json.dumps({"verdict": report["verdict"], "parity_claim": report["parity_claim"]["established"],
                      "scenarios": {n: (r.get("gate") or {}).get("verdict", r.get("status")) for n, r in report["scenarios"].items()},
                      "report": os.path.join(out_dir, "report.json")}))
    return code


def write_reports(report, out_dir):
    with open(os.path.join(out_dir, "report.json"), "w") as f:
        json.dump(report, f, indent=1)
    with open(os.path.join(out_dir, "report.md"), "w") as f:
        f.write(render_markdown(report))


def cmd_fixtures(args):
    fixtures = load_json(FIXTURES_PATH)
    cache = os.path.abspath(args.fixture_cache or os.environ.get("XMUSTARD_FIXTURE_CACHE")
                            or os.path.join(tempfile.gettempdir(), "xmustard-parity-fixtures"))
    names = args.names.split(",") if args.names else list(fixtures["fixtures"]) + list(fixtures.get("composites", {}))
    if args.compute_pins:  # maintainer mode: build unpinned and print the values to pin
        for spec in list(fixtures["fixtures"].values()) + list(fixtures.get("composites", {}).values()):
            spec["workspace"] = {"tree": None, "files": None, "content_sha256": None}
    out, code = {}, 0
    for n in names:
        try:
            info = ensure_workspace(n, fixtures, cache)
            out[n] = {"ok": True, **{k: info.get(k) for k in ("path", "source", "verified", "upstream", "members")}}
        except FixtureError as e:
            out[n] = {"ok": False, "error": str(e)}
            code = 1
    print(json.dumps({"cache": cache, "fixtures": out}, indent=1))
    return code


def cmd_ledger(args):
    ledger = load_json(args.ledger)
    if args.workstream:
        chk = ledger_check(ledger, args.workstream, ledger_view(load_json(args.head)), ledger_view(load_json(args.baseline)))
        print(json.dumps(chk, indent=1))
        return 0 if chk["passed"] else 1
    print(json.dumps(ledger_reconcile(ledger), indent=1))
    return 0


def cmd_render(args):
    report = load_json(args.report)
    md = render_markdown(report)
    if args.md:
        with open(args.md, "w") as f:
            f.write(md)
    else:
        print(md)
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run", help="run scenarios and write report.json + report.md")
    r.add_argument("--suite", choices=["ci", "parity"], default="ci")
    r.add_argument("--scenarios", help="comma-separated scenario names (overrides --suite)")
    r.add_argument("--out", default=os.path.join(REPO_ROOT, "bench-out", "gate-v2"))
    r.add_argument("--repeat", type=int, default=1, help="runs per scenario; the median-peak run is reported")
    r.add_argument("--rounds", type=int, default=3, help="query rounds per agent in parity scenarios")
    r.add_argument("--source-root", help="checkout to build the binaries from (default: this checkout)")
    r.add_argument("--core-bin", help="prebuilt xmustard-core (default: XMUSTARD_CORE_BIN or cargo build)")
    r.add_argument("--fixture-cache", help="scratch directory for pinned fixture clones")
    r.add_argument("--ledger", default=LEDGER_PATH)
    r.add_argument("--workstream", help="check this workstream's ledger line against --baseline")
    r.add_argument("--baseline", help="a v2 report.json from the base revision on the same machine")
    r.add_argument("--require-parity-claim", action="store_true", help="exit nonzero unless the parity-scale claim holds")
    r.add_argument("--keep", action="store_true")
    f = sub.add_parser("fixtures", help="clone and verify the pinned parity fixtures into the scratch cache")
    f.add_argument("--names")
    f.add_argument("--fixture-cache")
    f.add_argument("--compute-pins", action="store_true", help="build without workspace pins and print the values to pin")
    lg = sub.add_parser("ledger", help="reconcile the ledger, or check a workstream delta between two reports")
    lg.add_argument("--ledger", default=LEDGER_PATH)
    lg.add_argument("--workstream")
    lg.add_argument("--head")
    lg.add_argument("--baseline")
    rd = sub.add_parser("render", help="render report.md from a report.json")
    rd.add_argument("report")
    rd.add_argument("--md")
    args = ap.parse_args(argv)
    return {"run": cmd_run, "fixtures": cmd_fixtures, "ledger": cmd_ledger, "render": cmd_render}[args.cmd](args)


if __name__ == "__main__":
    sys.exit(main())
