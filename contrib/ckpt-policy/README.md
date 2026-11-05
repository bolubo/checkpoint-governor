# ckpt-policy

Version `0.27.0` — the same number as the release tag (decision 0029).

The training loop's half of the contract: one install, three lines, no runtime dependencies, and the
shards stay with whatever library you already use.

```bash
pip install ckpt-policy        # manylinux x86_64; the engine is inside the wheel
```

From a checkout, the same wheel:

```bash
make wheel                     # from the repository root: the wheel carries the Go engine
python3 -m pip install dist/wheels/ckpt_policy-*.whl
```

```python
from ckpt_policy import Governor

g = Governor("/ckpt/job-1", start_engine=True)   # torchrun's RANK/WORLD_SIZE are read

# ... load the latest checkpoint, then say what you adopted (one call)
g.resumed(gen=7, step=512, seconds=load_seconds)

for step, batch in loader:
    g.step(step)
    if g.should_save():               # the plan asked; the whole write side, one line
        gen = g.generation()          # write into gen-<gen>/, with your library
        g.sealed(gen, step, [f"rank{r}.pt" for r in range(g.world.ranks)])
```

`start_engine=True` starts the engine for that directory on rank 0; `close()` (or `with Governor(...)`)
stops it. Several ranks: `docs/multi-rank.md`, and the runnable `examples/two_ranks.py`.
If the engine dies, the plan ages out and `should_save()` answers false — the caller's fallback is the
end-of-training write and the announced stop. The decision stays in Go; this package does not
re-implement it.

To see the two arms without Go on PATH:

```bash
python -m ckpt_policy.demo
```

## What it writes, and what it never writes

| we write | you write |
|---|---|
| `step`, `step-rate`, `stopping`, `seconds-left`, `ending` | the shards of a checkpoint |
| `manifest.json` (sizes and hashes of your shards) | — |
| `resume.json` (what a restore would get) | — |
| `resumed-generation`, `resumed-step`, `load-seconds` | — |

A shard is never written, repaired or deleted by this package: `governed.sealed()` only reads the files
your `save()` left and records what they claim (decision 0025).

## `resumed()` is the one call people forget

Without it, a job that came back and started at step 0 looks exactly like a job that resumed, and the
`ckptctl resumed` verdict can only say `not adopted: step 3, newest sealed 512`. With it, the receipt
says `adopted gen 7 (step 512), load 9.1s` (decision 0026).

## Tests

Standard library only:

```bash
python3 -m unittest discover -s contrib/ckpt-policy/tests -q
```

`torch` appears only in `examples/`, because that example has to be a real training loop to be worth
reading.
