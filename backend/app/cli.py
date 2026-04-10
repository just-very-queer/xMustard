from __future__ import annotations

import json
from pathlib import Path
import time
from typing import Optional

import typer

from .models import (
    FixRecordRequest,
    FixUpdateRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    RunAcceptRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    SavedIssueViewRequest,
    VerifyIssueRequest,
    WorkspaceLoadRequest,
)
from .service import TrackerService
from .store import FileStore

app = typer.Typer(add_completion=False, no_args_is_help=True)
store = FileStore(Path(__file__).resolve().parents[1] / "data")
service = TrackerService(store)


def _echo_json(payload) -> None:
    typer.echo(json.dumps(payload, indent=2))


def _split_csv(value: str) -> list[str]:
    return [item.strip() for item in value.split(",") if item.strip()]


def _is_final_run_status(status: str) -> bool:
    return status in {"completed", "failed", "cancelled"}


@app.command("health")
def health() -> None:
    _echo_json({"status": "ok"})


@app.command("capabilities")
def capabilities() -> None:
    _echo_json(service.runtime_service.local_agent_capabilities().model_dump(mode="json"))


@app.command("load-workspace")
def load_workspace(root_path: str, name: str = typer.Option(default="")) -> None:
    snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=root_path, name=name or None, auto_scan=True))
    _echo_json(snapshot.model_dump(mode="json"))


@app.command("scan")
def scan(workspace_id: str) -> None:
    snapshot = service.scan_workspace(workspace_id)
    _echo_json(snapshot.model_dump(mode="json"))


@app.command("snapshot")
def snapshot(workspace_id: str) -> None:
    payload = service.read_snapshot(workspace_id)
    if not payload:
        raise typer.BadParameter(f"Snapshot not found for workspace: {workspace_id}")
    _echo_json(payload.model_dump(mode="json"))


@app.command("runtimes")
def runtimes() -> None:
    payload = [item.model_dump(mode="json") for item in service.runtime_service.detect_runtimes()]
    _echo_json(payload)


@app.command("models")
def models(runtime: str) -> None:
    runtimes = {item.runtime: item for item in service.runtime_service.detect_runtimes()}
    entry = runtimes.get(runtime)
    if not entry:
        raise typer.BadParameter(f"Unknown runtime: {runtime}")
    _echo_json([item.model_dump(mode="json") for item in entry.models])


@app.command("settings-get")
def settings_get() -> None:
    _echo_json(service.get_settings().model_dump(mode="json"))


@app.command("settings-set")
def settings_set(
    local_agent_type: str = typer.Option(default="codex"),
    codex_bin: str = typer.Option(default=""),
    opencode_bin: str = typer.Option(default=""),
    codex_args: str = typer.Option(default=""),
    codex_model: str = typer.Option(default=""),
    opencode_model: str = typer.Option(default=""),
) -> None:
    current = service.get_settings()
    updated = current.model_copy(
        update={
            "local_agent_type": local_agent_type,
            "codex_bin": codex_bin or None,
            "opencode_bin": opencode_bin or None,
            "codex_args": codex_args or None,
            "codex_model": codex_model or None,
            "opencode_model": opencode_model or None,
        }
    )
    _echo_json(service.update_settings(updated).model_dump(mode="json"))


@app.command("export")
def export(workspace_id: str, output: str = typer.Option(default="")) -> None:
    bundle = service.export_workspace(workspace_id)
    payload = json.dumps(bundle.model_dump(mode="json"), indent=2)
    if output:
        Path(output).write_text(payload, encoding="utf-8")
        typer.echo(output)
        return
    typer.echo(payload)


@app.command("activity")
def activity(
    workspace_id: str,
    issue_id: str = typer.Option(default=""),
    run_id: str = typer.Option(default=""),
    limit: int = typer.Option(default=50),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_activity(
            workspace_id,
            issue_id=issue_id or None,
            run_id=run_id or None,
            limit=limit,
        )
    ]
    _echo_json(payload)


@app.command("activity-overview")
def activity_overview(workspace_id: str, limit: int = typer.Option(default=200)) -> None:
    payload = service.get_activity_overview(workspace_id, limit=limit).model_dump(mode="json")
    _echo_json(payload)


@app.command("issues")
def issues(
    workspace_id: str,
    q: str = typer.Option(default=""),
    severity: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
    source: str = typer.Option(default=""),
    label: str = typer.Option(default=""),
    drift_only: bool = typer.Option(default=False),
    needs_followup: bool = typer.Option(default=False),
    followup_only: bool = typer.Option(default=False),
    review_ready_only: bool = typer.Option(default=False),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_issues(
            workspace_id,
            query=q,
            severities=_split_csv(severity) or None,
            issue_statuses=_split_csv(issue_status) or None,
            sources=_split_csv(source) or None,
            labels=_split_csv(label) or None,
            drift_only=drift_only,
            needs_followup=needs_followup if followup_only else None,
            review_ready_only=review_ready_only,
        )
    ]
    _echo_json(payload)


