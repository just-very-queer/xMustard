// Hidden oracle for pi-fuzzy-camel-humps (xmustard-eval parity corpus): a camelCase hump
// earns the word-boundary bonus; runs of capitals and matching stay as they were.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { fuzzyFilter, fuzzyMatch } from "../packages/tui/src/fuzzy.ts";

const score = (q: string, t: string) => {
	const m = fuzzyMatch(q, t);
	assert.equal(m.matches, true, `${q} should match ${t}`);
	return m.score;
};

describe("camelCase humps are word boundaries", () => {
	it("a hump after a lowercase letter scores better than the same letters without one", () => {
		assert.ok(score("gc", "getConfig") < score("gc", "getconfig"));
		assert.ok(score("rs", "readSettings") < score("rs", "readsettings"));
	});
	it("a hump after a digit counts", () => {
		assert.ok(score("vb", "v2Beta") < score("vb", "v2beta"));
	});
	it("a hump earns the same bonus as a separator", () => {
		const hump = score("c", "aC") - score("c", "ac");
		const separator = score("c", "a.c") - score("c", "a,c");
		assert.ok(hump < 0, "the hump must lower (improve) the score");
		assert.ok(Math.abs(hump - separator) < 1e-9, `hump bonus ${hump}, separator bonus ${separator}`);
	});
	it("ranks the camelCase candidate first", () => {
		assert.deepEqual(fuzzyFilter(["getconfig", "getConfig"], "gc", (s) => s), ["getConfig", "getconfig"]);
		assert.deepEqual(fuzzyFilter(["openrouterkey", "openRouterKey"], "ork", (s) => s)[0], "openRouterKey");
	});
});

describe("unchanged behaviour", () => {
	it("runs of capitals do not make every capital a boundary", () => {
		assert.equal(score("ab", "ABC"), score("ab", "abc"));
		assert.equal(score("s", "HTTPServer"), score("s", "httpserver"));
	});
	it("matching is case-insensitive", () => {
		assert.equal(score("GC", "getConfig"), score("gc", "getConfig"));
		assert.equal(fuzzyMatch("xyz", "getConfig").matches, false);
	});
	it("separators still count", () => {
		assert.ok(score("gc", "get-config") < score("gc", "getconfig"));
		assert.equal(score("", "anything"), 0);
	});
	it("an exact match keeps its bonus", () => {
		assert.ok(score("test", "test") < score("test", "testing"));
	});
});
