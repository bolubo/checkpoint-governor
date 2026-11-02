# Install

Three shapes, one binary. Pick the one that matches where you are; nothing below needs us in the loop.

## 1. The binary (no Go, no container)

```bash
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
ckptctl version
```

`install.sh` detects `linux/amd64` or `linux/arm64`, downloads the release tarball, **verifies
`SHA256SUMS` before installing anything**, and puts `ckptctl` and `ckpttrainer` in
`${PREFIX:-$HOME/.local/bin}`. A checksum mismatch stops the install; there is no `--force`.

With Go, the same two binaries come from a checkout: `make install`, or
`go install ./cmd/ckptctl ./cmd/ckpttrainer`.

## 2. The container

```bash
docker build -t ckpt-governor:local .
docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt/job-1 --no-us=none
```

A released tag is `ghcr.io/bolubo/checkpoint-governor:<tag>`; the workflow publishes a multi-arch image
for every `v*` tag.

## 3. A node with many jobs

```bash
kubectl apply -f deploy/governor.yaml
kubectl logs -l app=checkpoint-governor      # watching N units · 0 blind
```

The manifest assumes a volume at `/ckpt`, a non-root user, and a read-only root filesystem; a child
directory is a unit when it holds `.ckpt-governor/`. Details and the two-cluster form are in
`deploy/README.md`.

## What a job writes, and what the governor writes

Every file lives in the checkpoint directory, so the job and the controller need nothing but the
volume they already share.

| File | Who writes it | When the controller reads it |
|---|---|---|
| `.ckpt-governor/step` | the job | every round (staleness, generation) |
| `.ckpt-governor/step-rate`, `step-baseline` | the job | the `job.step-slow` condition |
| `.ckpt-governor/ending` | the job | the end of training; one final write |
| `.ckpt-governor/stopping` | the job, on SIGTERM | a preemption; one write before the grace expires |
| `.ckpt-governor/expected-bytes` | the job (optional) | the write-cost estimate before a seal |
| `.ckpt-governor/conditions.json` | the operator (optional) | thresholds; see `deploy/example-conditions.json` |
| `.ckpt-governor/deployment.json` | the operator (optional) | which upstreams exist; see `deploy/example-deployment.json` |
| `.ckpt-governor/conditions.d/*.json` | the operator (optional) | conditions added by the deployment |
| `.ckpt-governor/state.json` | **the controller** | the job, at its save point |
| `.ckpt-governor/events.jsonl`, `internal.json`, `attempts.json` | the controller | `explain`, `stats`, `audit`, the next round |

A missing optional file means the default, and the defaults are the safe ones. A malformed one stops
startup: believing a threshold is in force when it is not is the failure this repository is built to
avoid.

## The first minute

```bash
ckptctl doctor --dir=/ckpt/job-1      # plan schema, then one line per condition
ckptctl explain --dir=/ckpt/job-1     # why the last decision was what it was
ckptctl stats --dir=/ckpt/job-1       # what this deployment did: writes, costs, failures
```

`doctor` exits `2` when the plan was written by a version this binary cannot read, and says which
number it found. That is the one line to check before pointing a controller at someone else's
directory.
