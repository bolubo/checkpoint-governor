# v0.23.0 — the hold covers the nodes the arrival needs

This release fixes the one remaining single-node assumption in the gate: a `parallelism: 2` Job is
two pods, and a hold that only covers one node leaves the other node's victim with the same 0.2 s
notice the gate exists to remove.

## What the probe showed

A two-node world and a two-pod arrival, before this release:

```
arrival: parallelism=2, per-pod need=2000m, true total=4000m
pickNode: worker-1 free=1000m · shortfall(per-pod)=1000m · shortfall(2-pod truth)=3000m
candidates on worker-1: 1 · victims: [default/victim-a]
PROBE: one node picked (worker-1) — the second node is never asked
```

Room freed on worker-1 cannot hold a pod that must land on worker-2. That is the gap (decision
0031).

## What it does now

`podsOf` reads `spec.parallelism` (absent or zero is one); the arrival's total is `perPod × pods`.
The gate spreads the pods over the emptiest readable nodes — cordoned nodes and nodes whose requests
are unknown are skipped, and pods share a node when there are fewer nodes than pods — and computes
the shortfall **per node**:

```
gate       default/train-big needs 4000m over 2 pods; worker-1 is 1200m short; worker-2 is 800m short
victims    default/train-a (worker-1, priority 100, size 2000m), default/train-b (worker-2, priority 100, size 2000m)
```

Victims are chosen on each node with the fleet's existing rule (lowest priority, then the largest
footprint; a tie group is never broken). The queue's own gap is a gate, not the arithmetic: the
victims together must free `max(queue gap, Σ node gaps)`, and the queue must allow preemption in the
first place (0023).

**All-or-nothing.** If a node that must free room has no governed unit below the arrival, the gate
does not hold at all — it submits directly and says why. Holding one node while the other's victim
dies in 0.2 s is a promise nobody keeps.

`--node` now takes a comma-separated list. A single-pod, single-node arrival keeps the same
arithmetic and the same first line as before; a test pins the sentence.

## What is tested

Seven unit tests for the arithmetic (pod count, node spread, per-node shortfall, queue-vs-node gaps,
patience with all-or-nothing, tie rules) and two end-to-end tests against a fake client: a two-node
world with two governed directories and two stand-in controllers that write a manifest when they see
the reclaim. The positive case observes **both** seals before the job is created; the negative case
proves the refusal. Both run in CI (`make gate`).

## What is not promised

- **One pod per node is a model**, not a scheduler promise: it is how multi-pod Jobs are normally
  placed, and `--node` is the operator's override. A single pod spanning nodes (MPI-style) is a
  different shape this gate does not model.
- **Only CPU is in the arithmetic**, and only a `batch/v1 Job` is read. Memory belongs to the queue;
  PyTorchJob/MPIJob manifests are not accepted.
- **A real cross-node hold needs shared storage.** The lab's `hostPath /g1` is per node, so the
  control-plane gate cannot see a worker's checkpoint; the world-level test covers the gate's own
  path, and the deployment prerequisite is written down rather than pretended.

The Python package carries the same number without the `v`: `ckpt-policy 0.23.0`.
