# WS-RT1 baseline: pass counters on today's code (2026-10-04)

What each `xmustard-core` call and three API calls cost on the streaming-runtime fixtures, measured with the WS-RT1 pass counters before RT2, RT3 and RT6 change any pass. Not a parity claim and not a gate.

**Method.** Linux build box (6 cores, shared with lane work; `uptime` load 9.83 at the start and 4.53 at the end; git 2.53.0). Release `xmustard-core` and `xmustard-api` built from `parity/ws-rt1` at `9791656c` (the counters add no behaviour). Fixtures from `scripts/bench/rt_fixtures.sh` on the box's disk, removed afterwards (5.2 GB in total). Calls from `scripts/bench/rt_baseline.sh`, each under `/usr/bin/time -v timeout 300`, single runs: these are indications, not benchmarks. Max RSS is `ru_maxrss` of the call and its waited children, so it includes a `git` child. API rows: wall time from `curl`, counters are the growth of `/api/health` `budget.counters.core_passes` across the request (every core call it made, background work that ran meanwhile included), and no RSS. Box log: `.git/xm-parity/logs/ws-rt1-run-20261003T192828Z.log`; rows: `.git/xm-parity/logs/ws-rt1-baseline-20261003T192828Z/rows.jsonl` (218 rows); tables below generated from the rows.

**Fixtures.** `c0-control` (a.go, b.go, c.go); nested: `n1-nested` (unregistered repo at `sub/`), `n2-deep` (`a/b/sub` with a repo inside it), `n3-ignored` (repo inside an ignored `vendor/`), `n4-bare` (bare repo `bare.git/`), `n5-gitfile` (`.git` file pointing to a separate gitdir, plus a broken pointer), `g1-gitlink` (embedded repo added as a gitlink with no `.gitmodules`), `s1-sub-dirty`, `s2-sub-uninit`, `s3-sub-ignall` (registered submodule: dirty, uninitialised, `ignore = all`); scale: `u200k` (3 tracked files plus 200,000 untracked 10-byte `.go` files), `t100k` and `t1m` (100,000 and 1,000,000 tracked paths, `dNNNN/fNNNN.{go,txt,md,py}`, about 20-byte paths, half of them `.go`/`.py`). Generation took 0.52 s (nested), 11.2 s (u200k), 5.6 s (t100k) and 47.4 s (t1m).

## Readings

