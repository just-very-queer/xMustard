import json
import threading
import unittest
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from check_peer_registry import date_drift_messages, refresh_peer, refreshed_registry, registry_check_view


class RegistryHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/repos/letta-ai/letta":
            self.reply({
                "full_name": "letta-ai/letta",
                "html_url": "https://github.com/letta-ai/letta",
            })
            return
        if self.path == "/repos/letta-ai/letta/commits?per_page=1":
            self.commit_reply("2026-10-22")
            return
        if self.path == "/repos/block/goose":
            self.send_response(301)
            self.send_header(
                "Location",
                f"http://127.0.0.1:{self.server.server_port}/repos/aaif-goose/goose",
            )
            self.end_headers()
            return
        if self.path == "/repos/aaif-goose/goose":
            self.reply({
                "full_name": "aaif-goose/goose",
                "html_url": "https://github.com/aaif-goose/goose",
            })
            return
        if self.path == "/repos/aaif-goose/goose/commits?per_page=1":
            self.commit_reply("2026-10-23")
            return
        self.send_error(404)

    def commit_reply(self, date):
        self.reply([{"commit": {"committer": {"date": f"{date}T12:00:00Z"}}}])

    def reply(self, value):
        body = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        pass


class PeerRegistryTest(unittest.TestCase):
    def test_refresh_workflow_is_manual(self):
        workflow = Path(__file__).parents[1] / ".github/workflows/peer-registry.yml"
        workflow_text = workflow.read_text(encoding="utf-8")
        self.assertIn("on:\n  workflow_dispatch:\n", workflow_text)
        self.assertNotIn("schedule:", workflow_text)

    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RegistryHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.api_root = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def test_tombstone_keeps_curated_liveness_and_updates_date(self):
        peer = {
            "name": "Letta (retired server)",
            "repository": "letta-ai/letta",
            "canonical_url": "https://github.com/letta-ai/letta",
            "source_present": False,
            "superseded_by": "letta-code",
            "last_commit_date": "2026-08-15",
        }
        got = refresh_peer(peer, self.api_root)
        self.assertFalse(got["source_present"])
        self.assertEqual(got["superseded_by"], "letta-code")
        self.assertEqual(got["canonical_url"], "https://github.com/letta-ai/letta")
        self.assertEqual(got["last_commit_date"], "2026-10-22")

    def test_redirect_changes_canonical_url_without_rewriting_curated_fields(self):
        peer = {
            "name": "Goose",
            "repository": "block/goose",
            "canonical_url": "https://github.com/block/goose",
            "source_present": True,
            "superseded_by": None,
            "last_commit_date": "2026-10-01",
        }
        got = refresh_peer(peer, self.api_root)
        self.assertTrue(got["source_present"])
        self.assertIsNone(got["superseded_by"])
        self.assertEqual(got["canonical_url"], "https://github.com/aaif-goose/goose")
        self.assertEqual(got["last_commit_date"], "2026-10-23")

    def test_check_view_ignores_commit_date_drift_and_reports_it(self):
        before = {"peers": [{"repository": "x/y", "canonical_url": "url", "source_present": True, "superseded_by": None, "last_commit_date": "2026-10-01"}]}
        after = {"peers": [{**before["peers"][0], "last_commit_date": "2026-10-22"}]}
        self.assertEqual(registry_check_view(before), registry_check_view(after))
        self.assertEqual(
            date_drift_messages(before, after),
            ["commit date drift for x/y: 2026-10-01 -> 2026-10-22"],
        )

    def test_peer_without_a_public_repository_is_unchanged(self):
        peer = {"name": "hosted", "repository": None, "canonical_url": "https://example.com", "source_present": False}
        self.assertEqual(refresh_peer(peer, self.api_root), peer)


if __name__ == "__main__":
    unittest.main()
