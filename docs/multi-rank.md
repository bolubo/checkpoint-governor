# Several ranks: who starts the engine, where the shards go, who seals

A multi-rank job has one plan file and one engine, but every rank has to say where it is. The client
reads `RANK` and `WORLD_SIZE`, so a `torchrun` job needs no explicit topology:

```python
from ckpt_policy import Governor

g = Governor(job_dir, start_engine=True)   # only rank 0 starts the engine
for step in ...:
    g.step(step)
```

Rank 0 owns the engine and every team-level file. A non-leader that asks for an engine does not get a
second one; two controllers over one plan would see whichever wrote last. An explicit `world=` or
`rank=` still wins when a launcher uses other variable names.

## Where the shards go

A save is the job's own code writing into the generation directory the plan named:

```
gen-0007/
  rank0.pt          the shards, written by each rank
  rank1.pt
  resume.json       written by rank 0: what a restore would get
  manifest.json     written by rank 0 after every shard exists
```

The file name must start with `rank<N>` (`rank0.pt`, `rank-00.bin`, `rank3-model.bin`). The manifest
records the member from the name; that is what the completeness check uses. A name without it belongs
to whoever wrote it, and no rank check is possible.

## Who seals, and when

`g.sealed(gen, step, shards)` is rank 0's call, after a barrier that proves every rank's shard is
written. The manifest is sealed last on purpose: a generation without a manifest is a write in flight,
not a copy.

```python
gen = g.generation()
gen_dir = f"{job_dir}/gen-{gen:04d}"
save_my_shard(gen_dir, rank)            # your library, your format
dist.barrier()
if rank == 0:
    g.sealed(gen, step, [f"rank{r}.pt" for r in range(world)])
```

At normal end, every rank writes its last shard, then rank 0 seals and records `ending()`. A job whose
last shard is missing is not a copy, however complete the declaration looks.

## What `resumable` checks

For a multi-rank copy it checks two things, not one:

- the declaration's world (`world 2/2`), and
- every rank the manifest marks has its shard on the volume.

A manifest that names `rank1.pt` while the file is gone answers `not resumable: rank 1 shard missing`,
even if the declaration and the other shards agree.

## Ready-made glue

`contrib/ckpt-policy/examples/two_ranks.py` is the complete, runnable version of this page: file
rendezvous for a host whose loopback store is unavailable, the auto-detected rank and world, per-rank
shards, a barrier, and rank 0's seal. `torchrun --nproc_per_node=2` exports the same variables and
uses the same script where its rendezvous works.

## What this page does not claim

- Model/gradient sync is the framework's (DDP/FSDP); this page is about the checkpoint side.
- A rank that dies before the barrier leaves an unsealed generation: it is a write in flight, and the
  next attempt is a new one. That is the intended reading, not a repair.

## Every rank must see the same root

This is a premise, not a recommendation. A save is one generation directory, and rank 0 seals a
manifest that names every rank's shard; if the ranks do not write into the same directory, the manifest
describes files rank 1 cannot read and no seal makes the copy whole.

- One RWX volume (or one shared filesystem) mounted at the same path in every rank and in the
  controller.
- A per-node `hostPath` is a single-pod promise. `ckptctl doctor --storage <dir>` says what the volume
  itself promises ([storage.md](storage.md)); the gate refuses a multi-pod victim on a per-node volume
  ([gate.md](gate.md)).
