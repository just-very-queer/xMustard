# Open Code Review (OCR) interop

OCR is an optional, external review client. It is not installed, launched, or
configured by xMustard. Its model credentials remain in OCR's own configuration;
only its reader-role xMustard bearer token is sent to the local API. Use the config
format supported by your pinned OCR build; the example below shows the `mcp_servers`
endpoint, `mode=readonly`, and header fields.

Configure OCR's `mcp_servers` entry to use the Streamable HTTP endpoint and a
reader token supplied by the environment (never commit a token):

```toml
[mcp_servers.xmustard]
url = "http://127.0.0.1:8042/mcp?mode=readonly"
headers = { Authorization = "Bearer ${XMUSTARD_READER_TOKEN}" }
```

Use OCR's read-only mode and allow only `search`, `explain`, `impact`, and `recall`.
Do not allow `remember` or `verify`, and do not run any OCR `setup` command against
xMustard. If OCR does not expand environment variables in `headers`, put the bearer
value in its user-local configuration with mode 0600; do not put it in this repository.

A review can be saved as machine-readable evidence with OCR's
`--format json --output <file>` options. Import that file into the current review with
`xmustard-ops review record <workspace_id> --base <base-ref> --findings <file> --producer ocr`
(the `review` build of `xmustard-ops`, authenticated with a separate proposer credential
if auth is enabled); never give that credential to OCR. This imports evidence, not an
approval or a verified memory. Keep the OCR run and its model/provider accounting on the external
line; xMustard receives only the permitted read-only MCP calls.

The authenticated MCP e2e in `scripts/e2e/mcp_concurrency.py` exercises eight independent
Streamable HTTP clients against one API, verifies that a reader token can use the
allowlisted read tools but cannot call `remember` or `verify`, and records the API's
observed peak RSS. Run it on the Linux build box through `ap-heavy.sh`; it makes no
provider or model calls.
