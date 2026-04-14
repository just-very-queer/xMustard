import json
import tempfile
import unittest
from pathlib import Path

from app.scanners import extract_evidence, load_verdict_items, scan_repo_signals


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

    def test_scan_repo_signals_ignores_excluded_dirs_and_docs(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir)
            (root / "src").mkdir()
            (root / "backend" / "data").mkdir(parents=True)
            (root / "research").mkdir()
            (root / "docs").mkdir()

            (root / "src" / "app.py").write_text("# TODO: real signal\n", encoding="utf-8")
            (root / "backend" / "data" / "generated.py").write_text("# TODO: generated signal\n", encoding="utf-8")
            (root / "research" / "notes.py").write_text("# TODO: research signal\n", encoding="utf-8")
            (root / "docs" / "ARCHITECTURE.md").write_text("- TODO: documentation note\n", encoding="utf-8")

            signals = scan_repo_signals(root)

        self.assertEqual(len(signals), 1)
        self.assertEqual(signals[0].file_path, "src/app.py")
        self.assertEqual(signals[0].kind, "annotation")

    def test_scan_repo_signals_finds_actionable_matches_not_pattern_literals(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir)
            (root / "src").mkdir()
            (root / "tests").mkdir()

            (root / "src" / "sample.py").write_text(
                "\n".join(
                    [
                        'BUG_HEADER_RE = re.compile(r"^###\\s+(P\\\\d...)$")',
                        'patterns = [r"NotImplementedError|raise NotImplemented", r"xfail|skip\\\\(|todo"]',
                        "# TODO: real backlog item",
                        "def pending_feature():",
                        "    raise NotImplementedError()",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            (root / "tests" / "test_sample.py").write_text(
                "\n".join(
                    [
                        "import pytest",
                        '@pytest.mark.skip(reason=\"later\")',
                        "def test_pending():",
                        "    assert True",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            signals = scan_repo_signals(root)

        observed = {(signal.kind, signal.file_path, signal.line) for signal in signals}
        self.assertEqual(
            observed,
            {
                ("annotation", "src/sample.py", 3),
                ("not_implemented", "src/sample.py", 5),
                ("test_marker", "tests/test_sample.py", 2),
            },
        )


if __name__ == "__main__":
    unittest.main()
