# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/bolubo/checkpoint-governor?sort=semver)](https://github.com/bolubo/checkpoint-governor/releases/latest)
[![pypi](https://img.shields.io/pypi/v/ckpt-policy)](https://pypi.org/project/ckpt-policy/)
[![license](https://img.shields.io/github/license/bolubo/checkpoint-governor)](LICENSE)

Current release: [`v0.25.0`](docs/release-0.25.md) · [changelog](CHANGELOG.md). The Python package
carries the same number.

A safety net for training jobs. It watches the conditions that come before a lost job, asks the job to
write a checkpoint when one fires, and tells you whether the newest copy can continue after a restart —
whichever library wrote it. One static Go binary, one plan file; on Kubernetes, no custom resource and
no extra permission.

## The default is the problem

```
policy                          writes      io   worst case, no signal
500 steps (framework default)       28    1120s   ≤500 steps
nothing installed                    0       0s   everything
governor, unconfigured               1      40s   everything
2h bound                             2      80s   ≤2h
```

Four policies on one four-hour job (`make contrast`, explained in
[docs/contrast.md](docs/contrast.md)): same steps, same disk, same write cost. The job has **no
signals**, which is the last column: a job that configures nothing writes once, at the end, and a
silent death costs everything since the last write.

A stop is not silent: `SIGTERM`, an eviction, a drain, a full volume and a memory limit are named
conditions (`docs/conditions.md`); an urgent one asks for a write. The time a platform must allow for
that write is measured in `docs/operator-notes.md`.

What the framework defaults cost at four scales: `make waste` (`docs/waste.md`).

## Any checkpoint layout

Your job probably already writes checkpoints its own way: Hugging Face's `checkpoint-<step>/`,
Lightning's `last.ckpt`, Composer's epoch checkpoints. The governor reads the newest copy through one
finder and judges it by the declaration beside it — a small file the adapters write on every save, or
one command for copies that existed before the integration:

```bash
$ ckptctl resumable --dir=/ckpt/job-1
resumable (step 1000, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; …)

$ ckptctl declare --dir=/ckpt/job-1/checkpoint-1000 --step=1000
declared resume.json (step 1000, requires model, optimizer, scheduler, rng)
```

A copy nobody described is `unknown`, never "not resumable". The declaration, a `probe_cmd` in
`.ckpt-governor/layout.json`, or your own `glob` turns it into a known one. Recognised layouts:
`gen-N/`, `step-N/`, `checkpoint-N/`, `*.ckpt`, Composer's epoch checkpoints, and whatever your
`layout.json` names.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
ckptctl run --dir=/ckpt/job-1 --once
```

`install.sh` verifies the release's checksum before it installs anything. With Go, `make install` puts
the binaries on your PATH. In a container:

```bash
docker build -t ckpt-governor:local .
docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt
```

In Python, `pip install ckpt-policy` puts the client and the engine in one place (manylinux x86_64;
`make wheel` builds the same wheel). Three lines, and what to do when it fails: `docs/install-python.md`.

## In a training loop

```python
from ckpt_policy import Governor

g = Governor("/ckpt/job-1", start_engine=True)
for step, batch in loader:
    g.step(step)
    if g.should_save():            # the policy asked for a write
        save(g.generation())       # your library writes the shards
```

One callback each for Hugging Face Trainer, Lightning and Composer: `docs/huggingface.md`,
`docs/lightning.md`, `docs/composer.md`. Each writes a declaration beside every save, so
`ckptctl resumable` can answer for the framework's own checkpoint.

If your framework decides to exit on its own instead of waiting for `may-exit`, run the job under the
wrapper. It takes the signal, writes `stopping`, waits for a sealed copy (or the deadline), and only
then forwards:

```bash
$ ckptctl wrap --dir=/ckpt/job-1 --grace=60s -- python train.py
wrap: signal received, stopping written, waiting up to 1m0s
wrap: sealed at step 1000 (18.4s) → forwarding SIGTERM
```

A grace that is too short is said out loud (`final write NOT made`), never silent.

## Two numbers, both optional

| flag | meaning | required |
|---|---|---|
| `--no-us` | what the job would do without the governor: `none`, or a period | no — unset means `none`, and the first line of the run says so |
| `--with-us` | how stale the newest checkpoint may become while the governor is running | no — unset means exceptions only |

Unset `--with-us` means **no periodic writes**: the governor is silent until a condition says the work
is at risk. That is the posture the product exists for.

A write has two causes while the governor is running: a condition is `1`, or the staleness window
closes. Training end is always written once. Nothing is promised when no condition fires: an
unannounced death loses what was never written, and the status report names the gap.

## On a cluster

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor     # units watched, and how many it cannot read
```

One process over a checkpoint volume. Each directory under it that contains `.ckpt-governor/` is
watched as one job.

Volume, image tag and resource limits are in `deploy/README.md`.

A small team that submits higher-priority work through `contrib/gate` gets lead time instead of
notice: the arrival waits until the victim has sealed (`docs/gate.md`). Direct submissions keep the
announcement-only path.

## What it does not do

- It is not monitoring software: `ckptctl serve` exports numbers and decides nothing.
- It never writes your shards: your library writes; the governor asks, writes one declaration beside
  the copy, and checks what landed. It does retire copies it wrote itself when your retention rule
  says so — and it never removes a framework's checkpoint.
- It is not a scheduler, and it does not pick the checkpoint a job resumes from — it says whether the
  newest one can continue, and proves one step of it.
- It does not promise the machine, the network, or a death that announces nothing: a polling interval
  is not a completed write.

## Documentation

Start with `docs/install-python.md` (or `docs/install.md` for the binaries), the integration page for
your framework, and `docs/operator-notes.md` for the platform. Every page is in `docs/index.md`
(rendered at <https://bolubo.github.io/checkpoint-governor/>); decisions in `docs/decisions/`; a tag
releases as `RELEASE.md` says.

## Community

- Questions and ideas: [Discussions](https://github.com/bolubo/checkpoint-governor/discussions).
- Bugs and features: the issue templates; the
  [good first issues](https://github.com/bolubo/checkpoint-governor/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
  are the entry point.
- Before a pull request: `CONTRIBUTING.md` and its template.
- Decisions: `GOVERNANCE.md`, records in `docs/decisions/`. Conduct: `CODE_OF_CONDUCT.md`. Security
  reports: `SECURITY.md`, privately.

## License

Apache License 2.0. See `LICENSE`.
