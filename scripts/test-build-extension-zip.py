#!/usr/bin/env python3
"""collect() must never ship hidden-dir or coverage/ payload in extension.zip."""

import importlib.util
import sys
import tempfile
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "build_extension_zip", Path(__file__).parent / "build-extension-zip.py"
)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        src = Path(tmp)
        for rel in [
            "manifest.json",
            "popup.js",
            ".scannerwork/report.txt",
            ".github/workflows/ci.yml",
            "coverage/lcov.info",
            "nested/.hidden/file.js",
            ".env",
        ]:
            p = src / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text("x")
        mod.SRC = src
        got = sorted(f.relative_to(src).as_posix() for f in mod.collect())
    expected = ["manifest.json", "popup.js"]
    if got != expected:
        print(f"FAIL: collect() = {got}, want {expected}")
        return 1
    print("ok: hidden ancestors and coverage/ excluded")
    return 0


if __name__ == "__main__":
    sys.exit(main())
