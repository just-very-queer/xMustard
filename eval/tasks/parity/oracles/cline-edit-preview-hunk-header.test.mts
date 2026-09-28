// Hidden oracle for cline-edit-preview-hunk-header (xmustard-eval parity corpus): a hunk
// with an empty old or new range starts that range at the line before the change.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { makeUnifiedDiff } from "../apps/cli/src/tui/utils/diff.ts";

const headers = (oldText: string, newText: string, ctx?: number) =>
	makeUnifiedDiff(oldText, newText, "f.txt", ctx)
		.split("\n")
		.filter((l) => l.startsWith("@@"));
const lines = (n: number) => Array.from({ length: n }, (_, i) => `l${i + 1}`).join("\n");

describe("empty ranges", () => {
	it("a new file starts its old range at 0", () => {
		assert.equal(makeUnifiedDiff("", "a\nb", "f.txt"), "--- a/f.txt\n+++ b/f.txt\n@@ -0,0 +1,2 @@\n+a\n+b");
	});
	it("an insertion after line 8 starts its old range at 8", () => {
		assert.deepEqual(headers(lines(8), `${lines(8)}\nnew`, 0), ["@@ -8,0 +9,1 @@"]);
	});
	it("an insertion at the top starts its old range at 0", () => {
		assert.deepEqual(headers("a\nb", "x\na\nb", 0), ["@@ -0,0 +1,1 @@"]);
	});
	it("a deletion starts its new range at the line before", () => {
		assert.deepEqual(headers("a\nb\nc", "a\nc", 0), ["@@ -2,1 +1,0 @@"]);
		assert.deepEqual(headers("a\nb", "b", 0), ["@@ -1,1 +0,0 @@"]);
	});
});

describe("unchanged hunks", () => {
	it("hunks with context keep their ranges", () => {
		assert.deepEqual(headers("x\ny", "x\nz\ny"), ["@@ -1,2 +1,3 @@"]);
		assert.deepEqual(headers(lines(10), lines(10).replace("l5\n", "l5\nX\n")), ["@@ -3,6 +3,7 @@"]);
		assert.deepEqual(headers(lines(20), lines(20).replace("l3\n", "L3\n").replace("l17\n", "L17\n")), ["@@ -1,6 +1,6 @@", "@@ -14,7 +14,7 @@"]);
	});
	it("a replacement without context keeps both starts", () => {
		assert.deepEqual(headers("a\nb\nc", "a\nB\nc", 0), ["@@ -2,1 +2,1 @@"]);
	});
});
