// Hidden oracle for pi-protocol-limits-doc (xmustard-eval parity corpus): the README's
// Limits section states the defaults the code uses now, one limit per line.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import { DEFAULT_MAX_CBOR_BYTE_LENGTH, DEFAULT_MAX_CBOR_CONTAINER_LENGTH, DEFAULT_MAX_CBOR_DEPTH } from "../packages/protocol/src/cbor/options.ts";
import { DEFAULT_MAX_FRAME_LENGTH } from "../packages/protocol/src/framing.ts";

const readme = readFileSync(new URL("../packages/protocol/README.md", import.meta.url), "utf8");
const numbers = (line: string) => (line.replace(/(\d)[,_' ](?=\d)/g, "$1").match(/\d+/g) ?? []).map(Number);

function limitsSection(): string[] {
	const lines = readme.split("\n");
	const start = lines.findIndex((l) => /^#{1,6}\s+limits\b/i.test(l.trim()));
	assert.ok(start >= 0, "README must have a Limits heading");
	const level = lines[start].trim().match(/^#+/)![0].length;
	const rest = lines.slice(start + 1);
	const end = rest.findIndex((l) => {
		const h = l.trim().match(/^(#+)\s/);
		return h !== null && h[1].length <= level;
	});
	return (end < 0 ? rest : rest.slice(0, end)).filter((l) => numbers(l).length > 0);
}

describe("protocol README limits", () => {
	it("the code keeps its current defaults", () => {
		assert.equal(DEFAULT_MAX_FRAME_LENGTH, 8 * 1024 * 1024);
		assert.equal(DEFAULT_MAX_CBOR_DEPTH, 32);
		assert.equal(DEFAULT_MAX_CBOR_BYTE_LENGTH, 16 * 1024 * 1024);
		assert.equal(DEFAULT_MAX_CBOR_CONTAINER_LENGTH, 1_000_000);
	});

	it("each limit is on its own line with the current value", () => {
		const section = limitsSection();
		const kinds: Record<string, (l: string) => boolean> = {
			frame: (l) => /frame/i.test(l),
			depth: (l) => /depth|nest/i.test(l),
			container: (l) => /container/i.test(l),
		};
		const expect: Record<string, { want: number; stale?: number }> = {
			frame: { want: 8 * 1024 * 1024, stale: 16 * 1024 * 1024 },
			depth: { want: 32, stale: 64 },
			container: { want: 1_000_000 },
		};
		for (const [kind, match] of Object.entries(kinds)) {
			const rows = section.filter(match);
			assert.ok(rows.length > 0, `no ${kind} line in the Limits section`);
			for (const row of rows) {
				const ns = numbers(row);
				assert.ok(ns.includes(expect[kind].want), `${kind}: ${row}`);
				if (expect[kind].stale !== undefined) assert.ok(!ns.includes(expect[kind].stale!), `${kind} states the old default: ${row}`);
			}
		}
		const byteLength = section.filter((l) => !Object.values(kinds).some((m) => m(l)) && numbers(l).includes(16 * 1024 * 1024));
		assert.ok(byteLength.length > 0, "no line gives the CBOR maximum byte length");
	});

	it("the old summary sentence is gone", () => {
		assert.doesNotMatch(readme, /\b64 nested\b/i);
		assert.doesNotMatch(readme, /16 MiB per CBOR payload\/frame/i);
	});
});
