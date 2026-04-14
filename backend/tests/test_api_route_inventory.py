import json
import unittest
from pathlib import Path

from app import main as app_main


class ApiRouteInventoryTests(unittest.TestCase):
    def test_go_migration_inventory_matches_live_fastapi_routes(self):
        repo_root = Path(__file__).resolve().parents[2]
        inventory_path = repo_root / "api-go" / "internal" / "migration" / "api_route_groups.json"
        payload = json.loads(inventory_path.read_text(encoding="utf-8"))

        documented_routes = {
            endpoint
            for group in payload
            for endpoint in group.get("endpoints", [])
        }
        live_routes = {
            route.path
            for route in app_main.app.routes
            if getattr(route, "path", "").startswith("/api/")
        }

        self.assertTrue(documented_routes)
        self.assertFalse(documented_routes - live_routes)


if __name__ == "__main__":
    unittest.main()
