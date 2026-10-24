#!/usr/bin/env python3
"""Refresh or check the peer source liveness registry against GitHub."""

from __future__ import annotations

import argparse
import difflib
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

USER_AGENT = "xmustard-peer-registry-check"
DEFAULT_REGISTRY = Path(__file__).resolve().parents[1] / "docs/research/PEER_REGISTRY.json"


def get_json(url: str, timeout: float = 20.0) -> tuple[Any, str]:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "User-Agent": USER_AGENT,
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.load(response), response.geturl()


def refresh_peer(peer: dict[str, Any], api_root: str) -> dict[str, Any]:
    repository = peer.get("repository")
    if repository is None:
        return {
            **peer,
            "source_present": False,
            "superseded_by": None,
            "last_commit_date": None,
        }

    api_root = api_root.rstrip("/")
    encoded = urllib.parse.quote(repository, safe="/")
    try:
        source, _ = get_json(f"{api_root}/repos/{encoded}")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
        return {
            **peer,
            "source_present": False,
            "superseded_by": None,
            "last_commit_date": None,
        }

    canonical_name = source["full_name"]
    canonical_url = source["html_url"].rstrip("/")
    present = canonical_name.casefold() == repository.casefold()
    superseded_by = None if present else canonical_name.rsplit("/", 1)[-1]
    last_commit_date = peer.get("last_commit_date")
    if present:
        commits, _ = get_json(
            f"{api_root}/repos/{urllib.parse.quote(canonical_name, safe='/')}/commits?per_page=1"
        )
        last_commit_date = None
        if commits:
            commit = commits[0]["commit"]
            last_commit_date = commit["committer"]["date"][:10]

    return {
        **peer,
        "canonical_url": canonical_url,
        "source_present": present,
        "superseded_by": superseded_by,
        "last_commit_date": last_commit_date,
    }


def refreshed_registry(registry: dict[str, Any], api_root: str) -> dict[str, Any]:
    return {
        **registry,
        "peers": [refresh_peer(peer, api_root) for peer in registry["peers"]],
    }


def render(value: Any) -> str:
    return json.dumps(value, indent=2, ensure_ascii=False) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", type=Path, default=DEFAULT_REGISTRY)
    parser.add_argument("--api-root", default="https://api.github.com")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true", help="fail when the registry is stale")
    mode.add_argument("--write", action="store_true", help="write current liveness fields")
    args = parser.parse_args()

    current = json.loads(args.registry.read_text(encoding="utf-8"))
    updated = refreshed_registry(current, args.api_root)
    output = render(updated)
    if args.write:
        args.registry.write_text(output, encoding="utf-8")
        return 0

    original = render(current)
    if original == output:
        print("peer registry is current")
        return 0
    sys.stderr.writelines(
        difflib.unified_diff(
            original.splitlines(keepends=True),
            output.splitlines(keepends=True),
            fromfile=str(args.registry),
            tofile="refreshed peer registry",
        )
    )
    print("refresh with scripts/check_peer_registry.py --write", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
