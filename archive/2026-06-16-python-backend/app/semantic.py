from __future__ import annotations

import json
from importlib import util as importlib_util
from pathlib import Path
import shutil
import subprocess
from types import SimpleNamespace
from typing import Optional

from .models import RepoMapSymbolRecord, SemanticPatternMatchRecord


TREE_SITTER_LANGUAGE_FALLBACKS = {
    ".py": "python",
    ".ts": "typescript",
    ".tsx": "tsx",
    ".js": "javascript",
    ".jsx": "javascript",
    ".go": "go",
    ".rs": "rust",
}

TREE_SITTER_KIND_MAP = {
    "class": "class",
    "function": "function",
    "method": "method",
    "interface": "type",
    "type": "type",
    "typealias": "type",
    "struct": "type",
    "trait": "type",
    "enum": "type",
    "module": "module",
}

AST_GREP_LANGUAGE_FALLBACKS = {
    ".py": "python",
    ".ts": "typescript",
    ".tsx": "tsx",
    ".js": "javascript",
    ".jsx": "javascript",
    ".go": "go",
    ".rs": "rust",
    ".java": "java",
    ".sh": "bash",
    ".html": "html",
    ".css": "css",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".json": "json",
}


def tree_sitter_available() -> bool:
    return importlib_util.find_spec("tree_sitter_language_pack") is not None


def detect_tree_sitter_language(path: Path) -> Optional[str]:
    api = _load_language_pack_api()
    if api is not None:
        try:
            detected = api.detect_language_from_path(path.name)
        except Exception:
            detected = None
        if isinstance(detected, str) and detected:
            return detected
    return TREE_SITTER_LANGUAGE_FALLBACKS.get(path.suffix.lower())


def ast_grep_binary() -> Optional[str]:
    return shutil.which("sg") or shutil.which("ast-grep")


def ast_grep_available() -> bool:
    return ast_grep_binary() is not None


def detect_ast_grep_language(path: Path) -> Optional[str]:
    return AST_GREP_LANGUAGE_FALLBACKS.get(path.suffix.lower())


def extract_path_symbols(relative_path: Path, content: str) -> tuple[list[RepoMapSymbolRecord], str, Optional[str]]:
    language = detect_tree_sitter_language(relative_path)
    api = _load_language_pack_api()
    if api is None or not language:
        return [], "none", language
    try:
        result = api.process(content, api.ProcessConfig(language=language))
    except Exception:
        return [], "none", language
    if not isinstance(result, dict):
        return [], "none", language

    records: list[RepoMapSymbolRecord] = []
    seen: set[tuple[str, str, Optional[int], str]] = set()

    def append_symbol(
        kind: str,
        name: Optional[str],
        span: Optional[dict],
        enclosing_scope: Optional[str] = None,
    ) -> None:
        if not name:
            return
        normalized_kind = TREE_SITTER_KIND_MAP.get(kind.lower())
        if not normalized_kind:
            return
        line_start = _span_line(span, "start_line")
        line_end = _span_line(span, "end_line")
        key = (relative_path.as_posix(), name, line_start, normalized_kind)
        if key in seen:
            return
        seen.add(key)
        records.append(
            RepoMapSymbolRecord(
                path=relative_path.as_posix(),
                symbol=name,
                kind=normalized_kind,
                line_start=line_start,
                line_end=line_end,
                enclosing_scope=enclosing_scope,
            )
        )

    def walk_structure(entries: list[dict], enclosing_scope: Optional[str] = None) -> None:
        for entry in entries:
            if not isinstance(entry, dict):
                continue
            kind = str(entry.get("kind") or "")
            name = entry.get("name")
            span = entry.get("span")
            append_symbol(kind, name if isinstance(name, str) else None, span if isinstance(span, dict) else None, enclosing_scope)
            child_scope = name if isinstance(name, str) and TREE_SITTER_KIND_MAP.get(kind.lower()) in {"class", "type", "module"} else enclosing_scope
            children = entry.get("children")
            if isinstance(children, list):
                walk_structure(children, child_scope)

    structure = result.get("structure")
    if isinstance(structure, list):
        walk_structure(structure)
    if not records:
        for entry in result.get("symbols") or []:
            if not isinstance(entry, dict):
                continue
            kind = str(entry.get("kind") or "")
            name = entry.get("name")
            span = entry.get("span")
            append_symbol(kind, name if isinstance(name, str) else None, span if isinstance(span, dict) else None)
    return records[:32], "tree_sitter", language


