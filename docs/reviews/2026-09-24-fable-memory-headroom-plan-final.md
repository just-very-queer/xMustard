# Plan final audit: fixed-workload RSS headroom — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: `docs/plans/2026-09-24-memory-headroom.md`, sha256
`745d1595dd96e48416d36b5266d827f51ca055731953ebbf02a0b8b469e41acb` (186 lines; the file
changed once during this audit, from `d8ee997a…` to `745d1595…`, altering only the
sidecar env check on lines 53–54 to a names-only form; the audit is of `745d1595…`).
Prior reviews: `…-plan.md` (R1–R6) and `…-plan-recheck.md` (B1, B2; sha256
`e6eb0a87…`). Read in the isolated worktree `/private/tmp/xmustard-opus-l9b1F4`
(base `cd13e2b`, `source_diff_sha256` `a5aaf5d7…`, 39 untracked source files,
`untracked_source_sha256` `8562a222…`, both equal to the saved tuning reports' provenance).
This file is the only write. No gate run, no source, plan, benchmark, main-checkout,
commit, push or merge.

## Verdict: APPROVED

Both recheck blockers are closed by concrete, checkable text. The four non-blocking
clarifications are also addressed. No contradiction was introduced. Three optional
precision notes follow; none changes the verdict.

## B1 — `XMUSTARD_CORE_BIN` and persisted Go build flags: closed

| Requirement | Plan text | Verified against |
|---|---|---|
| Unset the raw-`os.environ` core override | Launch command (line 59) now includes `-u GOMAXPROCS -u XMUSTARD_CORE_BIN` | `harness.py:237` still reads `os.environ.get("XMUSTARD_CORE_BIN")` outside `clean_env`; the `env -u` removes it |
| Attest it was unset | Sidecar env check (53–54) covers the `XMUSTARD_` prefix, names only | Ambient `GO*`, `XMUSTARD_*`, `RUSTFLAGS`, `CARGO_*` are empty on this host today |
| Detect substitution after the fact | `core_bin_prebuilt=false` and core hash equal to a fresh in-worktree `cargo build --release` (61–63) | Report field exists (`rss_bench.py:425`); the six saved tuning reports carry `core_bin_prebuilt: true`, so this rule is stricter than the historical runs, as intended |
| Persisted `go env -w` values | `go env GOFLAGS GODEBUG` must be empty (54–55); prebuilt API/MCP hashes and `go version -m` recorded, no `-tags` (63–66); each gate report's binary hashes must match | `go env GOFLAGS GODEBUG` empty; `GOENV` file empty; `rg 'SetGCPercent|SetMemoryLimit|//go:debug|net/http/pprof'` over `api-go` non-test sources: no matches |

Feasibility of the hash-match rule is confirmed independently. A sanitized
`go build -o <job-tmp>/bin/xmustard-api ./cmd/xmustard-api` from this worktree produced
`3a1c6d67…f77`, and the shim `7045b1da…86b9`, identical to the binaries hashed in all six
saved tuning reports and in the prior review (line 44). The in-worktree
`rust-core/target/release/xmustard-core` hashes `d644743b…`, also identical. Output path
does not enter the Go binary, so an out-of-band prebuild is a valid attestation of the
in-harness build. Note that `go version -m` on these binaries shows no `vcs.*` stamp
(only `-buildmode`, `-compiler`, `CGO_*`, `GOARCH`, `GOOS`, `GOARM64`), so a hash match
attests build inputs and toolchain, not `head`; the report's `head`,
`source_diff_sha256` and `untracked_source_sha256` carry that, which the plan also requires.

## B2 — untracked benchmark scripts in A, preregistered provenance per slot: closed

- Lines 101–110: A is a separate detached worktree at `cd13e2b` with the exact tracked diff
  and untracked manifest reproduced; `scripts/bench/` and the `scripts/e2e/` files
  `harness.py` needs are copied in; the three pinned script hashes are verified there;
  the tracked-diff and untracked-source hashes must match the repaired isolated tree
  before B is edited; A is read-only; A and B binaries are rebuilt and recorded; the
  comparison is rejected if A cannot be reproduced exactly.
- Lines 148–152: before the first A/B attempt, `head`, `source_diff_sha256`,
  `untracked_source_sha256`, `binaries_sha256` and `script_sha256` are preregistered for
  A and for B; each A attempt runs through A's own `rss.sh`, each B through B's; a report
  whose provenance does not match its registered revision is invalid.
- The pinned script hashes were re-verified unchanged: `rss_bench.py` `7c7254e0…faa9`,
  `harness.py` `97314722…3dc`, `rss.sh` `7d8d9e54…eb51`. `rss.sh` does
  `cd "$(dirname "$0")/../.."` and `harness.py` derives `REPO_ROOT` from its own path, so
  launching A's copy benchmarks A's tree.

## Recheck clarifications: all addressed

- Baseline series has no pass/fail outcome; a failed baseline never triggers a rerun
  (72–74).
- An invalid A voids only the attribution claim, not valid B gate evidence (160–161).
- Fifteen full gate runs stated explicitly; Phase 1 reports stored under the same
  series/sidecar convention (145–148).
- The HEAD-move `stale` regression must go through the real GET handler path (118–120).

## Contradiction check: none found

Checked pairwise: status line vs. Phase 1.3/Phase 2 (plan is "proposed"; edits are
conditional on approval, unchanged from R-round wording); "do not edit benchmark source"
vs. copying benchmark files into A (copy, not edit; hashes verified); launch command's
`env -u` list vs. sidecar grep list (same five `GO*` names plus `XMUSTARD_`); attempt
counts (5 + 10 = 15); "in-worktree cargo build" vs. `core_bin_prebuilt=false` (the harness
performs exactly that build when the override is unset). The valid-A-failure and
invalid-A rules are mutually consistent with the "five valid passing B reports" gate.

## Optional precision notes (non-blocking)

1. The frozen report's `script_sha256` records only `rss_bench.py` and `harness.py`
   (`rss_bench.py:411–413`); `rss.sh`'s pinned hash is therefore verifiable only in the
   sidecar. Line 47 already requires it; naming it as a sidecar line would make the
   check explicit.
2. Phase 1 baseline attempts and acceptance A-slots are both labelled `A1`–`A5`. Series
   numbers (lines 74, 162) disambiguate them; prefixing labels with the series number in
   the preregistration would remove the reading ambiguity.
3. The sidecar attests Go inputs only. `RUSTFLAGS`, `CARGO_*` and a `.cargo/config.toml`
   would change the core binary without changing any recorded input; all are absent on
   this host today. Adding `env | cut -d= -f1 | grep -E '^(RUSTFLAGS|CARGO_)'` (must be
   empty) and "no `.cargo/config.toml`" to the sidecar closes that hole cheaply.

## Non-claims

I did not run the gate, `make check-backend`, or any e2e suite. The independent Go build
is a feasibility check of the hash-match rule, not a gate attempt or an attestation for
future runs. The 50–100 MB target remains unproven, as the plan states.
