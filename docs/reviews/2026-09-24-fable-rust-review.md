# Fable 5.1 independent Rust review — 2026-09-24

Reviewer: Claude Fable 5.1, independent of the implementer (Claude Opus 5.5, session
`bfbd1f21`). Scope: `rust-core/**` only, against the plan's Stage 1 (`indexcache.rs`
source identity) and Stage 3 (incremental, honest indexing and resource confinement).
Excluded here: Go, Pi, bench scripts, the process-tree RSS gate (open, owned by the Go
integrator), and the deferred goal-runtime defects (audit findings 3 and 4).
This file is the only artifact this review wrote. No source, cache, or user data was
modified; all probes ran on disposable Git repositories under the job scratch directory.

> **Recheck 2 (2026-09-24 17:02–17:12 IST):** the second residency optimization
> (borrowed definers and edges, moved symbol nodes, per-symbol search tokenization) was
> re-reviewed against the Recheck 1 snapshot; output is byte-identical on six fixtures and
> all checks pass. Current verdict: **APPROVED for the Rust scope** (see "Recheck 2" at
> the end). Recheck 1 (16:20–16:35) covered the F1–F4 fixes and the first optimization. The section below is the
> original 16:08 review, kept verbatim as history.

## Verdict (16:08, superseded): CHANGES_REQUIRED (one P2), everything else in scope approved

The audited P1 defects (stale dirty→dirty graphs, invisible symbol caps, silent read
failures) and the P2 confinement gaps (raw scanner/LSP reads, buffered ast-grep, LSP
header/queue bounds, retained file bodies) are fixed with regressions that fail on the
old code and pass now. Cross-process build serialization holds under real contention.

One reproducible resource defect remains and must change before the Rust scope is
merge-ready: untracked bytes decide graph cacheability and per-call cost (F1 below). The
fix is small and does not touch the key contract. The other findings are P3 notes.

## Reviewed state

Source moved three times during the review; every finding was re-read against the last
state before this verdict.

| Time (IST) | Change observed | Effect on review |
| --- | --- | --- |
| 15:59:40 | `symbolgraph.rs` unused `std::process::Command` import removed | clippy back to 11 warnings, all pre-existing |
| 16:03:49 | `indexcache.rs` `hidden_index_entries` (`git ls-files -v -z`), `symbolgraph.rs`, new test `assume_unchanged_and_skip_worktree_edits_are_identified` | closed two P2 findings I had reproduced (sparse checkout; assume-unchanged), see "Closed during review" |
| 16:05:02 | `indexcache.rs` `graph_cacheable` also bypasses on `git_index_flags_failed`; unit tests | consistent with the new limitation reason |

Final reviewed hash (`git diff -- rust-core` concatenated with both `rust-core/tests/*.rs`,
sha256): `0c0351cd279edcd1f0af2c6cfb3114b0f7f41cd91fb6617468f2e4685a94b957`. Base commit `cd13e2b`. Diff size: 9 files, 2554 insertions,
494 deletions, plus the two untracked test files (254 and 413 lines).

## Checks run (exact exit statuses)

All runs used an isolated `CARGO_TARGET_DIR` under the job scratch directory so they
did not contend with the implementer's builds. `rustc 1.93.1`. Exit statuses were
captured directly from each command (`pipestatus`), not inferred from output.

| Run | Source hash (prefix) | Command | Result |
| --- | --- | --- | --- |
| 1 | `0d97982d` (before 16:03) | `cargo test` | exit 0: lib 127 passed, `index_process` 4, `index_regressions` 9, 0 failed |
| 2 | after 15:59 | `cargo clippy` | exit 0, 12 warnings (the unused import, since removed) |
| 2 | after 15:59 | `cargo clippy --all-targets` | exit 0 |
| 2 | after 15:59 | `cargo test --test index_process` ×3 | exit 0 each; `competing_builders_are_serialized_across_processes` ok 3/3 (0.79–1.35 s) |
| 2 | after 15:59 | `cargo test --test index_regressions` ×2 | exit 0 each, 9 passed |
| 3 | `71ece2ac` | `cargo test`; `cargo clippy`; `cargo build --release --bin xmustard-core` | exit 0 / exit 0 (11 warnings) / exit 0 |
| 4 | `db83208b` (after 16:03:49) | `cargo test`; `cargo clippy` | exit 0: lib 127, `index_process` 4, `index_regressions` 10 / exit 0 (11 warnings) |
| 5 | `0c0351cd` (after 16:05:02, final) | `cargo test`; `cargo clippy`; `cargo build` | exit 0: lib 128, `index_process` 4, `index_regressions` 10, 0 failed / exit 0 (11 warnings) / exit 0; source hash unchanged after the run |

