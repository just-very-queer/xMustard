# Rust implementation results — 2026-09-24

Implementer: Claude Opus 5.5 (Rust scope only: `rust-core/**` and this file).
Plan: [`2026-09-24-lean-context-implementation.md`](../plans/2026-09-24-lean-context-implementation.md)
Stage 1 (`indexcache.rs` source identity) and Stage 3 (incremental, honest indexing and
resource confinement). Audit: [`2026-09-24-rust-audit.md`](2026-09-24-rust-audit.md).
Worktree `/private/tmp/xmustard-opus-l9b1F4`, base `cd13e2b`. Nothing committed or pushed.
Go/Pi/scripts belong to the integrator session `6a0c9933`. Deferred and untouched here:
goal-runtime findings 3 and 4.

Status: **Rust scope complete.** Fable's Rust review
([`2026-09-24-fable-rust-review.md`](2026-09-24-fable-rust-review.md)) findings F1–F5 are
addressed below, and Root reports Fable's current read as APPROVED. Main import still
waits for the full Go/Pi/resource review and human review.

Final same-tree checks (Root reran them independently):
- `cd rust-core && cargo test`: exit 0, 131 lib + 12 `index_regressions` + 5
  `index_process`, 0 failed.
- `cargo clippy --all-targets`: exit 0, 11 warnings, all pre-existing (baseline 12, none
  added). Touched files are rustfmt-clean.
- `cargo build --release --bin xmustard-core`: exit 0. The release binary is current and
  ready for the Go RSS rerun.

## Baseline

`cd rust-core && cargo test`: exit 0, 114 passed. `cargo clippy`: exit 0 with 12 warnings
(per the audit). `sg`/`ast-grep` and `gopls` are not installed here; `rust-analyzer` is.

## Red evidence (old behavior, before any fix)

Tests were added first. Seams without behavior changes came first: `lsp_session::read_document_text` still
does the old `fs::read_to_string(root.join(rel))`, and `semantic::run_ast_grep_with_binary`
holds the old `Command::output` body.

```
cargo test --test index_regressions          # exit 101: 0 passed, 8 failed
  dirty_to_dirty_edit_invalidates_warm_graph        warm cache served a stale graph: ["first_dirty_symbol"]
  same_size_same_mtime_dirty_edit_invalidates_...   same-size/same-mtime edit reused a stale graph
  unusual_filenames_invalidate_warm_graph           " lead.rs" first edit (leading space trimmed away)
  rename_delete_branch_and_untracked_are_current    renamed-then-edited file served stale
  symbols_beyond_prior_caps_are_indexed             per-file symbol count capped (32 != 70)
  unreadable_oversized_invalid_utf8_are_reported    {"eligible_files":4,"indexed_files":4,...,"truncated":false}
  one_file_edit_does_not_reread_unchanged_files     unchanged file was re-read (and lost) on a one-file edit
  stale_cache_temp_files_are_swept                  stale atomic-write temp file was not swept
cargo test --lib -- read_message_rejects document_text_uses scanner_skips ast_grep_   # exit 101: 5 failed
  lsp_session read_message_rejects_oversized_header      a 1 MiB header line must be refused, not buffered
  lsp_session document_text_uses_bounded_no_follow_reader symlink followed
  scanner scanner_skips_files_over_the_shared_read_cap   an over-cap file was read without the shared bound
  semantic ast_grep_child_is_killed_at_timeout           did not return within 15s of a 2s timeout
  semantic ast_grep_output_is_bounded_while_streaming    endless output was not cut off at the result limit
```

The old ast-grep path left its `sleep 600` and endless-echo children running after the test
thread was abandoned; they were killed by hand.

## Green evidence (same tests, after the fix)

```
cargo test --test index_regressions   # exit 0: 10 passed (8 above + per_file_symbol_bound_is_reported
                                      #   + assume_unchanged_and_skip_worktree_edits_are_identified)
cargo test --test index_process       # exit 0: 4 passed (3x normal, 3x under 4 busy CPU loops)
cargo test --lib                      # exit 0: 128 passed (114 baseline + 14 new)
```

