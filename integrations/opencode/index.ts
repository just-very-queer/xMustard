// The plugin package's entry. OpenCode 2.0.1 resolves a configured plugin directory to
// its `server` or `index` module (Bun.resolveSync against the directory); a package
// import takes `main`. Both reach src/index.ts.
export { default } from "./src/index.ts";
