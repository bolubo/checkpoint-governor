# The algorithm

The algorithm asks one question:

> Is there an event after which this job's work would be lost, and is there still time to save it?

It answers with a named boolean. It does not compute an interval, a tax, or a failure probability.
`docs/decisions/0013-risk-conditions.md` holds the catalogue (the frozen names, their meaning, and
what their absence means); `docs/decisions/history.md#0014` holds why the earlier
interval optimisation was abandoned; `docs/decisions/0017-two-numbers.md` holds what the user's two
numbers mean and what the executor does with them. This page says how a decision is computed.

## 1. The rule

1. Any **urgent** condition that is `1` → advise one write now. `Reason` names the first such
   condition in catalogue order, so the answer is stable.
2. A **required** condition that is unreadable is itself urgent: blindness is a risk, not a zero.
3. A condition the environment does not have is **absent** and changes nothing (a CPU job has no
   GPU condition; an on-prem job has no preemption condition).
4. A **warn** or **note** condition never triggers a write.
5. Job-local facts are triggers too, and they are not environment: training is ending, and two
   writes failed in a row.
6. The algorithm contains **no clock**. Backoff, the window timer, retries, atomic sealing and
   read-back verification belong to the implementation.

That is the whole policy. There is no arithmetic to tune, because the only quantities are facts.

## 2. What a decision contains

| Field | Meaning now |
|---|---|
| `Action` | `save` (a condition fired), `hold` (a write is in flight), `unknown` (a required input is missing at startup) |
| `Reason` | `risk` when a condition fired, plus the existing `repair`, `capacity`, `end-of-training` |
| `Reasons[0].Code` | the condition that fired, in the catalogue's vocabulary |
| `Missing` | the conditions that were blind, and the inputs that were absent |
| `Kind`, `WeightsOnly`, `CopiesToWrite`, `KeepCopies`, `OldestStepToKeep` | unchanged: what to write and how much to keep |
| `Interval`, `TriggerAt`, `NextCheck`, `Tax`, `TightestBound`, `FallbackInterval` | **superseded**. A decision that still carries them is a decision from the old policy |

A consumer must be able to act on the file alone, and it must be able to read *which fact* caused
the write, because that is the answer to "why did my job just checkpoint".

## 3. What the algorithm does not do

It does not read a disk, a scheduler, a GPU, or a network. It does not decide how many streams to
use, how long to back off, or how to delete an old copy. It does not know whether the job is alone,
in a gang, on spot instances, or in a private cluster. Every one of those facts reaches it only as a
named condition that is `0`, `1`, absent, or unknown.

## 4. The state file

The plan reaches the job as a file with an expiry stamp, not as a network call, and it is replaced
by rename so a reader never sees half of it. The controller renews it while it is the same plan,
keeping at least half of its life — and the renewal is **not** subject to the publish rate limit, only
a *change* of content is: the expiry is how a live controller stays visible, so a cap that could
starve it would turn a healthy controller into one that looks dead. **The expiry means "a live
controller exists"**, not a period:
it is a few rounds (`advise.DefaultPlanTTL`, 15 s), so a dead controller is noticed within a handful
of rounds rather than minutes. When the file has expired, the job falls back to the user's own number
(`no-us`): its own timer, or nothing if the user answered `none`. The file's shape is the
implementation layer's to own; the fields above are what it must carry.

## 5. Trace format

One JSON object per line, in the order recorded:

| Field | Meaning |
|---|---|
| `t` | RFC 3339 timestamp |
| `event` | `step`, `save_start`, `save_end`, `free`, `kill` |
| `step` | the step reached (`step` events) |
| `bytes` | free bytes (`free` events), or the size written (`save_end`) |
| `sealed` | whether the recorded write actually sealed (`save_end`) |

A trace is a description of the world, not of a schedule: it says how fast steps ran, how much
space was left, when the job was killed, and how long a write took. The replay reads the schedule's
own decisions; the trace's writes are used only for their cost.

## 6. How a decision is proven

Two mechanical checks, both already in the repository: the recorded replay runs every family through
every registered policy and compares the decisions line for line
(`internal/simulate/golden_test.go`), and the truth tables in `internal/policy` pin each condition's
`0`, `1`, absent and unknown states. Environment blindness is held by the loop's own tests: a
condition that cannot be read is urgent, and one the environment does not have changes nothing
(`condition_test.go` and `risk_test.go` in `internal/policy`). The capability and size-ladder tests
belonged to the interval algorithm and were deleted with it. A new condition is
proven the same way: a truth table over its four states, plus one family in the replay.

The measurement rules — what a trace must contain, which metrics are reported, and how a claim is
accepted — are in `docs/experiments-matrix.md`.

## 7. What was deleted, and why it must stay deleted

The folder that used to sit beside this page described an optimisation of the checkpoint interval
against an assumed failure rate. `math.md`, `worklist.md` and records 0006–0012 described it, and it
was internally consistent and externally wrong: the quantity it optimises (expected lost work against
a tax budget) is one the user cannot observe, and the arithmetic showed the best a constant period can
be is a few percent away from the best an oracle could do. That is why the risk core replaced it
(`docs/decisions/0014`), and why those pages are only in git history. Do not resurrect them; if a new
claim needs arithmetic, derive it again against the risk model.

## 8. What still has to be proven

Only these, each with a mechanical check:

1. **A condition fires exactly when it should.** A truth table per condition over `0`, `1`, absent
   and unknown; no condition may trigger a write when it is a diagnostic.
2. **Blindness is urgent.** A required condition that becomes unreadable produces a write, and its
   name appears in `Missing`.
3. **The algorithm stays environment-blind.** The same snapshot with all conditions absent behaves
   exactly like the same job with all conditions `0`; a CPU-shaped and a GPU-shaped snapshot with the
   same conditions decide the same.
4. **The old policy stays dead.** A decision that still carries `Interval`/`Tax` is a bug, not a
   compatibility case.
5. **The replay is the evidence.** The recorded replay over the generated families still runs, because
   the executor's behaviour (backoff, sealing, verification) is what the traces exercise.
