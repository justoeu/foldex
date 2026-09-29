#!/usr/bin/env python3
import re
import sys


def links(text: str) -> int:
    match = re.search(r"links=(\d+)", text or "")
    return int(match.group(1)) if match else -1


base, last = links(sys.argv[1]), links(sys.argv[2])
print(last - base if base >= 0 and last >= 0 else -1)
