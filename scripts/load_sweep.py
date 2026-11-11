#!/usr/bin/env python3
"""load_sweep.py: measure how the warm pool holds up as request rate grows.

For each offered rate (requests/second) it runs several timed trials of
open-loop load: requests start on a fixed schedule whether or not earlier
ones have finished, like independent users arriving. Each trial records
every request's status and latency.

Usage: python3 scripts/load_sweep.py [output.csv]   (default bench-results/sweep.csv)
Env:   GATEWAY (default http://localhost:8080), RATES ("2,4,8,12,16,24,32"),
       TRIALS (3), DURATION (seconds per trial, 10)

The pool holds 5 sandboxes and one refill takes ~480ms, so it can sustain
roughly 5 / 0.48 = ~10 warm hits per second. Above that, the extra requests
fall back to cold starts. Standard library only.
"""
import json
import os
import sys
import threading
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

GATEWAY = os.environ.get("GATEWAY", "http://localhost:8080")
RATES = [float(r) for r in os.environ.get("RATES", "2,4,8,12,16,24,32").split(",")]
TRIALS = int(os.environ.get("TRIALS", "3"))
DURATION = float(os.environ.get("DURATION", "10"))
REFILL_PAUSE = 4.0  # seconds between trials so each starts with a full pool


def execute(payload):
    """POST one task; return (status, allocation ms, end-to-end ms)."""
    req = urllib.request.Request(f"{GATEWAY}/execute", data=json.dumps({"payload": payload}).encode(),
                                 headers={"Content-Type": "application/json"}, method="POST")
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            body = json.load(resp)
        return body["status"], body["allocation_latency_ms"], (time.perf_counter() - start) * 1000
    except Exception:  # count it; one failure shouldn't stop the sweep
        return "ERROR", 0.0, (time.perf_counter() - start) * 1000


def trial(rate):
    """Open-loop load at `rate` req/s for DURATION seconds."""
    n = int(rate * DURATION)
    results, lock = [], threading.Lock()

    def run(i):
        r = execute(f"sweep {rate:g} {i}")
        with lock:
            results.append(r)

    with ThreadPoolExecutor(max_workers=128) as pool:
        t0 = time.perf_counter()
        for i in range(n):
            # Sleep until this request's scheduled start, not "after the last one".
            delay = t0 + i / rate - time.perf_counter()
            if delay > 0:
                time.sleep(delay)
            pool.submit(run, i)
    return results


def main():
    out = Path(sys.argv[1] if len(sys.argv) > 1 else "bench-results/sweep.csv")
    out.parent.mkdir(parents=True, exist_ok=True)
    execute("warmup")  # opens the gateway -> controller connection; not recorded
    time.sleep(REFILL_PAUSE)
    with out.open("w") as f:
        f.write("rate,trial,status,alloc_ms,total_ms\n")
        for rate in RATES:
            for t in range(1, TRIALS + 1):
                rows = trial(rate)
                for status, alloc, total in rows:
                    f.write(f"{rate:g},{t},{status},{alloc:.3f},{total:.3f}\n")
                warm = sum(s == "WARM_HIT" for s, _, _ in rows)
                errors = sum(s == "ERROR" for s, _, _ in rows)
                print(f"rate {rate:>4g}/s trial {t}: {len(rows)} requests, "
                      f"{100 * warm / len(rows):5.1f}% warm, {errors} errors", flush=True)
                time.sleep(REFILL_PAUSE)
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
