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
IDX=integrations/pi/src/index.ts
RUST=rust-core/src/run_wrapper.rs
PRISTINE="$(mktemp -d)"
cp "$SRC" "$PRISTINE/commandwrap.ts"
cp "$CFG" "$PRISTINE/config.ts"
cp "$DLV" "$PRISTINE/delivery.ts"
cp "$IDX" "$PRISTINE/index.ts"
cp "$RUST" "$PRISTINE/run_wrapper.rs"

restore() {
	cp "$PRISTINE/commandwrap.ts" "$SRC"
	cp "$PRISTINE/config.ts" "$CFG"
	cp "$PRISTINE/delivery.ts" "$DLV"
	cp "$PRISTINE/index.ts" "$IDX"
	cp "$PRISTINE/run_wrapper.rs" "$RUST"
	if cmp -s "$PRISTINE/commandwrap.ts" "$SRC" && cmp -s "$PRISTINE/config.ts" "$CFG" \
		&& cmp -s "$PRISTINE/delivery.ts" "$DLV" && cmp -s "$PRISTINE/index.ts" "$IDX" \
		&& cmp -s "$PRISTINE/run_wrapper.rs" "$RUST"; then
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
	run "$id" "$file"
}

run() {
	local id="$1" file="${2:-$SRC}"
	echo "=== MUTATION $id"
	local out rc
	if [[ $file == "$RUST" ]]; then
		# Every hint test, not just the ones whose name starts command_hint: the
		# run-script family test is named for what it asserts, and a filter that
		# missed it left M32-M34 with no test to kill (measured: applied=33
		# survived=0 while that test failed to compile and never ran).
		out="$(cd rust-core && cargo test --quiet --lib run_wrapper::tests 2>&1)"
	else
		out="$(cd integrations/pi && node --test test/unit.test.ts 2>&1)"
	fi
	rc=$?
	echo "$out" | grep -E "^# (tests|pass|fail)|^    not ok|test result:|FAILED|panicked"
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

mutate M1 "${TAB}if (!enabled) return false;" "" || true

mutate M2 'command.trim() === "" || ALREADY_WRAPPED.test(command.trim())' 'ALREADY_WRAPPED.test(command.trim())' || true

# M3: the input-shape guard is removed from the rewrite, so a non-string command is
# offered to the wrap. (M15 drops the other guard's tool-name check; M1 drops the knob.)
mutate M3 'if (!isRecord(input) || typeof input.command !== "string") return false;' 'if (!isRecord(input)) return false;' || true

mutate M4 'if (command.trim() === "" || ALREADY_WRAPPED.test(command.trim())) return false;' 'if (ALREADY_WRAPPED.test(command.trim())) return false;' || true

mutate M5 "${TAB}if (!ws) return false;" "" || true

mutate M6 'const ws = workspaceId?.trim();' 'const ws = "w1";' || true

# M7: mutateBashInput writes a copy instead of the caller's object. Pi applies the
# in-place mutation, so a copy leaves the command the model wrote.
LINE="${TAB}input.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`;"
mutate M7 "$LINE" "${TAB}const copy = { ...input }; copy.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`;" || true

mutate M8 '!isRecord(input) || typeof input.command !== "string"' '!isRecord(input)' || true

# M9: the rewritten input drops Pi's own timeout, which would silently un-bound it
mutate M9 "$LINE" "${TAB}input.command = \`\${WRAPPER} --workspace \${shellQuote(ws)} -- \${SHELL} \${SHELL_FLAG} \${shellQuote(command)}\`; delete input.timeout;" || true

mutate M10 '["1", "on", "true", "yes"].includes((env.XMUSTARD_PI_WRAP_BASH ?? "").trim().toLowerCase())' '(env.XMUSTARD_PI_WRAP_BASH ?? "").trim() !== ""' "$CFG" || true

# M11: the shell is dropped, so `cd sub && make` would be exec'd as a program
mutate M11 " -- \${SHELL} \${SHELL_FLAG} " " -- " || true

# M12: quoting is dropped, so a command with a quote or a space in the id breaks
mutate M12 '${SHELL} ${SHELL_FLAG} ${shellQuote(command)}' '${SHELL} ${SHELL_FLAG} ${command}' || true

# M13: carriesHandle loses the literal `]` closing the tag, which is the exact defect
# this row found: the tag never matches and no wrapper footer is recognised.
mutate M13 'search)\]/g;' 'search)/g;' "$DLV" || true

# M14: the wrapper's footer tag is no longer recognised, so a captured result never
# activates expansion and the model holds a handle with no tool.
mutate M14 '(?:evidence|masked|page|search)' '(?:page|search)' "$DLV" || true

# M15: the tool-name guard is removed, so every tool's input is offered to the wrap
# and `read`/`edit`/`write` payloads are rewritten. This is shouldWrapBash's copy of the
# guard, distinct from the rewrite's own (M37) because the two are separate refusals
# reached on opposite sides of the workspace lookup.
mutate M15 'if (!isRecord(event) || event.toolName !== "bash") return false;
	// No executable wrapper means' 'if (!isRecord(event)) return false;
	// No executable wrapper means' || true

# M16: the token gate is removed, so the wrapper is handed an argv with no way to
# authenticate and every wrapped call fails inside the wrapper instead of running
# natively.
mutate M16 'return effectiveToken(opts.token) === effectiveToken(opts.wrapperToken);' 'return true;' || true

# M22: the executable check is dropped, so a session with no xmustard-core on PATH
# rewrites every bash call to a command that exits 127.
mutate M22 'if (!opts.wrapperAvailable) return false;' '' || true

# M23: a directory named xmustard-core counts as the wrapper.
mutate M23 'return statSync(bin).isFile();' 'return true;' || true

# M19: an explicit-but-different API base is accepted, so a session pointed at one
# server captures through the wrapper into another.
mutate M19 'if (wrapperEndpoint(opts.wrapperApiBase) !== opts.apiBase) return false;' '' || true

# M20: every program gets a second word, so `echo hunter2` can put hunter2 in the
# capture query.
mutate M20 'None => None,
    };
    match rest {' 'None => subcommand(&mut words),
    };
    match rest {' "$RUST" || true

# M24: any program's -c script is read as a shell command, so `python -c <script>`
# puts a word of the script in the capture query.
mutate M24 'let script = SHELLS
        .contains(&name)' 'let script = true' "$RUST" || true

# M25: make-like targets lose their family keyword, so `make test` is reduced as a build.
mutate M25 'Some(Hint::Keyword) => keyword(&mut words),' 'Some(Hint::Keyword) => None,' "$RUST" || true

# M21: a handle in the result text no longer activates expansion, so the model holds
# a recovery handle and no tool to use it with.
mutate M21 'return resultCarriesHandle(content);' 'return false;' "$SRC" || true

# M17: the interpreter falls back to sh, so bash-only syntax fails on a host whose
# /bin/sh is dash (measured on the build box: /bin/sh -> dash).
mutate M17 'const SHELL = "bash";' 'const SHELL = "sh";' "$SRC" || true

# M18: the run_wrapper's command hint keeps naming the shell, so a wrapped `git diff`
# is reduced by the generic shell reducer instead of the diff family.
mutate M18 'Some(c) => Box::new(c.split_whitespace()),' 'Some(_) => Box::new(argv.iter().map(String::as_str)),' "$RUST" || true

# M26: the no-re-capture guard is removed, so the wrapper's own projection is captured a
# second time and the visible handle points at the projection rather than the output.
mutate M26 '	if (wrapped && carriesHandle(texts)) return undefined;' '' "$DLV" || true

# M27: the handle scan stops at the first tag, so a handle after a second tag on a
# later line is missed.
mutate M27 '		HANDLE_TAG.lastIndex = nl + 1;' '		return false;' "$DLV" || true

# M28: the no-re-capture guard drops the provenance requirement, so any result whose
# text looks like the footer skips capture, redaction and the output budget (r6).
mutate M28 'if (wrapped && carriesHandle(texts)) return undefined;' 'if (carriesHandle(texts)) return undefined;' "$DLV" || true

# M29: the endpoint comparison is narrowed back to an explicit value, so a session on a
# custom base with nothing set for the wrapper is allowed to wrap (r6 P1). M19 removes
# the check; this one keeps it and makes it agree with anything explicit.
mutate M29 'const set = raw?.trim();' 'const set = raw?.trim(); if (set !== undefined) return set;' || true

# M30: the reachability check is dropped, so an https or remote adapter base is wrapped
# even though the wrapper refuses every non-loopback host (r6).
mutate M30 'if (!isLoopbackHttp(opts.apiBase)) return false;' '' || true

# M31: the wrapper's own default is ignored again, so an unset base compares as empty
# rather than as the endpoint the wrapper will actually use.
mutate M31 'return set ? normalizeApiBase(set) : normalizeApiBase(WRAPPER_DEFAULT_API);' 'return set ? normalizeApiBase(set) : "";' || true

# M32: the family mapping is bypassed and the script's own name is sent, so a script
# named after a secret puts that word in the capture URL (r6).
mutate M32 'Some(family) => format!("run {family}"),' 'Some(_family) => format!("run {}", subcommand(words).unwrap_or_default()),' "$RUST" || true

# M33: the family mapping returns the script itself for every family, which is the
# pre-r6 behaviour.
mutate M33 'if TEST_PREFIX.iter().any(|p| s.starts_with(p)) || TEST_EXACT.contains(&s.as_str()) {' 'if !s.is_empty() {' "$RUST" || true

# M34: the exact-match lists become prefix tests, so `tsc` is claimed as a test by the
# `t` prefix and `tscheck` by `tsc`, neither of which the registry does (r7).
mutate M34 'TEST_EXACT.contains(&s.as_str())' 'TEST_EXACT.iter().any(|p| s.starts_with(p))' "$RUST" || true

# M35: the path check is dropped, so a base the wrapper addresses at the host root is
# accepted for an adapter serving the same host under a path prefix (r7).
mutate M35 'if (url.pathname !== "/" && url.pathname !== "") return false;' '' || true

# M36: the rewrite's own answer is ignored and every call is recorded as wrapped, so a
# command left alone runs natively and its footer-shaped result is trusted anyway (r7).
# The row targets recordWrappedCall's guard, which is the decision the hook now makes.
mutate M36 'if (!mutateBashInput(event, workspaceId, enabled)) return false;' 'mutateBashInput(event, workspaceId, enabled);' || true

# M37: the rewrite's own tool-name guard is removed, so a `read`/`edit`/`write` payload
# that reached the hook is rewritten even though only `bash` commands can be wrapped.
# M15 is the same guard in shouldWrapBash; this one is in mutateBashInput itself.
mutate M37 'if (!isRecord(event) || event.toolName !== "bash") return false;
	const input' 'if (!isRecord(event)) return false;
	const input' || true

# M38: the script word is read through `plain` again, which refuses npm's own colon
# variant naming (`test:unit`, `lint:fix`, `build:prod`), so all three fall to the
# generic reducer and the capture lands in no family.
mutate M38 'Some(match script_name(words).as_deref().and_then(family_of) {' 'Some(match subcommand(words).as_deref().and_then(family_of) {' "$RUST" || true

restore ALL
echo "=== final byte-compare"
cmp "$PRISTINE/commandwrap.ts" "$SRC" && cmp "$PRISTINE/config.ts" "$CFG" \
	&& cmp "$PRISTINE/delivery.ts" "$DLV" && cmp "$PRISTINE/index.ts" "$IDX" \
	&& cmp "$PRISTINE/run_wrapper.rs" "$RUST" \
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