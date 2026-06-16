import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from app.models import VerificationProfileRecord
from app.service import TrackerService
from app.store import FileStore


class RustVerificationMigrationTests(unittest.TestCase):
    def _rust_run_verification_command(self, workspace_root: Path, command: str, timeout_seconds: int):
        repo_root = Path(__file__).resolve().parents[2]
        completed = subprocess.run(
            [
                "cargo",
                "run",
                "--quiet",
                "--bin",
                "xmustard-core",
                "--",
                "run-verification-command",
                str(workspace_root),
                str(timeout_seconds),
                command,
            ],
            cwd=repo_root / "rust-core",
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(completed.stdout)

    def _rust_run_verification_profile(self, workspace_root: Path, profile: VerificationProfileRecord, run_id: str = "run-1", issue_id: str = "issue-1"):
        repo_root = Path(__file__).resolve().parents[2]
        profile_path = workspace_root / "verification-profile.json"
        profile_path.write_text(json.dumps(profile.model_dump(mode="json")), encoding="utf-8")
        completed = subprocess.run(
            [
                "cargo",
                "run",
                "--quiet",
                "--bin",
                "xmustard-core",
                "--",
                "run-verification-profile",
                str(workspace_root),
                str(profile_path),
                run_id,
                issue_id,
            ],
            cwd=repo_root / "rust-core",
            capture_output=True,
            text=True,
            check=True,
        )
        return json.loads(completed.stdout)

    def test_rust_verification_runner_reports_successful_command(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            workspace_root = Path(tmp_dir)
            rust_result = self._rust_run_verification_command(workspace_root, "printf 'ok\\n'", 2)

        self.assertTrue(rust_result["success"])
        self.assertFalse(rust_result["timed_out"])
        self.assertEqual(rust_result["exit_code"], 0)
        self.assertEqual(rust_result["stdout_excerpt"], "ok\n")

    def test_python_verification_runner_can_delegate_to_rust_via_env_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            workspace_root = Path(tmp_dir)
            service = TrackerService(FileStore(workspace_root / "data"))

            python_result = service._run_verification_command(workspace_root, "printf 'ok\\n'", 2)
            with patch.dict(os.environ, {"XMUSTARD_USE_RUST_VERIFICATION": "1"}, clear=False):
                rust_result = service._run_verification_command(workspace_root, "printf 'ok\\n'", 2)

        self.assertEqual(python_result.success, rust_result.success)
        self.assertEqual(python_result.timed_out, rust_result.timed_out)
        self.assertEqual(python_result.exit_code, rust_result.exit_code)
        self.assertEqual(python_result.stdout_excerpt, rust_result.stdout_excerpt)

    def test_python_verification_runner_marks_timeouts(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            workspace_root = Path(tmp_dir)
            service = TrackerService(FileStore(workspace_root / "data"))

            result = service._run_verification_command(workspace_root, "sleep 2", 1)

        self.assertFalse(result.success)
        self.assertTrue(result.timed_out)
        self.assertIsNone(result.exit_code)

    def test_rust_verification_profile_runner_handles_retry_and_coverage(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            workspace_root = Path(tmp_dir)
            profile = VerificationProfileRecord(
                profile_id="backend-pytest",
                workspace_id="workspace-1",
                name="Backend pytest",
                description="Verification profile",
                test_command="if [ ! -f .attempt ]; then touch .attempt; exit 1; fi; printf 'tests ok\\n'",
                coverage_command="printf 'SF:src/app.py\\nDA:1,1\\nDA:2,0\\nend_of_record\\n' > coverage.info",
                coverage_report_path="coverage.info",
                coverage_format="lcov",
                max_runtime_seconds=2,
                retry_count=1,
                source_paths=["AGENTS.md"],
            )

            rust_result = self._rust_run_verification_profile(workspace_root, profile)

        self.assertTrue(rust_result["success"])
        self.assertEqual(rust_result["attempt_count"], 2)
        self.assertEqual(len(rust_result["attempts"]), 2)
        self.assertFalse(rust_result["attempts"][0]["success"])
        self.assertTrue(rust_result["attempts"][1]["success"])
        self.assertTrue(rust_result["coverage_command_result"]["success"])
        self.assertEqual(rust_result["coverage_result"]["format"], "lcov")
        self.assertEqual(rust_result["coverage_result"]["lines_covered"], 1)

    def test_python_verification_profile_runner_can_delegate_to_rust_via_env_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            workspace_root = Path(tmp_dir)
            service = TrackerService(FileStore(workspace_root / "data"))
            profile = VerificationProfileRecord(
                profile_id="backend-pytest",
                workspace_id="workspace-1",
                name="Backend pytest",
                description="Verification profile",
                test_command="printf 'tests ok\\n'",
                coverage_command="printf 'SF:src/app.py\\nDA:1,1\\nend_of_record\\n' > coverage.info",
                coverage_report_path="coverage.info",
                coverage_format="lcov",
                max_runtime_seconds=2,
                retry_count=1,
                source_paths=[],
            )

            python_result = service._execute_verification_profile(workspace_root, profile, "run-1", "issue-1")
            with patch.dict(os.environ, {"XMUSTARD_USE_RUST_VERIFICATION": "1"}, clear=False):
                rust_result = service._execute_verification_profile(workspace_root, profile, "run-1", "issue-1")

        self.assertEqual(python_result.success, rust_result.success)
        self.assertEqual(python_result.attempt_count, rust_result.attempt_count)
        self.assertEqual(len(python_result.attempts), len(rust_result.attempts))
        self.assertEqual(python_result.coverage_result.format, rust_result.coverage_result.format)
        self.assertEqual(python_result.coverage_result.lines_covered, rust_result.coverage_result.lines_covered)


if __name__ == "__main__":
    unittest.main()
