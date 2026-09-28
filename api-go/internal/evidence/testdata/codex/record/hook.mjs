// PostToolUse command hook: appends the stdin payload Codex sends to the given file.
import fs from "node:fs";
let s = "";
process.stdin.on("data", (c) => (s += c));
process.stdin.on("end", () => fs.appendFileSync(process.argv[2], s.trim() + "\n"));
