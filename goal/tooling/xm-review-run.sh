#!/bin/bash
# usage: xm-review-run.sh <WS-ID> <worktree> <round> [base-ref]
# Runs every mandatory review channel on one committed workstream diff and saves the raw
# outputs under $XM_REVIEW_ROOT/<ws>/r<round>/, sealing each output's sha256 into range.txt.
# It never edits the worktree. v2 (2026-09-30): refuses an empty plan section; seals outputs.
set -uo pipefail
WS=${1:?WS id, e.g. WS-43}; WT=${2:?worktree}; N=${3:?round number}; BASE_REF=${4:-feat/parity-v2}
ROOT=${XM_REVIEW_ROOT:-/Users/for_work/Developer/xMustard/.git/xm-parity/reviews}
R="$ROOT/$(printf %s "$WS" | tr 'A-Z' 'a-z')/r$N"
OCR=${XM_OCR:-/Users/for_work/.local/bin/ocr}
CODEX=${XM_CODEX:-/opt/homebrew/bin/codex}
OPENCODE=${XM_OPENCODE:-/opt/homebrew/bin/opencode2}
AUTHOR=${XM_AUTHOR:?set XM_AUTHOR to the model id that wrote the change}
CODEX_MODEL=${XM_CODEX_REVIEW_MODEL:?set XM_CODEX_REVIEW_MODEL to a Codex model other than the author}
OC_MODEL=${XM_OPENCODE_REVIEW_MODEL:-zai-coding-plan/glm-5.3}
[ "$CODEX_MODEL" != "$AUTHOR" ] || { echo "refused: the author may not review its own change ($AUTHOR)"; exit 2; }
OPS=${XM_OPS_REVIEW:-}                                   # xmustard-ops built with -tags review
DATA=${XM_SCRATCH_DATA:-/Users/for_work/.xm-parity-scratch/data}   # never ~/.xmustard-eval/data
PLAN=${XM_PLAN:-$WT/docs/plans/2026-09-25-parity-build-plan.md}
HERE=$(cd "$(dirname "$0")" && pwd)

[ -z "$(git -C "$WT" status --porcelain)" ] || { echo "refused: $WT has uncommitted changes"; exit 2; }
[ -e "$R" ] && { echo "refused: $R exists; use the next round number"; exit 2; }
mkdir -p "$R"
MB=$(git -C "$WT" merge-base "$BASE_REF" HEAD) || exit 2
HD=$(git -C "$WT" rev-parse HEAD)
printf 'ws=%s\nbase_ref=%s\nmerge_base=%s\nhead=%s\nauthor=%s\ncodex_model=%s\nopencode_model=%s\nstarted=%s\n' \
  "$WS" "$BASE_REF" "$MB" "$HD" "$AUTHOR" "$CODEX_MODEL" "$OC_MODEL" "$(date -u +%FT%TZ)" > "$R/range.txt"
git -C "$WT" diff "$MB" "$HD" > "$R/diff.patch"
awk -v h="### $WS " 'index($0,h)==1{p=1;print;next} p&&/^### /{exit} p' "$PLAN" > "$R/background.md"
[ "$(wc -c < "$R/background.md")" -gt 40 ] || { echo "refused: the plan has no '### $WS ' section; add the spec-only commit first (goal §3 step 1)"; rm -rf "$R"; exit 2; }
cp "$HERE/findings.schema.json" "$R/findings.schema.json"
rc() { echo "$1=$2" >> "$R/exit-codes.txt"; }

# 1. OpenCodeReview: file list, rules, and the LLM review when a provider is configured.
"$OCR" delegate preview --repo "$WT" --from "$MB" --to "$HD" -f json > "$R/ocr-preview.json" 2> "$R/ocr-preview.err"; rc ocr_preview $?
jq -r '.reviewable_files[].path' "$R/ocr-preview.json" | xargs "$OCR" delegate rule --repo "$WT" --from "$MB" --to "$HD" > "$R/ocr-rules.md" 2> "$R/ocr-rules.err"; rc ocr_rules $?
if "$OCR" llm test > "$R/ocr-llm-test.txt" 2>&1; then
  "$OCR" review --repo "$WT" --from "$MB" --to "$HD" --audience agent --format json --effort high \
    --background-file "$R/background.md" --output "$R/ocr-review.json" > "$R/ocr-review.log" 2>&1; rc ocr_review $?
else
  rc ocr_review unavailable   # the reviewer then writes ocr-delegate.findings.json by hand (appendix B3)
