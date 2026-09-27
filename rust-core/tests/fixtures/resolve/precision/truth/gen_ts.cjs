// gen_ts writes the TypeScript-checker ground truth for the TS precision fixture: every
// call or `new` (caller symbol, callee declaration) whose callee is a function, method
// or class declared at top level or in a class/interface of the fixture. The checker
// is the one tsserver uses.
//
//   NODE_PATH="$(npm root -g)" node gen_ts.cjs ../ts > ts_calls.json
const ts = require('typescript');
const fs = require('fs');
const path = require('path');

const root = path.resolve(process.argv[2]);
const files = [];
(function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name);
    if (e.isDirectory()) walk(p);
    else if (/\.tsx?$/.test(e.name)) files.push(p);
  }
})(root);
const program = ts.createProgram(files, { target: ts.ScriptTarget.ES2020, strict: true, noEmit: true });
const checker = program.getTypeChecker();
const rel = (f) => path.relative(root, f).split(path.sep).join('/');

// "Class.member" / "name" for declarations the index names; null for locals.
function declName(d) {
  const own = d.name && d.name.getText ? d.name.getText() : null;
  const parent = d.parent;
  if (ts.isClassDeclaration(d) || ts.isFunctionDeclaration(d) || ts.isInterfaceDeclaration(d)) {
    return ts.isSourceFile(parent) ? own : null;
  }
  if (ts.isMethodDeclaration(d) || ts.isMethodSignature(d) || ts.isConstructorDeclaration(d)) {
    const owner = parent.name ? parent.name.getText() : null;
    if (!owner || !ts.isSourceFile(parent.parent)) return null;
    return `${owner}.${ts.isConstructorDeclaration(d) ? 'constructor' : own}`;
  }
  return null;
}

function callerOf(node) {
  for (let n = node.parent; n; n = n.parent) {
    if (ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n) || ts.isConstructorDeclaration(n)) {
      const name = declName(n);
      if (name) return `${rel(n.getSourceFile().fileName)}::${name}`;
    }
  }
  return `${rel(node.getSourceFile().fileName)}::<file>`;
}

const pairs = new Set();
for (const sf of program.getSourceFiles()) {
  if (!sf.fileName.startsWith(root)) continue;
  (function visit(n) {
    if (ts.isCallExpression(n) || ts.isNewExpression(n)) {
      let e = n.expression;
      const target = ts.isPropertyAccessExpression(e) ? e.name : e;
      let sym = checker.getSymbolAtLocation(target);
      if (sym && sym.flags & ts.SymbolFlags.Alias) sym = checker.getAliasedSymbol(sym);
      const d = sym && sym.declarations && sym.declarations[0];
      if (d && d.getSourceFile().fileName.startsWith(root)) {
        const name = declName(d);
        if (name) pairs.add(JSON.stringify([callerOf(n), `${rel(d.getSourceFile().fileName)}::${name}`]));
      }
    }
    ts.forEachChild(n, visit);
  })(sf);
}
const out = [...pairs].sort().map((p) => JSON.parse(p));
process.stdout.write(JSON.stringify(out, null, 2) + '\n');