Follow-up for the identity-completeness audit. Both cases below were red on this
tree before the fix and green after it.
- `git status` hides worktree edits to assume-unchanged and skip-worktree entries. The
  red run failed with `hidden-entry edit left the identity unchanged`. Now `git ls-files
  -v -z` (bounded, run at the top level) finds those entries, and their bytes are hashed
  like dirty files (`hidden_entries`). A failed listing becomes the limitation
  `git_index_flags_failed`. That makes identity incomplete, bypasses the graph cache, and
  makes the build read every file.
- `submodule.<name>.ignore=all` hid a dirty submodule. The red run failed with `ignored
  dirty submodule reported complete`. Status now passes `--ignore-submodules=none`, and
  the dirty submodule appears as a `not_regular` limitation.

`one_file_edit_does_not_reread_unchanged_files` was red through a chmod-000 proxy. After
the fix, Git itself reports an unreadable tracked file as ` M` because it cannot compare
content, so the proxy no longer isolates re-reading. The test now asserts the counters
directly. After two files change, `files_read=2, files_parsed=2, files_reused=2`, and the
unchanged caller's edge moves to the file that now defines the symbol, without reparsing
the caller.

## Fable review follow-ups (F1–F5)

Each regression was run on this tree before its fix, failed for the stated reason, and
passes after it.

| Finding | Red (before fix) | Fix | Regression |
| --- | --- | --- | --- |
| F1 untracked bytes blocked graph caching | 72 MiB untracked: `graph_cache="bypass"`, detail "…or a tracked dirty file was not hashed", 134,217,728 identity bytes, 11.7 s debug | `graph_bypass_reason()` blocks on repo-wide Git failures and *tracked* unhashed entries only. Untracked over-budget files keep their metadata token and `identity_budget` limitation (`identity_complete=false` for `repo-key`), and full-content hashing is unchanged. Bypass details name the reason. | `untracked_bytes_over_budget_do_not_block_graph_cache`: miss, then **hit**; an edit to an unhashed file re-keys |
| F2 unchanged dirty files reparsed | f1/f2 dirty, build, edit f3: `(files_read, files_parsed, files_reused) = (3,3,2)` | reuse key is `git:<blob>` for clean files and the identity pass's `sha256:<hex>` for dirty ones. The post-build identity pass still has to match, so source consistency holds. | `unchanged_dirty_files_are_not_reparsed`: `(1,1,4)`, every edited symbol present, `stable=true` |
| F3 `complete=true` with incomplete identity | PATH shim failing only `git status`: `"complete":true`, `identity_complete:false`, generic bypass text | `complete` also requires `graph_bypass_reason()==None`, and `degraded_reason` names the identity limitation | `git_status_failure_is_incomplete_and_never_serves_stale` (shim delegates every other Git command): `complete=false`, bypass detail contains `git_status_failed`, the edited symbol is served (no stale cache), `repo-key` reports `git_status_failed` |
| F4 whitespace paths trimmed | scanner skipped ` lead.py`; LSP read the `lead.rs` decoy; changetrack counted 2 of 4 files | `open_repo_file_beneath` is byte-exact (NUL still refused). Changetrack `tracked_files`/`dirty_paths` use bounded `ls-files -z` / `status --porcelain=v1 -z` and the shared NUL parser, so a rename now reports its new path instead of `old -> new`. | `scanner_reads_whitespace_named_files_exactly`, `document_text_uses_bounded_no_follow_reader` (decoy, whitespace symlink refused), `fingerprint_preserves_exact_paths` |
| F5 `lsof` dependency | n/a | documented at `holds_open`: it needs `/proc` or `lsof` and fails at its deadline rather than passing silently | — |

