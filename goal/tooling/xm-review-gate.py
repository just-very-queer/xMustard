#!/usr/bin/env python3
"""Merge gate for one workstream review round (v2, 2026-09-30).

usage: xm-review-gate.py <review-dir> <worktree> <base-sha> <author> [followups.md] [--leak-patterns FILE]

Exit 0 only when all of these hold:
  1. every channel file exists, is non-empty, and its sha256 equals the one xm-review-run.sh
     recorded in range.txt (channel outputs are not edited after the run);
  2. background.md (the plan section) is non-empty;
  3. reviewer.txt names a reviewer other than the author and other than range.txt's author=;
  4. every changed file of the reviewed range (merge_base..head from range.txt, not HEAD) is
     covered "reviewed" by at least one channel; a delegate (hand-written) coverage entry must
     carry a verbatim `quote` that occurs in diff.patch;
  5. every finding every channel produced (codex-spec, opencode, ocr-review, ocr-delegate, and
     each [P0]..[P3] line of codex-review.md) is referenced by a triage entry's source_refs;
  6. every triage entry is closed: fixed => fix_commit is a strict descendant of the reviewed
     head and contained in HEAD, with evidence; rejected => evidence, and for blocker/major a
     second_reviewer equal to reviewer.txt's reviewer; deferred => minor only, id in followups;
  7. slop-audit.md has the headings S1..S13; S1, S2 and S11 report rg_exit=1; the gate reruns
     S1 and S2 on the reviewed range and (with --leak-patterns) S11, and they must find nothing;
     every test listed under S5 has a mutation row (a line naming the test and containing FAIL);
  8. dogfood.md has at least three table rows.
"""
import hashlib, json, pathlib, re, subprocess, sys

SEALED = ["diff.patch", "background.md", "ocr-preview.json", "codex-review.md",
          "codex-spec.findings.json", "opencode.findings.json"]
OPTIONAL_SEALED = ["ocr-review.json"]
REQUIRED = SEALED + ["triage.json", "slop-audit.md", "dogfood.md", "reviewer.txt", "range.txt"]
COVERAGE = ["codex-spec.findings.json", "opencode.findings.json", "ocr-review.json", "ocr-delegate.findings.json"]
GATED = {"blocker", "major"}
S1 = r"\b(TODO|FIXME|XXX|HACK)\b|unimplemented!|todo!\(|not implemented|panic\(\"unimplemented|placeholder (value|impl|implementation)|stub(bed)? (out|for now)"
S2 = r"^\+\s*(//|#)\s*(now\b|first,|then\b|next,|step \d|this (function|method|test)\b|here we\b|we (now|then)\b|added\b|changed\b|updated\b|removed\b|fixed\b|new:|as requested|per (the )?review)"


def git(wt, *args):
    return subprocess.run(["git", "-C", wt, *args], capture_output=True, text=True)


