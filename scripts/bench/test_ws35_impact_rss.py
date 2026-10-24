import importlib.util
from pathlib import Path
import unittest


SCRIPT = Path(__file__).with_name("ws35_impact_rss.py")
SPEC = importlib.util.spec_from_file_location("ws35_impact_rss", SCRIPT)
ws35_impact_rss = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ws35_impact_rss)


class ImpactCountMedianTest(unittest.TestCase):
    def test_ignores_unresolved_null_counts(self):
        self.assertEqual(
            ws35_impact_rss.impact_count_median(
                [{"impacted_count": 2}, {"impacted_count": None}, {"impacted_count": 6}]
            ),
            4,
        )

    def test_returns_none_when_all_counts_are_unknown(self):
        self.assertIsNone(ws35_impact_rss.impact_count_median([{"impacted_count": None}]))


if __name__ == "__main__":
    unittest.main()
