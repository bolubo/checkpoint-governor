# Product v1 — the surface

Status: design. Date: 2026-11-01. Companions: `README.md`, `docs/decisions/0017-two-numbers.md`,
`docs/experiments-matrix.md`.

The decision engine is frozen. This page specifies the surface a user meets: what is asked, what is
shown, and what is not claimed.

## 1. Who this is for

Checkpointing is insurance. It costs time on every write and returns nothing until it returns
everything, so most people buy the cheapest policy available: a number from a blog post or from an
assistant that has never seen their job.

| | **never configured it** | **configured it carefully** |
|---|---|---|
| Today | nothing, or a number they cannot defend | every N minutes, from experience |
| Would pay for | not having to think about it | fewer writes for the same protection |
| Size | most of the market | a small, well-served minority |

v1 targets the first group. The second is a later conversation: they already have a number, and we
can beat it on cost.

## 2. The promise

> With no configuration, we write what no configuration writes — one checkpoint at the end — and one
> more if the platform announces that the job is about to go.

The claim is tested in §9.

## 3. Rules of the surface

1. The default equals "no configuration": no periodic writes, one at the end, plus the emergency save.
2. We never invent a period. We may compute what a window costs and propose it; the user applies it.
3. A number is never shown without its price.
4. `skip` is printed as often as `save`. A tool that reports only its successes cannot be trusted
   about its refusals.
5. Non-interactive is the first-class path: CI cannot answer questions.
6. State facts. No blame, no reassurance, no adjectives.

## 4. The two numbers

Unchanged from 0017; repeated because the surface must speak it.

| Input | Meaning | Required |
|---|---|---|
| `no-us` | what the job would have done without us: `none`, or a period | no — v1 default `none` |
| `with-us` | how stale a checkpoint may get while we are alive | no — unset means exceptions only |

While we run, a write has two causes: a condition is `1`, or the window is closing. Training end is
written once, always.

## 5. First sixty seconds

```
$ docker run -v /ckpt:/ckpt ghcr.io/…/governor --dir /ckpt
governor  watching /ckpt · no-us=none · with-us unset
governor  nothing configured: one write at the end, and one if the platform announces a stop.
governor  a 30-minute window on this job costs about 8 writes (16s). `governor init` to choose.
```

Then one line per decision (§6.1), silence in between, and a receipt at the end (§6.3).

`governor init` is optional and asks one question:

```
How long will this run take?  [4h]        enter to skip

  nothing              0 writes   same as today, plus the emergency save
  one at the middle    1 write    a silent death costs at most half the run
  every 30 minutes     8 writes   a silent death costs at most 30 minutes
```

## 6. What is shown

### 6.1 One line per decision

```
14:32:10  save  window        with-us=30m closed                1.2s
14:41:03  save  risk.preempt  announced by the platform         1.3s
14:42:11  skip  no-time       a write needs 40s, 12s left
```

### 6.2 Running tally

```
this run: 8 writes · 9.6s · 0.07% of training · 7 on the window, 1 on a preemption
```

### 6.3 End of run

```
run 3h58m · 8 writes · 9.6s (0.07%)
  7 on the 30-minute window
  1 on a platform preemption at 14:41
  a 10-minute habit would have written 24 times (29s)
durability      local disk: a copy outlives the process, not the volume
  a silent death costs at most 30 minutes
```

### 6.4 Status

`governor status` (and `--json`): current risk level with named conditions, the environment (node
pressure, pending higher-priority work, cordon, free space against one checkpoint, measured write
cost), the last decision and its reason, and what a window would cost given the run so far.

## 7. Discovery and pricing

The engine reads and prices. It does not decide for the user.

| Signal | Cold start | After the first write |
|---|---|---|
| the job's period | unknown | spacing of sealed copies |
| one checkpoint's size | `expected-bytes` if declared | measured |
| write cost | throughput probe on the volume | p90 of real writes |
| platform risk | declared capabilities | same |

With `D` the run duration and `w` the write cost:

| Option | Setting | Writes | Bound |
|---|---|---|---|
| nothing | `with-us` unset | 0 | everything |
| one at the middle | `with-us = D/2` | 1 | `D/2` |
| periodic | `with-us = 30m` (or `D/8` for short runs) | `⌊D / with-us⌋` | `with-us` |

Price: `writes × w`, in seconds and as a share of `D`.

Not done: applying a recommendation without a yes; quoting a price from a number neither measured nor
declared; recommending anything for runs short enough that the loss does not matter.

## 8. Bounds

| Event | Loss |
|---|---|
| silent death of the machine | ≤ `with-us`, or everything if unset |
| announced death (preemption, eviction, drain, stop) | detection plus one write. Detection is the observer's interval (1s in `F1`) plus the probe's poll (5s by default): an announcement is seen within that, not instantly |
| our death | the job falls back to `no-us`, or to nothing if `none` |

## 9. Claims and their tests

