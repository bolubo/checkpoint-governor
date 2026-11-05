#!/usr/bin/env python3
"""Two ranks, one checkpoint directory: the multi-rank glue, complete and copyable.

    rm -rf /tmp/ckpt-2r
    RANK=0 WORLD_SIZE=2 python3 examples/two_ranks.py /tmp/ckpt-2r 200 &
    RANK=1 WORLD_SIZE=2 python3 examples/two_ranks.py /tmp/ckpt-2r 200 &
    wait

`torchrun` exports the same RANK/WORLD_SIZE, so the Governor picks them up without being told. This
example deliberately does not use `torch.distributed`: gradient sync belongs to DDP/FSDP, while the
checkpoint protocol is the four calls below. The barrier is a file so the example runs on a machine
whose loopback transport is unavailable; a real job uses `dist.barrier()`.

The glue that is not in the framework: every rank reports its step, every rank writes its own shard,
rank 0 seals only after every shard exists, and rank 0 records the end.
"""

import os
import sys
import time

import torch
from torch import nn

from ckpt_policy import Governor  # noqa: F401 - the marked block

JOB = sys.argv[1] if len(sys.argv) > 1 else "/tmp/ckpt-2r"
STEPS = int(sys.argv[2]) if len(sys.argv) > 2 else 200
RANK = int(os.environ["RANK"])
WORLD = int(os.environ["WORLD_SIZE"])


class TinyModel(nn.Module):
    def __init__(self, vocab=16, dim=8):
        super().__init__()
        self.emb = nn.Embedding(vocab, dim)
        self.head = nn.Linear(dim, 1)

    def forward(self, ids):
        return self.head(self.emb(ids).mean(dim=1))


def batch(size=8, length=6, vocab=16):
    ids = torch.randint(0, vocab, (size, length))
    labels = torch.randint(0, 2, (size,)).float()
    return ids, labels


def barrier(tag: str) -> None:
    """Wait until every rank has written its tag. A real job uses dist.barrier() instead."""
    mine = os.path.join(JOB, f".barrier-{tag}-{RANK}")
    with open(mine, "w") as fh:
        fh.write("1")
    deadline = time.time() + 60
    while time.time() < deadline:
        if all(os.path.exists(os.path.join(JOB, f".barrier-{tag}-{r}")) for r in range(WORLD)):
            break
        time.sleep(0.05)
    else:
        raise TimeoutError(f"rank {RANK}: barrier {tag} waited 60s")
    if RANK == 0:
        for r in range(WORLD):
            try:
                os.remove(os.path.join(JOB, f".barrier-{tag}-{r}"))
            except OSError:
                pass


def save_shard(gen: int, step: int, model: nn.Module, opt: torch.optim.Optimizer) -> None:
    gen_dir = os.path.join(JOB, f"gen-{gen:04d}")
    os.makedirs(gen_dir, exist_ok=True)
    torch.save({"model": model.state_dict(), "optimizer": opt.state_dict()},
               os.path.join(gen_dir, f"rank{RANK}.pt"))


def main() -> int:
    torch.manual_seed(RANK)
    model = TinyModel()
    opt = torch.optim.SGD(model.parameters(), lr=0.01)
    loss_fn = nn.functional.binary_cross_entropy_with_logits

    # -- the marked block: no rank, no world, no engine flag; the launcher supplies them ---------
    g = Governor(JOB, start_engine=True)
    # -------------------------------------------------------------------------------------------
    try:
        started = time.time()
        for step in range(1, STEPS + 1):
            ids, labels = batch()
            loss = loss_fn(model(ids).squeeze(-1), labels)
            opt.zero_grad()
            loss.backward()
            opt.step()
            g.step(step)
            if g.requested():
                gen = g.generation()
                save_shard(gen, step, model, opt)
                barrier(f"save-{gen}")
                if RANK == 0:
                    g.sealed(gen, step, [f"rank{r}.pt" for r in range(WORLD)], loader="torch")
        gen = g.generation()
        save_shard(gen, STEPS, model, opt)
        barrier(f"save-{gen}")
        if RANK == 0:
            g.sealed(gen, STEPS, [f"rank{r}.pt" for r in range(WORLD)], loader="torch")
            g.ending()
        print(f"rank {RANK}/{WORLD}: {STEPS} steps in {time.time() - started:.1f}s")
        if RANK == 0:
            print(f"next: ckptctl resumable --dir={JOB}")
    finally:
        g.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
