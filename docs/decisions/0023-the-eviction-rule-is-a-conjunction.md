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

That the three terms are complete. They are complete *on the base grid*: one arrival, one queue, one
strategy. The extended grid measured the three ways that could be wrong, and two of them are now known:

| extension | predicted | observed | verdict |
|---|---|---|---|
| cohort, 2000m | 0 | 0 | agreed, after reading the cohort's room |
| cohort, 3000m | 0 | 0 | agreed, after the capacity term |
| equal priority, `LowerOrNewerEqualPriority` | 0 | 0 | agreed |
| two arrivals at once (1000m, 2000m) | 1, 5 | 1, 5 | agreed |

**18 cases: 18 agreed, no false positives, no false negatives, count exact 4/4.**

**Borrowing breaks the rule in the expensive direction, and half of it is now fixed.** A queue inside a
cohort lends and borrows, so a shortfall on paper can be absorbed by a sibling without taking anybody.
The third term now reads *the cohort's* headroom rather than the queue's, which turned the 2000m row from
a false positive into agreement.

**The 3000m row needed a different fact, and the evidence was one line.** The arrival was never admitted:

```
$ kubectl get job arrival -o jsonpath='{.spec.suspend}'
true
```

Its queue's own quota is 500m, and `borrowWithinCohort` defaults to *prohibit*, so 3000m can never fit,
however much the cohort has free. Kueue does not preempt to make room for work that cannot fit no matter
what: it waits. The rule now carries that as a **capacity** term — the most the queue could ever admit,
its own quota plus the borrowing its policy permits — and a demand past it is a shortfall of zero.

The distinction worth keeping: *does not fit now* is a death, *cannot fit at all* is a wait. Both look
identical from a headroom number.

The equal-priority row is reassuring but thin: one queue policy, one priority pair, one sample. It says
the strict "higher" requirement did not miss a death here; it does not say it never will.
