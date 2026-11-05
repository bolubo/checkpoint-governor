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

`--dry-run` prints the first block and stops. `--node` pins the nodes when the gate's "emptiest node"
heuristic is not what you want; it takes a comma-separated list (`--node=worker-1,worker-2`), one
entry per pod of the arrival.

## When the arrival needs more than one node

A `batch/v1 Job` with `parallelism: N` asks for N pods. The gate counts N pod requests, spreads them
over the emptiest readable nodes (cordoned and unmeasurable nodes are skipped, and pods share a node
when there are fewer nodes than pods), and computes a shortfall **per node**:

```
gate       default/train-big needs 4000m over 2 pods; worker-1 is 1200m short; worker-2 is 800m short
victims    default/train-a (worker-1, priority 100, size 2000m), default/train-b (worker-2, priority 100, size 2000m)
```

The victims are chosen on each node with the fleet's existing rule (lowest priority first, then the
largest footprint, a tie group is never broken), because room freed on worker-1 cannot hold the pod
that must land on worker-2. The queue's own gap is a gate, not the arithmetic: the queue must allow
preemption, and the victims together must free `max(queue gap, sum of node gaps)`.

**All-or-nothing.** If a node that must free room has no governed unit below the arrival, the gate
does not hold at all — it submits directly and says why. Holding one node and letting the other's
victim die with 0.2 s notice would be a promise nobody keeps. A single-pod, single-node arrival
produces exactly the same arithmetic and the same first line as before.

The real cross-node run needs a checkpoint volume both the gate and the pods can see. The test
cluster's `hostPath /g1` is per node, so the gate's own two-node path is covered by a two-node world and
a fake client (`contrib/gate/submit_fanout_test.go`), and the two-pod harness
(`hack/kind-multipod-gate.sh`) runs on one kind node with a PVC, not across nodes.

## When the victim is a multi-pod workload

A preemption stops every pod of a `batch/v1 Job` at once, so the gate holds the **workload**, not each
pod. One Job produces one hold, under the workload's id, and its record names the pods:

```
gate       default/train-big needs 4000m; lab is 2000m short on worker-1
victims    default/train-a (worker-1,worker-2, priority 50, size 4000m)
deadline   2m0s
```

The gate releases on the workload's copy, not on the first pod's step: the newest manifest must have
appeared after the hold, every shard it names must read back (size and tail hash), its `sealedAt` must
not predate the hold, and it must name a shard for **every rank the workload has**. A two-pod workload
whose seal names only `rank-0` is half a copy; the gate keeps waiting and prints which rank is missing:

```
gate       default/train-a: not verified — the seal names 1 of 2 ranks; rank 1 has no shard
```

**One shared root is a premise.** All pods of the workload must see the same checkpoint directory (an
RWX volume or a shared filesystem, mounted at the same path). The gate refuses a multi-pod victim whose
checkpoint root is a per-node `hostPath`, and refuses a workload whose pods resolve to different
directories, instead of waiting for a seal that cannot exist:

```
ckptgate submit: default/train-a: pod default/train-a-rank1 mounts the checkpoint root from hostPath /g1;
a multi-pod workload needs one shared root (RWX volume or shared filesystem)
```

Single-pod output is unchanged, down to the comma: the workload id is the pod's unit id and the
arithmetic is the same one earlier releases printed.

## Where it runs

The gate is a submission wrapper, not a daemon: it runs wherever the job would otherwise be
submitted (a control box, a CI runner, an operator's laptop), with the world volume and the
checkpoint root visible at the same paths the observer and the controllers use.

```bash
# same volume, same paths; the gate reads the world the observer writes
ckptgate submit --manifest=job.yaml \
  --world=/var/lib/ckpt/world --cluster=lab \
  --queue=lab --ckpt-root=/checkpoints --deadline=2m
```

There is nothing to install on the training nodes and no CRD to add. If the submitter cannot see the
checkpoint volume, the gate cannot match a unit and does not hold; mount the same volume (the
observer already needs it), or run the wrapper on a node that has it. `--dry-run` is the first
command to run on a new host: it prints the two blocks and changes nothing.

## What the victim sees

The victim needs no new configuration. Its controller reads the reclaim record as an announcement —
the same fact as a `deletionTimestamp` — and `risk.preempt` fires. A multi-pod victim's record is
written under the workload's id, and each pod's controller finds it through the `workload` field the
observer records for its unit. `ckptctl explain --dir=...` names the hold:

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
- **Fail-open, always.** At the deadline the arrival goes in with no seal, and the record is marked
  `uncovered`: the write was asked for and never promised. If the gate dies, the arrival was never
  created, so nothing is blocked; `ckptgate gc` marks that record `uncovered` too. If the gate picked
  the wrong victim, the write is wasted and the scheduler proceeds.
- **The checkpoint directory must be discoverable** under `--ckpt-root`: the gate matches a unit by
  the `unitId` its controller publishes in `.ckpt-governor/state.json`. When it cannot find one, it
  does not hold.
- **One pod, one node is a model, not a promise.** It is how multi-pod Jobs are normally placed, and
  `--node` overrides it; a single pod spanning nodes (MPI-style) is a different shape this gate does
  not model. A multi-pod victim is supported only when its pods share one checkpoint root.
- **Only CPU is in the arithmetic**, and only a `batch/v1 Job` is read. Memory belongs to the queue,
  and PyTorchJob/MPIJob manifests are not accepted.

## Measured, 2026-11-03

kind + Kueue v0.9.0, one node, a 4-CPU `lab` queue. Victim: 2000m at priority 50; four holders at
500m each; arrival: 2000m at priority 200. The harness is `experiments/gate/`.

**G1 — the hold buys the write.** The gate chose `default/victim-ww5cf`, waited, then:

```
gate       sealed at step 7 after 5s: releasing the arrival
submitted  default/arrival
```

The victim's newest manifest was `gen-0007` (step 7) and its step file also read 7 — **lost 0 steps**.

Five consecutive runs: wait **4.0, 4.5, 4.0, 4.5, 4.3 s** (median 4.5), and the newest checkpoint
equalled the step at death in all five — **0 lost, 5/5**.

The wait is the victim's probe cadence plus one write, not a fixed latency. It took 55 s on the
first run of this harness for two reasons that were bugs, not cost: the probes hashed the checkpoint
directory's basename instead of the `--unit` the run was given (so they were blind to the observer's
unit), and the 60 s write cooldown applied to a reclaim even though a reclaim is a new event. Both
are fixed; the deadline still has to cover the cadence plus one write and the seal
([operator-notes.md](operator-notes.md)).

**G2 — fail-open.** `kill -9` while the record was `held`, then wait past the deadline:

```
gc         default/victim-qk5vw held for default/arrival: past its deadline, never released
gc         1 marked uncovered
```

The arrival was never created, so nothing was blocked. Submitting the same arrival straight to
Kueue afterwards preempted as it always did — and the victim, whose newest checkpoint was step 7,
died at step 13: **lost 6 steps**. That is the difference the gate is for, and the limit it
admits to: only submissions through it get the lead.

