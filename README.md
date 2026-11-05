# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/bolubo/checkpoint-governor?sort=semver)](https://github.com/bolubo/checkpoint-governor/releases/latest)
[![pypi](https://img.shields.io/pypi/v/ckpt-policy)](https://pypi.org/project/ckpt-policy/)
[![license](https://img.shields.io/github/license/bolubo/checkpoint-governor)](LICENSE)

Current release: [`v0.28.0`](docs/release-0.28.md) · [changelog](CHANGELOG.md). The Python package
carries the same number.

**Know whether your training run can continue — whichever library wrote the checkpoint.**

```bash
pip install ckpt-policy
ckptctl demo        # thirty seconds, no cluster, no GPU
```

```
demo /tmp/ckpt-demo: 2 ranks, gen-0001, step 300, storage local
fault: rank1.pt deleted — the kill landed between the shards and the manifest
status      newest sealed step 300 · writing none
resumable   not resumable: rank 1 shard missing
verify      broken: shard rank1.pt is missing
evidence    1 problem · storage local (process death)
next        ckptctl demo --repair --dir=/tmp/ckpt-demo
```

`ckptctl demo --repair` seals it again; the same two commands then answer `resumable (world 2/2)` and
`verified`. The demo writes a real generation and deletes a real shard, so the answers are the ones a
production copy gets ([docs/demo.md](docs/demo.md)).

Works with Hugging Face Trainer, Lightning, Composer and Kueue. On Kubernetes: no custom resource and
no extra permission. Ask it from Claude Code or Codex: `ckptctl setup --agent=claude --apply`
([docs/agents.md](docs/agents.md)).

## The default is the problem

```
policy                          writes      io   worst case, no signal
500 steps (framework default)       28    1120s   ≤500 steps
nothing installed                    0       0s   everything
governor, unconfigured               1      40s   everything
2h bound                             2      80s   ≤2h
```

Four policies on one four-hour job (`make contrast`; the article is [docs/contrast.md](docs/contrast.md)).
The job has **no signals**, which is the last column: a job that configures nothing writes once, at the
end, and an unannounced death costs everything since the last write.

A stop is not silent: `SIGTERM`, an eviction, a drain, a full volume and a memory limit are named
conditions ([docs/conditions.md](docs/conditions.md)); an urgent one asks for a write. What the platform
must allow for that write is measured in [docs/operator-notes.md](docs/operator-notes.md).

## Install

```bash
pip install ckpt-policy
ckptctl version
```

The wheel carries the client and the engine; `pip install` also puts `ckptctl` on the venv's PATH.
The first ten minutes, with the lines they print: [docs/first-ten-minutes.md](docs/first-ten-minutes.md).

Without Python, `install.sh` installs the binaries; a container runs the same engine:

```bash
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
docker build -t ckpt-governor:local .
docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt
```

`install.sh` verifies the release's checksum before it installs anything.

## In a training loop

```python
from ckpt_policy import Governor

g = Governor("/ckpt/job-1", start_engine=True)
for step, batch in loader:
    g.step(step)
    if g.should_save():            # the policy asked for a write
        save(g.generation())       # your library writes the shards
```

One callback each for Hugging Face Trainer, Lightning and Composer:
[docs/huggingface.md](docs/huggingface.md), [docs/lightning.md](docs/lightning.md),
[docs/composer.md](docs/composer.md). Each writes a declaration beside every save, so `ckptctl
resumable` can answer for the framework's own checkpoint. Several ranks:
[docs/multi-rank.md](docs/multi-rank.md), with a runnable
[example](contrib/ckpt-policy/examples/two_ranks.py).

If your framework exits on its own instead of waiting for `may-exit`, run the job under `ckptctl wrap`:
it takes the signal, writes `stopping`, waits for a sealed copy (or the deadline), and then forwards.
A grace that is too short is said out loud (`final write NOT made`), never silent.

## Any checkpoint layout

Your job probably already writes checkpoints its own way: Hugging Face's `checkpoint-<step>/`,
Lightning's `last.ckpt`, Composer's epoch checkpoints. The governor finds the newest copy through one
finder and judges it by the declaration beside it:

```bash
$ ckptctl resumable --dir=/ckpt/job-1
resumable (step 1000, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; …)
```

A copy nobody described is `unknown`, never "not resumable". A declaration, a `probe_cmd` in
`.ckpt-governor/layout.json`, or your own `glob` turns it into a known one. The rules are in
[docs/resume-contract.md](docs/resume-contract.md).

## Two numbers, both optional

| flag | meaning | required |
|---|---|---|
| `--no-us` | what the job would do without the governor: `none`, or a period | no — unset means `none` |
| `--with-us` | how stale the newest checkpoint may become while the governor is running | no — unset means exceptions only |

Unset `--with-us` means **no periodic writes**: the governor is silent until a condition says the work
is at risk. A write has two causes while it runs: a condition is `1`, or the staleness window closes.
Training end is always written once. Nothing is promised when no condition fires: an unannounced death
loses what was never written, and the status report names the gap.

## On a cluster

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor     # units watched, and how many it cannot read
```

One process over a checkpoint volume; each directory under it that contains `.ckpt-governor/` is watched
as one job. Volume, image tag and limits are in [deploy/README.md](deploy/README.md). A small team that
submits higher-priority work through `contrib/gate` gets lead time instead of notice: the arrival waits
until the victim has sealed ([docs/gate.md](docs/gate.md)).

## What it does not do

- It is not monitoring software: `ckptctl serve` exports numbers and decides nothing.
- It never writes your shards: your library writes; the governor asks, writes one declaration beside
  the copy, and checks what landed. It retires copies it wrote itself when your retention rule says so
  — and it never removes a framework's checkpoint.
- It is not a scheduler, and it does not pick the checkpoint a job resumes from — it says whether the
  newest one can continue, and proves one step of it.
- It does not promise the machine, the network, or a death that announces nothing: a polling interval
  is not a completed write.

## Documentation

- Start here: [docs/first-ten-minutes.md](docs/first-ten-minutes.md).
- Your framework: the integration pages above; your platform: [docs/operator-notes.md](docs/operator-notes.md).
- A message that needs explaining: [docs/troubleshooting.md](docs/troubleshooting.md).
- Every page, one question each: [docs/index.md](docs/index.md); rendered at
  <https://bolubo.github.io/checkpoint-governor/>. Decisions: `docs/decisions/`. A tag releases as
  `RELEASE.md` says.

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