The 11 clippy warnings are the pre-existing collapsible-if, too-many-arguments and
complex-type warnings the audit listed; none were added by this diff. The earlier
integrator failure of `competing_builders_are_serialized_across_processes` (a 136 ms
wait under a blind-sleep threshold) is not reproducible with the current test: it now
handshakes on both builders holding `build.lock` open (`/proc` or `lsof`) before
releasing the lock, then asserts a wait of at least the 100 ms it itself held the lock
after the handshake. That is a lock proof, not a timing guess. Five runs passed here.

## Findings

### F1 — P2 — untracked bytes decide graph cacheability and per-call cost

`rust-core/src/indexcache.rs:348-362` (`graph_cacheable`) bypasses the graph cache when
any status entry is `DirtyContent::Unhashed`, including untracked entries, although the
same function's contract says untracked files never feed the graph and
`symbolgraph.rs` indexes `git ls-files` only. `indexcache.rs:472-500` hashes every
untracked file in full (tracked first, then untracked, until the 64 MiB budget), and
`build_graph` runs `source_identity` twice per build, so a build hashes up to 128 MiB of
bytes that cannot change the graph. `symbolgraph.rs:1234` then blames "a tracked dirty
file was not hashed".

Reproduction (disposable repo, debug binary from run 4):

```
git init; commit a.rs with `pub fn tracked_symbol() {}`
for i in 1..9: write blob$i.dat of 8 MiB (untracked, not ignored)   # 72 MiB
xmustard-core repo-key <repo>        -> identity_complete=false, files_hashed=8, bytes_hashed=67108864,
                                        limitations=[{path:"blob9.dat",reason:"identity_budget"}]; 3.19 s wall
xmustard-core search <repo> ws tracked_symbol 5   (run twice)
  -> graph_cache="bypass", identity_bytes_hashed=134217728, identity_passes=2, elapsed_ms=6346 then 6321
     (12434 ms on a loaded machine); detail: "... or a tracked dirty file was not hashed"
```

Baseline `cd13e2b` served this repository from its warm cache in about 0.1 s. Unignored
build output or data directories are a common state for agent-driven checkouts, and the
Go side also calls `repo-key` per evidence capture, which pays the same hashing. This is
a reproducible resource regression, not a correctness one: the answers were correct.

Required change: exclude untracked `Unhashed` entries from `graph_cacheable` (they are
already in the key through their metadata token, and `identity_complete=false` still
reports the budget loss), and make the bypass detail name the actual reason.
Recommended in the same change: give untracked entries a separate, smaller hashing
budget (or hash tracked dirty and hidden entries in full and untracked entries by
metadata token only) so one build cannot spend 128 MiB of hashing on files the graph
never reads. Whichever is chosen, `repo-key` must keep reporting the limitation so Go
treats the key as incomplete. Add a regression with untracked bytes over the budget
that asserts `graph_cache="hit"` on the second call.

### F2 — P3 — dirty-but-unchanged files are re-read and re-parsed every build

`symbolgraph.rs:1470-1479` reuses per-file features only for files clean against the
index (`git:<blob>` identity). A dirty file whose SHA-256 the identity pass already
computed is read again and parsed again on every build, even when unchanged since the
last build. Reproduction: five committed files, edit `f1.rs` and `f2.rs`, `symbolgraph
build` (5 read, 5 parsed, 0 reused); edit `f3.rs`, build again: `files_read=3,
files_parsed=3, files_reused=2`. The plan asked to reparse only changed files; two of
the three reads were of unchanged bytes. The identity's `sha256:` token is the reuse key
already stored in `files.json`, so reuse for dirty files is a small change and is safe
because the post-build identity pass still has to match. Cost is bounded by the number
of dirty files, hence P3.

### F3 — P3 — `complete=true` is possible while the identity is incomplete (unreproduced)

When `git status` fails (`git_status_failed`), `symbolgraph.rs:1381-1384` and `:1433`
treat every file as `Unhashed`, all files are read with `sha256:` identities, and the
degenerate key (`indexcache.rs:445-449`, root plus HEAD only) is equal across the two
passes, so `stable=true` and `complete=true` when no file loss occurs, while
`source_identity.identity_complete=false`. The graph is correctly not cached (bypass),
so no stale result is served. I could not force a status failure with a working
`rev-parse` and `ls-files` in a fixture, so this is a source-reading observation.
Suggested: `complete` should also require `identity_complete`, or the field doc should
say `complete` describes file coverage only.

### F4 — P3 — pre-existing: whitespace-named paths are trimmed in the non-graph lanes