1. **Every refresh pays a full listing.** `index update` and `index update --paths a.go` list and stat every tracked path: 100,000 at t100k (0.32 s) and 1,000,000 at t1m (4.33 s and 3.56 s, 210.6 MiB max RSS, the git child), with 0 files hashed and 10,000 stored rows loaded. This is G1 and G2 of the design; RT6's acceptance (`files_listed` at most the batch size, 500 ms or less) is measured against these rows.
2. **The 1M tree's full passes.** `repo-key` lists 1,000,000 records (7.3 s cold, 1.06 s warm; 211.0 MiB, the git child), `changetrack working-changes`/`drift` list 1,000,000 (about 1.1 s each), `symbolgraph build` returns in 1.33 s with 5 git spawns, 1,000,000 listed records and 0 stats: its own `ls-files -s` listing passes the 32 MiB cap at this size (65 MB in the evidence), and a listing that overflows is killed and counts no records, so the records are the identity's listing. The output of that call was not kept, so whether the graph was empty is the evidence's finding, not re-checked here. `changetrack index` timed out at 300 s (rc 124, 380.4 MiB max RSS, no counters, since a killed call prints nothing). `build-repo-map` walks 1,000,000 files and stats 500,001. The API registration (`POST /api/workspaces/load`) returned HTTP 500 after 121.94 s (`rust-core scan-signals timed out`), so the t1m ground and search rows were not run; its core_passes growth is 0 because the timed-out children reported nothing.
3. **Untracked files are hashed per identity pass.** On u200k, `repo-key` lists 200,003 records, stats and hashes 200,000 files (2,000,000 bytes) cold; the warm call hashes 0 and loads 200,000 stat-cache rows (2.29 s, 128.7 MiB). `symbolgraph build` lists 400,009, stats 400,003 and loads 400,000 rows (3.17 s, 175.9 MiB). One ground through the API costs 800,012 listed records, 800,200 stats, 800,003 loaded rows and 26 git spawns (3.73 s); the second 32 spawns (6.45 s). The index itself is untouched (3 files). This is G4b and G6; RT3 bounds it.
4. **One outlier, rerun.** The first u200k `repo-key` took 293.72 s, right after the fixture was written under load 9.8. A rerun on a fresh fixture with the identity cache removed took 2.20 s and 1.91 s with identical counters (listed 200,003, statted 200,000, hashed 200,000, bytes 2,000,000, 4 git spawns; `.git/xm-parity/logs/ws-rt1-run-20261003T194739Z.log`). The counters agree across all three runs; the 293 s is a wall-time outlier of a loaded box, not a different pass.
5. **Nested repositories cost little but list foreign files.** The nested and submodule cases stay under 12 MiB and 0.1 s per core call. `build-repo-map` counts the files of unregistered nested repos (n1 lists 5 where the index has 3; n2 lists 8 where it has 4), and the bare repo's 18 internal files are listed, statted and hashed by the identity (n4 `repo-key1`: 21 listed, 18 hashed, 26,227 bytes). These are RT2's cases.
6. **Queries are resident-sized.** `search` and `index impact` list nothing and load the resident file table (3 rows on the small fixtures, 10,000 on t100k and t1m), with 2 git spawns each, at 10.4-11.5 MiB.

## Tables

### c0-control

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.11 | 10.8 | 3 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.5 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.6 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 8.8 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.02 | 7.7 | 3 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.8 | 3 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.16 | 8.3 | 3 | 6 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 0.0 | 7.7 | 3 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.0 | 7.8 | 3 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.05 | 8.8 | 9 | 3 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.4 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.5 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.6 | 3 | 3 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.16 | - | 6 | 3 | 0 | 0 | 6 | 0 |
| api-ground1 | 200 | 0.03 | - | 9 | 0 | 0 | 0 | 20 | 3 |
| api-ground2 | 200 | 0.02 | - | 6 | 0 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.03 | - | 6 | 7 | 0 | 0 | 10 | 6 |

### n1-nested

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.01 | 10.9 | 3 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.2 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.8 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 8.9 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.7 | 4 | 3 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.6 | 4 | 3 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.07 | 8.2 | 3 | 6 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 0.0 | 7.6 | 4 | 3 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.0 | 7.6 | 4 | 3 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.04 | 9.1 | 11 | 9 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.8 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.5 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.9 | 5 | 5 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.51 | - | 5 | 5 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.1 | - | 8 | 6 | 0 | 0 | 14 | 3 |
| api-ground2 | 200 | 0.02 | - | 8 | 6 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.05 | - | 7 | 10 | 0 | 0 | 10 | 6 |

### n2-deep

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.02 | 10.8 | 4 | 8 | 4 | 205 | 4 | 0 |
| index-update | 0 | 0.0 | 9.2 | 4 | 4 | 0 | 0 | 4 | 4 |
| index-update-paths | 0 | 0.0 | 9.7 | 4 | 4 | 0 | 0 | 4 | 4 |
| index-stats | 0 | 0.0 | 9.0 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.6 | 5 | 3 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.7 | 5 | 3 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.03 | 8.8 | 4 | 8 | 4 | 205 | 6 | 0 |
| ct-working | 0 | 0.0 | 7.7 | 5 | 3 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.0 | 7.6 | 5 | 3 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.02 | 8.5 | 14 | 10 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.5 | 0 | 6 | 0 | 0 | 2 | 4 |
| impact | 0 | 0.0 | 9.5 | 0 | 3 | 0 | 0 | 2 | 4 |
| repo-map | 0 | 0.0 | 7.6 | 8 | 8 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.46 | - | 8 | 8 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.08 | - | 10 | 6 | 0 | 0 | 14 | 4 |
| api-ground2 | 200 | 0.02 | - | 10 | 6 | 0 | 0 | 14 | 4 |
| api-search | 200 | 0.05 | - | 9 | 13 | 0 | 0 | 10 | 8 |

