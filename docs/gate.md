# The gate: giving an arrival lead time

A higher-priority job that preempts a training run gives the victim no warning to act on. Measured on
Kueue v0.9.0: the victim's `deletionTimestamp` arrived **0.2 s** after the intruder was created. Any
inference that claims to see the scheduler's decision before the scheduler does is a lie.

The arrival is the part you control. `ckptgate submit` holds the submission, writes a reclaim record
for the victim, waits for the victim's checkpoint to seal, and only then creates the job in the cluster.
The scheduler still does the preempting; it just does it after the write instead of during it.

This is the one thing decision [0030](decisions/0030-the-reclaim-is-gated.md) adds. It is separate
from the governor and optional.

## Build it

```bash
make gate                                    # build and test the module
cd contrib/gate && go build -o ckptgate .

# or from a release: install.sh installs ckptgate beside the two core binaries
curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
```

It is its own Go module, like the observer, so `client-go` never reaches the core binary. The
release tarball carries it under the same version number; `ckptgate version` prints that number.

## Use it

```bash
ckptgate submit \
  --manifest=job.yaml \        # a batch/v1 Job; its priority and CPU requests decide the hold
  --world=/var/lib/ckpt/world \ # the same --world-dir the observer writes
  --cluster=lab \              # the same --cluster the observer writes
  --queue=lab \                # the ClusterQueue the arrival goes to
  --ckpt-root=/checkpoints \    # where the victims' job directories live
  --deadline=2m                # how long the gate may wait for a seal
```

What it prints before it changes anything:

```
gate       default/train-big needs 4000m; lab is 2000m short on worker-1
victims    default/train-a (priority 100, size 2000m)
deadline   2m0s
```

Then it waits. When the victim's controller has seen the reclaim, written and sealed:

```
gate       sealed at step 42 after 18.3s: releasing the arrival
submitted  default/train-big
```

`--dry-run` prints the first block and stops. `--node` pins the node when the gate's "emptiest node"
heuristic is not what you want.

## What the victim sees

The victim needs no new configuration. Its controller reads the reclaim record as an announcement —
the same fact as a `deletionTimestamp` — and `risk.preempt` fires. `ckptctl explain --dir=...` names
the hold:

```
basis      announcement: the reclaim gate is holding default/train-big for this unit until 10:24:04Z (reclaim …)
```

The gate also writes the fleet's permit for the victim (`granted`, `cause: reclaim`), which is the
same shape the observer already uses (decision 0021).

## What it does not promise

- **Only submissions through the gate get the lead.** A job submitted straight to Kueue keeps the
  announcement-only path: 0.2 s, no time to write. This is a limit, not a bug.
- **The deadline is the operator's number.** It must cover the controller's cadence, one write and
  the seal. A hold shorter than a write is not a hold. The measured chain is in
  [operator-notes.md](operator-notes.md).
- **Fail-open, always.** At the deadline the arrival goes in with no seal. If the gate dies, the
  arrival was never created, so nothing is blocked; `ckptgate gc` marks the record `uncovered` so
  the promise that was not kept is visible. If the gate picked the wrong victim, the write is
  wasted and the scheduler proceeds.
- **The checkpoint directory must be discoverable** under `--ckpt-root`: the gate matches a unit by
  the `unitId` its controller publishes in `.ckpt-governor/state.json`. When it cannot find one, it
  does not hold.

## Measured, 2026-11-03

kind + Kueue v0.9.0, one node, a 4-CPU `lab` queue. Victim: 2000m at priority 50; four holders at
500m each; arrival: 2000m at priority 200. The harness is `experiments/gate/`.

**G1 — the hold buys the write.** The gate chose `default/victim-qjdpz`, waited, then:

```
gate       sealed at step 82 after 55.4s: releasing the arrival
submitted  default/arrival
```

The victim pod went `Terminating`; the arrival went from `Suspended` to `Running`. The victim's newest
manifest was `gen-0082` (step 82) and its step file also read 82 — **lost 0 steps**.

The 55.4 s is the victim's own cadence, not the gate's: its controller was inside a write cooldown,
and the reclaim is one more event after that. The deadline (90 s) has to cover the controller's
cadence plus one write and the seal; see [operator-notes.md](operator-notes.md).

**G2 — fail-open.** `kill -9` while the record was `held`, then wait past the deadline:

```
gc         default/victim-qjkml held for default/arrival: past its deadline, never released
gc         1 marked uncovered
```

The arrival was never created, so nothing was blocked. Submitting the same arrival straight to
Kueue afterwards preempted as it always did — and the victim, whose newest checkpoint was step 1,
died at step 14: **lost 13 steps**. That is the difference the gate is for, and the limit it
admits to: only submissions through it get the lead.