`symbolgraph.rs:291` (`open_repo_file_beneath`) trims `rel` before the no-follow walk.
The graph and identity lanes use the untrimmed `open_repo_path_beneath`, which the
regression `unusual_filenames_invalidate_warm_graph` covers. The scanner
(`scanner.rs:110`), LSP `didOpen` (`lsp_session.rs` `read_document_text`),
`path-symbols` and changetrack hashing still go through the trimming wrapper, so a
tracked file named ` lead.rs` is silently skipped in those lanes. Not introduced by this
diff; noting it because the diff moved those lanes onto this reader.

### F5 — P3 — test-harness dependency

`tests/index_process.rs` `holds_open` needs `/proc/<pid>/fd` or `lsof`. On macOS without
`lsof` the handshake loop would fail at its 20 s deadline with "builders never reached
the lock". `lsof` ships with macOS, so this is a portability note only.

## Closed during review (reproduced before, verified fixed after 16:03:49)

- Sparse checkout (`git sparse-checkout set --no-cone keep`): before the change, each
  `search` reported `losses=[{skip/s.rs, missing}]`, `stable=false`, "source changed
  during indexing; this result is not cached", `files_reused=0`, and no `files.json`
  was ever written, so the cache never warmed. After: run 1 `miss`, run 2 `hit`,
  `complete=true`, `worktree_deleted_files=1`, no losses; `repo-key` reports
  `hidden_entries=1`, `identity_complete=true`.
- `git update-index --assume-unchanged c.rs`, then two successive edits: before, the
  second edit was served from the warm cache with the first edit's symbol. After: each
  edit is a `miss` with the current symbol, and an unchanged third call is a `hit`.
  Covered by the new test `assume_unchanged_and_skip_worktree_edits_are_identified`.

## Verified as claimed

Each item names the evidence; "probe" means a disposable-repo run of the debug binary.

- Status parsing: `git status --porcelain=v1 -z --untracked-files=all` is parsed
  untrimmed with rename origins (`parse_porcelain_v1_z`). Git's `-z` rename record order
  (`R M b.rs\0a.rs\0`, new path first) was checked empirically with Git 2.50.1 and
  matches the parser. Leading-space, newline, tab and trailing-space names round-trip
  (`unusual_filenames_invalidate_warm_graph`, `porcelain_z_parses_untrimmed_renames_and_odd_names`).
- Full hashing with honest degradation: dirty bytes are hashed in full through the
  no-follow reader; over-cap files contribute a metadata token and an `oversized`
  limitation with `bytes_hashed=0` (no prefix hash); the identity budget produces
  `identity_budget`; outside Git produces `git_unavailable`; `identity_complete` is
  false whenever any limitation exists (`oversized_dirty_file_is_an_explicit_limitation_not_a_prefix_hash`,
  `repo_key_contract_matches_cache_identity`, probes).
- Shared repo/trust/parser scope: two workspace IDs share one snapshot (`miss` then
  `hit`, `files_read=0`); a different `XMUSTARD_INDEX_TRUST_SCOPE` misses
  (`snapshot_is_shared_across_workspaces_but_not_trust_scopes`). A symlinked path to the
  same repository hits the snapshot built from the real path (canonical root, probe). A
  subdirectory root indexes only its subtree, invalidates on an edit inside it, and
  conservatively re-keys on an edit outside it (probe; 3 snapshots kept per scope).
- Unchanged-file reuse and dependent invalidation: after a two-file definition move,
  `files_read=2, files_parsed=2, files_reused=2` and the unchanged caller's edge is
  re-resolved to the new definer (`one_file_edit_does_not_reread_unchanged_files`).
- Cross-process lock and read consistency: six concurrent cold `search` processes with
  no external lock produced exactly one `miss` and five `hit`s, all `lock=acquired`,
  five `lock_contended=true` (468–516 ms waits), one `graph-*.json`, all `stable=true`
  (probe). Four concurrent uncached `symbolgraph build`s left one valid `files.json`
  (40 entries) and no temp leftovers. Waiters re-check the cache after acquiring the
  lock (`symbolgraph.rs:1268-1273`). Lock timeout builds and returns without storing
  (`lock_wait_is_bounded_and_reported`). The lock is an OS advisory lock released on
  process exit, so no stale lock survives a crash.
