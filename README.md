# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)

A checkpoint policy for training runs: it decides when a write is worth doing, and proves what a loss
would cost. One Go binary, one plan file, no CRD, no API, no new permission.

## The default is the problem

```
500 steps (framework default)   28 writes   1120s   ≤500 steps   ≤500 steps
nothing installed                0 writes      0s   everything   everything
governor, unconfigured           1 write      40s   0            everything
2h bound                         2 writes     80s   0            ≤2h
```

Four policies on one four-hour job, printed by `make contrast`: the same steps, the same disk, the same
write cost. Each row names its writes, its io and **what it covers** — a count without coverage is not a
claim. The framework's cadence writes 28 times; a job that configures nothing writes once, and says what
that still costs.

What the frameworks' actual defaults cost at four scales is `make waste`, explained in `docs/waste.md`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
ckptctl run --dir=/ckpt/job-1 --once
```

`install.sh` verifies the release's checksum before it installs anything. With Go, `make install` puts
the two binaries on your PATH. In a container:

```bash
docker build -t ckpt-governor:local .
docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt
```

In Python, `make wheel` builds `ckpt-policy` with the engine inside:
`pip install dist/wheels/ckpt_policy-*.whl`, three lines, and what to do when it fails are
`docs/install-python.md`.

## In a training loop

```python
from ckpt_policy import Governor

g = Governor("/ckpt/job-1", start_engine=True)
for step, batch in loader:
    g.step(step)
    if g.should_save():            # the plan asked; the whole write side, one line
        save(g.generation())       # your library writes the shards
```

Integrations for Hugging Face Trainer, Lightning and Composer are one callback each:
`docs/huggingface.md`, `docs/lightning.md`, `docs/composer.md`.

## The two inputs

| input | meaning | required |
|---|---|---|
| `no-us` | what the job would do without us: `none`, or a period | no — unset means `none` |
| `with-us` | how stale the newest checkpoint may become while we are alive | no — unset means exceptions only |

A write has two causes while the process is alive: a condition is `1`, or the window is closing. Training
end is written once, always. The contract is decision 0017.

## On a cluster

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor     # watching 2 units · 0 blind
```

One process over a checkpoint volume; a child directory is a unit when it holds `.ckpt-governor/`.
Volume, tag and limits are in `deploy/README.md`.

## What it does not do

- It is not monitoring software: `ckptctl serve` exports numbers and decides nothing.
- It is not a scheduler, and it does not pick the checkpoint a job resumes from — it says whether the
  newest one can continue, and proves one step of it.
- It does not promise the machine, the network, or a silent death between writes. It decides when to
  write and checks that the write is sound.

## Documentation

Start with the plan (`docs/plan-library.md`), the cost model (`docs/waste.md`), the resume contract
(`docs/resume-contract.md`), and the integration template (`docs/integration-template.md`).
Every page is indexed in `docs/index.md`; decisions are `docs/decisions/README.md`; a tag becomes a
release as `RELEASE.md` says.

## Community

- Questions and ideas: [Discussions](https://github.com/bolubo/checkpoint-governor/discussions).
- Bugs and features: the issue templates; `good first issue` is the entry point.
- Before a pull request: `CONTRIBUTING.md` and the pull request template.
- How decisions are made: `GOVERNANCE.md`; the records are numbered in `docs/decisions/`.
- Code of conduct: `CODE_OF_CONDUCT.md`. Security reports: `SECURITY.md`, privately.

## License

Apache License 2.0. See `LICENSE`.
