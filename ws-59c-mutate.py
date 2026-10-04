#!/usr/bin/env python3
"""WS-59c mutation runner: one mutation per new test, each on a saved copy.

Every subject is a byte-exact single-line substitution verified against the source
BEFORE any box time is spent (WS-29b's lesson: a guessed subject is wrong about as
often as it is right, and the check costs nothing and needs no box). Each mutation is
applied to a copy, run, and restored from the copy with a checksum comparison -- never
by a clean status, because a stale tree reads clean (FU/WS-FIX-29b).

Prints `subject=` lines and a SUMMARY. Exits 1 if any subject is missing from the
source, so a typo cannot masquerade as a survivor.
"""
import hashlib
import pathlib
import subprocess
import sys

WT = pathlib.Path("/private/tmp/claude-501/xm-wt/ws-59c")
EXACT = WT / "rust-core/src/exactscan.rs"
SEARCH = WT / "rust-core/src/search.rs"
KNOW = WT / "api-go/internal/workspaceops/knowledge.go"

# (id, file, old, new, cargo filter or None for the whole lib)
MUTATIONS = [
    ("M1", EXACT, 'let n = want.min(CHUNK_BYTES as u64) as usize;',
     'let n = want.min(CHUNK_BYTES as u64) as usize + 0;', "exactscan"),
    # the line cap: never reached
    ("M2", EXACT, "if report.spans.len() >= limits.max_lines {",
     "if report.spans.len() > limits.max_lines {", "exactscan"),
    # the byte cap: never reached
    ("M3", EXACT, "if report.bytes_scanned >= limits.max_bytes {",
     "if report.bytes_scanned > limits.max_bytes {", "exactscan"),
    # the time cap: never reached
    ("M4", EXACT, "if started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms) {",
     "if started.elapsed() > std::time::Duration::from_millis(limits.timeout_ms + 60000) {", "exactscan"),
    # the ceiling refusal becomes a clamp: an over-ceiling cap is silently honoured
    ("M5", EXACT, "if max_lines > DEFAULT_MAX_LINES {", "if false {", "exactscan"),
    # a zero cap answers nothing instead of taking the default
    ("M6", EXACT, "if max_lines > 0 {\n        l.max_lines = max_lines;\n    }",
     "l.max_lines = max_lines;", "exactscan"),
    # `exact` stops escaping the query, so it becomes `regex`
    ("M7", EXACT, "Matcher::Exact => regex::escape(query),", "Matcher::Exact => query.to_string(),",
     "exactscan"),
    # the regex ceiling check is dropped
    ("M8", EXACT, "if pattern.len() > MAX_MATCH_BYTES {", "if false {", "exactscan"),
    # the cursor's line numbering loses its base, so a pruned file reports line 1
    ("M9", EXACT, "Some((self.base_line + i, offset - self.starts[i] + 1))",
     "Some((i + 1, offset - self.starts[i] + 1))", "exactscan"),
    # the entry bound is dropped, so a many-short-lines file accumulates without limit
    ("M9b", EXACT, "if self.starts.len().saturating_sub(cut) > MAX_CURSOR_LINES {",
     "if false {", "exactscan"),
    # the byte bound is dropped, so the cursor grows with the file
    ("M9c", EXACT, "let keep_from = end.saturating_sub(MAX_MATCH_BYTES);",
     "let keep_from = 0;", "exactscan"),
    # the secret-path guard is dropped: a secret file's bytes are read
    ("M10", EXACT, "if crate::secretpath::is_secret_path(rel) {", "if false {",
     "search_tests::a_secret_path"),
    # two occurrences on one line collapse to one
    ("M11", EXACT, "if end <= carry_len {", "if false {", "search_tests"),
    # the scan reports hits as well as spans
    ("M12", SEARCH, "hits: Vec::new(),\n        spans,", "hits: Vec::new(),\n        spans,",
     "search_tests::exact_mode_reports_a_span_per_occurrence"),
    # `mode=exact` no longer dispatches to the scan, so it answers as the hybrid
    ("M13", SEARCH, "if opts.mode.is_scan() {\n        return scan(root, workspace_id, identity_key, query, opts);\n    }",
     "if false {\n        return scan(root, workspace_id, identity_key, query, opts);\n    }",
     "search_tests::exact_mode_reports_a_span_per_occurrence"),
    # the scan no longer applies the lang filter
    ("M14", SEARCH, "crate::index::lang::Lang::for_path(p).is_some_and(|l| l.name() == want),",
     "true,", "search_tests::the_scan_composes_with_path_glob_and_lang"),
    # an over-ceiling cap is silently clamped instead of refused
    ("M15", SEARCH, "if report.positions_lost > 0 {", "if false {",
     "search_tests::an_over_ceiling_cap_is_refused"),
    # the cap names are dropped from the Go argv
    ("M16", KNOW, 'coreArgs = append(coreArgs, cap.flag+strconv.Itoa(cap.value))',
     '_ = cap', "TestSearchSendsTheScanCapsOnlyWhenSet"),
    # the mode flag stops reaching the core
    ("M17", KNOW, 'coreArgs = append(coreArgs, "--mode="+req.Mode)',
     '_ = req.Mode', "TestSearchSendsTheScanModesToTheCore"),
    # the mode allow-list accepts anything again
    ("M18", KNOW, "if req.Mode != \"\" && !IsSearchMode(req.Mode) {", "if false {",
     "TestSearchRefusesAnUnknownModeBelowTheRoute"),
    # the cursor ranking key forgets the three caps
    ("M19", KNOW, 'strconv.Itoa(r.ScanLines), strconv.Itoa(r.ScanBytes), strconv.Itoa(r.ScanTimeoutMS)})',
     '})', "TestSearchCursorIsBoundToLangAndMode"),
]


