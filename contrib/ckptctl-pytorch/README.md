# ckptctl-pytorch

The training loop's half of the contract. Three lines, no runtime dependencies, and the shards stay with
whatever library you already use.

```python
from ckptctl_pytorch import Governor, World, Saves

g = Governor("/ckpt/job-1", world=World(ranks=8), saves=Saves())

# ... load the latest checkpoint, then say what you adopted (one call)
g.resumed(gen=7, step=512, seconds=load_seconds)

for step, batch in loader:
    g.step(step)
    if g.requested():
        gen = g.generation()          # write into gen-<gen>/, with your library
        g.sealed(gen, step, ["rank-00.bin", "..."])
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
python3 -m unittest discover -s contrib/ckptctl-pytorch/tests -q
```

`torch` appears only in `examples/`, because that example has to be a real training loop to be worth
reading.
