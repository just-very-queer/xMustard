// Hidden oracle for cline-schedule-preset-decision (xmustard-eval parity corpus): the
// agreed preset is in its agreed place and every other preset is unchanged.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { CRON_PRESETS } from "../apps/cli/src/wizards/schedule/cron-presets.ts";

const every = (label: string, cron: string) => ({ label, value: cron, hint: cron });

describe("schedule wizard presets", () => {
	it("match the agreed list, in order", () => {
		assert.deepEqual(CRON_PRESETS, [
			every("Every 5 minutes", "*/5 * * * *"),
			every("Every 15 minutes", "*/15 * * * *"),
			every("Every 30 minutes", "*/30 * * * *"),
			every("Every hour", "0 * * * *"),
			every("Every 6 hours", "0 */6 * * *"),
			every("Daily at midnight", "0 0 * * *"),
			every("Daily at 9am", "0 9 * * *"),
			every("Every weekday at 9am", "0 9 * * 1-5"),
			every("Every Monday at 9am", "0 9 * * 1"),
			every("First of every month", "0 0 1 * *"),
			{ label: "Custom", value: "__custom__", hint: "enter your own cron expression" },
		]);
	});
});
