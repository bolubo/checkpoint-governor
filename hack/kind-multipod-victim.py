"""The torch-free victim for hack/kind-multipod-gate.sh.

Two pods of one indexed Job write one shard each into one shared directory, meet at a file barrier,
and rank 0 seals a manifest that names both. The victim saves when it sees the gate's hold — the
behaviour the gate exists to buy time for — and then parks, so the harness can compare "the step at
death" with "the step in the seal" without the answer depending on how fast the scheduler acts.

It is deliberately not a training loop: this harness tests the gate's wait and release, not a
framework's gradient sync. The client is the real `ckpt_policy` package.
"""

import json
import os
import sys
import time

sys.path.insert(0, "/opt")

from ckpt_policy import Governor, World  # noqa: E402

RANK = int(os.environ.get("JOB_COMPLETION_INDEX", "0"))
WORLD = int(os.environ.get("WORLD_SIZE", "2"))
JOB = "/ckpt/victim"
WORLD_DIR = os.environ.get("WORLD_DIR", "/world")
CLUSTER = os.environ.get("CLUSTER", "a")
WORKLOAD = "default/victim"
UNIT = f"{os.environ['NAMESPACE']}/{os.environ['POD_NAME']}"

g = Governor(
    JOB,
    world=World(ranks=WORLD),
    rank=RANK,
    rank0=(RANK == 0),
    storage="shared",
    start_engine=(RANK == 0),
    engine_args=[f"--unit={UNIT}", "--no-us=none", "--every=1s", "--window=5s"],
)


def held() -> bool:
    """Whether the gate is holding the arrival for this workload.

    The victim reads the world directly because the harness has no controller-side condition layer;
    a real deployment asks its own `ckptctl run`, which reads the same record through the unit's
    `workload` field.
    """
    directory = os.path.join(WORLD_DIR, CLUSTER, "reclaims")
    try:
        names = os.listdir(directory)
    except OSError:
        return False
    for name in names:
        try:
            with open(os.path.join(directory, name), encoding="utf-8") as f:
                rec = json.load(f)
        except (OSError, ValueError):
            continue
        if rec.get("state") != "held":
            continue
        if rec.get("unitId") == WORKLOAD or UNIT in (rec.get("pods") or []):
            return True
    return False


def barrier(gen: int) -> bool:
    path = os.path.join(JOB, f".barrier-{gen}-{RANK}")
    with open(path, "w", encoding="utf-8") as f:
        f.write("1")
    other = os.path.join(JOB, f".barrier-{gen}-{(RANK + 1) % WORLD}")
    for _ in range(200):
        if os.path.exists(other):
            return True
        time.sleep(0.05)
    return False


def save(gen: int, step: int) -> None:
    gen_dir = os.path.join(JOB, f"gen-{gen:04d}")
    os.makedirs(gen_dir, exist_ok=True)
    with open(os.path.join(gen_dir, f"rank{RANK}.pt"), "wb") as f:
        f.write(f"rank {RANK} step {step}".encode())
    if not barrier(gen):
        raise SystemExit(f"rank {RANK}: the other rank never reached the barrier")
    if RANK == 0:
        g.sealed(gen, step, [f"rank{r}.pt" for r in range(WORLD)])
    for r in range(WORLD):
        try:
            os.remove(os.path.join(JOB, f".barrier-{gen}-{r}"))
        except OSError:
            pass


def main() -> None:
    step = 0
    sealed = False
    while True:
        if sealed:
            # Parked on purpose: the platform will preempt us now, and the harness measures what the
            # gate bought (the copy holds the step we reached), not how fast the eviction is.
            time.sleep(1)
            continue
        step += 1
        g.step(step)
        if held():
            save(1, step)
            sealed = True
            if RANK == 0:
                g.ending()
                with open(os.path.join(JOB, "sealed-step"), "w", encoding="utf-8") as f:
                    f.write(str(step))
        time.sleep(0.05)


if __name__ == "__main__":
    main()