fi

PROMPT="Independent adversarial review of workstream $WS of xMustard. The change is git diff $MB..$HD in this repository (base $BASE_REF). \
Spec: the section '### $WS' of docs/plans/2026-09-25-parity-build-plan.md and its PAR rows in docs/research/PARITY_REQUIREMENTS_2026-09-25.md. \
Read the code; do not edit, build or commit. Report only defects you can point to in the new code: correctness, security and trust, memory and output budgets, \
spec conformance, tests that assert nothing or restate the implementation, stubs or TODOs counted as done, dead flags or config, duplicate helpers, \
docs that claim behaviour the code does not have, and if/else ladders a table, match or interface should replace (style, low). \
Quote the offending new-file lines verbatim in existing_code. Put every changed file in coverage, reviewed or not_reviewed with a reason."

# 2. Codex built-in review (its own prompt; --base cannot take custom instructions).
(cd "$WT" && "$CODEX" exec review --base "$BASE_REF" -m "$CODEX_MODEL" -c 'sandbox_mode="read-only"' --ephemeral \
  -o "$R/codex-review.md" < /dev/null > "$R/codex-review.log" 2>&1); rc codex_review $?

# 3. Codex spec review with a findings schema (OCR's closed finding shape plus coverage).
"$CODEX" exec -C "$WT" -m "$CODEX_MODEL" -s read-only --ephemeral --output-schema "$R/findings.schema.json" \
  -o "$R/codex-spec.findings.json" "$PROMPT" < /dev/null > "$R/codex-spec.log" 2>&1; rc codex_spec $?

# 4. OpenCode review on a non-OpenAI model, read-only plan agent, diff attached.
(cd "$WT" && "$OPENCODE" run --agent plan -m "$OC_MODEL" --format json --file "$R/diff.patch" \
  "$PROMPT Output, as your final message, exactly one JSON object between the lines BEGIN_FINDINGS and END_FINDINGS with keys findings (items: path, content, existing_code, category, severity, start_line, end_line; category bug|security|performance|maintainability|test|style|documentation|other; severity critical|high|medium|low) and coverage (items: path, status reviewed|not_reviewed, reason)." \
  < /dev/null > "$R/opencode.jsonl" 2> "$R/opencode.err"); rc opencode_run $?
jq -rs '[.[]|select(.type=="text")|.part.text]|join("\n")' "$R/opencode.jsonl" | python3 -c '
import json, re, sys
blocks = re.findall(r"BEGIN_FINDINGS\s*(.*?)\s*END_FINDINGS", sys.stdin.read(), re.S)
if not blocks: sys.exit("no BEGIN_FINDINGS block in the OpenCode output")
body = re.sub(r"^```(?:json)?\s*|\s*```$", "", blocks[-1].strip())
json.dump(json.loads(body), open(sys.argv[1], "w"), indent=1)' "$R/opencode.findings.json"; rc opencode_extract $?

# 5. xMustard's own review anchoring: which quoted findings exist in the change (dogfood, evidence only).
if [ -n "$OPS" ]; then
  case "$DATA" in "$HOME/.xmustard-eval"*) echo "refused: $DATA is the owner's install"; exit 2;; esac
  mkdir -p "$DATA"
  WSID=$(XMUSTARD_DATA_DIR="$DATA" "$OPS" workspace load --root-path "$WT" 2> "$R/xm-load.err" | jq -r .workspace.workspace_id)
  for f in codex-spec.findings.json opencode.findings.json ocr-review.json; do
    [ -s "$R/$f" ] || continue
    XMUSTARD_DATA_DIR="$DATA" "$OPS" review anchor "$WSID" --base "$MB" --head "$HD" --findings "$R/$f" --data-dir "$DATA" \
      > "$R/${f%.json}.anchor.json" 2> "$R/${f%.json}.anchor.err"; rc "anchor_${f%.json}" $?
  done
else
  rc anchor skipped_no_XM_OPS_REVIEW
fi
for f in diff.patch background.md ocr-preview.json codex-review.md codex-spec.findings.json opencode.findings.json ocr-review.json; do
  [ -s "$R/$f" ] && echo "sha256 $f=$(shasum -a 256 "$R/$f" | cut -d" " -f1)" >> "$R/range.txt"
done
echo "finished=$(date -u +%FT%TZ)" >> "$R/range.txt"
cat "$R/exit-codes.txt"
echo "review dir: $R"
