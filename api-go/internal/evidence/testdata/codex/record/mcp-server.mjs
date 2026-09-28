// Minimal stdio MCP server with one tool whose result is a long CI log.
import readline from "node:readline";
const rl = readline.createInterface({ input: process.stdin });
const send = (m) => process.stdout.write(JSON.stringify(m) + "\n");
rl.on("line", (line) => {
  const m = JSON.parse(line);
  if (m.id === undefined) return;
  switch (m.method) {
    case "initialize":
      return send({ jsonrpc: "2.0", id: m.id, result: { protocolVersion: m.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "cilogs", version: "1.0.0" } } });
    case "tools/list":
      return send({ jsonrpc: "2.0", id: m.id, result: { tools: [{ name: "fetch_log", description: "fetch a CI job log", inputSchema: { type: "object", properties: { job: { type: "string" } }, required: ["job"] } }] } });
    case "tools/call":
      return send({ jsonrpc: "2.0", id: m.id, result: { content: [{ type: "text", text: "step 1 ok\nstep 2 FAILED: exit 1\n" }], isError: m.params.arguments.job === "err" } });
    default:
      return send({ jsonrpc: "2.0", id: m.id, error: { code: -32601, message: "method not found" } });
  }
});