- Coverage honesty including symbol caps: extraction is bounded at 4096 symbols per
  file with a `symbols_truncated` loss, 70-symbol files index fully with an edge to
  `symbol_070`, the graph budget is 100 000 symbols with a `symbol_budget` loss,
  `path-symbols` keeps its 32-entry display but reports `total_symbols` and
  `symbols_truncated`, and unreadable/oversized/invalid-UTF-8 inputs are counted in
  `loss_counts` with `indexed_files=2 of 4` (`symbols_beyond_prior_caps_are_indexed`,
  `per_file_symbol_bound_is_reported`, `unreadable_oversized_invalid_utf8_are_reported`,
  `extraction_is_complete_past_old_cap_and_reports_truncation`). Changetrack signature
  baselines use the complete extraction (`extract_path_symbols_limited(.., usize::MAX)`).
- Bounded no-follow reads: scanner (`scanner_skips_files_over_the_shared_read_cap`) and
  LSP `didOpen` (`document_text_uses_bounded_no_follow_reader`: symlink refused,
  over-cap refused) use the shared reader; the reader refuses symlinks at every
  component, fifos, directories and over-cap files (existing `symbolgraph` unit tests).
- ast-grep: stdout is streamed with a 1 MiB line cap, 64 MiB total cap, 64 KiB stderr
  cap, the result limit stops and kills the child, and a 60 s timeout kills it
  (`ast_grep_child_is_killed_at_timeout` asserts the recorded PID is gone;
  `ast_grep_output_is_bounded_while_streaming` cuts an endless producer at 3 matches).
- LSP: 8 KiB header line, 32 header lines, 16 MiB payload, notifications dropped
  before queueing, at most 16 queued messages and 32 MiB queued bytes, overflow fails
  the waiting request (`read_message_rejects_oversized_header`,
  `inbound_queue_drops_notifications_and_bounds_responses`).
- Git subprocesses on the index and identity path run through `run_git_bounded`
  (32 MiB stdout cap, `XMUSTARD_GIT_TIMEOUT_MS`, kill on overflow or timeout, error not
  truncation). Stability is re-checked after every build and unstable results are never
  cached (`edit_during_build_is_unstable_and_not_cached`).
- Temp sweep: crash-leftover atomic-write temp files older than 10 minutes are removed
  on the next store (`stale_cache_temp_files_are_swept`, `sweep_removes_only_old_temps`).

## Remaining limitations (exact, not defects)

- Per-call identity cost is now two `git status` runs, one `git ls-files -v -z`, one
  `git ls-files -s -z`, and full hashing of every dirty, hidden and untracked file up to
  64 MiB per pass, two passes per build. On this worktree (52 dirty, 61 untracked,
  1.5 MB hashed) `repo-key` takes 0.24 s. F1 bounds the bad case.
- Not verified by this review: live language servers (`gopls` absent; bounds were
  tested with framed input and a `cat` child), the real `sg` binary (absent; a shell
  fake was used), non-Unix fallbacks, `semantic-onnx`, and the 50–100 MB process-tree
  target. Nothing here supports a claim on that target; the combined gate stays open
  with the Go integrator. I built a release binary in my own target directory; I did
  not verify that the repository's `rust-core/target/release/xmustard-core` is fresh.
- `XMUSTARD_INDEX_TRUST_SCOPE` is read only in Rust. `grep` finds no Go reference, so
  until the integrator sets it every caller shares the `local` scope. Workspace IDs
  correctly never select a cache.
- Rust-side timeouts kill the direct child only; grandchildren rely on the Go
  process-group kill.
- An edit made and reverted between the two identity passes of one build is
  undetectable by construction (same key before and after).
- The `git_status_failed` key depends only on root and HEAD and is always reported
  incomplete; callers must not compare it as content.
- Untracked files are hashed into the key but never indexed.

## How this verdict was formed

Source was treated as truth over the plan, the audit and the implementer's results file.
Each claimed fix was traced to its code path and to a test that exercises that path, then
probed on a fixture where a cheaper proof was possible. Findings are ranked by whether
they reproduce (F1, F2 reproduce; F3 is source-only; F4 is pre-existing; F5 is
portability). Human final merge authority is unchanged.

---

# Recheck 1 — 2026-09-24 16:20 IST onward (F1–F4 fixes, then the optimization delta)

Same reviewer, same scope, same rules (Rust only; no source edits; disposable fixtures;
the combined RSS gate stays with the Go integrator and is not claimed here). The root
relayed the 16:08 findings to the implementer; the implementer landed F1–F4 between
16:14 and 16:18 and then started an RSS optimization that was still in progress when
this recheck began.

## State rechecked

