// Hidden oracle for pi-truncate-reason (xmustard-eval parity corpus). Every copy of the
// head/tail truncation helpers must name the limit that actually cut the output.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import * as harness from "../packages/agent/src/harness/utils/truncate.ts";
import * as codingAgent from "../packages/coding-agent/src/core/tools/truncate.ts";
import * as durable from "../packages/durable/src/env/utils/truncate.ts";

type Result = { content: string; truncated: boolean; truncatedBy: string | null; outputLines: number };
type Truncate = (content: string, options?: { maxLines?: number; maxBytes?: number }) => Result;

const copies: Record<string, { truncateHead: Truncate; truncateTail: Truncate }> = { harness, codingAgent, durable };

const cases: { name: string; fn: "truncateHead" | "truncateTail"; content: string; maxLines: number; maxBytes: number; by: string; out: string }[] = [
	{ name: "head: a trailing newline over the byte limit", fn: "truncateHead", content: "a\nb\nc\n", maxLines: 10, maxBytes: 5, by: "bytes", out: "a\nb\nc" },
	{ name: "tail: a trailing newline over the byte limit", fn: "truncateTail", content: "a\nb\nc\n", maxLines: 10, maxBytes: 5, by: "bytes", out: "a\nb\nc" },
	{ name: "head: at the line limit, only bytes exceeded", fn: "truncateHead", content: "a\nb\nc\n", maxLines: 3, maxBytes: 5, by: "bytes", out: "a\nb\nc" },
	{ name: "tail: at the line limit, only bytes exceeded", fn: "truncateTail", content: "a\nb\nc\n", maxLines: 3, maxBytes: 5, by: "bytes", out: "a\nb\nc" },
	{ name: "head: lines omitted", fn: "truncateHead", content: "a\nb\nc\nd", maxLines: 2, maxBytes: 100, by: "lines", out: "a\nb" },
	{ name: "tail: lines omitted", fn: "truncateTail", content: "a\nb\nc\nd", maxLines: 2, maxBytes: 100, by: "lines", out: "c\nd" },
	{ name: "head: lines omitted with a trailing newline", fn: "truncateHead", content: "a\nb\nc\nd\n", maxLines: 2, maxBytes: 100, by: "lines", out: "a\nb" },
	{ name: "head: a byte break inside the content", fn: "truncateHead", content: "aaaa\nbbbb\ncccc", maxLines: 10, maxBytes: 9, by: "bytes", out: "aaaa\nbbbb" },
	{ name: "tail: a byte break inside the content", fn: "truncateTail", content: "aaaa\nbbbb\ncccc", maxLines: 10, maxBytes: 9, by: "bytes", out: "bbbb\ncccc" },
];

for (const [copy, mod] of Object.entries(copies)) {
	describe(`${copy} truncation`, () => {
		for (const c of cases) {
			it(c.name, () => {
				const r = mod[c.fn](c.content, { maxLines: c.maxLines, maxBytes: c.maxBytes });
				assert.equal(r.truncated, true);
				assert.equal(r.truncatedBy, c.by);
				assert.equal(r.content, c.out);
			});
		}
		it("content within both limits is not truncated", () => {
			const r = mod.truncateHead("a\nb\n", { maxLines: 10, maxBytes: 100 });
			assert.equal(r.truncated, false);
			assert.equal(r.truncatedBy, null);
		});
	});
}
