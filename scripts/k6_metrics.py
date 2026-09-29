#!/usr/bin/env python3
"""Read /metrics and print one line of pool gauges. Never prints the token."""
import os
import sys
import urllib.request
from pathlib import Path

GAUGES = (
    "go_goroutines",
    "foldex_http_requests_in_flight",
    "foldex_db_pool_acquired_conns",
    "foldex_db_pool_idle_conns",
    "foldex_db_pool_empty_acquire_count",
    "foldex_db_pool_empty_acquire_wait_seconds",
    "foldex_db_pool_canceled_acquire_count",
)
NA = "\t".join(["na"] * (len(GAUGES) + 2))


def token() -> str:
    for line in Path(os.environ["FOLDEX_GATE_ENV"]).read_text().splitlines():
        if line.startswith("METRICS_TOKEN="):
            return line.split("=", 1)[1]
    return ""


def metrics_url() -> str:
    explicit = os.environ.get("FOLDEX_K6_METRICS_URL", "").strip()
    if explicit:
        return explicit
    base = os.environ.get("K6_BASE_URL", "http://127.0.0.1:9089").strip().rstrip("/")
    return base + "/metrics"


def main() -> None:
    alert = os.environ.get("FOLDEX_GATE_ALERT", "")
    try:
        bearer = token()
        if not bearer:
            raise RuntimeError("missing")
        req = urllib.request.Request(
            metrics_url(),
            headers={"Authorization": "Bearer " + bearer},
        )
        with urllib.request.urlopen(req, timeout=3) as res:
            body = res.read().decode("utf-8", "replace")
    except Exception:
        sys.stdout.write(NA + "\n")
        return
    values = {name: "na" for name in GAUGES}
    status_200 = 0.0
    status_503 = 0.0
    seen_status = False
    for line in body.splitlines():
        if not line or line.startswith("#"):
            continue
        name, _, rest = line.partition(" ")
        if name in values:
            values[name] = rest.split()[0]
            continue
        if name.startswith("foldex_http_requests_total{") and 'status="' in name:
            try:
                number = float(rest.split()[0])
            except ValueError:
                continue
            seen_status = True
            if 'status="503"' in name:
                status_503 += number
            elif 'status="200"' in name or 'status="201"' in name:
                status_200 += number
    if not seen_status:
        status_200_s = "na"
        status_503_s = "na"
    else:
        status_200_s = str(int(status_200))
        status_503_s = str(int(status_503))
    ordered = [values[name] for name in GAUGES] + [status_503_s, status_200_s]
    sys.stdout.write("\t".join(ordered) + "\n")
    try:
        goroutines = float(values["go_goroutines"])
    except ValueError:
        goroutines = 0
    if alert and goroutines > 15000:
        Path(alert).write_text(f"goroutines={int(goroutines)}\n")


if __name__ == "__main__":
    main()