| Time (IST) | Files | What |
| --- | --- | --- |
| 16:14:57–16:15:25 | `scanner.rs`, `lsp_session.rs` | F4: exact-path reads plus decoy tests |
| 16:17:04 | `indexcache.rs` | F1: `tracked_unhashed`, `graph_bypass_reason` |
| 16:17:38 | `symbolgraph.rs` | F1 detail message; F2 dirty reuse by `sha256:`; F3 `complete` requires the identity to determine the graph; F4 trim removed from `open_repo_file_beneath` |
| 16:18:41 | `changetrack.rs`, both test files | F4 changetrack on NUL-delimited bounded Git; new regressions |
| 16:21:30, 16:22:48 | `treesitter.rs`, `symbolgraph.rs` | optimization in progress (`line_flows`); tree did not compile at 16:22:48 |

Hash of the F1–F4 state at the start of run 6 (16:21:26): `2dc3dcfb…f56255`.
Run 6 compiled its test binaries between 16:21:26 and 16:22:12; `treesitter.rs` changed
at 16:21:30 inside that window, so the tested state may include that edit. The
`symbolgraph.rs` edit at 16:22:48 came after the test binaries were built and broke
`cargo clippy` and `cargo build` (`line_flows` undefined, `E0425`/`E0282`). That is an
incomplete edit, not a defect; the final verdict below waits for it to settle.

## Checks (exact exit statuses)

| Run | Command | Result |
| --- | --- | --- |
| 6 | `cargo test` | exit 0: lib 130 passed, `index_process` 5, `index_regressions` 12, 0 failed |
| 6 | `cargo clippy` | exit 101: 2 errors in `symbolgraph.rs` (`line_flows` not found; type annotation), mid-edit at 16:22:48 |
| 6 | `cargo build` | exit 101, same cause |

## F1–F4 recheck against source and probes (debug binary built 16:22:12)

- **F1 (untracked bytes blocked the graph cache) — fixed.** `indexcache.rs:344-347,
  353-381` keep a `tracked_unhashed` list (hidden entries count as tracked) and
  `graph_bypass_reason` names the real cause; untracked-only budget losses no longer
  bypass. Source identity was not weakened: every untracked file is still hashed in full
  up to the 64 MiB budget, entries past it contribute a metadata token and an
  `identity_budget` limitation, and `identity_complete` stays false. Probe on the same
  72 MiB untracked fixture: run 1 `miss`, run 2 `hit`, `complete=true`,
  `source_identity.identity_complete=false`, `repo-key` limitation
  `{blob9.dat, identity_budget}` still present; 0.66 s then 0.30 s wall. The earlier
  6–12 s figures came from an unoptimized SHA-256 in the debug build: `Cargo.toml`
  (16:17:56) now sets `[profile.dev.package.sha2] opt-level = 3`, a dev-profile-only
  change that leaves release builds untouched and hashes the same 64 MiB per pass. Regression
  `untracked_bytes_over_budget_do_not_block_graph_cache` also asserts that editing an
  unhashed untracked file still re-keys through the metadata token.
- **F2 (dirty-but-unchanged files reparsed) — fixed.** `symbolgraph.rs:1471-1490`
  reuses features for dirty files by `sha256:<identity hash>`; the stored feature
  identity is the SHA-256 of the bytes actually parsed, and the post-build identity
  pass must re-hash to the same key for the result to be stable, so the reuse cannot
  drift from the reported identity. Probe: five files, two dirty, cold build reads 5;
  a third edit then reads 1, parses 1, reuses 4, `stable=true`, all five current
  symbols present. Regression `unchanged_dirty_files_are_not_reparsed` asserts
  `(files_read, files_parsed, files_reused) == (1, 1, 4)`.
- **F3 (`complete=true` with an incomplete identity) — fixed and now reproduced by
  test.** `symbolgraph.rs:1771-1778`: `complete` also requires
  `graph_bypass_reason().is_none()`, and the degraded reason names the limitation. The
  new process test `git_status_failure_is_incomplete_and_never_serves_stale` uses a PATH
  shim that fails only `git status`; it asserts `identity_complete=false`,
  `complete=false`, `graph_cache=bypass` naming `git_status_failed`, and that the
  current symbol (not the warm cache's) is served. This closes the "unreproduced"
  caveat from the 16:08 review.
- **F4 (trimmed paths in the non-graph lanes) — fixed.** `symbolgraph.rs:289-299` no
  longer trims; `scanner_reads_whitespace_named_files_exactly`,
  `document_text_uses_bounded_no_follow_reader` (now with a ` lead.rs` real/decoy pair
  and a whitespace-named symlink) and changetrack `fingerprint_preserves_exact_paths`
  passed in run 6. Changetrack's `ls-files` and `status` now run through
  `run_git_bounded` with `-z`, which also removes two of the unbounded `.output()`
  calls the implementer's limits section listed.
- **F5 (lsof dependency in the lock test)** — unchanged; portability note only.

The implementer's results file (`2026-09-24-rust-implementation-results.md`, last
written 16:06) does not yet describe the F1–F4 changes or the new tests; it should be
updated before handoff so its claims match the code.

