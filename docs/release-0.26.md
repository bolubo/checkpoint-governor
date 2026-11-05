# v0.26.0 — the command, the check, and the glue

Status: release, 2026-11-05. The number is the tag, the wheel and `ckpt_policy.__version__` (decision
0029). This release is the first one where `pip install ckpt-policy` is the whole install.

## 1. The command comes with the wheel

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install ckpt-policy
ckptctl version
```

```
ckptctl v0.26.0
```

`ckptctl status <dir>` and `ckptctl resumable --dir` work while the venv is active. `0.25.0` carried the
client and the engine but not the command; `0.26.0` carries both.

## 2. The two answers that were wrong

**A missing shard is not `world 2/2`.** `resumable` stats every rank shard the manifest marks:

```
$ ckptctl resumable --dir=/tmp/ckpt-2r
resumable (gen 1, step 300, world 2/2, full state)
$ rm /tmp/ckpt-2r/gen-0001/rank1.pt
$ ckptctl resumable --dir=/tmp/ckpt-2r
not resumable: rank 1 shard missing
```

**A finished job is not `phase writing`.** `status` reads the directory's newest seal, including a
framework checkpoint that is not a `gen-*` directory:

```
$ ckptctl status /tmp/ckpt-demo
unit ckpt-demo   generation 1   phase idle
newest sealed   step 1000 at ...
writing         none
resume: resumable (step 1000, ...)
```

## 3. The per-step tax is gone

`step()` keeps the atomic rename but skips the fsync: the progress cell says "this rank is alive", and
a crash loses at most the current value, which the next step rewrites. Manifests, declarations and the
plan still fsync.

| run | before | after | control |
|---|---|---|---|
| S1, 1000 steps, one GPU | 16.8s | **6.1s** | 5.9s |
| S2, 300 steps, two ranks | 15.2s / 13.8s | **5.0s / 4.9s** | model-only, not timed |

The S1 control is the same `TrainingArguments` without the governor, so the remaining difference is the
user's own glue, not ours. The host is a local ext4 disk; a tmpfs run was always cheap, and a shared
volume is a different measurement.

## 4. Ready-made glue

- `contrib/ckpt-policy/examples/hf_trainer.py`: a 200-step single-GPU Trainer (1.9s in the observed
  run), silent while training, one checkpoint at the end, `resumable` answers for it.
- `contrib/ckpt-policy/examples/two_ranks.py`: two ranks, per-rank shards, a barrier, rank 0 seals;
  `resumable (gen 1, step 300, world 2/2, full state)`.
- `docs/multi-rank.md`: who starts the engine, where the shards go (`gen-0007/rank0.pt`), who seals
  (rank 0), and what `resumable` checks.
- `docs/first-ten-minutes.md`: the commands and the lines they print.
- `docs/troubleshooting.md`: starts from the message a user is looking at.

`Governor` now reads `RANK` and `WORLD_SIZE` from `torchrun`; only rank 0 starts the engine. The
multi-rank glue lost four lines, and a non-leader can no longer start a second controller on one plan.

## 5. Reproduce it

```bash
make fast
make py                 # 54 tests
make test-claims        # claims: 37 · tests found: 37 · missing: 0
make docs
make wheel
python3 contrib/ckpt-policy/examples/hf_trainer.py /tmp/ckpt-demo 200
ckptctl resumable --dir=/tmp/ckpt-demo
```

For the two-rank example, run the two commands in its docstring on one machine and check
`ckptctl status /tmp/ckpt-2r`.

## 6. What this release does not claim

- **Model sync across ranks.** DDP/FSDP is the framework's; this repository's multi-rank path is the
  checkpoint protocol. The two-rank example uses a file barrier and does not sync gradients; a real
  DDP job uses the same four calls, and that combination was not run on a multi-GPU host here (the
  WSL loopback transport is unavailable).
- **A real multi-pod Kueue victim.** The S3 harness runs `ckpttrainer` on one pod; the gate timing is
  measured, the placement of a multi-pod `Job` is not.
- **A shared volume's rename/fsync semantics.** The local ext4 numbers are measured; NFS/Lustre is a
  deployment limit, not a different policy.
- **Lightning and Composer examples.** Their callbacks are supported and tested; the copyable example
  loops are still open issues.
