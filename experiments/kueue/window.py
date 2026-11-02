#!/usr/bin/env python3
"""Measure how long a waiting arrival stays visible.

The one-decision-per-event rule identifies an event by the *suspended* jobs: a job the queue is holding
back. On a preemptible queue Kueue admits within a second, so the window in which that job is suspended
may be shorter than any decision cadence we could run — in which case the rule is not slow, it is blind.

This prints the arrangement's window in milliseconds so the answer is a number:

    python3 window.py --job arrival --for 20
"""
import argparse
import json
import subprocess
import sys
import time


def suspend_state(job):
    out = subprocess.run(
        ["kubectl", "get", "job", job, "-o", "json"],
        capture_output=True, text=True)
    if out.returncode != 0:
        return None
    try:
        spec = json.loads(out.stdout)["spec"]
    except Exception:
        return None
    return spec.get("suspend")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--job", default="arrival")
    ap.add_argument("--for", dest="secs", type=float, default=20.0)
    ap.add_argument("--interval", type=float, default=0.2)
    args = ap.parse_args()

    start = time.time()
    last = object()
    transitions = []
    samples = 0
    while time.time() - start < args.secs:
        state = suspend_state(args.job)
        samples += 1
        if state != last:
            transitions.append((time.time() - start, state))
            last = state
        time.sleep(args.interval)

    print(f"job {args.job}: {samples} samples over {args.secs:.0f}s at {args.interval*1000:.0f}ms")
    for at, state in transitions:
        label = "suspended (waiting)" if state else "admitted" if state is False else "absent"
        print(f"  {at*1000:7.0f}ms  {label}")

    suspended = [at for at, s in transitions if s is True]
    admitted = [at for at, s in transitions if s is False]
    if len(suspended) == 1 and admitted:
        window = admitted[0] - suspended[0]
        print(f"\nwaiting window: {window*1000:.0f}ms")
        if window < 1.0:
            print("shorter than one second: a five-second controller will usually miss it entirely")
    elif not suspended:
        print("\nthe job was never seen waiting — it was admitted before the first sample")


if __name__ == "__main__":
    sys.exit(main())
