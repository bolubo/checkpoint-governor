# 0021 — The fleet decides how many writes an event justifies

Date: 2026-11-02. Status: accepted. Applies 0017 (two numbers) and 0019 (detection) to the case where
one cause makes several units look endangered at once.

## Context

The algorithm answers one question about one unit: is this work at risk? It is a pure function of that
unit's facts, and it stays that way (0010, 0015).

Ten units share a node. A higher-priority workload arrives. Kueue reclaims **one** of them. All ten
read the same world file, all ten see "a higher-priority pod is waiting", and **all ten decide their
work is at risk and write**. Nine of those writes were for units that were never going to die, and
they landed on the same volume in the same minute.

The mistake is not the threshold. A unit that sees only itself **cannot** distinguish "I am the victim"
from "my neighbour is": the fact they need is not about them, it is about the set. Per-unit inference
cannot produce it, however well it is tuned.

## Decision

**The number of writes is a property of the cause, not of how many units noticed.**

A risk signal is classified by **who dies** when it is true, and that classification sets the fanout:

| Cause | Who dies | Who writes | Concurrency |
|---|---|---|---|
| Reclaimed for a higher priority (Kueue preemption) | **the N units the scheduler picks** out of M candidates | **exactly those N** | parallel |
| Node drained (cordon + evict) | every unit on that node | all of them | **queued** on the volume |
| Spot or instance interruption | the whole instance | all of them | queued |
| Node failure, kernel, whole-machine OOM | all of them | whoever is still alive | — |
| Disk full, volume slow | shared damage, and **a write makes it worse** | **one at a time**, most-at-risk first | strict serial |
| Training finished (not a risk) | this unit | this unit | none needed |
| Window closed (not a risk) | this unit | this unit | none needed |

Three fanouts follow — `one-per-victim`, `all-affected`, `serialized` — across two dimensions that are
independent: **who writes**, and **when**. "Sometimes only the right one writes, sometimes all of them
should" is one table read along two axes, not two policies.

**Who is the victim is decided by the component that can see the set.** `contrib/observer` already
publishes one record per unit (node, priority, QoS, deletion timestamp); it is the only component in
this design with a view of all units on a node. It publishes a verdict per unit, not a suggestion:

```json
{ "permit": "granted" | "held" | "queued", "cause": "reclaim", "rank": 1,
  "token": "reclaim-<event>", "until": "2026-11-02T00:05:00Z" }
```

The governor obeys it. A unit with `held` publishes
`skip  risk.preempt  this unit is not the reclaim target` — which is **more accurate** than today's
"everything is at risk", not less.

## The rule that outranks all of this

**Coordination may reduce writes. It may never reduce safety.**

Every failure of the permit path means `granted`: no observer, a missing field, an unreadable value, an
expired `until`, a token for an event that already closed. A coordination layer that can suppress a
save is worse than no coordination layer, because it fails exactly when the fleet is under pressure.

The two acceptance numbers, and neither alone proves anything:

1. **Recall must not fall.** Of the units that are actually reclaimed, the share that wrote must be
   100% before and 100% after.
2. **Writes must fall.** `writes per preemption` goes from M:1 to 1:1.

Reporting only the second is how a project trades safety for a metric and calls it coordination.

## Where each part lives

| Place | Change | Layer |
|---|---|---|
| `docs/glue.md` | the unit file gains a `permit` block; every failure reads `granted` | contract |
| `internal/permit` | parse and decide, fail-open, one function | delivery |
| `internal/exec` | a second gate after the volume gate; `held` means no write | delivery |
| `contrib/observer` | computes `rank` and `token` for `one-per-victim`; orders `serialized` by what is at risk | observer |
| `internal/policy` | **nothing** | algorithm |

**The diff to `internal/policy` must stay empty.** If deciding who writes requires touching the
algorithm, the boundary was drawn in the wrong place.

## Consequences

- A single-unit deployment never sees a permit and behaves byte for byte as before. That is the
  regression test, not a promise: `make demo` must produce identical output with and without this
  layer.
- The serialized class needs an ordering that is not priority — priority decides who dies, not who
  loses most. Order by progress since the last checkpoint times the value of a step, both of which the
  observer already has.
- If the queue cannot drain before `until`, the class degrades to "whoever still fits" and says so in
  the receipt, rather than pretending the whole fleet was covered.
- The measurement this record enables, and does not perform: how often the reclaim class fires at all.
  That is a question about the world, and `docs/experiments-matrix.md` is where it gets an answer.

## What this does not promise

That the observer is right. A wrong verdict is a lost write for a victim that was never offered the
chance — which is why the verdict is published with a rank and a token (so it can be argued with) and
why recall is measured separately from the write count. The layer is allowed to be wrong about *who
writes*; it is not allowed to be wrong about *whether the victim wrote*.
