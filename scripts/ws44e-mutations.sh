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
PRISTINE="$(mktemp -d)"
cp "$SRC" "$PRISTINE/commandwrap.ts"
cp "$CFG" "$PRISTINE/config.ts"

restore() {
	cp "$PRISTINE/commandwrap.ts" "$SRC"
	cp "$PRISTINE/config.ts" "$CFG"
	if cmp -s "$PRISTINE/commandwrap.ts" "$SRC" && cmp -s "$PRISTINE/config.ts" "$CFG"; then
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
	if [[ $rc -ne 0 ]]; then echo "MUTATION $id DID NOT APPLY"; return 1; fi
	run "$id"
}

run() {
	local id="$1"
	echo "=== MUTATION $id"
	(cd integrations/pi && node --test test/unit.test.ts 2>&1 | grep -E "^# (tests|pass|fail)|^    not ok"; echo "EXIT=${PIPESTATUS[0]}")
	restore "$id"
}

TAB=$'\t'

mutate M1 "${TAB}if (!enabled) return;" "" || true

mutate M2 'command.trim() === "" || ALREADY_WRAPPED.test(command.trim())' 'ALREADY_WRAPPED.test(command.trim())' || true

mutate M3 'event.toolName !== "bash"' 'false' || true

mutate M4 'if (command.trim() === "" || ALREADY_WRAPPED.test(command.trim())) return;' 'if (ALREADY_WRAPPED.test(command.trim())) return;' || true

mutate M5 "${TAB}if (!ws) return;" "" || true

mutate M6 'const ws = workspaceId?.trim();' 'const ws = "";' || true

# M7: mutateBashInput writes a copy instead of the caller's object. Pi applies the
# in-place mutation, so a copy leaves the command the model wrote.
LINE="${TAB}input.command = \`\${WRAPPER} --workspace \${ws} -- \${command}\`;"
mutate M7 "$LINE" "${TAB}{ const copy = { ...input }; copy.command = \`\${WRAPPER} --workspace \${ws} -- \${command}\`; }" || true

mutate M8 '!isRecord(input) || typeof input.command !== "string"' '!isRecord(input)' || true

# M9: the rewritten input drops Pi's own timeout, which would silently un-bound it
mutate M9 "$LINE" "${TAB}input.command = \`\${WRAPPER} --workspace \${ws} -- \${command}\`; delete input.timeout;" || true

mutate M10 '["1", "on", "true", "yes"].includes((env.XMUSTARD_PI_WRAP_BASH ?? "").trim().toLowerCase())' '(env.XMUSTARD_PI_WRAP_BASH ?? "").trim() !== ""' "$CFG" || true

restore ALL
echo "=== final byte-compare"
cmp "$PRISTINE/commandwrap.ts" "$SRC" && cmp "$PRISTINE/config.ts" "$CFG" && echo "TREE_CLEAN=yes" || echo "TREE_CLEAN=no"
rm -rf "$PRISTINE"