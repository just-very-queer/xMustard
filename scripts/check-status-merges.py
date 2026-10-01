#!/usr/bin/env python3
"""Check that docs/STATUS.md's "In progress" section agrees with the merge history.

Every workstream row in that section must name a merge commit that exists and is
the workstream's own merge into feat/parity-v2, and every post-cut merge must have
a row. Read-only: it runs git plumbing and parses one markdown file.
"""
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

STATUS = Path("docs/STATUS.md")
# The section describes workstreams merged into feat/parity-v2 after the v0.1.1 cut;
# its own header says so. Merges below that cut are described in "What ships in
# v0.1.1" and "Measured" instead, and are out of scope here.
CUT = "09d2a1f"
# Merges that are not product workstreams and so have no STATUS.md row: the release
# merge-back, and the plan-hygiene WS-DOC rows (they change the plan, not the product).
NOT_WORKSTREAMS = {"rel-011"}
DOC_ROW = re.compile(r"^ws-doc-")


class CheckError(Exception):
    """The check cannot run: the checkout does not support what it needs."""


def git(*args: str) -> str:
    return subprocess.run(
        ["git", *args], check=True, capture_output=True, text=True
    ).stdout.strip()


def slug_of(ident: str) -> str:
    """WS-40a -> ws-40a, WS-CQ-01 -> ws-cq-01."""
    return "ws-" + ident.lower()


def merged_workstreams() -> dict[str, str]:
    """slug -> merge sha, for merges at or after CUT on the first-parent chain.

    The chain is walked to CUT inclusive. A checkout that does not contain CUT
    (a shallow clone, or history rewritten under it) reports that as a diagnostic
    failure rather than dying inside next().
    """
    lines = git("log", "--first-parent", "--format=%h %s").splitlines()
    cutoff_index = next(
        (i for i, l in enumerate(lines) if l.startswith(CUT + " ")), None
    )
    if cutoff_index is None:
        raise CheckError(
            f"cutoff commit {CUT} is not on the first-parent chain of this checkout; "
            "the post-cut window cannot be established (shallow clone?)"
        )
    kept: dict[str, str] = {}
    for line in lines[: cutoff_index + 1]:
        sha, subject = line.split(" ", 1)
        m = re.fullmatch(r"merge: parity/(\S+) into feat/parity-v2", subject)
        if m:
            kept[m.group(1)] = sha
    return kept


def section_rows(text: str) -> list[tuple[str, str | None]]:
    """The merged list's rows as (workstream id, sha or None)."""
    start = text.index("**Merged into `feat/parity-v2`")
    end = text.index("**On workstream branches", start)
    rows = []
    for line in text[start:end].splitlines():
        m = re.match(r"- \*\*WS-([0-9A-Za-z-]+)\*\*(?: \(`([0-9a-f]{7})`\))?:", line)
        if m:
            rows.append((m.group(1), m.group(2)))
    return rows


def check(text: str, merged: dict[str, str]) -> list[str]:
    """Every rule the section must satisfy, as a list of failure messages."""
    rows = section_rows(text)
    errors: list[str] = []

    unmerged = text[text.index("**On workstream branches"):]
    unmerged = unmerged[: unmerged.index("**Planned")]
    listed = {slug_of(i) for i in re.findall(r"\*\*WS-([0-9A-Za-z-]+)\*\*", unmerged)}
    # A workstream already merged must not still be listed as unmerged. One that has
    # not merged is legitimate there: that is what the list is for.
    stale = sorted(listed & set(merged))
    if stale:
        errors.append(f"still listed as unmerged: {', '.join(stale)}")

    named = {slug_of(ident) for ident, _ in rows}
    expected = {s for s in merged if s not in NOT_WORKSTREAMS and not DOC_ROW.match(s)}
    missing = sorted(expected - named)
    if missing:
        errors.append(f"merged but no row in the section: {', '.join(missing)}")

    for ident, sha in rows:
        slug = slug_of(ident)
        if sha is None:
            errors.append(f"WS-{ident} has no merge sha")
            continue
        if merged.get(slug) != sha:
            errors.append(
                f"WS-{ident} cites {sha}, but its merge is {merged.get(slug)}"
            )
            continue
        if git("cat-file", "-t", sha) != "commit":
            errors.append(f"WS-{ident} cites {sha}, which is not a commit")

    for line in ("This is not a parity claim",):
        if line not in text:
            errors.append(f"missing required sentence: {line!r}")

    for sha in re.findall(r"\]\((benchmarks/[^)]+)\)", text):
        if not (STATUS.parent / sha).is_file():
            errors.append(f"benchmark link does not resolve: {sha}")
    return errors


