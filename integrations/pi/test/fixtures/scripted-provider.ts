// Test-only Pi extension: an offline, scripted model provider built on pi-ai's faux
// core. It never opens a network connection and needs no credential. Each model
// request is answered from the JSON scenario at $XM_PI_SCENARIO and recorded (active
// tools plus the tool results that arrived since the previous assistant turn) as one
// JSONL line in $XM_PI_TRACE, so tests can prove what the model actually received.
//
// Scenario: { "steps": Step[] }, consumed in order, where Step is one of
//   { "calls": [{ "name": string, "args": object }], "delay_ms"?: number }
//        — one assistant turn of tool calls (optionally answered after a delay)
//   { "expand_all": { "workspace_id": string }, "max_pages"?: number }
//        — xmustard_expand from offset 0 following next_offset until eof, one call per turn
//   { "text": string }                                   — final answer, ends the run
// String args may use $WS ($XM_PI_WS), $HANDLE (latest [xmustard evidence] handle),
// $ENV:NAME (an environment variable) and $JSON:tool:field (a field of the latest
// JSON result of that tool, e.g. the id `remember` returned).

import { appendFileSync, readFileSync } from "node:fs";
import { createFauxCore, fauxAssistantMessage, fauxText, fauxToolCall, getCurrentTools } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Json = null | boolean | number | string | Json[] | { [k: string]: Json };
interface Call {
	name: string;
	args: Record<string, Json>;
}
type Step = { calls: Call[]; delay_ms?: number } | { expand_all: { workspace_id: string }; max_pages?: number } | { text: string };

interface ToolResultLike {
	role: string;
	toolCallId?: string;
	toolName?: string;
	isError?: boolean;
	content?: { type: string; text?: string }[];
}

const textOf = (m: ToolResultLike): string => (m.content ?? []).map((c) => (c.type === "text" ? (c.text ?? "") : `[${c.type}]`)).join("");

function latestJsonLine(messages: ToolResultLike[], prefix: string): Record<string, Json> | undefined {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m.role !== "toolResult") continue;
		for (const line of textOf(m).split("\n").reverse()) {
			if (line.startsWith(prefix)) {
				try {
					return JSON.parse(line.slice(prefix.length)) as Record<string, Json>;
				} catch {
					return undefined;
				}
			}
		}
	}
	return undefined;
}

function latestJsonField(messages: ToolResultLike[], tool: string, field: string): Json {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m.role !== "toolResult" || m.toolName !== tool) continue;
		try {
			return (JSON.parse(textOf(m).split("\n[xmustard evidence] ")[0]) as Record<string, Json>)[field] ?? "";
		} catch {
			return "";
		}
	}
	return "";
}

export default function scriptedProvider(pi: ExtensionAPI): void {
	const scenario = JSON.parse(readFileSync(process.env.XM_PI_SCENARIO ?? "", "utf8")) as { steps: Step[] };
	const tracePath = process.env.XM_PI_TRACE;
	const ws = process.env.XM_PI_WS ?? "";
	const core = createFauxCore({
		api: "xmustard-scripted",
		provider: "xmscripted",
		models: [{ id: "scripted", input: ["text"], contextWindow: 1_000_000, maxTokens: 16_384 }],
		tokenSize: { min: 64, max: 64 },
	});
	let cursor = 0;
	let pagesInStep = 0;
	let request = 0;

	const substitute = (v: Json, messages: ToolResultLike[]): Json => {
		if (typeof v === "string") {
			if (v === "$WS") return ws;
			if (v === "$HANDLE") return (latestJsonLine(messages, "[xmustard evidence] ")?.handle as string) ?? "";
			if (v.startsWith("$ENV:")) return process.env[v.slice(5)] ?? "";
			if (v.startsWith("$JSON:")) {
				const [, tool, field] = v.split(":");
				return latestJsonField(messages, tool, field);
			}
			return v;
		}
		if (Array.isArray(v)) return v.map((x) => substitute(x, messages));
		if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, substitute(x, messages)]));
		return v;
	};

	const next = async (messages: ToolResultLike[]) => {
		for (;;) {
			const step = scenario.steps[cursor];
			if (!step) return fauxAssistantMessage(fauxText("scenario exhausted"));
			if ("text" in step) {
				cursor++;
				return fauxAssistantMessage(fauxText(step.text));
			}
			if ("calls" in step) {
				cursor++;
				if (step.delay_ms) await new Promise((r) => setTimeout(r, step.delay_ms));
				return fauxAssistantMessage(
					step.calls.map((c) => fauxToolCall(c.name, substitute(c.args, messages) as Record<string, Json>)),
					{ stopReason: "toolUse" },
				);
			}
			const page = pagesInStep > 0 ? latestJsonLine(messages, "[xmustard page] ") : undefined;
			const last = messages[messages.length - 1];
			const failed = pagesInStep > 0 && last?.role === "toolResult" && last.isError;
			if (failed || page?.eof === true || pagesInStep >= (step.max_pages ?? 1000)) {
				cursor++;
				pagesInStep = 0;
				continue;
			}
			pagesInStep++;
			const offset = page ? (page.next_offset as number) : 0;
			const args = substitute({ workspace_id: step.expand_all.workspace_id, handle: "$HANDLE", offset }, messages);
			return fauxAssistantMessage([fauxToolCall("xmustard_expand", args as Record<string, Json>)], { stopReason: "toolUse" });
		}
	};

	pi.registerProvider("xmscripted", {
		name: "xMustard scripted (offline test)",
		baseUrl: "http://127.0.0.1:9",
		apiKey: "offline-fixture-not-a-credential",
		api: "xmustard-scripted",
		models: [
			{
				id: "scripted",
				name: "scripted",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 1_000_000,
				maxTokens: 16_384,
			},
		],
		streamSimple: (model, context, options) => {
			const messages = context.messages as unknown as ToolResultLike[];
			// results since the previous assistant turn; Pi may append a system message
			// (e.g. an active-tool change) after them
			const fresh: ToolResultLike[] = [];
			for (let i = messages.length - 1; i >= 0 && messages[i].role !== "assistant"; i--) {
				if (messages[i].role === "toolResult") fresh.unshift(messages[i]);
			}
			request++;
			if (tracePath) {
				const tools = getCurrentTools(context.messages);
				const line = {
					request,
					active_tools: tools.map((t) => t.name),
					// full definitions once, so conformance checks see what the model sees
					...(request === 1 ? { tools: tools.map((t) => ({ name: t.name, description: t.description, parameters: t.parameters })) } : {}),
					tool_results: fresh.map((m) => ({ toolCallId: m.toolCallId, toolName: m.toolName, isError: m.isError, text: textOf(m) })),
				};
				appendFileSync(tracePath, `${JSON.stringify(line)}\n`);
			}
			core.setResponses([() => next(messages)]);
			return core.streamSimple(model, context, options);
		},
	});
}