def run_ast_grep_query(
    root: Path,
    pattern: str,
    *,
    language: Optional[str] = None,
    path_glob: Optional[str] = None,
    limit: int = 50,
) -> tuple[list[SemanticPatternMatchRecord], Optional[str], Optional[str], bool]:
    binary = ast_grep_binary()
    if not binary:
        return [], None, "ast-grep binary is not installed on this machine.", False
    command = [binary, "run", "--pattern", pattern, "--json=stream"]
    if language:
        command.extend(["--lang", language])
    if path_glob:
        command.extend(["--globs", path_glob])
    command.append(str(root))
    try:
        completed = subprocess.run(command, capture_output=True, text=True, check=False)
    except FileNotFoundError:
        return [], None, "ast-grep binary is not available.", False
    if completed.returncode not in (0, 1):
        error = completed.stderr.strip() or completed.stdout.strip() or f"ast-grep exited with code {completed.returncode}"
        return [], binary, error, False
    matches: list[SemanticPatternMatchRecord] = []
    truncated = False
    for raw_line in completed.stdout.splitlines():
        raw_line = raw_line.strip()
        if not raw_line:
            continue
        try:
            item = json.loads(raw_line)
        except json.JSONDecodeError:
            continue
        if not isinstance(item, dict):
            continue
        file_path = item.get("file")
        text = item.get("text")
        if not isinstance(file_path, str) or not isinstance(text, str):
            continue
        relative_path = _relative_match_path(root, file_path)
        range_payload = item.get("range") or {}
        start = range_payload.get("start") if isinstance(range_payload, dict) else {}
        end = range_payload.get("end") if isinstance(range_payload, dict) else {}
        meta_variables = item.get("metaVariables") if isinstance(item.get("metaVariables"), dict) else {}
        single_meta = meta_variables.get("single") if isinstance(meta_variables, dict) else {}
        matches.append(
            SemanticPatternMatchRecord(
                path=relative_path,
                language=_normalize_ast_grep_language(item.get("language")),
                line_start=_one_based_index(start, "line"),
                line_end=_one_based_index(end, "line"),
                column_start=_one_based_index(start, "column"),
                column_end=_one_based_index(end, "column"),
                matched_text=text,
                context_lines=item.get("lines") if isinstance(item.get("lines"), str) else None,
                meta_variables=sorted(single_meta.keys()) if isinstance(single_meta, dict) else [],
            )
        )
        if len(matches) >= limit:
            truncated = True
            break
    return matches, binary, None, truncated


def _load_language_pack_api():
    if not tree_sitter_available():
        return None
    try:
        from tree_sitter_language_pack import ProcessConfig, detect_language_from_path, process
    except Exception:
        return None
    return SimpleNamespace(
        ProcessConfig=ProcessConfig,
        detect_language_from_path=detect_language_from_path,
        process=process,
    )


def _span_line(span: Optional[dict], key: str) -> Optional[int]:
    if not isinstance(span, dict):
        return None
    value = span.get(key)
    if not isinstance(value, int):
        return None
    return value + 1


def _relative_match_path(root: Path, file_path: str) -> str:
    candidate = Path(file_path)
    if candidate.is_absolute():
        try:
            return candidate.resolve().relative_to(root.resolve()).as_posix()
        except ValueError:
            return candidate.as_posix()
    return candidate.as_posix().lstrip("./")


def _one_based_index(payload: object, key: str) -> Optional[int]:
    if not isinstance(payload, dict):
        return None
    value = payload.get(key)
    if not isinstance(value, int):
        return None
    return value + 1


def _normalize_ast_grep_language(value: object) -> Optional[str]:
    if not isinstance(value, str) or not value:
        return None
    return value.lower()
