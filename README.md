# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)

Current release: [`v0.21.0`](docs/release-0.21.md) · [changelog](CHANGELOG.md). The Python package
carries the same number.

A backstop for training runs: it watches the named conditions that come before a lost job, asks for a
write when one fires, and proves that the copy which landed can resume. One Go engine, one plan file,
no CRD, no API, no new permission.

## The default is the problem

```
policy                          writes      io   worst case, no signal
500 steps (framework default)       28    1120s   ≤500 steps
nothing installed                    0       0s   everything
governor, unconfigured               1      40s   everything
2h bound                             2      80s   ≤2h
```

Four policies on one four-hour job (`make contrast`): same steps, same disk, same write cost. The job
has **no signals**, which is the last column: a job that configures nothing writes once, at the end, and
a silent death costs everything since the last write.

A stop is not silent: `SIGTERM`, an eviction, a drain, a full volume and a memory limit are named
conditions (`docs/conditions.md`); an urgent one asks for a write. The budget is one probe interval, one
round and the write — that page prints it instead of a probability.

The frameworks' real defaults at four scales are `make waste` (`docs/waste.md`).

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
docker run --rm --tmpfs /tmp --entrypoint /contrast ckpt-governor:local    # the four rows above
```

In Python, `pip install ckpt-policy` puts the client and the engine in one place (manylinux x86_64;
`make wheel` builds the same wheel). Three lines, and what to do when it fails: `docs/install-python.md`.

## In a training loop

```python
from ckpt_policy import Governor

g = Governor("/ckpt/job-1", start_engine=True)
for step, batch in loader:
    g.step(step)
    if g.should_save():            # the plan asked; the whole write side, one line
        save(g.generation())       # your library writes the shards
```

One callback each for Hugging Face Trainer, Lightning and Composer: `docs/huggingface.md`,
`docs/lightning.md`, `docs/composer.md`.

## The two inputs

| input | meaning | required |
|---|---|---|
| `no-us` | what the job would do without us: `none`, or a period | **yes** — `none` is an answer; unset refuses to start |
| `with-us` | how stale the newest checkpoint may become while we are alive | no — unset means exceptions only |

Unset `with-us` means **no periodic writes**: the governor is silent until a condition says the work is
at risk. That is the posture the product exists for.

A write has two causes while we live: a condition is `1`, or the window closes. Training end is always
written once (decision 0017). Nothing is promised when no condition fires: a death that announces
nothing loses what was never written, and the status prints that hole.

## On a cluster

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor     # watching 2 units · 0 blind
```

One process over a checkpoint volume; a child directory is a unit when it holds `.ckpt-governor/`.
Volume, tag and limits are in `deploy/README.md`.

## What it does not do

- It is not monitoring software: `ckptctl serve` exports numbers and decides nothing.
- It never writes, repairs or deletes a checkpoint: your library writes; the governor asks and checks
  what landed.
- It is not a scheduler, and it does not pick the checkpoint a job resumes from — it says whether the
  newest one can continue, and proves one step of it.
- It does not promise the machine, the network, or a death that announces nothing: a polling interval
  is not a completed write.

## Documentation

Start with the plan (`docs/plan-library.md`), the cost model (`docs/waste.md`), the resume contract
(`docs/resume-contract.md`) and the integration template (`docs/integration-template.md`). Every page is
in `docs/index.md`; decisions in `docs/decisions/`; a tag releases as `RELEASE.md` says.

## Community

- Questions and ideas: [Discussions](https://github.com/bolubo/checkpoint-governor/discussions).
- Bugs and features: the issue templates; `good first issue` is the entry point.
- Before a pull request: `CONTRIBUTING.md` and its template.
- Decisions: `GOVERNANCE.md`, records in `docs/decisions/`. Conduct: `CODE_OF_CONDUCT.md`. Security
  reports: `SECURITY.md`, privately.

## License

Apache License 2.0. See `LICENSE`.
