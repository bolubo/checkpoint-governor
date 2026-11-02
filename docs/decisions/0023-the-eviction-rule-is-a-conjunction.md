# 0023 — The eviction rule is a conjunction, because the sweep said so

Date: 2026-11-02. Status: accepted. Replaces the definition in `docs/conditions.md` for
`risk.eviction-likely`, and follows 0022.

## Context

`risk.eviction-likely` fired on an **or** of four signals: CPU at 90% of allocatable, memory at 90%, a
pending pod with higher priority, or a cordon. Each of the four was a plausible proxy for "the node is
about to need this unit's room".

Twelve measured cases say it is not an or (`experiments/kueue/results.csv`):

| conditions | preempted |
|---|---|
| equal priority, 500m short | 0 |
| higher priority, 500m short | 1 |
| higher priority, 2500m short | 5 |
| higher priority, `preemption: Never` | 0 |

Every case that lacked one of the three took nobody.

## Decision

The condition is the **conjunction** of three facts, each read rather than guessed:

```
higher priority waiting above this unit   AND   the queue has a shortfall   AND   the queue may preempt
```

- **higher priority waiting** is per unit: the held-back jobs that outrank *this* unit. The node-level
  count cannot answer "could I be the one taken", which is the only question that matters here.
- **shortfall** is the queue's own arithmetic: demand from held-back work minus free quota.
- **may preempt** is the queue's policy. `Never` means the arrival waits and nobody dies.

Where the cluster publishes no queue — no Kueue — the third term falls back to the node: a shortfall is
"no room left on this node" against the operator's own line, and the first term falls back to the
node's pending count. That is the same rule measured one layer down, not a second rule.

## Consequences

- **A full node with nobody waiting above the unit no longer fires.** That is a behaviour change and it is
  the point: in the sweep, pressure alone took nobody, and firing on it is what made every unit in a
  fleet believe it was about to die.
- **A cordon no longer fires this condition.** On a cordon everybody goes; `risk.evicted` is the
  condition for a whole-node death, and its fanout is `all-affected`. Using this one for that case was a
  fanout error wearing a risk's clothes (0021).
- The line (90%) stays as a configuration knob, but it now only breaks ties in the no-queue fallback.
- Three tests changed, and each says in place why the old expectation was wrong. A rule that changes
  because a measurement says so should leave the old belief visible, not quietly overwrite it.

## What this does not promise

That the three terms are complete. They are complete *on this grid*: one arrival, one queue, one
strategy. Borrowing, cohorts, multiple simultaneous arrivals and `LowerOrNewerPriority` are untested, and
each of them can make the conjunction true while nothing is taken. Until they are measured, this rule is
the best available and the sweep is how it gets better.
