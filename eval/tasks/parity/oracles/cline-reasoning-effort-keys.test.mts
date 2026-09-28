// Hidden oracle for cline-reasoning-effort-keys (xmustard-eval parity corpus): only the
// effort names the ratio table defines are accepted, case-insensitively and trimmed.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
	REASONING_EFFORT_RATIOS,
	resolveEffectiveReasoningEffort,
	resolveReasoningBudgetFromRatio,
	resolveReasoningEffortRatio,
} from "../sdk/packages/shared/src/llms/reasoning-effort.ts";

const inherited = ["constructor", "toString", "__proto__", "hasOwnProperty", "valueOf", "isPrototypeOf"];

describe("inherited object keys are not effort levels", () => {
	for (const key of inherited) {
		it(key, () => {
			assert.equal(resolveEffectiveReasoningEffort(key), undefined);
			assert.equal(resolveEffectiveReasoningEffort(key.toUpperCase()), undefined);
			assert.equal(resolveReasoningEffortRatio(key), undefined);
			assert.equal(resolveReasoningEffortRatio(key, { fallbackEffort: "low" }), 0.2);
			assert.equal(resolveReasoningBudgetFromRatio({ effort: key, maxBudget: 1000 }), undefined);
		});
	}
	it("thinking without a valid effort falls back to the default", () => {
		assert.equal(resolveEffectiveReasoningEffort("constructor", true), undefined);
	});
});

describe("valid efforts", () => {
	it("are case-insensitive and ignore surrounding whitespace", () => {
		assert.equal(resolveEffectiveReasoningEffort("HIGH"), "high");
		assert.equal(resolveEffectiveReasoningEffort("  medium "), "medium");
		assert.equal(resolveReasoningEffortRatio(" XHigh"), 0.95);
		assert.equal(resolveReasoningEffortRatio("none"), 0);
	});
	it("every table entry resolves to its ratio", () => {
		for (const [name, ratio] of Object.entries(REASONING_EFFORT_RATIOS)) {
			assert.equal(resolveEffectiveReasoningEffort(name), name);
			assert.equal(resolveReasoningEffortRatio(name), ratio);
		}
	});
	it("budgets are unchanged", () => {
		assert.equal(resolveReasoningBudgetFromRatio({ effort: "high", maxBudget: 1000 }), 800);
		assert.equal(resolveReasoningBudgetFromRatio({ effort: "none", maxBudget: 1000 }), 0);
		assert.equal(resolveReasoningBudgetFromRatio({ effort: "bogus", maxBudget: 1000 }), undefined);
		assert.equal(resolveReasoningBudgetFromRatio({ effort: "bogus", maxBudget: 1000, fallbackEffort: "medium" }), 500);
	});
});