`complete` semantics: no coverage loss, a stable build, and an identity that determines
every tracked file the graph reads. A limitation that concerns only untracked files
(never indexed) leaves `complete=true` and the graph cacheable; it appears only in
`source_identity.identity_complete=false` and in `repo-key`. The F1 regression asserts
this split.

## Performance and memory changes (after correctness was green)

- Tree-sitter queries are compiled once per grammar per thread (`treesitter::compiled_query`).
  Previously `ts_query_new` ran for every file and was the top CPU frame.
- Flow classification (`symbolgraph::line_flows`) reads `return`/branch keyword positions
  from the line's identifier spans once, instead of a `find_word` substring search per
  keyword per identifier. `line_flows_matches_reference_classifier` asserts equality with
  the retained reference `flow_edge_kind`, including word-boundary cases (`x9return`,
  `9return`) and non-ASCII identifiers.
- `build_graph` keeps one `FileFeatures` store (`next.files`). The former second copy
  (`indexed`) is gone. Features are written to the cache and dropped before the graph
  snapshot is stored.
- Cache I/O is streamed: `serde_json::to_writer` into a buffered atomic temp file, and
  `from_reader` on load. No whole-file `Vec` is built for `files.json` or `graph-*.json`.
- `Cargo.toml` adds `[profile.dev.package.sha2] opt-level = 3` for debug and test runs
  only (the F1 regression hashes 64 MiB). Release builds are unaffected.
- Graph equivalence: on an edge-rich fixture, `symbolgraph build` JSON is identical before
  and after these changes, excluding `generated_at`, `work` and the root-dependent key
  (25,514 symbols, 25,392 edges, 16,857 flow edges).

## What changed, by audit finding

| Finding | Fix (file) | Regression |
| --- | --- | --- |
| 1 stale dirty→dirty graph | `indexcache::source_identity`: `git status --porcelain=v1 -z --untracked-files=all`, untrimmed NUL parse with rename origins, full SHA-256 of every dirty **and untracked** file through the no-follow reader; key = root + HEAD + entries + content tokens | `dirty_to_dirty…`, `same_size_same_mtime…`, `unusual_filenames…` (leading space, `\n`, tab, trailing space), `rename_delete_branch_and_untracked…`; unit `porcelain_z_parses…`, `identity_changes…` |
| 2 symbol caps invisible | tree-sitter/regex extraction bounded at 4096/file with a truncation flag (`treesitter::extract_symbols_limited`, `repomap::extract_source_symbols`); graph symbol budget 100 000; `path-symbols` keeps its 32 display limit but reports `total_symbols`/`symbols_truncated`; changetrack signature baselines and `blast-radius` use complete extraction | `symbols_beyond_prior_caps…` (70 symbols + edge to `symbol_070`), `per_file_symbol_bound_is_reported`, treesitter unit test |
| 2 `unwrap_or_default` read failures | classified reader `symbolgraph::read_source_beneath` (`oversized` via fstat before reading, `symlink`, `not_regular`, `missing`, `unreadable`); invalid UTF-8 indexed lossily and reported | `unreadable_oversized_invalid_utf8…` asserts exact `loss_counts`, `indexed_files=2 of 4` |
| 5 scanner/LSP raw reads | scanner and LSP `didOpen` use the shared bounded no-follow reader | `scanner_skips_files_over…`, `document_text_uses_bounded…` |
| 5 ast-grep buffering, no timeout | `semantic`: streamed stdout, 1 MiB/line, 64 MiB total, 64 KiB stderr, result limit stops the child, 60 s timeout kills it | `ast_grep_child_is_killed_at_timeout` (asserts the PID is gone), `ast_grep_output_is_bounded…` |
| 5 LSP header/queue | 8 KiB header line, 32 header lines, 16 MiB payload (was 64 MiB), notifications dropped before queueing, ≤16 queued messages and ≤32 MiB queued bytes, overflow fails the waiting request | `read_message_rejects_oversized_header`, `inbound_queue_drops_notifications…` |
| 5 `content_cache` retained bodies | graph built from per-file features (symbols, words, imports, inherits, relative specs, flow counts); each body is dropped after analysis | cold RSS 97.5 → 84.9 MiB on the 20 MiB workload (below) |

