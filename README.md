# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)

One process watches a training job's checkpoint directory and decides when a checkpoint is written.
It writes when a condition says the work is at risk, or when a window you set is about to close; it
prints one line per decision and a receipt when the run ends.

Status: prototype. Every number in this file is printed by a test or an experiment in this
repository: `make verify` runs the fast ones, `make demo` runs the product end to end.

## Quick start

```bash
docker build -t governor .
docker run --rm -v /ckpt:/ckpt governor run --dir /ckpt
```

With no configuration the behaviour is fixed: one write when training ends, and one more if the
platform announces that the job is about to go.

```
watching /ckpt every 5s (inflight 200ms, no-us none, window off)
governor  one write: 1.2s (2.0 GB at 1.7 GB/s)
governor  nothing configured: one write at the end, and one if the platform announces a stop.
16:30:22  skip  calm               no condition says the work is at risk
```

At the end of the run:

```
run 3s · no writes
  nothing said the work was at risk
  a silent death costs everything: no window was set
```

With a Go toolchain, `make install` puts `ckptctl` and `ckpttrainer` on `PATH` instead of using the
image.

## Choosing a window

```
$ ckptctl init --dir /ckpt --duration 4h
one write: 1.1s (2.0 GB at 1.8 GB/s)

  none       nothing            0 writes                   a silent death costs everything
  middle     one at the middle  1 write, about 1.0s        a silent death costs at most 2h
  periodic   every 30m          8 writes, about 9.0s       a silent death costs at most 30m
```

The answer is written to `.ckpt-governor/numbers.json` inside the checkpoint directory, so it travels
with the data. A flag overrides it for one run; `none` writes no file.

## Output

```
14:41:03  save  risk.preempt  higher-priority or more urgent work can take these resources
14:42:11  skip  no-time       a write needs 40s, 12s left
```

Every decision is printed with its reason, `skip` included. Rounds that only observed print nothing.

## The two inputs

| Input | Meaning | Required |
|---|---|---|
| `no-us` | what the job would have done without this process: `none`, or a period | no — unset means `none` |
| `with-us` | how stale the newest checkpoint may become while the process is alive | no — unset means exceptions only |

A write has two causes while the process is alive: a condition is `1`, or the window is closing.
Training end is written once, always. The contract is `docs/decisions/0017-two-numbers.md`.

## Failure behaviour

| Event | What happens |
|---|---|
| the process stops | the job falls back to `no-us`, or writes nothing when that answer is `none` |
| the job is preempted or evicted | one write before the announcement deadline, if a write fits |
| the volume fills | writes are refused and the refusal is printed; the volume is not filled |
| a write is interrupted | the copy is reported broken and a repair write is requested |

## What it does not do

- It is not monitoring software: it keeps no metrics and does not track training progress.
- It is not a scheduler: it does not choose jobs or preemptions.
- It does not select the checkpoint a job resumes from.
- It does not make training safe; it decides when to write and checks that the write is sound.

## How it works

1. The controller scans the checkpoint directory.
2. It reads the condition catalogue and the job's glue files: step, size, deadline.
3. It computes a decision — `save`, `repair`, `skip`, `infeasible`, `unknown` — with its reason.
4. It writes `.ckpt-governor/state.json` in that directory.
5. The job reads that file at its save point and saves, holds, or keeps its own habit.

Integration is a file read: no library, no API, no new permission.

## Build and check

```bash
make check    # format, vet, build, tests
make verify   # check, the contract pins and the recorded decisions
make exp      # the experiments: decisions in milliseconds, writes in seconds
make demo     # three acts, ending with the receipt
make install  # ckptctl and ckpttrainer on PATH
```

Requirements: Docker, or Go 1.26. The product and the demo need nothing else; the experiments also
use `python3` for `experiments/rig/verify.py`.

## Documentation

- `docs/product-v1.md` — the surface: what is asked, what is shown, what is claimed.
- `docs/decisions/0017-two-numbers.md` — the contract, in one page.
- `docs/design-backstop.md` — why the product has this shape.
- `docs/design-v1.md` — the engineering design, and what each part must print.
- `docs/policy.md` and `docs/conditions.md` — the rule and the 14 conditions.
- `docs/glue.md` — the files the job and the controller exchange.
- `docs/write-cost.md` — how the write duration is measured.
- `docs/experiments-matrix.md` — every claim and the experiment that checks it.
- `docs/b-class.md` — what a bound costs, and the certificate that it held.

The decision records in `docs/decisions/` are the authoritative history of what changed and why.
These pages are superseded and kept for provenance only: `docs/design.md` and
`docs/design-delivery.md`.

## Repository layout

```
cmd/ckptctl/         the controller and its inspectors: run, init, status, price, audit, explain,
                     doctor, events, glue, config, supervise, world, simulate, table, trace
cmd/ckpttrainer/     a demo job that writes the glue files and asks for a checkpoint
internal/policy/     the algorithm: a pure function from conditions to a decision
internal/exec/       the executor: attempts, the gate, the deadline, the window
internal/loop/       one round: measure, decide, publish, remember
internal/produce/    the samplers and the condition adapters
internal/bound/      pricing: what a bound costs on one mount
internal/audit/      the bound certificate: whether it was held
experiments/rig/     the experiments: 10 decision cases, 3 write-protocol scenarios
experiments/demo/    the three-act demo and the acceptance scenarios
docs/                the product, the design, the conditions, the decision records
```

## License

Apache License 2.0. See `LICENSE`.
