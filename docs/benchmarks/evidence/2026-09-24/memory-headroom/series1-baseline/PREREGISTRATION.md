# Series 1 — Phase 1 baseline (characterization; no pass/fail outcome)

Registered 2026-09-24T15:32:37Z before the first attempt. Plan sha256 745d1595dd96e48416d36b5266d827f51ca055731953ebbf02a0b8b469e41acb.

Attempts (exactly five, back-to-back, one host session, no sixth attempt, no rerun on failure):
S1-A1..S1-A5 -> series1-baseline/S1-A{1..5}.json (+ .sidecar.txt, .log, .vm_stat/.swap before/after)

Launcher: tooling/gate_attempt.sh (sha256 154ce975fccfb3ac1fd78589600d269f4c4f18a85541567e9cd01a61803a87e5), tree /private/tmp/xmustard-opus-l9b1F4 (revision A, unchanged repaired diagnostics source).

Expected provenance (a mismatch makes the attempt invalid):
- head: cd13e2b78a9fadcbcd760b19442d5992136b1db0
- source_diff_sha256: a5aaf5d72914e738f7d8bb5569680bb9f081a4773034b922141a767a6ffec0a6
- untracked_source_files / untracked_source_sha256: 39 8562a222dbdbe62fffc545087173cb3932c12824909a05fbd8ea19c2bfd37719
- binaries_sha256: xmustard-api 3a1c6d67044693164573ecec3bff700dd1bb25500dae7c8c2b9f30f463479f77, xmustard-mcp 7045b1daf7e234fc19b23ca909e86693a155c23108174e423a3cd721079e86b9, xmustard-core d644743ba005318ebbce451d7924ce4bcbefb26be057a48ff64f24ff430c3576 (prebuild record: prebuild/A-prebuild.txt; no -tags)
- core_bin_prebuilt: false
- script_sha256: rss_bench.py 7c7254e0282379e60aa19eb20a9ba040344533c6097758eb91a91089edf5faa9, harness.py 973147225ffce59f2d5af01fd71ba06cc0d0d74964f140e00365bb4ae129d5bc; rss.sh 7d8d9e545e507e980faf8fa67658d346122bebdc5c4b0d88af62fa11277ceb51 (sidecar only)