def self_test(text: str, merged: dict[str, str]) -> int:
    """Each rule, broken on purpose, must be reported. Nothing else may be."""
    base = section_rows(text)
    if not base:
        print("FAIL self-test: the real section parsed no rows", file=sys.stderr)
        return 1
    ident, sha = next((i, s) for i, s in base if s)

    start = text.index("**On workstream branches")
    end = text.index("**Planned", start)
    unmerged_block = text[start:end]

    cases = [
        ("a row dropped from the merged list",
         lambda t: t.replace(f"- **WS-{ident}** (`{sha}`)", "- (removed)", 1),
         "merged but no row in the section"),
        ("a row citing another workstream's merge",
         lambda t: t.replace(f"- **WS-{ident}** (`{sha}`)", f"- **WS-{ident}** (`{'0' * 7}`)", 1),
         f"WS-{ident} cites"),
        ("a row with no sha",
         lambda t: t.replace(f"- **WS-{ident}** (`{sha}`)", f"- **WS-{ident}**", 1),
         f"WS-{ident} has no merge sha"),
        ("a merged row still listed as unmerged",
         lambda t: t.replace(unmerged_block, unmerged_block + f"- **WS-{ident}** (`parity/ws-{ident.lower()}`)\n\n", 1),
         f"still listed as unmerged: ws-{ident.lower()}"),
        ("the parity claim removed",
         lambda t: t.replace("This is not a parity claim.", "This is a parity claim.", 1),
         "missing required sentence"),
        ("a benchmark link that resolves to nothing",
         lambda t: t + "\n[gone](benchmarks/does-not-exist.md)\n",
         "benchmark link does not resolve"),
    ]
    if check(text, merged):
        print("FAIL self-test: the real document does not pass its own check", file=sys.stderr)
        return 1
    # A row for a workstream that has not merged belongs in the unmerged list, so
    # listing one must NOT be reported. Without this the check would reject every
    # active branch and still pass this self-test.
    active = text.replace(
        unmerged_block,
        unmerged_block + "- **WS-NOTMERGED** (`parity/ws-notmerged`)\n\n",
        1,
    )
    if check(active, merged):
        print(
            "FAIL self-test: listing a not-yet-merged workstream was reported",
            file=sys.stderr,
        )
        return 1
    failures = []
    for name, break_it, want in cases:
        errors = check(break_it(text), merged)
        if not any(want in e for e in errors):
            failures.append(f"{name}: expected {want!r}, got {errors or 'no error'}")
    if failures:
        for f in failures:
            print(f"FAIL self-test {f}", file=sys.stderr)
        return 1
    print(f"SELF-TEST OK {len(cases)} rules, each caught when broken")
    return 0


def main() -> int:
    text = STATUS.read_text()
    rows = section_rows(text)
    merged = merged_workstreams()
    if "--self-test" in sys.argv:
        return self_test(text, merged)
    errors = check(text, merged)
    if errors:
        for e in errors:
            print(f"FAIL {e}", file=sys.stderr)
        return 1
    print(f"OK {len(rows)} merged rows, {len(merged)} post-cut merges, all shas agree")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except CheckError as exc:
        print(f"CHECK-CANNOT-RUN {exc}", file=sys.stderr)
        sys.exit(2)