Plan items also delivered:
- **Incremental and shared.** Per-file features are keyed by identity: `git:<index blob>`
  for files clean against the index (reused without reading), `sha256:<bytes>` for dirty
  ones. Deleted and renamed-away entries drop out. Edges are recomputed from features on
  every build, so dependents are always re-resolved.
- **Cache scope.** Caches live in `<git-dir>/xmustard-cache/index-v2/<sha(canonical root,
  trust scope, parser version)>`. The workspace ID only labels the response.
- **Build serialization.** Builders are serialized across processes by `File::try_lock`
  on `build.lock` (an OS advisory lock, released on exit, so no stale lock), with a bounded
  wait. A waiter re-checks the cache before building.
- **Temp sweep.** Temp files older than 10 min are swept, and legacy per-workspace graph
  and symbol caches are removed.
- **Consistency.** Stability is re-checked after each build. A second identity pass must
  match, and every parsed dirty file must hash to the identity's bytes. Otherwise the result
  is `stable=false`, labeled, and not cached (unit test `edit_during_build_is_unstable…`).
- **Bounded Git.** Index and identity Git calls (`status`, `ls-files -s -z`, `rev-parse`,
  docs `ls-files -z`) run through `indexcache::run_git_bounded`: 32 MiB stdout cap, timeout,
  kill on overflow or timeout, and an explicit error instead of truncation.

## Integration contract for Go (`6a0c9933`)

CLI names, positional arguments, exit codes and existing JSON fields are unchanged. All
additions are new fields or one new command.

### `xmustard-core repo-key <root>` (new)

Exit 0 with JSON; exit 2 on a missing argument. It uses the same identity as
graph-cache invalidation, so `search` → `coverage.source_identity.key` equals `repo-key`
→ `key` for an unchanged tree (test `repo_key_contract_matches_cache_identity`). The
example below is real output for a dirty `a.rs`, an untracked `notes.txt`, an over-cap
`big.rs` and a staged invalid-UTF-8 `bad.rs`:

```json
{"key":"f1b4d7fb…3e6f","head":"d27cda9a…269c",
 "parser_version":"xm-index-2;ts-abi-15;rust-0.24;go-0.25;typescript-0.23;javascript-0.25;regex-2",
 "identity_complete":false,
 "limitations":[{"path":"big.rs","reason":"oversized","detail":"8388609 bytes exceeds the 8388608-byte read cap; not read"}],
 "repo_mode":"git","root":"/abs/canonical/root","dirty_entries":3,"untracked_entries":1,
 "hidden_entries":0,"files_hashed":3,"bytes_hashed":47}
```

- `limitations[]` objects are `{path?, reason, detail?}`. `path` is absent for repo-wide
  reasons, and `detail` is absent when empty. The reasons are `oversized`, `unreadable`,
  `not_regular` (a directory, submodule or fifo), `identity_budget` (more than 64 MiB
  hashed per call), `git_status_failed` (spawn failure, nonzero exit, over 32 MiB of
  output, or timeout), `git_index_flags_failed` (the assume-unchanged/skip-worktree
  listing failed) and `git_unavailable`.
- Coverage of the identity:
  - Included: HEAD; every entry of `git status --porcelain=v1 -z --untracked-files=all
    --ignore-submodules=none`, which covers staged, unstaged, renamed, deleted and
    non-ignored untracked files and dirty submodules; and every assume-unchanged or
    skip-worktree entry, hashed directly.
  - Not included: files ignored by `.gitignore` and other exclude rules. This is a
    definition of "source", not a hidden omission.
  - Snapshot semantics: the identity is taken at one point in time and trusts Git's stat
    checks and any configured fsmonitor. A graph build re-checks it; `repo-key` alone
    does not.
