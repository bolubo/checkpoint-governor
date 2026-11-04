# v0.22.0 — the arrival waits for the write

This release adds one thing a platform can do without changing the scheduler: hold a
higher-priority arrival until the job it is about to preempt has sealed a checkpoint. It also closes
the two measurement gaps that had only unit tests: a real OOM death under memory pressure, and a
disk-full run on a real file system rather than a size-limited tmpfs.

## The gate

A job submitted straight to Kueue preempts the victim **0.2 s** after the intruder is created. That is
measured on Kueue v0.9.0 (decision 0030), and it is why "we predict the preemption" is not a thing
this product claims.

`ckptgate submit` holds the submission instead, writes a reclaim record the victim's controller reads
as an announcement, waits for its checkpoint to seal, and only then creates the job:

```
gate       default/train-big needs 4000m; lab is 2000m short on worker-1
victims    default/train-a (priority 100, size 2000m)
deadline   2m0s
gate       sealed at step 42 after 18.3s: releasing the arrival
submitted  default/train-big
```

The harness (`experiments/gate/`) was run five times: the arrival waited **4.0-4.8 s** and the victim
lost **0 steps**. Killing the gate at its deadline (`kill -9`, G2) marked the record `uncovered` and
created no job, so nothing was blocked; the same arrival submitted directly lost **6 steps**. Only
submissions through the gate get the lead — a job sent straight to Kueue keeps the 0.2 s path, and
that limit is printed rather than hidden (`docs/gate.md`).

The measurement also found two real bugs, both fixed here: the probe hashed the checkpoint directory's
basename instead of the `--unit` it was given, and the 60 s write cooldown suppressed the first write
of a reclaim, which is a new event rather than a repeat.

## The two receipts

- A checkpoint directory written under the generation layout (`gen-N`) is now counted by the receipt.
  Before this release the product counted only `step-` directories, so a receipt could say `no writes`
  while the directory filled up.
- The Python client dedupes a plan by `requestId` rather than by generation, so one request cannot be
  served twice.

## What was measured on real machinery

| condition | what it produced |
|---|---|
| `host.memory-pressure` | 2 GiB cgroup, N=3: condition fires, first write at step 494-495, a **real OOM** (`oom_kill` 1/1/1) arrives 3.0-4.1 s after that write, and the job and the controller both survive |
| `store.low-space` | **loopback ext4** (12 MiB image, real fsync and ENOSPC), N=5: naive ends at 0 KiB free; governed stops at 4.264-4.268 MiB and leaves one intact copy |
| `risk.shutdown` | grace 60/30/10, N=5 each; with a declared `T_write` (`--estimate=20s`) the answer is `skip no-time` and **zero writes** |

The commands and all the numbers are the P9 rows in [plan-library.md](plan-library.md). Two rows are
closed without a number: `watch.broken` is the sampler's own failure budget and has no external
trigger (the sampler *is* the controller), and multi-node preemption needs a second node and a
per-node quota in the ClusterQueue; the single-node hold is what this release promises.

## What is not in this release

- **The gate is not a scheduler.** It holds the arrival it is given; it does not choose the victim's
  replacement, and it does not promise that a job submitted elsewhere gets any lead.
- **The storage cadence is not scaled to your volume.** The rule is read every 5 s by default; a
  volume that can be consumed inside one interval has no trigger to catch it. The rig scales the
  cadence to its seconds-long rounds, and the row says which knob it turned.
- macOS and Windows wheels, a Python re-implementation of the policy, and platform deployment
  recipes remain out (decision 0028).

The Python package carries the same number without the `v`: `ckpt-policy 0.22.0`, from the same tag.
