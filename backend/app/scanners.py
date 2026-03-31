from __future__ import annotations

import hashlib
import json
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from .models import DiscoverySignal, EvidenceRef, IssueRecord, SourceRecord

BUG_HEADER_RE = re.compile(r"^###\s+(P\d_\d{2}M\d{2}_\d{3})\.\s+(.*)$")
STATUS_RE = re.compile(r"^- Status \(([^)]+)\):\s*(.*)$")
EVIDENCE_RE = re.compile(r"`([^`:]+)(?::(\d+))?`")


def normalize_status(value: str) -> str:
    lowered = value.lower()
    if "already fixed" in lowered or lowered.startswith("fixed"):
        return "fixed"
    if "partial" in lowered:
        return "partial"
    if "resolved" in lowered:
        return "resolved"
    return "open"


def extract_evidence(value: str) -> list[EvidenceRef]:
    items: list[EvidenceRef] = []
    for match in EVIDENCE_RE.finditer(value):
        path, line = match.groups()
        lowered = path.lower().strip()
        if " " in lowered or lowered.startswith(("python", "pytest", "npm ", "10 passed", "1 passed", "2 passed")):
            continue
        if "/" not in lowered and "." not in Path(lowered).name:
            continue
        items.append(EvidenceRef(path=path, line=int(line) if line else None))
    return items


def latest_bug_ledger(root_path: Path) -> Optional[Path]:
    bug_dir = root_path / "docs" / "bugs"
    if not bug_dir.exists():
        return None
    candidates = [
        path
        for path in bug_dir.glob("Bugs_*.md")
        if "_status_" not in path.name and "/tracker/" not in str(path)
    ]
    if not candidates:
        return None
    return sorted(candidates, key=lambda path: path.name)[-1]


def verdict_bundles(root_path: Path) -> list[Path]:
    search_roots = [root_path, root_path.parent]
    candidates: set[Path] = set()
    for search_root in search_roots:
        if not search_root.exists():
            continue
        candidates.update(search_root.rglob("*_verdicts.json"))
    return sorted(candidates, key=lambda path: str(path))


def file_timestamp(path: Path) -> str:
    return datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc).isoformat()


def parse_ledger(ledger_path: Path) -> list[IssueRecord]:
    lines = ledger_path.read_text(encoding="utf-8").splitlines()
    issues: list[IssueRecord] = []
    current_id: Optional[str] = None
    current_title = ""
    bucket: list[str] = []

    def flush() -> None:
        nonlocal current_id, current_title, bucket
        if not current_id:
            return
        issue = build_issue_from_block(ledger_path, current_id, current_title, bucket)
        issues.append(issue)
        current_id = None
        current_title = ""
        bucket = []

    for line in lines:
        header = BUG_HEADER_RE.match(line)
        if header:
            flush()
            current_id, current_title = header.groups()
            continue
        if current_id:
            bucket.append(line)
    flush()
    return issues


def build_issue_from_block(ledger_path: Path, bug_id: str, title: str, lines: list[str]) -> IssueRecord:
    summary = None
    impact = None
    notes: list[str] = []
    evidence: list[EvidenceRef] = []
    verification_evidence: list[EvidenceRef] = []
    tests_added: list[str] = []
    tests_passed: list[str] = []
    doc_status = "open"
    verified_at = None
    in_evidence = False
    in_verification = False

    for raw_line in lines:
        line = raw_line.strip()
        if line.startswith("- Summary:"):
            summary = line.replace("- Summary:", "", 1).strip()
        elif line.startswith("- Impact:"):
            impact = line.replace("- Impact:", "", 1).strip()
        elif line.startswith("- Evidence:"):
            in_evidence = True
            in_verification = False
        elif line.startswith("- Verification/update evidence:"):
            in_verification = True
            in_evidence = False
        elif line.startswith("- Remaining gap:") or line.startswith("- Correction"):
            notes.append(line[2:].strip())
            in_evidence = False
            in_verification = False
        else:
            status_match = STATUS_RE.match(raw_line)
            if status_match:
                verified_at, status_value = status_match.groups()
                doc_status = normalize_status(status_value)
                continue
            if in_evidence and line.startswith("- "):
                evidence.extend(extract_evidence(line))
                if "pytest" in line or "npm run" in line:
                    tests_passed.append(line[2:].strip())
            elif in_verification and line.startswith("- "):
                verification_evidence.extend(extract_evidence(line))
                if "pytest" in line or "npm run" in line:
                    tests_passed.append(line[2:].strip())
            elif line.startswith("- ") and "test/" in line:
                tests_added.append(line[2:].strip())

    issue_status = "resolved" if doc_status in {"fixed", "resolved"} else "partial" if doc_status == "partial" else "open"
    fingerprint = hashlib.sha1(f"{ledger_path}:{bug_id}:{title}".encode("utf-8")).hexdigest()
    return IssueRecord(
        bug_id=bug_id,
        title=title,
        severity=bug_id.split("_", 1)[0],
        issue_status=issue_status,
        source="ledger",
        source_doc=str(ledger_path),
        doc_status=doc_status,
        summary=summary,
        impact=impact,
        evidence=evidence,
        verification_evidence=verification_evidence,
        tests_added=tests_added,
        tests_passed=tests_passed,
        notes=" | ".join(notes) or None,
        verified_at=verified_at,
        verified_by="ledger",
        needs_followup=doc_status != "fixed",
        fingerprint=fingerprint,
    )


