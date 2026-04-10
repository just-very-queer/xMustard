from __future__ import annotations

import os
import json
import re
import signal
import shlex
import shutil
import subprocess
import threading
import time
import uuid
from pathlib import Path
from typing import Optional

from .models import (
    ActivityRecord,
    AppSettings,
    LocalAgentCapabilities,
    RunMetrics,
    RunRecord,
    RuntimeCapabilities,
    RuntimeModel,
    RuntimeProbeResult,
    WorktreeStatus,
    build_activity_actor,
    utc_now,
)
from .store import FileStore


DEFAULT_CODEX_MODELS = [
    "gpt-5.4",
    "gpt-5.4-mini",
    "gpt-5.3-codex",
    "gpt-5.3-codex-spark",
    "gpt-5.2-codex",
]

MODEL_PRICING = {
    "gpt-5.4": {"input_per_1k": 0.01, "output_per_1k": 0.03},
    "gpt-5.4-mini": {"input_per_1k": 0.005, "output_per_1k": 0.015},
    "gpt-5.3-codex": {"input_per_1k": 0.02, "output_per_1k": 0.06},
    "gpt-5.3-codex-spark": {"input_per_1k": 0.015, "output_per_1k": 0.045},
    "gpt-5.2-codex": {"input_per_1k": 0.025, "output_per_1k": 0.075},
    "gpt-4o": {"input_per_1k": 0.0025, "output_per_1k": 0.01},
    "gpt-4o-mini": {"input_per_1k": 0.00015, "output_per_1k": 0.0006},
    "claude-sonnet-4-5": {"input_per_1k": 0.003, "output_per_1k": 0.015},
    "claude-opus-4": {"input_per_1k": 0.015, "output_per_1k": 0.075},
}