def sha(p: pathlib.Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def main() -> int:
    missing = []
    for mid, f, old, new, _ in MUTATIONS:
        if not f.exists():
            missing.append(f"{mid}: {f} does not exist")
        elif old not in f.read_text():
            missing.append(f"{mid}: subject not byte-exact in {f}")
    if missing:
        print("MISSING SUBJECTS:")
        for m in missing:
            print(" ", m)
        return 1

    for mid, f, old, new, filt in MUTATIONS:
        print(f"subject={mid} file={f.relative_to(WT)} filter={filt}", flush=True)
        before = sha(f)
        backup = f.with_suffix(f.suffix + ".mutbak")
        backup.write_bytes(f.read_bytes())
        try:
            text = f.read_text()
            f.write_text(text.replace(old, new, 1))
            if sha(f) == before:
                print(f"  MUTATION {mid} DID NOT APPLY (bytes unchanged)")
                continue
            run = ["cargo", "test", "--lib", "--", filt] if filt != "go" else []
            if run:
                p = subprocess.run(run, cwd=WT / "rust-core", capture_output=True, text=True, timeout=1800)
            else:
                p = subprocess.run(
                    ["go", "test", "-count=1", "-run", filt, "./internal/workspaceops"],
                    cwd=WT / "api-go", capture_output=True, text=True, timeout=1800)
            out = p.stdout + p.stderr
            failed = "FAILED" in out or "FAIL" in out
            print(f"  {mid} {'KILLED (test failed as it must)' if failed else 'SURVIVED'}")
            if failed:
                for line in out.splitlines():
                    if "test result:" in line or line.startswith("test ") and "FAILED" in line:
                        print("   ", line.strip()[:160])
        finally:
            f.write_bytes(backup.read_bytes())
            backup.unlink()
            if sha(f) != before:
                print(f"  RESTORE MISMATCH on {mid}")
                return 1
    print("SUMMARY: all subjects verified byte-exact and every file restored")
    return 0


if __name__ == "__main__":
    sys.exit(main())