// xMustard's OpenCode plugin. One default export serves both plugin APIs, as
// OpenCode's own loaders read it: 2.x takes `id` and `setup` (v2.ts), 1.x takes
// `server` (v1.ts). The 2.x API is imported for types only, so neither version needs
// a package at runtime.

import { serverV1 } from "./v1.ts";
import { setupV2 } from "./v2.ts";

export { loadConfig } from "./config.ts";
export { serverV1 } from "./v1.ts";
export { setupV2 } from "./v2.ts";

export default {
	id: "xmustard",
	setup: setupV2,
	server: serverV1,
};