def load_verdict_items(verdict_path: Path) -> list[dict]:
    payload = json.loads(verdict_path.read_text(encoding="utf-8"))
    if isinstance(payload, list):
        return [item for item in payload if isinstance(item, dict)]
    if isinstance(payload, dict):
        wrapped = payload.get("verdicts")
        if isinstance(wrapped, list):
            return [item for item in wrapped if isinstance(item, dict)]
    return []


def build_source_records(
    ledger_path: Optional[Path],
    issues: list[IssueRecord],
    verdict_paths: list[Path],
    signals: list[DiscoverySignal],
) -> list[SourceRecord]:
    sources: list[SourceRecord] = []
    if ledger_path and ledger_path.exists():
        sources.append(
            SourceRecord(
                source_id=f"src_{hashlib.sha1(str(ledger_path).encode('utf-8')).hexdigest()[:10]}",
                kind="ledger",
                label=ledger_path.name,
                path=str(ledger_path),
                record_count=len(issues),
                modified_at=file_timestamp(ledger_path),
                notes="Canonical markdown ledger source.",
            )
        )
    for verdict_path in verdict_paths:
        items = load_verdict_items(verdict_path)
        sources.append(
            SourceRecord(
                source_id=f"src_{hashlib.sha1(str(verdict_path).encode('utf-8')).hexdigest()[:10]}",
                kind="verdict_bundle",
                label=verdict_path.name,
                path=str(verdict_path),
                record_count=len(items),
                modified_at=file_timestamp(verdict_path),
                notes="Recursive verdict bundle ingestion.",
            )
        )
    scanner_counts: dict[str, int] = {}
    for signal in signals:
        scanner_counts[signal.kind] = scanner_counts.get(signal.kind, 0) + 1
    for kind, count in sorted(scanner_counts.items()):
        sources.append(
            SourceRecord(
                source_id=f"src_scanner_{kind}",
                kind="scanner",
                label=kind,
                path=kind,
                record_count=count,
                notes="Auto-discovery heuristic feed.",
            )
        )
    return sources


def apply_verdicts(issues: list[IssueRecord], verdict_paths: list[Path], repo_root: Path) -> list[IssueRecord]:
    verdict_by_id: dict[str, dict] = {}
    for verdict_path in verdict_paths:
        for item in load_verdict_items(verdict_path):
            bug_id = item.get("id")
            if isinstance(bug_id, str):
                verdict_by_id[bug_id] = item
    updated: list[IssueRecord] = []
    for issue in issues:
        verdict = verdict_by_id.get(issue.bug_id)
        if not verdict:
            updated.append(issue)
            continue
        code_status = normalize_status(str(verdict.get("verdict", "open")))
        verification_evidence = list(issue.verification_evidence)
        for evidence_item in verdict.get("evidence", []):
            if not isinstance(evidence_item, str):
                continue
            path, _, line = evidence_item.partition(":")
            normalized = normalize_evidence_path(repo_root, path)
            verification_evidence.append(
                EvidenceRef(
                    path=path,
                    line=int(line) if line.isdigit() else None,
                    normalized_path=normalized,
                    path_exists=(repo_root / normalized).exists() if normalized else False,
                    path_scope="repo-relative" if normalized else "unresolved",
                )
            )
        drift_flags = list(issue.drift_flags)
        if issue.doc_status == "fixed" and code_status != "fixed":
            drift_flags.append("doc_fixed_code_not_fixed")
        if issue.doc_status != "fixed" and code_status == "fixed":
            drift_flags.append("code_fixed_doc_not_fixed")
        updated.append(
            issue.model_copy(
                update={
                    "code_status": code_status,
                    "verification_evidence": verification_evidence,
                    "verified_by": "codex",
                    "notes": verdict.get("concise_restatement") or issue.notes,
                    "drift_flags": sorted(set(drift_flags)),
                    "needs_followup": code_status != "fixed" or issue.doc_status != "fixed",
                }
            )
        )
    return updated


