#!/usr/bin/env python3
"""Measure xmustard-cursor's peak RSS for large and responding events.

Each trial runs under exitpeak (exitpeak.c), which reads VmHWM at the child's exit
and reaps it with wait4. The peak is the larger reading. Successful 1 MiB and 4 MiB
posts also check that the daemon receives every event byte. Linux only."""
import hashlib
import json
import os
import socket
import statistics
import struct
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

LIMIT_BYTES = 2 * 1024 * 1024
TRIALS = 3
EVENT = b'{"hook_event_name":"postToolUse","workspace_roots":["/tmp"],"tool_name":"MCP:recall","tool_output":"ok"}'
MEDIUM_EVENT = (
    b'{"hook_event_name":"postToolUse","workspace_roots":["/tmp"],"tool_name":"MCP:recall","tool_output":"'
    + b'x' * (1 * 1024 * 1024)
    + b'"}'
)
LARGE_EVENT = (
    b'{"conversation_id":"responding","generation_id":"g","hook_event_name":"postToolUse",'
    b'"cwd":"/tmp","workspace_roots":["/tmp"],"tool_name":"MCP:recall","tool_output":"'
    + b'x' * (4 * 1024 * 1024)
    + b'"}'
)
ANSWER = b'{"additional_context":"ok"}'
FAIL_OPEN_EXIT = 3
RUNNER_SOURCE = Path(__file__).with_name("exitpeak.c")
PT_INTERP = 3


class Handler(BaseHTTPRequestHandler):
    received_lengths = []
    received_hashes = []

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.received_lengths.append(len(body))
        self.received_hashes.append(hashlib.sha256(body).hexdigest())
        self.send_response(200)
        self.send_header("Content-Length", str(len(ANSWER)))
        self.end_headers()
        self.wfile.write(ANSWER)

    def log_message(self, _format, *_args):
        pass


def validate_observations(observations):
    if not observations:
        raise ValueError("no RSS observations were collected")
    for observation in observations:
        for key in ("exit_hwm_kb", "maxrss_kb"):
            value = observation.get(key)
            if not isinstance(value, int) or value <= 0:
                raise ValueError(f"missing or zero client RSS observation ({key}): {observation}")


def build_runner(directory):
    path = str(Path(directory) / "exitpeak")
    subprocess.run(["cc", "-O2", "-o", path, str(RUNNER_SOURCE)], check=True)
    return path


def linkage(path):
    """'dynamic' when the ELF file names a program interpreter, else 'static'."""
    with open(path, "rb") as elf:
        header = elf.read(64)
        if header[:4] != b"\x7fELF" or header[4] != 2:
            raise ValueError(f"{path}: not a 64-bit ELF file")
        order = "<" if header[5] == 1 else ">"
        (offset,) = struct.unpack_from(order + "Q", header, 32)
        size, count = struct.unpack_from(order + "HH", header, 54)
        elf.seek(offset)
        table = elf.read(size * count)
    kinds = {struct.unpack_from(order + "I", table, i * size)[0] for i in range(count)}
    return "dynamic" if PT_INTERP in kinds else "static"


def run_traced(runner, binary, base, event, output_file):
    """One run under exitpeak; peak_kb is the larger of its two readings."""
    env = os.environ.copy()
    env["XMUSTARD_API_BASE"] = base
    env["CURSOR_PROJECT_DIR"] = "/tmp"
    env["XMUSTARD_WORKSPACE_ID"] = "rss-test"
    # The event goes in and stdin closes at once: the reading covers the whole run, so
    # nothing has to hold the client open for a sampler.
    try:
        result = subprocess.run(
            [runner, "run", output_file, "10", binary, "postToolUse"],
            input=event,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env=env,
            timeout=15,
        )
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"{base}: RSS runner exceeded its 15-second bound") from error
    if result.returncode != 0:
        raise RuntimeError(f"{base}: RSS runner failed: {result.stdout!r}")
    stats = json.loads(result.stdout)
    stats["peak_kb"] = max(stats["exit_hwm_kb"], stats["maxrss_kb"])
    return stats


def measure(runner, binary, base, event, output_file, expected_exit, expected_output):
    stats = run_traced(runner, binary, base, event, output_file)
    if stats["rc"] != expected_exit:
        raise RuntimeError(f"{base}: expected client exit {expected_exit}: {stats}")
    output = Path(output_file).read_bytes()
    if output != expected_output:
        raise RuntimeError(f"{base}: expected client output {expected_output!r}, got {output!r}")
    validate_observations([stats])
    return stats


def unavailable_base():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    return f"http://127.0.0.1:{port}"


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: cursor_client_rss.py <release-xmustard-cursor>")
    if not sys.platform.startswith("linux"):
        raise SystemExit("cursor client RSS measurement requires Linux (/proc and ru_maxrss in KiB)")
    binary = str(Path(sys.argv[1]).resolve())
    if not Path(binary).is_file():
        raise SystemExit(f"client binary not found: {binary}")
    print(json.dumps({"client": binary, "linkage": linkage(binary)}), flush=True)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    server.timeout = 0.2
    server_thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.1}, daemon=True)
    server_thread.start()
    responding = f"http://127.0.0.1:{server.server_port}"
    unreachable = unavailable_base()
    results = []
    summaries = []
    try:
        with tempfile.TemporaryDirectory(prefix="cursor-rss-") as temp_dir:
            runner = build_runner(temp_dir)
            output_file = str(Path(temp_dir) / "stdout")
            scenarios = (
                ("large-unreachable", unreachable, LARGE_EVENT, FAIL_OPEN_EXIT, b""),
                ("responding-small", responding, EVENT, 0, ANSWER),
                ("responding-medium", responding, MEDIUM_EVENT, 0, ANSWER),
                ("responding-large", responding, LARGE_EVENT, 0, ANSWER),
            )
            for label, base, event, expected, answer in scenarios:
                peaks = []
                for trial in range(TRIALS):
                    stats = measure(runner, binary, base, event, output_file, expected, answer)
                    print(f"{label} trial={trial + 1}", json.dumps({"endpoint": base, **stats}), flush=True)
                    results.append(stats)
                    peaks.append(stats["peak_kb"])
                summaries.append({"endpoint": label, "trials": len(peaks),
                                  "median_kb": statistics.median(peaks), "max_kb": max(peaks)})
    finally:
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=2)
        if server_thread.is_alive():
            raise RuntimeError("responding endpoint server did not shut down")
    validate_observations(results)
    expected_lengths = (
        [len(EVENT)] * TRIALS
        + [len(MEDIUM_EVENT)] * TRIALS
        + [len(LARGE_EVENT)] * TRIALS
    )
    if Handler.received_lengths != expected_lengths:
        raise RuntimeError(f"responding event delivery mismatch: {Handler.received_lengths}")
    expected_hashes = [
        hashlib.sha256(event).hexdigest()
        for event in [EVENT] * TRIALS + [MEDIUM_EVENT] * TRIALS + [LARGE_EVENT] * TRIALS
    ]
    if Handler.received_hashes != expected_hashes:
        raise RuntimeError("responding event bodies differed from their input bytes")
    for summary in summaries:
        print(json.dumps(summary), flush=True)
    over_limit = [result["peak_kb"] for result in results if result["peak_kb"] * 1024 > LIMIT_BYTES]
    if over_limit:
        raise RuntimeError(f"client peak RSS exceeded 2 MiB in {len(over_limit)} of {len(results)} trials: {over_limit} KiB")


if __name__ == "__main__":
    main()
