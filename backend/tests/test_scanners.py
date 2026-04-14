import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from app.scanners import build_repo_map, extract_evidence, load_verdict_items, scan_repo_signals


class ScannerTests(unittest.TestCase):
    def _rust_scan(self, root: Path):
        repo_root = Path(__file__).resolve().parents[2]
        completed = subprocess.run(
            [
                "cargo",
                "run",
                "--quiet",
                "--bin",
                "xmustard-core",
                "--",
                "scan-signals",
                str(root),
            ],
            cwd=repo_root / "rust-core",
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(completed.stdout)

    def _rust_repo_map(self, root: Path, workspace_id: str):
        repo_root = Path(__file__).resolve().parents[2]
        completed = subprocess.run(
            [
                "cargo",
                "run",
                "--quiet",
                "--bin",
                "xmustard-core",
                "--",
                "build-repo-map",
                workspace_id,
                str(root),
            ],
            cwd=repo_root / "rust-core",
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(completed.stdout)

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

    def test_rust_signal_scanner_matches_python_output_for_actionable_fixture(self):
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

            python_signals = scan_repo_signals(root)
            rust_signals = self._rust_scan(root)

        python_observed = sorted(
            {
                (
                    signal.kind,
                    signal.severity,
                    signal.file_path,
                    signal.line,
                    signal.summary,
                )
                for signal in python_signals
            }
        )
        rust_observed = sorted(
            {
                (
                    signal["kind"],
                    signal["severity"],
                    signal["file_path"],
                    signal["line"],
                    signal["summary"],
                )
                for signal in rust_signals
            }
        )
        self.assertEqual(python_observed, rust_observed)

    def test_scan_repo_signals_can_delegate_to_rust_via_env_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir)
            (root / "src").mkdir()
            (root / "src" / "app.py").write_text(
                "\n".join(
                    [
                        "# TODO: real backlog item",
                        "def pending_feature():",
                        "    raise NotImplementedError()",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            python_signals = scan_repo_signals(root)
            with patch.dict(os.environ, {"XMUSTARD_USE_RUST_SCANNER": "1"}, clear=False):
                rust_backed_signals = scan_repo_signals(root)

        python_observed = sorted((signal.kind, signal.file_path, signal.line) for signal in python_signals)
        rust_observed = sorted((signal.kind, signal.file_path, signal.line) for signal in rust_backed_signals)
        self.assertEqual(python_observed, rust_observed)

    def test_rust_repo_map_matches_python_summary_for_fixture(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir)
            workspace_id = "workspace-1"
            (root / "backend" / "app").mkdir(parents=True)
            (root / "frontend" / "src").mkdir(parents=True)
            (root / "tests").mkdir(parents=True)
            (root / "backend" / "data").mkdir(parents=True)
            (root / "research").mkdir(parents=True)

            (root / "AGENTS.md").write_text("# instructions\n", encoding="utf-8")
            (root / "README.md").write_text("# readme\n", encoding="utf-8")
            (root / "backend" / "app" / "main.py").write_text("print('main')\n", encoding="utf-8")
            (root / "frontend" / "src" / "App.tsx").write_text("export function App() { return null }\n", encoding="utf-8")
            (root / "tests" / "test_sample.py").write_text("def test_ok():\n    assert True\n", encoding="utf-8")
            (root / "backend" / "data" / "generated.py").write_text("print('ignore')\n", encoding="utf-8")
            (root / "research" / "notes.py").write_text("print('ignore')\n", encoding="utf-8")

            python_summary = build_repo_map(root, workspace_id).model_dump(mode="json")
            rust_summary = self._rust_repo_map(root, workspace_id)

        self.assertEqual(python_summary["workspace_id"], rust_summary["workspace_id"])
        self.assertEqual(python_summary["root_path"], rust_summary["root_path"])
        self.assertEqual(python_summary["total_files"], rust_summary["total_files"])
        self.assertEqual(python_summary["source_files"], rust_summary["source_files"])
        self.assertEqual(python_summary["test_files"], rust_summary["test_files"])
        self.assertEqual(python_summary["top_extensions"], rust_summary["top_extensions"])

        python_directories = sorted(
            (item["path"], item["file_count"], item["source_file_count"], item["test_file_count"])
            for item in python_summary["top_directories"]
        )
        rust_directories = sorted(
            (item["path"], item["file_count"], item["source_file_count"], item["test_file_count"])
            for item in rust_summary["top_directories"]
        )
        self.assertEqual(python_directories, rust_directories)

        python_key_files = sorted((item["path"], item["role"]) for item in python_summary["key_files"])
        rust_key_files = sorted((item["path"], item["role"]) for item in rust_summary["key_files"])
        self.assertEqual(python_key_files, rust_key_files)

    def test_build_repo_map_can_delegate_to_rust_via_env_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir)
            workspace_id = "workspace-2"
            (root / "src").mkdir()
            (root / "tests").mkdir()
            (root / "src" / "app.py").write_text("print('ok')\n", encoding="utf-8")
            (root / "tests" / "test_app.py").write_text("def test_ok():\n    assert True\n", encoding="utf-8")

            python_summary = build_repo_map(root, workspace_id)
            with patch.dict(os.environ, {"XMUSTARD_USE_RUST_REPOMAP": "1"}, clear=False):
                rust_backed_summary = build_repo_map(root, workspace_id)

        self.assertEqual(python_summary.total_files, rust_backed_summary.total_files)
        self.assertEqual(python_summary.source_files, rust_backed_summary.source_files)
        self.assertEqual(python_summary.test_files, rust_backed_summary.test_files)
        self.assertEqual(
            sorted((item.path, item.role) for item in python_summary.key_files),
            sorted((item.path, item.role) for item in rust_backed_summary.key_files),
        )


if __name__ == "__main__":
    unittest.main()