## Optimization delta and current verdict

The implementer's RSS optimization settled at 16:24:17 (no `rust-core` edit for two
minutes, checked by mtime). Final rechecked hash (same recipe as before):
`0ddf5562a0634e6fff92a30d301c42f3345f628aaeadb498eea67978eeb9666e`, unchanged after
every check and probe below. Diff size: 10 files (including `Cargo.toml`), 2806 insertions, 529 deletions,
plus the two test files.

### Checks on the settled state (exact exit statuses)

| Run | Command | Result |
| --- | --- | --- |
| 7 | `cargo build` | exit 0 |
| 7 | `cargo test` | exit 0: lib 131 passed, `index_process` 5, `index_regressions` 12, 0 failed |
| 7 | `cargo clippy` | exit 0, 11 warnings, all pre-existing (none added by the diff) |
| 7 | `cargo test --test index_process` ×2 more | exit 0 each (5 passed; 1.04–1.05 s) |
| 7b | `cargo build --release --bin xmustard-core` (my target dir) | exit 0 |

### What the optimization changed (source read, behavior-preserving)

- `treesitter.rs`: compiled tree-sitter queries are cached per grammar per thread
  (`QUERY_CACHE`, `compiled_query`). A query depends only on the grammar and the static
  query text, so this cannot change extraction results.
- `symbolgraph.rs:762-796` `line_flows`: one pass over a line's identifier spans instead
  of a substring search per keyword per identifier. The old `find_word`/`flow_edge_kind`
  are kept under `#[cfg(test)]` as the reference and
  `line_flows_matches_reference_classifier` pins the new classifier to them. I checked
  the two edge cases by hand: the earliest branch keyword wins in both (first span in
  order equals the minimum position), and only `writes` survives when no `return` or
  branch keyword precedes the identifier, as before.
- `symbolgraph.rs:1496-1560`: the per-file `indexed` vector and its `f.clone()` are
  gone; assembly iterates `next.files` (a `BTreeMap`, path order; the old vector was in
  index order, which is also path order) and `next` is stored then dropped after the
  stability check, as before.
- `indexcache.rs:70-97, 803-815`: `files.json` and graph snapshots are streamed through
  `serde_json::to_writer`/`from_reader` over a `BufWriter`/`BufReader` instead of a
  whole-file `Vec<u8>`; the temp-file, `sync_all`, rename sequence of `atomic_write`
  is unchanged (`atomic_write_with`).
- `Cargo.toml`: `[profile.dev.package.sha2] opt-level = 3` (debug/test builds only),
  so the identity-budget regression hashes 64 MiB in well under a second instead of
  about 12 s; no effect on release behavior or on what is hashed.
- `indexcache.rs:467-478`: `git status` now passes `--ignore-submodules=none` so a
  `submodule.<name>.ignore=all` config cannot hide a dirty submodule from the identity;
  new unit test `ignored_dirty_submodule_is_not_complete` (real nested repo).

### Probes on the settled binaries