| Claim | Test |
|---|---|
| zero config writes what no config writes | the calm run: every round `skip`, one write at the end |
| the emergency save fires on an announcement | the preemption case, on the host and in the cluster |
| a declared channel that is silent is not a calm platform | the declared-blind case: a save, and the line says `cannot read` |
| at equal cadence we cost no more than a timer | demo act 1 |
| a longer window costs proportionally less | demo act 1 |
| a stop leaves promptly with the same final write | `the stop case` (5.3s against 21.0s, both `intact=1`) |
| a full disk is refused, not filled | the full-disk case (6.0MB headroom against 0KiB) |

The first two run in `make exp`; the cluster cases run in `experiments/rig/cluster/f.sh`.

## 10. Metrics

| Metric | Today for A | v1 target |
|---|---|---|
| runs ending with one checkpoint | ~0% | ~100% |
| install to running | unknown | < 60s |
| install to a chosen number | never happens | < 20s, one question |
| writes in a calm zero-config run | 1 | 1 |
| users who can say why the last write happened | none | anyone who reads one line |

## 11. Scope

**Ship:** one container; zero-config start; `init` with the price sheet; line per decision; `status`;
end-of-run receipt; `--json`; the compose demo; `--webhook`; `serve --metrics`.

**Not ship:** CRD or operator; a fleet-wide merged view (one process reads one root, or several named
roots); dashboard; resume logic; auto-applied recommendations; durability promises not measured.

## 12. Work order

| # | Work | Where | Done when |
|---|---|---|---|
| 1 ✅ | `--no-us` optional, default `none`; the run states its configuration | `cmd/ckptctl/run.go` | a run with no flags starts and prints `no-us none (unset …)` |
| 2 ✅ | one line per decision | `internal/loop`, `cmd/ckptctl/run.go` | `save` and `skip` lines carry the time, the condition and its sentence; rounds that only observed print nothing; the counters moved behind `--verbose` |
| 3 ✅ | one write is priced at start-up, with its source named | `internal/loop/glue.go`, `cmd/ckptctl/run.go` | a cold directory with `expected-bytes` prints `one write: 1.2s (2.0 GB at 1.7 GB/s)`; without it, `unknown` |
| 4 ✅ | `ckptctl init`: one question, three prices | `cmd/ckptctl/init.go`, `internal/advise/numbers.go` | it prints three priced options and writes `.ckpt-governor/numbers.json` (or nothing, for `none`); `run` picks the answer up when no flag is given |
| 5 ✅ | end-of-run receipt | `internal/loop` (`Receipt`), `cmd/ckptctl/run.go` | printed on stop; it never says "no writes" while checkpoints are on disk, and it always states the window's bound |
| 6 ✅ | the demo, three acts | `experiments/demo/demo.sh` | `make demo` runs in ~15s: no config (no writes), an announcement (one write, named), a window (one write, priced), then the three receipts |

Each step lands with its test; nothing lands on a promise that is not measured.

## 13. Decided

1. **The default recommendation is one write at the middle** (`with-us = D/2`), not a periodic window.
   For someone who has never written, one write for half the run is worth more than eight for thirty
   minutes, and it is the smallest thing that makes the emergency save meaningful.
2. **A run that passes its stated duration is not re-quoted.** The tax stays proportional to duration,
   so nothing needs to change; a prompt at hour 8 would be a notification about arithmetic.
3. **`init` writes nothing when the answer is "skip".** The configuration file stays absent, the run
   states `no-us none (unset)`, and "zero config" keeps meaning what it says.

4. **The receipt always states the bound**, including when `with-us` is unset:

   ```
   run 3s · no writes
     nothing said the work was at risk
     a silent death costs everything: no window was set
   ```

   The line is not conditional. It is the one sentence that answers "what does losing this machine
   cost me", and it matters most in the case where nobody thought about the question.

These four, with the reasoning and the rejected alternatives, are
`docs/decisions/history.md#0018`.

## 14. The second user: the number they already have

The zero-config user (A) never configured anything; the product is a default and a receipt. The
configured user (B) **already has a number and needs it held and proved, not replaced**. One engine,
two surfaces: `init` and the run's lines are A's; the arithmetic above the controller
(`internal/bound`, `ckptctl price`) and the after-the-fact certificate (`ckptctl audit`) are B's.

Two measured facts make B's case, before any claim about being cleverer:

```
spread of best fixed periods across jobs: 12.0x
tiny / small-team-latency / small-team-shards   best fixed  5m
small-team / delta / delta-storm                best fixed 10m
steady / drift / storm / capacity / killed      best fixed 15m
big                                             best fixed  1h
```

One fleet-wide number is either 12× too expensive for the big jobs or 12× too lossy for the small
ones. And one equation, exact because `W` is the measured p90 of sealed writes (§16):

```
writes = run / bound                 tax = writes · W / run = W / bound
```

The tax does not depend on the run, which is why it is a number a person can carry between jobs.

| saving | size | prove it with |
|---|---|---|
| per-job bound instead of one fleet number | up to 12× (above) | `ckptctl price` per directory; the table's spread line |
| no double billing: an exception write serves the window | up to the exception count | `Kinds` split (window vs exception) in the run's receipt |
| the write-time subtraction (`W`) at an equal bound | `W/(P+W)`, ~5% at 10m/30s | `docs/experiments-matrix.md` layer D; the price sheet |
| what is *not* a saving | the refresh alone | §15: an exception write would have happened anyway |

