"""Summarize rss_bench reports and check provenance against expected values.
Usage: summarize.py <expected.json|-> report.json...   (expected: {head, source_diff_sha256, untracked_source_sha256, binaries_sha256, script_sha256})"""
import json, sys
exp = json.load(open(sys.argv[1])) if sys.argv[1] != "-" else None
for path in sys.argv[2:]:
    try:
        r = json.load(open(path))["results"]
    except Exception as e:
        print(f"{path}: UNREADABLE {e!r}"); continue
    g, s, p = r.get("gate", {}), r.get("sampled_tree") or {}, r.get("provenance", {})
    bad = []
    if exp:
        for k, v in exp.items():
            if p.get(k) != v: bad.append(k)
    if p.get("core_bin_prebuilt") is not False: bad.append("core_bin_prebuilt")
    steps = {x["step"]: x["seconds"] for x in r.get("steps", [])}
    di = r.get("diagnostics_imports", {})
    print(f"{path.split('/')[-1]}: peak {s.get('peak_mb')} MB ({s.get('peak_mib')} MiB) step={s.get('peak_step')!r} samples={s.get('samples')} "
          f"gate valid={g.get('valid')} passed={g.get('passed')} invalid={g.get('invalid_reasons')} prov_mismatch={bad or 'none'}")
    print("   at peak:", [(q['comm'], round(q['rss_kib']/1024, 1), q['pid'], q['ppid']) for q in s.get('processes_at_peak', [])])
    print("   step peaks MB:", {k[:24]: v for k, v in (s.get('step_sampled_peak_mb') or {}).items() if v > 60})
    print(f"   idx step {steps.get('two concurrent index clients')} s; cli step {steps.get('CLI diagnostics imports with concurrent reads')} s; api_reads {di.get('api_reads')} statuses {di.get('read_statuses')} cli {di.get('cli_exit_codes')}")
