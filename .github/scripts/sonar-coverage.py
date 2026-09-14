#!/usr/bin/env python3
"""Map coverage from module/test roots to the Sonar repository root."""
from pathlib import Path
from functools import cache
import sys

frontend = sys.argv[1]
profiles = [Path(value) for value in sys.argv[2:]]
module = next(line.split()[1] for line in Path("backend/go.mod").read_text().splitlines() if line.startswith("module "))
root = Path.cwd().resolve()

@cache
def source(value, prefix):
    path = Path(value)
    if not path.is_absolute():
        path = root / path
    if not path.is_file():
        path = root / prefix / value
    resolved = path.resolve()
    if not resolved.is_relative_to(root) or not resolved.is_file():
        raise SystemExit(f"Coverage source not found inside checkout: {value}")
    return resolved.relative_to(root).as_posix()

for profile in profiles:
    lines = profile.read_text().splitlines()
    if len(lines) < 2 or not lines[0].startswith("mode: "):
        raise SystemExit(f"Missing or empty Go coverage: {profile}")
    mapped = [lines[0]]
    for line in lines[1:]:
        file, data = line.split(":", 1)
        if file.startswith(module + "/"):
            file = "backend/" + file[len(module) + 1:]
        mapped.append(source(file, "backend") + ":" + data)
    profile.write_text("\n".join(mapped) + "\n")

lcov = Path(frontend) / "coverage/lcov.info"
lines = lcov.read_text().splitlines()
if not any(line.startswith("SF:") for line in lines):
    raise SystemExit("Missing or empty frontend LCOV")
mapped = ["SF:" + source(line[3:], frontend) if line.startswith("SF:") else line for line in lines]
lcov.write_text("\n".join(mapped) + "\n")
