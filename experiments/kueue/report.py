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
print("-" * 58)
for r in rows:
    if r["predicted"] in ("ERROR", "na"):
        continue
    pred, obs = int(r["predicted"]), int(r["observed"])
    if pred > 0 and obs > 0:
        verdict = "agreed"
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
    label = f"{r['policy']}/gap{r['gap']}/{r['cpu']}"
    print(f"{label:26} {pred:5} {obs:4}  {verdict}")

total = tp + fp + fn + tn
print("-" * 58)
print(f"cases {total} · agreed {tp + tn}/{total} · false positives {fp} · false negatives {fn}")
if tp:
    print(f"where it fired, the count was right {exact}/{tp} times")
print()
print("        observed: yes   no")
print(f"predict yes |  {tp:3}  {fp:3}")
print(f"predict  no |  {fn:3}  {tn:3}")

# The rule is only worth keeping if a wrong answer costs a write rather than a death: a false negative
# is a unit that died with nothing on disk, which no amount of saved writes pays for.
if fn:
    print("\nfalse negatives exist: the rule is unsafe until they are explained")
    sys.exit(1)
