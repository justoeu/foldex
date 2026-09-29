#!/usr/bin/env python3
import re
import sys

try:
    data = open(sys.argv[1], "rb").read().decode("utf-8", "replace")
except OSError:
    data = ""
found = re.findall(r"(\d+) complete", data)
print(found[-1] if found else "0")