### n3-ignored

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.03 | 10.9 | 4 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.9 | 4 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.4 | 4 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 8.9 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.7 | 5 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.01 | 7.8 | 5 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.03 | 8.9 | 4 | 7 | 4 | 167 | 6 | 0 |
| ct-working | 0 | 0.03 | 7.8 | 4 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.01 | 7.7 | 4 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.03 | 8.2 | 12 | 3 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.3 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.9 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.6 | 4 | 3 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.21 | - | 4 | 3 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.09 | - | 12 | 7 | 0 | 0 | 20 | 7 |
| api-ground2 | 200 | 0.02 | - | 8 | 0 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.04 | - | 9 | 7 | 0 | 0 | 10 | 6 |

### n4-bare

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.02 | 10.7 | 3 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.3 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.01 | 10.0 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 9.0 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.02 | 7.5 | 21 | 18 | 18 | 26227 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.5 | 21 | 18 | 0 | 0 | 4 | 18 |
| ct-index | 0 | 0.03 | 8.4 | 3 | 6 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 0.0 | 8.0 | 21 | 18 | 0 | 0 | 6 | 18 |
| ct-drift | 0 | 0.0 | 7.6 | 21 | 18 | 0 | 0 | 6 | 18 |
| sg-build | 0 | 0.04 | 8.2 | 45 | 39 | 0 | 0 | 9 | 36 |
| search | 0 | 0.0 | 10.3 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.7 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.7 | 21 | 3 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.06 | - | 21 | 3 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.05 | - | 42 | 36 | 0 | 0 | 14 | 39 |
| api-ground2 | 200 | 0.02 | - | 42 | 36 | 0 | 0 | 14 | 39 |
| api-search | 200 | 0.03 | - | 24 | 25 | 0 | 0 | 10 | 24 |

### n5-gitfile

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.01 | 10.8 | 3 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.7 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.5 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 9.4 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.7 | 5 | 4 | 1 | 43 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.7 | 5 | 4 | 0 | 0 | 4 | 1 |
| ct-index | 0 | 0.01 | 8.5 | 3 | 6 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 0.0 | 8.8 | 5 | 5 | 0 | 0 | 6 | 1 |
| ct-drift | 0 | 0.0 | 7.8 | 5 | 4 | 0 | 0 | 6 | 1 |
| sg-build | 0 | 0.01 | 8.4 | 13 | 11 | 0 | 0 | 9 | 2 |
| search | 0 | 0.0 | 10.5 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.7 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.6 | 5 | 5 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.16 | - | 10 | 9 | 0 | 0 | 6 | 1 |
| api-ground1 | 200 | 0.04 | - | 10 | 9 | 0 | 0 | 14 | 5 |
| api-ground2 | 200 | 0.02 | - | 10 | 9 | 0 | 0 | 14 | 5 |
| api-search | 200 | 0.05 | - | 8 | 11 | 0 | 0 | 10 | 7 |

### g1-gitlink

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.02 | 11.0 | 4 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.6 | 4 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.5 | 4 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 9.1 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.6 | 4 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.7 | 4 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.02 | 8.5 | 4 | 7 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 0.0 | 7.6 | 4 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.01 | 7.8 | 4 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.02 | 8.2 | 12 | 3 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.7 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.8 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.7 | 5 | 5 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.11 | - | 5 | 5 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.05 | - | 8 | 0 | 0 | 0 | 14 | 3 |
| api-ground2 | 200 | 0.02 | - | 8 | 0 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.05 | - | 8 | 7 | 0 | 0 | 10 | 6 |

