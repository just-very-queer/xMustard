// Hidden oracle for cline-hub-ipv6-url (xmustard-eval parity corpus): IPv6 bind hosts
// get a bracketed default public URL; everything else keeps its current behaviour.
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { buildInviteUrl, isNonLocalBindHost, resolveClineHubServerOptions } from "../apps/cline-hub/src/options.ts";

const publicUrl = (env: Record<string, string>) => resolveClineHubServerOptions(env).publicUrl;

describe("IPv6 bind hosts", () => {
	it("the local IPv6 host starts without PUBLIC_URL", () => {
		assert.equal(publicUrl({ HOST: "::1" }), "http://[::1]:8787");
	});
	it("a LAN IPv6 host with a room secret gets a bracketed URL", () => {
		assert.equal(publicUrl({ HOST: "fd00::1", ROOM_SECRET: "s" }), "http://[fd00::1]:8787");
	});
	it("the invite URL keeps the brackets", () => {
		assert.equal(buildInviteUrl(publicUrl({ HOST: "::1" }), "invite"), "http://[::1]:8787/?roomSecret=invite");
	});
});

describe("unchanged behaviour", () => {
	it("defaults, hostnames and IPv4 hosts", () => {
		assert.equal(publicUrl({}), "http://127.0.0.1:8787");
		assert.equal(publicUrl({ HOST: "localhost" }), "http://localhost:8787");
		assert.equal(publicUrl({ HOST: "192.168.1.5", ROOM_SECRET: "s" }), "http://192.168.1.5:8787");
	});
	it("wildcard hosts keep the current fallback host", () => {
		assert.equal(publicUrl({ HOST: "0.0.0.0", ROOM_SECRET: "s" }), "http://localhost:8787");
		assert.equal(publicUrl({ HOST: "::", ROOM_SECRET: "s" }), "http://localhost:8787");
	});
	it("an explicit PUBLIC_URL wins and is still validated", () => {
		assert.equal(publicUrl({ HOST: "::1", PUBLIC_URL: "https://hub.example/" }), "https://hub.example");
		assert.throws(() => publicUrl({ HOST: "::1", PUBLIC_URL: "ftp://hub.example" }));
	});
	it("only loopback hosts may run without a room secret", () => {
		assert.equal(isNonLocalBindHost("::1"), false);
		assert.equal(isNonLocalBindHost("::"), true);
		assert.equal(isNonLocalBindHost("0.0.0.0"), true);
		assert.throws(() => publicUrl({ HOST: "fd00::1" }), /ROOM_SECRET/);
		assert.throws(() => publicUrl({ HOST: "0.0.0.0" }), /ROOM_SECRET/);
	});
});
