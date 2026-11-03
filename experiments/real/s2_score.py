#!/usr/bin/env python3
"""Score one S2 trial from `job.json`, and say whether the copy is usable.

    python experiments/real/s2_score.py /root/tmp/s2/us-30s-5s-1

Prints one verdict line and appends the same record to `results/s2.jsonl`:

    us grace=30s every=5s trial=1  detected=2.1s sealed_at=+7.4s margin=+22.6s lost=0 resumable=0
"""

import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
RESULTS = os.path.join(HERE, "results")


def main() -> int:
    root = sys.argv[1]
    trial = sys.argv[2] if len(sys.argv) > 2 else "?"
    with open(os.path.join(root, "job.json")) as f:
        rec = json.load(f)

    saves = rec["saves"]
    signal_at = rec["t_signal"]
    after = [s for s in saves if signal_at is not None and s["t_end"] > signal_at]
    last = saves[-1]["step"] if saves else 0
    steps_at_signal = rec["steps_at_signal"] if rec["steps_at_signal"] is not None else rec["steps_done"]
    lost = max(0, steps_at_signal - last)

    detected = None
    margin = None
    sealed_at = None
    if signal_at is not None and rec["t_request_seen"] is not None:
        detected = rec["t_request_seen"] - signal_at
    if after:
        sealed_at = after[-1]["t_end"] - signal_at
        margin = rec["grace"] - sealed_at

    resumable = None
    engine = os.environ.get("CKPTCTL_ENGINE") or os.path.join(HERE, "bin", "ckptctl")
    if os.path.exists(engine):
        p = subprocess.run([engine, "resumable", f"--dir={root}"], capture_output=True, text=True)
        resumable = p.returncode

    transfer = None
    seal = None
    if after:
        s = after[-1]
        transfer = s["t_end"] - s["t_start"]
        seal = s.get("seal_s")

    row = {
        "arm": rec["arm"], "grace": rec["grace"], "every": rec["every"], "trial": trial,
        "detected_s": detected, "sealed_after_signal_s": sealed_at, "margin_s": margin,
        "write_s": transfer, "seal_s": seal,
        "lost_steps": lost, "writes_after_signal": len(after), "exit": rec["exit"],
        "resumable_exit": resumable,
        "save_interval": rec.get("save_interval"),
        "reason_at_request": rec.get("reason_at_request"),
        "may_exit": rec.get("may_exit"),
    }

    def fmt(v, unit="s"):
        return "-" if v is None else f"{v:+.{1}f}{unit}" if unit == "s" else f"{v}{unit}"

    verdict = "landed" if (margin is not None and margin > 0) else "late/missing"
    reason = (rec.get("reason_at_request") or {}).get("reason", "-")
    line = (
        f"{rec['arm']:10s} grace={rec['grace']:>2}s every={rec['every']:>3s} trial={trial:<3} "
        f"{verdict:12s} reason={reason:<13} detected={fmt(detected)} sealed={fmt(sealed_at)} "
        f"margin={fmt(margin)} lost={lost:<4} writes={len(after)} "
        f"write={fmt(transfer) if transfer is None else f'{transfer:.1f}s'} seal={fmt(seal) if seal is None else f'{seal:.1f}s'} "
        f"resumable={resumable}"
    )
    print(line)
    os.makedirs(RESULTS, exist_ok=True)
    with open(os.path.join(RESULTS, "s2.jsonl"), "a") as f:
        f.write(json.dumps(row) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