@app.command("issue-create")
def issue_create(
    workspace_id: str,
    title: str = typer.Option(...),
    severity: str = typer.Option(...),
    bug_id: str = typer.Option(default=""),
    summary: str = typer.Option(default=""),
    impact: str = typer.Option(default=""),
    issue_status: str = typer.Option(default="open"),
    labels: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
    source_doc: str = typer.Option(default=""),
    needs_followup: bool = typer.Option(default=False),
) -> None:
    payload = service.create_issue(
        workspace_id,
        IssueCreateRequest(
            bug_id=bug_id or None,
            title=title,
            severity=severity,
            summary=summary or None,
            impact=impact or None,
            issue_status=issue_status,
            labels=_split_csv(labels),
            notes=notes or None,
            source_doc=source_doc or None,
            needs_followup=needs_followup,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("signals")
def signals(
    workspace_id: str,
    q: str = typer.Option(default=""),
    severity: str = typer.Option(default=""),
    promoted: Optional[bool] = typer.Option(default=None),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_signals(
            workspace_id,
            query=q,
            severity=severity or None,
            promoted=promoted,
        )
    ]
    _echo_json(payload)


@app.command("fixes")
def fixes(workspace_id: str, issue_id: str = typer.Option(default="")) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_fixes(workspace_id, issue_id=issue_id or None)]
    _echo_json(payload)


@app.command("verifications")
def verifications(workspace_id: str, issue_id: str = typer.Option(default="")) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_verifications(workspace_id, issue_id=issue_id or None)]
    _echo_json(payload)


@app.command("review-queue")
def review_queue(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_review_queue(workspace_id)]
    _echo_json(payload)


@app.command("runbooks")
def runbooks(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_runbooks(workspace_id)]
    _echo_json(payload)


