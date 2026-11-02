# 0022 — One decision per risk event

Date: 2026-11-02. Status: accepted. Extends 0021 (who writes) with how many and how often.

## Context

0021 answered *who* writes when one cause endangers several units. Two questions were left open, and
the Kueue experiment (`experiments/kueue/`, c0/c1/c5) made both concrete.

**How many.** With five identical holders and one arrival, Kueue takes exactly one. Our rule granted
one — by name, and it was not the one Kueue took. Among equal priorities the victim is not predictable
from anything a unit can read, so "grant exactly one" is a guess with a 1/N chance of being right, and
when it is wrong the unit that dies is the unit we told not to write.

**How often.** A risk that stays true satisfies "this unit is at risk" every round. Left alone, the
controller asks for a write every five seconds, and the job writes every five seconds, for an event
that will happen once.

## Decision

### 1. The unit of decision is the event, not the round and not the clock

A preemption is a **thing that happens**: a set of higher-priority work waiting for room that the
scheduler will satisfy by taking somebody. It is decided **once**:

```
sees the event first time   → decide: nobody dies, or a chosen unit / batch writes once
same event still in force   → nothing. The window keeps the checkpoint fresh (0017)
event changes               → a new decision, a new write if it earns one
```

An earlier draft of this record used a time ladder (`2x`, `4x`, `8x` the write cost) to slow the repeat
writes down. That was wrong twice over: the ladder is a rate limiter for something that is not a rate,
and its unit — one write's cost — is shorter than the decision cadence, so it backed off from nothing.
A timer cannot know that the same workload is still waiting; the event's identity can.

**The identity is what the observer read.** `causeId` is the set of higher-priority work waiting for
room, ordered and hashed. Two rounds of the same waiting workload carry the same id, because they are
the same thing happening; a different waiting workload is a new event and gets its own decision.

**An unreadable id is not an identity.** No observer, no field, an observer that predates the field: the
gate has nothing to compare, and refusing on that would suppress exactly the write the risk exists to
justify. Empty means write.

### 2. The number of writes is a cost comparison

The algorithm stays boolean: *is this unit's work at risk* (0010). The delivery layer decides how many
writes the event is worth, from quantities it can observe:

```
write the whole tied group   iff   (M − N) · C_w  <  N · V · (1 − precision)
```

| Symbol | Meaning | Observable from |
|---|---|---|
| `M` | candidates: units in the same position (same node, same queue) | the observer's snapshot |
| `N` | expected deaths: `ceil(shortfall / unitSize)` | the queue's unused quota and pending demand |
| `C_w` | the cost of one write | the ledger's measured write cost |
| `V` | what one death costs: progress since the last checkpoint | the job's own step rate |
| `precision` | the chance our pick is among the dead | the candidates' distinguishability |

`N = 0` is the case that must not fire: a higher-priority arrival with room to spare takes nobody, and
from a single unit's point of view that looks identical to the case where it takes one.

### 3. Ties are never broken; ranks need no arithmetic

Where the scheduler's ordering separates the candidates — different priorities — the pick is reliable,
`precision` is 1, and only `N` write. Where a group is tied — the common case, one job template with N
replicas — the choice inside the tie is a coin flip, and the comparison almost always chooses the whole
tied group.

The honest consequence: **a fleet of identical units cannot be helped by coordination.** The layer says
so and lets them all write once, rather than pretending to choose.

### 4. The numbers are visible, and they are not in the plan

`explain` and `stats` print them:

```
risk    reclaim/pending-7f3a · 5 candidates · 1 expected death · indistinguishable -> 5 write once
```

They are **not** added to `state.json`. The plan is the contract with the job, and the job does not act
on any of this; a field no reader uses is a second thing to keep in sync — the same rule that keeps the
plan to one `requestId` and one `action`.

## Evidence

### FACT — the Kueue experiment, three cases on a 4 CPU queue

| Case | Arrival | Preempted | Arrival admitted |
|---|---|---|---|
| c0 control | none | **0** | — |
| c1 | 2000m, 500m short | **1** (holder-2) | yes |
| c5 (`preemption: Never`) | 2000m, same 500m short | **0** | no, "500m more needed" |

Two facts follow:

- c5 has the same waiting higher-priority pod and the same shortfall as c1, and nothing dies. **"A
  higher priority is waiting" is not sufficient** — a rule that fires on it alone is wrong, and that is
  the root of the many-writes behaviour.
- the victim in c1 was **holder-2**, not the first or the last of five equal holders: among ties the
  choice is not predictable by name, priority, or age.

### ASSUMPTION

- That the queue's shortfall is a usable estimate of `N`. It is exact for the arrival in front of it
  and optimistic when several arrivals compete; the observer reports the number it read and the receipt
  carries it, so a wrong `N` is visible rather than silent.

## Consequences

- A single-unit deployment has no candidates and often no cause: `M = 1`, an empty id, and nothing
  changes. `make demo` is byte for byte what it was.
- Long-lived risk costs nothing extra: the preemption write is the *immediate* one, and the window
  governs freshness afterwards. A risk that lasts an hour costs one write, not seven hundred.
- **The policy package does not change.** `git diff -- internal/policy` must stay empty; a write count
  that needs an algorithm change is a layer mistake.

## What this does not promise

That `N` is right, or that `precision` can be measured — it is inferred from distinguishability, which
is all a unit's own facts can allow. The promise is narrower and stronger: **the unit that dies writes,
and the writes that were pure tax disappear.**