def normalize_evidence_path(repo_root: Path, raw_path: str) -> Optional[str]:
    candidate = Path(raw_path)
    if candidate.is_absolute():
        try:
            return str(candidate.resolve().relative_to(repo_root.resolve()))
        except ValueError:
            return None
    relative = candidate.as_posix().lstrip("./")
    if (repo_root / relative).exists():
        return relative
    return relative


def scan_repo_signals(root_path: Path) -> list[DiscoverySignal]:
    commands = [
        ("annotation", [r"TODO|FIXME|BUG|HACK|XXX"], "P2", "Backlog annotation in code"),
        ("exception_swallow", [r"except Exception:\s*(pass|continue|return None|return)"], "P1", "Swallowed generic exception"),
        ("not_implemented", [r"NotImplementedError|raise NotImplemented"], "P2", "Not implemented code path"),
        ("test_marker", [r"xfail|skip\(|todo"], "P3", "Deferred or skipped test coverage"),
    ]
    signals: list[DiscoverySignal] = []
    for kind, patterns, severity, default_title in commands:
        for pattern in patterns:
            signals.extend(run_ripgrep_signal_scan(root_path, kind, severity, default_title, pattern))
    deduped: dict[str, DiscoverySignal] = {}
    for signal in signals:
        deduped[signal.fingerprint] = signal
    return list(sorted(deduped.values(), key=lambda item: (item.severity, item.file_path, item.line)))


def run_ripgrep_signal_scan(
    root_path: Path,
    kind: str,
    severity: str,
    title: str,
    pattern: str,
) -> list[DiscoverySignal]:
    command = [
        "rg",
        "-n",
        "--hidden",
        "--glob",
        "!.git",
        "--glob",
        "!node_modules",
        "--glob",
        "!dist",
        "--glob",
        "!build",
        pattern,
        str(root_path),
    ]
    try:
        completed = subprocess.run(command, capture_output=True, text=True, check=False)
    except FileNotFoundError:
        return []

    if completed.returncode not in (0, 1):
        return []

    signals: list[DiscoverySignal] = []
    for line in completed.stdout.splitlines():
        file_path, sep, rest = line.partition(":")
        if not sep:
            continue
        line_number_text, sep, content = rest.partition(":")
        if not sep or not line_number_text.isdigit():
            continue
        line_number = int(line_number_text)
        relative_path = str(Path(file_path).resolve().relative_to(root_path.resolve()))
        fingerprint = hashlib.sha1(f"{kind}:{relative_path}:{line_number}:{content.strip()}".encode("utf-8")).hexdigest()
        signals.append(
            DiscoverySignal(
                signal_id=f"sig_{fingerprint[:12]}",
                kind=kind,
                severity=severity,
                title=title,
                summary=content.strip()[:280],
                file_path=relative_path,
                line=line_number,
                evidence=[EvidenceRef(path=relative_path, line=line_number, excerpt=content.strip())],
                tags=[kind, "auto-discovery"],
                fingerprint=fingerprint,
            )
        )
    return signals


def list_tree_nodes(root_path: Path, relative_path: str = "", limit: int = 80) -> list[dict]:
    target = (root_path / relative_path).resolve()
    entries = []
    for child in sorted(target.iterdir(), key=lambda path: (path.is_file(), path.name.lower()))[:limit]:
        entries.append(
            {
                "path": str(child.relative_to(root_path)),
                "name": child.name,
                "node_type": "directory" if child.is_dir() else "file",
                "has_children": child.is_dir() and any(True for _ in child.iterdir()),
                "size_bytes": None if child.is_dir() else child.stat().st_size,
            }
        )
    return entries


def summarize_tree(root_path: Path) -> dict[str, int]:
    files = 0
    directories = 0
    for path in root_path.rglob("*"):
        if ".git" in path.parts or "node_modules" in path.parts or "dist" in path.parts or "build" in path.parts:
            continue
        if path.is_dir():
            directories += 1
        else:
            files += 1
    return {"files": files, "directories": directories}