@app.command("runbook-save")
def runbook_save(
    workspace_id: str,
    name: str = typer.Option(...),
    template: str = typer.Option(...),
    runbook_id: str = typer.Option(default=""),
    description: str = typer.Option(default=""),
    scope: str = typer.Option(default="issue"),
) -> None:
    normalized_template = template.replace("\\n", "\n")
    payload = service.save_runbook(
        workspace_id,
        RunbookUpsertRequest(
            runbook_id=runbook_id or None,
            name=name,
            description=description,
            scope=scope,
            template=normalized_template,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("runbook-delete")
def runbook_delete(workspace_id: str, runbook_id: str) -> None:
    service.delete_runbook(workspace_id, runbook_id)
    _echo_json({"ok": True, "runbook_id": runbook_id})


@app.command("sources")
def sources(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.read_sources(workspace_id)]
    _echo_json(payload)


@app.command("drift")
def drift(workspace_id: str) -> None:
    _echo_json(service.read_drift(workspace_id))


@app.command("worktree")
def worktree(workspace_id: str) -> None:
    _echo_json(service.read_worktree_status(workspace_id).model_dump(mode="json"))


@app.command("issue-context")
def issue_context(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.build_issue_context(workspace_id, issue_id).model_dump(mode="json"))


@app.command("issue-work")
def issue_work(
    workspace_id: str,
    issue_id: str,
    runbook_id: str = typer.Option(default=""),
) -> None:
    _echo_json(service.issue_work(workspace_id, issue_id, runbook_id=runbook_id or None).model_dump(mode="json"))


@app.command("issue-drift")
def issue_drift(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.read_issue_drift(workspace_id, issue_id).model_dump(mode="json"))


@app.command("issue-update")
def issue_update(
    workspace_id: str,
    issue_id: str,
    severity: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
    doc_status: str = typer.Option(default=""),
    code_status: str = typer.Option(default=""),
    labels: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
    needs_followup: bool = typer.Option(default=False),
) -> None:
    payload = service.update_issue(
        workspace_id,
        issue_id,
        IssueUpdateRequest(
            severity=severity or None,
            issue_status=issue_status or None,
            doc_status=doc_status or None,
            code_status=code_status or None,
            labels=_split_csv(labels) if labels else None,
            notes=notes or None,
            needs_followup=needs_followup,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("fix-record")
def fix_record(
    workspace_id: str,
    issue_id: str,
    summary: str = typer.Option(...),
    status: str = typer.Option(default="proposed"),
    run_id: str = typer.Option(default=""),
    runtime: str = typer.Option(default=""),
    model: str = typer.Option(default=""),
    how: str = typer.Option(default=""),
    changed_files: str = typer.Option(default=""),
    tests_run: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
) -> None:
    payload = service.record_fix(
        workspace_id,
        issue_id,
        FixRecordRequest(
            summary=summary,
            status=status,
            run_id=run_id or None,
            runtime=runtime or None,
            model=model or None,
            how=how or None,
            changed_files=_split_csv(changed_files),
            tests_run=_split_csv(tests_run),
            notes=notes or None,
            issue_status=issue_status or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("fix-draft")
def fix_draft(workspace_id: str, issue_id: str, run_id: str = typer.Option(...)) -> None:
    payload = service.suggest_fix_draft(workspace_id, issue_id, run_id)
    _echo_json(payload.model_dump(mode="json"))


@app.command("verify-bug")
def verify_bug(
    workspace_id: str,
    issue_id: str,
    runtime: str = typer.Option(default="opencode"),
    models: str = typer.Option(default=""),
    runbook_id: str = typer.Option(default="verify"),
    instruction: str = typer.Option(default=""),
    timeout_seconds: float = typer.Option(default=60.0),
    poll_interval: float = typer.Option(default=2.0),
) -> None:
    payload = service.verify_issue_three_pass(
        workspace_id,
        issue_id,
        VerifyIssueRequest(
            runtime=runtime,
            models=_split_csv(models),
            runbook_id=runbook_id or None,
            instruction=instruction or None,
            timeout_seconds=timeout_seconds,
            poll_interval=poll_interval,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("fix-update")
def fix_update(
    workspace_id: str,
    fix_id: str,
    status: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
) -> None:
    payload = service.update_fix(
        workspace_id,
        fix_id,
        FixUpdateRequest(
            status=status or None,
            notes=notes or None,
            issue_status=issue_status or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("run-start")
def run_start(
    workspace_id: str,
    issue_id: str,
    runtime: str = typer.Option(...),
    model: str = typer.Option(...),
    instruction: str = typer.Option(default=""),
    runbook_id: str = typer.Option(default=""),
) -> None:
    payload = service.start_issue_run(
        workspace_id,
        issue_id,
        runtime,
        model,
        instruction or None,
        runbook_id or None,
    )
    _echo_json(payload)


@app.command("agent-probe")
def agent_probe(
    workspace_id: str,
    runtime: str = typer.Option(...),
    model: str = typer.Option(...),
) -> None:
    _echo_json(service.probe_runtime(workspace_id, runtime, model))


@app.command("agent-query")
def agent_query(
    workspace_id: str,
    runtime: str = typer.Option(...),
    model: str = typer.Option(...),
    prompt: str = typer.Option(...),
) -> None:
    _echo_json(service.start_agent_query(workspace_id, runtime, model, prompt))


@app.command("runs")
def runs(workspace_id: str) -> None:
    _echo_json(service.list_runs(workspace_id))


@app.command("run-get")
def run_get(workspace_id: str, run_id: str) -> None:
    _echo_json(service.get_run(workspace_id, run_id))


@app.command("run-log")
def run_log(workspace_id: str, run_id: str, offset: int = typer.Option(default=0)) -> None:
    _echo_json(service.read_run_log(workspace_id, run_id, offset))


@app.command("run-wait")
def run_wait(
    workspace_id: str,
    run_id: str,
    timeout_seconds: float = typer.Option(default=30.0),
    poll_interval: float = typer.Option(default=1.0),
) -> None:
    deadline = time.monotonic() + timeout_seconds
    last = service.get_run(workspace_id, run_id)
    while not _is_final_run_status(last["status"]) and time.monotonic() < deadline:
        time.sleep(poll_interval)
        last = service.get_run(workspace_id, run_id)
    _echo_json(last)


@app.command("run-cancel")
def run_cancel(workspace_id: str, run_id: str) -> None:
    _echo_json(service.cancel_run(workspace_id, run_id))


@app.command("run-retry")
def run_retry(workspace_id: str, run_id: str) -> None:
    _echo_json(service.retry_run(workspace_id, run_id))


@app.command("run-review")
def run_review(
    workspace_id: str,
    run_id: str,
    disposition: str = typer.Option(...),
    notes: str = typer.Option(default=""),
) -> None:
    payload = service.review_run(
        workspace_id,
        run_id,
        RunReviewRequest(
            disposition=disposition,  # validated by pydantic literal
            notes=notes or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("review-accept")
def review_accept(
    workspace_id: str,
    run_id: str,
    issue_status: str = typer.Option(default="verification"),
    notes: str = typer.Option(default=""),
) -> None:
    payload = service.accept_review_run(
        workspace_id,
        run_id,
        RunAcceptRequest(issue_status=issue_status or None, notes=notes or None),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("views")
def views(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_saved_views(workspace_id)]
    _echo_json(payload)


@app.command("view-save")
def view_save(
    workspace_id: str,
    name: str,
    query: str = typer.Option(default=""),
    severity: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
    source: str = typer.Option(default=""),
    label: str = typer.Option(default=""),
    drift_only: bool = typer.Option(default=False),
    followup_only: bool = typer.Option(default=False),
    review_ready_only: bool = typer.Option(default=False),
) -> None:
    payload = service.create_saved_view(
        workspace_id,
        SavedIssueViewRequest(
            name=name,
            query=query,
            severities=_split_csv(severity),
            statuses=_split_csv(issue_status),
            sources=_split_csv(source),
            labels=_split_csv(label),
            drift_only=drift_only,
            needs_followup=True if followup_only else None,
            review_ready_only=review_ready_only,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("view-update")
def view_update(
    workspace_id: str,
    view_id: str,
    name: str = typer.Option(default=""),
    query: str = typer.Option(default=""),
    severity: str = typer.Option(default=""),
    issue_status: str = typer.Option(default=""),
    source: str = typer.Option(default=""),
    label: str = typer.Option(default=""),
    drift_only: bool = typer.Option(default=False),
    followup_only: bool = typer.Option(default=False),
    review_ready_only: bool = typer.Option(default=False),
) -> None:
    payload = service.update_saved_view(
        workspace_id,
        view_id,
        SavedIssueViewRequest(
            name=name,
            query=query,
            severities=_split_csv(severity),
            statuses=_split_csv(issue_status),
            sources=_split_csv(source),
            labels=_split_csv(label),
            drift_only=drift_only,
            needs_followup=True if followup_only else None,
            review_ready_only=review_ready_only,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("view-delete")
def view_delete(workspace_id: str, view_id: str) -> None:
    service.delete_saved_view(workspace_id, view_id)
    _echo_json({"ok": True, "view_id": view_id})


@app.command("smoke")
def smoke(
    workspace_id: str,
    issue_id: str,
    runtime: str = typer.Option(default="codex"),
    model: str = typer.Option(default="gpt-5.4-mini"),
    query_prompt: str = typer.Option(default='Reply with exactly {"status":"ok"}'),
    instruction: str = typer.Option(default="Validate only. If already fixed, report that and stop."),
    settle_seconds: float = typer.Option(default=2.0),
) -> None:
    health_payload = {"status": "ok"}
    capabilities_payload = service.runtime_service.local_agent_capabilities().model_dump(mode="json")
    snapshot_payload = service.read_snapshot(workspace_id)
    if not snapshot_payload:
        raise typer.BadParameter(f"Snapshot not found for workspace: {workspace_id}")
    issue_context_payload = service.build_issue_context(workspace_id, issue_id).model_dump(mode="json")
    probe_payload = service.probe_runtime(workspace_id, runtime, model)

    query_run = service.start_agent_query(workspace_id, runtime, model, query_prompt)
    time.sleep(settle_seconds)
    query_state = service.get_run(workspace_id, query_run["run_id"])
    query_log = service.read_run_log(workspace_id, query_run["run_id"], 0)
    if not _is_final_run_status(query_state["status"]):
        query_state = service.cancel_run(workspace_id, query_run["run_id"])

    issue_run = service.start_issue_run(workspace_id, issue_id, runtime, model, instruction)
    time.sleep(settle_seconds)
    issue_state = service.get_run(workspace_id, issue_run["run_id"])
    issue_log = service.read_run_log(workspace_id, issue_run["run_id"], 0)
    if not _is_final_run_status(issue_state["status"]):
        issue_state = service.cancel_run(workspace_id, issue_run["run_id"])

    _echo_json(
        {
            "health": health_payload,
            "capabilities": {
                "selected_runtime": capabilities_payload["selected_runtime"],
                "runtimes": [
                    {"runtime": item["runtime"], "available": item["available"]}
                    for item in capabilities_payload["runtimes"]
                ],
            },
            "snapshot": {
                "workspace_id": snapshot_payload.workspace.workspace_id,
                "workspace_name": snapshot_payload.workspace.name,
                "issues_total": len(snapshot_payload.issues),
                "signals_total": len(snapshot_payload.signals),
            },
            "issue_context": {
                "bug_id": issue_context_payload["issue"]["bug_id"],
                "runbook_steps": len(issue_context_payload["runbook"]),
            },
            "probe": {
                "runtime": probe_payload["runtime"],
                "model": probe_payload["model"],
                "ok": probe_payload["ok"],
                "exit_code": probe_payload["exit_code"],
            },
            "query_run": {
                "run_id": query_state["run_id"],
                "status": query_state["status"],
                "log_offset": query_log["offset"],
                "log_eof": query_log["eof"],
            },
            "issue_run": {
                "run_id": issue_state["run_id"],
                "status": issue_state["status"],
                "log_offset": issue_log["offset"],
                "log_eof": issue_log["eof"],
            },
        }
    )


if __name__ == "__main__":
    app()
