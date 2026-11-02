#!/usr/bin/env python3
"""Turn results.csv into the only table that matters: did the rule agree with the scheduler."""
import csv
import sys
from pathlib import Path

path = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).with_name("results.csv"))
rows = list(csv.DictReader(path.open()))

tp = fp = fn = tn = 0
exact = 0
print(f"{'case':26} {'pred':>5} {'obs':>4}  verdict")
print("-" * 64)
for r in rows:
    if r.get("predicted") in ("ERROR", "na", None):
        print(f"{r.get('policy', '?'):26} {'-':>5} {'-':>4}  not measured")
        continue
    pred, obs = int(r["predicted"]), int(r["observed"])
    if pred > 0 and obs > 0:
        verdict = "agreed" if pred == obs else f"agreed on yes, count off ({pred} vs {obs})"
        tp += 1
        if pred == obs:
            exact += 1
    elif pred == 0 and obs == 0:
        verdict = "agreed"
        tn += 1
    elif pred > 0:
        verdict = "FALSE POSITIVE (writes nobody needed)"
        fp += 1
    else:
        verdict = "FALSE NEGATIVE (a death we did not see)"
        fn += 1
    label = f"{r['policy']}/{r['cpu']}"
    print(f"{label:26} {pred:5} {obs:4}  {verdict}")

total = tp + fp + fn + tn
print("-" * 64)
print(f"cases {total} · agreed {tp + tn}/{total} · false positives {fp} · false negatives {fn}")
if tp:
    print(f"where it fired, the count was exact {exact}/{tp}")
print()
print("        observed: yes   no")
print(f"predict yes |  {tp:3}  {fp:3}")
print(f"predict  no |  {fn:3}  {tn:3}")

if fn:
    print("\nfalse negatives: a unit died with nothing on disk, and no saved write pays that back")
    sys.exit(1)
