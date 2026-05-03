import json
from pathlib import Path
import subprocess
import time
from typing import Optional

import typer

from .models import (
    BrowserDumpUpsertRequest,
    DismissImprovementRequest,
    EvalScenarioReplayRequest,
    EvalScenarioUpsertRequest,
    FixRecordRequest,
    FixUpdateRequest,
    GitHubPRCreate,
    GuidanceStarterRequest,
    IntegrationTestRequest,
    IssueContextReplayRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    PlanApproveRequest,
    PlanRejectRequest,
    PlanTrackingUpdateRequest,
    PromoteSignalRequest,
    RunAcceptRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    SavedIssueViewRequest,
    ThreatModelUpsertRequest,
    TicketContextUpsertRequest,
    VulnerabilityFindingUpsertRequest,
    VulnerabilityImportRequest,
    VerificationProfileRunRequest,
    VerificationProfileUpsertRequest,
    VerifyIssueRequest,
    WorkspaceLoadRequest,
)
from .service import TrackerService
from .store import FileStore

app = typer.Typer(add_completion=False, no_args_is_help=True)
semantic_index_app = typer.Typer(add_completion=False, no_args_is_help=True)
app.add_typer(semantic_index_app, name="semantic-index")
store = FileStore(Path(__file__).resolve().parents[1] / "data")
service = TrackerService(store)


def _echo_json(payload) -> None:
    typer.echo(json.dumps(payload, indent=2))


def _split_csv(value: str) -> list[str]:
    return [item.strip() for item in value.split(",") if item.strip()]


def _normalize_newlines(value: str) -> str:
    return value.replace("\\n", "\n")


def _read_text_input(value: str, file_path: str = "") -> str:
    if file_path:
        return Path(file_path).read_text(encoding="utf-8")
    return _normalize_newlines(value)


def _parse_json_value(value: str):
    normalized = value.strip()
    if not normalized:
        return ""
    try:
        return json.loads(normalized)
    except json.JSONDecodeError:
        return value


def _parse_optional_bool(value: str) -> Optional[bool]:
    normalized = value.strip().lower()
    if not normalized:
        return None
    if normalized in {"1", "true", "yes", "y", "on"}:
        return True
    if normalized in {"0", "false", "no", "n", "off"}:
        return False
    raise typer.BadParameter("expected true or false")


def _parse_settings(settings: list[str], settings_json: str = "") -> dict:
    payload: dict = {}
    if settings_json:
        try:
            parsed = json.loads(settings_json)
        except json.JSONDecodeError as exc:
            raise typer.BadParameter(f"Invalid --settings-json: {exc}") from exc
        if not isinstance(parsed, dict):
            raise typer.BadParameter("--settings-json must decode to an object")
        payload.update(parsed)
    for item in settings:
        if "=" not in item:
            raise typer.BadParameter(f"Invalid --setting value '{item}'. Expected key=value.")
        key, raw_value = item.split("=", 1)
        key = key.strip()
        if not key:
            raise typer.BadParameter(f"Invalid --setting value '{item}'. Key must not be empty.")
        payload[key] = _parse_json_value(raw_value)
    return payload


def _is_final_run_status(status: str) -> bool:
    return status in {"completed", "failed", "cancelled"}


def _echo_ok(**payload) -> None:
    _echo_json({"ok": True, **payload})


def _run_go_semantic_index(action: str, workspace_id: str, *, surface: str, strategy: str, paths: list[str], limit: int, dsn: Optional[str] = None, schema: Optional[str] = None, dry_run: bool = False):
    api_go_dir = Path(__file__).resolve().parents[2] / "api-go"
    command = [
        "go",
        "run",
        "./cmd/xmustard-ops",
        "semantic-index",
        action,
        workspace_id,
        "--data-dir",
        str(store.root),
        "--surface",
        surface,
        "--strategy",
        strategy,
        "--limit",
        str(limit),
    ]
    for item in paths:
        command.extend(["--path", item])
    if dsn:
        command.extend(["--dsn", dsn])
    if schema:
        command.extend(["--schema", schema])
    if dry_run:
        command.append("--dry-run")
    try:
        completed = subprocess.run(
            command,
            cwd=api_go_dir,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError as exc:
        typer.echo(f"go semantic-index {action} failed: {exc}", err=True)
        raise typer.Exit(code=1) from exc
    if completed.returncode != 0:
        message = completed.stderr.strip() or completed.stdout.strip() or f"go semantic-index {action} failed"
        typer.echo(message, err=True)
        raise typer.Exit(code=1)
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        typer.echo(f"Invalid JSON from Go semantic-index {action}: {exc}", err=True)
        raise typer.Exit(code=1) from exc


@app.command("health")
def health() -> None:
    _echo_json({"status": "ok"})


@app.command("capabilities")
def capabilities() -> None:
    _echo_json(service.runtime_service.local_agent_capabilities().model_dump(mode="json"))


@app.command("workspaces")
def workspaces() -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_workspaces()])


