#!/usr/bin/env bash
# WS-44e mutation pass: one mutation per new test in
# integrations/pi/test/unit.test.ts "bash command wrapping", each reverted
# immediately after its run. Run on the build box; the caller reads the log.
#
# The edits are applied with python rather than sed because the target lines
# contain literal tabs and backtick template literals, which BSD sed (macOS) and
# GNU sed disagree about. The box rsyncs the tree without .git, so a pristine copy
# is the restore mechanism and the byte-compare below is the cleanliness proof.
set -uo pipefail
ROOT="${XM_MUT_TREE:-$PWD}"
cd "$ROOT" || exit 1
echo "TREE=$ROOT"

SRC=integrations/pi/src/commandwrap.ts
CFG=integrations/pi/src/config.ts
DLV=integrations/pi/src/delivery.ts
PRISTINE="$(mktemp -d)"
cp "$SRC" "$PRISTINE/commandwrap.ts"
cp "$CFG" "$PRISTINE/config.ts"
cp "$DLV" "$PRISTINE/delivery.ts"

restore() {
	cp "$PRISTINE/commandwrap.ts" "$SRC"
	cp "$PRISTINE/config.ts" "$CFG"
	cp "$PRISTINE/delivery.ts" "$DLV"
	if cmp -s "$PRISTINE/commandwrap.ts" "$SRC" && cmp -s "$PRISTINE/config.ts" "$CFG" \
		&& cmp -s "$PRISTINE/delivery.ts" "$DLV"; then
		echo "RESTORED=$1 clean=yes"
	else
		echo "RESTORED=$1 clean=no"
	fi
}

# mutate <id> <old-substring> <new-substring> [file]; refuses when the old text is
# absent or ambiguous, so a mutation that silently failed to apply can never be
# read as a kill.
mutate() {
	local id="$1" old="$2" new="$3" file="${4:-$SRC}"
	python3 - "$id" "$file" "$old" "$new" <<'PY'
import sys, pathlib
id_, path, old, new = sys.argv[1:5]
p = pathlib.Path(path)
s = p.read_text()
if s.count(old) != 1:
    print(f"REFUSED {id_}: expected exactly one occurrence of {old!r}, found {s.count(old)}")
    sys.exit(3)
p.write_text(s.replace(old, new))
print(f"APPLIED {id_}")
PY
	local rc=$?
	if [[ $rc -ne 0 ]]; then echo "MUTATION $id DID NOT APPLY"; NOT_APPLIED="$((NOT_APPLIED + 1))"; return 1; fi
	APPLIED="$((APPLIED + 1))"
	run "$id"
}

run() {
	local id="$1"
	echo "=== MUTATION $id"
	local out rc
	out="$(cd integrations/pi && node --test test/unit.test.ts 2>&1)"
	rc=$?
	echo "$out" | grep -E "^# (tests|pass|fail)|^    not ok"
	echo "EXIT=$rc"
	restore "$id"
	# A mutation that did not kill a test is a failed row, not a note: the plan's
	# Tests-required list and §6 A4 both require one kill per mutation, and a runner
	# that cannot fail is a runner that reports a survivor as a kill.
	if [[ $rc -eq 0 ]]; then
		echo "SURVIVED $id (the test suite passed with the mutation applied)"
		SURVIVORS="$((SURVIVORS + 1))"
	fi
}

SURVIVORS=0
APPLIED=0
NOT_APPLIED=0
TAB=$'\t'

mutate M1 "${TAB}if (!enabled) return;" "" || true

mutate M2 'command.trim() === "" || ALREADY_WRAPPED.test(command.trim())' 'ALREADY_WRAPPED.test(command.trim())' || true

mutate M3 'if (!isRecord(event) || event.toolName !== "bash") return;' 'if (!isRecord(event)) return;'

mutate M4 'if (command.trim() === "" || ALREADY_WRAPPED.test(command.trim())) return;' 'if (ALREADY_WRAPPED.test(command.trim())) return;' || true

mutate M5 "${TAB}if (!ws) return;" "" || true

mutate M6 'const ws = workspaceId?.trim();' 'const ws = "";' || true

# M7: mutateBashInput writes a copy instead of the caller's object. Pi applies the
# in-place mutation, so a copy leaves the command the model wrote.
LINE="${TAB}input.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`;"
mutate M7 "$LINE" "${TAB}{ const copy = { ...input }; copy.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`; }" || true

mutate M8 '!isRecord(input) || typeof input.command !== "string"' '!isRecord(input)' || true

# M9: the rewritten input drops Pi's own timeout, which would silently un-bound it
mutate M9 "$LINE" "${TAB}input.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`; delete input.timeout;" || true

mutate M10 '["1", "on", "true", "yes"].includes((env.XMUSTARD_PI_WRAP_BASH ?? "").trim().toLowerCase())' '(env.XMUSTARD_PI_WRAP_BASH ?? "").trim() !== ""' "$CFG" || true

# M11: the shell is dropped, so `cd sub && make` would be exec'd as a program
mutate M11 " -- \${SHELL} \${SHELL_FLAG} " " -- " || true

# M12: quoting is dropped, so a command with a quote or a space in the id breaks
mutate M12 '${SHELL} ${SHELL_FLAG} ${shellQuote(command)}' '${SHELL} ${SHELL_FLAG} ${command}' || true

# M13: carriesHandle loses the literal `]`, which is the exact defect this row found:
# the class then has to span the bracket closing the tag and can never match.
mutate M13 'search)\][^\n]*xm1\.' 'search)[^\]]*xm1\.' "$DLV" || true

# M14: the wrapper's footer tag is no longer recognised, so a captured result never
# activates expansion and the model holds a handle with no tool.
mutate M14 '(?:evidence|masked|page|search)' '(?:page|search)' "$DLV" || true

# M15: the tool-name guard is removed, so every tool's input is offered to the wrap
# and `read`/`edit`/`write` payloads are rewritten.
mutate M15 'if (!isRecord(event) || event.toolName !== "bash") return false;' '' "$SRC" || true

# M16: the token gate is removed, so the wrapper is handed an argv with no way to
# authenticate and every wrapped call fails inside the wrapper instead of running
# natively.
mutate M16 'return !(opts.tokenConfigured && !opts.wrapperTokenPresent);' 'return true;' "$SRC" || true

restore ALL
echo "=== final byte-compare"
cmp "$PRISTINE/commandwrap.ts" "$SRC" && cmp "$PRISTINE/config.ts" "$CFG" \
	&& cmp "$PRISTINE/delivery.ts" "$DLV" \
	&& echo "TREE_CLEAN=yes" || echo "TREE_CLEAN=no"
rm -rf "$PRISTINE"
echo "=== SUMMARY applied=$APPLIED survived=$SURVIVORS not_applied=$NOT_APPLIED"
# The runner fails when any mutation was skipped or survived, so "all killed" cannot
# be a claim the script is structurally unable to contradict.
if [[ $SURVIVORS -ne 0 || $NOT_APPLIED -ne 0 ]]; then
	echo "MUTATION_PASS=failed"
	exit 1
fi
echo "MUTATION_PASS=ok"