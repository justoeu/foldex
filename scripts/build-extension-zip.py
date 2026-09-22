#!/usr/bin/env python3
"""Build the deterministic extension zip the backend embeds via go:embed.

`make extension` is the only entry point. Determinism is not a nicety: the
output is COMMITTED under backend/internal/addon/dist/, so two builds of the
same tree must produce byte-identical bytes or every rebuild leaves the tree
dirty and a stale embed becomes indistinguishable from a current one.

Fixed inputs: sorted entry order, mtime 1980-01-01 (the ZIP epoch), deflate
compression, no directory entries, no platform-dependent attributes.
"""

import json
import os
import re
import sys
import tempfile
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "extension"
DIST = ROOT / "backend/internal/addon/dist"

# Test sources never ship inside the extension an admin downloads: they are
# dev-only in either layout wt3 may use (a test/ dir or co-located *.test.*).
EXCLUDED_DIRS = {"node_modules", ".git", "test", "tests"}
EXCLUDED_SUFFIXES = (".test.js", ".test.mjs", ".test.cjs")

FIXED_DATE_TIME = (1980, 1, 1, 0, 0, 0)  # the ZIP epoch


def fail(msg: str) -> None:
    sys.stderr.write(f"build-extension-zip: {msg}\n")
    sys.exit(1)


def collect() -> list[Path]:
    out = []
    for path in sorted(SRC.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(SRC)
        if any(part in EXCLUDED_DIRS for part in rel.parts[:-1]):
            continue
        if rel.name.startswith("."):
            continue
        if rel.name.endswith(EXCLUDED_SUFFIXES):
            continue
        out.append(path)
    return out


def main() -> None:
    manifest_path = SRC / "manifest.json"
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"extension/manifest.json does not parse: {exc}")

    version = str(manifest.get("version", ""))
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        fail(f"manifest version {version!r} is not a dotted triple (Chrome requirement)")

    files = collect()
    if not any(f.relative_to(SRC) == Path("manifest.json") for f in files):
        fail("no files collected — manifest.json must ship at the zip root")

    DIST.mkdir(parents=True, exist_ok=True)
    zip_path = DIST / "extension.zip"
    # Write to a sibling temp file and replace, so a failed run never leaves a
    # half-written zip in the embed dir (go:embed would happily compile it).
    fd, tmp = tempfile.mkstemp(dir=DIST, suffix=".tmp")
    os.close(fd)
    try:
        with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
            for path in files:
                rel = path.relative_to(SRC).as_posix()
                info = zipfile.ZipInfo(rel, date_time=FIXED_DATE_TIME)
                info.compress_type = zipfile.ZIP_DEFLATED
                info.create_system = 0  # platform-independent
                info.external_attr = 0o644 << 16
                zf.writestr(info, path.read_bytes())
        # mkstemp creates 0600; the committed artifact keeps the tree's norm.
        os.chmod(tmp, 0o644)
        os.replace(tmp, zip_path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)

    (DIST / "version.txt").write_text(version + "\n", encoding="utf-8")
    print(f"extension {version}: {len(files)} files -> {zip_path.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
