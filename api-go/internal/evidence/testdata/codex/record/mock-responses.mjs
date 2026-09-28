// A local stand-in for the Responses API, so `codex exec` runs one tool call with no
// model: a turn's first request gets a function call (exec_command, or the cilogs MCP
// tool when the prompt says CALL_MCP), the follow-up gets a final message.
// Usage: XM_SHELL='<script>' node mock-responses.mjs <port>
import http from "node:http";
const port = Number(process.argv[2] || 18765);
const sse = (events) => events.map((e) => `event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`).join("");
let n = 0;
http
	.createServer((req, res) => {
		let body = "";
		req.on("data", (c) => (body += c));
		req.on("end", () => {
			if (req.method === "GET" && req.url.startsWith("/v1/models")) {
				res.writeHead(200, { "content-type": "application/json" });
				return res.end(JSON.stringify({ models: [] }));
			}
			if (req.method !== "POST" || !req.url.startsWith("/v1/responses")) {
				res.writeHead(404);
				return res.end();
			}
			const input = JSON.parse(body).input || [];
			const id = `resp-${++n}`;
			const text = JSON.stringify(input);
			const item = input.some((i) => i.type === "function_call_output")
				? { type: "message", role: "assistant", id: `msg-${n}`, content: [{ type: "output_text", text: "done" }] }
				: text.includes("CALL_MCP")
					? { type: "function_call", call_id: "call-mcp-1", namespace: "mcp__cilogs", name: "fetch_log", arguments: JSON.stringify({ job: "42" }) }
					: { type: "function_call", call_id: "call-exec-1", name: "exec_command", arguments: JSON.stringify({ cmd: process.env.XM_SHELL }) };
			const usage = { input_tokens: 100, input_tokens_details: { cached_tokens: 40, cache_write_tokens: 50 }, output_tokens: 10,
				output_tokens_details: { reasoning_tokens: 2 }, total_tokens: 110 };
			res.writeHead(200, { "content-type": "text/event-stream" });
			res.end(sse([{ type: "response.created", response: { id } }, { type: "response.output_item.done", item },
				{ type: "response.completed", response: { id, usage } }]));
		});
	})
	.listen(port, "127.0.0.1");
