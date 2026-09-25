# Diagnostics redesign writer checkpoint — 2026-09-24

Claude Opus job `8180613c` stopped after local Claude authentication expired;
`claude auth status` reported `loggedIn: false`.

The isolated worktree is `/private/tmp/xmustard-opus-l9b1F4`, at base/HEAD
`cd13e2b78a9fadcbcd760b19442d5992136b1db0`. The only writer artifact from that
job is the untracked 232-line
`api-go/internal/workspaceops/diagnostics_read_stream.go` (SHA-256
`bb444fb35935fc4233690e59fd3a829409770245a9fc452f0ae656053db9e0bc`). The
provisional design plan is
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md` (SHA-256
`62f8fee5be15792fc70c65206635c4287a8e60672ef64dd1ed85f2a0799f8501`). No
writer integration exists yet, and nothing has been imported into the main
checkout.

Reported check: `cd api-go && go test ./internal/workspaceops` passed in 31.8
seconds. That suite does not exercise the new helper; no helper-specific test
has run.

Confirmed correctness issue: `jsonStream.str` writes a literal U+FFFD
replacement rune for invalid UTF-8. Go `encoding/json` emits the six ASCII
bytes `\ufffd` instead, so current output is not byte-compatible. Short-write
handling also needs inspection: `flush` records the writer's count/error and
clears the buffer; whether a short write with nil error is handled correctly
remains unverified, not a confirmed defect.

Open work: fix and test encoder parity (including the plan's row-string cases),
inspect short writes, then integrate the helper behind the prepared-read seam
and add end-to-end/API coverage. Keep the redesign provisional; this checkpoint
does not authorize implementation or main-checkout import.