def sha256(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def kv(text):
    return dict(line.split("=", 1) for line in text.splitlines() if "=" in line)


def findings_of(r, name):
    p = r / name
    if not p.is_file():
        return []
    doc = json.loads(p.read_text())
    return doc.get("findings", []) if isinstance(doc, dict) else []


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    leak_file = None
    if "--leak-patterns" in sys.argv:
        leak_file = sys.argv[sys.argv.index("--leak-patterns") + 1]
        args = [a for a in args if a != leak_file]
    rdir, wt, base, author = args[:4]
    followups = pathlib.Path(args[4]).read_text() if len(args) > 4 else ""
    r = pathlib.Path(rdir)
    errors = []

    # 1. files, seals
    for name in REQUIRED:
        if not (r / name).is_file() or (r / name).stat().st_size == 0:
            errors.append(f"missing or empty {name}")
    if not ((r / "ocr-review.json").is_file() or (r / "ocr-delegate.findings.json").is_file()):
        errors.append("missing ocr-review.json or ocr-delegate.findings.json")
    rng = kv((r / "range.txt").read_text()) if (r / "range.txt").is_file() else {}
    for name in SEALED + [n for n in OPTIONAL_SEALED if (r / n).is_file()]:
        want = rng.get(f"sha256 {name}")
        if not want:
            errors.append(f"range.txt has no sha256 for {name} (run xm-review-run.sh v2)")
        elif (r / name).is_file() and sha256(r / name) != want:
            errors.append(f"{name} changed after the run (sha256 mismatch)")

    # 2. spec present
    if (r / "background.md").is_file() and len((r / "background.md").read_text().strip()) < 40:
        errors.append("background.md is empty: the plan has no section for this workstream; add the spec-only commit first")

    # 3. reviewer identity
    rev = kv((r / "reviewer.txt").read_text()).get("reviewer", "").strip() if (r / "reviewer.txt").is_file() else ""
    if not rev:
        errors.append("reviewer.txt has no reviewer= line")
    elif rev in (author, rng.get("author", "").strip()):
        errors.append(f"reviewer {rev!r} equals the author")

    # 4. coverage on the reviewed range
    mb, hd = rng.get("merge_base", base).strip(), rng.get("head", "").strip()
    if not hd:
        errors.append("range.txt has no head=")
    changed = set(git(wt, "diff", "--name-only", mb, hd or "HEAD").stdout.split())
    diff_text = (r / "diff.patch").read_text(errors="replace") if (r / "diff.patch").is_file() else ""
    covered = set()
    for name in COVERAGE:
        p = r / name
        if not p.is_file():
            continue
        doc = json.loads(p.read_text())
        for c in doc.get("coverage", []):
            if c.get("status") != "reviewed":
                continue
            if name == "ocr-delegate.findings.json":
                q = (c.get("quote") or "").strip()
                if not q or q not in diff_text:
                    errors.append(f"delegate coverage for {c.get('path')} has no verbatim quote from diff.patch")
                    continue
            covered.add(c["path"])
    errors += [f"not reviewed by any channel: {p}" for p in sorted(changed - covered)]

    # 5. every channel finding is triaged
    triage = json.loads((r / "triage.json").read_text()) if (r / "triage.json").is_file() else []
    refs = set()
    for f in triage:
        refs |= set(f.get("source_refs", []))
    expected = set()
    for name in COVERAGE:
        chan = name.split(".")[0]
        for i, _ in enumerate(findings_of(r, name), 1):
            expected.add(f"{chan}:{i}")
    if (r / "codex-review.md").is_file():
        n = len(re.findall(r"\[P[0-3]\]", (r / "codex-review.md").read_text(errors="replace")))
        expected |= {f"codex-review:{i}" for i in range(1, n + 1)}
    errors += [f"channel finding {ref} is not referenced by any triage entry (source_refs)" for ref in sorted(expected - refs)]

    # 6. closure
    for f in triage:
        fid, sev, status = f.get("id", "?"), f.get("severity"), f.get("status")
        ev = (f.get("evidence") or "").strip()
        fix = f.get("fix_commit") or ""

        def fixed_ok():
            if not (ev and fix and hd):
                return False
            same = git(wt, "rev-parse", fix).stdout.strip() == git(wt, "rev-parse", hd).stdout.strip()
            after_head = git(wt, "merge-base", "--is-ancestor", hd, fix).returncode == 0
            in_branch = git(wt, "merge-base", "--is-ancestor", fix, "HEAD").returncode == 0
            return after_head and in_branch and not same

        closes = {
            "fixed": (fixed_ok, "fixed needs evidence and a fix_commit that is a strict descendant of the reviewed head and contained in HEAD"),
            "rejected": (lambda: bool(ev) and (sev not in GATED or (f.get("second_reviewer") == rev and rev != author)),
                         "rejected needs evidence, and for a blocker/major second_reviewer must equal reviewer.txt's reviewer"),
            "deferred": (lambda: sev not in GATED and fid in followups,
                         "only minors may be deferred, and the id must appear in the followups file"),
        }
        ok, why = closes.get(status, (lambda: False, f"status {status!r} does not close a finding"))
        if sev not in GATED | {"minor"}:
            errors.append(f"{fid}: severity {sev!r} is not blocker, major or minor")
        elif not f.get("source_refs"):
            errors.append(f"{fid}: no source_refs (which channel finding is this?)")
        elif not ok():
            errors.append(f"{fid} ({sev}): {why}")

    # 7. slop audit
    audit = (r / "slop-audit.md").read_text(errors="replace") if (r / "slop-audit.md").is_file() else ""
    for i in range(1, 14):
        if not re.search(rf"^## S{i}\b", audit, re.M):
            errors.append(f"slop-audit.md lacks heading S{i}")
    for s in ("S1", "S2", "S11"):
        sec = re.search(rf"^## {s}\b.*?(?=^## S\d|\Z)", audit, re.M | re.S)
        if sec and "rg_exit=1" not in sec.group(0):
            errors.append(f"slop-audit.md {s} does not report rg_exit=1")
    added = [l for l in git(wt, "diff", "-U0", mb, hd or "HEAD").stdout.splitlines() if l.startswith("+") and not l.startswith("+++")]
    if any(re.search(S1, l) for l in added):
        errors.append("gate rerun of S1 found a stub/TODO marker in the reviewed range")
    code_added = [l for l in git(wt, "diff", "-U0", mb, hd or "HEAD", "--", "*.go", "*.rs", "*.ts").stdout.splitlines() if l.startswith("+") and not l.startswith("+++")]
    if any(re.search(S2, l, re.I) for l in code_added):
        errors.append("gate rerun of S2 found a narrating comment in the reviewed range")
    if leak_file:
        pats = [p for p in pathlib.Path(leak_file).read_text().splitlines() if p.strip()]
        hits = sum(1 for l in added for p in pats if p in l)
        if hits:
            errors.append(f"gate rerun of S11 found {hits} box-identifier hit(s) in the reviewed range")
    s5 = re.search(r"^## S5\b.*?(?=^## S\d|\Z)", audit, re.M | re.S)
    if s5:
        tests = [t for t in re.findall(r"^(?:Test\w+|[a-z_][a-z0-9_]+)$", s5.group(0), re.M) if t not in ("done",)]
        for t in tests:
            if not any(t in l and "FAIL" in l for l in audit.splitlines() if not l.startswith("$")):
                errors.append(f"new test {t} has no mutation row (a line naming it with its FAIL output)")

    # 8. dogfood
    if (r / "dogfood.md").is_file():
        rows = [l for l in (r / "dogfood.md").read_text().splitlines() if l.startswith("|") and not re.match(r"^\|\s*-", l)]
        if len(rows) < 4:  # header + 3
            errors.append("dogfood.md has fewer than three table rows")

    for e in errors:
        print("GATE FAIL:", e)
    print(f"gate={'PASS' if not errors else 'FAIL'} changed={len(changed)} covered={len(changed & covered)} "
          f"channel_findings={len(expected)} triaged={len(triage)} reviewer={rev or '-'}")
    sys.exit(1 if errors else 0)


if __name__ == "__main__":
    main()
