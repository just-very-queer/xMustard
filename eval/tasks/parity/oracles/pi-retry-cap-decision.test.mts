// Hidden oracle for pi-retry-cap-decision (xmustard-eval parity corpus): the agreed
// default cap on agent retry delays is in force wherever the default is defined, and
// the separate cap on server-requested delays is untouched.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import { DEFAULT_MAX_AGENT_RETRY_DELAY_MS, retryDelayMs } from "../packages/ai/src/utils/retry.ts";

const AGREED_MS = 45_000;
const read = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), "utf8");
const digits = (s: string) => s.replace(/(\d)[,_' ](?=\d)/g, "$1");

describe("agent retry delay cap", () => {
	it("the SDK default is the agreed value", () => {
		assert.equal(DEFAULT_MAX_AGENT_RETRY_DELAY_MS, AGREED_MS);
	});
	it("computed delays are capped at the default", () => {
		assert.equal(retryDelayMs({ baseDelayMs: 1000 }, 20), AGREED_MS);
		assert.equal(retryDelayMs({ baseDelayMs: 1000 }, 3), 4000);
	});
	it("an explicit cap still wins", () => {
		assert.equal(retryDelayMs({ baseDelayMs: 1000, maxAgentDelayMs: 90_000 }, 20), 90_000);
		assert.equal(retryDelayMs({ baseDelayMs: 1000, maxAgentDelayMs: 0 }, 5), 0);
	});
	it("the pico3 generation defaults no longer hard-code the old cap", () => {
		const src = digits(read("packages/agent/src/harness/pico3/kinds/generation.ts"));
		assert.doesNotMatch(src, /\b60000\b/);
	});
	it("the coding-agent settings reference documents the new default", () => {
		const rows = read("packages/coding-agent/docs/settings.md").split("\n");
		const agent = rows.find((r) => r.includes("`retry.maxAgentDelayMs`"));
		const provider = rows.find((r) => r.includes("`retry.provider.maxRetryDelayMs`"));
		assert.ok(agent && provider, "both retry delay rows must stay in the settings table");
		assert.match(digits(agent), /\b45000\b/);
		assert.doesNotMatch(digits(agent), /\b60000\b/);
		// server-requested delays are a different setting with their own default
		assert.match(digits(provider), /\b60000\b/);
	});
});
