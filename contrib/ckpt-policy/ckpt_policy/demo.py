"""A synthetic loop, no torch, no Go on PATH: the framework's cadence against the policy.

    python -m ckpt_policy.demo

The point is the same as `make contrast`, at the smallest scale that still runs on any laptop: a job
writes a checkpoint every 500 steps because nothing told it not to, while the policy writes once at the
end and asks for nothing during training. The numbers are a smoke test, not the four-row model.
"""

import argparse
import json
import os
import sys
import tempfile
import time

from .governor import Governor, World, engine_path


class Row:
    def __init__(self, policy, writes, io, overhead=0.0, note=""):
        self.policy, self.writes, self.io, self.overhead, self.note = policy, writes, io, overhead, note


def run(steps=1000, save_every=500, write_ms=100, plan_timeout=3.0) -> list[Row]:
    """Two arms on the same synthetic job: a cadence, and the policy."""
    rows = []
    write_s = write_ms / 1000

    # Arm A: the framework default. A cadence writes on every multiple of `save_every`, whether or not
    # anything is at risk. It has no glue, so its overhead is zero by construction.
    writes = steps // save_every
    for _ in range(writes):
        time.sleep(write_s)
    rows.append(Row(f"framework default (save every {save_every} steps)", writes, writes * write_s))

    # Arm B: the engine, with nothing configured. It is asked at every step, and it asks for nothing
    # while nothing is at risk; the job's own final write is the one write. `overhead` is the client's
    # own per-step glue (one cell per rank, plus the leader's team cell), measured rather than hidden.
    root = tempfile.mkdtemp(prefix="ckpt-policy-demo-")
    g = Governor(root, world=World(ranks=1), start_engine=True, engine_every="100ms")
    try:
        plan_seen = _wait_for_plan(root, plan_timeout)
        wall_start = time.monotonic()
        writes = 0
        for step in range(1, steps + 1):
            g.step(step)
            if g.should_save():
                writes += 1
                time.sleep(write_s)
        g.ending()
        writes += 1  # the end of training is always written once
        time.sleep(write_s)
        wall = time.monotonic() - wall_start
        io = writes * write_s
        rows.append(Row("ckpt-policy, unconfigured", writes, io, max(0.0, wall - io),
                        "" if plan_seen else "no plan: the engine did not start"))
    finally:
        g.close()
    return rows


def _wait_for_plan(root, timeout):
    state = os.path.join(root, ".ckpt-governor", "state.json")
    deadline = time.time() + timeout
    while time.time() < deadline:
        if os.path.exists(state):
            return True
        time.sleep(0.05)
    return False


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="python -m ckpt_policy.demo")
    parser.add_argument("--steps", type=int, default=1000)
    parser.add_argument("--save-every", type=int, default=500)
    parser.add_argument("--write-ms", type=int, default=100)
    parser.add_argument("--json", action="store_true", help="print the two result rows as JSON")
    args = parser.parse_args(argv)

    if engine_path() is None:
        print("no engine: build the wheel with `make wheel`, or set CKPTCTL_ENGINE", file=sys.stderr)
        return 2

    rows = run(steps=args.steps, save_every=args.save_every, write_ms=args.write_ms)
    if args.json:
        json.dump([{"policy": r.policy, "writes": r.writes, "io": r.io, "overhead": r.overhead,
                    "note": r.note}
                   for r in rows], sys.stdout, indent=2)
        print()
        return 0

    print(f"a {args.steps}-step job, one write costs {args.write_ms}ms (real time, a smoke test)")
    print()
    print(f"{'policy':<42} {'writes':>7} {'io':>8} {'overhead':>10}")
    for r in rows:
        print(f"{r.policy:<42} {r.writes:>7} {r.io:>7.2f}s {r.overhead:>9.2f}s")
        if r.note:
            print(f"  {r.note}")
    print()
    print("overhead is the client's own glue (one cell per rank per step, plus the leader's team cell), "
          "not a checkpoint write")
    print("the plan asked for nothing during training; the only write is the end of the job")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