@app.command("load-workspace")
def load_workspace(
    root_path: str,
    name: str = typer.Option(default=""),
    auto_scan: bool = typer.Option(True, "--auto-scan"),
    prefer_cached_snapshot: bool = typer.Option(True, "--prefer-cached-snapshot"),
) -> None:
    snapshot = service.load_workspace(
        WorkspaceLoadRequest(
            root_path=root_path,
            name=name or None,
            auto_scan=auto_scan,
            prefer_cached_snapshot=prefer_cached_snapshot,
        )
    )
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
    postgres_dsn: str = typer.Option(default=""),
    postgres_schema: str = typer.Option(default="xmustard"),
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
            "postgres_dsn": postgres_dsn or None,
            "postgres_schema": postgres_schema,
        }
    )
    _echo_json(service.update_settings(updated).model_dump(mode="json"))


@app.command("postgres-plan")
def postgres_plan() -> None:
    _echo_json(service.read_postgres_schema_plan().model_dump(mode="json"))


@app.command("postgres-render")
def postgres_render(
    schema: str = typer.Option(default=""),
    output: str = typer.Option(default=""),
) -> None:
    payload = service.render_postgres_schema_sql(schema=schema or None)
    if output:
        Path(output).write_text(payload, encoding="utf-8")
        typer.echo(output)
        return
    typer.echo(payload)


@app.command("postgres-bootstrap")
def postgres_bootstrap(
    dsn: str = typer.Option(default=""),
    schema: str = typer.Option(default=""),
) -> None:
    _echo_json(service.bootstrap_postgres_schema(dsn=dsn or None, schema=schema or None).model_dump(mode="json"))


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
    drift_only: bool = typer.Option(False, "--drift-only"),
    needs_followup: bool = typer.Option(False, "--needs-followup"),
    followup_only: bool = typer.Option(False, "--followup-only"),
    review_ready_only: bool = typer.Option(False, "--review-ready-only"),
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
    needs_followup: bool = typer.Option(False, "--needs-followup"),
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
    promoted: str = typer.Option(default=""),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_signals(
            workspace_id,
            query=q,
            severity=severity or None,
            promoted=_parse_optional_bool(promoted),
        )
    ]
    _echo_json(payload)


