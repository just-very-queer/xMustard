import importlib.util
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("cursor_client_rss.py")
spec = importlib.util.spec_from_file_location("cursor_client_rss", MODULE_PATH)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

# Reads stdin to EOF, then touches TOUCH_KIB KiB of fresh memory, unmaps it and exits: a
# peak after the input ends, which only a whole-run reading sees. With REPORT it prints
# its own VmRSS line and exits at once.
WITNESS = r"""
#include <fcntl.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

int main(void) {
    char buf[8192];
    while (read(0, buf, sizeof buf) > 0) {
    }
    size_t n = (size_t)TOUCH_KIB << 10;
    if (n) {
        char *p = mmap(NULL, n, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
        if (p == MAP_FAILED)
            return 1;
        for (size_t i = 0; i < n; i += 4096)
            ((volatile char *)p)[i] = 1;
        munmap(p, n);
    }
#ifdef REPORT
    int fd = open("/proc/self/status", O_RDONLY);
    ssize_t len = fd < 0 ? -1 : read(fd, buf, sizeof buf - 1);
    if (len <= 0)
        return 1;
    buf[len] = 0;
    char *line = strstr(buf, "VmRSS:");
    if (!line || write(1, line, strcspn(line, "\n")) < 0)
        return 1;
    _exit(0);
#endif
    return 0;
}
"""
TOUCH_KIB = 4096


class CursorClientRSSValidationTest(unittest.TestCase):
    def test_absent_observation_fails(self):
        with self.assertRaisesRegex(ValueError, "no RSS observations"):
            module.validate_observations([])

    def test_zero_observation_fails(self):
        with self.assertRaisesRegex(ValueError, "zero client RSS"):
            module.validate_observations([{"exit_hwm_kb": 0, "maxrss_kb": 1}])

    def test_a_run_that_never_stopped_at_exit_fails(self):
        with self.assertRaisesRegex(ValueError, "exit_hwm_kb"):
            module.validate_observations([{"exit_hwm_kb": -1, "maxrss_kb": 2000}])

    def test_unreachable_endpoint_is_a_closed_port(self):
        import socket

        base = module.unavailable_base()
        port = int(base.rsplit(":", 1)[1])
        with socket.socket() as probe:
            self.assertNotEqual(probe.connect_ex(("127.0.0.1", port)), 0)


@unittest.skipUnless(sys.platform.startswith("linux") and shutil.which("cc"), "needs Linux and a C compiler")
class CursorClientRSSMeasurementTest(unittest.TestCase):
    """The reading is the child's peak over its whole run (batch23 on line 60)."""

    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="cursor-rss-test-")
        cls.dir = Path(cls.temp.name)
        cls.source = cls.dir / "witness.c"
        cls.source.write_text(WITNESS)
        cls.runner = module.build_runner(cls.dir)
        cls.idle = cls.compile("idle", "-DTOUCH_KIB=0")
        cls.late = cls.compile("late", f"-DTOUCH_KIB={TOUCH_KIB}")

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    @classmethod
    def compile(cls, name, *flags):
        path = cls.dir / name
        subprocess.run(["cc", "-O2", *flags, "-o", str(path), str(cls.source)], check=True, capture_output=True)
        return str(path)

    def run_witness(self, binary):
        out = self.dir / "stdout"
        stats = module.run_traced(self.runner, binary, "http://127.0.0.1:9", module.EVENT, str(out))
        self.assertEqual(stats["rc"], 0, stats)
        module.validate_observations([stats])
        return stats, out.read_bytes()

    def test_memory_touched_after_the_input_ends_is_measured(self):
        stats, _ = self.run_witness(self.late)
        self.assertGreaterEqual(stats["peak_kb"], TOUCH_KIB, stats)

    def test_the_reading_is_never_under_the_rss_the_child_saw(self):
        # Since Linux 6.2, wait4 alone reads under it for this static witness (exitpeak.c).
        try:
            report = self.compile("report", "-static", "-DTOUCH_KIB=0", "-DREPORT")
        except subprocess.CalledProcessError:
            self.skipTest("no static C library on this host")
        stats, output = self.run_witness(report)
        seen = int(output.split()[1])
        self.assertGreaterEqual(stats["peak_kb"], seen, stats)

    def test_the_runner_does_not_lend_the_child_its_own_peak(self):
        # A child started from Python would read Python's peak, about 12 MiB.
        stats, _ = self.run_witness(self.idle)
        self.assertLess(stats["peak_kb"], TOUCH_KIB, stats)

    def test_linkage_tells_a_static_build_from_a_dynamic_one(self):
        self.assertEqual(module.linkage(self.idle), "dynamic")
        try:
            static = self.compile("idle-static", "-static", "-DTOUCH_KIB=0")
        except subprocess.CalledProcessError:
            self.skipTest("no static C library on this host")
        self.assertEqual(module.linkage(static), "static")


if __name__ == "__main__":
    unittest.main()
