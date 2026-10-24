import json
import threading
import unittest
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from check_peer_registry import refresh_peer, refreshed_registry


class RegistryHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/repos/letta-ai/letta":
            self.send_response(301)
            self.send_header(
                "Location",
                f"http://127.0.0.1:{self.server.server_port}/repos/letta-ai/letta-code",
            )
            self.end_headers()
            return
        if self.path == "/repos/letta-ai/letta-code":
            self.reply({
                "full_name": "letta-ai/letta-code",
                "html_url": "https://github.com/letta-ai/letta-code",
            })
            return
        if self.path == "/repos/letta-ai/letta-code/commits?per_page=1":
            self.reply([{"commit": {"author": {"date": "2026-10-20T12:00:00Z"}, "committer": {"date": "2026-10-22T12:00:00Z"}}}])
            return
        self.send_error(404)

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

    def test_redirect_updates_canonical_url_and_supersession(self):
        peer = {
            "name": "Letta (retired server)",
            "repository": "letta-ai/letta",
            "canonical_url": "https://github.com/letta-ai/letta",
            "source_present": True,
            "superseded_by": None,
            "last_commit_date": "2026-08-15",
        }
        registry = refreshed_registry({"peers": [peer]}, self.api_root)
        got = registry["peers"][0]
        self.assertFalse(got["source_present"])
        self.assertEqual(got["superseded_by"], "letta-code")
        self.assertEqual(got["canonical_url"], "https://github.com/letta-ai/letta-code")
        self.assertEqual(got["last_commit_date"], "2026-08-15")

    def test_live_repository_uses_committer_date(self):
        peer = {
            "name": "Letta Code",
            "repository": "letta-ai/letta-code",
            "canonical_url": "https://github.com/letta-ai/letta-code",
            "source_present": True,
            "superseded_by": None,
            "last_commit_date": "2026-09-24",
        }
        got = refresh_peer(peer, self.api_root)
        self.assertTrue(got["source_present"])
        self.assertIsNone(got["superseded_by"])
        self.assertEqual(got["last_commit_date"], "2026-10-22")

    def test_peer_without_a_public_repository_has_no_commit_date(self):
        peer = {"name": "hosted", "repository": None, "canonical_url": "https://example.com"}
        got = refresh_peer(peer, self.api_root)
        self.assertFalse(got["source_present"])
        self.assertIsNone(got["superseded_by"])
        self.assertIsNone(got["last_commit_date"])


if __name__ == "__main__":
    unittest.main()
