# WS-16 language packs: binary size and worker memory — 2026-09-26

Status: **measured.** All 15 language packs (Rust, Go, TypeScript, TSX, JavaScript,
Python, Java, C, C++, C#, Ruby, PHP, Kotlin, Swift, Bash) are compiled into the default
build and indexed through one provider contract (`rust-core/src/index/lang`). These are
single-machine results under shared load, not a universal ceiling.

## Setup

- Binary: `rust-core/target/release/xmustard-core` from `parity/ws-16`.
- Machine: Apple M1 (arm64), macOS 27.0, shared with other agents.
- Metric: `/usr/bin/time -l` maximum resident set size (RSS) and the macOS peak memory
  footprint (the process's dirty memory), for `index build <repo> --no-cache`.
- Fixtures: `rust-core/tests/fixtures/lang/*` (one file per pack) in throwaway Git
  repositories.

## Binary size (D-22)

| Build | Release binary |
|---|---|
| `--no-default-features` (Go, Rust, TS/TSX, JS grammars only) | 11.9 MB |
| default (`lang-all`: every pack) | 34.1 MB |

The ten added grammars cost **+22.2 MB**, inside D-22's +15–25 MB estimate. Each grammar
is an optional dependency behind its own `lang-<name>` feature; with a feature off, the
pack keeps its extensions and precompiled lexical patterns, and coverage reports its
files as `unsupported`.

## Worker memory per language present

One fixture file per repository (the Go row is the baseline):

| Language present | Peak RSS | Footprint |
|---|---|---|
| Go (baseline) | 9.8 MiB | 2.9 MiB |
| Python | 11.7 MiB | 4.5 MiB |
| Java | 11.0 MiB | 3.8 MiB |
| C | 11.4 MiB | 4.0 MiB |
| PHP | 12.8 MiB | 5.0 MiB |
| Bash | 15.3 MiB | 6.9 MiB |
| Ruby | 15.6 MiB | 6.4 MiB |
| C++ | 17.7 MiB | 7.6 MiB |
| Kotlin | 18.8 MiB | 8.8 MiB |
| C# | 19.0 MiB | 7.2 MiB |
| Swift | 19.6 MiB | 9.3 MiB |
| all 15 packs (3 files each) | 48.4 MiB | 20.3 MiB |

Parsing costs about 1 MiB per grammar. The rest is compiling the pack's tag query once
per worker: tree-sitter's query analysis scans the grammar's whole parse table and
builds a per-state predecessor map, whatever the query. The scan pages in the parse
table (clean, shareable pages, counted in RSS) and allocates transient analysis memory
(dirty, freed after compilation). The larger grammars (C++, C#, Kotlin, Swift) cost
the most.

Consequences:

- The spec fixture (5,000 Go/Rust/TS/JS files, `tests/index_build.rs`) stays under the
  25 MiB RSS line with every pack compiled in: a pack costs memory only when its
  language is present.
- A worker that parses every pack peaks at about 50 MiB RSS but 16–21 MiB dirty.
  `tests/lang_fixtures.rs` builds 5,000 files spread over all 15 packs and asserts a
  dirty peak of at most 25 MiB and an RSS peak of at most 64 MiB.
- A deployment that must hold RSS under 25 MiB on polyglot repositories can drop the
  largest packs with `--no-default-features --features lang-python,lang-java,...`.
  Those files are then indexed lexically and counted as `unsupported`.

## Coverage honesty

- Index coverage (`index build|update|stats`) reports `language_support` per language:
  `supported` (grammar), `unsupported` (no grammar in this build: lexical symbols and
  references only) and `failed` (grammar compiled in, but the file was extracted
  lexically past the parse bounds or parsed with errors).
- Graph coverage (search, explain, impact) reports `languages` with the same three
  counts. `failed` there means the file was extracted by the legacy regexes: it is past
  the parse bounds, or tree-sitter found no symbols in it but the regexes did.
- `ground` carries the graph's coverage block (`coverage`: `complete` and `languages`)
  in its index section, from `xmustard-core symbolgraph coverage`. When the core cannot
  give it, `coverage` is null and listed in `unknown`.

## Parse bounds on the graph path

The graph path (`repomap::extract_source_symbols`, used by search, explain and impact)
applies the index's parse refusal (`DEFAULT_MAX_PARSE_BYTES`, `MAX_PARSE_TOKENS`,
`MAX_PARSE_NESTING`) before tree-sitter. Before this, it parsed any grammar file up to
8 MiB: a generated 1 MB C file peaked at 66 MiB footprint and a 4 MB one at 252 MiB.
Files past the bounds now take the regexes (for every language, including Go, Rust,
TS and JS, which were parsed to 8 MiB before) and count as `failed`.

## Known limitations of `exported`

The export rules read the declaration header only:

- C++ members under a `private:` or `protected:` access specifier are reported
  `exported=true` (the specifier is not in the member's header).
- Java interface members and enum constants are implicitly public but are reported
  `exported=false`.
- In Kotlin and Swift, a word such as `private` inside an annotation argument in the
  header (`@Suppress("private")`) flips the result.

## Query provenance

Every query in `rust-core/src/index/lang/queries/` is original to this repository and
says so in its first line. The capture vocabulary (`@definition.*`, `@reference.*`,
`@name`) follows the MIT tree-sitter `tags.scm` convention. No query is taken from
GitNexus. All ten added grammar crates are MIT-licensed.

## Not covered

- Framework entry points, and the provider fields for import-resolver configuration
  and entry-point patterns, are left to WS-17/WS-39 (resolvers) and WS-60 (entry-point
  extractors). Imports are recorded with their semantics (`import`, `wildcard`,
  `namespace`, `require`, `include`) but are not resolved for the added languages.
- Scala and Lua (optional in PAR-SYM-05) are not packed.