### s1-sub-dirty

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.02 | 10.9 | 5 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.4 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.4 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 8.9 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.8 | 6 | 3 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.6 | 6 | 3 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.02 | 8.4 | 5 | 8 | 4 | 235 | 6 | 0 |
| ct-working | 0 | 0.01 | 7.6 | 6 | 3 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.01 | 7.6 | 6 | 3 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.03 | 8.7 | 17 | 9 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.3 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.6 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.6 | 7 | 5 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.13 | - | 7 | 5 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.03 | - | 12 | 6 | 0 | 0 | 14 | 3 |
| api-ground2 | 200 | 0.03 | - | 12 | 6 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.04 | - | 11 | 10 | 0 | 0 | 10 | 6 |

### s2-sub-uninit

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.01 | 10.7 | 5 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.0 | 9.6 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.0 | 9.4 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 9.3 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.6 | 5 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.0 | 7.7 | 5 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.01 | 8.1 | 5 | 8 | 4 | 235 | 6 | 0 |
| ct-working | 0 | 0.0 | 7.6 | 5 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.0 | 7.7 | 5 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.01 | 8.4 | 15 | 3 | 0 | 0 | 9 | 0 |
| search | 0 | 0.0 | 10.4 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 10.0 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.0 | 7.6 | 4 | 3 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.2 | - | 4 | 3 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.03 | - | 15 | 0 | 0 | 0 | 20 | 3 |
| api-ground2 | 200 | 0.02 | - | 10 | 0 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.03 | - | 10 | 7 | 0 | 0 | 10 | 6 |

### s3-sub-ignall

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.02 | 11.3 | 5 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.01 | 9.5 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.01 | 9.6 | 5 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 8.8 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.01 | 7.9 | 6 | 3 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.02 | 7.6 | 6 | 3 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 0.03 | 8.4 | 5 | 8 | 4 | 249 | 6 | 0 |
| ct-working | 0 | 0.03 | 7.5 | 6 | 3 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.04 | 7.6 | 6 | 3 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.06 | 8.6 | 17 | 9 | 0 | 0 | 9 | 0 |
| search | 0 | 0.02 | 10.8 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.02 | 9.9 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.01 | 7.6 | 6 | 5 | 0 | 0 | 0 | 0 |
| api-load | 200 | 1.8 | - | 18 | 11 | 0 | 0 | 12 | 0 |
| api-ground1 | 200 | 0.31 | - | 12 | 6 | 0 | 0 | 14 | 3 |
| api-ground2 | 200 | 0.08 | - | 12 | 6 | 0 | 0 | 14 | 3 |
| api-search | 200 | 0.86 | - | 11 | 10 | 0 | 0 | 10 | 6 |

### u200k

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 0.1 | 10.8 | 3 | 6 | 3 | 159 | 4 | 0 |
| index-update | 0 | 0.03 | 9.7 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-update-paths | 0 | 0.01 | 9.5 | 3 | 3 | 0 | 0 | 4 | 3 |
| index-stats | 0 | 0.0 | 9.5 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 293.72 | 109.2 | 200003 | 200000 | 200000 | 2000000 | 4 | 0 |
| repo-key2 | 0 | 2.29 | 128.7 | 200003 | 200000 | 0 | 0 | 4 | 200000 |
| ct-index | 0 | 0.04 | 8.2 | 3 | 6 | 3 | 159 | 6 | 0 |
| ct-working | 0 | 1.84 | 129.0 | 200003 | 200200 | 0 | 0 | 6 | 200000 |
| ct-drift | 0 | 1.61 | 129.4 | 200003 | 200000 | 0 | 0 | 6 | 200000 |
| sg-build | 0 | 3.17 | 175.9 | 400009 | 400003 | 0 | 0 | 9 | 400000 |
| search | 0 | 0.01 | 10.9 | 0 | 4 | 0 | 0 | 2 | 3 |
| impact | 0 | 0.0 | 9.6 | 0 | 3 | 0 | 0 | 2 | 3 |
| repo-map | 0 | 0.67 | 7.6 | 200003 | 200003 | 0 | 0 | 0 | 0 |
| api-load | 200 | 58.26 | - | 200003 | 200003 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 3.73 | - | 800012 | 800200 | 0 | 0 | 26 | 800003 |
| api-ground2 | 200 | 6.45 | - | 800015 | 800206 | 0 | 0 | 32 | 800006 |
| api-search | 200 | 1.6 | - | 200006 | 200007 | 0 | 0 | 10 | 200006 |