Debug binary built 16:26:46 (run 7); release binary built 16:28:02 in my own target
directory (the repository's `rust-core/target/release` was not touched or verified).

- F1 fixture (72 MiB untracked): `hit`, `complete=true`, `identity_complete=false`,
  0.30 s and 0.26 s.
- F2 fixture: a fourth dirty edit reads 1, parses 1, reuses 4, `stable=true`, all
  five current symbols present.
- Sparse checkout fixture: `hit`, `complete=true`, `worktree_deleted_files=1`, no losses.
- Six concurrent cold `search` processes on a fresh 40-file repo with one dirty file:
  6 × exit 0, one `miss`, five contended `hit`s, all `stable=true`, one `graph-*.json`,
  no temp files.

Single-process max RSS of the **release** binary (`/usr/bin/time -l`, macOS bytes
converted to MiB) on a generated fixture of 501 tracked Rust files (500 × 60 functions
plus the 70-symbol `many.rs`; 8.0 MB of source, smaller than the plan's 21 MB shape, so
these numbers are not the plan workload and not the combined process-tree gate):

| Step | Max RSS | Wall | Counters |
| --- | --- | --- | --- |
| cold `search symbol_070` | 47.1 MiB | 1.31 s | miss, read 501, 30,070 symbols, complete |
| warm `search` | 21.1 MiB | 0.15 s | hit, read 0 |
| one-file dirty edit | 43.2 MiB | 0.28 s | miss, read 1, reused 500, new symbol found |
| same-size second dirty edit | 41.8 MiB | — | miss, read 1, reused 500, new symbol found |
| uncached `symbolgraph build` | 35.5 MiB | 0.18 s | reused 501 |
| `repo-key` | 6.3 MiB | — | — |
| two concurrent cold clients (fresh scope) | 46.9 + 21.1 MiB | — | one miss, one contended hit (959 ms wait) |

These are Rust-process-only values on a lighter workload; they show the direction of the
optimization (the implementer's own 20 MiB fixture reported 84.9 MiB cold before it) and
say nothing about the Go API, the MCP shim, or the 100 MB combined target, which the
integrator reported failing at 134.1 MB with two index clients. That gate remains open
and is not claimed here.

### Current verdict (16:35 IST): APPROVED for the Rust scope

All four 16:08 findings are fixed at the source and covered by regressions that name
them (F1 `untracked_bytes_over_budget_do_not_block_graph_cache`, F2
`unchanged_dirty_files_are_not_reparsed`, F3
`git_status_failure_is_incomplete_and_never_serves_stale` with a real failing `git`,
F4 exact-path tests in scanner, LSP and changetrack). Full source identity was not
weakened: untracked and hidden bytes are still hashed in full to the budget, overflow
still yields `identity_complete=false` with an `identity_budget` limitation, and only
the graph cache stops treating untracked overflow as disqualifying. The optimization
delta is behavior-preserving by construction and by its reference test, and every
suite passes on the settled hash with clippy and build at exit 0.

Conditions and limits that stay attached to this approval:

- The implementer's results file (last written 16:06) must be updated to describe the
  F1–F4 changes, the new `--ignore-submodules=none` behavior, the streamed cache I/O and
  the new tests before handoff, so its claims match the code that ships.
- The combined RSS gate (100 MB, two index clients, plan workload) is failing per the
  integrator and is outside this review; nothing above should be read as passing it.
- Still unverified by this review: live language servers, the real `sg` binary,
  non-Unix fallbacks, `semantic-onnx`, and the freshness of the repository's own
  release binary. Per-call identity cost (two `status`, one `ls-files -v -z`, one
  `ls-files -s -z`, full hashing to 64 MiB per pass) is unchanged and documented.
- Human review of the exact final diff remains required before any merge.


---

# Recheck 2 — 2026-09-24 17:02–17:12 IST (second residency optimization)

Same reviewer, scope and rules. The root reported the full combined gate failing at
106.4 MB after Recheck 1; the implementer then reduced Rust-process residency in
`symbolgraph.rs` and `search.rs`. This recheck asks one question: does the new code
produce the same graph, search results, counters, identity, locking and caps as the
snapshot approved in Recheck 1? It does not bless the combined 100 MB gate, which a
single-process profile cannot establish.

## State rechecked

Files changed since the Recheck 1 snapshot (`0ddf5562…`): `rust-core/src/symbolgraph.rs`
(16:56:52) and `rust-core/src/search.rs` (16:59:32). Nothing else in `rust-core` moved.
Final rechecked hash (`git diff -- rust-core` concatenated with both test files, sha256):
`d039d76da66876af79a9df12e6dc23ef9941a0a846ba3e64a720dad7ae77a358`, unchanged after
every check and probe below. Diff size: 11 files, 2880 insertions, 571 deletions, plus
the two untracked test files. The repository's release binary
`rust-core/target/release/xmustard-core` (17:00:10) hashes to
`d644743ba005318ebbce451d7924ce4bcbefb26be057a48ff64f24ff430c3576`, matching the
implementer's report; it was used unmodified as the "new" side of the equivalence runs.

## Checks (exact exit statuses, isolated target dir)

| Run | Command | Result |
| --- | --- | --- |
| 8 | `cargo build` | exit 0 |
| 8 | `cargo test` | exit 0: lib 131 passed, `index_process` 5, `index_regressions` 12, 0 failed |
| 8 | `cargo clippy` | exit 0, 11 warnings, all pre-existing |
| 8 | `cargo build --release --bin xmustard-core` | exit 0 |

## Source review of the delta

- `symbolgraph.rs:559-575`: `name_to_defs` is `HashMap<&str, Definer>` borrowing names,
  paths and kinds from the feature store. `Definer::One{path, kind}` is set on the
  first occurrence; a later occurrence in a *different* file turns it into
  `Definer::Ambiguous`; a repeat in the same file keeps the first. That reproduces the
  old `Vec<SymbolDef>` rules (one entry per defining file, edge only when exactly one
  file defines the name, kind from the first definition in that file) with no
  observable difference.
- `symbolgraph.rs:1659-1745`: edge aggregation keys and `via` sets borrow (`EdgeAgg<'a>`)
  and are materialized into owned `GraphEdge`s by `to_edges` before `name_to_defs` is
  dropped and before the feature store is consumed. Edge ordering, the 8-entry `via`
  cap, self-edge suppression and the `imports`/`inherits`/`calls`/flow kinds are
  unchanged.
- `symbolgraph.rs:1757-1783`: the second identity pass, the `stable` rule and
  `store_features(&next)` run first; only then are symbol names and kinds *moved* out
  of `next.files` into a `Vec` sized once to `kept_total`. The move zips
  `next.files` (a `BTreeMap`) with `file_nodes`, which were pushed one per map entry in
  the same iteration, so alignment holds; `symbol_count` per node bounds the take, so
  the symbol budget cut is preserved. Coverage, `complete`, `graph_cache`, lock
  handling and cache storage are untouched.
- `search.rs:247-291`: `hybrid_search` no longer materializes token sets for every
  symbol; it tokenizes per symbol in the document-frequency pass and again in the
  candidate pass with the same `tokens` function. Scores are computed from identical
  inputs, so ranking cannot change; the cost is a second tokenization per symbol.
- Nothing in the delta touches `indexcache.rs`, the identity key, the build lock, the
  Git/ast-grep/LSP bounds, the per-file or graph symbol caps, or the CLI contracts.

## Equivalence probes (approved-snapshot release binary vs current release binary)

The "prev" binary is the release build I made from the Recheck 1 snapshot at 16:28
(sha256 `a23473a6…cda2e`, kept aside before rebuilding). Outputs were compared after
removing `generated_at`, `work` and `elapsed_ms` at every level; the root-dependent key
is identical because both binaries ran on the same fixture paths. Search used 10
results per query.

| Fixture | `symbolgraph build` | `search` (5 queries) | Notes |
| --- | --- | --- | --- |
| 501-file workload, one dirty file | equal (30,071 symbols, 500 edges) | 5/5 equal | same top hit per query |
| 40-file call ring, one dirty file | equal (80 symbols, 40 edges) | 5/5 equal | |
| sparse checkout | equal | 5/5 equal | `worktree_deleted_files=1` |
| five files, four dirty | equal | 5/5 equal | |
| edge-rich fixture (below) | equal (802 symbols, 406 edges, 600 flow edges) | 5/5 equal | `blast-radius defn_7` also equal |

The edge-rich fixture was built to hit the rewritten rules: 200 Rust files whose
functions reference two other files' symbols inside `return`, `if`, `match` and
assignment contexts (199 `branches`, 203 `returns`, 198 `writes` flow edges), a name
defined twice in every file (`shared_dup_name`: repeat-in-file plus ambiguous across
files; it anchors zero edges, as before), a Python `class Child_2(Base_1)` (1 `inherits`
edge), a TypeScript relative import (`imports` edges), a `.c` file (`unsupported_language`
loss, still contributing references) and one dirty file. All outputs are byte-identical
between the two binaries.

Single-process max RSS of the release binaries on the 501-file workload
(`/usr/bin/time -l`, fresh trust scope for the cold run; Rust process only, an 8 MB
fixture, not the plan workload and not the combined gate):

| Step | Recheck 1 snapshot | Current |
| --- | --- | --- |
| cold `search` (miss, 30,071 symbols) | 47.1 MiB / 1.20 s | 29.2 MiB / 1.09 s |
| warm `search` (hit) | 21.1 MiB | 13.2 MiB |

These agree in direction with the implementer's live-heap table. They say nothing about
the Go API or MCP shim, and the combined process-tree gate (100 MB, two index clients,
plan workload) remains the integrator's measurement; the last reported result was a
failure at 106.4 MB before this round, and no number here changes that status.

## Findings

None new. The two P3 notes from Recheck 1 still apply: the lock test needs `lsof` on
macOS, and the implementer's results file must keep pace with the code. The results
file was updated at 17:00:59 and now describes rounds 1 and 2; its claims about
equivalence (`symbolgraph build` and five `search` queries identical on 29,737- and
25,514-symbol fixtures) are consistent with what I measured independently on smaller
fixtures. I did not run the implementer's Go fixture or its scratch allocator-counting
binary; the heap numbers in that file are its own evidence.

## Current verdict (17:12 IST): APPROVED for the Rust scope

The second optimization is output-preserving by source reading and by byte-identical
graph, search and blast-radius output across six fixtures that exercise the rewritten
definer, edge and tokenization paths, with the identity key, locking, caps and
counters untouched. Tests, clippy and both builds pass on hash `d039d76d…7a358`.
Human review of the exact final diff remains required before any merge, and the
combined RSS gate is not claimed.