@app.command("signal-promote")
def signal_promote(
    workspace_id: str,
    signal_id: str,
    title: str = typer.Option(default=""),
    severity: str = typer.Option(default=""),
    labels: str = typer.Option(default=""),
) -> None:
    payload = service.promote_signal(
        workspace_id,
        signal_id,
        PromoteSignalRequest(
            title=title or None,
            severity=severity or None,
            labels=_split_csv(labels),
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


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


@app.command("verification-profiles")
def verification_profiles(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_verification_profiles(workspace_id)]
    _echo_json(payload)


@app.command("verification-profile-history")
def verification_profile_history(
    workspace_id: str,
    profile_id: str = typer.Option(default=""),
    issue_id: str = typer.Option(default=""),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_verification_profile_history(
            workspace_id,
            profile_id=profile_id or None,
            issue_id=issue_id or None,
        )
    ]
    _echo_json(payload)


@app.command("verification-profile-reports")
def verification_profile_reports(
    workspace_id: str,
    issue_id: str = typer.Option(default=""),
) -> None:
    payload = [
        item.model_dump(mode="json")
        for item in service.list_verification_profile_reports(workspace_id, issue_id=issue_id or None)
    ]
    _echo_json(payload)


@app.command("verification-profile-save")
def verification_profile_save(
    workspace_id: str,
    name: str = typer.Option(...),
    test_command: str = typer.Option(...),
    profile_id: str = typer.Option(default=""),
    description: str = typer.Option(default=""),
    coverage_command: str = typer.Option(default=""),
    coverage_report_path: str = typer.Option(default=""),
    coverage_format: str = typer.Option(default="unknown"),
    max_runtime_seconds: int = typer.Option(default=30),
    retry_count: int = typer.Option(default=1),
    source_paths: str = typer.Option(default=""),
    checklist_items: str = typer.Option(default=""),
) -> None:
    payload = service.save_verification_profile(
        workspace_id,
        VerificationProfileUpsertRequest(
            profile_id=profile_id or None,
            name=name,
            description=description,
            test_command=_normalize_newlines(test_command),
            coverage_command=_normalize_newlines(coverage_command) or None,
            coverage_report_path=coverage_report_path or None,
            coverage_format=coverage_format,
            max_runtime_seconds=max_runtime_seconds,
            retry_count=retry_count,
            source_paths=_split_csv(source_paths),
            checklist_items=_split_csv(checklist_items),
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("verification-profile-delete")
def verification_profile_delete(workspace_id: str, profile_id: str) -> None:
    service.delete_verification_profile(workspace_id, profile_id)
    _echo_ok(profile_id=profile_id)


@app.command("verification-profile-run")
def verification_profile_run(
    workspace_id: str,
    issue_id: str,
    profile_id: str,
    run_id: str = typer.Option(default=""),
) -> None:
    payload = service.run_issue_verification_profile(
        workspace_id,
        issue_id,
        profile_id,
        VerificationProfileRunRequest(run_id=run_id or None),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("sources")
def sources(workspace_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.read_sources(workspace_id)]
    _echo_json(payload)


@app.command("drift")
def drift(workspace_id: str) -> None:
    _echo_json(service.read_drift(workspace_id))


@app.command("quality-get")
def quality_get(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.get_issue_quality(workspace_id, issue_id))


@app.command("quality-score")
def quality_score(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.score_issue_quality(workspace_id, issue_id))


@app.command("quality-score-all")
def quality_score_all(workspace_id: str) -> None:
    _echo_json(service.score_all_issues(workspace_id))


@app.command("duplicates")
def duplicates(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.find_duplicates(workspace_id, issue_id))


@app.command("triage-issue")
def triage_issue(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.triage_issue(workspace_id, issue_id))


@app.command("triage-all")
def triage_all(workspace_id: str) -> None:
    _echo_json(service.triage_all_issues(workspace_id))


@app.command("worktree")
def worktree(workspace_id: str) -> None:
    _echo_json(service.read_worktree_status(workspace_id).model_dump(mode="json"))


@app.command("repo-state")
def repo_state(workspace_id: str) -> None:
    _echo_json(service.read_repo_tool_state(workspace_id).model_dump(mode="json"))


@app.command("ingestion-plan")
def ingestion_plan(workspace_id: str) -> None:
    _echo_json(service.read_ingestion_plan(workspace_id).model_dump(mode="json"))


@semantic_index_app.command("plan")
def semantic_index_plan(
    workspace_id: str,
    surface: str = typer.Option("cli", "--surface", help="cli | web | all"),
    strategy: str = typer.Option("key_files", "--strategy", help="key_files | paths"),
    path: list[str] = typer.Option([], "--path"),
    limit: int = typer.Option(12, "--limit"),
    dsn: str = typer.Option("", "--dsn"),
) -> None:
    _echo_json(
        _run_go_semantic_index(
            "plan",
            workspace_id,
            surface=surface,
            strategy=strategy,
            paths=path,
            limit=limit,
            dsn=dsn or None,
        )
    )


@semantic_index_app.command("run")
def semantic_index_run(
    workspace_id: str,
    surface: str = typer.Option("cli", "--surface", help="cli | web | all"),
    strategy: str = typer.Option("key_files", "--strategy", help="key_files | paths"),
    path: list[str] = typer.Option([], "--path"),
    limit: int = typer.Option(12, "--limit"),
    dsn: str = typer.Option("", "--dsn"),
    schema: str = typer.Option("", "--schema"),
    dry_run: bool = typer.Option(False, "--dry-run"),
) -> None:
    _echo_json(
        _run_go_semantic_index(
            "run",
            workspace_id,
            surface=surface,
            strategy=strategy,
            paths=path,
            limit=limit,
            dsn=dsn or None,
            schema=schema or None,
            dry_run=dry_run,
        )
    )


@semantic_index_app.command("status")
def semantic_index_status(
    workspace_id: str,
    surface: str = typer.Option("cli", "--surface", help="cli | web | all"),
    strategy: str = typer.Option("key_files", "--strategy", help="key_files | paths"),
    path: list[str] = typer.Option([], "--path"),
    limit: int = typer.Option(12, "--limit"),
    dsn: str = typer.Option("", "--dsn"),
    schema: str = typer.Option("", "--schema"),
) -> None:
    _echo_json(
        _run_go_semantic_index(
            "status",
            workspace_id,
            surface=surface,
            strategy=strategy,
            paths=path,
            limit=limit,
            dsn=dsn or None,
            schema=schema or None,
        )
    )


@app.command("changes")
def changes(workspace_id: str, base_ref: str = typer.Option(default="HEAD")) -> None:
    _echo_json(service.read_change_summary(workspace_id, base_ref=base_ref).model_dump(mode="json"))


@app.command("changed-symbols")
def changed_symbols(workspace_id: str, base_ref: str = typer.Option(default="HEAD")) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.read_changed_symbols(workspace_id, base_ref=base_ref)])


@app.command("changed-since-last-run")
def changed_since_last_run(workspace_id: str) -> None:
    _echo_json(service.read_changes_since_last_run(workspace_id).model_dump(mode="json"))


@app.command("changed-since-last-accepted-fix")
def changed_since_last_accepted_fix(workspace_id: str) -> None:
    _echo_json(service.read_changes_since_last_accepted_fix(workspace_id).model_dump(mode="json"))


@app.command("impact")
def impact(workspace_id: str, base_ref: str = typer.Option(default="HEAD")) -> None:
    _echo_json(service.read_impact(workspace_id, base_ref=base_ref).model_dump(mode="json"))


@app.command("repo-context")
def repo_context(workspace_id: str, base_ref: str = typer.Option(default="HEAD")) -> None:
    _echo_json(service.read_repo_context(workspace_id, base_ref=base_ref).model_dump(mode="json"))


@app.command("retrieval-search")
def retrieval_search(workspace_id: str, query: str = typer.Option(...), limit: int = typer.Option(default=12)) -> None:
    _echo_json(service.search_retrieval(workspace_id, query, limit=limit).model_dump(mode="json"))


@app.command("run-targets")
def run_targets(workspace_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_run_targets(workspace_id)])


@app.command("verify-targets")
def verify_targets(workspace_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_verify_targets(workspace_id)])


@app.command("code-explainer")
def code_explainer(workspace_id: str, path: str = typer.Option(...)) -> None:
    _echo_json(service.explain_path(workspace_id, path).model_dump(mode="json"))


@app.command("path-symbols")
def path_symbols(workspace_id: str, path: str = typer.Option(...)) -> None:
    _echo_json(service.read_path_symbols(workspace_id, path).model_dump(mode="json"))


@app.command("postgres-materialize-path")
def postgres_materialize_path(
    workspace_id: str,
    path: str = typer.Option(...),
    dsn: str = typer.Option(default=""),
    schema: str = typer.Option(default=""),
) -> None:
    _echo_json(
        service.materialize_path_symbols_to_postgres(
            workspace_id,
            path,
            dsn=dsn or None,
            schema=schema or None,
        ).model_dump(mode="json")
    )


@app.command("postgres-materialize-workspace-symbols")
def postgres_materialize_workspace_symbols(
    workspace_id: str,
    strategy: str = typer.Option(default="key_files"),
    path: list[str] = typer.Option(default=[]),
    limit: int = typer.Option(default=12),
    dsn: str = typer.Option(default=""),
    schema: str = typer.Option(default=""),
) -> None:
    _echo_json(
        service.materialize_workspace_symbols_to_postgres(
            workspace_id,
            strategy=strategy,
            paths=path,
            limit=limit,
            surface="cli",
            dsn=dsn or None,
            schema=schema or None,
        ).model_dump(mode="json")
    )


@app.command("semantic-search")
def semantic_search(
    workspace_id: str,
    pattern: str = typer.Option(...),
    language: str = typer.Option(default=""),
    path_glob: str = typer.Option(default=""),
    limit: int = typer.Option(default=50),
) -> None:
    _echo_json(
        service.search_semantic_pattern(
            workspace_id,
            pattern,
            language=language or None,
            path_glob=path_glob or None,
            limit=limit,
        ).model_dump(mode="json")
    )


@app.command("postgres-materialize-semantic-search")
def postgres_materialize_semantic_search(
    workspace_id: str,
    pattern: str = typer.Option(...),
    language: str = typer.Option(default=""),
    path_glob: str = typer.Option(default=""),
    limit: int = typer.Option(default=50),
    dsn: str = typer.Option(default=""),
    schema: str = typer.Option(default=""),
) -> None:
    _echo_json(
        service.materialize_semantic_search_to_postgres(
            workspace_id,
            pattern,
            language=language or None,
            path_glob=path_glob or None,
            limit=limit,
            dsn=dsn or None,
            schema=schema or None,
        ).model_dump(mode="json")
    )


@app.command("tree")
def tree(workspace_id: str, relative_path: str = typer.Option(default="")) -> None:
    _echo_json(service.list_tree(workspace_id, relative_path))


@app.command("guidance")
def guidance(workspace_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_workspace_guidance(workspace_id)])