### t100k

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 4.28 | 25.0 | 100000 | 112000 | 12000 | 335546 | 4 | 0 |
| index-update | 0 | 0.32 | 25.1 | 100000 | 100000 | 0 | 0 | 4 | 10000 |
| index-update-paths | 0 | 0.32 | 25.1 | 100000 | 100000 | 0 | 0 | 4 | 10000 |
| index-stats | 0 | 0.01 | 11.1 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 0.91 | 25.4 | 100000 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 0.11 | 25.4 | 100000 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 0 | 31.8 | 44.0 | 100000 | 150001 | 100000 | 2012578 | 6 | 0 |
| ct-working | 0 | 0.2 | 44.6 | 100000 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 0.18 | 43.4 | 100000 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 0.56 | 39.1 | 300000 | 10000 | 0 | 0 | 9 | 0 |
| search | 0 | 0.05 | 11.5 | 0 | 4 | 0 | 0 | 2 | 10000 |
| impact | 0 | 0.01 | 10.9 | 0 | 2 | 0 | 0 | 2 | 10000 |
| repo-map | 0 | 0.23 | 7.6 | 100000 | 50001 | 0 | 0 | 0 | 0 |
| api-load | 200 | 3.11 | - | 100000 | 50001 | 0 | 0 | 0 | 0 |
| api-ground1 | 200 | 0.26 | - | 400000 | 0 | 0 | 0 | 26 | 10000 |
| api-ground2 | 200 | 0.24 | - | 200000 | 0 | 0 | 0 | 14 | 10000 |
| api-search | 200 | 0.58 | - | 400000 | 0 | 0 | 0 | 15 | 10000 |

### t1m

| call | rc | wall s | max RSS MiB | listed | statted | hashed | bytes hashed | git spawns | rows loaded |
|---|---|---|---|---|---|---|---|---|---|
| index-build | 0 | 13.56 | 210.7 | 1000000 | 1012000 | 12000 | 335546 | 4 | 0 |
| index-update | 0 | 4.33 | 210.6 | 1000000 | 1000000 | 0 | 0 | 4 | 10000 |
| index-update-paths | 0 | 3.56 | 210.7 | 1000000 | 1000000 | 0 | 0 | 4 | 10000 |
| index-stats | 0 | 0.01 | 11.2 | 0 | 0 | 0 | 0 | 1 | 0 |
| repo-key1 | 0 | 7.3 | 211.0 | 1000000 | 0 | 0 | 0 | 4 | 0 |
| repo-key2 | 0 | 1.06 | 210.9 | 1000000 | 0 | 0 | 0 | 4 | 0 |
| ct-index | 124 | 300.01 | 380.4 | - | - | - | - | - | - |
| ct-working | 0 | 1.13 | 210.8 | 1000000 | 0 | 0 | 0 | 6 | 0 |
| ct-drift | 0 | 1.09 | 211.0 | 1000000 | 0 | 0 | 0 | 6 | 0 |
| sg-build | 0 | 1.33 | 210.9 | 1000000 | 0 | 0 | 0 | 5 | 0 |
| search | 0 | 0.06 | 11.2 | 0 | 4 | 0 | 0 | 2 | 10000 |
| impact | 0 | 0.01 | 11.2 | 0 | 2 | 0 | 0 | 2 | 10000 |
| repo-map | 0 | 2.35 | 7.5 | 1000000 | 500001 | 0 | 0 | 0 | 0 |
| api-load | 500 | 121.94 | - | 0 | 0 | 0 | 0 | 0 | 0 |