class RuntimeService:
    OPENCODE_MODEL_CACHE_TTL_SECONDS = 15.0
    RUNTIME_CAPABILITIES_CACHE_TTL_SECONDS = 10.0

    def __init__(self, store: FileStore) -> None:
        self.store = store
        self.processes: dict[str, subprocess.Popen] = {}
        self._opencode_model_cache: dict[str, tuple[float, list[str]]] = {}
        self._runtime_capabilities_cache: Optional[tuple[str, float, list[RuntimeCapabilities]]] = None

    def detect_runtimes(self) -> list[RuntimeCapabilities]:
        settings = self.store.load_settings()
        cache_key = "|".join(
            [
                settings.local_agent_type,
                settings.codex_bin or "",
                settings.opencode_bin or "",
            ]
        )
        now = time.monotonic()
        cached = self._runtime_capabilities_cache
        if cached and cached[0] == cache_key and cached[1] > now:
            return [item.model_copy() for item in cached[2]]
        codex_bin = self._resolve_binary(settings.codex_bin, "codex")
        opencode_bin = self._resolve_binary(settings.opencode_bin, "opencode")
        runtimes = [
            RuntimeCapabilities(
                runtime="codex",
                available=bool(codex_bin),
                binary_path=codex_bin,
                models=[RuntimeModel(runtime="codex", id=model) for model in DEFAULT_CODEX_MODELS],
                notes="Uses codex exec JSON streaming for issue runs.",
            ),
            RuntimeCapabilities(
                runtime="opencode",
                available=bool(opencode_bin),
                binary_path=opencode_bin,
                models=[RuntimeModel(runtime="opencode", id=model) for model in self._opencode_models()],
                notes="Uses opencode run JSON streaming and supports local OpenCode providers.",
            ),
        ]
        self._runtime_capabilities_cache = (
            cache_key,
            now + self.RUNTIME_CAPABILITIES_CACHE_TTL_SECONDS,
            [item.model_copy() for item in runtimes],
        )
        return runtimes

    def local_agent_capabilities(self) -> LocalAgentCapabilities:
        settings = self.store.load_settings()
        return LocalAgentCapabilities(
            selected_runtime=settings.local_agent_type,
            supports_live_subscribe=settings.local_agent_type == "codex",
            supports_terminal=True,
            runtimes=self.detect_runtimes(),
        )

    def _opencode_models(self) -> list[str]:
        settings = self.store.load_settings()
        opencode_bin = self._resolve_binary(settings.opencode_bin, "opencode")
        if not opencode_bin:
            return []
        cache_entry = self._opencode_model_cache.get(opencode_bin)
        now = time.monotonic()
        if cache_entry and cache_entry[0] > now:
            return list(cache_entry[1])
        try:
            completed = subprocess.run([opencode_bin, "models"], capture_output=True, text=True, check=False)
        except FileNotFoundError:
            return []
        if completed.returncode != 0:
            return []
        models = self._parse_opencode_models_output(completed.stdout)
        self._opencode_model_cache[opencode_bin] = (now + self.OPENCODE_MODEL_CACHE_TTL_SECONDS, models)
        return models

    def _parse_opencode_models_output(self, output: str) -> list[str]:
        normalized: list[str] = []
        seen: set[str] = set()
        model_pattern = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]*(/[A-Za-z0-9][A-Za-z0-9._:-]*)+$")

        def push(candidate: str) -> None:
            token = candidate.strip().strip(",")
            if not token or token in seen:
                return
            if model_pattern.match(token):
                seen.add(token)
                normalized.append(token)

        stripped = output.strip()
        if not stripped:
            return []

        if stripped.startswith("["):
            try:
                payload = json.loads(stripped)
            except json.JSONDecodeError:
                payload = None
            if isinstance(payload, list):
                for item in payload:
                    if isinstance(item, str):
                        push(item)
                return normalized

        for raw_line in output.splitlines():
            line = raw_line.strip()
            if not line:
                continue
            if line.startswith(("-", "*")):
                line = line[1:].strip()
            token = line.split()[0]
            push(token)
        return normalized

    def _sanitize_codex_args(self, raw_args: str) -> list[str]:
        args = shlex.split(raw_args or "")
        blocked_with_value = {
            "-m",
            "--model",
            "-C",
            "--cd",
            "--cwd",
            "-s",
            "--sandbox",
            "--sandbox-mode",
            "-a",
            "--ask-for-approval",
            "--approval-mode",
        }
        blocked_exact = {"exec", "--json", "--skip-git-repo-check"}
        sanitized: list[str] = []
        skip_next = False

        for arg in args:
            if skip_next:
                skip_next = False
                continue
            if arg in blocked_with_value:
                skip_next = True
                continue
            if arg in blocked_exact:
                continue
            if any(arg.startswith(f"{flag}=") for flag in blocked_with_value):
                continue
            sanitized.append(arg)
        return sanitized

    def start_issue_run(
        self,
        workspace_id: str,
        workspace_path: Path,
        issue_id: str,
        runtime: str,
        model: str,
        prompt: str,
        worktree: WorktreeStatus | None = None,
        runbook_id: str | None = None,
        wait_for_approval: bool = False,
    ) -> RunRecord:
        run_id = f"run_{uuid.uuid4().hex[:12]}"
        run_dir = self.store.runs_dir(workspace_id)
        log_path = run_dir / f"{run_id}.log"
        output_path = run_dir / f"{run_id}.out.json"
        command = self._build_command(runtime, model, workspace_path, prompt)
        status = "planning" if wait_for_approval else "queued"
        run = RunRecord(
            run_id=run_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            runtime=runtime,
            model=model,
            status=status,
            title=f"{runtime}:{issue_id}",
            prompt=prompt,
            command=command,
            command_preview=" ".join(shlex.quote(part) for part in command),
            log_path=str(log_path),
            output_path=str(output_path),
            runbook_id=runbook_id,
            worktree=worktree,
        )
        self.store.save_run(run)
        if not wait_for_approval:
            thread = threading.Thread(
                target=self._run_process,
                args=(run, workspace_path),
                daemon=True,
            )
            thread.start()
        return run

    def start_approved_run(self, run: RunRecord, workspace_path: Path) -> None:
        run = run.model_copy(update={"status": "queued", "started_at": __import__("datetime").datetime.utcnow().isoformat() + "Z"})
        self.store.save_run(run)
        thread = threading.Thread(
            target=self._run_process,
            args=(run, workspace_path),
            daemon=True,
        )
        thread.start()

    def _build_command(self, runtime: str, model: str, workspace_path: Path, prompt: str) -> list[str]:
        settings = self.store.load_settings()
        if runtime == "codex":
            codex_bin = self._resolve_binary(settings.codex_bin, "codex") or "codex"
            codex_args = self._sanitize_codex_args(settings.codex_args or "")
            return [
                codex_bin,
                "exec",
                "--json",
                "--skip-git-repo-check",
                "-s",
                "workspace-write",
                "-C",
                str(workspace_path),
                "-m",
                model,
                *codex_args,
                prompt,
            ]
        opencode_bin = self._resolve_binary(settings.opencode_bin, "opencode") or "opencode"
        return [
            opencode_bin,
            "run",
            "--format",
            "json",
            "--dir",
            str(workspace_path),
            "-m",
            model,
            prompt,
        ]

    def validate_runtime_model(self, runtime: str, model: str) -> None:
        runtimes = {entry.runtime: entry for entry in self.detect_runtimes()}
        runtime_entry = runtimes.get(runtime)
        if not runtime_entry or not runtime_entry.available:
            raise FileNotFoundError(f"Runtime {runtime} is not available")
        available_models = {entry.id for entry in runtime_entry.models}
        if available_models and model not in available_models:
            raise ValueError(f"Model {model} is not available for runtime {runtime}")

    def probe_runtime(self, workspace_path: Path, runtime: str, model: str) -> RuntimeProbeResult:
        self.validate_runtime_model(runtime, model)
        runtimes = {entry.runtime: entry for entry in self.detect_runtimes()}
        runtime_entry = runtimes[runtime]
        prompt = (
            "Reply with JSON only: "
            + json.dumps({"status": "ok", "runtime": runtime, "model": model})
        )
        command = self._build_command(runtime, model, workspace_path, prompt)
        started = time.monotonic()
        try:
            completed = subprocess.run(
                command,
                cwd=str(workspace_path),
                capture_output=True,
                text=True,
                timeout=45,
                check=False,
                env={**os.environ},
            )
        except subprocess.TimeoutExpired as exc:
            excerpt = ((exc.stdout or "") + (exc.stderr or "")).strip() or None
            return RuntimeProbeResult(
                runtime=runtime,
                model=model,
                ok=False,
                available=runtime_entry.available,
                duration_ms=int((time.monotonic() - started) * 1000),
                binary_path=runtime_entry.binary_path,
                command_preview=" ".join(shlex.quote(part) for part in command),
                output_excerpt=excerpt[:1400] if excerpt else None,
                error="Probe timed out after 45 seconds",
            )

        combined_output = ((completed.stdout or "") + (completed.stderr or "")).strip()
        summary = self._summarize_run_output(runtime, combined_output)
        excerpt = summary.get("text_excerpt") or combined_output or None
        if excerpt and len(excerpt) > 1400:
            excerpt = excerpt[:1400].rstrip() + "..."
        return RuntimeProbeResult(
            runtime=runtime,
            model=model,
            ok=completed.returncode == 0,
            available=runtime_entry.available,
            duration_ms=int((time.monotonic() - started) * 1000),
            exit_code=completed.returncode,
            binary_path=runtime_entry.binary_path,
            command_preview=" ".join(shlex.quote(part) for part in command),
            output_excerpt=excerpt,
            error=None if completed.returncode == 0 else excerpt or "Runtime probe failed",
        )

    def _run_process(self, run: RunRecord, workspace_path: Path) -> None:
        persisted = self.store.load_run(run.workspace_id, run.run_id)
        if persisted and persisted.status in {"planning", "cancelled"}:
            return
        log_path = Path(run.log_path)
        output_path = Path(run.output_path)
        log_path.parent.mkdir(parents=True, exist_ok=True)
        process: Optional[subprocess.Popen] = None
        try:
            with log_path.open("w", encoding="utf-8") as log_handle:
                process = subprocess.Popen(
                    run.command,
                    cwd=str(workspace_path),
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    stdin=subprocess.DEVNULL,
                    text=True,
                    bufsize=1,
                    env={**os.environ},
                )
                self.processes[run.run_id] = process
                current = run.model_copy(update={"status": "running", "started_at": __import__("datetime").datetime.utcnow().isoformat() + "Z", "pid": process.pid})
                self.store.save_run(current)
                full_output: list[str] = []
                assert process.stdout is not None
                for chunk in process.stdout:
                    log_handle.write(chunk)
                    log_handle.flush()
                    full_output.append(chunk)
                exit_code = process.wait()
                combined_output = "".join(full_output)
                output_path.write_text(combined_output, encoding="utf-8")
                summary = self._summarize_run_output(run.runtime, combined_output)
                persisted = self.store.load_run(run.workspace_id, run.run_id)
                if persisted and persisted.status == "cancelled":
                    final_status = "cancelled"
                else:
                    final_status = "completed" if exit_code == 0 else "failed"
                final = current.model_copy(
                    update={
                        "status": final_status,
                        "completed_at": __import__("datetime").datetime.utcnow().isoformat() + "Z",
                        "exit_code": exit_code,
                        "summary": summary,
                    }
                )
                self.store.save_run(final)
                metrics = self.calculate_run_metrics(final, len(combined_output))
                self.save_run_metrics(metrics)
                self._record_run_activity(final, "run.completed" if final_status == "completed" else f"run.{final_status}")
        except Exception as exc:
            failed = run.model_copy(
                update={
                    "status": "failed",
                    "completed_at": __import__("datetime").datetime.utcnow().isoformat() + "Z",
                    "error": str(exc),
                    "summary": {"event_count": 0, "tool_event_count": 0, "text_excerpt": None, "last_event_type": None},
                }
            )
            self.store.save_run(failed)
            metrics = self.calculate_run_metrics(failed, 0)
            self.save_run_metrics(metrics)
            log_path.write_text(str(exc), encoding="utf-8")
            self._record_run_activity(failed, "run.failed")
        finally:
            if process is not None:
                self.processes.pop(run.run_id, None)

    def read_run_log(self, workspace_id: str, run_id: str, offset: int = 0) -> dict[str, int | str | bool]:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        log_path = Path(run.log_path)
        if not log_path.exists():
            return {"offset": offset, "content": "", "eof": run.status in {"completed", "failed", "cancelled"}}
        with log_path.open("r", encoding="utf-8") as handle:
            handle.seek(offset)
            content = handle.read()
            next_offset = handle.tell()
        return {
            "offset": next_offset,
            "content": content,
            "eof": run.status in {"completed", "failed", "cancelled"},
        }

    def cancel_run(self, workspace_id: str, run_id: str) -> RunRecord:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        process = self.processes.get(run_id)
        if process is not None and process.poll() is None:
            try:
                process.send_signal(signal.SIGTERM)
            except ProcessLookupError:
                pass
        updated = run.model_copy(
            update={
                "status": "cancelled",
                "completed_at": __import__("datetime").datetime.utcnow().isoformat() + "Z",
                "exit_code": run.exit_code if run.exit_code is not None else -15,
            }
        )
        self.store.save_run(updated)
        return updated

    def _resolve_binary(self, configured_value: Optional[str], default_name: str) -> Optional[str]:
        candidate = (configured_value or "").strip()
        if candidate:
            if Path(candidate).exists():
                return candidate
            resolved = shutil.which(candidate)
            if resolved:
                return resolved
            return None
        return shutil.which(default_name)

    def _summarize_run_output(self, runtime: str, output: str) -> dict:
        event_count = 0
        tool_event_count = 0
        session_id = None
        last_event_type = None
        text_chunks: list[str] = []

        for raw_line in output.splitlines():
            line = raw_line.strip()
            if not line:
                continue
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                continue
            event_count += 1
            event_type = payload.get("type")
            if isinstance(event_type, str):
                last_event_type = event_type
            session_id = session_id or payload.get("sessionID") or payload.get("session_id")
            if event_type == "tool_use":
                tool_event_count += 1
            text = self._extract_text(payload)
            if text:
                text_chunks.append(text)

        excerpt = "\n".join(chunk.strip() for chunk in text_chunks if chunk.strip()).strip() or None
        if excerpt and len(excerpt) > 1400:
            excerpt = excerpt[:1400].rstrip() + "..."
        return {
            "runtime": runtime,
            "session_id": session_id,
            "event_count": event_count,
            "tool_event_count": tool_event_count,
            "last_event_type": last_event_type,
            "text_excerpt": excerpt,
        }

    def _extract_text(self, payload: dict) -> Optional[str]:
        direct = payload.get("text")
        if isinstance(direct, str) and direct.strip():
            return direct
        part = payload.get("part")
        if isinstance(part, dict):
            text = part.get("text")
            if isinstance(text, str) and text.strip():
                return text
        message = payload.get("message")
        if isinstance(message, dict):
            text = message.get("text")
            if isinstance(text, str) and text.strip():
                return text
        return None

    def _record_run_activity(self, run: RunRecord, action: str) -> None:
        activity = ActivityRecord(
            activity_id=uuid.uuid4().hex[:16],
            workspace_id=run.workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action=action,
            summary=f"{action.replace('.', ' ')} for {run.issue_id}",
            actor=build_activity_actor("agent", run.runtime, runtime=run.runtime, model=run.model),
            issue_id=run.issue_id,
            run_id=run.run_id,
            details={"status": run.status, "exit_code": run.exit_code, "runtime": run.runtime, "model": run.model},
            created_at=utc_now(),
        )
        self.store.append_activity(activity)

    def _estimate_tokens(self, text: str) -> int:
        return max(1, len(text) // 4)

    def _calculate_cost(self, model: str, input_tokens: int, output_tokens: int) -> float:
        pricing = MODEL_PRICING.get(model, {"input_per_1k": 0.01, "output_per_1k": 0.03})
        input_cost = (input_tokens / 1000) * pricing["input_per_1k"]
        output_cost = (output_tokens / 1000) * pricing["output_per_1k"]
        return round(input_cost + output_cost, 6)

    def calculate_run_metrics(self, run: RunRecord, output_length: int) -> RunMetrics:
        input_tokens = self._estimate_tokens(run.prompt)
        output_tokens = self._estimate_tokens(output_length)
        duration_ms = 0
        if run.started_at and run.completed_at:
            try:
                from datetime import datetime, timezone
                start = datetime.fromisoformat(run.started_at.replace("Z", "+00:00"))
                end = datetime.fromisoformat(run.completed_at.replace("Z", "+00:00"))
                duration_ms = int((end - start).total_seconds() * 1000)
            except (ValueError, TypeError):
                duration_ms = 0
        cost = self._calculate_cost(run.model, input_tokens, output_tokens)
        return RunMetrics(
            run_id=run.run_id,
            workspace_id=run.workspace_id,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            estimated_cost=cost,
            duration_ms=duration_ms,
            model=run.model,
            runtime=run.runtime,
            calculated_at=utc_now(),
        )

    def save_run_metrics(self, metrics: RunMetrics) -> None:
        metrics_dir = self.store.data_dir / "metrics"
        metrics_dir.mkdir(parents=True, exist_ok=True)
        metrics_path = metrics_dir / f"{metrics.run_id}.json"
        metrics_path.write_text(metrics.model_dump_json(), encoding="utf-8")

    def load_run_metrics(self, run_id: str) -> Optional[RunMetrics]:
        metrics_dir = self.store.data_dir / "metrics"
        metrics_path = metrics_dir / f"{run_id}.json"
        if not metrics_path.exists():
            return None
        try:
            return RunMetrics.model_validate_json(metrics_path.read_text(encoding="utf-8"))
        except Exception:
            return None

    def get_workspace_metrics(self, workspace_id: str) -> list[RunMetrics]:
        metrics_dir = self.store.data_dir / "metrics"
        if not metrics_dir.exists():
            return []
        metrics_list = []
        for metrics_file in metrics_dir.glob("*.json"):
            try:
                metrics = RunMetrics.model_validate_json(metrics_file.read_text(encoding="utf-8"))
                if metrics.workspace_id == workspace_id:
                    metrics_list.append(metrics)
            except Exception:
                continue
        return metrics_list
