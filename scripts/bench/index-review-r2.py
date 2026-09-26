#!/usr/bin/env python3
"""index-review-r2.py BIN WORK [CLINE]: WS-07 review round 2 scenarios, one JSON line per
run on stdout (worker-peak.py records plus a `scenario` field). Fixtures are generated
under WORK; CLINE (optional) is a cline checkout, cloned into WORK for the placement runs.

Scenarios: ignore files beyond the bounds (1 MB files, nested; files at the 64 KiB cap
with 2,000 long names), 64,000-byte deeply nested or dense grammar files, files just
under the 24,000-token parse bound, 1 MB single-line files (lexical extraction), and
cline with one adversarial file placed last in path order. macOS only (worker-peak.py)."""
import json, os, re, shutil, subprocess, sys

binp, work = sys.argv[1], sys.argv[2]
cline = sys.argv[3] if len(sys.argv) > 3 else None
peak = os.path.join(os.path.dirname(os.path.abspath(__file__)), "worker-peak.py")
TOK = re.compile(r"[A-Za-z0-9_$]+|[^\sA-Za-z0-9_$]")


def git(d, *a):
    subprocess.run(["git", "-C", d, *a], check=True, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL)


def repo(d, files):
    shutil.rmtree(d, ignore_errors=True)
    for rel, src in files.items():
        p = os.path.join(d, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        open(p, "w").write(src)
    git(d, "init", "-q")
    git(d, "add", "-A")
    git(d, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "c")
    return d


def run(scenario, d, *extra):
    idx = os.path.join(work, "idx")
    shutil.rmtree(idx, ignore_errors=True)
    out = subprocess.run([sys.executable, peak, scenario, binp, "index", "build", d,
                          "--index-dir", idx, "--no-cache", *extra],
                         capture_output=True, text=True).stdout
    rec = json.loads(out)
    rec["scenario"] = scenario
    print(json.dumps(rec), flush=True)


def fill(prefix, unit, suffix, n=64000):
    return prefix + unit * ((n - len(prefix) - len(suffix)) // len(unit)) + suffix


def tokens(src):
    return len(TOK.findall(src))


def under(prefix, unit, suffix, limit=23990):
    lo, hi = 1, 1
    while tokens(prefix + unit * hi + suffix) <= limit:
        hi *= 2
    while lo + 1 < hi:
        mid = (lo + hi) // 2
        lo, hi = (mid, hi) if tokens(prefix + unit * mid + suffix) <= limit else (lo, mid)
    return prefix + unit * lo + suffix


os.makedirs(work, exist_ok=True)
# ---- ignore files beyond the bounds
big = ("*a" * 500 + "*b\n") * 1000
capped = "".join(f"{'*a' * 30}*b{i:03d}*\n" for i in range(970))
files = {f"s{d}/.xmustardignore": big for d in range(10)}
files["x.ts"] = "export const x = 1;\n"
run("ignore-1mb-siblings", repo(os.path.join(work, "ign-a"), files))
chain = "/".join(f"n{d}" for d in range(10))
files = {"/".join(f"n{k}" for k in range(d + 1)) + "/.xmustardignore": big for d in range(10)}
files.update({f"{chain}/{'a' * 200}{i:03d}.ts": f"export const v{i} = {i};\n" for i in range(20)})
run("ignore-1mb-nested-20-long-names", repo(os.path.join(work, "ign-b"), files))
files = {"/".join(f"n{k}" for k in range(d + 1)) + "/.xmustardignore": capped for d in range(10)}
files.update({f"{chain}/{'a' * 200}{i:04d}.ts": f"export const v{i} = {i};\n" for i in range(2000)})
run("ignore-64k-nested-2000-long-names", repo(os.path.join(work, "ign-c"), files))

# ---- 64,000-byte grammar files
ternary = "const t = " + "".join(f"a?{i}:" for i in range(12000))[:63970] + "0;\n"
deep = {
    "neg.rs": fill("fn f() { let x = ", "-", "1; }\n"),
    "refs.rs": fill("fn f() { let x = ", "&", "1; }\n"),
    "mixed.rs": fill("fn f() { let x = ", "-!", "1; }\n"),
    "not.ts": fill("const x = ", "!", "x;\n"),
    "deref.go": fill("package p\nfunc f() { _ = ", "*", "p }\n"),
    "recv.go": fill("package p\nfunc f() { _ = ", "<-", "ch }\n"),
    "ternary.js": ternary,
    "generic.ts": "type T = " + "A<" * 21000 + "B" + ">" * 21000 + ";\n",
    "ltexpr.ts": fill("const x = ", "a<", "a;\n"),
    "member.rs": fill("fn f() { let y = y", ".a", "; }\n"),
    "flat.ts": fill("", "a;", "\n"),
    "binary.ts": fill("const s = a", "+a", ";\n"),
    "assign.js": fill("a", "=a", ";\n"),
    "arrow.ts": fill("const f = ", "a=>", "1;\n"),
    "exp.js": fill("const x = a", "**a", ";\n"),
    "casts.ts": fill("const x = ", "<A>", "x;\n"),
    "calls.ts": fill("const c = x", ".f()", ";\n"),
    "closures.rs": fill("fn f() { let c = ", "|a| ", "1; }\n"),
    "cond.ts": fill("type T = ", "A extends B ? C : ", "D;\n"),
    "jsx.jsx": fill("const x = ", "<a>", "</a>;\n"),
    "nested-fns.ts": "function a(){" * 4600 + "}" * 4600 + "\n",
}
for name, src in deep.items():
    run(f"64kb-{name}", repo(os.path.join(work, "deep"), {name: src}))
# ---- just under the token bound (shapes the nesting guards admit)
cap = {
    "assign.js": ("a", "=a", ";\n"), "mutref.rs": ("fn f() { let x = ", "&mut ", "1; }\n"),
    "ternary.js": ("const t = ", "a?1:", "0;\n"), "exp.js": ("const x = a", "**a", ";\n"),
    "casts.ts": ("const x = ", "<A>", "x;\n"), "union.ts": ("type T = ", "A | ", "B;\n"),
    "arrow.ts": ("const f = ", "a=>", "1;\n"), "flat.ts": ("", "a;", "\n"),
    "closures.rs": ("fn f() { let c = ", "|a| ", "1; }\n"),
}
for name, (p, u, s) in cap.items():
    run(f"24k-tokens-{name}", repo(os.path.join(work, "cap"), {name: under(p, u, s)}))
# ---- 1 MB single-line files (lexical)
long = {"lifetimes.rs": "'a" * 500000, "cmp.tsx": "a <b && <c " * 90000,
        "jsx.jsx": "<a>{" * 250000, "lines.tsx": "a <b && <c\n" * 90000}
for name, src in long.items():
    run(f"1mb-{name}", repo(os.path.join(work, "long"), {name: src}))
# ---- cline with one adversarial file last in path order
if cline:
    c = os.path.join(work, "cline")
    if not os.path.isdir(os.path.join(c, ".git")):
        subprocess.run(["git", "clone", "-q", cline, c], check=True)
    run("cline", c)
    for name, src in [("ltexpr-64kb.ts", deep["ltexpr.ts"]), ("neg-64kb.rs", deep["neg.rs"]),
                      ("mutref-24k.rs", under(*cap["mutref.rs"])),
                      ("assign-24k.js", under(*cap["assign.js"]))]:
        os.makedirs(os.path.join(c, "zzz"), exist_ok=True)
        open(os.path.join(c, "zzz", name), "w").write(src)
        git(c, "add", "-A")
        git(c, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "adv")
        run(f"cline+{name}", c)
        git(c, "rm", "-q", "-r", "zzz")
        git(c, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "rm")
