import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from typing import Optional
from unittest.mock import patch

from app.service import TrackerService
from app.store import FileStore


class RustCoverageMigrationTests(unittest.TestCase):
    def _rust_parse_coverage(
        self,
        report_path: Path,
        workspace_id: str,
        run_id: Optional[str] = None,
        issue_id: Optional[str] = None,
    ):
        repo_root = Path(__file__).resolve().parents[2]
        command = [
            "cargo",
            "run",
            "--quiet",
            "--bin",
            "xmustard-core",
            "--",
            "parse-coverage",
            workspace_id,
            str(report_path),
        ]
        if run_id:
            command.append(run_id)
        if issue_id:
            command.append(issue_id)

        completed = subprocess.run(
            command,
            cwd=repo_root / "rust-core",
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(completed.stdout)

    def test_rust_lcov_parser_matches_python_result(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            report_path = Path(tmp_dir) / "coverage.info"
            report_path.write_text(
                "\n".join(
                    [
                        "SF:src/app.py",
                        "DA:1,1",
                        "DA:2,0",
                        "end_of_record",
                        "SF:src/empty.py",
                        "DA:1,0",
                        "end_of_record",
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            service = TrackerService(FileStore(Path(tmp_dir) / "data"))
            python_result = service._parse_lcov(
                report_path.read_text(encoding="utf-8"),
                "workspace-1",
                "run-1",
                "issue-1",
                "lcov",
                str(report_path),
            ).model_dump(mode="json")
            rust_result = self._rust_parse_coverage(report_path, "workspace-1", "run-1", "issue-1")

        self.assertEqual(python_result["workspace_id"], rust_result["workspace_id"])
        self.assertEqual(python_result["run_id"], rust_result["run_id"])
        self.assertEqual(python_result["issue_id"], rust_result["issue_id"])
        self.assertEqual(python_result["line_coverage"], rust_result["line_coverage"])
        self.assertEqual(python_result["lines_covered"], rust_result["lines_covered"])
        self.assertEqual(python_result["lines_total"], rust_result["lines_total"])
        self.assertEqual(python_result["files_covered"], rust_result["files_covered"])
        self.assertEqual(python_result["files_total"], rust_result["files_total"])
        self.assertEqual(python_result["uncovered_files"], rust_result["uncovered_files"])
        self.assertEqual(python_result["format"], rust_result["format"])
        self.assertEqual(python_result["raw_report_path"], rust_result["raw_report_path"])

    def test_rust_cobertura_parser_matches_python_result(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            report_path = Path(tmp_dir) / "coverage.xml"
            report_path.write_text(
                '<coverage line-rate="0.5" branch-rate="0.25"><packages><package><classes>'
                '<class filename="src/app.py"><lines><line number="1" hits="1"/><line number="2" hits="0"/></lines></class>'
                '<class filename="src/empty.py"><lines><line number="1" hits="0"/></lines></class>'
                "</classes></package></packages></coverage>",
                encoding="utf-8",
            )

            service = TrackerService(FileStore(Path(tmp_dir) / "data"))
            python_result = service._parse_cobertura(
                report_path.read_text(encoding="utf-8"),
                "workspace-1",
                "run-1",
                "issue-1",
                "cobertura",
                str(report_path),
            ).model_dump(mode="json")
            rust_result = self._rust_parse_coverage(report_path, "workspace-1", "run-1", "issue-1")

        self.assertEqual(python_result["line_coverage"], rust_result["line_coverage"])
        self.assertEqual(python_result["branch_coverage"], rust_result["branch_coverage"])
        self.assertEqual(python_result["lines_covered"], rust_result["lines_covered"])
        self.assertEqual(python_result["lines_total"], rust_result["lines_total"])
        self.assertEqual(python_result["files_covered"], rust_result["files_covered"])
        self.assertEqual(python_result["files_total"], rust_result["files_total"])
        self.assertEqual(python_result["uncovered_files"], rust_result["uncovered_files"])
        self.assertEqual(python_result["format"], rust_result["format"])

    def test_rust_istanbul_parser_matches_python_result(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            report_path = Path(tmp_dir) / "coverage.json"
            report_path.write_text(
                json.dumps(
                    {
                        "coverage": {
                            "src/app.ts": {"s": {"1": 1, "2": 0}},
                            "src/empty.ts": {"s": {"1": 0}},
                        }
                    }
                ),
                encoding="utf-8",
            )

            service = TrackerService(FileStore(Path(tmp_dir) / "data"))
            python_result = service._parse_istanbul(
                json.loads(report_path.read_text(encoding="utf-8")),
                "workspace-1",
                "run-1",
                "issue-1",
                "istanbul",
                str(report_path),
            ).model_dump(mode="json")
            rust_result = self._rust_parse_coverage(report_path, "workspace-1", "run-1", "issue-1")

        self.assertEqual(python_result["line_coverage"], rust_result["line_coverage"])
        self.assertEqual(python_result["lines_covered"], rust_result["lines_covered"])
        self.assertEqual(python_result["lines_total"], rust_result["lines_total"])
        self.assertEqual(python_result["files_covered"], rust_result["files_covered"])
        self.assertEqual(python_result["files_total"], rust_result["files_total"])
        self.assertEqual(python_result["uncovered_files"], rust_result["uncovered_files"])
        self.assertEqual(python_result["format"], rust_result["format"])

    def test_python_coverage_parser_can_delegate_to_rust_via_env_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            report_path = Path(tmp_dir) / "coverage.info"
            report_path.write_text("SF:src/app.py\nDA:1,1\nDA:2,0\nend_of_record\n", encoding="utf-8")
            service = TrackerService(FileStore(Path(tmp_dir) / "data"))

            python_result = service._parse_coverage_file(report_path, "workspace-1", "run-1", "issue-1")
            with patch.dict(os.environ, {"XMUSTARD_USE_RUST_COVERAGE": "1"}, clear=False):
                rust_result = service._parse_coverage_file(report_path, "workspace-1", "run-1", "issue-1")

        self.assertEqual(python_result.line_coverage, rust_result.line_coverage)
        self.assertEqual(python_result.lines_covered, rust_result.lines_covered)
        self.assertEqual(python_result.lines_total, rust_result.lines_total)
        self.assertEqual(python_result.files_covered, rust_result.files_covered)
        self.assertEqual(python_result.files_total, rust_result.files_total)
        self.assertEqual(python_result.uncovered_files, rust_result.uncovered_files)
        self.assertEqual(python_result.format, rust_result.format)


if __name__ == "__main__":
    unittest.main()
