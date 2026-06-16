from __future__ import annotations

import fcntl
import os
import pty
import signal
import struct
import subprocess
import termios
import threading
import uuid
from pathlib import Path
from typing import Optional

from .store import FileStore


class TerminalSession:
    def __init__(self, terminal_id: str, process: subprocess.Popen, master_fd: int, log_path: Path) -> None:
        self.terminal_id = terminal_id
        self.process = process
        self.master_fd = master_fd
        self.log_path = log_path
        self.offset = 0


class TerminalService:
    def __init__(self, store: FileStore) -> None:
        self.store = store
        self.sessions: dict[str, TerminalSession] = {}

    def open_terminal(self, workspace_id: str, workspace_path: Path, cols: int, rows: int, terminal_id: Optional[str] = None) -> dict:
        terminal_id = terminal_id or f"term_{uuid.uuid4().hex[:12]}"
        log_path = self.store.workspace_dir(workspace_id) / "terminals" / f"{terminal_id}.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        master_fd, slave_fd = pty.openpty()
        self._resize_fd(slave_fd, cols, rows)
        shell = os.environ.get("SHELL", "/bin/zsh")
        process = subprocess.Popen(
            [shell, "-l"],
            cwd=str(workspace_path),
            stdin=slave_fd,
            stdout=slave_fd,
            stderr=slave_fd,
            text=False,
            close_fds=True,
            preexec_fn=os.setsid,
        )
        os.close(slave_fd)
        session = TerminalSession(terminal_id=terminal_id, process=process, master_fd=master_fd, log_path=log_path)
        self.sessions[terminal_id] = session
        thread = threading.Thread(target=self._pump_output, args=(session,), daemon=True)
        thread.start()
        return {"terminal_id": terminal_id, "pid": process.pid}

    def _pump_output(self, session: TerminalSession) -> None:
        with session.log_path.open("ab") as log_handle:
            while True:
                try:
                    chunk = os.read(session.master_fd, 4096)
                except OSError:
                    break
                if not chunk:
                    break
                log_handle.write(chunk)
                log_handle.flush()
            try:
                os.close(session.master_fd)
            except OSError:
                pass

    def write(self, terminal_id: str, data: str) -> None:
        session = self._require(terminal_id)
        os.write(session.master_fd, data.encode("utf-8"))

    def resize(self, terminal_id: str, cols: int, rows: int) -> None:
        session = self._require(terminal_id)
        self._resize_fd(session.master_fd, cols, rows)

    def close(self, terminal_id: str) -> None:
        session = self._require(terminal_id)
        try:
            os.killpg(os.getpgid(session.process.pid), signal.SIGTERM)
        except ProcessLookupError:
            pass
        self.sessions.pop(terminal_id, None)

    def read(self, workspace_id: str, terminal_id: str, offset: int) -> dict:
        session = self.sessions.get(terminal_id)
        log_path = session.log_path if session else self.store.workspace_dir(workspace_id) / "terminals" / f"{terminal_id}.log"
        if not log_path.exists():
            return {"offset": offset, "content": "", "eof": True}
        with log_path.open("rb") as handle:
            handle.seek(offset)
            content = handle.read()
            next_offset = handle.tell()
        return {
            "offset": next_offset,
            "content": content.decode("utf-8", errors="replace"),
            "eof": True if session is None else session.process.poll() is not None,
            "workspace_id": workspace_id,
            "terminal_id": terminal_id,
        }

    def _require(self, terminal_id: str) -> TerminalSession:
        session = self.sessions.get(terminal_id)
        if not session:
            raise FileNotFoundError(terminal_id)
        return session

    def _resize_fd(self, fd: int, cols: int, rows: int) -> None:
        winsize = struct.pack("HHHH", rows, cols, 0, 0)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, winsize)
