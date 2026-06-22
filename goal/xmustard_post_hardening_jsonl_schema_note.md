# JSONL appendix schema

The uploaded package did not contain a root `findings.jsonl`; it did contain `goal/xmustard_new_findings.jsonl`. The new appendix follows that file's schema exactly:

- `record_type`
- `id`
- `status`
- `confidence`
- `severity`
- `priority`
- `category`
- `title`
- `summary`
- `trigger`
- `impact`
- `evidence` array of `{path,line_start,line_end,note}`
- `failure_chain` array
- `fix`
- `verification`
- `effort`
- `duplicate_check`

All records were validated with Python `json.loads`.
