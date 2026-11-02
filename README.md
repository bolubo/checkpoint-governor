# checkpoint-governor

[![ci](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml/badge.svg)](https://github.com/bolubo/checkpoint-governor/actions/workflows/ci.yml)

**A write policy for training runs.** It decides when a checkpoint is worth writing, and proves what a
loss would cost. One Go binary, one plan file, no CRD, no API, no new permission: the job reads a file.

A run with nothing configured writes once at the end, and once more if the platform announces the job
is about to go. That is the point: our default is **zero periodic writes**, and the frameworks' defaults
are not.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
ckptctl run --dir=/ckpt/job-1 --once
```

`install.sh` verifies the release's checksum before it installs anything. With Go, `make install` puts
the same two binaries on your PATH. Or in a container:

```bash
docker build -t ckpt-governor:local .
docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt
```

The three shapes — binary, container, cluster manifest — are `docs/install.md`. In Python, `make wheel`
builds a wheel with the engine inside; the three lines and the fallback are `docs/install-python.md`.

## What the default costs

```
500 steps (framework default)   28 writes   1120s   ≤500 steps   ≤500 steps
nothing installed                0 writes      0s   everything   everything
governor, unconfigured           1 write      40s   0            everything
2h bound                         2 writes     80s   0            ≤2h
```

Four policies on one four-hour job, printed by `make contrast`: the same steps, the same disk, the same
write cost. Each row names its writes, its io and **what it covers** — a count without coverage is not
a claim. The framework's cadence writes 28 times; the job that never configures anything writes once and
says what that still costs.

What the frameworks' actual defaults cost at four scales, with their real intervals and retention, is
`make waste` and `docs/waste.md`.

## On a cluster

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor     # watching 2 units · 0 blind
```

One process over a checkpoint volume; a child directory is a unit when it holds `.ckpt-governor/`.
Volume, tag and limits are in `deploy/README.md`; manifests to copy are in `examples/`.

## Choosing a window

```
  none       nothing            0 writes                   a silent death costs everything
  middle     one at the middle  1 write, about 1.0s        a silent death costs at most 2h
  periodic   every 30m          8 writes, about 9.0s       a silent death costs at most 30m
```

`ckptctl init --duration 4h` prices the three and writes the answer to `.ckpt-governor/numbers.json`
inside the checkpoint directory, so it travels with the data. A flag overrides it for one run; `none`
writes no file.

## Output

```
14:41:03  save  risk.preempt  higher-priority or more urgent work can take these resources
14:42:11  skip  no-time       a write needs 40s, 12s left
```

Every decision is printed with its reason, `skip` included. At the end of a run:

```
run 3s · no writes
  nothing said the work was at risk
  a silent death costs everything: no window was set
```

## What the checkpoint can tell you

Writing is half the job. The other half is whether the newest copy can continue the run:

```bash
ckptctl resumable --dir=/ckpt/job-1    # resumable (gen 512, step 512, world 8/8, full state)
ckptctl resumable --dir=/ckpt/job-2    # not resumable: optimizer state absent
ckptctl resumed   --dir=/ckpt/job-1    # what the job came back from, against the seal it saw at boot
ckptctl drill     --dir=/ckpt/job-1 --loader="./load.py"   # resume: ok — step 512 → 513; 0.4s
```

Exit codes are the contract: `0` yes, `1` checked and no, `2` cannot tell or refused. A drill is the only
proof of `restorable`: it fingerprints the tree, runs the loader for exactly one step, and refuses if the
checkpoint changed. The declaration is `resume.json` (schema 1) beside the manifest, and
`contrib/ckpt-policy` writes it in three lines with no runtime dependency. The whole contract is
`docs/resume-contract.md`.

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

- It is not monitoring software. `ckptctl serve` exports the numbers a scraper needs — staleness,
  over-bound time, blind conditions, writes by kind — and it decides nothing; `--webhook` posts one
  event per changed plan. Neither of them tracks progress.
- It is not a scheduler: it does not choose jobs or preemptions.
- It does not select the checkpoint a job resumes from; it says whether the newest one can continue,
  whether the job adopted it, and proves one step of it.
- It does not make training safe; it decides when to write and checks that the write is sound.

## How it works

It scans the directory and the job's glue files, decides — `save`, `repair`, `skip`, `infeasible`,
`unknown` — and writes `.ckpt-governor/state.json`, which the job reads at its save point. Integration
is a file read: no library, no API, no new permission, and no custom resource
(`docs/decisions/0019-deployment-without-crd.md`).

## Build and check

```bash
make fast     # the loop CI runs for a code change: format check, vet, build, tests, docs
make docs     # the docs pins on their own: dead links, comment citations
make ci       # the full gate: fast, e2e, exp, demo, install, spot, resume, py, drill
make verify   # the release-grade gate: ci plus parity and the four pinned tests
make exp      # the experiments: decisions in milliseconds, writes in seconds
make dist     # linux archives and SHA256SUMS; make release for the image
```

The gate classifies the diff and skips what it cannot justify: a Markdown change runs the docs pins only,
a Go change runs `make fast`, a behaviour change runs everything, and a tag is `release.yml`'s job, not
this one. What was red before, why, and the measured numbers are `docs/ci.md`.

Requirements: Docker, or Go 1.26. The product and the demo need nothing else; the experiments also
use `python3`.

## Documentation

Thirty pages, and one directory of decisions. Start here, then read only what your question needs.

- `docs/ci.md` — the gate: what was red, what runs for which change, and the rules that keep it deterministic.

- `docs/product-v1.md` — the surface: what is asked, shown and claimed; the second user and their
  price/certificate; why it did not write; what one write costs.
- `docs/product-v0.2.md` — the current release: the three verbs, the declaration, the drill, and what it
  refuses.
- `docs/product-v0.15.md` — the previous release: one-line install, the compatibility promise, the proof.
- `docs/install-python.md` — the wheel, the three lines, what should appear, and what to do when the
  engine is missing.
- `docs/huggingface.md` — the Trainer integration: the diff, the four events, and the two caveats it
  states rather than hides.
- `docs/lightning.md` — the Lightning integration: one batch of latency, because its save is public.
- `docs/composer.md` — the Composer integration: and what `CheckpointSaver` used to provide that it no
  longer does.
- `docs/references.md` — what fifteen neighbouring projects actually do about "when to save", read from
  their source, and what that says about who the customer is.
- `docs/plan-library.md` — the current plan: the contrast, the Python front door, and the three
  framework integrations.
- `docs/waste.md` — what the frameworks' default intervals cost, per scale, with the formula and what
  the model does not claim.
- `docs/resume-contract.md` — the schema, the two verdicts, the receipt, and the drill that is the only
  proof of `restorable`.
- `docs/verify-and-demand.md` — the other half of v0.2: the request file, the four check tiers, and why a
  copy is never called durable without a proof.
- `docs/design.md` — one document in two parts: the argument (Appendix A) and the build (§1–§14).
- `docs/policy.md` — the algorithm: the rule, the decision, the state file, what was deleted and why.
- `docs/conditions.md` — the 14 condition cards, their probe cadences and the detection bound, and how
  a deployment adds one.
- `docs/glue.md` — the files the job writes and the controller reads.
- `docs/experiments-matrix.md` — every claim and the experiment that checks it.
- `docs/first-edition-parity.md` — what the last batch added, and why `make parity` exists.
- `docs/language-boundary.md` — which layer may import which, and the tests that keep it.

`docs/decisions/README.md` indexes the records that are still load-bearing; `docs/decisions/history.md`
carries one paragraph each for the ones that are not, with their full text in git history.

`RELEASE.md` says how a tag becomes a release.

## License

Apache License 2.0. See `LICENSE`.
