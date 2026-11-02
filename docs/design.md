# Design: the backstop, and how it is built

This page is one document in two parts: the argument (Appendix A) and the build (§1–§14).
It absorbs `design-backstop.md` and `design-v1.md`; both stay in git history.

Status: engineering design. Date: 2026-11-01. Companion to Appendix A (why and what); this page is
*how*, and it is the page an implementer works from. Where an older page conflicts (`docs/policy.md`
§4), this page wins for v1.

The definitions this design is cut against, in three lines:

1. **Without us** the user writes every `no-us`. He states that number once; it is the baseline of
   the saving and the job's timer if we die.
2. **With us** he may go `with-us` between checkpoints (optional). It is a **deadman window**, not a
   schedule, refreshed by **any** write. While we run, a write has exactly two causes: an urgent
   condition is `1`, or the window is due.
3. **The algorithm is the frozen rule** in `docs/policy.md` §1: a named boolean, no clock, no
   arithmetic. Everything numeric below is the implementation's.

## 1. Vocabulary (one word per meaning)

| Product word | Code word | Meaning | Where it travels |
|---|---|---|---|
| `no-us` | `NoUs` | "without the governor, how often would you have written?" (`none` allowed) | plan (`baselineSeconds` / `baselineNone`), the report |
| `with-us` | `Window` (bound `WithUs`) | "with the governor, how long may a checkpoint be stale?" (unset = exceptions only) | plan (`windowSeconds`), executor, display |
| (nothing) | `W` | the write's own duration: p90 after two seals, else the estimate with its source | executor only, never the algorithm |
| (nothing) | `T` | the deadline a platform announced (`seconds-left`) minus the teardown margin | executor only |

The old words are gone: `Fallback*` → `NoUs*` in the bound and `Window` in the executor, and
`MaxLostWork`/`MaxTax` survive only as simulator inputs.

## 2. The round, end to end

One round is one `loop.Reconcile()`, ≤ 1 ms on a directory with ten copies (asserted by the round-overhead test). It is
level-triggered: nothing depends on message order, and a missed event costs one round.

```
read the directory        observe.Scanner        copies, step, progress, in-flight, members
settle                   exec.Ledger            outcome of the running attempt, from the directory
read the job's numbers    adapter glue           expected-bytes, last-save-seconds, seconds-left
sample conditions         produce.Sampler        each at its own cadence, blind after 3 misses
decide                    policy.Risk            the frozen rule → action, reason, reasons
gate                      exec.Gate              resource truth only (doors below)
publish                   advise.Publish         state.json, by rename, expiry = one round
record                    advise.Ledger          epoch, status, attempts, window due, counters
window                    exec.Window            arm at the seal, due = seal + with-us − W
verify (rare)             loop.maybeVerify       one read-back per VerifyEvery, byte-budgeted
retire + sweep            loop.retireBelow       never the last, never the newest, bounded
```

Order matters in two places: settling before deciding (an in-flight write must be a fact, not a
guess), and the window being armed by the **seal** observed in this round, never by the request that
started the attempt.

## 3. The window (the only clock we own)

```
span   = with-us                         (0 → no window at all)
dueAt  = lastSeal + span − W             (W clamped to ≥ 0; if span < W the window is infeasible)
signal = now ≥ dueAt                     (the executor asks the policy for a write)
```

Rules:

1. **Armed by a seal, not by a request.** `internal/exec/window.go` has one arming point: an attempt
   that ended `sealed` or `broken`, the moment the newest copy changed. Arming at the attempt's start
   would make peak staleness `with-us + W`, and a request that is refused, held or abandoned must
   never extend the window.
2. **Refreshed by any write.** An exception write that seals re-arms the window. Refreshing is
   bookkeeping: it prevents billing the same window twice (it saves nothing, by the product's own
   definition).
3. **Infeasible is a configuration error, not a runtime mode.** `with-us < W` is refused at start
   (`window 30m is shorter than one write (43m) — this bound cannot hold`). The code never silently
   degrades to continuous writing.