- `identity_complete=false` whenever any limitation exists. Go should treat the key as
  unknown or stale. No key comes from a prefix hash: incomplete entries contribute only a
  metadata token (size, mtime, ctime) and a limitation.
- `head` is `""` for an unborn branch or outside Git. `repo_mode` is `git` or
  `git-unavailable`. `root` is a local absolute path, and Go decides whether to expose it.
- Deleted files are complete (`deleted` token). A symlink's identity is the hash of its
  target string; it is never followed.

### Coverage additions (`search` → `coverage`, `symbolgraph build|build-lsp|hotspots` → `coverage`)

Existing fields keep their names. `indexed_files` now counts only files actually analyzed.
`truncated` also covers per-file symbol and graph-budget truncation.

| Field | Meaning |
| --- | --- |
| `selected_files`, `worktree_deleted_files`, `symbols_truncated_files` | counts |
| `complete` | no losses, stable build, and an identity that determines every tracked (indexed) file; untracked-only limitations appear in `source_identity` only (see F3) |
| `loss_counts{reason:n}` | exact counts, reasons `file_cap`, `oversized`, `unreadable`, `symlink`, `not_regular`, `invalid_path_encoding`, `invalid_utf8`, `excluded_path`, `unsupported_language`, `symbols_truncated`, `symbol_budget` |
| `losses[]` | first 200 `{path, reason, detail?, content_indexed}` sorted by path; `losses_truncated` |
| `extraction{engine:n}` | `tree_sitter`, `regex`, `none`, `excluded_path`, `unsupported_language` |
| `source_identity` | `{key, head, parser_version, identity_complete, stable, limitations}` |
| `work` | this invocation's counters, listed below |

`work` fields:
- `graph_cache`: `hit`, `miss`, `bypass`, or `uncached` (the `symbolgraph build` family);
  `graph_cache_detail` says why a result was not cached.
- Lock: `lock` (`not_used`, `acquired`, `timeout`, `unavailable`), `lock_wait_ms`,
  `lock_contended`.
- Identity: `identity_passes`, `identity_files_hashed`, `identity_bytes_hashed`.
- Read and parse: `files_read`, `bytes_read`, `files_parsed`, `bytes_parsed`,
  `files_reused`.
- Output and housekeeping: `symbols_extracted`, `symbols_indexed`, `coverage_losses`,
  `stale_temps_removed`, `elapsed_ms`.

Real example: a miss with 2 files read and parsed, 2 losses, `stable=true`. The full
dump is in the session transcript. A hit reports `files_read=0` and the requesting
`workspace_id`.

Other additive fields:
- `path-symbols`: `total_symbols` and `symbols_truncated`.
- `symbolgraph blast-radius`: `coverage_losses[]`.
- `semantic-search`: on a timeout or output overrun, `error` names the bound; matches
  found so far are kept.

### Environment (all optional)

| Variable | Default | Effect |
| --- | --- | --- |
| `XMUSTARD_INDEX_TRUST_SCOPE` | `local` | Opaque trust-scope string hashed into the cache scope; set it to the authorization scope Go resolved so different scopes never share a snapshot. |
| `XMUSTARD_INDEX_LOCK_TIMEOUT_MS` | `60000` | Build-lock wait. On timeout the answer is still built and returned, but not stored (`lock=timeout`, `graph_cache=bypass`). |
| `XMUSTARD_GIT_TIMEOUT_MS` | `30000` | Timeout per index or identity Git command. |

Rust children (Git, `sg`, language servers) stay in the Rust process's group, so Go's
`KillProcessTree` on cancellation reaches them. The Rust-side timeouts kill only the
direct child.

## Current metrics: Go owner's fixed fixture

The fixture comes from `scripts/bench/rss_bench.py::generate_repo`, imported unchanged
into scratch. It has 501 tracked files, 21,179,988 bytes and 29,737 symbols (Go-owned;
not edited). Measurements are single-process `/usr/bin/time -l` values in MiB.
Columns: `cd13e2b` base; this tree after F1–F4 (A); after round 1 of the memory/CPU
changes (B, the binary Root measured at 106.4 MB tree peak); and after round 2 (C,
current). Each cell is wall time / RSS / footprint. Each binary ran on its own copy of the
fixture.

