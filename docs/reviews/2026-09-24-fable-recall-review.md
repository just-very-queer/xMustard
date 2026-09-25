# Stage 1a recall review — PARTIAL (not full-program approval)

Scope: Stage 1a only (bounded plain recall, version-bound content, admin full history) in worktree
`/private/tmp/xmustard-opus-l9b1F4` vs `cd13e2b`. Files: `context_governance.go`,
`recall_contract_test.go`, `recall_route_test.go`, the `GET /context/active` handler. Snapshot
reviewed: context_governance.go sha256 85fd9382…, main.go abd9bbc5… (main.go was being edited
concurrently; line numbers shifted during review). Lifecycle/cancel/evidence/index/Pi not judged.

Verified: `go build ./...`, `go vet`, and `go test ./internal/workspaceops ./cmd/xmustard-api -count=1`
pass. Reviewed files are gofmt-clean (12 gofmt-unclean files exist but are unchanged since cd13e2b).

## Verdict: CHANGES_REQUIRED

### What is correct
- Root's truncated-hash finding is addressed in current source: acceptance compares a full SHA-256
  `ContentDigest`; the 64-bit `ContentHash` only names the content file. Legacy caches without a
  digest fail closed to the source and are migrated under the store lock. Both have tests.
- Update/recall interleaving: `saveContextEntries` writes source → meta cache; content file after.
  Every intermediate state yields either a fresh reload or a digest mismatch, so the 3-attempt
  retry and final withhold are sound. `consistency_attempts`/`consistency_withheld` are reported.
- Derived `ContentHash`/`ContentDigest` are cleared before return on the meta-fast path; the
  source-loaded paths never populate them.
- `?scope=all` requires admin once auth is configured; in open local mode `requireRole` allows it,
  consistent with the rest of the API. MCP `recall` cannot reach `scope`/`limit`.

### Findings
1. **Implicit working-change focus has no effect (results doc claim is false).** In `recallOnce`,
   `score := boost` and relevance is only added when `hasSignal`, which is now
   `explicitSignal && …`. With no query/paths the `focusSet` overlap (+1.5) is computed and discarded,
   so plain recall is pure recency/approval ranking and `currentChangedFiles` spawns a Rust child for
   nothing. Fix: `score = relevance + boost` when `!hasSignal` (gate only when explicit).
   Test: seed 12 entries, give the OLDEST `Paths: ["changed.go"]`, stub `recallChangedFiles` to
   return it, assert it appears (first) in the top-8; today it is excluded.
2. **Full promoted history still leaks via the sibling route.** `GET /context?filter=promoted`
   (`ListContextEntries`) returns every promoted entry with content to any principal, including
   readonly, and `"context"` is in `coreWorkspaceSubpaths` (method-agnostic), so CORE_ONLY does not
   block it. The plan's "retain full history only behind an explicitly administrative read" is not
   met. Fix: gate promoted/active/all filters on that route behind `requireRole(admin)` or restrict
   the allowlist entry to POST. Test: agent token GET `/context?filter=promoted` → 403.
3. **Limit cap ignores instead of clamps.** `limit=51` silently becomes 8, while the results doc
   says "capped at 50". Clamp to `maxRecallLimit` (or 400) and test `limit=500` → 50 entries.
4. **Race test under-asserts.** `TestRecallNeverAttachesRevisedContentToOldApproval` only checks
   absence of revised text. Add `consistency_attempts == 2`, `returned == 2`, and no
   `consistency_withheld`, so a regression that withholds everything (or never retries) fails.
5. **Withhold leaves no backfill.** On the last attempt withheld entries shrink `returned` below
   `limit` with no replacement. Acceptable fail-closed behavior, but document it in the response
   contract (`docs/CONTEXT_LAYER.md`) or backfill from the candidate window.
6. **Doc accuracy.** Results doc says stdio-MCP coverage is in `scripts/e2e/mcp-evidence.sh`
   (Stage 5, not yet present); mark it pending. Also `api-go/cmd/xmustard-api/xmustard-api` is an
   untracked, non-ignored build binary in the worktree — delete before commit.

### Minor
- `recallChangedFiles`/`recallBeforeContentLoad` are package globals mutated by tests; the
  `defer` resets are correct but tests in this package cannot run with `t.Parallel()`.
- `migrateContextMetaCache` runs on every recall until it succeeds; fine, but a failed write
  (read-only data dir) would repeat a full source read per recall. Consider a once-per-process guard.

Findings 1 and 2 block approval; 3–4 are small and should land with them.

## Root disposition

Confirmed findings 1–2 in the current source. Close the sibling full-history route
for every full-history filter/default, not just the literal promoted filter, while
preserving agent POST remember. Apply findings 3–4 and document 5–6 honestly.
Keep generated binaries out of import; no runtime data or unrelated files need
deleting. The optional once-per-process migration suggestion is not a completion
requirement. This is an intermediate review; final full-program review remains.

Review session: `5357ccec-8020-4f79-8898-37071bb61484`; actual model
`claude-fable-5-1`.