4. **No window, no signal.** `with-us` unset → the window object is inert and `status` says
   `protecting: exceptions only · oldest safe point step 0 (2h14m ago)`.
5. **Cold start.** With no seal yet and a window set, the window is due at `open + with-us − W`, not
   immediately: the job has not run anything worth saving yet.
6. **Restart.** `dueAt` is derived from the ledger's `SealAt`, not from process memory, so a restart
   neither forgets nor doubles the window.

## 4. The gate (resource truth, and only that)

Doors, in this order, each with its own reason code:

| Door | Refuses because | Can be asked again | Resets a clock |
|---|---|---|---|
| `not-a-save` | the decision does not ask for a write | n/a | no |
| `in-flight` | a write is running (**hold**, not a refusal) | yes | no |
| `already-saved` | the work at this step is already on disk | yes | no |
| `no-time` | `T < W + margin` (an announcement that cannot fit) | yes | no |
| `headroom` | `free < (copies + retained) × size`, or the size is unknown | yes | no |
| `duplicate` | this request was already served | yes | no |
| `cooldown` | the last write ended too recently | yes | **it is the clock** |
| `backoff` | the same reason failed recently | yes | **it is the clock** |
| `volume-busy` | another unit is writing on this volume | yes | no |

`already-saved` before `no-time` is deliberate: work that is on disk needs no time to write. A
refusal is never a write: it resets no clock, grows no backoff, and leaves the cooldown alone.

## 5. The plan file (contract, schema 2)

Written by rename; read by the job and by `ckptctl`. **Expiry = last publish + one round** (it means
only "a live plan exists"; it is not a period).

Two acts wear the name "publish", and they have different limits:

- **A renewal** re-stamps the same plan. It is the heartbeat, it runs whenever the file is within
  half its life, and it is *not* rate-limited: a plan that expires between two decisions tells a
  healthy job that we are gone, and a job told that writes nothing (`no-us none`) or falls back to
  its own timer. `advise.Renew` is the function; the loop calls it when this round published nothing.
- **A change** replaces the plan the job may be reading right now. Only this one is capped
  (`MaxPublishPerMinute`), because only this one can flap.

The two were once one, and a 200 ms cadence starved the heartbeat: twelve renewals filled the
breaker's minute, the plan died, and the job sat silent for 103 s under a 60 s span. The test that
pins it is `TestThePlanNeverExpiresWhileTheControllerIsAlive`.

```json
{
  "schema": 2, "epoch": 12, "unitId": "team-a/train-llama-0", "generation": 3,
  "expiresAt": 1793523605,
  "action": "save", "reason": "risk",
  "reasons": [{"code": "risk.shutdown", "evidence": ["Evidence[risk.shutdown]"]}],
  "missing": ["blind:store.low-space"],
  "requestId": "3f9c…",
  "baselineSeconds": 600, "baselineNone": false, "windowSeconds": 1800,
  "copiesToWrite": 1, "keepCopies": 2, "kind": "full", "oldestStepToKeep": 0,
  "why": "risk.shutdown is 1: the platform announced a stop",
  "version": "risk-v1"
}
```

Rules that make it safe to act on:

- **No `requestId` → no request.** The job acts on a request at most once, keyed by this field.
  `TriggerAt` was always zero under risk-v1 and could not serve as a key; `requestId` replaces it.
- **`reasons[].values` is empty.** Numbers live in L1/L2; the display fetches them
  (`ckptctl explain` already does).
- **`missing` is not `absent`.** A blind condition is a risk; a condition the deployment does not
  have is not mentioned.
- **The plan carries both numbers**, so a job that has never met the controller can still behave.

## 6. The job's side (when the governor is gone)

At every save point the job does one of three things, in this order:

1. **No live plan** (absent, unreadable, `now > expiresAt`) → *we are gone*:
   - `baselineSeconds > 0` → the job's own timer: write if `now − lastWrite ≥ baseline − W`;
     refreshed by any write it makes. This is the whole fallback.
   - `baselineNone` → write nothing; print `governor absent; no-us=none: no writes
     this)`.