| Step | base `cd13e2b` | A | B | C (current) |
| --- | --- | --- | --- | --- |
| cold search (501 parsed) | 12.52 s / 64.8 / 63.3 | 10.77 s / 56.7 / 50.1 | 8.42 s / 46.9 / 40.2 | 7.00 s / 31.5 / 24.7 |
| warm search (hit) | — | 0.23 s / 23.4 / 18.5 | 0.25 s / 20.9 / 16.0 | 0.21 s / 13.3 / 8.5 |
| dirty edit (1 parsed) | — | 0.39 s / 49.3 / 44.0 | 0.41 s / 40.3 / 35.0 | 0.35 s / 21.2 / 15.9 |
| two concurrent cold clients: builder | — | 10.02 s / 56.6 / 50.0 | 7.07 s / 47.7 / 40.1 | 7.00 s / 31.6 / 24.0 |
| two concurrent cold clients: waiter (hit) | — | 10.00 s / 22.5 / 18.5 | 7.10 s / 20.9 / 16.0 | 7.02 s / 12.4 / 8.5 |

Live-heap profile: a scratch binary outside the repo links the library with a counting
global allocator and a counting tree-sitter C allocator.

| Phase | Rust live peak, B | Rust live peak, C |
| --- | --- | --- |
| cold build | 23.5 MiB | 13.4 MiB |
| warm graph load | 4.8 MiB | 4.8 MiB |
| warm `hybrid_search` | 11.8 MiB | 4.8 MiB |

The tree-sitter C heap peaks at 0.8–3.7 MiB in either version.

Round 2 targeted the hotspots that profile found:
- **Definitions and edges.** `name_to_defs` is now `HashMap<&str, Definer>`, borrowing
  from the feature store. `Definer` is `One {path, kind}` or `Ambiguous`, which keeps the
  old rules exactly: first definition per file, ambiguous when a second file defines the
  name. Edge aggregation keys and `via` sets borrow too.
- **Graph symbols.** Symbol names and kinds are *moved* into graph nodes, only after the
  features are stored. They go into a vector sized once. Previously every symbol held a
  cloned name, kind and path, plus a cloned definition entry.
- **Search.** `hybrid_search` tokenizes each symbol when it is needed. It no longer builds
  a `Vec<HashSet<String>>` of token sets for all 29,737 symbols; that was the waiter's
  main allocation.
- **Waiter.** The waiter allocates nothing large before the lock: one small identity
  pass and a missing-snapshot probe. It never clones the graph. Its peak is the graph load
  plus search, after the builder stores the snapshot.

Equivalence: B and C produce identical `symbolgraph build` JSON and identical `search`
results for five queries. This holds on the Go fixture (29,737 symbols) and on an
edge-rich fixture (25,514 symbols, 25,392 edges, 16,857 flow edges). The comparison
excludes `generated_at`, `work` and the root-dependent key.

Current release binary sha256: `d644743b…c3576`, the same bytes as the profiled binary.
Checks: `cargo test` exit 0 (131 + 5 + 12); `cargo clippy --all-targets` exit 0 with the
11 pre-existing warnings.

In B, 5.7 s of the 8.4 s cold time was the fixture's Rust third. Its generated statements lack
`;` (for example `v += 0`), so tree-sitter-rust runs GLR error recovery. The Go and TS
thirds take 0.73 s and 0.76 s. The fixture is Go-owned and unchanged.

Single-process values do not establish the ≤100 MB process-tree gate. Root's rerun
measured B at 106.4 MB, with builder 40,752 KiB and waiter 20,512 KiB. C lowers both
single-process peaks (31.6 and 12.4 MiB here), but only Root's whole-tree rerun on C can
decide the gate.

## Historical metrics (earlier scratch fixture, before F1–F4 and optimization)

