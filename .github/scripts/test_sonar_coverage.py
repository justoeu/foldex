import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("sonar-coverage.py")


class CoveragePathsTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        for name in ("backend/internal/service.go", "web/src/App.tsx"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        (self.root / "backend/go.mod").write_text("module foldex\n\ngo 1.26.6\n")
        self.go = self.root / "backend/coverage.out"
        self.lcov = self.root / "web/coverage/lcov.info"
        self.lcov.parent.mkdir()
        self.go.write_text("mode: atomic\nfoldex/internal/service.go:1.1,2.1 1 1\n")
        self.lcov.write_text("TN:\nSF:src/App.tsx\nDA:1,1\nend_of_record\n")

    def run_script(self):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "web", "backend/coverage.out"],
            cwd=self.root, capture_output=True, text=True,
        )

    def test_module_and_frontend_paths_preserve_counts(self):
        expected_go = "mode: atomic\nbackend/internal/service.go:1.1,2.1 1 1\n"
        expected_lcov = "TN:\nSF:web/src/App.tsx\nDA:1,1\nend_of_record\n"
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.go.read_text(), expected_go)
        self.assertEqual(self.lcov.read_text(), expected_lcov)
        self.assertEqual(self.run_script().returncode, 0)
        self.assertEqual(self.go.read_text(), expected_go)
        self.assertEqual(self.lcov.read_text(), expected_lcov)

    def test_absolute_paths_inside_checkout(self):
        self.lcov.write_text(f"SF:{self.root}/web/src/App.tsx\nDA:1,0\nend_of_record\n")
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.lcov.read_text(), "SF:web/src/App.tsx\nDA:1,0\nend_of_record\n")

    def test_backend_relative_path(self):
        self.go.write_text("mode: atomic\ninternal/service.go:1.1,2.1 1 0\n")
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("backend/internal/service.go:1.1,2.1 1 0", self.go.read_text())

    def test_missing_source_is_not_silently_dropped(self):
        self.lcov.write_text("SF:src/absent.tsx\nDA:1,1\nend_of_record\n")
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Coverage source not found inside checkout: src/absent.tsx", result.stderr)

    def test_empty_reports_fail(self):
        for go, lcov in (("mode: atomic\n", "SF:src/App.tsx\n"),
                         ("mode: atomic\nfoldex/internal/service.go:1.1,2.1 1 1\n", "TN:\n")):
            with self.subTest(go=go, lcov=lcov):
                self.go.write_text(go)
                self.lcov.write_text(lcov)
                result = self.run_script()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Missing or empty", result.stderr)

    def test_source_cannot_escape_checkout(self):
        with tempfile.TemporaryDirectory() as outside:
            external = Path(outside) / "private.tsx"
            external.touch()
            link = self.root / "web/src/external.tsx"
            link.symlink_to(external)
            for value in (str(external), "src/external.tsx"):
                with self.subTest(value=value):
                    self.lcov.write_text(f"SF:{value}\nDA:1,1\nend_of_record\n")
                    result = self.run_script()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("Coverage source not found inside checkout", result.stderr)


if __name__ == "__main__":
    unittest.main()
