# Four policies, one job: the number that matters

This page is the article form of `make contrast`: one command, four rows, and the only number that
decides whether a backstop is worth installing — **writes per job**.

```
$ make contrast
a 4h job, 14400 steps, one write costs 40s — a model, not a measurement

policy                         writes         io  worst case, no signal
500 steps (framework default)       28      1120s  ≤500 steps
nothing installed                   0         0s  everything
governor, unconfigured              1        40s  everything
2h bound                            2        80s  ≤2h
```

## What each row is

| row | policy | writes | what a silent death costs |
|---|---|---|---|
| 500 steps (framework default) | a timer, the habit almost every job has | 28 (19.5% of the run writing) | up to 500 steps |
| nothing installed | no checkpointing | 0 | everything |
| governor, unconfigured | the product's default: one write at the end, one on an announced stop | 1 | everything |
| 2h bound | the user set a bound of two hours | 2 | up to 2h |

The unconfigured row is the product's whole default: **it does not write more, it writes at the end
and on an announced stop.** The bound row is what a user buys when they answer one question.

## The exclusions, stated

- **A model, not a measurement.** Steps, write cost and the four-hour duration are inputs to the
  contrast program (`experiments/contrast/`), not readings from a cluster. The real write cost is
  measured by `ckptctl price` on your own directory.
- **This job has no signals.** A stop that announces itself (`SIGTERM`, an eviction, a drain, a full
  volume, a memory limit) is `risk.shutdown` and the executor covers it one probe interval plus one
  round later. That budget belongs in `docs/conditions.md`; this table does not model it.
- **No money.** The columns are writes, seconds of I/O, and steps at risk. A GPU-second price is the
  reader's own arithmetic on their own hardware.
- **The framework default is a timer.** It is not a straw man: `t mod period = 0` is the definition of
  the baseline's decision, and it is why the comparison is a scheduling comparison rather than a
  throughput one.

## Reproduce it

```
git clone https://github.com/bolubo/checkpoint-governor
cd checkpoint-governor
make contrast
```

The rows come from `internal/contrast`, and `TestEveryRowNamesItsCoverageAndItsWrites` holds each row
to its coverage and its write count. Paste the command and you should get the same four rows.