Kept for reference; superseded by the section above.

The fixture has 501 tracked files: 500 Go/Rust/TS files at about 40 KB each, 20,177,755
bytes in total, plus a 70-symbol `many.rs`. Release builds of base `cd13e2b` and this
tree ran identical steps on separate copies. Measurements are `/usr/bin/time -l` wall time
and max RSS of the single `xmustard-core` process (macOS reports bytes, converted here to
MiB). The generator and runner were scratch files outside the repo and are not the
integrator's `scripts/bench/rss.sh`.

| Step | Base wall / RSS / gold | New wall / RSS / gold | New counters |
| --- | --- | --- | --- |
| cold search `symbol_070` | 2.60 s / 97.5 MiB / **miss** (capped at 32) | 2.93 s / 84.9 MiB / hit | read and parsed 501 files (20,177,755 B), 25,514 symbols, complete |
| warm search | 0.10 s / 36.3 / miss | 0.20 s / 40.9 / hit | `hit`, files_read 0 |
| same-size, same-mtime dirty edit 1 | 0.63 s / 89.5 / hit | 0.36 s / 76.8 / hit | read/parsed 1 file, reused 500, identity hashed 81,224 B |
| dirty edit 2 (same size, same mtime) | 0.10 s / 36.4 / **stale** (old name) | 0.37 s / 76.7 / hit | read/parsed 1, reused 500 |
| staged rename + worktree delete | 0.64 s / 93.1 / hit | 0.37 s / 79.2 / hit | read/parsed 2, reused 498 |
| `symbolgraph build` (uncached) | 0.54 s / 86.7 | 0.28 s / 75.4 | feature cache reuse |
| `repo-key` | n/a | 0.05 s / 6.5 MiB | complete, 3 dirty entries |
| two concurrent cold clients | n/a | 2.83 s + 2.87 s; 81.4 + 40.0 MiB | one `miss` builds; one `lock_contended`, waits 2,534 ms, then `hit`; one snapshot |

Tradeoffs are reported rather than hidden. Over 10 warm runs, search averaged 142 ms new
vs 102 ms base, and cached-graph `impact` 70 vs 51 ms. The new graph indexes all 25,514
symbols where the base capped at ≤16,000, and it takes one more Git call for identity.
The hidden-entry listing added one more call: after it, `repo-key` averaged 48 ms (was
37) and warm search 153 ms (was 142).
Cold search is 0.33 s slower because extraction is complete and feature/snapshot caches
are written.

These are single-process RSS values. They cannot establish the ≤100 MB xMustard-owned
process-tree gate once the API and MCP processes are included. The concurrent-client
figures are unsynchronized per-process maxima and must not be summed into a tree peak.
The integrator measures the actual concurrent tree on the specified fixture.

## Limits and remaining work (not claimed)

- Git calls outside the index, identity and changetrack path listings still use
  unbounded `.output()`: changetrack's small `rev-parse`/`remote`/`diff` calls, `wiki.rs`,
  `ownership.rs`, and `semantic::find_binary` (`which`).
- The Rust-side ast-grep and Git timeouts kill the direct child only; a grandchild of that
  child could outlive them. Go's group kill covers cancellation. Real `sg` is a single
  process.
- LSP bounds are unit-tested with framed input and a `cat` child. No live language
  server was exercised; `gopls` and `sg` are not installed here.
- A graph is not cached when a tracked file is clean against the index but unreadable, so
  it is retried on every call.
- Untracked files are hashed but never indexed: the graph covers `git ls-files` only.
- A `git_status_failed` key depends only on root and HEAD. It is always incomplete, so
  callers must not compare it as content.
- Non-Unix builds compile the fallbacks. Those fallbacks do not refuse symlinks, and none
  was tested.
- `semantic-onnx` was not built. Not done here, by scope: goal-runtime findings 3 and 4,
  the 12-query fixture, the e2e and RSS scripts, Go/Pi wiring, and the process-tree RSS gate.
