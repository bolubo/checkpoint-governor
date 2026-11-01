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
end-of-run receipt; `--json`; the compose demo.

**Not ship:** CRD or operator; multi-cluster; dashboard; resume logic; auto-applied recommendations;
durability promises not measured.

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
`docs/decisions/0018-first-mile.md`.
