# 0031 — The fanout is per node

Date: 2026-11-04. Status: accepted. Extends 0021 (fleet writes), 0023 (the eviction rule) and 0030
(the reclaim is gated). Replaces no record.

## Context

**FACT.** A Kubernetes Job with `parallelism: N` asks for N pods' worth of requests: the scheduler
places pods one by one, and Kueue's Workload counts both podSets. Distributed training on
Kubernetes is, in the common shape, one pod per node (`torchrun nnodes=N`); a single pod spanning
several nodes (MPI) is a different model and is not what this record claims.

**The gate did not know either of those facts.** `needOf` read one pod's request, `pickNode` chose
one node, and `candidates` dropped every unit whose `node` differed. A two-pod arrival on two short
nodes therefore held victims on one node only (the probe printed exactly that: one node picked,
one victim, the second node never asked). Room freed on worker-1 cannot hold a pod that must land
on worker-2, so the second node's victim died with the same 0.2 s notice the gate exists to remove.

## Decision

1. **The arrival is `perPod × pods`.** `pods` is `spec.parallelism` (absent or zero means one).
   Completions index pods one after another and do not add a concurrent request.
2. **Pods are spread over the emptiest readable nodes**: at most one node per pod, evenly distributed
   when there are fewer nodes than pods. Cordoned nodes and nodes whose requests are unknown are
   skipped — a node we cannot measure is not a place to look for a victim.
3. **The shortfall is per node.** Each node's gap is `max(0, perPod × podsOnNode − free)`, and
   victims are chosen on that node with the fleet's existing rule (`permit.Victims`: lowest
   priority, then the largest footprint, and a tie group is never broken). A global ordering over
   all nodes is rejected: it can put every victim on the node with the cheaper units.
4. **The queue stays a gate, not the arithmetic** (0023). The queue must allow preemption, and work
   past its capacity is never admitted; its gap is `max(0, total − queue room)`. The victims
   together must free `max(queue gap, Σ node gaps)`; the part that exceeds the node gaps may come
   from any selected node.
5. **All-or-nothing.** If a node that must free room has no governed unit below the arrival, or the
   victims cannot free what the plan asks, the gate does not hold at all. Partial lead is a promise
   nobody keeps: the unheld node's victim still dies in 0.2 s.
6. **`--node` takes a comma-separated list.** An empty list means the heuristic above. A single-pod,
   single-node arrival produces the same arithmetic and the same sentence as before:
   `want = max(queue gap, node gap)`, which is what `shortfallFor` used to compute.

## Consequences

- The first block names one shortfall per node, and the victims line carries the node each victim
  is on. `NeedMilli` in the reclaim record is the arrival's total request.
- **Limits, stated rather than implied:** the node choice is a heuristic, not the scheduler; only
  CPU enters the arithmetic (memory is still the queue's business); the manifest must be a
  `batch/v1 Job` (PyTorchJob and MPIJob are not accepted); a real cross-node hold needs a checkpoint
  volume both the gate and the pods can see — the harness's `hostPath /g1` is per node.
- **Tests:** `TestPickNodesSpreadsThePods`, `TestShortfallPerNodeIsPerNode`,
  `TestPlanRoomKeepsTheNodeGaps`, `TestPickVictimsTakesOnePerShortNode`,
  `TestPickVictimsTopsUpTheQueueGap`, and the end-to-end `TestFanoutHoldsOneVictimPerNodeAndWaitsForBoth`
  against a fake client and two stand-in controllers.

## Alternatives rejected

- **One global shortfall with a global sort.** Measured to pick every victim on one node; the
  second node's pod never gets room.
- **Partial hold** (hold the node we can, submit anyway). It delays the arrival and still fails the
  victim on the other node, so it buys cost without buying the promise.
- **Pod-per-node written into the product as a truth about the scheduler.** It is a model of how
  multi-pod jobs are placed, not a guarantee; `--node` stays the operator's override.
