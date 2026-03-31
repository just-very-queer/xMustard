import json
import tempfile
import unittest
from pathlib import Path

from app.scanners import extract_evidence, load_verdict_items


class ScannerTests(unittest.TestCase):
    def test_extract_evidence_ignores_commands_and_keeps_paths(self):
        line = "- `api/src/routers/kb.py:273` `python3 -m pytest test/test_kb_router.py -v` (`10 passed`)"
        evidence = extract_evidence(line)

        self.assertEqual(len(evidence), 1)
        self.assertEqual(evidence[0].path, "api/src/routers/kb.py")
        self.assertEqual(evidence[0].line, 273)

    def test_load_verdict_items_accepts_wrapped_verdict_payload(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            verdict_path = Path(tmp_dir) / "wrapped_verdicts.json"
            verdict_path.write_text(
                json.dumps(
                    {
                        "verdicts": [
                            {"id": "P2_25M03_044", "verdict": "open"},
                            {"id": "P2_25M03_045", "verdict": "fixed"},
                        ]
                    }
                ),
                encoding="utf-8",
            )

            items = load_verdict_items(verdict_path)

        self.assertEqual(len(items), 2)
        self.assertEqual(items[0]["id"], "P2_25M03_044")


if __name__ == "__main__":
    unittest.main()