Both questions B asks are exact: **"what does holding 15 minutes cost?"** is `tax = W/15m` (3.3% at
a 30-second write, 16 writes in 4h); **"we may spend at most 1% on writes — how often may we save?"**
is `bound ≥ W/T` (1% allows no tighter than 50 minutes with a 30-second write). The left edge is one
write: a bound shorter than `W` cannot hold, the executor refuses it, and the price sheet prints
`infeasible` instead of a price.

**The number is measured from their own history.** `internal/bound/observed.go` reads a directory
and reports the habit (median seal-to-seal gap), the worst staleness the job has actually lived with,
and the measured write cost. Unsealed copies, copies without a known time, and two seals inside one
second are excluded: a period invented from a burst is the failure this page exists to avoid. So the
first question to B is not "what bound do you want" but "what has your job been living with", read
from their disk.

**The deliverable that is not a feature: a certificate.** For a compliance bound a team cannot answer
an auditor with a hope, so `ckptctl audit --dir=<dir> [--bound=15m]` computes one after the fact,
read-only: max observed staleness, over-bound seconds, the writes with their causes, and the deletions
below `keepCopies`. `over-bound > 0s` exits non-zero, so the claim is tested rather than asserted.
`run --once --dry-run` is the plan/apply split: it prints what it would publish and retire and leaves
the directory byte-for-byte unchanged; the preview and the act share one selection function
(`loop.retirableBelow`), so the preview cannot drift from the act.

When this design is wrong: their number is already at the optimum (`P ≈ L`) and the saving is `W/L`,
about 5% — then the value is the certificate, not the count; exceptions dominate (the `killed` and
`delta` families) and the window saves nothing; or `W` is unknown, and the sheet must print `unknown`
and propose nothing.

## 15. Why it did not write

A controller that says nothing is indistinguishable from one that is broken. `ckptctl explain
--dir=<dir>` reads the plan the job was given, not a log, so the answer is exactly what the job saw:

```
decision   skip (calm)
why        no condition says the work is at risk
write      full, keep 1
```

A save request has to pass every door below. The first that refuses is the answer, and the reason
names it:

| Door | What it means | What to do |
|---|---|---|
| `not-a-save` | the decision is `skip` or `hold` | read `why`: calm, or waiting for the job |
| `in-flight` | a write from this unit is still running | wait; a second write would protect nothing |
| `headroom` | less free space than the next write needs | free space, or lower `freeSpaceFactor` deliberately |
| `duplicate` | the same trigger at the same step was already attempted | nothing: the attempt is recorded once |
| `cooldown` | too soon after the previous attempt | nothing: this is the cost of writing |
| `backoff` | the last attempts failed | look at the attempts, not the timer |
| `volume-busy` | another unit on the same volume is writing | wait: one write per volume at a time |
| `no-time` | the announced deadline is shorter than one write | accept the loss, or make the write cheaper |

A refusal is not a debt: the controller does not "make up" a write later because it skipped one
earlier. Writing because you owe a write is cost without a reason. The protection against near
misses is the job's own timer, which keeps running whether the controller is alive or not.

## 16. How long one write takes

Every decision that involves a deadline needs this number, and arithmetic on a guess is worse than
saying "I do not know". It comes from, in order of authority:

1. **Measured** — after two sealed writes, the p90 of the durations actually seen. The only number
   used for a deadline.
2. **Declared by the job** — `expected-bytes` in `.ckpt-governor/`; printed as `(estimated)`.
3. **One bounded probe** — when the job declared `expected-bytes` and no measured or declared duration
   exists: write 32 MiB into the checkpoint directory, fsync, remove, measure. The probe belongs to a
   `run`, never to a read-only command like `price` — without a size there is nothing to price. Once per
   process, counted as overhead.
4. **Nothing** — and then the controller says so. It does not invent a duration to look decisive.

Only a **finished** write has a cost: `sealed` and `broken` carry one; `failed`, `abandoned` and
`refused` carry none, because they measured the failure, not the work. Estimates never enter
`measured`: mixing them would let a guess become the p90 by repetition. A stop is a fit test — the
job reports `seconds-left`, the executor compares it with the write it is about to ask for, and
`no-time` is a reported loss rather than a write that will be killed halfway.

```
ckptctl glue --dir=/path/to/checkpoint
glue: expected=1.2GiB stopRequested=1 mayExit=1 seconds-left=27
```

## 17. Live numbers

The product's own accounting is `ckptctl stats --dir` (writes and their costs, failures it survived,
plans the ledger never recorded, checkpoints read back) — it reads files and needs no process, because
the most common time to ask is after the controller is gone. `ckptctl serve --addr=<addr>
--members=<dir,...>` is the live counterpart: a read-only Prometheus endpoint (`checkpoint_staleness_seconds`,
`checkpoint_over_bound_seconds`, `checkpoint_blind_conditions`, `checkpoint_writes_total{kind}`), one
walk per scrape, no cache, no lease, no decision. `--webhook` posts one JSON event per changed plan so
a platform hears about it without polling. Neither of them tracks training progress.