2. **Live plan, `action` does not ask for a write** → write nothing; the window is the controller's
   business, not the job's.
3. **Live plan, `action = save|repair`, `requestId` not yet acted on** → write and remember the id.
   `may-exit` is not the job's to write (see §7): it waits for it.

Two copies of `no-us` (the deployment's and the plan's) are reconciled, not duplicated:
`ckptctl doctor` prints both and complains when they differ. A job that has never seen a plan uses
its own local value — the only case where the local copy decides.

## 7. End of training, and the stop

`ending` (training finished) and `stopping` (SIGTERM) share one protocol, because both mean "the work
that exists now is the work that matters":

```
job:      ending / stopping          (presence file) — and it keeps running, it does not exit
engine:   one write requested        (the final save; `job.ending` is an urgent condition already)
engine:   may-exit                   (written by the controller, once it has observed that no write
                                      is in flight, or that the write's deadline passed)
engine:   "final write landed" / "final write NOT made: <reason>" in status and in the last line
```

**The controller writes `may-exit`, not the job** (decision 0017): it is the side that can observe
whether a write is still in flight, and asking the job to report what the controller can see is the
mistake this handshake exists to avoid. A job that never hears back exits at its own grace period, so
a dead governor cannot hang a finished training.

A final write that cannot be made (no space, no time, gate refusal) is a **failed final write**: it is
named, counted, and printed as the last line of the run.

## 8. What the report must hold (so the claims are printable)

| Fact | Where | Printed as |
|---|---|---|
| window: span, dueAt, last refresh, armed-by | ledger | `window 30m · next due 10:05 (in 12m)` |
| write kinds | ledger counters | `writes 19 (window 16, exception 2, final 1)` |
| staleness at request / at seal, per write | attempt record | `receipt: stale 29m58s → 0m, cost 92s` |
| last round, blind count, watch broken | ledger status | `presence: watching 14/14, 0 blind · nothing fired` |
| last refusal, per door | ledger status | `last gate: no-time (need 4m10s, have 30s)` |
| estimate + source | ledger status | `estimate 4m10s (measured p90)` |
| baseline vs actual | computed in `advise` at render | `no-us 10m → 48 · with-us 30m → 16 · saved 30 (62%)` |
| failure taxonomy | ledger status | `absent | blind | read-only | refused | abandoned | broken | final-write-failed | baseline-unset` |

Everything above is O(1) to read and bounded in size (the same rule as the attempt history: counters
are derived from bounded events, not accumulated forever).

## 9. What top-tier controllers do, and what we take from them

| Pattern | Where it comes from | What we adopt | What we reject, and why |
|---|---|---|---|
| Level-triggered reconcile; a missed event costs one resync | kube-controller-manager | the round already works this way; the queue is keyed and deduped | event ordering (we never depend on it) |
| Desired state vs observed state, with named conditions | kubelet (`NodeCondition`) | the window *is* our desired state, so "distance to promise" is printable (`due in 12m`); conditions carry `since` | a status object per condition × machine (too much for a directory) |
| Three layers: observation, threshold, enforcement | kubelet eviction manager | the catalogue's tier + missing policy keeps observation and consequence separate | soft/hard tiers and rationing: we have no budget to ration against |
| Graceful termination with a grace period | kubelet pod lifecycle | `stopping` → final write → `may-exit`, with the deadline fit test | extending the grace period (we cannot; we can only report the loss) |
| Idempotency by identity (uid, resourceVersion) | k8s API machinery | attempt identity, `requestId`, epoch, generation | storing a version in the job (the id is enough) |
| Separate observer binary with its own module | node-exporter, kube-state-metrics | `contrib/observer` (round 3) | putting client-go in the core |
| Gang semantics (`minAvailable`) | volcano | `members` + generation: a gang is one unit | scheduling the gang (not our business) |
| Deadman switch (hardware watchdogs) | plcwatch, systemd `RuntimeWatchdogSec` | the window: refreshed by evidence, not by a promise | periodic "I'm alive" pings (the evidence is the checkpoint itself) |

## 10. Edge cases, decided

