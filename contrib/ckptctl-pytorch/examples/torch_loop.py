"""A real training loop, in the shape the adapter expects. Not run in CI (it needs torch); it exists so
the README has a loop that can actually be copied rather than a sketch.

The interesting parts are the three calls around the loop, not the training itself.
"""

import os
import time

import torch
from torch import nn

from ckptctl_pytorch import Governor, Saves, World

JOB = "/ckpt/job-1"


def save(gen: int, step: int, model: nn.Module, opt: torch.optim.Optimizer) -> list[str]:
    """Whatever library you already use. The adapter only needs the file names afterwards."""
    gen_dir = os.path.join(JOB, f"gen-{gen:04d}")
    os.makedirs(gen_dir, exist_ok=True)
    ranks = 1
    torch.save(model.state_dict(), os.path.join(gen_dir, "rank-00.bin"))
    torch.save(opt.state_dict(), os.path.join(gen_dir, "rank-00.optim.bin"))
    return [f"rank-{r:02d}.bin" for r in range(ranks)]


def main() -> None:
    model, opt = nn.Linear(8, 8), None
    opt = torch.optim.AdamW(model.parameters())

    # 1. Construct the governor before loading, so the load time is measurable.
    g = Governor(JOB, world=World(ranks=1, reshardable=False),
                 saves=Saves(model=True, optimizer=True, scheduler=True, rng=True,
                             dataloader=True))

    # 2. Load (your loader), then say what you adopted.
    started = time.monotonic()
    step = 0  # ... restore model/opt/step from the latest sealed generation
    g.resumed(gen=0, step=step, seconds=time.monotonic() - started)

    for step in range(step, 10_000):
        g.step(step)
        if g.requested():
            gen = g.generation()
            names = save(gen, step, model, opt)
            g.sealed(gen, step, names)

    g.ending()
    for _ in range(60):
        if g.may_exit():
            return
        time.sleep(0.5)


if __name__ == "__main__":
    main()