@app.command("guidance-health")
def guidance_health(workspace_id: str) -> None:
    _echo_json(service.get_workspace_guidance_health(workspace_id).model_dump(mode="json"))


@app.command("guidance-generate")
def guidance_generate(
    workspace_id: str,
    template_id: str = typer.Option(..., help="agents | openhands_repo | conventions"),
    overwrite: bool = typer.Option(False, "--overwrite"),
) -> None:
    payload = service.generate_guidance_starter(
        workspace_id,
        GuidanceStarterRequest(template_id=template_id, overwrite=overwrite),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("repo-config")
def repo_config(workspace_id: str) -> None:
    _echo_json(service.read_workspace_repo_config(workspace_id).model_dump(mode="json"))


@app.command("repo-config-health")
def repo_config_health(workspace_id: str) -> None:
    _echo_json(service.get_workspace_repo_config_health(workspace_id).model_dump(mode="json"))


@app.command("repo-map")
def repo_map(workspace_id: str) -> None:
    _echo_json(service.read_repo_map(workspace_id).model_dump(mode="json"))


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


@app.command("ticket-contexts")
def ticket_contexts(workspace_id: str, issue_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_ticket_contexts(workspace_id, issue_id)])


@app.command("ticket-context-save")
def ticket_context_save(
    workspace_id: str,
    issue_id: str,
    title: str = typer.Option(...),
    provider: str = typer.Option(default="manual"),
    context_id: str = typer.Option(default=""),
    external_id: str = typer.Option(default=""),
    summary: str = typer.Option(default=""),
    acceptance_criteria: str = typer.Option(default=""),
    links: str = typer.Option(default=""),
    labels: str = typer.Option(default=""),
    status: str = typer.Option(default=""),
    source_excerpt: str = typer.Option(default=""),
) -> None:
    payload = service.save_ticket_context(
        workspace_id,
        issue_id,
        TicketContextUpsertRequest(
            context_id=context_id or None,
            provider=provider,
            external_id=external_id or None,
            title=title,
            summary=_normalize_newlines(summary),
            acceptance_criteria=_split_csv(acceptance_criteria),
            links=_split_csv(links),
            labels=_split_csv(labels),
            status=status or None,
            source_excerpt=_normalize_newlines(source_excerpt) or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("ticket-context-delete")
def ticket_context_delete(workspace_id: str, issue_id: str, context_id: str) -> None:
    service.delete_ticket_context(workspace_id, issue_id, context_id)
    _echo_ok(context_id=context_id)


@app.command("threat-models")
def threat_models(workspace_id: str, issue_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_threat_models(workspace_id, issue_id)])


@app.command("threat-model-save")
def threat_model_save(
    workspace_id: str,
    issue_id: str,
    title: str = typer.Option(...),
    methodology: str = typer.Option(default="manual"),
    threat_model_id: str = typer.Option(default=""),
    summary: str = typer.Option(default=""),
    assets: str = typer.Option(default=""),
    entry_points: str = typer.Option(default=""),
    trust_boundaries: str = typer.Option(default=""),
    abuse_cases: str = typer.Option(default=""),
    mitigations: str = typer.Option(default=""),
    references: str = typer.Option(default=""),
    status: str = typer.Option(default="draft"),
) -> None:
    payload = service.save_threat_model(
        workspace_id,
        issue_id,
        ThreatModelUpsertRequest(
            threat_model_id=threat_model_id or None,
            title=title,
            methodology=methodology,
            summary=_normalize_newlines(summary),
            assets=_split_csv(assets),
            entry_points=_split_csv(entry_points),
            trust_boundaries=_split_csv(trust_boundaries),
            abuse_cases=_split_csv(abuse_cases),
            mitigations=_split_csv(mitigations),
            references=_split_csv(references),
            status=status,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("threat-model-delete")
def threat_model_delete(workspace_id: str, issue_id: str, threat_model_id: str) -> None:
    service.delete_threat_model(workspace_id, issue_id, threat_model_id)
    _echo_ok(threat_model_id=threat_model_id)


@app.command("context-replays")
def context_replays(workspace_id: str, issue_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_issue_context_replays(workspace_id, issue_id)])


@app.command("context-replay-capture")
def context_replay_capture(
    workspace_id: str,
    issue_id: str,
    label: str = typer.Option(default=""),
) -> None:
    payload = service.capture_issue_context_replay(
        workspace_id,
        issue_id,
        IssueContextReplayRequest(label=label or None),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("context-replay-compare")
def context_replay_compare(workspace_id: str, issue_id: str, replay_id: str) -> None:
    _echo_json(service.compare_issue_context_replay(workspace_id, issue_id, replay_id).model_dump(mode="json"))


@app.command("eval-scenarios")
def eval_scenarios(workspace_id: str, issue_id: str = typer.Option(default="")) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_eval_scenarios(workspace_id, issue_id or None)])


@app.command("eval-scenario-save")
def eval_scenario_save(
    workspace_id: str,
    issue_id: str = typer.Option(...),
    name: str = typer.Option(...),
    scenario_id: str = typer.Option(default=""),
    description: str = typer.Option(default=""),
    baseline_replay_id: str = typer.Option(default=""),
    guidance_paths: str = typer.Option(default=""),
    ticket_context_ids: str = typer.Option(default=""),
    verification_profile_ids: str = typer.Option(default=""),
    run_ids: str = typer.Option(default=""),
    browser_dump_ids: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
) -> None:
    payload = service.save_eval_scenario(
        workspace_id,
        EvalScenarioUpsertRequest(
            scenario_id=scenario_id or None,
            issue_id=issue_id,
            name=name,
            description=_normalize_newlines(description) or None,
            baseline_replay_id=baseline_replay_id or None,
            guidance_paths=_split_csv(guidance_paths),
            ticket_context_ids=_split_csv(ticket_context_ids),
            verification_profile_ids=_split_csv(verification_profile_ids),
            run_ids=_split_csv(run_ids),
            browser_dump_ids=_split_csv(browser_dump_ids),
            notes=_normalize_newlines(notes) or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("eval-scenario-delete")
def eval_scenario_delete(workspace_id: str, scenario_id: str) -> None:
    service.delete_eval_scenario(workspace_id, scenario_id)
    _echo_ok(scenario_id=scenario_id)


@app.command("eval-report")
def eval_report(workspace_id: str, scenario_id: str = typer.Option(default="")) -> None:
    _echo_json(service.get_eval_report(workspace_id, scenario_id or None).model_dump(mode="json"))


@app.command("eval-scenario-replay")
def eval_scenario_replay(
    workspace_id: str,
    issue_id: str,
    runtime: str = typer.Option(...),
    model: str = typer.Option(...),
    scenario_ids: str = typer.Option(default=""),
    instruction: str = typer.Option(default=""),
    runbook_id: str = typer.Option(default=""),
    planning: bool = typer.Option(False, "--planning"),
) -> None:
    _echo_json(
        service.replay_eval_scenarios(
            workspace_id,
            issue_id,
            EvalScenarioReplayRequest(
                runtime=runtime,
                model=model,
                scenario_ids=_split_csv(scenario_ids),
                instruction=_normalize_newlines(instruction) or None,
                runbook_id=runbook_id or None,
                planning=planning,
            ),
        ).model_dump(mode="json")
    )


@app.command("browser-dumps")
def browser_dumps(workspace_id: str, issue_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_browser_dumps(workspace_id, issue_id)])


@app.command("browser-dump-save")
def browser_dump_save(
    workspace_id: str,
    issue_id: str,
    label: str = typer.Option(...),
    source: str = typer.Option(default="mcp-chrome"),
    dump_id: str = typer.Option(default=""),
    page_url: str = typer.Option(default=""),
    page_title: str = typer.Option(default=""),
    summary: str = typer.Option(default=""),
    dom_snapshot: str = typer.Option(default=""),
    dom_snapshot_file: str = typer.Option(default=""),
    console_message: list[str] = typer.Option([], "--console-message"),
    network_request: list[str] = typer.Option([], "--network-request"),
    screenshot_path: str = typer.Option(default=""),
    notes: str = typer.Option(default=""),
) -> None:
    payload = service.save_browser_dump(
        workspace_id,
        issue_id,
        BrowserDumpUpsertRequest(
            dump_id=dump_id or None,
            source=source,
            label=label,
            page_url=page_url or None,
            page_title=page_title or None,
            summary=_normalize_newlines(summary),
            dom_snapshot=_read_text_input(dom_snapshot, dom_snapshot_file),
            console_messages=[_normalize_newlines(item) for item in console_message],
            network_requests=[_normalize_newlines(item) for item in network_request],
            screenshot_path=screenshot_path or None,
            notes=_normalize_newlines(notes) or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("browser-dump-delete")
def browser_dump_delete(workspace_id: str, issue_id: str, dump_id: str) -> None:
    service.delete_browser_dump(workspace_id, issue_id, dump_id)
    _echo_ok(dump_id=dump_id)


@app.command("vulnerability-findings")
def vulnerability_findings(workspace_id: str, issue_id: str) -> None:
    _echo_json([item.model_dump(mode="json") for item in service.list_vulnerability_findings(workspace_id, issue_id)])


@app.command("vulnerability-finding-save")
def vulnerability_finding_save(
    workspace_id: str,
    issue_id: str,
    title: str = typer.Option(...),
    scanner: str = typer.Option(...),
    source: str = typer.Option(default="manual"),
    severity: str = typer.Option(default="medium"),
    status: str = typer.Option(default="open"),
    finding_id: str = typer.Option(default=""),
    summary: str = typer.Option(default=""),
    rule_id: str = typer.Option(default=""),
    location_path: str = typer.Option(default=""),
    location_line: int = typer.Option(default=0),
    cwe_id: list[str] = typer.Option([], "--cwe-id"),
    cve_id: list[str] = typer.Option([], "--cve-id"),
    reference: list[str] = typer.Option([], "--reference"),
    evidence: list[str] = typer.Option([], "--evidence"),
    threat_model_id: list[str] = typer.Option([], "--threat-model-id"),
    raw_payload: str = typer.Option(default=""),
) -> None:
    payload = service.save_vulnerability_finding(
        workspace_id,
        issue_id,
        VulnerabilityFindingUpsertRequest(
            finding_id=finding_id or None,
            scanner=scanner,
            source=source,
            severity=severity,
            status=status,
            title=title,
            summary=_normalize_newlines(summary),
            rule_id=rule_id or None,
            location_path=location_path or None,
            location_line=location_line or None,
            cwe_ids=[item.strip() for item in cwe_id if item.strip()],
            cve_ids=[item.strip() for item in cve_id if item.strip()],
            references=[_normalize_newlines(item) for item in reference if item.strip()],
            evidence=[_normalize_newlines(item) for item in evidence if item.strip()],
            threat_model_ids=[item.strip() for item in threat_model_id if item.strip()],
            raw_payload=_normalize_newlines(raw_payload) or None,
        ),
    )
    _echo_json(payload.model_dump(mode="json"))


@app.command("vulnerability-findings-import")
def vulnerability_findings_import(
    workspace_id: str,
    issue_id: str,
    source: str = typer.Option(...),
    payload: str = typer.Option(default=""),
    payload_file: str = typer.Option(default=""),
) -> None:
    imported = service.import_vulnerability_findings(
        workspace_id,
        issue_id,
        VulnerabilityImportRequest(
            source=source,
            payload=_read_text_input(payload, payload_file),
        ),
    )
    _echo_json([item.model_dump(mode="json") for item in imported])


@app.command("vulnerability-import-batches")
def vulnerability_import_batches(workspace_id: str, issue_id: str) -> None:
    payload = [item.model_dump(mode="json") for item in service.list_vulnerability_import_batches(workspace_id, issue_id)]
    _echo_json(payload)


@app.command("vulnerability-finding-delete")
def vulnerability_finding_delete(workspace_id: str, issue_id: str, finding_id: str) -> None:
    service.delete_vulnerability_finding(workspace_id, issue_id, finding_id)
    _echo_ok(finding_id=finding_id)


@app.command("vulnerability-report")
def vulnerability_report(
    workspace_id: str,
    issue_id: str,
    format: str = typer.Option(default="json"),
    output: str = typer.Option(default=""),
) -> None:
    selected_format = format.strip().lower() or "json"
    if selected_format == "markdown":
        payload = service.render_vulnerability_finding_report_markdown(workspace_id, issue_id)
        if output:
            Path(output).write_text(payload, encoding="utf-8")
            typer.echo(output)
            return
        typer.echo(payload)
        return
    report = service.get_vulnerability_finding_report(workspace_id, issue_id).model_dump(mode="json")
    if output:
        Path(output).write_text(json.dumps(report, indent=2), encoding="utf-8")
        typer.echo(output)
        return
    _echo_json(report)


@app.command("workspace-vulnerability-report")
def workspace_vulnerability_report(
    workspace_id: str,
    format: str = typer.Option(default="json"),
    output: str = typer.Option(default=""),
) -> None:
    selected_format = format.strip().lower() or "json"
    if selected_format == "markdown":
        payload = service.render_workspace_vulnerability_report_markdown(workspace_id)
        if output:
            Path(output).write_text(payload, encoding="utf-8")
            typer.echo(output)
            return
        typer.echo(payload)
        return
    report = service.get_workspace_vulnerability_report(workspace_id).model_dump(mode="json")
    if output:
        Path(output).write_text(json.dumps(report, indent=2), encoding="utf-8")
        typer.echo(output)
        return
    _echo_json(report)


@app.command("workspace-security-review-bundle")
def workspace_security_review_bundle(
    workspace_id: str,
    format: str = typer.Option(default="json"),
    output: str = typer.Option(default=""),
) -> None:
    selected_format = format.strip().lower() or "json"
    if selected_format == "markdown":
        payload = service.render_workspace_security_review_bundle_markdown(workspace_id)
        if output:
            Path(output).write_text(payload, encoding="utf-8")
            typer.echo(output)
            return
        typer.echo(payload)
        return
    bundle = service.get_workspace_security_review_bundle(workspace_id).model_dump(mode="json")
    if output:
        Path(output).write_text(json.dumps(bundle, indent=2), encoding="utf-8")
        typer.echo(output)
        return
    _echo_json(bundle)


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
    needs_followup: bool = typer.Option(False, "--needs-followup"),
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


@app.command("test-suggestions")
def test_suggestions(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.get_test_suggestions(workspace_id, issue_id))


@app.command("test-suggestions-generate")
def test_suggestions_generate(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.generate_test_suggestions(workspace_id, issue_id))


@app.command("coverage-parse")
def coverage_parse(
    workspace_id: str,
    report_path: str = typer.Option(...),
    run_id: str = typer.Option(default=""),
    issue_id: str = typer.Option(default=""),
) -> None:
    _echo_json(service.parse_coverage_report(workspace_id, report_path, run_id or None, issue_id or None))


@app.command("coverage-get")
def coverage_get(
    workspace_id: str,
    issue_id: str = typer.Option(default=""),
    run_id: str = typer.Option(default=""),
) -> None:
    payload = service.get_coverage(workspace_id, issue_id or None, run_id or None)
    if payload is None:
        raise typer.BadParameter("No coverage data found for the requested filters")
    _echo_json(payload)


@app.command("coverage-delta")
def coverage_delta(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.get_coverage_delta(workspace_id, issue_id))


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


@app.command("critique-generate")
def critique_generate(workspace_id: str, run_id: str) -> None:
    _echo_json(service.generate_patch_critique(workspace_id, run_id))


@app.command("critique-get")
def critique_get(workspace_id: str, run_id: str) -> None:
    payload = service.get_patch_critique(workspace_id, run_id)
    if payload is None:
        raise typer.BadParameter(f"No critique found for run: {run_id}")
    _echo_json(payload)


@app.command("improvements")
def improvements(workspace_id: str, run_id: str) -> None:
    _echo_json(service.get_run_improvements(workspace_id, run_id))


@app.command("improvement-dismiss")
def improvement_dismiss(
    workspace_id: str,
    run_id: str,
    suggestion_id: str,
    reason: str = typer.Option(default=""),
) -> None:
    _echo_json(
        service.dismiss_improvement(
            workspace_id,
            run_id,
            suggestion_id,
            DismissImprovementRequest(reason=reason or None),
        )
    )


@app.command("run-start")
def run_start(
    workspace_id: str,
    issue_id: str,
    runtime: str = typer.Option(...),
    model: str = typer.Option(...),
    instruction: str = typer.Option(default=""),
    runbook_id: str = typer.Option(default=""),
    eval_scenario_id: str = typer.Option(default=""),
    planning: bool = typer.Option(False, "--planning"),
) -> None:
    payload = service.start_issue_run(
        workspace_id,
        issue_id,
        runtime,
        model,
        instruction or None,
        runbook_id or None,
        eval_scenario_id or None,
        None,
        planning,
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


@app.command("run-insights")
def run_insights(workspace_id: str, run_id: str) -> None:
    _echo_json(service.get_run_session_insight(workspace_id, run_id))


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


@app.command("plan-generate")
def plan_generate(workspace_id: str, run_id: str) -> None:
    _echo_json(service.generate_run_plan(workspace_id, run_id))


@app.command("plan-get")
def plan_get(workspace_id: str, run_id: str) -> None:
    _echo_json(service.get_run_plan(workspace_id, run_id))


@app.command("plan-approve")
def plan_approve(
    workspace_id: str,
    run_id: str,
    feedback: str = typer.Option(default=""),
) -> None:
    _echo_json(service.approve_run_plan(workspace_id, run_id, PlanApproveRequest(feedback=feedback or None)))


@app.command("plan-reject")
def plan_reject(workspace_id: str, run_id: str, reason: str = typer.Option(...)) -> None:
    _echo_json(service.reject_run_plan(workspace_id, run_id, PlanRejectRequest(reason=reason)))


@app.command("plan-track")
def plan_track(
    workspace_id: str,
    run_id: str,
    ownership_mode: str = typer.Option(default=""),
    owner_label: str = typer.Option(default=""),
    attached_file: list[str] = typer.Option(default=[]),
    replace_attachments: bool = typer.Option(False, "--replace-attachments"),
    feedback: str = typer.Option(default=""),
) -> None:
    _echo_json(
        service.update_run_plan_tracking(
            workspace_id,
            run_id,
            PlanTrackingUpdateRequest(
                ownership_mode=ownership_mode or None,
                owner_label=owner_label or None,
                attached_files=attached_file,
                replace_attachments=replace_attachments,
                feedback=feedback or None,
            ),
        )
    )


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


@app.command("run-metrics")
def run_metrics(workspace_id: str, run_id: str) -> None:
    _echo_json(service.get_run_metrics(workspace_id, run_id))


@app.command("workspace-metrics")
def workspace_metrics(workspace_id: str) -> None:
    _echo_json(service.list_workspace_metrics(workspace_id))


@app.command("costs")
def costs(workspace_id: str) -> None:
    _echo_json(service.get_workspace_cost_summary(workspace_id))


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
    drift_only: bool = typer.Option(False, "--drift-only"),
    followup_only: bool = typer.Option(False, "--followup-only"),
    review_ready_only: bool = typer.Option(False, "--review-ready-only"),
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
    drift_only: bool = typer.Option(False, "--drift-only"),
    followup_only: bool = typer.Option(False, "--followup-only"),
    review_ready_only: bool = typer.Option(False, "--review-ready-only"),
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


@app.command("integrations")
def integrations(workspace_id: str) -> None:
    _echo_json(service.get_integration_configs(workspace_id))


@app.command("integration-configure")
def integration_configure(
    workspace_id: str,
    provider: str,
    setting: list[str] = typer.Option([], "--setting"),
    settings_json: str = typer.Option("", "--settings-json"),
) -> None:
    _echo_json(service.configure_integration(workspace_id, provider, _parse_settings(setting, settings_json)))


@app.command("integration-test")
def integration_test(
    provider: str,
    setting: list[str] = typer.Option([], "--setting"),
    settings_json: str = typer.Option("", "--settings-json"),
) -> None:
    payload = service.test_integration(
        IntegrationTestRequest(
            provider=provider,
            settings=_parse_settings(setting, settings_json),
        )
    )
    _echo_json(payload)


@app.command("github-import")
def github_import(workspace_id: str, repo: str, state: str = typer.Option(default="open")) -> None:
    _echo_json(service.import_github_issues(workspace_id, repo, state=state))


@app.command("github-pr")
def github_pr(
    workspace_id: str,
    run_id: str,
    issue_id: str,
    head_branch: str,
    base_branch: str = typer.Option(default="main"),
    title: str = typer.Option(default=""),
    body: str = typer.Option(default=""),
    draft: bool = typer.Option(False, "--draft"),
) -> None:
    _echo_json(
        service.create_github_pr(
            workspace_id,
            GitHubPRCreate(
                workspace_id=workspace_id,
                run_id=run_id,
                issue_id=issue_id,
                head_branch=head_branch,
                base_branch=base_branch,
                title=title or None,
                body=body or None,
                draft=draft,
            ),
        )
    )


@app.command("slack-notify")
def slack_notify(workspace_id: str, event: str, message: str = typer.Option(default="")) -> None:
    _echo_json(service.send_slack_notification(workspace_id, event, message=message or None))


@app.command("linear-sync")
def linear_sync(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.sync_issue_to_linear(workspace_id, issue_id))


@app.command("jira-sync")
def jira_sync(workspace_id: str, issue_id: str) -> None:
    _echo_json(service.sync_issue_to_jira(workspace_id, issue_id))


@app.command("terminal-open")
def terminal_open(
    workspace_id: str,
    cols: int = typer.Option(default=120),
    rows: int = typer.Option(default=36),
    terminal_id: str = typer.Option(default=""),
) -> None:
    _echo_json(service.open_terminal(workspace_id, cols, rows, terminal_id or None))


@app.command("terminal-write")
def terminal_write(terminal_id: str, data: str = typer.Option(...)) -> None:
    service.terminal_write(terminal_id, _normalize_newlines(data))
    _echo_ok(terminal_id=terminal_id)


@app.command("terminal-resize")
def terminal_resize(
    terminal_id: str,
    cols: int = typer.Option(...),
    rows: int = typer.Option(...),
) -> None:
    service.terminal_resize(terminal_id, cols, rows)
    _echo_ok(terminal_id=terminal_id)


@app.command("terminal-read")
def terminal_read(
    workspace_id: str,
    terminal_id: str,
    offset: int = typer.Option(default=0),
) -> None:
    _echo_json(service.terminal_read(workspace_id, terminal_id, offset))


@app.command("terminal-close")
def terminal_close(terminal_id: str) -> None:
    service.terminal_close(terminal_id)
    _echo_ok(terminal_id=terminal_id)


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