| Situation | Behaviour |
|---|---|
| `baseline` not answered | `run` refuses at start, one line saying why (the fallback and the saving both need it) |
| `baseline = none`, `window` unset | pure exceptions; the hole is displayed (`oldest safe point …`) |
| `baseline < W` | warning: the death timer cannot finish a write in time; the job's interval is effectively `W + margin` |
| `window < W` | refuse at start: a bound shorter than one write cannot hold |
| `window < baseline` | allowed but reported: `you are stricter with us than without us; the saving is negative` |
| `window = 0` | invalid; use "unset" |
| no seal yet, window set | due at `open + window − W` |
| a write is in flight when the window is due | hold; the window re-arms from the seal when it lands |
| the newest copy is broken | repair now (the catalogue's `Copies` fact), not a window write |
| two writes failed in a row | `infeasible`, a person must look (named state) |
| lease lost | read-only: publish nothing, release volume slots, adopt when it returns |
| the controller restarts mid-write | the new epoch settles the attempt from the directory; the window derives from `SealAt` |

## 11. Acceptance: five printed lines (the exit criteria)

```
fallback      governor stopped 61s ago → job wrote at 10m04s on its own (baseline 10m)
exception     risk.shutdown 1 → request in 0s → sealed 92s (window 300s);  no-time: need 4m10s, have 30s — 12m at risk
presence      presence: watching 14/14, 0 blind · nothing fired · window due in 51m
final write   ending → final write requested → sealed 1m08s → may-exit (step 12000)
saving        19 writes (window 16, exception 2, final 1) · no-us 10m → 48 · saved 30 (62%)
```

Plus three failure lines that must print, not be imagined:

```
window infeasible: 30m < one write (43m) — refusing to start
final write NOT made: headroom (need 2.0x, have 1.2x) — the last state is not on disk
governor absent; no-us none: no writes — oldest safe point step 0 (2h14m)
```

## 12. Sequence and what each item must print

All six items are built. Each row names the files it touched (`internal/exec`, `internal/loop`,
`internal/queue`, `internal/supervisor`, `internal/policy`, `internal/observe`, and the delivery
layer's files) and the line its test prints.

| Item | Files | Acceptance line |
|---|---|---|
| window replaces fallback: armed at the seal, `due = seal + window − W`, refreshed by any write, infeasible refused | `internal/exec/window.go`, `internal/loop` | `window 30m · exception sealed 09:05 → next due 10:05` |
| fallback: `baseline` published, the job times itself when the plan is expired | `internal/advise`, `cmd/ckpttrainer`, `docs/glue.md` (coordinated) | the fallback line above |
| dead fields, `values`, superseded decision fields | `internal/policy`, `internal/assemble` | `go build` + the contract-field test |
| `requestId` replaces `TriggerAt` | `internal/advise`, `loop`, `cmd/*` | `save once per requestId: 1 write for 3 rounds of the same plan` |
| the report and the render: window line, kinds, receipt, baseline comparison | `internal/advise`, `cmd/ckptctl` | the presence and saving lines |
| final-write protocol | `internal/loop`, `internal/produce`, `cmd/ckpttrainer` | the final-write line + the failed-final-write line |

The fallback is the item that decides whether the product's central sentence is true; it was built
as one coordinated change with the delivery layer.

## 13. What this design deleted (the reasoning is in `design-backstop.md` §4)

the old three-state `Fallback` timer, armed at the request, and its `none`/`unset` error states ·
`Bounds.MaxLostWork`/`MaxLostSteps`/`MaxTax`/`ExpectedEnd`/`Guarantee`/`OnConflict`/`MemoryBudgetBytes` ·
fifteen `Snapshot` fields the risk core never reads · `ReasonEntry.Values` numbers · `Decision.Interval`,
`.TriggerAt`, `.NextCheck`, `.Tax`, `.FallbackInterval`, and the unused `Kind`/`WeightsOnly`/`Shards`/
`Async`/`Buffers` · the `2 × baseline` expiry arithmetic · `status --bound`, `run --lost/--tax`.

## 14. Open questions (three, and only three)

1. **`baseline` required?** This design refuses to start without it. The alternative is to start, print
   `baseline unset: the saving and the fallback are unknown`, and nag. (Refusing is one line of
   friction at setup; starting is a silent zero in every report.)
2. **`window` unset is legal** (pure exceptions) and the hole is displayed — agreed?
3. **The job's local default for `baseline`**: one value in `conditions.json` read by both sides, with
   `doctor` complaining on mismatch, or the plan's value always wins after the first plan? (This
   design: plan wins after the first plan; the local copy decides only before the first plan.)

## Appendix A — the saving, and the hole

This appendix is what was `docs/design-backstop.md`. It restates the frozen rule and says why the
product exists; §1–§14 above say how it is built.

### A.1 The saving, in one paragraph

Without the governor a user writes every 10 minutes because he is afraid of what he cannot see. With
it he configures 30 minutes, because the exceptions are covered. The saving is the difference between
those two periods: 3× fewer scheduled writes for the same protection. Everything else follows:

- The window's refresh rule saves nothing. It only refuses to charge the user twice for a window that
  an exception write has already served.
- Configuration convenience is real value, but it is not the saving: it is why the longer period is
  reachable without trusting a guess.
- The report is real value: it is how the user checks that the longer period is safe.

### A.2 The two numbers, two questions

| | `no-us` (required answer) | `with-us` (optional) |
|---|---|---|
| the question | "without the governor, how often would you have written?" | "with the governor, how long may a checkpoint be stale?" |
| while we run | nobody uses it | **the executor** keeps the window |
| when we die | **the job's own timer** | nobody |
| in the report | **the baseline: `run / no-us`** | the comparison: `run / with-us` |
| valid answers | a duration, or `none` (you would not have written at all) | a duration, or unset (no time-based write) |

The window is a deadman, and its refresh is bookkeeping. It is measured seal to seal, and any write
restarts it:

```
written at 09:00 (exception, sealed 09:05) → the window is served; next due 10:05
written at 10:05 (window,   sealed 10:10) → next due 11:10
written at 10:40 (exception, sealed 10:45) → next due 11:45
```

The middle line is *not* a saving: had the exception not happened, the window write would have
happened anyway. The refresh only stops the window from billing twice. The window bounds the staleness
of the newest **sealed** copy, so the request goes out one write-time early (`due − W`); if
`W ≥ with-us` the window is infeasible and is reported as such. `no-us` is asked once, and the CLI
states which answer is in force on the first line of every run.

### A.3 The saving, as arithmetic

```
no-us = 10m   → 8h/10m  = 48 scheduled writes
with-us = 30m → 8h/30m  = 16 scheduled writes
exceptions in that run: 2   (each one eats a window slot it would have paid anyway)
actual: 16 + 2 − 2 = 16, plus the final write (excluded)
saved: 32 writes; time: 32 × W

writes 18 (16 window + 2 exception + final excluded)
no-us 10m → 48 scheduled · with-us 30m → 16 scheduled · saved 32 (66%), 32 × write time
window refreshed by an exception 2 times (not a saving; the window saved twice)
```

The arithmetic has one baseline: **the number the user stated as his own habit.** It is not a claim
about what he would have chosen — he chose it. If `no-us` is `none`, the baseline is zero writes and
the saving is zero, and the report says the value there is the coverage, not the count.

### A.4 The hole, and where it lives

With `with-us` **unset**, while we are alive and every condition reads false, nothing is written: an
environment failure that produces no boolean loses everything since the last exception. That is the
explicit trade, and it is displayed (`protecting: exceptions only · oldest safe point: step 0 (2h14m)`).
With `with-us` set, the hole is bounded by that number — which is the only reason to set it.

### A.5 What we never add

1. No controller-invented period, in any disguise (no `auto`, no `k × write time`).
2. No second role for the algorithm: it stays boolean, clock-free, arithmetic-free.
3. No severity or probability in the plan; no prediction of the platform.
4. No third number.
5. No silent absence: "governor gone", "blind", "read-only", "final write failed", "baseline unset"
   are named states.
